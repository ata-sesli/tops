package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	anyllm "github.com/mozilla-ai/any-llm-go"
	anyanthropic "github.com/mozilla-ai/any-llm-go/providers/anthropic"
	anygemini "github.com/mozilla-ai/any-llm-go/providers/gemini"
	anyopenai "github.com/mozilla-ai/any-llm-go/providers/openai"

	"tops/internal/config"
	"tops/internal/obs"
	"tops/internal/ops/benchmetrics"
	"tops/internal/storage/modelprofile"
)

type CompletionRequest struct {
	SystemPrompt    string
	UserPrompt      string
	Temperature     float64
	MaxTokens       int
	Think           *bool
	SamplingProfile string
}

type CompletionResponse struct {
	Content string
	Raw     string
}

type ChatMessage struct {
	Role       string
	Content    string
	Name       string
	ToolCallID string
	ToolCalls  []ToolCall
}

type ToolProperty struct {
	Type        string
	Description string
	Enum        []string
	Items       *ToolProperty
}

type ToolDefinition struct {
	Name        string
	Description string
	Properties  map[string]ToolProperty
	Required    []string
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments map[string]any
}

type ToolChatRequest struct {
	SystemPrompt    string
	Messages        []ChatMessage
	Tools           []ToolDefinition
	Temperature     float64
	MaxTokens       int
	Stream          bool
	Think           *bool
	SamplingProfile string
}

const (
	SamplingProfilePlanner = "planner"
	SamplingProfileGen     = "gen"
	SamplingProfileAsk     = "ask"
	SamplingProfileHelp    = "help"
	SamplingProfileSample  = "sample"
)

func NormalizeSamplingProfile(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case SamplingProfilePlanner:
		return SamplingProfilePlanner
	case SamplingProfileGen:
		return SamplingProfileGen
	case SamplingProfileAsk:
		return SamplingProfileAsk
	case SamplingProfileHelp:
		return SamplingProfileHelp
	case SamplingProfileSample, "default":
		return SamplingProfileSample
	default:
		return ""
	}
}

type ToolChatResponse struct {
	Content   string
	ToolCalls []ToolCall
	Raw       string
}

type LLMProvider interface {
	Name() string
	Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
}

type StreamingProvider interface {
	CompleteStream(ctx context.Context, req CompletionRequest, onThinking func(string), onResponse func(string)) (CompletionResponse, error)
}

type NativeToolCallingProvider interface {
	ToolChat(ctx context.Context, req ToolChatRequest, onThinking func(string), onResponse func(string)) (ToolChatResponse, error)
}

type LocalModelLifecycle interface {
	Unload(ctx context.Context) error
}

type YZMAContextParamsSnapshot struct {
	NCtx          uint32 `json:"n_ctx,omitempty"`
	NBatch        uint32 `json:"n_batch,omitempty"`
	NUbatch       uint32 `json:"n_ubatch,omitempty"`
	NSeqMax       uint32 `json:"n_seq_max,omitempty"`
	NThreads      int32  `json:"n_threads,omitempty"`
	NThreadsBatch int32  `json:"n_threads_batch,omitempty"`
	NGPULayers    int32  `json:"n_gpu_layers,omitempty"`
	OffloadKQV    bool   `json:"offload_kqv,omitempty"`
	OpOffload     bool   `json:"op_offload,omitempty"`
}

type ProbeContextSnapshot struct {
	ModelPath              string                    `json:"model_path,omitempty"`
	LibPath                string                    `json:"lib_path,omitempty"`
	YZMALibEnv             string                    `json:"yzma_lib_env,omitempty"`
	CPUFallback            bool                      `json:"cpu_fallback"`
	DebugLog               bool                      `json:"debug_log"`
	DebugRaw               bool                      `json:"debug_raw"`
	ProcessPID             int                       `json:"process_pid,omitempty"`
	ProcessPPID            int                       `json:"process_ppid,omitempty"`
	ProcessIsTTY           bool                      `json:"process_is_tty"`
	ProcessGOOS            string                    `json:"process_goos,omitempty"`
	ProcessGOARCH          string                    `json:"process_goarch,omitempty"`
	TERM                   string                    `json:"term,omitempty"`
	SSHConnection          string                    `json:"ssh_connection,omitempty"`
	TMUX                   string                    `json:"tmux,omitempty"`
	CI                     string                    `json:"ci,omitempty"`
	Uname                  string                    `json:"uname,omitempty"`
	CPUBrand               string                    `json:"cpu_brand,omitempty"`
	RosettaTranslated      string                    `json:"rosetta_translated,omitempty"`
	ProviderInstanceID     string                    `json:"provider_instance_id,omitempty"`
	EnsureLoadCalls        int                       `json:"ensure_load_calls,omitempty"`
	EnsureLoadReused       bool                      `json:"ensure_load_reused,omitempty"`
	LoadCount              int                       `json:"load_count,omitempty"`
	UnloadCount            int                       `json:"unload_count,omitempty"`
	LifecycleState         string                    `json:"lifecycle_state,omitempty"`
	LastCallType           string                    `json:"last_call_type,omitempty"`
	LastSamplingProfile    string                    `json:"last_sampling_profile,omitempty"`
	LastMaxTokens          int                       `json:"last_max_tokens,omitempty"`
	ResolvedLlamaDylibPath string                    `json:"resolved_llama_dylib_path,omitempty"`
	BackendCount           int                       `json:"backend_count,omitempty"`
	BackendNames           []string                  `json:"backend_names,omitempty"`
	DeviceCount            int                       `json:"device_count,omitempty"`
	DeviceNames            []string                  `json:"device_names,omitempty"`
	GPUDeviceName          string                    `json:"gpu_device_name,omitempty"`
	GPUDeviceMemoryFree    uint64                    `json:"gpu_device_memory_free,omitempty"`
	GPUDeviceMemoryTotal   uint64                    `json:"gpu_device_memory_total,omitempty"`
	ContextParams          YZMAContextParamsSnapshot `json:"context_params,omitempty"`
}

type ProbeContextProvider interface {
	ProbeContext() ProbeContextSnapshot
}

type ProviderOptions struct {
	ModelProfile modelprofile.ModelProfile
}

const hostedRequestTimeout = 45 * time.Second

func NewFromConfig(cfg config.Config, logger *obs.Logger, opts ...ProviderOptions) (LLMProvider, error) {
	options := ProviderOptions{}
	if len(opts) > 0 {
		options = opts[0]
	}
	switch cfg.Provider.Type {
	case config.ProviderOpenAI:
		return newHostedProvider(cfg, logger, config.ProviderOpenAI)
	case config.ProviderAnthropic:
		return newHostedProvider(cfg, logger, config.ProviderAnthropic)
	case config.ProviderGemini:
		if strings.TrimSpace(cfg.Provider.Endpoint) != "" {
			return newLegacyGeminiProvider(cfg, logger)
		}
		return newHostedProvider(cfg, logger, config.ProviderGemini)
	case config.ProviderYZMA, config.ProviderOllama, config.ProviderLocal:
		return newYZMAProvider(cfg, logger, options.ModelProfile)
	default:
		return nil, fmt.Errorf("unsupported provider type %q", cfg.Provider.Type)
	}
}

type hostedProvider struct {
	name     string
	model    string
	provider anyllm.Provider
	logger   *obs.Logger
}

func newHostedProvider(cfg config.Config, logger *obs.Logger, providerType config.ProviderType) (LLMProvider, error) {
	key := strings.TrimSpace(os.Getenv(cfg.Provider.APIKeyEnv))
	if key == "" {
		return nil, fmt.Errorf("environment variable %s is not set", cfg.Provider.APIKeyEnv)
	}
	opts := []anyllm.Option{
		anyllm.WithAPIKey(key),
		anyllm.WithTimeout(hostedRequestTimeout),
	}
	if endpoint := strings.TrimSpace(cfg.Provider.Endpoint); endpoint != "" {
		baseURL, err := normalizeHostedBaseURL(providerType, endpoint)
		if err != nil {
			return nil, err
		}
		opts = append(opts, anyllm.WithBaseURL(baseURL))
	}

	var (
		provider anyllm.Provider
		err      error
		name     string
	)
	switch providerType {
	case config.ProviderOpenAI:
		name = "openai"
		provider, err = anyopenai.New(opts...)
	case config.ProviderAnthropic:
		name = "anthropic"
		provider, err = anyanthropic.New(opts...)
	case config.ProviderGemini:
		name = "gemini"
		provider, err = anygemini.New(opts...)
	default:
		return nil, fmt.Errorf("unsupported hosted provider %q", providerType)
	}
	if err != nil {
		return nil, fmt.Errorf("initialize %s provider: %w", providerType, err)
	}
	return &hostedProvider{
		name:     name,
		model:    cfg.Provider.Model,
		provider: provider,
		logger:   logger,
	}, nil
}

func (p *hostedProvider) Name() string {
	return p.name
}

func (p *hostedProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	started := time.Now()
	promptTokens := 0
	completionTokens := 0
	defer func() {
		benchmetrics.RecordLLMCallWithUsage(ctx, time.Since(started), promptTokens, completionTokens)
	}()

	params := anyllm.CompletionParams{
		Model: p.model,
		Messages: []anyllm.Message{
			{Role: anyllm.RoleSystem, Content: req.SystemPrompt},
			{Role: anyllm.RoleUser, Content: req.UserPrompt},
		},
	}
	if req.Temperature >= 0 {
		params.Temperature = float64Ptr(req.Temperature)
	}
	if req.MaxTokens > 0 {
		params.MaxTokens = intPtr(req.MaxTokens)
	}

	if p.logger != nil && p.logger.Enabled() {
		p.logger.Printf("provider=%s model=%s prompt_chars=%d", p.name, p.model, len(req.UserPrompt))
	}
	resp, err := p.provider.Completion(ctx, params)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("provider request failed: %w", err)
	}
	if resp != nil && resp.Usage != nil {
		promptTokens = resp.Usage.PromptTokens
		completionTokens = resp.Usage.CompletionTokens
	}
	content, err := completionContent(resp)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("parse provider response: %w", err)
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("marshal provider response: %w", err)
	}
	return CompletionResponse{
		Content: content,
		Raw:     string(raw),
	}, nil
}

func (p *hostedProvider) CompleteStream(ctx context.Context, req CompletionRequest, onThinking func(string), onResponse func(string)) (CompletionResponse, error) {
	resp, err := p.Complete(ctx, req)
	if err != nil {
		return CompletionResponse{}, err
	}
	if onResponse != nil && strings.TrimSpace(resp.Content) != "" {
		onResponse(resp.Content)
	}
	return resp, nil
}

func completionContent(resp *anyllm.ChatCompletion) (string, error) {
	if resp == nil || len(resp.Choices) == 0 {
		return "", fmt.Errorf("no choices returned")
	}
	message := resp.Choices[0].Message
	switch content := message.Content.(type) {
	case string:
		if strings.TrimSpace(content) == "" {
			return "", fmt.Errorf("no message content returned")
		}
		return content, nil
	case []anyllm.ContentPart:
		var b strings.Builder
		for _, part := range content {
			if part.Type == "text" {
				b.WriteString(part.Text)
			}
		}
		out := strings.TrimSpace(b.String())
		if out == "" {
			return "", fmt.Errorf("no message content returned")
		}
		return out, nil
	case []any:
		var b strings.Builder
		for _, item := range content {
			asMap, ok := item.(map[string]any)
			if !ok {
				continue
			}
			partType, _ := asMap["type"].(string)
			if partType != "text" {
				continue
			}
			text, _ := asMap["text"].(string)
			b.WriteString(text)
		}
		out := strings.TrimSpace(b.String())
		if out == "" {
			return "", fmt.Errorf("no message content returned")
		}
		return out, nil
	default:
		return "", fmt.Errorf("unsupported completion content type %T", message.Content)
	}
}

func normalizeHostedBaseURL(providerType config.ProviderType, endpoint string) (string, error) {
	base := strings.TrimSpace(endpoint)
	if base == "" {
		return "", fmt.Errorf("empty endpoint")
	}
	base = strings.TrimSuffix(base, "/")
	switch providerType {
	case config.ProviderOpenAI:
		base = strings.TrimSuffix(base, "/chat/completions")
	case config.ProviderAnthropic:
		base = strings.TrimSuffix(base, "/messages")
	case config.ProviderGemini:
		return "", fmt.Errorf("gemini endpoint override is not supported with any-llm-go in this path")
	}
	return base, nil
}

type httpProvider struct {
	name      string
	model     string
	endpoint  string
	http      *http.Client
	buildBody func(model string, req CompletionRequest) (any, error)
	extract   func(body []byte) (string, error)
	logger    *obs.Logger
}

func (p *httpProvider) Name() string {
	return p.name
}

func (p *httpProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	started := time.Now()
	defer func() {
		benchmetrics.RecordLLMCall(ctx, time.Since(started))
	}()

	payload, err := p.buildBody(p.model, req)
	if err != nil {
		return CompletionResponse{}, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("marshal provider request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("build provider request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	res, err := p.http.Do(httpReq)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("provider request failed: %w", err)
	}
	defer res.Body.Close()
	respBody, err := io.ReadAll(res.Body)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("read provider response: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return CompletionResponse{}, fmt.Errorf("provider returned HTTP %d: %s", res.StatusCode, string(respBody))
	}
	content, err := p.extract(respBody)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("parse provider response: %w", err)
	}
	return CompletionResponse{Content: content, Raw: string(respBody)}, nil
}

func (p *httpProvider) CompleteStream(ctx context.Context, req CompletionRequest, onThinking func(string), onResponse func(string)) (CompletionResponse, error) {
	resp, err := p.Complete(ctx, req)
	if err != nil {
		return CompletionResponse{}, err
	}
	if onResponse != nil && strings.TrimSpace(resp.Content) != "" {
		onResponse(resp.Content)
	}
	return resp, nil
}

func newLegacyGeminiProvider(cfg config.Config, logger *obs.Logger) (LLMProvider, error) {
	key := strings.TrimSpace(os.Getenv(cfg.Provider.APIKeyEnv))
	if key == "" {
		return nil, fmt.Errorf("environment variable %s is not set", cfg.Provider.APIKeyEnv)
	}
	endpoint := cfg.Provider.Endpoint
	if endpoint == "" {
		endpoint = fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", cfg.Provider.Model, key)
	}
	return &httpProvider{
		name:     "gemini",
		model:    cfg.Provider.Model,
		endpoint: endpoint,
		http:     &http.Client{Timeout: 45 * time.Second},
		buildBody: func(model string, req CompletionRequest) (any, error) {
			prompt := req.SystemPrompt + "\n\n" + req.UserPrompt
			return map[string]any{
				"contents": []map[string]any{
					{
						"parts": []map[string]string{{"text": prompt}},
					},
				},
			}, nil
		},
		extract: func(body []byte) (string, error) {
			var resp struct {
				Candidates []struct {
					Content struct {
						Parts []struct {
							Text string `json:"text"`
						} `json:"parts"`
					} `json:"content"`
				} `json:"candidates"`
			}
			if err := json.Unmarshal(body, &resp); err != nil {
				return "", err
			}
			if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
				return "", fmt.Errorf("no candidates returned")
			}
			return resp.Candidates[0].Content.Parts[0].Text, nil
		},
		logger: logger,
	}, nil
}

func float64Ptr(v float64) *float64 {
	return &v
}

func intPtr(v int) *int {
	return &v
}

// MockProvider is used in unit and integration tests.
type MockProvider struct {
	NameValue  string
	CompleteFn func(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
	StreamFn   func(ctx context.Context, req CompletionRequest, onThinking func(string), onResponse func(string)) (CompletionResponse, error)
}

func (m MockProvider) Name() string {
	if m.NameValue == "" {
		return "mock"
	}
	return m.NameValue
}

func (m MockProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	if m.CompleteFn == nil {
		return CompletionResponse{}, fmt.Errorf("mock provider complete function is nil")
	}
	return m.CompleteFn(ctx, req)
}

func (m MockProvider) CompleteStream(ctx context.Context, req CompletionRequest, onThinking func(string), onResponse func(string)) (CompletionResponse, error) {
	if m.StreamFn != nil {
		return m.StreamFn(ctx, req, onThinking, onResponse)
	}
	return m.Complete(ctx, req)
}
