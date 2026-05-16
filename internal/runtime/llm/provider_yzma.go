package llm

import (
	"context"
	"debug/macho"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ebitengine/purego"
	yzllama "github.com/hybridgroup/yzma/pkg/llama"
	yzmsg "github.com/hybridgroup/yzma/pkg/message"
	yztemplate "github.com/hybridgroup/yzma/pkg/template"
	yzutils "github.com/hybridgroup/yzma/pkg/utils"

	"tops/internal/config"
	"tops/internal/obs"
	"tops/internal/ops/benchmetrics"
	"tops/internal/storage/modelprofile"
)

const (
	yzmaRequestTimeout       = 3 * time.Minute
	yzmaDefaultContextTokens = 8192
	yzmaDefaultRepeatPenalty = 1.05
)

var (
	yzmaLibMu          sync.Mutex
	yzmaLibInitialized bool
	yzmaLibPath        string

	yzmaLogMu             sync.Mutex
	yzmaLogCallback       uintptr
	yzmaLogMirrorToStdout bool
	yzmaLogLines          []string

	yzmaProviderSeq atomic.Uint64
)

const yzmaLogRingCapacity = 500

var yzmaVersionPattern = regexp.MustCompile(`\bb[0-9]{3,}\b`)

// YZMAInitError provides stage-specific diagnostics for local runtime failures.
type YZMAInitError struct {
	Stage        string
	Reason       string
	Evidence     []string
	LikelyCauses []string
	Fix          []string
}

func (e *YZMAInitError) Error() string {
	if e == nil {
		return "yzma initialization failed"
	}
	stage := strings.TrimSpace(e.Stage)
	if stage == "" {
		stage = "yzma_init"
	}
	reason := strings.TrimSpace(e.Reason)
	if reason == "" {
		reason = "unknown initialization failure"
	}

	var b strings.Builder
	b.WriteString(stage)
	b.WriteString("_failed: ")
	b.WriteString(reason)

	if len(e.Evidence) > 0 {
		b.WriteString("\n\nEvidence:")
		for _, item := range e.Evidence {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			b.WriteString("\n  ")
			b.WriteString(item)
		}
	}
	if len(e.LikelyCauses) > 0 {
		b.WriteString("\n\nLikely causes:")
		for _, item := range e.LikelyCauses {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			b.WriteString("\n  - ")
			b.WriteString(item)
		}
	}
	if len(e.Fix) > 0 {
		b.WriteString("\n\nFix:")
		for _, item := range e.Fix {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			b.WriteString("\n  - ")
			b.WriteString(item)
		}
	}
	return b.String()
}

// AsYZMAInitError unwraps rich YZMA initialization diagnostics when available.
func AsYZMAInitError(err error) (*YZMAInitError, bool) {
	var target *YZMAInitError
	if errors.As(err, &target) && target != nil {
		return target, true
	}
	return nil, false
}

type yzmaDylibInfo struct {
	Name          string
	Path          string
	Architectures []string
}

func (d yzmaDylibInfo) supportsArm64() bool {
	for _, arch := range d.Architectures {
		switch strings.ToLower(strings.TrimSpace(arch)) {
		case "arm64", "arm64e":
			return true
		}
	}
	return false
}

type yzmaLibraryReport struct {
	LibPath    string
	YZMALibEnv string
	Dylibs     []yzmaDylibInfo
}

type yzmaProvider struct {
	name string

	model     string
	modelPath string
	libPath   string
	profile   modelprofile.ModelProfile
	logger    *obs.Logger

	mu           sync.Mutex
	loaded       bool
	loadSeen     bool
	ensureCalls  int
	loadCount    int
	unloadCount  int
	instanceID   string
	modelHandle  yzllama.Model
	ctxHandle    yzllama.Context
	vocab        yzllama.Vocab
	chatTemplate string
	probeContext ProbeContextSnapshot
}

type yzmaSamplingConfig struct {
	ProfileName      string
	Temperature      float64
	MaxTokens        int
	TopK             int
	TopP             float64
	MinP             float64
	RepeatPenalty    float64
	HasTemperature   bool
	HasTopK          bool
	HasTopP          bool
	HasMinP          bool
	HasRepeatPenalty bool
}

type yzmaSamplingPreset struct {
	Temperature   *float64
	MaxTokens     *int
	TopK          *int
	TopP          *float64
	MinP          *float64
	RepeatPenalty *float64
}

func newYZMAProvider(cfg config.Config, logger *obs.Logger, profile modelprofile.ModelProfile) (LLMProvider, error) {
	modelPath := strings.TrimSpace(cfg.Provider.ModelPath)
	libPath := strings.TrimSpace(cfg.Provider.LibPath)
	if libPath == "" {
		libPath = strings.TrimSpace(os.Getenv("YZMA_LIB"))
	}
	if libPath == "" {
		libPath = config.DefaultYZMARuntimeLibDir()
	}
	return &yzmaProvider{
		name:      "yzma",
		model:     strings.TrimSpace(cfg.Provider.Model),
		modelPath: modelPath,
		libPath:   libPath,
		profile:   profile,
		logger:    logger,
		instanceID: fmt.Sprintf("yzma-%d-%d",
			os.Getpid(),
			yzmaProviderSeq.Add(1),
		),
	}, nil
}

func (p *yzmaProvider) Name() string {
	return p.name
}

func (p *yzmaProvider) ProbeContext() ProbeContextSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return copyProbeContextSnapshot(p.probeContext)
}

func (p *yzmaProvider) Warm(ctx context.Context) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.probeContext.LastCallType = "warm"
	p.probeContext.LifecycleState = "warming"
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	yzmaDebugf(p.logger,
		"call_start pid=%d instance=%s type=warm model_path=%s lib_path=%s YZMA_LIB=%s TOPS_YZMA_CPU_FALLBACK=%s TOPS_YZMA_DEBUG_LOG=%s TOPS_YZMA_DEBUG_RAW=%s loaded=%t load_seen=%t",
		os.Getpid(),
		p.instanceID,
		strings.TrimSpace(p.modelPath),
		strings.TrimSpace(p.libPath),
		strings.TrimSpace(os.Getenv("YZMA_LIB")),
		strings.TrimSpace(os.Getenv("TOPS_YZMA_CPU_FALLBACK")),
		strings.TrimSpace(os.Getenv("TOPS_YZMA_DEBUG_LOG")),
		strings.TrimSpace(os.Getenv("TOPS_YZMA_DEBUG_RAW")),
		p.loaded,
		p.loadSeen,
	)
	return p.ensureLoadedLocked()
}

func (p *yzmaProvider) Unload(ctx context.Context) error {
	_ = ctx
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.ctxHandle != 0 {
		_ = yzllama.Free(p.ctxHandle)
		p.ctxHandle = 0
	}
	if p.modelHandle != 0 {
		yzllama.ModelFree(p.modelHandle)
		p.modelHandle = 0
	}
	p.vocab = 0
	p.loaded = false
	p.loadSeen = false
	p.unloadCount++
	p.probeContext.UnloadCount = p.unloadCount
	p.probeContext.LifecycleState = "unloaded"
	unloadYZMALibraries()
	return nil
}

func (p *yzmaProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	return p.completeInternal(ctx, req, nil)
}

func (p *yzmaProvider) CompleteStream(ctx context.Context, req CompletionRequest, onThinking func(string), onResponse func(string)) (CompletionResponse, error) {
	_ = onThinking
	return p.completeInternal(ctx, req, onResponse)
}

func (p *yzmaProvider) ToolChat(ctx context.Context, req ToolChatRequest, onThinking func(string), onResponse func(string)) (ToolChatResponse, error) {
	_ = onThinking
	started := time.Now()
	promptTokens := 0
	completionTokens := 0
	defer func() {
		benchmetrics.RecordLLMCallWithUsage(ctx, time.Since(started), promptTokens, completionTokens)
	}()

	p.mu.Lock()
	defer p.mu.Unlock()
	p.probeContext.LastCallType = "toolchat"
	p.probeContext.LastSamplingProfile = strings.TrimSpace(req.SamplingProfile)
	p.probeContext.LastMaxTokens = req.MaxTokens
	p.probeContext.LifecycleState = "toolchat_start"
	yzmaDebugf(p.logger,
		"call_start pid=%d instance=%s type=toolchat profile=%s max_tokens=%d model_path=%s lib_path=%s YZMA_LIB=%s TOPS_YZMA_CPU_FALLBACK=%s TOPS_YZMA_DEBUG_LOG=%s TOPS_YZMA_DEBUG_RAW=%s loaded=%t load_seen=%t",
		os.Getpid(),
		p.instanceID,
		strings.TrimSpace(req.SamplingProfile),
		req.MaxTokens,
		strings.TrimSpace(p.modelPath),
		strings.TrimSpace(p.libPath),
		strings.TrimSpace(os.Getenv("YZMA_LIB")),
		strings.TrimSpace(os.Getenv("TOPS_YZMA_CPU_FALLBACK")),
		strings.TrimSpace(os.Getenv("TOPS_YZMA_DEBUG_LOG")),
		strings.TrimSpace(os.Getenv("TOPS_YZMA_DEBUG_RAW")),
		p.loaded,
		p.loadSeen,
	)

	if err := p.ensureLoadedLocked(); err != nil {
		return ToolChatResponse{}, err
	}

	systemPrompt := strings.TrimSpace(req.SystemPrompt)
	if profilePrompt := strings.TrimSpace(p.profile.SystemPrompt); profilePrompt != "" {
		if systemPrompt == "" {
			systemPrompt = profilePrompt
		} else {
			systemPrompt = systemPrompt + "\n\n" + profilePrompt
		}
	}
	if len(req.Tools) > 0 {
		systemPrompt = buildYZMAToolSystemPrompt(systemPrompt, req.Tools)
	}

	msgs := make([]yzmsg.Message, 0, len(req.Messages)+1)
	if systemPrompt != "" {
		msgs = append(msgs, yzmsg.Chat{Role: "system", Content: systemPrompt})
	}
	msgs = append(msgs, toYZMAMessages(req.Messages)...)

	prompt, err := p.applyChatTemplateLocked(msgs, true)
	if err != nil {
		return ToolChatResponse{}, err
	}

	sampling := p.effectiveSamplingConfig(req.SamplingProfile, req.Temperature, req.MaxTokens, req.Think)
	stopSequences := p.defaultStopSequencesLocked()
	logYZMARawRequest("tool_chat", prompt, sampling, stopSequences)
	out, promptCount, completionCount, genErr := p.generateLocked(ctx, prompt, sampling, stopSequences, onResponse)
	promptTokens = promptCount
	completionTokens = completionCount
	if genErr != nil {
		return ToolChatResponse{}, genErr
	}
	logYZMARawOutput("tool_chat", out)
	p.probeContext.LifecycleState = "toolchat_done"

	toolCalls := parseYZMAToolCalls(out)
	content := strings.TrimSpace(out)
	if len(toolCalls) > 0 {
		content = ""
	}

	rawBlob, _ := json.Marshal(map[string]any{
		"content":           strings.TrimSpace(out),
		"tool_calls":        toolCalls,
		"prompt_tokens":     promptTokens,
		"completion_tokens": completionTokens,
	})
	return ToolChatResponse{
		Content:   content,
		ToolCalls: toolCalls,
		Raw:       string(rawBlob),
	}, nil
}

func (p *yzmaProvider) completeInternal(ctx context.Context, req CompletionRequest, onResponse func(string)) (CompletionResponse, error) {
	started := time.Now()
	promptTokens := 0
	completionTokens := 0
	defer func() {
		benchmetrics.RecordLLMCallWithUsage(ctx, time.Since(started), promptTokens, completionTokens)
	}()

	p.mu.Lock()
	defer p.mu.Unlock()
	p.probeContext.LastCallType = "completion"
	p.probeContext.LastSamplingProfile = strings.TrimSpace(req.SamplingProfile)
	p.probeContext.LastMaxTokens = req.MaxTokens
	p.probeContext.LifecycleState = "completion_start"
	yzmaDebugf(p.logger,
		"call_start pid=%d instance=%s type=completion profile=%s max_tokens=%d model_path=%s lib_path=%s YZMA_LIB=%s TOPS_YZMA_CPU_FALLBACK=%s TOPS_YZMA_DEBUG_LOG=%s TOPS_YZMA_DEBUG_RAW=%s loaded=%t load_seen=%t",
		os.Getpid(),
		p.instanceID,
		strings.TrimSpace(req.SamplingProfile),
		req.MaxTokens,
		strings.TrimSpace(p.modelPath),
		strings.TrimSpace(p.libPath),
		strings.TrimSpace(os.Getenv("YZMA_LIB")),
		strings.TrimSpace(os.Getenv("TOPS_YZMA_CPU_FALLBACK")),
		strings.TrimSpace(os.Getenv("TOPS_YZMA_DEBUG_LOG")),
		strings.TrimSpace(os.Getenv("TOPS_YZMA_DEBUG_RAW")),
		p.loaded,
		p.loadSeen,
	)

	if err := p.ensureLoadedLocked(); err != nil {
		return CompletionResponse{}, err
	}

	systemPrompt := strings.TrimSpace(req.SystemPrompt)
	if profilePrompt := strings.TrimSpace(p.profile.SystemPrompt); profilePrompt != "" {
		if systemPrompt == "" {
			systemPrompt = profilePrompt
		} else {
			systemPrompt = systemPrompt + "\n\n" + profilePrompt
		}
	}

	msgs := []yzmsg.Message{
		yzmsg.Chat{Role: "user", Content: strings.TrimSpace(req.UserPrompt)},
	}
	if systemPrompt != "" {
		msgs = append([]yzmsg.Message{yzmsg.Chat{Role: "system", Content: systemPrompt}}, msgs...)
	}

	prompt, err := p.applyChatTemplateLocked(msgs, true)
	if err != nil {
		return CompletionResponse{}, err
	}

	sampling := p.effectiveSamplingConfig(req.SamplingProfile, req.Temperature, req.MaxTokens, req.Think)
	stopSequences := p.defaultStopSequencesLocked()
	logYZMARawRequest("complete", prompt, sampling, stopSequences)
	content, promptCount, completionCount, genErr := p.generateLocked(ctx, prompt, sampling, stopSequences, onResponse)
	promptTokens = promptCount
	completionTokens = completionCount
	if genErr != nil {
		return CompletionResponse{}, genErr
	}
	logYZMARawOutput("complete", content)
	p.probeContext.LifecycleState = "completion_done"
	content = strings.TrimSpace(content)
	if content == "" {
		return CompletionResponse{}, fmt.Errorf("parse provider response: no message content returned")
	}

	rawBlob, _ := json.Marshal(map[string]any{
		"content":           content,
		"prompt_tokens":     promptTokens,
		"completion_tokens": completionTokens,
	})
	return CompletionResponse{Content: content, Raw: string(rawBlob)}, nil
}

func (p *yzmaProvider) ensureLoadedLocked() error {
	modelPath := strings.TrimSpace(p.modelPath)
	libPath := strings.TrimSpace(p.libPath)
	p.ensureCalls++
	prev := p.probeContext
	p.probeContext = ProbeContextSnapshot{
		ModelPath:           modelPath,
		LibPath:             libPath,
		YZMALibEnv:          strings.TrimSpace(os.Getenv("YZMA_LIB")),
		CPUFallback:         allowYZMACPUFallback(),
		DebugLog:            yzmaDebugEnabled(),
		DebugRaw:            yzmaRawDebugEnabled(),
		ProcessPID:          os.Getpid(),
		ProcessPPID:         os.Getppid(),
		ProcessGOOS:         runtime.GOOS,
		ProcessGOARCH:       runtime.GOARCH,
		ProviderInstanceID:  p.instanceID,
		EnsureLoadCalls:     p.ensureCalls,
		EnsureLoadReused:    false,
		LoadCount:           p.loadCount,
		UnloadCount:         p.unloadCount,
		LifecycleState:      "ensure_loaded_start",
		LastCallType:        strings.TrimSpace(prev.LastCallType),
		LastSamplingProfile: strings.TrimSpace(prev.LastSamplingProfile),
		LastMaxTokens:       prev.LastMaxTokens,
	}
	applyYZMARuntimeEnvSnapshot(&p.probeContext)
	if modelPath == "" {
		return fmt.Errorf("yzma provider missing model_path; set provider.model_path to a local GGUF file")
	}
	if libPath == "" {
		return fmt.Errorf("yzma provider missing lib_path; set provider.lib_path or YZMA_LIB to llama.cpp shared libraries")
	}
	yzmaDebugf(p.logger,
		"runtime_env pid=%d ppid=%d is_tty=%t goos=%s goarch=%s term=%s ssh=%s tmux=%s ci=%s uname=%s cpu_brand=%s rosetta=%s",
		p.probeContext.ProcessPID,
		p.probeContext.ProcessPPID,
		p.probeContext.ProcessIsTTY,
		safeUnknown(p.probeContext.ProcessGOOS),
		safeUnknown(p.probeContext.ProcessGOARCH),
		safeUnknown(p.probeContext.TERM),
		safeUnknown(p.probeContext.SSHConnection),
		safeUnknown(p.probeContext.TMUX),
		safeUnknown(p.probeContext.CI),
		safeUnknown(p.probeContext.Uname),
		safeUnknown(p.probeContext.CPUBrand),
		safeUnknown(p.probeContext.RosettaTranslated),
	)
	yzmaDebugf(p.logger, "effective_paths model_path=%s lib_path=%s YZMA_LIB=%s", modelPath, libPath, strings.TrimSpace(os.Getenv("YZMA_LIB")))
	modelAbs, err := filepath.Abs(modelPath)
	if err == nil {
		modelPath = modelAbs
	}
	libAbs, err := filepath.Abs(libPath)
	if err == nil {
		libPath = libAbs
	}
	p.probeContext.ModelPath = modelPath
	p.probeContext.LibPath = libPath
	p.probeContext.ResolvedLlamaDylibPath = resolveLlamaDylibPath(libPath)
	if _, statErr := os.Stat(modelPath); statErr != nil {
		return fmt.Errorf("yzma provider model_path is not readable: %w", statErr)
	}
	if _, statErr := os.Stat(libPath); statErr != nil {
		return fmt.Errorf("yzma provider lib_path is not readable: %w", statErr)
	}
	if p.loaded && p.modelHandle != 0 && p.ctxHandle != 0 && p.vocab != 0 {
		p.probeContext.EnsureLoadReused = true
		if p.probeContext.ContextParams.NCtx == 0 {
			p.probeContext.ContextParams = prev.ContextParams
		}
		if len(p.probeContext.BackendNames) == 0 && len(prev.BackendNames) > 0 {
			p.probeContext.BackendNames = append([]string(nil), prev.BackendNames...)
		}
		if len(p.probeContext.DeviceNames) == 0 && len(prev.DeviceNames) > 0 {
			p.probeContext.DeviceNames = append([]string(nil), prev.DeviceNames...)
		}
		p.probeContext.LifecycleState = "ensure_loaded_reuse"
		yzmaDebugf(p.logger,
			"ensure_loaded pid=%d instance=%s reused=true ensure_calls=%d load_count=%d unload_count=%d",
			os.Getpid(),
			p.instanceID,
			p.ensureCalls,
			p.loadCount,
			p.unloadCount,
		)
		return nil
	}
	if err := ensureYZMALibraries(libPath, p.logger); err != nil {
		backendSnapshot := collectYZMABackendSnapshot()
		applyBackendSnapshotToProbeContext(&p.probeContext, backendSnapshot)
		return fmt.Errorf("initialize yzma libraries failed: %w", err)
	}
	backendSnapshot := collectYZMABackendSnapshot()
	applyBackendSnapshotToProbeContext(&p.probeContext, backendSnapshot)
	p.modelPath = modelPath
	p.libPath = libPath

	resetYZMALogBuffer()
	model, err := yzllama.ModelLoadFromFile(modelPath, yzllama.ModelDefaultParams())
	if err != nil {
		return fmt.Errorf("load yzma model: %w", err)
	}
	params := yzllama.ContextDefaultParams()
	if p.profile.Context > 0 {
		params.NCtx = uint32(p.profile.Context)
	} else if params.NCtx < yzmaDefaultContextTokens {
		params.NCtx = yzmaDefaultContextTokens
	}
	modelParams := yzllama.ModelDefaultParams()
	p.probeContext.ContextParams = contextParamsSnapshot(params)
	p.probeContext.ContextParams.NGPULayers = int32(modelParams.NGpuLayers)
	yzmaDebugf(p.logger,
		"context_params pid=%d instance=%s n_ctx=%d n_batch=%d n_ubatch=%d n_seq_max=%d n_threads=%d n_threads_batch=%d offload_kqv=%t op_offload=%t n_gpu_layers=%d",
		os.Getpid(),
		p.instanceID,
		p.probeContext.ContextParams.NCtx,
		p.probeContext.ContextParams.NBatch,
		p.probeContext.ContextParams.NUbatch,
		p.probeContext.ContextParams.NSeqMax,
		p.probeContext.ContextParams.NThreads,
		p.probeContext.ContextParams.NThreadsBatch,
		p.probeContext.ContextParams.OffloadKQV,
		p.probeContext.ContextParams.OpOffload,
		p.probeContext.ContextParams.NGPULayers,
	)
	ctxHandle, err := yzllama.InitFromModel(model, params)
	if err != nil {
		primaryErr := err
		evidence := extractBackendEvidence(snapshotYZMALogBuffer(), primaryErr)
		evidence = appendBackendSnapshotEvidence(evidence, backendSnapshot, p.probeContext.ContextParams)
		yzllama.ModelFree(model)
		if p.logger != nil && p.logger.Enabled() {
			p.logger.Printf("provider=yzma context_init=failed mode=default error=%v", primaryErr)
		}
		if !allowYZMACPUFallback() {
			return fmt.Errorf("create yzma context: %w", newYZMABackendInitError(primaryErr, evidence))
		}
		fallbackModel, fallbackCtx, fallbackErr := loadYZMAContextCPUFallback(modelPath, params)
		if fallbackErr != nil {
			return fmt.Errorf("create yzma context: %w (cpu fallback failed: %v)", newYZMABackendInitError(primaryErr, evidence), fallbackErr)
		}
		model = fallbackModel
		ctxHandle = fallbackCtx
		if p.logger != nil && p.logger.Enabled() {
			p.logger.Printf("provider=yzma context_init=ok mode=cpu_fallback")
		}
	}

	backendSnapshot = collectYZMABackendSnapshot()
	applyBackendSnapshotToProbeContext(&p.probeContext, backendSnapshot)

	template := strings.TrimSpace(yzllama.ModelChatTemplate(model, ""))
	if template == "" {
		template = defaultTemplateForModel(p.model)
	}

	p.modelHandle = model
	p.ctxHandle = ctxHandle
	p.vocab = yzllama.ModelGetVocab(model)
	p.chatTemplate = template
	p.loaded = true
	p.loadCount++
	p.probeContext.LoadCount = p.loadCount
	p.probeContext.LifecycleState = "ensure_loaded_created"
	p.probeContext.EnsureLoadReused = false
	if !p.loadSeen {
		p.loadSeen = true
		if p.logger != nil {
			p.logger.Printf("provider=yzma model=%s model_path=%s status=loaded", p.model, p.modelPath)
		}
	}
	yzmaDebugf(p.logger,
		"ensure_loaded pid=%d instance=%s reused=false ensure_calls=%d load_count=%d unload_count=%d backend_names=%s device_names=%s",
		os.Getpid(),
		p.instanceID,
		p.ensureCalls,
		p.loadCount,
		p.unloadCount,
		strings.Join(p.probeContext.BackendNames, ","),
		strings.Join(p.probeContext.DeviceNames, ","),
	)
	return nil
}

func (p *yzmaProvider) generateLocked(ctx context.Context, prompt string, sampling yzmaSamplingConfig, stopSequences []string, onResponse func(string)) (string, int, int, error) {
	if p.ctxHandle == 0 || p.modelHandle == 0 || p.vocab == 0 {
		return "", 0, 0, fmt.Errorf("yzma runtime is not initialized")
	}
	mem, memErr := yzllama.GetMemory(p.ctxHandle)
	if memErr == nil && mem != 0 {
		_ = yzllama.MemoryClear(mem, true)
	}

	tokens := yzllama.Tokenize(p.vocab, prompt, true, false)
	if len(tokens) == 0 {
		return "", 0, 0, fmt.Errorf("tokenization produced no tokens")
	}

	batchLimit := int(yzllama.NBatch(p.ctxHandle))
	if batchLimit <= 0 {
		batchLimit = 2048
	}

	var batch yzllama.Batch
	if yzllama.ModelHasEncoder(p.modelHandle) {
		for start := 0; start < len(tokens); start += batchLimit {
			end := start + batchLimit
			if end > len(tokens) {
				end = len(tokens)
			}
			batch = yzllama.BatchGetOne(tokens[start:end])
			if _, err := yzllama.Encode(p.ctxHandle, batch); err != nil {
				return "", len(tokens), 0, fmt.Errorf("encode failed: %w", err)
			}
		}
		start := yzllama.ModelDecoderStartToken(p.modelHandle)
		if start == yzllama.TokenNull {
			start = yzllama.VocabBOS(p.vocab)
		}
		batch = yzllama.BatchGetOne([]yzllama.Token{start})
		if _, err := yzllama.Decode(p.ctxHandle, batch); err != nil {
			return "", len(tokens), 0, fmt.Errorf("decode failed: %w", err)
		}
	} else {
		for start := 0; start < len(tokens); start += batchLimit {
			end := start + batchLimit
			if end > len(tokens) {
				end = len(tokens)
			}
			batch = yzllama.BatchGetOne(tokens[start:end])
			if _, err := yzllama.Decode(p.ctxHandle, batch); err != nil {
				return "", len(tokens), 0, fmt.Errorf("decode failed: %w", err)
			}
		}
	}

	sp := yzllama.DefaultSamplerParams()
	if sampling.HasTemperature {
		sp.Temp = float32(sampling.Temperature)
	}
	if sampling.HasTopK {
		sp.TopK = int32(sampling.TopK)
	}
	if sampling.HasTopP {
		sp.TopP = float32(sampling.TopP)
	}
	if sampling.HasMinP {
		sp.MinP = float32(sampling.MinP)
	}
	if sampling.HasRepeatPenalty {
		sp.PenaltyRepeat = float32(sampling.RepeatPenalty)
	}
	if yzmaRawDebugEnabled() {
		_, _ = fmt.Fprintf(os.Stdout, "[tops yzma raw sampler] profile=%s temp=%.4f top_k=%d top_p=%.4f min_p=%.4f repeat_penalty=%.4f\n",
			strings.TrimSpace(safeUnknown(sampling.ProfileName)),
			sp.Temp, sp.TopK, sp.TopP, sp.MinP, sp.PenaltyRepeat)
	}
	sampler := yzllama.NewSampler(p.modelHandle, yzllama.DefaultSamplers, sp)
	defer yzllama.SamplerFree(sampler)

	maxTokens := sampling.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 512
	}

	var b strings.Builder
	completionCount := 0
	for i := 0; i < maxTokens; i++ {
		select {
		case <-ctx.Done():
			return b.String(), len(tokens), completionCount, ctx.Err()
		default:
		}

		token := yzllama.SamplerSample(sampler, p.ctxHandle, -1)
		if yzllama.VocabIsEOG(p.vocab, token) {
			break
		}
		buf := make([]byte, 256)
		n := yzllama.TokenToPiece(p.vocab, token, buf, 0, true)
		if n > 0 {
			piece := string(buf[:n])
			prevLen := b.Len()
			b.WriteString(piece)
			current := b.String()
			if trimmed, hit := cutAtStopSequence(current, stopSequences); hit {
				if onResponse != nil && len(trimmed) > prevLen {
					onResponse(trimmed[prevLen:])
				}
				b.Reset()
				b.WriteString(trimmed)
				break
			}
			if onResponse != nil {
				onResponse(piece)
			}
		}
		completionCount++
		batch = yzllama.BatchGetOne([]yzllama.Token{token})
		if _, err := yzllama.Decode(p.ctxHandle, batch); err != nil {
			return b.String(), len(tokens), completionCount, fmt.Errorf("decode continuation failed: %w", err)
		}
	}
	return b.String(), len(tokens), completionCount, nil
}

func toYZMAMessages(messages []ChatMessage) []yzmsg.Message {
	out := make([]yzmsg.Message, 0, len(messages))
	for _, msg := range messages {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		switch role {
		case "tool":
			out = append(out, yzmsg.ToolResponse{
				Role:    "tool",
				Name:    strings.TrimSpace(msg.Name),
				Content: strings.TrimSpace(msg.Content),
			})
		case "assistant":
			if len(msg.ToolCalls) > 0 {
				blocks := make([]string, 0, len(msg.ToolCalls))
				for _, tc := range msg.ToolCalls {
					payload := map[string]any{
						"name":      strings.TrimSpace(tc.Name),
						"arguments": tc.Arguments,
					}
					blob, err := json.Marshal(payload)
					if err != nil {
						continue
					}
					blocks = append(blocks, "<tool_call>\n"+string(blob)+"\n</tool_call>")
				}
				content := strings.TrimSpace(strings.Join(blocks, "\n"))
				if content == "" {
					content = strings.TrimSpace(msg.Content)
				}
				out = append(out, yzmsg.Chat{Role: "assistant", Content: content})
				continue
			}
			out = append(out, yzmsg.Chat{Role: "assistant", Content: msg.Content})
		default:
			if role == "" {
				role = "user"
			}
			out = append(out, yzmsg.Chat{Role: role, Content: msg.Content})
		}
	}
	return out
}

func (p *yzmaProvider) applyChatTemplateLocked(msgs []yzmsg.Message, addGenerationPrompt bool) (string, error) {
	templateText := strings.TrimSpace(p.chatTemplate)
	if templateText == "" {
		templateText = defaultTemplateForModel(p.model)
	}
	prompt, err := yztemplate.Apply(templateText, msgs, addGenerationPrompt)
	if err == nil {
		return prompt, nil
	}
	defaultTemplate := defaultTemplateForModel(p.model)
	if strings.TrimSpace(templateText) == strings.TrimSpace(defaultTemplate) {
		return "", fmt.Errorf("apply yzma chat template: %w", err)
	}
	fallbackPrompt, fallbackErr := yztemplate.Apply(defaultTemplate, msgs, addGenerationPrompt)
	if fallbackErr != nil {
		return "", fmt.Errorf("apply yzma chat template: %w (fallback failed: %v)", err, fallbackErr)
	}
	p.chatTemplate = defaultTemplate
	if p.logger != nil && p.logger.Enabled() {
		p.logger.Printf("provider=yzma template_fallback=default reason=%v", err)
	}
	return fallbackPrompt, nil
}

func buildYZMAToolSystemPrompt(base string, tools []ToolDefinition) string {
	var b strings.Builder
	if strings.TrimSpace(base) != "" {
		b.WriteString(strings.TrimSpace(base))
		b.WriteString("\n\n")
	}
	b.WriteString("You may call tools. When calling a tool, output ONLY this XML block:\n")
	b.WriteString("<tool_call>\n{\"name\":\"function_name\",\"arguments\":{...}}\n</tool_call>\n")
	b.WriteString("No extra prose before tool calls.\n")
	b.WriteString("Available tools JSON:\n")
	b.WriteString(renderToolsJSON(tools))
	return b.String()
}

func renderToolsJSON(tools []ToolDefinition) string {
	type functionShape struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	}
	type toolShape struct {
		Type     string        `json:"type"`
		Function functionShape `json:"function"`
	}
	items := make([]toolShape, 0, len(tools))
	for _, tool := range tools {
		properties := map[string]any{}
		for key, prop := range tool.Properties {
			item := map[string]any{
				"type":        strings.TrimSpace(prop.Type),
				"description": strings.TrimSpace(prop.Description),
			}
			if len(prop.Enum) > 0 {
				item["enum"] = append([]string(nil), prop.Enum...)
			}
			if prop.Items != nil {
				item["items"] = map[string]any{
					"type": strings.TrimSpace(prop.Items.Type),
				}
			}
			properties[key] = item
		}
		items = append(items, toolShape{
			Type: "function",
			Function: functionShape{
				Name:        strings.TrimSpace(tool.Name),
				Description: strings.TrimSpace(tool.Description),
				Parameters: map[string]any{
					"type":       "object",
					"properties": properties,
					"required":   append([]string(nil), tool.Required...),
				},
			},
		})
	}
	blob, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return "[]"
	}
	return string(blob)
}

func parseYZMAToolCalls(text string) []ToolCall {
	calls := yzmsg.ParseToolCalls(text)
	out := make([]ToolCall, 0, len(calls))
	for i, call := range calls {
		args := map[string]any{}
		for k, v := range call.Function.Arguments {
			args[k] = parseToolArgument(v)
		}
		out = append(out, ToolCall{
			ID:        fmt.Sprintf("yzma-tc-%d", i+1),
			Name:      strings.TrimSpace(call.Function.Name),
			Arguments: args,
		})
	}
	return out
}

func parseToolArgument(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if parsedBool, err := strconv.ParseBool(value); err == nil {
		return parsedBool
	}
	if parsedInt, err := strconv.ParseInt(value, 10, 64); err == nil {
		return parsedInt
	}
	if parsedFloat, err := strconv.ParseFloat(value, 64); err == nil {
		return parsedFloat
	}
	var parsed any
	if err := json.Unmarshal([]byte(value), &parsed); err == nil {
		return parsed
	}
	return value
}

func toolArgString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	default:
		blob, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(blob)
	}
}

func (p *yzmaProvider) effectiveSamplingConfig(requestSamplingProfile string, requestTemperature float64, requestMaxTokens int, requestThink *bool) yzmaSamplingConfig {
	sampling := p.defaultSamplingConfig(requestTemperature, requestMaxTokens)
	profileName := NormalizeSamplingProfile(requestSamplingProfile)
	if profileName != "" {
		if preset, ok := yzmaSamplingPresetForProfile(profileName); ok {
			sampling.ProfileName = profileName
			applyYZMASamplingPreset(&sampling, preset)
		}
	}
	if sampling.ProfileName == "" {
		sampling.ProfileName = "global_fallback"
	}
	if requestTemperature >= 0 {
		sampling.Temperature = requestTemperature
		sampling.HasTemperature = true
	}
	if requestMaxTokens > 0 {
		sampling.MaxTokens = requestMaxTokens
	}
	thinkEnabled, thinkTier := effectiveThinkState(p.profile.Think, requestThink)
	if thinkEnabled {
		budget := p.profile.ThinkBudgetTokens
		if budget <= 0 {
			budget = defaultThinkBudgetForTier(thinkTier)
		}
		if budget > 0 && (sampling.MaxTokens <= 0 || budget < sampling.MaxTokens) {
			sampling.MaxTokens = budget
		}
	}
	return sampling
}

func (p *yzmaProvider) defaultSamplingConfig(requestTemperature float64, requestMaxTokens int) yzmaSamplingConfig {
	sampling := yzmaSamplingConfig{
		Temperature:      requestTemperature,
		HasTemperature:   requestTemperature >= 0,
		MaxTokens:        effectiveMaxTokens(requestMaxTokens, p.profile.MaxLength),
		RepeatPenalty:    effectiveRepeatPenalty(p.profile.RepeatPenalty),
		HasRepeatPenalty: true,
	}
	if !sampling.HasTemperature && p.profile.Temperature > 0 {
		sampling.Temperature = p.profile.Temperature
		sampling.HasTemperature = true
	}
	if p.profile.TopK > 0 {
		sampling.TopK = p.profile.TopK
		sampling.HasTopK = true
	}
	if p.profile.TopP > 0 {
		sampling.TopP = p.profile.TopP
		sampling.HasTopP = true
	}
	if p.profile.MinP > 0 {
		sampling.MinP = p.profile.MinP
		sampling.HasMinP = true
	}
	return sampling
}

func applyYZMASamplingPreset(sampling *yzmaSamplingConfig, preset yzmaSamplingPreset) {
	if sampling == nil {
		return
	}
	if preset.Temperature != nil {
		sampling.Temperature = *preset.Temperature
		sampling.HasTemperature = true
	}
	if preset.MaxTokens != nil {
		sampling.MaxTokens = *preset.MaxTokens
	}
	if preset.TopK != nil {
		sampling.TopK = *preset.TopK
		sampling.HasTopK = true
	}
	if preset.TopP != nil {
		sampling.TopP = *preset.TopP
		sampling.HasTopP = true
	}
	if preset.MinP != nil {
		sampling.MinP = *preset.MinP
		sampling.HasMinP = true
	}
	if preset.RepeatPenalty != nil {
		sampling.RepeatPenalty = *preset.RepeatPenalty
		sampling.HasRepeatPenalty = true
	}
}

func yzmaSamplingPresetForProfile(profileName string) (yzmaSamplingPreset, bool) {
	switch NormalizeSamplingProfile(profileName) {
	case SamplingProfilePlanner:
		return yzmaSamplingPreset{
			Temperature:   float64Ptr(0),
			MaxTokens:     intPtr(256),
			TopK:          intPtr(0),
			TopP:          float64Ptr(1.0),
			MinP:          float64Ptr(0),
			RepeatPenalty: float64Ptr(1.0),
		}, true
	case SamplingProfileGen:
		return yzmaSamplingPreset{
			Temperature:   float64Ptr(0),
			MaxTokens:     intPtr(512),
			TopK:          intPtr(0),
			TopP:          float64Ptr(1.0),
			MinP:          float64Ptr(0),
			RepeatPenalty: float64Ptr(1.03),
		}, true
	case SamplingProfileAsk:
		return yzmaSamplingPreset{
			Temperature:   float64Ptr(0.4),
			MaxTokens:     intPtr(700),
			TopK:          intPtr(40),
			TopP:          float64Ptr(0.9),
			MinP:          float64Ptr(0.02),
			RepeatPenalty: float64Ptr(1.05),
		}, true
	case SamplingProfileHelp:
		return yzmaSamplingPreset{
			Temperature:   float64Ptr(0.3),
			MaxTokens:     intPtr(700),
			TopK:          intPtr(40),
			TopP:          float64Ptr(0.9),
			MinP:          float64Ptr(0.02),
			RepeatPenalty: float64Ptr(1.05),
		}, true
	case SamplingProfileSample:
		return yzmaSamplingPreset{
			Temperature:   float64Ptr(0.8),
			MaxTokens:     intPtr(512),
			TopK:          intPtr(40),
			TopP:          float64Ptr(0.95),
			MinP:          float64Ptr(0.05),
			RepeatPenalty: float64Ptr(1.05),
		}, true
	default:
		return yzmaSamplingPreset{}, false
	}
}

func effectiveThinkState(profileThink string, requestThink *bool) (enabled bool, tier string) {
	tier = normalizeThinkTier(profileThink)
	switch tier {
	case "off":
		return false, tier
	case "on":
		if requestThink == nil {
			return false, tier
		}
		return *requestThink, tier
	case "low", "medium", "high":
		if requestThink != nil {
			return *requestThink, tier
		}
		return true, tier
	default:
		if requestThink == nil {
			return false, tier
		}
		return *requestThink, tier
	}
}

func normalizeThinkTier(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true":
		return "on"
	case "false":
		return "off"
	case "on", "off", "low", "medium", "high":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func defaultThinkBudgetForTier(tier string) int {
	switch tier {
	case "low":
		return 128
	case "medium":
		return 256
	case "high":
		return 512
	default:
		return 0
	}
}

func effectiveMaxTokens(requestMax int, profileMax int) int {
	if profileMax > 0 {
		return profileMax
	}
	if requestMax > 0 {
		return requestMax
	}
	return 512
}

func effectiveRepeatPenalty(profileRepeatPenalty float64) float64 {
	if profileRepeatPenalty > 0 {
		return profileRepeatPenalty
	}
	return yzmaDefaultRepeatPenalty
}

func defaultTemplateForModel(model string) string {
	if strings.Contains(strings.ToLower(strings.TrimSpace(model)), "gemma") {
		return defaultYZMAGemmaTemplate
	}
	return defaultYZMAChatMLTemplate
}

func (p *yzmaProvider) defaultStopSequencesLocked() []string {
	stops := []string{}
	template := strings.TrimSpace(p.chatTemplate)
	if template == "" {
		template = defaultTemplateForModel(p.model)
	}
	lower := strings.ToLower(template)
	modelLower := strings.ToLower(strings.TrimSpace(p.model))
	if strings.Contains(lower, "<start_of_turn>") || strings.Contains(lower, "<end_of_turn>") || strings.Contains(modelLower, "gemma") {
		stops = append(stops, "<end_of_turn>", "<end_of_turn>\n", "<start_of_turn>model", "<start_of_turn>user")
	}
	if strings.Contains(lower, "<|im_start|>") || strings.Contains(lower, "<|im_end|>") {
		stops = append(stops, "<|im_end|>", "<|im_end|>\n", "<|im_start|>")
	}
	return dedupeStopSequences(stops)
}

func dedupeStopSequences(stops []string) []string {
	if len(stops) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(stops))
	for _, stop := range stops {
		stop = strings.TrimSpace(stop)
		if stop == "" {
			continue
		}
		if _, ok := seen[stop]; ok {
			continue
		}
		seen[stop] = struct{}{}
		out = append(out, stop)
	}
	return out
}

func cutAtStopSequence(text string, stopSequences []string) (string, bool) {
	if strings.TrimSpace(text) == "" || len(stopSequences) == 0 {
		return text, false
	}
	first := -1
	for _, stop := range stopSequences {
		if stop == "" {
			continue
		}
		idx := strings.Index(text, stop)
		if idx < 0 {
			continue
		}
		if first == -1 || idx < first {
			first = idx
		}
	}
	if first < 0 {
		return text, false
	}
	return text[:first], true
}

func ensureYZMALibraries(libPath string, logger *obs.Logger) error {
	yzmaLibMu.Lock()
	defer yzmaLibMu.Unlock()

	libPath = strings.TrimSpace(libPath)
	if libPath == "" {
		return fmt.Errorf("empty yzma library path")
	}

	report, reportErr := inspectYZMALibraries(libPath)
	if reportErr != nil {
		return reportErr
	}
	logYZMALibraryReport(report, logger)
	if validateErr := validateYZMALibraries(report); validateErr != nil {
		return validateErr
	}

	if yzmaLibInitialized {
		if yzmaLibPath != "" && !samePath(yzmaLibPath, libPath) {
			return fmt.Errorf("yzma libraries already initialized from %s; cannot switch to %s in this process", yzmaLibPath, libPath)
		}
		configureYZMALogging(yzmaDebugEnabled())
		if err := ensureYZMABackendsReady(libPath, logger); err != nil {
			return err
		}
		if err := verifyYZMAVersionConsistency(libPath, logger); err != nil {
			return err
		}
		return nil
	}
	if err := yzllama.Load(libPath); err != nil {
		return err
	}
	configureYZMALogging(yzmaDebugEnabled())
	yzllama.Init()
	if err := ensureYZMABackendsReady(libPath, logger); err != nil {
		return err
	}
	if err := verifyYZMAVersionConsistency(libPath, logger); err != nil {
		return err
	}
	yzmaLibInitialized = true
	yzmaLibPath = libPath
	return nil
}

func ensureYZMABackendsReady(libPath string, logger *obs.Logger) error {
	snapshot := collectYZMABackendSnapshot()
	if snapshot.Count == 0 {
		if logger != nil && logger.Enabled() {
			logger.Printf("provider=yzma backends=0 source=lib_path action=fallback_load_all lib_path=%s", libPath)
		}
		yzllama.GGMLBackendLoadAll()
		snapshot = collectYZMABackendSnapshot()
	}

	logYZMABackendSnapshot(snapshot, logger)
	if snapshot.Count == 0 {
		return &YZMAInitError{
			Stage:  "library_validation",
			Reason: "no llama.cpp backends were loaded",
			Evidence: []string{
				fmt.Sprintf("lib_path=%s", libPath),
			},
			LikelyCauses: []string{
				"incomplete or incompatible llama.cpp dylib set",
				"backend plugins failed to load from provider.lib_path",
			},
			Fix: []string{
				"verify YZMA_LIB/provider.lib_path points to a full llama.cpp build output directory",
				"ensure libggml-metal.dylib is present and compatible",
			},
		}
	}
	if runtime.GOOS == "darwin" && !containsStringFold(snapshot.RegisteredNames, "METAL") && !containsStringFold(snapshot.RegisteredNames, "MTL") {
		return &YZMAInitError{
			Stage:  "library_validation",
			Reason: "required Metal backend registration is missing",
			Evidence: []string{
				fmt.Sprintf("backend_count=%d", snapshot.Count),
				fmt.Sprintf("registered_backends=%s", strings.Join(snapshot.RegisteredNames, ",")),
			},
			LikelyCauses: []string{
				"missing libggml-metal.dylib",
				"incompatible backend plugin architecture",
			},
			Fix: []string{
				"ensure libggml-metal.dylib exists under YZMA_LIB and includes arm64",
				"use a consistent llama.cpp dylib set built for this machine",
			},
		}
	}
	if runtime.GOOS == "darwin" && (containsStringFold(snapshot.RegisteredNames, "METAL") || containsStringFold(snapshot.RegisteredNames, "MTL")) {
		if !snapshot.HasGPUDevice {
			evidence := []string{
				fmt.Sprintf("backend_count=%d", snapshot.Count),
				fmt.Sprintf("registered_backends=%s", strings.Join(snapshot.RegisteredNames, ",")),
				fmt.Sprintf("device_names=%s", strings.Join(snapshot.DeviceNames, ",")),
			}
			evidence = append(evidence, backendDeviceInventoryEvidence(snapshot)...)
			return &YZMAInitError{
				Stage:    "backend_init",
				Reason:   "Metal backend registered but no GPU device handle was exposed",
				Evidence: evidence,
				LikelyCauses: []string{
					"process cannot access a usable Metal device in the current runtime context",
					"session is headless/sandboxed and cannot create GPU command queues",
					"Metal backend library mismatch despite successful registration",
				},
				Fix: []string{
					"run `llama-cli --list-devices` in the same process environment and verify a usable GPU device appears",
					"run TOPS from a normal user terminal session (non-headless) with Metal available",
					"verify all llama.cpp dylibs come from one arm64 build in provider.lib_path",
				},
			}
		}
		if strings.TrimSpace(snapshot.GPUDevice.Name) == "" && snapshot.GPUDevice.TotalBytes == 0 {
			evidence := []string{
				fmt.Sprintf("backend_count=%d", snapshot.Count),
				fmt.Sprintf("registered_backends=%s", strings.Join(snapshot.RegisteredNames, ",")),
				fmt.Sprintf("gpu_device_handle=0x%x", snapshot.GPUDevice.Handle),
				fmt.Sprintf("gpu_device_name=%q", snapshot.GPUDevice.Name),
				fmt.Sprintf("gpu_device_memory_free=%d", snapshot.GPUDevice.FreeBytes),
				fmt.Sprintf("gpu_device_memory_total=%d", snapshot.GPUDevice.TotalBytes),
			}
			evidence = append(evidence, backendDeviceInventoryEvidence(snapshot)...)
			return &YZMAInitError{
				Stage:    "backend_init",
				Reason:   "Metal GPU device is present but unusable (empty name with zero reported memory)",
				Evidence: evidence,
				LikelyCauses: []string{
					"running in SSH session without GUI access",
					"running inside restricted terminal / container",
					"macOS Metal device not accessible to this process",
					"Rosetta/mismatched process architecture",
				},
				Fix: []string{
					"run TOPS in the same terminal context where `llama.cpp --list-devices` reports a valid Metal device name",
					"verify macOS user session has Metal access (no remote/headless restriction)",
					"rebuild and reinstall TOPS-owned llama.cpp dylibs, then rerun `tps local doctor --yzma --generate`",
				},
			}
		}
	}
	return nil
}

func inspectYZMALibraries(libPath string) (yzmaLibraryReport, error) {
	entries, err := os.ReadDir(libPath)
	if err != nil {
		return yzmaLibraryReport{}, &YZMAInitError{
			Stage:  "library_validation",
			Reason: "could not read YZMA_LIB directory",
			Evidence: []string{
				fmt.Sprintf("lib_path=%s", libPath),
				strings.TrimSpace(err.Error()),
			},
			Fix: []string{
				"set provider.lib_path or YZMA_LIB to a readable llama.cpp library directory",
			},
		}
	}

	report := yzmaLibraryReport{
		LibPath:    strings.TrimSpace(libPath),
		YZMALibEnv: strings.TrimSpace(os.Getenv("YZMA_LIB")),
		Dylibs:     make([]yzmaDylibInfo, 0, len(entries)),
	}
	libExt := sharedLibraryExtension()
	if libExt == "" {
		libExt = ".dylib"
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), strings.ToLower(libExt)) {
			continue
		}
		fullPath := filepath.Join(libPath, entry.Name())
		archs, archErr := readLibraryArchitectures(fullPath)
		if archErr != nil {
			archs = []string{fmt.Sprintf("unknown (%v)", archErr)}
		}
		report.Dylibs = append(report.Dylibs, yzmaDylibInfo{
			Name:          entry.Name(),
			Path:          fullPath,
			Architectures: archs,
		})
	}
	sort.Slice(report.Dylibs, func(i, j int) bool {
		return strings.ToLower(report.Dylibs[i].Name) < strings.ToLower(report.Dylibs[j].Name)
	})
	return report, nil
}

func validateYZMALibraries(report yzmaLibraryReport) error {
	if len(report.Dylibs) == 0 {
		return &YZMAInitError{
			Stage:  "library_validation",
			Reason: "no shared libraries found in YZMA_LIB",
			Evidence: []string{
				fmt.Sprintf("YZMA_LIB=%s", report.LibPath),
			},
			Fix: []string{
				"point provider.lib_path or YZMA_LIB at a llama.cpp binaries directory containing dylibs",
			},
		}
	}

	required := requiredCoreLibraryNames()
	missingRequired := make([]string, 0, len(required))
	for _, name := range required {
		if !report.hasDylib(name) {
			missingRequired = append(missingRequired, name)
		}
	}
	if len(missingRequired) > 0 {
		return &YZMAInitError{
			Stage:  "library_validation",
			Reason: "required llama.cpp dylibs are missing",
			Evidence: []string{
				fmt.Sprintf("missing=%s", strings.Join(missingRequired, ",")),
				fmt.Sprintf("lib_path=%s", report.LibPath),
			},
			LikelyCauses: []string{
				"incomplete llama.cpp runtime distribution",
			},
			Fix: []string{
				"reinstall or copy a complete llama.cpp dylib set into YZMA_LIB",
			},
		}
	}
	if runtime.GOOS == "darwin" && !report.hasDylib("libggml-metal.dylib") {
		return &YZMAInitError{
			Stage:  "library_validation",
			Reason: "Metal backend dylib is missing",
			Evidence: []string{
				fmt.Sprintf("missing=libggml-metal.dylib"),
				fmt.Sprintf("lib_path=%s", report.LibPath),
			},
			LikelyCauses: []string{
				"CPU-only or incomplete llama.cpp library set",
			},
			Fix: []string{
				"install the Metal-enabled llama.cpp dylib build",
			},
		}
	}

	if runtime.GOOS == "darwin" {
		nonArm64 := make([]string, 0, len(report.Dylibs))
		for _, dylib := range report.Dylibs {
			if !dylib.supportsArm64() {
				nonArm64 = append(nonArm64, fmt.Sprintf("%s [%s]", dylib.Name, strings.Join(dylib.Architectures, ",")))
			}
		}
		if len(nonArm64) > 0 {
			return &YZMAInitError{
				Stage:    "library_validation",
				Reason:   "one or more llama.cpp dylibs are not arm64-compatible",
				Evidence: append([]string{fmt.Sprintf("lib_path=%s", report.LibPath)}, nonArm64...),
				LikelyCauses: []string{
					"x86_64-only build on an arm64 runtime",
					"mixed architecture dylib directory",
				},
				Fix: []string{
					"use a consistent arm64 llama.cpp dylib set",
					"remove stale x86_64 dylibs from YZMA_LIB",
				},
			}
		}
	}
	return nil
}

func sharedLibraryExtension() string {
	switch runtime.GOOS {
	case "darwin":
		return ".dylib"
	case "linux", "freebsd":
		return ".so"
	case "windows":
		return ".dll"
	default:
		return ""
	}
}

func requiredCoreLibraryNames() []string {
	ext := sharedLibraryExtension()
	switch runtime.GOOS {
	case "windows":
		return []string{"ggml" + ext, "ggml-base" + ext, "llama" + ext}
	default:
		return []string{"libggml" + ext, "libggml-base" + ext, "libllama" + ext}
	}
}

func (r yzmaLibraryReport) hasDylib(name string) bool {
	for _, item := range r.Dylibs {
		if strings.EqualFold(strings.TrimSpace(item.Name), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

func readLibraryArchitectures(path string) ([]string, error) {
	if runtime.GOOS != "darwin" {
		return []string{runtime.GOARCH}, nil
	}
	archSet := map[string]struct{}{}
	if fat, err := macho.OpenFat(path); err == nil {
		for _, arch := range fat.Arches {
			name := machoCPUName(arch.Cpu)
			if name == "" {
				name = fmt.Sprintf("cpu_%d", arch.Cpu)
			}
			archSet[name] = struct{}{}
		}
		_ = fat.Close()
		return sortedKeys(archSet), nil
	}

	mf, err := macho.Open(path)
	if err != nil {
		return nil, err
	}
	defer mf.Close()
	name := machoCPUName(mf.Cpu)
	if name == "" {
		name = fmt.Sprintf("cpu_%d", mf.Cpu)
	}
	archSet[name] = struct{}{}
	return sortedKeys(archSet), nil
}

func machoCPUName(cpu macho.Cpu) string {
	switch cpu {
	case macho.CpuArm64:
		return "arm64"
	case macho.CpuAmd64:
		return "x86_64"
	case macho.Cpu386:
		return "x86"
	default:
		return strings.ToLower(strings.TrimSpace(fmt.Sprintf("%v", cpu)))
	}
}

func sortedKeys(items map[string]struct{}) []string {
	out := make([]string, 0, len(items))
	for key := range items {
		out = append(out, key)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i]) < strings.ToLower(out[j])
	})
	return out
}

type yzmaBackendSnapshot struct {
	Count           int
	RegisteredNames []string
	DeviceNames     []string
	Devices         []yzmaBackendDeviceInfo
	GPUDevice       yzmaBackendDeviceInfo
	HasGPUDevice    bool
}

type yzmaBackendDeviceInfo struct {
	Index      int
	Handle     uintptr
	Name       string
	FreeBytes  uint64
	TotalBytes uint64
}

func collectYZMABackendSnapshot() yzmaBackendSnapshot {
	defer func() {
		_ = recover()
	}()
	snapshot := yzmaBackendSnapshot{
		Count: int(yzllama.GGMLBackendRegCount()),
	}
	known := []string{"CPU", "METAL", "MTL", "BLAS", "CUDA", "VULKAN", "HIP", "SYCL", "OPENCL", "RPC"}
	for _, name := range known {
		if yzllama.GGMLBackendRegByName(name) != 0 {
			snapshot.RegisteredNames = append(snapshot.RegisteredNames, name)
		}
	}
	sort.Slice(snapshot.RegisteredNames, func(i, j int) bool {
		return strings.ToLower(snapshot.RegisteredNames[i]) < strings.ToLower(snapshot.RegisteredNames[j])
	})

	deviceCount := int(yzllama.GGMLBackendDeviceCount())
	for i := 0; i < deviceCount; i++ {
		dev := yzllama.GGMLBackendDeviceGet(uint64(i))
		if dev == 0 {
			continue
		}
		name := strings.TrimSpace(yzllama.GGMLBackendDeviceName(dev))
		freeBytes, totalBytes := yzllama.GGMLBackendDeviceMemory(dev)
		snapshot.Devices = append(snapshot.Devices, yzmaBackendDeviceInfo{
			Index:      i,
			Handle:     uintptr(dev),
			Name:       name,
			FreeBytes:  freeBytes,
			TotalBytes: totalBytes,
		})
		if name != "" {
			snapshot.DeviceNames = append(snapshot.DeviceNames, name)
		}
	}
	sort.Slice(snapshot.DeviceNames, func(i, j int) bool {
		return strings.ToLower(snapshot.DeviceNames[i]) < strings.ToLower(snapshot.DeviceNames[j])
	})

	gpuDevice := yzllama.GGMLBackendDeviceByType(yzllama.GGMLBackendDeviceTypeGPU)
	if gpuDevice != 0 {
		freeBytes, totalBytes := yzllama.GGMLBackendDeviceMemory(gpuDevice)
		snapshot.GPUDevice = yzmaBackendDeviceInfo{
			Index:      -1,
			Handle:     uintptr(gpuDevice),
			Name:       strings.TrimSpace(yzllama.GGMLBackendDeviceName(gpuDevice)),
			FreeBytes:  freeBytes,
			TotalBytes: totalBytes,
		}
		snapshot.HasGPUDevice = true
	}
	return snapshot
}

func applyBackendSnapshotToProbeContext(ctx *ProbeContextSnapshot, snapshot yzmaBackendSnapshot) {
	if ctx == nil {
		return
	}
	ctx.BackendCount = snapshot.Count
	ctx.BackendNames = append([]string(nil), snapshot.RegisteredNames...)
	ctx.DeviceCount = len(snapshot.Devices)
	ctx.DeviceNames = append([]string(nil), snapshot.DeviceNames...)
	if snapshot.HasGPUDevice {
		ctx.GPUDeviceName = strings.TrimSpace(snapshot.GPUDevice.Name)
		ctx.GPUDeviceMemoryFree = snapshot.GPUDevice.FreeBytes
		ctx.GPUDeviceMemoryTotal = snapshot.GPUDevice.TotalBytes
	}
}

func applyYZMARuntimeEnvSnapshot(ctx *ProbeContextSnapshot) {
	if ctx == nil {
		return
	}
	ctx.ProcessIsTTY = yzmaStdinIsTTY()
	ctx.TERM = strings.TrimSpace(os.Getenv("TERM"))
	ctx.SSHConnection = strings.TrimSpace(os.Getenv("SSH_CONNECTION"))
	ctx.TMUX = strings.TrimSpace(os.Getenv("TMUX"))
	ctx.CI = strings.TrimSpace(os.Getenv("CI"))
	ctx.Uname = yzmaCommandOutput("uname", "-a")
	ctx.CPUBrand = yzmaCommandOutput("sysctl", "-n", "machdep.cpu.brand_string")
	ctx.RosettaTranslated = yzmaRosettaStatus()
}

func yzmaStdinIsTTY() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

func yzmaCommandOutput(name string, args ...string) string {
	command := strings.TrimSpace(name)
	if command == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, command, args...).CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func yzmaRosettaStatus() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	out := yzmaCommandOutput("sysctl", "-in", "sysctl.proc_translated")
	switch strings.TrimSpace(out) {
	case "1":
		return "1"
	case "0":
		return "0"
	default:
		return ""
	}
}

func verifyYZMAVersionConsistency(libPath string, logger *obs.Logger) error {
	systemInfo := strings.TrimSpace(yzllama.PrintSystemInfo())
	if yzmaDebugEnabled() && systemInfo != "" {
		yzmaDebugf(logger, "llama_system_info=%s", systemInfo)
	}

	yzmaVersion := resolveYZMAVersion()
	llamaBuild := extractLlamaBuildVersion(systemInfo)
	metadataBuild := readLlamaBuildVersionFromMetadata(libPath)
	yzmaDebugf(logger, "version_check yzma=%s llama_runtime=%s llama_metadata=%s", safeUnknown(yzmaVersion), safeUnknown(llamaBuild), safeUnknown(metadataBuild))

	if metadataBuild != "" && llamaBuild != "" && !strings.EqualFold(metadataBuild, llamaBuild) {
		return &YZMAInitError{
			Stage:  "version_mismatch",
			Reason: "llama.cpp runtime build does not match version metadata in YZMA_LIB",
			Evidence: []string{
				fmt.Sprintf("yzma_version=%s", safeUnknown(yzmaVersion)),
				fmt.Sprintf("llama_runtime_build=%s", llamaBuild),
				fmt.Sprintf("llama_metadata_build=%s", metadataBuild),
			},
			LikelyCauses: []string{
				"mixed dylibs from different llama.cpp builds",
				"stale version.json after manual library replacement",
			},
			Fix: []string{
				"replace YZMA_LIB with a single consistent llama.cpp build directory",
				"ensure libllama/libggml/libggml-metal come from the same release build",
			},
		}
	}
	return nil
}

func resolveYZMAVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		if strings.TrimSpace(info.Main.Path) == "github.com/hybridgroup/yzma" {
			return strings.TrimSpace(info.Main.Version)
		}
		for _, dep := range info.Deps {
			if dep == nil {
				continue
			}
			if dep.Path == "github.com/hybridgroup/yzma" {
				return strings.TrimSpace(dep.Version)
			}
		}
	}
	return ""
}

func extractLlamaBuildVersion(systemInfo string) string {
	matched := yzmaVersionPattern.FindString(strings.TrimSpace(systemInfo))
	return strings.TrimSpace(matched)
}

func readLlamaBuildVersionFromMetadata(libPath string) string {
	metadataPath := filepath.Join(strings.TrimSpace(libPath), "version.json")
	raw, err := os.ReadFile(metadataPath)
	if err != nil {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	candidates := []string{
		mapString(payload, "tag_name"),
		mapString(payload, "version"),
		mapString(payload, "llama_version"),
		mapString(payload, "llama_cpp_version"),
	}
	for _, candidate := range candidates {
		if match := yzmaVersionPattern.FindString(strings.TrimSpace(candidate)); match != "" {
			return strings.TrimSpace(match)
		}
	}
	return ""
}

func mapString(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	raw, ok := values[key]
	if !ok || raw == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprintf("%v", raw))
}

func safeUnknown(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	return value
}

func copyProbeContextSnapshot(snapshot ProbeContextSnapshot) ProbeContextSnapshot {
	out := snapshot
	out.BackendNames = append([]string(nil), snapshot.BackendNames...)
	out.DeviceNames = append([]string(nil), snapshot.DeviceNames...)
	return out
}

func resolveLlamaDylibPath(libPath string) string {
	libPath = strings.TrimSpace(libPath)
	if libPath == "" {
		return ""
	}
	candidates := []string{
		filepath.Join(libPath, "libllama.dylib"),
		filepath.Join(libPath, "libllama.0.dylib"),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	matches, err := filepath.Glob(filepath.Join(libPath, "libllama*.dylib"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	sort.Strings(matches)
	return matches[0]
}

func contextParamsSnapshot(params yzllama.ContextParams) YZMAContextParamsSnapshot {
	return YZMAContextParamsSnapshot{
		NCtx:          params.NCtx,
		NBatch:        params.NBatch,
		NUbatch:       params.NUbatch,
		NSeqMax:       params.NSeqMax,
		NThreads:      params.NThreads,
		NThreadsBatch: params.NThreadsBatch,
		OffloadKQV:    params.Offload_kqv != 0,
		OpOffload:     params.OpOffload != 0,
	}
}

func appendBackendSnapshotEvidence(evidence []string, snapshot yzmaBackendSnapshot, params YZMAContextParamsSnapshot) []string {
	out := append([]string(nil), evidence...)
	appendEvidence := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		for _, existing := range out {
			if strings.EqualFold(strings.TrimSpace(existing), value) {
				return
			}
		}
		out = append(out, value)
	}

	if len(snapshot.RegisteredNames) > 0 {
		appendEvidence("backend_names=" + strings.Join(snapshot.RegisteredNames, ","))
	}
	if len(snapshot.DeviceNames) > 0 {
		appendEvidence("device_names=" + strings.Join(snapshot.DeviceNames, ","))
	}
	if snapshot.HasGPUDevice {
		appendEvidence(fmt.Sprintf("gpu_device_handle=0x%x", snapshot.GPUDevice.Handle))
		appendEvidence(fmt.Sprintf("gpu_device_name=%q", snapshot.GPUDevice.Name))
		appendEvidence(fmt.Sprintf("gpu_device_memory_free=%d", snapshot.GPUDevice.FreeBytes))
		appendEvidence(fmt.Sprintf("gpu_device_memory_total=%d", snapshot.GPUDevice.TotalBytes))
	} else {
		appendEvidence("gpu_device_handle=(none)")
	}
	for _, line := range backendDeviceInventoryEvidence(snapshot) {
		appendEvidence(line)
	}
	appendEvidence(fmt.Sprintf("context_params n_ctx=%d n_batch=%d n_ubatch=%d n_seq_max=%d n_threads=%d n_threads_batch=%d offload_kqv=%t op_offload=%t",
		params.NCtx, params.NBatch, params.NUbatch, params.NSeqMax, params.NThreads, params.NThreadsBatch, params.OffloadKQV, params.OpOffload))
	appendEvidence(fmt.Sprintf("context_params n_gpu_layers=%d", params.NGPULayers))
	return out
}

func backendDeviceInventoryEvidence(snapshot yzmaBackendSnapshot) []string {
	lines := make([]string, 0, len(snapshot.Devices))
	for _, dev := range snapshot.Devices {
		lines = append(lines, fmt.Sprintf("backend_device[%d]=handle:0x%x name:%q mem_free:%d mem_total:%d",
			dev.Index, dev.Handle, dev.Name, dev.FreeBytes, dev.TotalBytes))
	}
	return lines
}

func containsStringFold(values []string, target string) bool {
	target = strings.TrimSpace(target)
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}

func logYZMALibraryReport(report yzmaLibraryReport, logger *obs.Logger) {
	yzmaDebugf(logger, "library_probe YZMA_LIB=%s effective_lib_path=%s dylib_count=%d", safeUnknown(report.YZMALibEnv), safeUnknown(report.LibPath), len(report.Dylibs))
	for _, dylib := range report.Dylibs {
		yzmaDebugf(logger, "dylib name=%s path=%s arch=%s", dylib.Name, dylib.Path, strings.Join(dylib.Architectures, ","))
	}
}

func logYZMABackendSnapshot(snapshot yzmaBackendSnapshot, logger *obs.Logger) {
	yzmaDebugf(logger, "backend_probe backend_count=%d backend_names=%s device_names=%s", snapshot.Count, strings.Join(snapshot.RegisteredNames, ","), strings.Join(snapshot.DeviceNames, ","))
	if snapshot.HasGPUDevice {
		yzmaDebugf(logger, "backend_probe gpu_device handle=0x%x name=%q mem_free=%d mem_total=%d", snapshot.GPUDevice.Handle, snapshot.GPUDevice.Name, snapshot.GPUDevice.FreeBytes, snapshot.GPUDevice.TotalBytes)
	} else {
		yzmaDebugf(logger, "backend_probe gpu_device handle=(none)")
	}
	for _, dev := range snapshot.Devices {
		yzmaDebugf(logger, "backend_probe device[%d] handle=0x%x name=%q mem_free=%d mem_total=%d", dev.Index, dev.Handle, dev.Name, dev.FreeBytes, dev.TotalBytes)
	}
}

func yzmaDebugEnabled() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("TOPS_YZMA_DEBUG_LOG")))
	switch value {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func yzmaRawDebugEnabled() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("TOPS_YZMA_DEBUG_RAW")))
	switch value {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func yzmaDebugf(logger *obs.Logger, format string, args ...any) {
	msg := strings.TrimSpace(fmt.Sprintf(format, args...))
	if msg == "" {
		return
	}
	if yzmaDebugEnabled() {
		_, _ = fmt.Fprintf(os.Stdout, "[tops yzma] %s\n", msg)
	}
	if logger != nil && logger.Enabled() {
		logger.Printf("provider=yzma %s", msg)
	}
}

func logYZMARawRequest(kind string, prompt string, sampling yzmaSamplingConfig, stopSequences []string) {
	if !yzmaRawDebugEnabled() {
		return
	}
	_, _ = fmt.Fprintf(os.Stdout, "[tops yzma raw params][%s] profile=%s max_tokens=%d temp=%.4f top_k=%d top_p=%.4f min_p=%.4f repeat_penalty=%.4f stop_tokens=%s prompt_chars=%d\n",
		strings.TrimSpace(kind),
		strings.TrimSpace(safeUnknown(sampling.ProfileName)),
		sampling.MaxTokens,
		sampling.Temperature,
		sampling.TopK,
		sampling.TopP,
		sampling.MinP,
		sampling.RepeatPenalty,
		strings.Join(stopSequences, ","),
		len(prompt),
	)
	_, _ = fmt.Fprintf(os.Stdout, "[tops yzma raw prompt][%s]\n%s\n", strings.TrimSpace(kind), prompt)
}

func logYZMARawOutput(kind string, output string) {
	if !yzmaRawDebugEnabled() {
		return
	}
	_, _ = fmt.Fprintf(os.Stdout, "[tops yzma raw output][%s]\n%s\n", strings.TrimSpace(kind), strings.TrimSpace(output))
}

func configureYZMALogging(mirrorToStdout bool) {
	yzmaLogMu.Lock()
	defer yzmaLogMu.Unlock()

	yzmaLogMirrorToStdout = mirrorToStdout
	if yzmaLogCallback == 0 {
		yzmaLogCallback = purego.NewCallback(func(level int32, text *byte, data uintptr) uintptr {
			_ = level
			_ = data
			var line string
			if text != nil {
				line = strings.TrimSpace(yzutils.BytePtrToString(text))
			}
			if line == "" {
				return 0
			}
			yzmaLogMu.Lock()
			yzmaLogLines = append(yzmaLogLines, line)
			if len(yzmaLogLines) > yzmaLogRingCapacity {
				yzmaLogLines = append([]string(nil), yzmaLogLines[len(yzmaLogLines)-yzmaLogRingCapacity:]...)
			}
			shouldMirror := yzmaLogMirrorToStdout
			yzmaLogMu.Unlock()
			if shouldMirror {
				_, _ = fmt.Fprintf(os.Stdout, "[llama.cpp] %s\n", line)
			}
			return 0
		})
	}
	yzllama.LogSet(yzmaLogCallback)
}

func resetYZMALogBuffer() {
	yzmaLogMu.Lock()
	defer yzmaLogMu.Unlock()
	yzmaLogLines = yzmaLogLines[:0]
}

func snapshotYZMALogBuffer() []string {
	yzmaLogMu.Lock()
	defer yzmaLogMu.Unlock()
	out := make([]string, 0, len(yzmaLogLines))
	out = append(out, yzmaLogLines...)
	return out
}

func extractBackendEvidence(lines []string, primaryErr error) []string {
	out := make([]string, 0, 6)
	appendEvidence := func(text string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		for _, existing := range out {
			if strings.EqualFold(existing, text) {
				return
			}
		}
		out = append(out, text)
	}
	for _, line := range lines {
		lower := strings.ToLower(strings.TrimSpace(line))
		if lower == "" {
			continue
		}
		if strings.Contains(lower, "ggml_metal_init") ||
			strings.Contains(lower, "failed to create command queue") ||
			strings.Contains(lower, "failed to initialize backend") ||
			strings.Contains(lower, "llama_init_from_model") {
			appendEvidence(line)
		}
	}
	if primaryErr != nil {
		appendEvidence(primaryErr.Error())
	}
	if len(out) == 0 {
		appendEvidence("llama.cpp backend initialization failed without additional log evidence")
	}
	return out
}

func newYZMABackendInitError(primaryErr error, evidence []string) *YZMAInitError {
	reason := "Metal backend could not initialize"
	for _, item := range evidence {
		lower := strings.ToLower(strings.TrimSpace(item))
		switch {
		case strings.Contains(lower, "failed to create command queue"):
			reason = "failed to create Metal command queue"
		case strings.Contains(lower, "failed to initialize backend"):
			reason = "llama backend initialization failed"
		}
	}
	if strings.TrimSpace(reason) == "" && primaryErr != nil {
		reason = strings.TrimSpace(primaryErr.Error())
	}
	return &YZMAInitError{
		Stage:    "backend_init",
		Reason:   reason,
		Evidence: evidence,
		LikelyCauses: []string{
			"incompatible dylib set",
			"wrong architecture",
			"missing Metal backend",
		},
		Fix: []string{
			"verify YZMA_LIB contents",
			"ensure arm64 dylibs",
			"ensure llama.cpp version matches YZMA",
		},
	}
}

func loadYZMAContextCPUFallback(modelPath string, params yzllama.ContextParams) (yzllama.Model, yzllama.Context, error) {
	for _, name := range []string{"MTL", "METAL", "BLAS", "CUDA", "VULKAN", "HIP", "SYCL", "OPENCL", "RPC"} {
		reg := yzllama.GGMLBackendRegByName(name)
		if reg != 0 {
			yzllama.GGMLBackendUnload(reg)
		}
	}
	if yzllama.GGMLBackendRegByName("CPU") == 0 {
		yzllama.GGMLBackendLoadAll()
	}
	if yzllama.GGMLBackendRegByName("CPU") == 0 {
		return 0, 0, fmt.Errorf("cpu backend is not available after backend fallback")
	}

	modelParams := yzllama.ModelDefaultParams()
	modelParams.NGpuLayers = 0

	model, err := yzllama.ModelLoadFromFile(modelPath, modelParams)
	if err != nil {
		return 0, 0, fmt.Errorf("cpu fallback model load: %w", err)
	}
	params.Offload_kqv = 0
	params.OpOffload = 0

	ctxHandle, err := yzllama.InitFromModel(model, params)
	if err != nil {
		yzllama.ModelFree(model)
		return 0, 0, fmt.Errorf("cpu fallback context init: %w", err)
	}
	return model, ctxHandle, nil
}

func allowYZMACPUFallback() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("TOPS_YZMA_CPU_FALLBACK")))
	switch value {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// ProbeYZMAMetalBackend initializes YZMA libraries and backend registry without loading a model/context.
// This is intended for environment diagnostics (e.g. Metal availability) only.
func ProbeYZMAMetalBackend(libPath string, logger *obs.Logger) (ProbeContextSnapshot, error) {
	snapshot := ProbeContextSnapshot{
		LibPath:       strings.TrimSpace(libPath),
		YZMALibEnv:    strings.TrimSpace(os.Getenv("YZMA_LIB")),
		CPUFallback:   allowYZMACPUFallback(),
		DebugLog:      yzmaDebugEnabled(),
		DebugRaw:      yzmaRawDebugEnabled(),
		ProcessPID:    os.Getpid(),
		ProcessPPID:   os.Getppid(),
		ProcessGOOS:   runtime.GOOS,
		ProcessGOARCH: runtime.GOARCH,
	}
	applyYZMARuntimeEnvSnapshot(&snapshot)
	if snapshot.LibPath == "" {
		snapshot.LibPath = config.DefaultYZMARuntimeLibDir()
	}
	if abs, err := filepath.Abs(snapshot.LibPath); err == nil {
		snapshot.LibPath = abs
	}
	snapshot.ResolvedLlamaDylibPath = resolveLlamaDylibPath(snapshot.LibPath)
	yzmaDebugf(logger,
		"metal_probe_env pid=%d ppid=%d is_tty=%t goos=%s goarch=%s term=%s ssh=%s tmux=%s ci=%s uname=%s cpu_brand=%s rosetta=%s lib_path=%s",
		snapshot.ProcessPID,
		snapshot.ProcessPPID,
		snapshot.ProcessIsTTY,
		safeUnknown(snapshot.ProcessGOOS),
		safeUnknown(snapshot.ProcessGOARCH),
		safeUnknown(snapshot.TERM),
		safeUnknown(snapshot.SSHConnection),
		safeUnknown(snapshot.TMUX),
		safeUnknown(snapshot.CI),
		safeUnknown(snapshot.Uname),
		safeUnknown(snapshot.CPUBrand),
		safeUnknown(snapshot.RosettaTranslated),
		safeUnknown(snapshot.LibPath),
	)
	if err := ensureYZMALibraries(snapshot.LibPath, logger); err != nil {
		backendSnapshot := collectYZMABackendSnapshot()
		applyBackendSnapshotToProbeContext(&snapshot, backendSnapshot)
		return snapshot, fmt.Errorf("initialize yzma libraries failed: %w", err)
	}
	backendSnapshot := collectYZMABackendSnapshot()
	applyBackendSnapshotToProbeContext(&snapshot, backendSnapshot)
	return snapshot, nil
}

func unloadYZMALibraries() {
	yzmaLibMu.Lock()
	defer yzmaLibMu.Unlock()
	if !yzmaLibInitialized {
		return
	}
	yzllama.Close()
	yzmaLibInitialized = false
	yzmaLibPath = ""
}

func samePath(a, b string) bool {
	if a == b {
		return true
	}
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return false
	}
	return aa == bb
}

const defaultYZMAChatMLTemplate = `{% if messages[0]['role'] == 'system' %}
{% set offset = 1 %}
{% else %}
{% set offset = 0 %}
{% endif %}
{{ bos_token }}
{% for message in messages %}
{{ '<|im_start|>' + message['role'] + '\n' + message['content'] | trim + '<|im_end|>\n' }}
{% endfor %}
{% if add_generation_prompt %}
{{ '<|im_start|>assistant\n' }}
{% endif %}`

const defaultYZMAGemmaTemplate = `{{ bos_token }}{% for message in messages %}{% if message['role'] == 'assistant' %}{{ '<start_of_turn>model\n' + message['content'] | trim + '<end_of_turn>\n' }}{% else %}{{ '<start_of_turn>user\n' + message['content'] | trim + '<end_of_turn>\n' }}{% endif %}{% endfor %}{% if add_generation_prompt %}{{ '<start_of_turn>model\n' }}{% endif %}`
