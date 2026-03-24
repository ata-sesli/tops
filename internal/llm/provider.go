package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	anyllm "github.com/mozilla-ai/any-llm-go"
	anyanthropic "github.com/mozilla-ai/any-llm-go/providers/anthropic"
	anygemini "github.com/mozilla-ai/any-llm-go/providers/gemini"
	anyopenai "github.com/mozilla-ai/any-llm-go/providers/openai"
	ollamaapi "github.com/ollama/ollama/api"

	"tops/internal/config"
	"tops/internal/modelprofile"
	"tops/internal/obs"
	"tops/internal/ollama"
	"tops/internal/progress"
)

type CompletionRequest struct {
	SystemPrompt string
	UserPrompt   string
	Temperature  float64
	MaxTokens    int
}

type CompletionResponse struct {
	Content string
	Raw     string
}

type LLMProvider interface {
	Name() string
	Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
}

type ProviderOptions struct {
	ModelProfile modelprofile.ModelProfile
}

const hostedRequestTimeout = 45 * time.Second
const ollamaRequestTimeout = 3 * time.Minute

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
	case config.ProviderOllama:
		return newOllamaProvider(cfg, logger, "ollama", options.ModelProfile)
	case config.ProviderLocal:
		return newOllamaProvider(cfg, logger, "local", options.ModelProfile)
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
	params := anyllm.CompletionParams{
		Model: p.model,
		Messages: []anyllm.Message{
			{Role: anyllm.RoleSystem, Content: req.SystemPrompt},
			{Role: anyllm.RoleUser, Content: req.UserPrompt},
		},
		Temperature: float64Ptr(req.Temperature),
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

type ollamaProvider struct {
	name     string
	model    string
	endpoint string
	http     *http.Client
	manager  ollama.Manager
	profile  modelprofile.ModelProfile
	logger   *obs.Logger
}

func newOllamaProvider(cfg config.Config, logger *obs.Logger, providerName string, profile modelprofile.ModelProfile) (LLMProvider, error) {
	endpoint := cfg.Provider.Endpoint
	if endpoint == "" {
		endpoint = "http://localhost:11434"
	}
	return &ollamaProvider{
		name:     providerName,
		model:    cfg.Provider.Model,
		endpoint: ollama.NormalizeBaseURL(endpoint),
		http:     &http.Client{Timeout: ollamaRequestTimeout},
		manager:  ollama.NewManager(ollama.Options{Logger: logger}),
		profile:  profile,
		logger:   logger,
	}, nil
}

func (p *ollamaProvider) Name() string {
	return p.name
}

func (p *ollamaProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	if err := p.manager.EnsureRunning(ctx, p.endpoint); err != nil {
		return CompletionResponse{}, fmt.Errorf("ollama availability check failed: %w", err)
	}
	client, err := newOllamaAPIClient(p.endpoint, p.http)
	if err != nil {
		return CompletionResponse{}, err
	}

	chatReq := buildOllamaChatRequest(p.model, req, p.profile)

	var last ollamaapi.ChatResponse
	var contentBuilder strings.Builder
	var thinkingBuilder strings.Builder
	if err := client.Chat(ctx, chatReq, func(resp ollamaapi.ChatResponse) error {
		last = resp
		if thinking := resp.Message.Thinking; thinking != "" {
			thinkingBuilder.WriteString(thinking)
			progress.UpdatePhase(ctx, "provider-thinking")
			progress.EmitThinkingChunk(ctx, thinking)
		}
		if content := resp.Message.Content; content != "" {
			contentBuilder.WriteString(content)
			progress.UpdatePhase(ctx, "provider-answering")
			progress.EmitResponseChunk(ctx, content)
		}
		return nil
	}); err != nil {
		return CompletionResponse{}, fmt.Errorf("provider request failed: %w", err)
	}
	content := strings.TrimSpace(contentBuilder.String())
	if content == "" {
		content = strings.TrimSpace(last.Message.Content)
	}
	if content == "" {
		return CompletionResponse{}, fmt.Errorf("parse provider response: no message content returned")
	}
	if collected := strings.TrimSpace(contentBuilder.String()); collected != "" {
		last.Message.Content = collected
	}
	if collected := strings.TrimSpace(thinkingBuilder.String()); collected != "" {
		last.Message.Thinking = collected
	}
	raw, err := json.Marshal(last)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("marshal provider response: %w", err)
	}
	return CompletionResponse{
		Content: content,
		Raw:     string(raw),
	}, nil
}

func buildOllamaChatRequest(model string, req CompletionRequest, profile modelprofile.ModelProfile) *ollamaapi.ChatRequest {
	systemPrompt := strings.TrimSpace(req.SystemPrompt)
	if profilePrompt := strings.TrimSpace(profile.SystemPrompt); profilePrompt != "" {
		if systemPrompt == "" {
			systemPrompt = profilePrompt
		} else {
			systemPrompt = systemPrompt + "\n\n" + profilePrompt
		}
	}
	maxTokens := req.MaxTokens
	if profile.MaxLength > 0 {
		maxTokens = profile.MaxLength
	}
	options := map[string]any{
		"temperature": req.Temperature,
	}
	if profile.Context > 0 {
		options["num_ctx"] = profile.Context
	}
	if maxTokens > 0 {
		options["num_predict"] = maxTokens
	}
	stream := true
	chatReq := &ollamaapi.ChatRequest{
		Model: model,
		Messages: []ollamaapi.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: req.UserPrompt},
		},
		Stream:  &stream,
		Options: options,
	}
	if thinkValue := ollamaThinkValue(profile.Think); thinkValue != nil {
		chatReq.Think = thinkValue
	}
	return chatReq
}

func newOllamaAPIClient(endpoint string, httpClient *http.Client) (*ollamaapi.Client, error) {
	baseURL, err := url.Parse(ollama.NormalizeBaseURL(endpoint))
	if err != nil {
		return nil, fmt.Errorf("invalid ollama endpoint %q: %w", endpoint, err)
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: ollamaRequestTimeout}
	}
	return ollamaapi.NewClient(baseURL, httpClient), nil
}

func ollamaThinkValue(value string) *ollamaapi.ThinkValue {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return nil
	case "on", "true":
		return &ollamaapi.ThinkValue{Value: true}
	case "off", "false":
		return &ollamaapi.ThinkValue{Value: false}
	case "low", "medium", "high":
		return &ollamaapi.ThinkValue{Value: strings.ToLower(strings.TrimSpace(value))}
	default:
		return nil
	}
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
