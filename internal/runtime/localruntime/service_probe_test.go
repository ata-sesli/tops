package localruntime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tops/internal/config"
	"tops/internal/obs"
	"tops/internal/runtime/llm"
)

type probeTestStore struct{}

func (probeTestStore) Load() (RuntimeState, error)   { return RuntimeState{}, nil }
func (probeTestStore) Save(state RuntimeState) error { return nil }
func (probeTestStore) Path() string                  { return "" }

type fakeWarmProvider struct {
	warmErr error
}

func (f fakeWarmProvider) Name() string { return "yzma" }
func (f fakeWarmProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{}, nil
}
func (f fakeWarmProvider) Warm(ctx context.Context) error   { return f.warmErr }
func (f fakeWarmProvider) Unload(ctx context.Context) error { return nil }

type fakeGenerationFailProvider struct {
	completeErr error
}

func (f fakeGenerationFailProvider) Name() string { return "yzma" }
func (f fakeGenerationFailProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	if f.completeErr != nil {
		return llm.CompletionResponse{}, f.completeErr
	}
	return llm.CompletionResponse{Content: "OK"}, nil
}
func (f fakeGenerationFailProvider) Warm(ctx context.Context) error   { return nil }
func (f fakeGenerationFailProvider) Unload(ctx context.Context) error { return nil }

type fakeNativeToolChatFailProvider struct {
	toolChatErr error
}

func (f fakeNativeToolChatFailProvider) Name() string { return "yzma" }
func (f fakeNativeToolChatFailProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{Content: "OK"}, nil
}
func (f fakeNativeToolChatFailProvider) Warm(ctx context.Context) error { return nil }
func (f fakeNativeToolChatFailProvider) Unload(ctx context.Context) error {
	return nil
}
func (f fakeNativeToolChatFailProvider) ToolChat(ctx context.Context, req llm.ToolChatRequest, onThinking func(string), onResponse func(string)) (llm.ToolChatResponse, error) {
	if f.toolChatErr != nil {
		return llm.ToolChatResponse{}, f.toolChatErr
	}
	return llm.ToolChatResponse{Content: "OK"}, nil
}

func testLocalConfig(t *testing.T) config.Config {
	t.Helper()
	root := t.TempDir()
	modelPath := filepath.Join(root, "model.gguf")
	libPath := filepath.Join(root, "libs")
	if err := os.WriteFile(modelPath, []byte("gguf"), 0o644); err != nil {
		t.Fatalf("write model: %v", err)
	}
	if err := os.MkdirAll(libPath, 0o755); err != nil {
		t.Fatalf("mkdir lib path: %v", err)
	}
	cfg := config.Config{
		Provider: config.ProviderConfig{
			Type:      config.ProviderYZMA,
			Model:     "gemma4:e4b",
			ModelPath: modelPath,
			LibPath:   libPath,
		},
		Shell:      "zsh",
		Output:     config.OutputConfig{Format: "text"},
		Inspection: config.InspectionConfig{TimeoutSeconds: 10},
	}
	if err := cfg.ApplyDefaults(); err != nil {
		t.Fatalf("apply defaults: %v", err)
	}
	return cfg
}

func TestProbeYZMABackendInitFailure(t *testing.T) {
	cfg := testLocalConfig(t)
	t.Setenv("YZMA_LIB", "/tmp/yzma-env-libs")
	t.Setenv("TOPS_YZMA_CPU_FALLBACK", "1")
	t.Setenv("TOPS_YZMA_DEBUG_LOG", "1")
	svc := Service{
		store: probeTestStore{},
		now:   time.Now,
		providerFactory: func(cfg config.Config, logger *obs.Logger) (llm.LLMProvider, error) {
			return fakeWarmProvider{
				warmErr: fmt.Errorf("create yzma context: %w", &llm.YZMAInitError{
					Stage:    "backend_init",
					Reason:   "failed to create Metal command queue",
					Evidence: []string{"ggml_metal_init: failed to create command queue"},
				}),
			}, nil
		},
	}
	result, err := svc.ProbeYZMA(context.Background(), cfg)
	if err != nil {
		t.Fatalf("probe yzma: %v", err)
	}
	if result.Status != "failed" {
		t.Fatalf("expected failed status, got %q", result.Status)
	}
	if result.Stage != "backend_init" {
		t.Fatalf("expected backend_init stage, got %q", result.Stage)
	}
	if result.Category != CategoryBackendInitFailed {
		t.Fatalf("expected backend init category, got %q", result.Category)
	}
	if len(result.Evidence) == 0 {
		t.Fatal("expected backend evidence to be populated")
	}
	if strings.TrimSpace(result.ProbeCtx.ModelPath) != strings.TrimSpace(cfg.Provider.ModelPath) {
		t.Fatalf("expected probe context model_path %q, got %q", cfg.Provider.ModelPath, result.ProbeCtx.ModelPath)
	}
	if strings.TrimSpace(result.ProbeCtx.LibPath) != strings.TrimSpace(cfg.Provider.LibPath) {
		t.Fatalf("expected probe context lib_path %q, got %q", cfg.Provider.LibPath, result.ProbeCtx.LibPath)
	}
	if strings.TrimSpace(result.ProbeCtx.YZMALibEnv) != "/tmp/yzma-env-libs" {
		t.Fatalf("unexpected yzma_lib_env: %q", result.ProbeCtx.YZMALibEnv)
	}
	if result.ProbeCtx.CPUFallback {
		t.Fatal("expected probe context cpu_fallback=false for doctor probe")
	}
	if !result.ProbeCtx.DebugLog {
		t.Fatal("expected probe context debug_log=true from env")
	}
	if result.ProbeCtx.ProbeSource != "doctor" {
		t.Fatalf("expected probe source doctor, got %q", result.ProbeCtx.ProbeSource)
	}
}

func TestStatusWithProbeOverridesReadyWhenProbeFails(t *testing.T) {
	cfg := testLocalConfig(t)
	svc := Service{
		store: probeTestStore{},
		now:   time.Now,
		providerFactory: func(cfg config.Config, logger *obs.Logger) (llm.LLMProvider, error) {
			return fakeWarmProvider{
				warmErr: fmt.Errorf("create yzma context: %w", &llm.YZMAInitError{
					Stage:    "backend_init",
					Reason:   "Metal backend could not initialize",
					Evidence: []string{"llama_init_from_model: failed to initialize backend"},
				}),
			}, nil
		},
	}
	status, err := svc.StatusWithOptions(context.Background(), cfg, StatusOptions{Probe: true})
	if err != nil {
		t.Fatalf("status with probe: %v", err)
	}
	if status.Ready {
		t.Fatal("expected status ready=false when probe fails")
	}
	if !status.ProbeRan {
		t.Fatal("expected probe to run")
	}
	if status.Category != CategoryBackendInitFailed {
		t.Fatalf("expected backend init category, got %q", status.Category)
	}
	if status.WarmState != "probe_failed" {
		t.Fatalf("expected warm state probe_failed, got %q", status.WarmState)
	}
}

func TestProbeYZMAWithOptionsDisablesCPUFallbackForBuildValidation(t *testing.T) {
	cfg := testLocalConfig(t)
	t.Setenv("TOPS_YZMA_CPU_FALLBACK", "1")
	svc := Service{
		store: probeTestStore{},
		now:   time.Now,
		providerFactory: func(cfg config.Config, logger *obs.Logger) (llm.LLMProvider, error) {
			return fakeWarmProvider{
				warmErr: fmt.Errorf("create yzma context: %w", &llm.YZMAInitError{
					Stage:    "backend_init",
					Reason:   "Metal backend could not initialize",
					Evidence: []string{"llama_init_from_model: failed to initialize backend"},
				}),
			}, nil
		},
	}
	result, err := svc.ProbeYZMAWithOptions(context.Background(), cfg, ProbeOptions{
		Source:             "build_post_install",
		DisableCPUFallback: true,
	})
	if err != nil {
		t.Fatalf("probe yzma with options: %v", err)
	}
	if result.ProbeCtx.CPUFallback {
		t.Fatal("expected probe context cpu_fallback=false when DisableCPUFallback is set")
	}
	if result.ProbeCtx.ProbeSource != "build_post_install" {
		t.Fatalf("expected build_post_install probe source, got %q", result.ProbeCtx.ProbeSource)
	}
}

func TestProbeYZMAGenerationFailureIsReported(t *testing.T) {
	cfg := testLocalConfig(t)
	svc := Service{
		store: probeTestStore{},
		now:   time.Now,
		providerFactory: func(cfg config.Config, logger *obs.Logger) (llm.LLMProvider, error) {
			return fakeGenerationFailProvider{
				completeErr: fmt.Errorf("create yzma context: %w", &llm.YZMAInitError{
					Stage:    "backend_init",
					Reason:   "failed to create Metal command queue",
					Evidence: []string{"ggml_metal_init: failed to create command queue"},
				}),
			}, nil
		},
	}
	result, err := svc.ProbeYZMAWithOptions(context.Background(), cfg, ProbeOptions{
		Source:   "doctor",
		Generate: true,
	})
	if err != nil {
		t.Fatalf("probe yzma with generation: %v", err)
	}
	if result.Status != "failed" {
		t.Fatalf("expected failed status, got %q", result.Status)
	}
	if result.Stage != "backend_init" && result.Stage != "generation_complete" {
		t.Fatalf("expected backend_init or generation_complete stage, got %q", result.Stage)
	}
	if result.Category != CategoryBackendInitFailed {
		t.Fatalf("expected backend init category, got %q", result.Category)
	}
}

func TestProbeYZMAToolChatFailureIsReported(t *testing.T) {
	cfg := testLocalConfig(t)
	svc := Service{
		store: probeTestStore{},
		now:   time.Now,
		providerFactory: func(cfg config.Config, logger *obs.Logger) (llm.LLMProvider, error) {
			return fakeNativeToolChatFailProvider{
				toolChatErr: fmt.Errorf("toolchat failed for probe"),
			}, nil
		},
	}
	result, err := svc.ProbeYZMAWithOptions(context.Background(), cfg, ProbeOptions{
		Source:   "doctor",
		Generate: true,
	})
	if err != nil {
		t.Fatalf("probe yzma with generation: %v", err)
	}
	if result.Status != "failed" {
		t.Fatalf("expected failed status, got %q", result.Status)
	}
	if result.Stage != "generation_toolchat" {
		t.Fatalf("expected generation_toolchat stage, got %q", result.Stage)
	}
	if result.Category == CategoryNone {
		t.Fatalf("expected non-empty category for generation toolchat failure")
	}
}

func TestProbeYZMAMetalDeviceUnusableClassifiesSubReason(t *testing.T) {
	cfg := testLocalConfig(t)
	t.Setenv("SSH_CONNECTION", "127.0.0.1 22 127.0.0.1 53000")
	svc := Service{
		store: probeTestStore{},
		now:   time.Now,
		providerFactory: func(cfg config.Config, logger *obs.Logger) (llm.LLMProvider, error) {
			return fakeWarmProvider{
				warmErr: fmt.Errorf("create yzma context: %w", &llm.YZMAInitError{
					Stage:  "backend_init",
					Reason: "Metal GPU device is present but unusable (empty name with zero reported memory)",
					Evidence: []string{
						`gpu_device_name=""`,
						"gpu_device_memory_total=0",
					},
				}),
			}, nil
		},
	}
	result, err := svc.ProbeYZMA(context.Background(), cfg)
	if err != nil {
		t.Fatalf("probe yzma: %v", err)
	}
	if result.SubReason != "metal_device_unusable" {
		t.Fatalf("expected sub reason metal_device_unusable, got %q", result.SubReason)
	}
	if strings.TrimSpace(result.EnvironmentSignal) == "" {
		t.Fatal("expected environment signal for metal_device_unusable")
	}
	if len(result.Hints) == 0 {
		t.Fatal("expected hints for metal_device_unusable")
	}
}

func TestProbeYZMAMetalUsesBackendProbeFunction(t *testing.T) {
	cfg := testLocalConfig(t)
	original := probeYZMAMetalBackend
	t.Cleanup(func() { probeYZMAMetalBackend = original })
	probeYZMAMetalBackend = func(libPath string, logger *obs.Logger) (llm.ProbeContextSnapshot, error) {
		return llm.ProbeContextSnapshot{
			LibPath:      strings.TrimSpace(libPath),
			ProcessPID:   12345,
			BackendCount: 3,
			BackendNames: []string{"BLAS", "CPU", "MTL"},
			DeviceCount:  2,
			DeviceNames:  []string{"CPU", "MTL0"},
		}, nil
	}

	svc := Service{store: probeTestStore{}, now: time.Now}
	result, err := svc.ProbeYZMAMetal(context.Background(), cfg)
	if err != nil {
		t.Fatalf("probe yzma metal: %v", err)
	}
	if result.Status != "ok" {
		t.Fatalf("expected ok status, got %q", result.Status)
	}
	if result.Stage != "metal_backend" {
		t.Fatalf("expected metal_backend stage, got %q", result.Stage)
	}
	if result.ProbeCtx.BackendCount != 3 {
		t.Fatalf("expected backend count 3, got %d", result.ProbeCtx.BackendCount)
	}
}
