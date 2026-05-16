package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tops/internal/app"
	"tops/internal/config"
	"tops/internal/runtime/localruntime"
	"tops/internal/storage/modelprofile"
)

func TestResolveLocalModelSelectionByIndex(t *testing.T) {
	models := []localruntime.ModelEntry{
		{Name: "llama3.1", Path: "/models/llama3.1.gguf"},
		{Name: "qwen2.5", Path: "/models/qwen2.5.gguf"},
	}
	name, path, err := resolveLocalModelSelection("2", models)
	if err != nil {
		t.Fatalf("resolve model failed: %v", err)
	}
	if name != "qwen2.5" || path != "/models/qwen2.5.gguf" {
		t.Fatalf("unexpected resolution: name=%q path=%q", name, path)
	}
}

func TestResolveLocalModelSelectionAmbiguousName(t *testing.T) {
	models := []localruntime.ModelEntry{
		{Name: "same", Path: "/a/same.gguf"},
		{Name: "same", Path: "/b/same.gguf"},
	}
	_, _, err := resolveLocalModelSelection("same", models)
	if err == nil {
		t.Fatal("expected ambiguous name error")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSwitchModelPersistsAndReloads(t *testing.T) {
	t.Setenv("TOPS_MODEL_PROFILES", t.TempDir()+"/models.json")
	modelDir := t.TempDir()
	modelA := filepath.Join(modelDir, "alpha.gguf")
	modelB := filepath.Join(modelDir, "beta.gguf")
	if err := os.WriteFile(modelA, []byte("a"), 0o644); err != nil {
		t.Fatalf("seed model A failed: %v", err)
	}
	if err := os.WriteFile(modelB, []byte("b"), 0o644); err != nil {
		t.Fatalf("seed model B failed: %v", err)
	}

	configPath := t.TempDir() + "/config.json"
	cfg := config.Config{
		Provider: config.ProviderConfig{
			Type:       config.ProviderYZMA,
			Model:      "alpha",
			ModelPath:  modelA,
			ModelsDirs: []string{modelDir},
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

	out, _, updated, err := switchModel(context.Background(), rt, configPath, func(path string) (app.Runtime, error) {
		loaded, loadErr := config.Load(path)
		if loadErr != nil {
			return app.Runtime{}, loadErr
		}
		return app.NewRuntime(loaded)
	}, "2")
	if err != nil {
		t.Fatalf("switch model failed: %v", err)
	}
	if updated == nil {
		t.Fatal("expected updated runtime")
	}
	if updated.Config.Provider.Model != "beta" {
		t.Fatalf("expected selected model beta, got %q", updated.Config.Provider.Model)
	}
	if updated.Config.Provider.ModelPath != modelB {
		t.Fatalf("expected selected model path %q, got %q", modelB, updated.Config.Provider.ModelPath)
	}
	if !strings.Contains(out, "Model switched to") {
		t.Fatalf("expected switch output, got %q", out)
	}

	reloaded, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("reload config failed: %v", err)
	}
	if reloaded.Provider.Model != "beta" {
		t.Fatalf("expected persisted model beta, got %q", reloaded.Provider.Model)
	}
}

func TestSetModelConfigPersistsAndReloads(t *testing.T) {
	modelsPath := t.TempDir() + "/models.json"
	t.Setenv("TOPS_MODEL_PROFILES", modelsPath)
	configPath := t.TempDir() + "/config.json"

	cfg := config.Config{
		Provider: config.ProviderConfig{
			Type:      config.ProviderYZMA,
			Model:     "llama3.1",
			ModelPath: "/tmp/llama3.1.gguf",
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
		loaded, loadErr := config.Load(path)
		if loadErr != nil {
			return app.Runtime{}, loadErr
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
		t.Fatal("expected runtime reload")
	}

	profiles, err := modelprofile.Load(modelsPath)
	if err != nil {
		t.Fatalf("load profiles failed: %v", err)
	}
	profile, ok := profiles.Get(config.ProviderYZMA, "llama3.1")
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
			Type:      config.ProviderYZMA,
			Model:     "llama3.1",
			ModelPath: "/tmp/llama3.1.gguf",
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
		loaded, loadErr := config.Load(path)
		if loadErr != nil {
			return app.Runtime{}, loadErr
		}
		return app.NewRuntime(loaded)
	}, "think", "off")
	if err != nil {
		t.Fatalf("set think failed: %v", err)
	}

	profiles, err := modelprofile.Load(modelsPath)
	if err != nil {
		t.Fatalf("load profiles failed: %v", err)
	}
	profile, ok := profiles.Get(config.ProviderYZMA, "llama3.1")
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
		t.Fatalf("expected deprecated enabled flag omitted, got %q", out)
	}
	if !strings.Contains(out, "read_only: allow") || !strings.Contains(out, "write: request") {
		t.Fatalf("expected permission details, got %q", out)
	}
}

func TestResetModelConfigRemovesProfile(t *testing.T) {
	modelsPath := t.TempDir() + "/models.json"
	t.Setenv("TOPS_MODEL_PROFILES", modelsPath)
	configPath := t.TempDir() + "/config.json"

	cfg := config.Config{
		Provider: config.ProviderConfig{
			Type:      config.ProviderYZMA,
			Model:     "llama3.1",
			ModelPath: "/tmp/llama3.1.gguf",
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
		Provider:  config.ProviderYZMA,
		Model:     "llama3.1",
		Context:   4096,
		MaxLength: 1000,
	}); err != nil {
		t.Fatalf("upsert profile failed: %v", err)
	}
	if err := modelprofile.SaveAtomic(modelsPath, profiles); err != nil {
		t.Fatalf("save profiles failed: %v", err)
	}
	rt, err := app.NewRuntime(cfg)
	if err != nil {
		t.Fatalf("new runtime failed: %v", err)
	}

	_, _, _, err = resetModelConfig(rt, configPath, func(path string) (app.Runtime, error) {
		loaded, loadErr := config.Load(path)
		if loadErr != nil {
			return app.Runtime{}, loadErr
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
	if _, ok := loadedProfiles.Get(config.ProviderYZMA, "llama3.1"); ok {
		t.Fatal("expected profile to be removed")
	}
}

func TestDeriveLocalStatusAvailable(t *testing.T) {
	rt := &app.Runtime{
		Config: config.Config{
			Provider: config.ProviderConfig{
				Type:  config.ProviderYZMA,
				Model: "qwen2.5",
			},
		},
	}
	status := deriveLocalStatus(rt, localruntime.StatusResult{Ready: true, WarmState: "ready"}, nil)
	if !status.Applicable || !status.Available {
		t.Fatalf("expected applicable+available status, got %+v", status)
	}
}

func TestDeriveLocalStatusUnavailable(t *testing.T) {
	rt := &app.Runtime{
		Config: config.Config{
			Provider: config.ProviderConfig{
				Type:  config.ProviderYZMA,
				Model: "missing-model",
			},
		},
	}
	status := deriveLocalStatus(rt, localruntime.StatusResult{}, errors.New("dial error"))
	if !status.Applicable || status.Available {
		t.Fatalf("expected applicable+unavailable status, got %+v", status)
	}
}

func TestSetExecutionPolicyPersistsAndReloads(t *testing.T) {
	configPath := t.TempDir() + "/config.json"
	t.Setenv("TOPS_MODEL_PROFILES", t.TempDir()+"/models.json")
	cfg := config.Config{
		Provider: config.ProviderConfig{
			Type:      config.ProviderYZMA,
			Model:     "llama3.1",
			ModelPath: "/tmp/llama3.1.gguf",
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
		loaded, loadErr := config.Load(path)
		if loadErr != nil {
			return app.Runtime{}, loadErr
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
}
