package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"tops/internal/app"
	"tops/internal/config"
	"tops/internal/modelprofile"
)

type fakeOllamaManager struct {
	ensureErr error
	listErr   error
	models    []string
	warmErr   error
}

func (f fakeOllamaManager) EnsureRunning(ctx context.Context, endpoint string) error {
	return f.ensureErr
}

func (f fakeOllamaManager) ListModels(ctx context.Context, endpoint string) ([]string, error) {
	return f.models, f.listErr
}

func (f fakeOllamaManager) WarmModel(ctx context.Context, endpoint string, model string) error {
	return f.warmErr
}

func TestResolveModelChoiceByIndex(t *testing.T) {
	got, err := resolveModelChoice("2", []string{"llama3.1", "qwen2.5"})
	if err != nil {
		t.Fatalf("resolve model failed: %v", err)
	}
	if got != "qwen2.5" {
		t.Fatalf("expected qwen2.5, got %q", got)
	}
}

func TestResolveModelChoiceOutOfRange(t *testing.T) {
	_, err := resolveModelChoice("3", []string{"llama3.1"})
	if err == nil {
		t.Fatal("expected out-of-range error")
	}
}

func TestSwitchModelPersistsAndReloads(t *testing.T) {
	t.Setenv("TOPS_MODEL_PROFILES", t.TempDir()+"/models.json")
	path := t.TempDir() + "/config.json"
	rt := app.Runtime{
		Config: config.Config{
			Provider: config.ProviderConfig{
				Type:     config.ProviderLocal,
				Model:    "llama3.1",
				Endpoint: "http://localhost:11434",
			},
			Shell:      "zsh",
			Output:     config.OutputConfig{Format: "text"},
			Inspection: config.InspectionConfig{TimeoutSeconds: 10},
		},
	}
	if err := config.SaveAtomic(path, rt.Config); err != nil {
		t.Fatalf("seed config failed: %v", err)
	}

	out, _, updated, err := switchModel(
		context.Background(),
		fakeOllamaManager{models: []string{"llama3.1", "qwen2.5"}},
		rt,
		path,
		func(configPath string) (app.Runtime, error) {
			cfg, err := config.Load(configPath)
			if err != nil {
				return app.Runtime{}, err
			}
			return app.NewRuntime(cfg)
		},
		"2",
	)
	if err != nil {
		t.Fatalf("switch model failed: %v", err)
	}
	if out == "" {
		t.Fatal("expected output message")
	}
	if !strings.Contains(out, "Ollama warmed the selected model.") {
		t.Fatalf("expected warm-up success note, got %q", out)
	}
	if updated == nil {
		t.Fatal("expected updated runtime")
	}
	if updated.Config.Provider.Model != "qwen2.5" {
		t.Fatalf("expected updated runtime model qwen2.5, got %q", updated.Config.Provider.Model)
	}

	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload config failed: %v", err)
	}
	if reloaded.Provider.Model != "qwen2.5" {
		t.Fatalf("expected persisted model qwen2.5, got %q", reloaded.Provider.Model)
	}
}

func TestSwitchModelWarmFailureDoesNotAbortSwitch(t *testing.T) {
	t.Setenv("TOPS_MODEL_PROFILES", t.TempDir()+"/models.json")
	path := t.TempDir() + "/config.json"
	rt := app.Runtime{
		Config: config.Config{
			Provider: config.ProviderConfig{
				Type:     config.ProviderLocal,
				Model:    "llama3.1",
				Endpoint: "http://localhost:11434",
			},
			Shell:      "zsh",
			Output:     config.OutputConfig{Format: "text"},
			Inspection: config.InspectionConfig{TimeoutSeconds: 10},
		},
	}
	if err := config.SaveAtomic(path, rt.Config); err != nil {
		t.Fatalf("seed config failed: %v", err)
	}

	out, _, updated, err := switchModel(
		context.Background(),
		fakeOllamaManager{models: []string{"llama3.1", "qwen2.5"}, warmErr: errors.New("timeout")},
		rt,
		path,
		func(configPath string) (app.Runtime, error) {
			cfg, err := config.Load(configPath)
			if err != nil {
				return app.Runtime{}, err
			}
			return app.NewRuntime(cfg)
		},
		"2",
	)
	if err != nil {
		t.Fatalf("switch model failed unexpectedly: %v", err)
	}
	if updated == nil {
		t.Fatal("expected updated runtime despite warm failure")
	}
	if !strings.Contains(out, "Warning: model warm-up failed") {
		t.Fatalf("expected warm-up warning, got %q", out)
	}
}

func TestSetModelConfigPersistsAndReloads(t *testing.T) {
	modelsPath := t.TempDir() + "/models.json"
	t.Setenv("TOPS_MODEL_PROFILES", modelsPath)
	configPath := t.TempDir() + "/config.json"

	cfg := config.Config{
		Provider: config.ProviderConfig{
			Type:     config.ProviderLocal,
			Model:    "llama3.1",
			Endpoint: "http://localhost:11434",
		},
		Shell:      "zsh",
		Output:     config.OutputConfig{Format: "text"},
		Inspection: config.InspectionConfig{TimeoutSeconds: 10},
	}
	if err := config.SaveAtomic(configPath, cfg); err != nil {
		t.Fatalf("seed config failed: %v", err)
	}
	rt, err := app.NewRuntime(cfg)
	if err != nil {
		t.Fatalf("new runtime failed: %v", err)
	}

	out, _, updated, err := setModelConfig(rt, configPath, func(path string) (app.Runtime, error) {
		loaded, err := config.Load(path)
		if err != nil {
			return app.Runtime{}, err
		}
		return app.NewRuntime(loaded)
	}, "context", "8192")
	if err != nil {
		t.Fatalf("set model config failed: %v", err)
	}
	if !strings.Contains(out, "context") {
		t.Fatalf("expected context output, got %q", out)
	}
	if updated == nil {
		t.Fatal("expected runtime to be reloaded")
	}

	profiles, err := modelprofile.Load(modelsPath)
	if err != nil {
		t.Fatalf("load model profiles failed: %v", err)
	}
	profile, ok := profiles.Get(config.ProviderLocal, "llama3.1")
	if !ok {
		t.Fatal("expected profile to exist")
	}
	if profile.Context != 8192 {
		t.Fatalf("expected context 8192, got %d", profile.Context)
	}
}

func TestSetModelConfigThinkPersists(t *testing.T) {
	modelsPath := t.TempDir() + "/models.json"
	t.Setenv("TOPS_MODEL_PROFILES", modelsPath)
	configPath := t.TempDir() + "/config.json"

	cfg := config.Config{
		Provider: config.ProviderConfig{
			Type:     config.ProviderLocal,
			Model:    "llama3.1",
			Endpoint: "http://localhost:11434",
		},
		Shell:      "zsh",
		Output:     config.OutputConfig{Format: "text"},
		Inspection: config.InspectionConfig{TimeoutSeconds: 10},
	}
	if err := config.SaveAtomic(configPath, cfg); err != nil {
		t.Fatalf("seed config failed: %v", err)
	}
	rt, err := app.NewRuntime(cfg)
	if err != nil {
		t.Fatalf("new runtime failed: %v", err)
	}
	_, _, _, err = setModelConfig(rt, configPath, func(path string) (app.Runtime, error) {
		loaded, err := config.Load(path)
		if err != nil {
			return app.Runtime{}, err
		}
		return app.NewRuntime(loaded)
	}, "think", "off")
	if err != nil {
		t.Fatalf("set think failed: %v", err)
	}
	profiles, err := modelprofile.Load(modelsPath)
	if err != nil {
		t.Fatalf("load model profiles failed: %v", err)
	}
	profile, ok := profiles.Get(config.ProviderLocal, "llama3.1")
	if !ok {
		t.Fatal("expected profile to exist")
	}
	if profile.Think != "off" {
		t.Fatalf("expected think=off, got %q", profile.Think)
	}
}

func TestShowExecutionPolicyOmitsDeprecatedEnabledFlag(t *testing.T) {
	rt := app.Runtime{
		Config: config.Config{
			Execution: config.ExecutionConfig{
				Enabled: true,
				Permissions: config.ExecutionPermissionsConfig{
					ReadOnly: config.ActionPermissionAllow,
					Write:    config.ActionPermissionRequest,
				},
				TraceMode: config.TraceModeRelease,
			},
		},
	}

	out, _, err := showExecutionPolicy(rt)
	if err != nil {
		t.Fatalf("show execution policy failed: %v", err)
	}
	if strings.Contains(out, "enabled:") {
		t.Fatalf("expected deprecated enabled flag to be omitted, got %q", out)
	}
	if !strings.Contains(out, "read_only: allow") || !strings.Contains(out, "write: request") {
		t.Fatalf("expected permission details, got %q", out)
	}
	if strings.Contains(out, "trace_mode:") {
		t.Fatalf("expected trace mode to have a dedicated command, got %q", out)
	}
}

func TestResetModelConfigRemovesProfile(t *testing.T) {
	modelsPath := t.TempDir() + "/models.json"
	t.Setenv("TOPS_MODEL_PROFILES", modelsPath)
	configPath := t.TempDir() + "/config.json"

	cfg := config.Config{
		Provider: config.ProviderConfig{
			Type:     config.ProviderLocal,
			Model:    "llama3.1",
			Endpoint: "http://localhost:11434",
		},
		Shell:      "zsh",
		Output:     config.OutputConfig{Format: "text"},
		Inspection: config.InspectionConfig{TimeoutSeconds: 10},
	}
	if err := config.SaveAtomic(configPath, cfg); err != nil {
		t.Fatalf("seed config failed: %v", err)
	}
	profiles := modelprofile.Empty()
	if err := profiles.Upsert(modelprofile.ModelProfile{
		Provider:  config.ProviderLocal,
		Model:     "llama3.1",
		Context:   4096,
		MaxLength: 1000,
	}); err != nil {
		t.Fatalf("upsert seed profile failed: %v", err)
	}
	if err := modelprofile.SaveAtomic(modelsPath, profiles); err != nil {
		t.Fatalf("save seed profiles failed: %v", err)
	}
	rt, err := app.NewRuntime(cfg)
	if err != nil {
		t.Fatalf("new runtime failed: %v", err)
	}

	_, _, _, err = resetModelConfig(rt, configPath, func(path string) (app.Runtime, error) {
		loaded, err := config.Load(path)
		if err != nil {
			return app.Runtime{}, err
		}
		return app.NewRuntime(loaded)
	})
	if err != nil {
		t.Fatalf("reset model config failed: %v", err)
	}

	loadedProfiles, err := modelprofile.Load(modelsPath)
	if err != nil {
		t.Fatalf("load profiles failed: %v", err)
	}
	if _, ok := loadedProfiles.Get(config.ProviderLocal, "llama3.1"); ok {
		t.Fatal("expected profile to be removed")
	}
}

func TestDeriveOllamaStatusAvailable(t *testing.T) {
	rt := &app.Runtime{
		Config: config.Config{
			Provider: config.ProviderConfig{
				Type:  config.ProviderLocal,
				Model: "qwen2.5",
			},
		},
	}
	status := deriveOllamaStatus(rt, []string{"llama3.1", "qwen2.5"}, nil)
	if !status.Applicable || !status.Available {
		t.Fatalf("expected applicable+available status, got %+v", status)
	}
}

func TestDeriveOllamaStatusUnavailable(t *testing.T) {
	rt := &app.Runtime{
		Config: config.Config{
			Provider: config.ProviderConfig{
				Type:  config.ProviderOllama,
				Model: "missing-model",
			},
		},
	}
	status := deriveOllamaStatus(rt, []string{"llama3.1"}, nil)
	if !status.Applicable || status.Available {
		t.Fatalf("expected applicable+unavailable status, got %+v", status)
	}
	if !strings.Contains(status.Detail, "not found") {
		t.Fatalf("unexpected status detail: %q", status.Detail)
	}
}

func TestDeriveOllamaStatusNotServing(t *testing.T) {
	rt := &app.Runtime{
		Config: config.Config{
			Provider: config.ProviderConfig{
				Type:  config.ProviderLocal,
				Model: "llama3.1",
			},
		},
	}
	status := deriveOllamaStatus(rt, nil, errors.New("dial error"))
	if !status.Applicable || status.Available {
		t.Fatalf("expected applicable+unavailable status, got %+v", status)
	}
}

func TestSetExecutionPolicyPersistsAndReloads(t *testing.T) {
	configPath := t.TempDir() + "/config.json"
	t.Setenv("TOPS_MODEL_PROFILES", t.TempDir()+"/models.json")
	cfg := config.Config{
		Provider: config.ProviderConfig{
			Type:     config.ProviderLocal,
			Model:    "llama3.1",
			Endpoint: "http://localhost:11434",
		},
		Shell:      "zsh",
		Output:     config.OutputConfig{Format: "text"},
		Inspection: config.InspectionConfig{TimeoutSeconds: 10},
	}
	if err := config.SaveAtomic(configPath, cfg); err != nil {
		t.Fatalf("save config failed: %v", err)
	}
	rt, err := app.NewRuntime(cfg)
	if err != nil {
		t.Fatalf("new runtime failed: %v", err)
	}

	out, _, updated, err := setExecutionPolicy(rt, configPath, func(path string) (app.Runtime, error) {
		loaded, err := config.Load(path)
		if err != nil {
			return app.Runtime{}, err
		}
		return app.NewRuntime(loaded)
	}, "read-only", "disallow")
	if err != nil {
		t.Fatalf("set execution policy failed: %v", err)
	}
	if !strings.Contains(out, "read-only=disallow") {
		t.Fatalf("unexpected output: %q", out)
	}
	if updated == nil {
		t.Fatal("expected updated runtime")
	}
	if updated.Config.Execution.Permissions.ReadOnly != config.ActionPermissionDisallow {
		t.Fatalf("expected read_only disallow, got %s", updated.Config.Execution.Permissions.ReadOnly)
	}

	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("reload config failed: %v", err)
	}
	if loaded.Execution.Permissions.ReadOnly != config.ActionPermissionDisallow {
		t.Fatalf("expected persisted read_only disallow, got %s", loaded.Execution.Permissions.ReadOnly)
	}
}

func TestSetExecutionTracePersistsAndReloads(t *testing.T) {
	configPath := t.TempDir() + "/config.json"
	t.Setenv("TOPS_MODEL_PROFILES", t.TempDir()+"/models.json")
	cfg := config.Config{
		Provider: config.ProviderConfig{
			Type:     config.ProviderLocal,
			Model:    "llama3.1",
			Endpoint: "http://localhost:11434",
		},
		Shell:      "zsh",
		Output:     config.OutputConfig{Format: "text"},
		Inspection: config.InspectionConfig{TimeoutSeconds: 10},
	}
	if err := config.SaveAtomic(configPath, cfg); err != nil {
		t.Fatalf("save config failed: %v", err)
	}
	rt, err := app.NewRuntime(cfg)
	if err != nil {
		t.Fatalf("new runtime failed: %v", err)
	}

	out, _, updated, err := setExecutionTrace(rt, configPath, func(path string) (app.Runtime, error) {
		loaded, err := config.Load(path)
		if err != nil {
			return app.Runtime{}, err
		}
		return app.NewRuntime(loaded)
	}, "debug")
	if err != nil {
		t.Fatalf("set execution trace failed: %v", err)
	}
	if !strings.Contains(out, "trace_mode=debug") {
		t.Fatalf("unexpected output: %q", out)
	}
	if updated == nil || updated.Config.Execution.TraceMode != config.TraceModeDebug {
		t.Fatalf("expected updated runtime debug trace, got %+v", updated)
	}

	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("reload config failed: %v", err)
	}
	if loaded.Execution.TraceMode != config.TraceModeDebug {
		t.Fatalf("expected persisted debug trace, got %s", loaded.Execution.TraceMode)
	}
}

func TestSetModelResponseProfilePersistsAndReloads(t *testing.T) {
	modelsPath := t.TempDir() + "/models.json"
	t.Setenv("TOPS_MODEL_PROFILES", modelsPath)
	configPath := t.TempDir() + "/config.json"

	cfg := config.Config{
		Provider: config.ProviderConfig{
			Type:     config.ProviderLocal,
			Model:    "llama3.1",
			Endpoint: "http://localhost:11434",
		},
		Shell:      "zsh",
		Output:     config.OutputConfig{Format: "text"},
		Inspection: config.InspectionConfig{TimeoutSeconds: 10},
	}
	if err := config.SaveAtomic(configPath, cfg); err != nil {
		t.Fatalf("seed config failed: %v", err)
	}
	rt, err := app.NewRuntime(cfg)
	if err != nil {
		t.Fatalf("new runtime failed: %v", err)
	}

	out, _, updated, err := setModelResponseProfile(rt, configPath, func(path string) (app.Runtime, error) {
		loaded, err := config.Load(path)
		if err != nil {
			return app.Runtime{}, err
		}
		return app.NewRuntime(loaded)
	}, "notes", "off")
	if err != nil {
		t.Fatalf("set model response profile failed: %v", err)
	}
	if !strings.Contains(out, "notes=off") {
		t.Fatalf("unexpected output: %q", out)
	}
	if updated == nil || updated.AskResponseProfile.Notes {
		t.Fatalf("expected updated runtime notes disabled, got %+v", updated)
	}

	profiles, err := modelprofile.Load(modelsPath)
	if err != nil {
		t.Fatalf("load model profiles failed: %v", err)
	}
	profile, ok := profiles.Get(config.ProviderLocal, "llama3.1")
	if !ok {
		t.Fatal("expected model profile to exist")
	}
	if profile.AskResponse.Notes == nil || *profile.AskResponse.Notes {
		t.Fatalf("expected persisted notes off, got %+v", profile.AskResponse)
	}
}
