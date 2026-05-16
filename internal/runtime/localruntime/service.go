package localruntime

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"tops/internal/config"
	"tops/internal/obs"
	"tops/internal/runtime/llm"
	"tops/internal/storage/modelprofile"
)

type ErrorCategory string

const (
	CategoryNone              ErrorCategory = ""
	CategoryProviderNotLocal  ErrorCategory = "provider_not_local"
	CategoryModelNotSet       ErrorCategory = "model_not_set"
	CategoryMissingModelPath  ErrorCategory = "missing_model_path"
	CategoryMissingLibPath    ErrorCategory = "missing_lib_path"
	CategoryModelPathInvalid  ErrorCategory = "model_path_invalid"
	CategoryLibPathInvalid    ErrorCategory = "lib_path_invalid"
	CategoryModelsDirInvalid  ErrorCategory = "models_dir_invalid"
	CategoryLibLoadFailed     ErrorCategory = "lib_load_failed"
	CategoryModelLoadFailed   ErrorCategory = "model_load_failed"
	CategoryBackendInitFailed ErrorCategory = "backend_init_failed"
	CategoryWarmupFailed      ErrorCategory = "warmup_failed"
	CategoryUnloadFailed      ErrorCategory = "unload_failed"
)

type ProviderFactory func(cfg config.Config, logger *obs.Logger) (llm.LLMProvider, error)

type Service struct {
	store           StateStore
	logger          *obs.Logger
	now             func() time.Time
	providerFactory ProviderFactory
}

var probeYZMAMetalBackend = llm.ProbeYZMAMetalBackend

type ValidationResult struct {
	ModelPath  string
	LibPath    string
	ModelsDir  string
	ModelsDirs []string

	ModelPathExists bool
	LibPathExists   bool
	ModelsDirExists bool

	Category ErrorCategory
	Error    error
}

type ModelEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type ModelListResult struct {
	Models     []ModelEntry `json:"models"`
	ModelsDirs []string     `json:"models_dirs,omitempty"`
	Warnings   []string     `json:"warnings,omitempty"`
}

type ModelDiscoveryResult struct {
	Entries  []ModelEntry
	Dirs     []string
	Warnings []string
}

type StatusResult struct {
	ProviderType string `json:"provider_type"`
	Model        string `json:"model"`

	ModelPath  string   `json:"model_path"`
	LibPath    string   `json:"lib_path"`
	ModelsDir  string   `json:"models_dir"`
	ModelsDirs []string `json:"models_dirs,omitempty"`

	ModelPathExists bool `json:"model_path_exists"`
	LibPathExists   bool `json:"lib_path_exists"`
	ModelsDirExists bool `json:"models_dir_exists"`

	Ready      bool          `json:"ready"`
	WarmState  string        `json:"warm_state"`
	Category   ErrorCategory `json:"category,omitempty"`
	LastError  string        `json:"last_error,omitempty"`
	LikelyFix  string        `json:"likely_fix,omitempty"`
	StatePath  string        `json:"state_path,omitempty"`
	Warnings   []string      `json:"warnings,omitempty"`
	LastWarmAt string        `json:"last_warmup_at,omitempty"`

	ActiveWarmedModel string        `json:"active_warmed_model,omitempty"`
	LastWarmupStatus  string        `json:"last_warmup_status,omitempty"`
	LastKnownError    string        `json:"last_known_error,omitempty"`
	LastErrorCategory ErrorCategory `json:"last_error_category,omitempty"`

	ProbeRan        bool          `json:"probe_ran,omitempty"`
	ProbeStatus     string        `json:"probe_status,omitempty"`
	ProbeStage      string        `json:"probe_stage,omitempty"`
	ProbeReason     string        `json:"probe_reason,omitempty"`
	ProbeCategory   ErrorCategory `json:"probe_category,omitempty"`
	ProbeSubReason  string        `json:"probe_sub_reason,omitempty"`
	ProbeHints      []string      `json:"probe_hints,omitempty"`
	ProbeEnvSignal  string        `json:"probe_environment_signal,omitempty"`
	ProbeDurationMs int64         `json:"probe_duration_ms,omitempty"`
	ProbeEvidence   []string      `json:"probe_evidence,omitempty"`
	ProbeContext    ProbeContext  `json:"probe_context,omitempty"`
}

type StatusOptions struct {
	Probe bool
}

type ActionResult struct {
	Success  bool          `json:"success"`
	Category ErrorCategory `json:"category,omitempty"`
	Messages []string      `json:"messages,omitempty"`
}

type BenchmarkPreflightResult struct {
	Ready    bool          `json:"ready"`
	Category ErrorCategory `json:"category,omitempty"`
	Message  string        `json:"message,omitempty"`
}

type ProbeResult struct {
	Status            string        `json:"status"`
	Stage             string        `json:"stage,omitempty"`
	Reason            string        `json:"reason,omitempty"`
	Category          ErrorCategory `json:"category,omitempty"`
	SubReason         string        `json:"sub_reason,omitempty"`
	Hints             []string      `json:"hints,omitempty"`
	EnvironmentSignal string        `json:"environment_signal,omitempty"`
	Evidence          []string      `json:"evidence,omitempty"`
	DurationMs        int64         `json:"duration_ms,omitempty"`
	ProbeCtx          ProbeContext  `json:"probe_context,omitempty"`
}

type ProbeContext struct {
	ModelPath              string                        `json:"model_path"`
	LibPath                string                        `json:"lib_path"`
	YZMALibEnv             string                        `json:"yzma_lib_env"`
	CPUFallback            bool                          `json:"cpu_fallback"`
	DebugLog               bool                          `json:"debug_log"`
	DebugRaw               bool                          `json:"debug_raw"`
	ProcessPID             int                           `json:"process_pid,omitempty"`
	ProcessPPID            int                           `json:"process_ppid,omitempty"`
	ProcessIsTTY           bool                          `json:"process_is_tty"`
	ProcessGOOS            string                        `json:"process_goos,omitempty"`
	ProcessGOARCH          string                        `json:"process_goarch,omitempty"`
	TERM                   string                        `json:"term,omitempty"`
	SSHConnection          string                        `json:"ssh_connection,omitempty"`
	TMUX                   string                        `json:"tmux,omitempty"`
	CI                     string                        `json:"ci,omitempty"`
	Uname                  string                        `json:"uname,omitempty"`
	CPUBrand               string                        `json:"cpu_brand,omitempty"`
	RosettaTranslated      string                        `json:"rosetta_translated,omitempty"`
	ProviderInstanceID     string                        `json:"provider_instance_id,omitempty"`
	EnsureLoadCalls        int                           `json:"ensure_load_calls,omitempty"`
	EnsureLoadReused       bool                          `json:"ensure_load_reused,omitempty"`
	LoadCount              int                           `json:"load_count,omitempty"`
	UnloadCount            int                           `json:"unload_count,omitempty"`
	LifecycleState         string                        `json:"lifecycle_state,omitempty"`
	LastCallType           string                        `json:"last_call_type,omitempty"`
	LastSamplingProfile    string                        `json:"last_sampling_profile,omitempty"`
	LastMaxTokens          int                           `json:"last_max_tokens,omitempty"`
	ResolvedLlamaDylibPath string                        `json:"resolved_llama_dylib_path,omitempty"`
	BackendCount           int                           `json:"backend_count,omitempty"`
	BackendNames           []string                      `json:"backend_names,omitempty"`
	DeviceCount            int                           `json:"device_count,omitempty"`
	DeviceNames            []string                      `json:"device_names,omitempty"`
	GPUDeviceName          string                        `json:"gpu_device_name,omitempty"`
	GPUDeviceMemoryFree    uint64                        `json:"gpu_device_memory_free,omitempty"`
	GPUDeviceMemoryTotal   uint64                        `json:"gpu_device_memory_total,omitempty"`
	ContextParams          llm.YZMAContextParamsSnapshot `json:"context_params,omitempty"`
	ProbeSource            string                        `json:"probe_source"`
}

type ProbeOptions struct {
	Source             string
	DisableCPUFallback bool
	Generate           bool
	Attempts           int
}

func NewService(logger *obs.Logger, store StateStore) Service {
	if store == nil {
		fsStore, err := NewFileStateStore("")
		if err == nil {
			store = fsStore
		}
	}
	return Service{
		store:           store,
		logger:          logger,
		now:             time.Now,
		providerFactory: defaultProviderFactory,
	}
}

func defaultProviderFactory(cfg config.Config, logger *obs.Logger) (llm.LLMProvider, error) {
	profiles, err := modelprofile.Load("")
	if err != nil {
		return nil, fmt.Errorf("load model profiles failed: %w", err)
	}
	profile, _ := profiles.Get(cfg.Provider.Type, cfg.Provider.Model)
	return llm.NewFromConfig(cfg, logger, llm.ProviderOptions{ModelProfile: profile})
}

func (s Service) Status(cfg config.Config) (StatusResult, error) {
	return s.StatusWithOptions(context.Background(), cfg, StatusOptions{})
}

func (s Service) StatusWithOptions(ctx context.Context, cfg config.Config, opts StatusOptions) (StatusResult, error) {
	cfg = normalizeConfig(cfg)
	state, err := s.loadState()
	if err != nil {
		return StatusResult{}, err
	}

	out := StatusResult{
		ProviderType:      string(cfg.Provider.Type),
		Model:             strings.TrimSpace(cfg.Provider.Model),
		StatePath:         statePathOrEmpty(s.store),
		Warnings:          append([]string(nil), cfg.Provider.MigrationWarnings...),
		ActiveWarmedModel: state.ActiveWarmedModel,
		LastWarmupStatus:  state.LastWarmupStatus,
		LastWarmAt:        state.LastSuccessfulWarmupAt,
		LastKnownError:    state.LastKnownError,
		LastErrorCategory: ErrorCategory(strings.TrimSpace(state.LastErrorCategory)),
		WarmState:         "not_ready",
	}

	if !isLocalProvider(cfg.Provider.Type) {
		out.WarmState = "provider_not_local"
		out.Category = CategoryProviderNotLocal
		out.LikelyFix = "Set provider.type to \"yzma\" and configure model_path/lib_path."
		return out, nil
	}

	validation := validatePaths(cfg)
	out.ModelPath = validation.ModelPath
	out.LibPath = validation.LibPath
	out.ModelsDir = validation.ModelsDir
	out.ModelsDirs = append([]string(nil), validation.ModelsDirs...)
	out.ModelPathExists = validation.ModelPathExists
	out.LibPathExists = validation.LibPathExists
	out.ModelsDirExists = validation.ModelsDirExists

	if strings.TrimSpace(out.Model) == "" {
		out.Category = CategoryModelNotSet
		out.LastError = "provider.model is empty"
		out.LikelyFix = "Set provider.model to a logical model identifier."
		out.WarmState = "model_not_set"
		if opts.Probe {
			out = s.applyProbeResult(ctx, cfg, out)
		}
		return out, nil
	}
	if validation.Error != nil {
		out.Category = validation.Category
		out.LastError = strings.TrimSpace(validation.Error.Error())
		out.LikelyFix = likelyFixForCategory(validation.Category)
		out.WarmState = string(validation.Category)
		if opts.Probe {
			out = s.applyProbeResult(ctx, cfg, out)
		}
		return out, nil
	}

	out.Ready = true
	if strings.EqualFold(state.LastWarmupStatus, "success") && strings.TrimSpace(state.ActiveWarmedModel) == strings.TrimSpace(cfg.Provider.Model) {
		out.WarmState = "ready"
	} else {
		out.WarmState = "ready_for_warmup"
	}
	if opts.Probe {
		out = s.applyProbeResult(ctx, cfg, out)
	}
	return out, nil
}

func (s Service) applyProbeResult(ctx context.Context, cfg config.Config, status StatusResult) StatusResult {
	probe, probeErr := s.ProbeYZMAWithOptions(ctx, cfg, ProbeOptions{
		Source:             "status_probe",
		DisableCPUFallback: true,
		Generate:           true,
		Attempts:           1,
	})
	if probeErr != nil {
		status.ProbeRan = true
		status.ProbeStatus = "failed"
		status.ProbeStage = "provider"
		status.ProbeReason = strings.TrimSpace(probeErr.Error())
		status.ProbeCategory = classifyProviderError(probeErr)
		status.ProbeContext = ProbeContext{
			ModelPath:     resolvedModelPath(cfg),
			LibPath:       resolvedLibPath(cfg),
			YZMALibEnv:    strings.TrimSpace(os.Getenv("YZMA_LIB")),
			CPUFallback:   isTruthyEnv("TOPS_YZMA_CPU_FALLBACK"),
			DebugLog:      isTruthyEnv("TOPS_YZMA_DEBUG_LOG"),
			DebugRaw:      isTruthyEnv("TOPS_YZMA_DEBUG_RAW"),
			ProcessPID:    os.Getpid(),
			ProcessPPID:   os.Getppid(),
			ProcessIsTTY:  isStdinTTY(),
			ProcessGOOS:   runtime.GOOS,
			ProcessGOARCH: runtime.GOARCH,
			TERM:          strings.TrimSpace(os.Getenv("TERM")),
			SSHConnection: strings.TrimSpace(os.Getenv("SSH_CONNECTION")),
			TMUX:          strings.TrimSpace(os.Getenv("TMUX")),
			CI:            strings.TrimSpace(os.Getenv("CI")),
			ProbeSource:   "status_probe",
		}
		status.Ready = false
		if status.Category == CategoryNone {
			status.Category = status.ProbeCategory
		}
		if strings.TrimSpace(status.LastError) == "" {
			status.LastError = status.ProbeReason
		}
		if strings.TrimSpace(status.LikelyFix) == "" {
			status.LikelyFix = likelyFixForCategory(status.ProbeCategory)
		}
		return status
	}

	status.ProbeRan = true
	status.ProbeStatus = strings.TrimSpace(probe.Status)
	status.ProbeStage = strings.TrimSpace(probe.Stage)
	status.ProbeReason = strings.TrimSpace(probe.Reason)
	status.ProbeCategory = probe.Category
	status.ProbeSubReason = strings.TrimSpace(probe.SubReason)
	status.ProbeHints = append([]string(nil), probe.Hints...)
	status.ProbeEnvSignal = strings.TrimSpace(probe.EnvironmentSignal)
	status.ProbeDurationMs = probe.DurationMs
	status.ProbeEvidence = append([]string(nil), probe.Evidence...)
	status.ProbeContext = probe.ProbeCtx

	if !strings.EqualFold(probe.Status, "ok") {
		status.Ready = false
		status.Category = probe.Category
		switch {
		case strings.TrimSpace(probe.Stage) != "" && strings.TrimSpace(probe.Reason) != "":
			status.LastError = strings.TrimSpace(probe.Stage) + ": " + strings.TrimSpace(probe.Reason)
		case strings.TrimSpace(probe.Reason) != "":
			status.LastError = strings.TrimSpace(probe.Reason)
		default:
			status.LastError = strings.TrimSpace(probe.Stage)
		}
		status.LikelyFix = likelyFixForCategory(probe.Category)
		status.WarmState = "probe_failed"
	}
	return status
}

func (s Service) ProbeYZMA(ctx context.Context, cfg config.Config) (ProbeResult, error) {
	return s.ProbeYZMAWithOptions(ctx, cfg, ProbeOptions{
		Source:             "doctor",
		DisableCPUFallback: true,
		Generate:           true,
		Attempts:           1,
	})
}

func (s Service) ProbeYZMAMetal(ctx context.Context, cfg config.Config) (ProbeResult, error) {
	_ = ctx
	started := time.Now()
	cfg = normalizeConfig(cfg)
	result := ProbeResult{
		Status: "failed",
		ProbeCtx: ProbeContext{
			ModelPath:     resolvedModelPath(cfg),
			LibPath:       resolvedLibPath(cfg),
			YZMALibEnv:    strings.TrimSpace(os.Getenv("YZMA_LIB")),
			CPUFallback:   isTruthyEnv("TOPS_YZMA_CPU_FALLBACK"),
			DebugLog:      isTruthyEnv("TOPS_YZMA_DEBUG_LOG"),
			DebugRaw:      isTruthyEnv("TOPS_YZMA_DEBUG_RAW"),
			ProcessPID:    os.Getpid(),
			ProcessPPID:   os.Getppid(),
			ProcessIsTTY:  isStdinTTY(),
			ProcessGOOS:   runtime.GOOS,
			ProcessGOARCH: runtime.GOARCH,
			TERM:          strings.TrimSpace(os.Getenv("TERM")),
			SSHConnection: strings.TrimSpace(os.Getenv("SSH_CONNECTION")),
			TMUX:          strings.TrimSpace(os.Getenv("TMUX")),
			CI:            strings.TrimSpace(os.Getenv("CI")),
			ProbeSource:   "metal_check",
		},
	}
	if !isLocalProvider(cfg.Provider.Type) {
		result.Stage = "provider"
		result.Reason = "configured provider is not local"
		result.Category = CategoryProviderNotLocal
		result.DurationMs = time.Since(started).Milliseconds()
		return result, nil
	}
	libPath := strings.TrimSpace(result.ProbeCtx.LibPath)
	if libPath == "" {
		result.Stage = "library_validation"
		result.Reason = "missing lib_path: set provider.lib_path or YZMA_LIB"
		result.Category = CategoryMissingLibPath
		result.DurationMs = time.Since(started).Milliseconds()
		return result, nil
	}

	restoreCPUFallback := forceEnvValue("TOPS_YZMA_CPU_FALLBACK", "0")
	defer restoreCPUFallback()
	result.ProbeCtx.CPUFallback = false

	snapshot, err := probeYZMAMetalBackend(libPath, s.logger)
	result.ProbeCtx = mergeProbeContext(result.ProbeCtx, snapshot, "metal_check")
	if err != nil {
		result.Stage, result.Reason, result.Evidence, result.Category = summarizeProbeError(err)
		applyProbeFailureDiagnostics(&result)
		result.DurationMs = time.Since(started).Milliseconds()
		return result, nil
	}
	result.Status = "ok"
	result.Stage = "metal_backend"
	result.Reason = "Metal backend probe succeeded"
	result.Category = CategoryNone
	result.DurationMs = time.Since(started).Milliseconds()
	return result, nil
}

func (s Service) ProbeYZMAWithOptions(ctx context.Context, cfg config.Config, opts ProbeOptions) (ProbeResult, error) {
	started := time.Now()
	cfg = normalizeConfig(cfg)
	probeSource := strings.TrimSpace(opts.Source)
	if probeSource == "" {
		probeSource = "doctor"
	}
	if opts.Attempts > 1 {
		var last ProbeResult
		for attempt := 1; attempt <= opts.Attempts; attempt++ {
			sub := opts
			sub.Attempts = 1
			sub.Source = probeSource
			res, err := s.ProbeYZMAWithOptions(ctx, cfg, sub)
			if err != nil {
				return res, err
			}
			if !strings.EqualFold(strings.TrimSpace(res.Status), "ok") {
				res.Evidence = append(res.Evidence, fmt.Sprintf("probe_attempt=%d/%d", attempt, opts.Attempts))
				return res, nil
			}
			last = res
		}
		last.DurationMs = time.Since(started).Milliseconds()
		return last, nil
	}

	result := ProbeResult{
		Status: "failed",
		ProbeCtx: ProbeContext{
			ModelPath:     resolvedModelPath(cfg),
			LibPath:       resolvedLibPath(cfg),
			YZMALibEnv:    strings.TrimSpace(os.Getenv("YZMA_LIB")),
			CPUFallback:   isTruthyEnv("TOPS_YZMA_CPU_FALLBACK"),
			DebugLog:      isTruthyEnv("TOPS_YZMA_DEBUG_LOG"),
			DebugRaw:      isTruthyEnv("TOPS_YZMA_DEBUG_RAW"),
			ProcessPID:    os.Getpid(),
			ProcessPPID:   os.Getppid(),
			ProcessIsTTY:  isStdinTTY(),
			ProcessGOOS:   runtime.GOOS,
			ProcessGOARCH: runtime.GOARCH,
			TERM:          strings.TrimSpace(os.Getenv("TERM")),
			SSHConnection: strings.TrimSpace(os.Getenv("SSH_CONNECTION")),
			TMUX:          strings.TrimSpace(os.Getenv("TMUX")),
			CI:            strings.TrimSpace(os.Getenv("CI")),
			ProbeSource:   probeSource,
		},
	}
	if strings.TrimSpace(result.ProbeCtx.LibPath) != "" {
		result.ProbeCtx.ResolvedLlamaDylibPath = filepath.Join(result.ProbeCtx.LibPath, "libllama.dylib")
	}
	if !isLocalProvider(cfg.Provider.Type) {
		result.Stage = "provider"
		result.Reason = "configured provider is not local"
		result.Category = CategoryProviderNotLocal
		result.DurationMs = time.Since(started).Milliseconds()
		return result, nil
	}

	provider, err := s.providerFactory(cfg, s.logger)
	if err != nil {
		result.Stage, result.Reason, result.Evidence, result.Category = summarizeProbeError(err)
		applyProbeFailureDiagnostics(&result)
		result.DurationMs = time.Since(started).Milliseconds()
		return result, nil
	}
	if snapshotProvider, ok := provider.(llm.ProbeContextProvider); ok {
		result.ProbeCtx = mergeProbeContext(result.ProbeCtx, snapshotProvider.ProbeContext(), probeSource)
	}

	warmer, ok := provider.(interface{ Warm(context.Context) error })
	if !ok {
		result.Stage = "provider_init"
		result.Reason = "provider does not support warm-up"
		result.Category = CategoryModelLoadFailed
		result.DurationMs = time.Since(started).Milliseconds()
		return result, nil
	}

	warmCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	restoreCPUFallback := func() {}
	if opts.DisableCPUFallback {
		restoreCPUFallback = forceEnvValue("TOPS_YZMA_CPU_FALLBACK", "0")
		result.ProbeCtx.CPUFallback = false
	}
	defer restoreCPUFallback()

	if err := warmer.Warm(warmCtx); err != nil {
		if snapshotProvider, ok := provider.(llm.ProbeContextProvider); ok {
			result.ProbeCtx = mergeProbeContext(result.ProbeCtx, snapshotProvider.ProbeContext(), probeSource)
		}
		result.Stage, result.Reason, result.Evidence, result.Category = summarizeProbeError(err)
		applyProbeFailureDiagnostics(&result)
		result.DurationMs = time.Since(started).Milliseconds()
		if lifecycle, ok := provider.(llm.LocalModelLifecycle); ok {
			_ = lifecycle.Unload(context.Background())
		}
		return result, nil
	}
	if snapshotProvider, ok := provider.(llm.ProbeContextProvider); ok {
		result.ProbeCtx = mergeProbeContext(result.ProbeCtx, snapshotProvider.ProbeContext(), probeSource)
	}

	if opts.Generate {
		completeReq := llm.CompletionRequest{
			SystemPrompt:    "You are a runtime health probe. Reply with exactly: OK",
			UserPrompt:      "Reply with exactly: OK",
			Temperature:     0,
			MaxTokens:       16,
			SamplingProfile: llm.SamplingProfileSample,
		}
		if _, completeErr := provider.Complete(warmCtx, completeReq); completeErr != nil {
			if snapshotProvider, ok := provider.(llm.ProbeContextProvider); ok {
				result.ProbeCtx = mergeProbeContext(result.ProbeCtx, snapshotProvider.ProbeContext(), probeSource)
			}
			result.Stage, result.Reason, result.Evidence, result.Category = summarizeProbeError(completeErr)
			if strings.TrimSpace(result.Stage) == "provider" || strings.TrimSpace(result.Stage) == "" {
				result.Stage = "generation_complete"
			}
			applyProbeFailureDiagnostics(&result)
			result.DurationMs = time.Since(started).Milliseconds()
			if lifecycle, ok := provider.(llm.LocalModelLifecycle); ok {
				_ = lifecycle.Unload(context.Background())
			}
			return result, nil
		}
		if snapshotProvider, ok := provider.(llm.ProbeContextProvider); ok {
			result.ProbeCtx = mergeProbeContext(result.ProbeCtx, snapshotProvider.ProbeContext(), probeSource)
		}

		if native, ok := provider.(llm.NativeToolCallingProvider); ok {
			toolReq := llm.ToolChatRequest{
				SystemPrompt:    "You are a runtime health probe. Reply with exactly: OK",
				Messages:        []llm.ChatMessage{{Role: "user", Content: "Reply with exactly: OK"}},
				Temperature:     0,
				MaxTokens:       32,
				SamplingProfile: llm.SamplingProfilePlanner,
			}
			if _, toolErr := native.ToolChat(warmCtx, toolReq, nil, nil); toolErr != nil {
				if snapshotProvider, ok := provider.(llm.ProbeContextProvider); ok {
					result.ProbeCtx = mergeProbeContext(result.ProbeCtx, snapshotProvider.ProbeContext(), probeSource)
				}
				result.Stage, result.Reason, result.Evidence, result.Category = summarizeProbeError(toolErr)
				if strings.TrimSpace(result.Stage) == "provider" || strings.TrimSpace(result.Stage) == "" {
					result.Stage = "generation_toolchat"
				}
				applyProbeFailureDiagnostics(&result)
				result.DurationMs = time.Since(started).Milliseconds()
				if lifecycle, ok := provider.(llm.LocalModelLifecycle); ok {
					_ = lifecycle.Unload(context.Background())
				}
				return result, nil
			}
			if snapshotProvider, ok := provider.(llm.ProbeContextProvider); ok {
				result.ProbeCtx = mergeProbeContext(result.ProbeCtx, snapshotProvider.ProbeContext(), probeSource)
			}
		}
	}

	if lifecycle, ok := provider.(llm.LocalModelLifecycle); ok {
		_ = lifecycle.Unload(context.Background())
	}
	if snapshotProvider, ok := provider.(llm.ProbeContextProvider); ok {
		result.ProbeCtx = mergeProbeContext(result.ProbeCtx, snapshotProvider.ProbeContext(), probeSource)
	}

	result.Status = "ok"
	if opts.Generate {
		result.Stage = "generation_path"
		result.Reason = "context initialization and generation checks succeeded"
	} else {
		result.Stage = "backend_init"
		result.Reason = "context initialization succeeded"
	}
	result.Category = CategoryNone
	result.DurationMs = time.Since(started).Milliseconds()
	return result, nil
}

func (s Service) Load(ctx context.Context, cfg config.Config) (ActionResult, error) {
	cfg = normalizeConfig(cfg)
	state, err := s.loadState()
	if err != nil {
		return ActionResult{}, err
	}

	if !isLocalProvider(cfg.Provider.Type) {
		return ActionResult{
			Success:  false,
			Category: CategoryProviderNotLocal,
			Messages: []string{
				fmt.Sprintf("Configured provider is %q.", cfg.Provider.Type),
				"`tps local load` only applies when provider is \"yzma\".",
			},
		}, nil
	}

	validation := validatePaths(cfg)
	if strings.TrimSpace(cfg.Provider.Model) == "" {
		state = s.updateFailureState(state, CategoryModelNotSet, "provider.model is empty")
		if saveErr := s.saveState(state); saveErr != nil {
			return ActionResult{}, saveErr
		}
		return ActionResult{
			Success:  false,
			Category: CategoryModelNotSet,
			Messages: []string{
				"provider.model is not configured.",
				"Set provider.model, then run `tps local load` again.",
			},
		}, nil
	}
	if validation.Error != nil {
		state = s.updateFailureState(state, validation.Category, validation.Error.Error())
		state.LastModelPath = validation.ModelPath
		state.LastLibPath = validation.LibPath
		if saveErr := s.saveState(state); saveErr != nil {
			return ActionResult{}, saveErr
		}
		return ActionResult{
			Success:  false,
			Category: validation.Category,
			Messages: []string{
				strings.TrimSpace(validation.Error.Error()),
				likelyFixForCategory(validation.Category),
			},
		}, nil
	}

	provider, err := s.providerFactory(cfg, s.logger)
	if err != nil {
		category := classifyProviderError(err)
		state = s.updateFailureState(state, category, err.Error())
		state.LastModelPath = validation.ModelPath
		state.LastLibPath = validation.LibPath
		if saveErr := s.saveState(state); saveErr != nil {
			return ActionResult{}, saveErr
		}
		return ActionResult{
			Success:  false,
			Category: category,
			Messages: []string{
				fmt.Sprintf("Failed to initialize local runtime: %s", strings.TrimSpace(err.Error())),
				likelyFixForCategory(category),
			},
		}, nil
	}

	warmer, ok := provider.(interface{ Warm(context.Context) error })
	if !ok {
		state = s.updateFailureState(state, CategoryModelLoadFailed, "provider does not support local warm-up")
		if saveErr := s.saveState(state); saveErr != nil {
			return ActionResult{}, saveErr
		}
		return ActionResult{
			Success:  false,
			Category: CategoryModelLoadFailed,
			Messages: []string{"Configured provider does not expose warm-up support."},
		}, nil
	}
	if err := warmer.Warm(ctx); err != nil {
		category := classifyProviderError(err)
		state = s.updateFailureState(state, category, err.Error())
		state.LastModelPath = validation.ModelPath
		state.LastLibPath = validation.LibPath
		if saveErr := s.saveState(state); saveErr != nil {
			return ActionResult{}, saveErr
		}
		return ActionResult{
			Success:  false,
			Category: category,
			Messages: []string{
				fmt.Sprintf("Local runtime warm-up failed: %s", strings.TrimSpace(err.Error())),
				likelyFixForCategory(category),
			},
		}, nil
	}

	state.ActiveWarmedModel = strings.TrimSpace(cfg.Provider.Model)
	state.LastSuccessfulWarmupAt = s.now().UTC().Format(time.RFC3339)
	state.LastWarmupStatus = "success"
	state.LastKnownError = ""
	state.LastErrorCategory = ""
	state.LastModelPath = validation.ModelPath
	state.LastLibPath = validation.LibPath
	state.UpdatedAt = state.LastSuccessfulWarmupAt
	if err := s.saveState(state); err != nil {
		return ActionResult{}, err
	}

	return ActionResult{
		Success:  true,
		Category: CategoryNone,
		Messages: []string{
			fmt.Sprintf("Local runtime warmed model %q.", cfg.Provider.Model),
			fmt.Sprintf("model_path: %s", validation.ModelPath),
			fmt.Sprintf("lib_path: %s", validation.LibPath),
		},
	}, nil
}

func (s Service) Unload(ctx context.Context, cfg config.Config) (ActionResult, error) {
	cfg = normalizeConfig(cfg)
	state, err := s.loadState()
	if err != nil {
		return ActionResult{}, err
	}
	if !isLocalProvider(cfg.Provider.Type) {
		return ActionResult{
			Success:  false,
			Category: CategoryProviderNotLocal,
			Messages: []string{
				fmt.Sprintf("Configured provider is %q.", cfg.Provider.Type),
				"`tps local unload` only applies when provider is \"yzma\".",
			},
		}, nil
	}

	var unloadErr error
	provider, err := s.providerFactory(cfg, s.logger)
	if err == nil {
		if lifecycle, ok := provider.(llm.LocalModelLifecycle); ok {
			unloadErr = lifecycle.Unload(ctx)
		}
	}

	state.ActiveWarmedModel = ""
	state.LastWarmupStatus = "failure"
	state.LastKnownError = ""
	state.LastErrorCategory = ""
	state.UpdatedAt = s.now().UTC().Format(time.RFC3339)
	if saveErr := s.saveState(state); saveErr != nil {
		return ActionResult{}, saveErr
	}

	if unloadErr != nil {
		return ActionResult{
			Success:  false,
			Category: CategoryUnloadFailed,
			Messages: []string{
				fmt.Sprintf("Runtime unload failed: %s", strings.TrimSpace(unloadErr.Error())),
			},
		}, nil
	}
	return ActionResult{
		Success:  true,
		Category: CategoryNone,
		Messages: []string{
			"Cleared local warm state.",
		},
	}, nil
}

func (s Service) ListModels(cfg config.Config) ([]ModelEntry, error) {
	result, err := s.ListModelsDetailed(cfg)
	if err != nil {
		return nil, err
	}
	return result.Models, nil
}

func (s Service) ListModelsDetailed(cfg config.Config) (ModelListResult, error) {
	cfg = normalizeConfig(cfg)
	if !isLocalProvider(cfg.Provider.Type) {
		return ModelListResult{}, fmt.Errorf("configuration error: /models is only available for yzma provider")
	}
	if err := ensureDefaultModelsDirExists(); err != nil {
		return ModelListResult{}, fmt.Errorf("%s: failed to prepare default models_dir: %w", CategoryModelsDirInvalid, err)
	}
	discovery, err := DiscoverModelsAcrossDirs(resolvedModelsDirs(cfg))
	if err != nil {
		return ModelListResult{}, fmt.Errorf("%s: %w", CategoryModelsDirInvalid, err)
	}
	return ModelListResult{
		Models:     discovery.Entries,
		ModelsDirs: append([]string(nil), discovery.Dirs...),
		Warnings:   append([]string(nil), discovery.Warnings...),
	}, nil
}

func (s Service) BenchmarkPreflight(ctx context.Context, cfg config.Config, workflow string) (BenchmarkPreflightResult, error) {
	cfg = normalizeConfig(cfg)
	workflow = strings.ToLower(strings.TrimSpace(workflow))
	if !isLocalProvider(cfg.Provider.Type) {
		return BenchmarkPreflightResult{Ready: true}, nil
	}
	if workflow != "ask" && workflow != "gen" {
		return BenchmarkPreflightResult{Ready: true}, nil
	}

	if strings.TrimSpace(cfg.Provider.Model) == "" {
		return BenchmarkPreflightResult{
			Ready:    false,
			Category: CategoryModelNotSet,
			Message:  "provider.model is empty; set provider.model and retry benchmark.",
		}, nil
	}

	validation := validatePaths(cfg)
	if validation.Error != nil {
		return BenchmarkPreflightResult{
			Ready:    false,
			Category: validation.Category,
			Message:  strings.TrimSpace(validation.Error.Error()),
		}, nil
	}

	provider, err := s.providerFactory(cfg, s.logger)
	if err != nil {
		category := classifyProviderError(err)
		return BenchmarkPreflightResult{
			Ready:    false,
			Category: category,
			Message:  fmt.Sprintf("local runtime init failed: %s", strings.TrimSpace(err.Error())),
		}, nil
	}

	warmer, ok := provider.(interface{ Warm(context.Context) error })
	if !ok {
		return BenchmarkPreflightResult{
			Ready:    false,
			Category: CategoryModelLoadFailed,
			Message:  "configured provider does not support local warm-up",
		}, nil
	}

	loadCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if err := warmer.Warm(loadCtx); err != nil {
		category := classifyProviderError(err)
		return BenchmarkPreflightResult{
			Ready:    false,
			Category: category,
			Message:  fmt.Sprintf("local runtime warm-up failed: %s", strings.TrimSpace(err.Error())),
		}, nil
	}

	return BenchmarkPreflightResult{Ready: true}, nil
}

func DiscoverModels(modelsDir string) ([]ModelEntry, error) {
	result, err := DiscoverModelsAcrossDirs([]string{modelsDir})
	if err != nil {
		return nil, err
	}
	return result.Entries, nil
}

func DiscoverModelsAcrossDirs(modelsDirs []string) (ModelDiscoveryResult, error) {
	normalizedDirs := normalizeModelsDirs(modelsDirs)
	if len(normalizedDirs) == 0 {
		return ModelDiscoveryResult{}, fmt.Errorf("models_dirs is empty")
	}

	warnings := make([]string, 0, len(normalizedDirs))
	entries := make([]ModelEntry, 0, 32)
	seenPaths := map[string]struct{}{}

	for _, root := range normalizedDirs {
		info, err := os.Stat(root)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("models_dir %q is not readable: %s", root, strings.TrimSpace(err.Error())))
			continue
		}
		if !info.IsDir() {
			warnings = append(warnings, fmt.Sprintf("models_dir %q is not a directory", root))
			continue
		}
		dirEntries, dirWarnings := discoverModelsInDir(root)
		warnings = append(warnings, dirWarnings...)
		for _, entry := range dirEntries {
			key := strings.ToLower(strings.TrimSpace(entry.Path))
			if key == "" {
				continue
			}
			if _, exists := seenPaths[key]; exists {
				continue
			}
			seenPaths[key] = struct{}{}
			entries = append(entries, entry)
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		if strings.EqualFold(entries[i].Name, entries[j].Name) {
			return entries[i].Path < entries[j].Path
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})

	return ModelDiscoveryResult{
		Entries:  entries,
		Dirs:     append([]string(nil), normalizedDirs...),
		Warnings: warnings,
	}, nil
}

func discoverModelsInDir(root string) ([]ModelEntry, []string) {
	entries := make([]ModelEntry, 0, 16)
	warnings := make([]string, 0, 2)

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			warnings = append(warnings, fmt.Sprintf("scan warning under %q: %s", root, strings.TrimSpace(walkErr.Error())))
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(d.Name()), ".gguf") {
			return nil
		}
		modelPath := path
		if absPath, err := filepath.Abs(path); err == nil {
			modelPath = absPath
		}
		name := strings.TrimSuffix(d.Name(), filepath.Ext(d.Name()))
		entries = append(entries, ModelEntry{Name: name, Path: modelPath})
		return nil
	})
	if walkErr != nil {
		warnings = append(warnings, fmt.Sprintf("scan warning under %q: %s", root, strings.TrimSpace(walkErr.Error())))
	}
	return entries, warnings
}

func normalizeModelsDirs(modelsDirs []string) []string {
	out := make([]string, 0, len(modelsDirs)+1)
	seen := map[string]struct{}{}
	defaultDir := canonicalizePath(defaultModelsDir())
	if defaultDir != "" {
		out = append(out, defaultDir)
		seen[strings.ToLower(defaultDir)] = struct{}{}
	}
	for _, raw := range modelsDirs {
		path := canonicalizePath(raw)
		if path == "" {
			continue
		}
		key := strings.ToLower(path)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, path)
	}
	return out
}

func likelyFixForCategory(category ErrorCategory) string {
	switch category {
	case CategoryMissingModelPath:
		return "Set provider.model_path to an existing .gguf file."
	case CategoryMissingLibPath:
		return "Set provider.lib_path or YZMA_LIB to a directory with llama.cpp shared libraries."
	case CategoryModelPathInvalid:
		return "Fix provider.model_path so it points to a readable .gguf file."
	case CategoryLibPathInvalid:
		return "Fix provider.lib_path (or YZMA_LIB) so it points to a readable library directory."
	case CategoryLibLoadFailed:
		return "Verify provider.lib_path matches your platform build of llama.cpp shared libraries."
	case CategoryBackendInitFailed:
		return "Run `tps local doctor --yzma` and fix Metal/backend compatibility issues reported in evidence."
	case CategoryModelLoadFailed:
		return "Verify model_path points to a valid GGUF file compatible with your runtime."
	default:
		return ""
	}
}

func classifyProviderError(err error) ErrorCategory {
	if err == nil {
		return CategoryNone
	}
	if yzmaErr, ok := llm.AsYZMAInitError(err); ok {
		switch strings.ToLower(strings.TrimSpace(yzmaErr.Stage)) {
		case "library_validation", "version_mismatch":
			return CategoryLibLoadFailed
		case "backend_init":
			return CategoryBackendInitFailed
		}
	}
	lower := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.Contains(lower, "missing model_path"):
		return CategoryMissingModelPath
	case strings.Contains(lower, "missing lib_path"):
		return CategoryMissingLibPath
	case strings.Contains(lower, "model_path is not readable"):
		return CategoryModelPathInvalid
	case strings.Contains(lower, "lib_path is not readable"):
		return CategoryLibPathInvalid
	case strings.Contains(lower, "initialize yzma libraries failed"):
		return CategoryLibLoadFailed
	case strings.Contains(lower, "backend_init_failed"),
		strings.Contains(lower, "failed to create metal command queue"):
		return CategoryBackendInitFailed
	case strings.Contains(lower, "load yzma model"),
		strings.Contains(lower, "create yzma context"),
		strings.Contains(lower, "provider initialization failed"):
		return CategoryModelLoadFailed
	default:
		return CategoryWarmupFailed
	}
}

func summarizeProbeError(err error) (stage string, reason string, evidence []string, category ErrorCategory) {
	if err == nil {
		return "ready", "probe succeeded", nil, CategoryNone
	}
	if yzmaErr, ok := llm.AsYZMAInitError(err); ok {
		stage = strings.TrimSpace(yzmaErr.Stage)
		reason = strings.TrimSpace(yzmaErr.Reason)
		evidence = append([]string(nil), yzmaErr.Evidence...)
		category = classifyProviderError(err)
		if stage == "" {
			stage = "provider"
		}
		if reason == "" {
			reason = strings.TrimSpace(err.Error())
		}
		return stage, reason, evidence, category
	}
	stage = "provider"
	reason = strings.TrimSpace(err.Error())
	category = classifyProviderError(err)
	return stage, reason, nil, category
}

func applyProbeFailureDiagnostics(result *ProbeResult) {
	if result == nil {
		return
	}
	subReason, hints, envSignal := classifyProbeFailureDetails(result.Stage, result.Reason, result.Evidence, result.ProbeCtx)
	result.SubReason = strings.TrimSpace(subReason)
	result.Hints = append([]string(nil), hints...)
	result.EnvironmentSignal = strings.TrimSpace(envSignal)
}

func classifyProbeFailureDetails(stage string, reason string, evidence []string, ctx ProbeContext) (string, []string, string) {
	stage = strings.ToLower(strings.TrimSpace(stage))
	reasonLower := strings.ToLower(strings.TrimSpace(reason))
	if stage != "backend_init" {
		return "", nil, ""
	}
	if !(strings.Contains(reasonLower, "metal gpu device is present but unusable") || hasEmptyZeroMetalDeviceEvidence(evidence)) {
		return "", nil, ""
	}
	hints := []string{
		"running in SSH session without GUI access",
		"running inside restricted terminal / container",
		"macOS Metal device not accessible to this process",
		"Rosetta/mismatched process architecture",
	}
	if strings.TrimSpace(ctx.SSHConnection) != "" {
		hints = append(hints, "SSH_CONNECTION is set; run from a local GUI terminal session")
	}
	if !ctx.ProcessIsTTY {
		hints = append(hints, "stdin is not a TTY in this session")
	}
	if strings.TrimSpace(ctx.RosettaTranslated) == "1" {
		hints = append(hints, "process appears to be running under Rosetta translation")
	}
	return "metal_device_unusable", dedupeStrings(hints), "Metal is not available in this runtime session. This is an environment limitation, not a build/runtime bug."
}

func hasEmptyZeroMetalDeviceEvidence(evidence []string) bool {
	seenName := false
	seenMemory := false
	for _, line := range evidence {
		lower := strings.ToLower(strings.TrimSpace(line))
		if lower == "" {
			continue
		}
		if strings.Contains(lower, `gpu_device_name=""`) || strings.Contains(lower, `name:""`) {
			seenName = true
		}
		if strings.Contains(lower, "gpu_device_memory_total=0") || strings.Contains(lower, "mem_total:0") {
			seenMemory = true
		}
		if seenName && seenMemory {
			return true
		}
	}
	return false
}

func dedupeStrings(items []string) []string {
	out := make([]string, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		key := strings.ToLower(item)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	return out
}

func mergeProbeContext(base ProbeContext, snapshot llm.ProbeContextSnapshot, source string) ProbeContext {
	out := base
	if strings.TrimSpace(snapshot.ModelPath) != "" {
		out.ModelPath = strings.TrimSpace(snapshot.ModelPath)
	}
	if strings.TrimSpace(snapshot.LibPath) != "" {
		out.LibPath = strings.TrimSpace(snapshot.LibPath)
	}
	if strings.TrimSpace(snapshot.YZMALibEnv) != "" {
		out.YZMALibEnv = strings.TrimSpace(snapshot.YZMALibEnv)
	}
	out.CPUFallback = snapshot.CPUFallback
	out.DebugLog = snapshot.DebugLog
	out.DebugRaw = snapshot.DebugRaw
	out.ProcessPID = snapshot.ProcessPID
	out.ProcessPPID = snapshot.ProcessPPID
	out.ProcessIsTTY = snapshot.ProcessIsTTY
	if strings.TrimSpace(snapshot.ProcessGOOS) != "" {
		out.ProcessGOOS = strings.TrimSpace(snapshot.ProcessGOOS)
	}
	if strings.TrimSpace(snapshot.ProcessGOARCH) != "" {
		out.ProcessGOARCH = strings.TrimSpace(snapshot.ProcessGOARCH)
	}
	if strings.TrimSpace(snapshot.TERM) != "" {
		out.TERM = strings.TrimSpace(snapshot.TERM)
	}
	if strings.TrimSpace(snapshot.SSHConnection) != "" {
		out.SSHConnection = strings.TrimSpace(snapshot.SSHConnection)
	}
	if strings.TrimSpace(snapshot.TMUX) != "" {
		out.TMUX = strings.TrimSpace(snapshot.TMUX)
	}
	if strings.TrimSpace(snapshot.CI) != "" {
		out.CI = strings.TrimSpace(snapshot.CI)
	}
	if strings.TrimSpace(snapshot.Uname) != "" {
		out.Uname = strings.TrimSpace(snapshot.Uname)
	}
	if strings.TrimSpace(snapshot.CPUBrand) != "" {
		out.CPUBrand = strings.TrimSpace(snapshot.CPUBrand)
	}
	if strings.TrimSpace(snapshot.RosettaTranslated) != "" {
		out.RosettaTranslated = strings.TrimSpace(snapshot.RosettaTranslated)
	}
	if strings.TrimSpace(snapshot.ProviderInstanceID) != "" {
		out.ProviderInstanceID = strings.TrimSpace(snapshot.ProviderInstanceID)
	}
	out.EnsureLoadCalls = snapshot.EnsureLoadCalls
	out.EnsureLoadReused = snapshot.EnsureLoadReused
	out.LoadCount = snapshot.LoadCount
	out.UnloadCount = snapshot.UnloadCount
	if strings.TrimSpace(snapshot.LifecycleState) != "" {
		out.LifecycleState = strings.TrimSpace(snapshot.LifecycleState)
	}
	if strings.TrimSpace(snapshot.LastCallType) != "" {
		out.LastCallType = strings.TrimSpace(snapshot.LastCallType)
	}
	if strings.TrimSpace(snapshot.LastSamplingProfile) != "" {
		out.LastSamplingProfile = strings.TrimSpace(snapshot.LastSamplingProfile)
	}
	if snapshot.LastMaxTokens != 0 {
		out.LastMaxTokens = snapshot.LastMaxTokens
	}
	if strings.TrimSpace(snapshot.ResolvedLlamaDylibPath) != "" {
		out.ResolvedLlamaDylibPath = strings.TrimSpace(snapshot.ResolvedLlamaDylibPath)
	}
	out.BackendCount = snapshot.BackendCount
	out.BackendNames = append([]string(nil), snapshot.BackendNames...)
	out.DeviceCount = snapshot.DeviceCount
	out.DeviceNames = append([]string(nil), snapshot.DeviceNames...)
	out.GPUDeviceName = strings.TrimSpace(snapshot.GPUDeviceName)
	out.GPUDeviceMemoryFree = snapshot.GPUDeviceMemoryFree
	out.GPUDeviceMemoryTotal = snapshot.GPUDeviceMemoryTotal
	out.ContextParams = snapshot.ContextParams
	out.ProbeSource = strings.TrimSpace(source)
	if out.ProbeSource == "" {
		out.ProbeSource = "doctor"
	}
	return out
}

func isTruthyEnv(key string) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(strings.TrimSpace(key))))
	switch value {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func isStdinTTY() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

func forceEnvValue(key string, value string) func() {
	key = strings.TrimSpace(key)
	if key == "" {
		return func() {}
	}
	current, hadValue := os.LookupEnv(key)
	_ = os.Setenv(key, strings.TrimSpace(value))
	return func() {
		if !hadValue {
			_ = os.Unsetenv(key)
			return
		}
		_ = os.Setenv(key, current)
	}
}

func validatePaths(cfg config.Config) ValidationResult {
	modelsDirs := resolvedModelsDirs(cfg)
	modelsDir := ""
	if len(modelsDirs) > 0 {
		modelsDir = modelsDirs[0]
	}
	out := ValidationResult{
		ModelPath:  resolvedModelPath(cfg),
		LibPath:    resolvedLibPath(cfg),
		ModelsDir:  modelsDir,
		ModelsDirs: append([]string(nil), modelsDirs...),
	}

	if strings.TrimSpace(out.ModelPath) == "" {
		out.Category = CategoryMissingModelPath
		out.Error = fmt.Errorf("missing model_path: set provider.model_path to a GGUF file")
		return out
	}
	if strings.TrimSpace(out.LibPath) == "" {
		out.Category = CategoryMissingLibPath
		out.Error = fmt.Errorf("missing lib_path: set provider.lib_path or YZMA_LIB")
		return out
	}

	modelInfo, modelErr := os.Stat(out.ModelPath)
	if modelErr != nil {
		out.Category = CategoryModelPathInvalid
		out.Error = fmt.Errorf("model_path %q is not readable: %w", out.ModelPath, modelErr)
		return out
	}
	if modelInfo.IsDir() {
		out.Category = CategoryModelPathInvalid
		out.Error = fmt.Errorf("model_path %q must be a file, not a directory", out.ModelPath)
		return out
	}
	out.ModelPathExists = true

	libInfo, libErr := os.Stat(out.LibPath)
	if libErr != nil {
		out.Category = CategoryLibPathInvalid
		out.Error = fmt.Errorf("lib_path %q is not readable: %w", out.LibPath, libErr)
		return out
	}
	if !libInfo.IsDir() {
		out.Category = CategoryLibPathInvalid
		out.Error = fmt.Errorf("lib_path %q must be a directory", out.LibPath)
		return out
	}
	out.LibPathExists = true

	if strings.TrimSpace(out.ModelsDir) != "" {
		if modelsInfo, err := os.Stat(out.ModelsDir); err == nil && modelsInfo.IsDir() {
			out.ModelsDirExists = true
		}
	}
	return out
}

func normalizeConfig(cfg config.Config) config.Config {
	out := cfg
	switch out.Provider.Type {
	case config.ProviderOllama, config.ProviderLocal:
		out.Provider.Type = config.ProviderYZMA
	}
	if strings.TrimSpace(out.Provider.ModelPath) == "" {
		out.Provider.ModelPath = strings.TrimSpace(out.Provider.LocalModel)
	}
	if strings.TrimSpace(out.Provider.LibPath) == "" {
		out.Provider.LibPath = strings.TrimSpace(os.Getenv("YZMA_LIB"))
	}
	if err := out.ApplyDefaults(); err == nil {
		return out
	}
	if len(out.Provider.ModelsDirs) == 0 {
		out.Provider.ModelsDirs = []string{defaultModelsDir()}
	}
	if strings.TrimSpace(out.Provider.ModelsDir) == "" && len(out.Provider.ModelsDirs) > 0 {
		out.Provider.ModelsDir = out.Provider.ModelsDirs[0]
	}
	return out
}

func resolvedModelPath(cfg config.Config) string {
	value := strings.TrimSpace(cfg.Provider.ModelPath)
	if value == "" {
		value = strings.TrimSpace(cfg.Provider.LocalModel)
	}
	if value == "" {
		return ""
	}
	if abs, err := filepath.Abs(value); err == nil {
		return abs
	}
	return value
}

func resolvedLibPath(cfg config.Config) string {
	value := strings.TrimSpace(cfg.Provider.LibPath)
	if value == "" {
		value = strings.TrimSpace(os.Getenv("YZMA_LIB"))
	}
	if value == "" {
		value = config.DefaultYZMARuntimeLibDir()
	}
	if value == "" {
		return ""
	}
	if abs, err := filepath.Abs(value); err == nil {
		return abs
	}
	return value
}

func resolvedModelsDir(cfg config.Config) string {
	modelsDirs := resolvedModelsDirs(cfg)
	if len(modelsDirs) == 0 {
		return ""
	}
	return modelsDirs[0]
}

func resolvedModelsDirs(cfg config.Config) []string {
	modelsDirs := make([]string, 0, len(cfg.Provider.ModelsDirs)+1)
	modelsDirs = append(modelsDirs, cfg.Provider.ModelsDirs...)
	if len(modelsDirs) == 0 && strings.TrimSpace(cfg.Provider.ModelsDir) != "" {
		modelsDirs = append(modelsDirs, strings.TrimSpace(cfg.Provider.ModelsDir))
	}
	return normalizeModelsDirs(modelsDirs)
}

func defaultModelsDir() string {
	return canonicalizePath(config.DefaultModelsDir())
}

func ensureDefaultModelsDirExists() error {
	defaultDir := defaultModelsDir()
	if strings.TrimSpace(defaultDir) == "" {
		return fmt.Errorf("default models_dir is empty")
	}
	return os.MkdirAll(defaultDir, 0o755)
}

func canonicalizePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "~/") || path == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			if path == "~" {
				path = home
			} else {
				path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
			}
		}
	}
	path = filepath.Clean(path)
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

func isLocalProvider(provider config.ProviderType) bool {
	switch provider {
	case config.ProviderYZMA, config.ProviderOllama, config.ProviderLocal:
		return true
	default:
		return false
	}
}

func statePathOrEmpty(store StateStore) string {
	if store == nil {
		return ""
	}
	return strings.TrimSpace(store.Path())
}

func (s Service) loadState() (RuntimeState, error) {
	if s.store == nil {
		return RuntimeState{}, nil
	}
	state, err := s.store.Load()
	if err != nil {
		return RuntimeState{}, fmt.Errorf("load local runtime state: %w", err)
	}
	return state, nil
}

func (s Service) saveState(state RuntimeState) error {
	if s.store == nil {
		return nil
	}
	if err := s.store.Save(state); err != nil {
		return fmt.Errorf("save local runtime state: %w", err)
	}
	return nil
}

func (s Service) updateFailureState(state RuntimeState, category ErrorCategory, detail string) RuntimeState {
	state.ActiveWarmedModel = ""
	state.LastWarmupStatus = "failure"
	state.LastKnownError = strings.TrimSpace(detail)
	state.LastErrorCategory = string(category)
	state.UpdatedAt = s.now().UTC().Format(time.RFC3339)
	return state
}
