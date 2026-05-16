package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigValidateHostedProviderRequiresAPIEnv(t *testing.T) {
	cfg := Config{
		Provider:   ProviderConfig{Type: ProviderOpenAI, Model: "gpt-5"},
		Shell:      "zsh",
		Output:     OutputConfig{Format: "text"},
		Inspection: InspectionConfig{TimeoutSeconds: 10},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "api_key_env") {
		t.Fatalf("expected api_key_env validation error, got %v", err)
	}
}

func TestConfigApplyDefaults(t *testing.T) {
	cfg := Config{
		Provider: ProviderConfig{Type: ProviderYZMA, Model: "llama"},
	}
	if err := cfg.ApplyDefaults(); err != nil {
		t.Fatalf("apply defaults: %v", err)
	}
	if cfg.Shell == "" || cfg.Output.Format == "" || cfg.Inspection.TimeoutSeconds == 0 {
		t.Fatalf("defaults were not applied: %+v", cfg)
	}
	if cfg.Execution.Permissions.ReadOnly != ActionPermissionAllow {
		t.Fatalf("expected default read_only allow, got %s", cfg.Execution.Permissions.ReadOnly)
	}
	if cfg.Execution.Permissions.Write != ActionPermissionRequest {
		t.Fatalf("expected default write request, got %s", cfg.Execution.Permissions.Write)
	}
	if cfg.Execution.TraceMode != TraceModeRelease {
		t.Fatalf("expected default trace mode release, got %s", cfg.Execution.TraceMode)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg := Config{
		Provider: ProviderConfig{
			Type:  ProviderYZMA,
			Model: "llama3.1",
		},
		Shell:      "zsh",
		Output:     OutputConfig{Format: "text"},
		Inspection: InspectionConfig{TimeoutSeconds: 10},
	}
	if err := SaveAtomic(path, cfg); err != nil {
		t.Fatalf("save failed: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if loaded.Provider.Type != ProviderYZMA || loaded.Provider.Model != "llama3.1" {
		t.Fatalf("unexpected loaded config: %+v", loaded)
	}
	if loaded.Execution.Enabled {
		t.Fatalf("expected execution.enabled default false, got true")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config file missing: %v", err)
	}
}

func TestSaveAndLoadExecutionEnabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg := Config{
		Provider: ProviderConfig{
			Type:  ProviderYZMA,
			Model: "llama3.1",
		},
		Shell:      "zsh",
		Output:     OutputConfig{Format: "text"},
		Inspection: InspectionConfig{TimeoutSeconds: 10},
		Execution: ExecutionConfig{
			Enabled: true,
			Permissions: ExecutionPermissionsConfig{
				ReadOnly: ActionPermissionDisallow,
				Write:    ActionPermissionRequest,
			},
			TraceMode: TraceModeRelease,
		},
	}
	if err := SaveAtomic(path, cfg); err != nil {
		t.Fatalf("save failed: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if !loaded.Execution.Enabled {
		t.Fatal("expected execution.enabled=true after round trip")
	}
	if loaded.Execution.Permissions.ReadOnly != ActionPermissionDisallow {
		t.Fatalf("expected read_only disallow, got %s", loaded.Execution.Permissions.ReadOnly)
	}
	if loaded.Execution.TraceMode != TraceModeRelease {
		t.Fatalf("expected release trace mode, got %s", loaded.Execution.TraceMode)
	}
}

func TestConfigValidateExecutionSettings(t *testing.T) {
	cfg := Config{
		Provider: ProviderConfig{
			Type:  ProviderYZMA,
			Model: "llama3.1",
		},
		Shell:      "zsh",
		Output:     OutputConfig{Format: "text"},
		Inspection: InspectionConfig{TimeoutSeconds: 10},
		Execution: ExecutionConfig{
			Permissions: ExecutionPermissionsConfig{
				ReadOnly: "bad",
				Write:    ActionPermissionRequest,
			},
			TraceMode: TraceModeDebug,
		},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "read_only") {
		t.Fatalf("expected read_only validation error, got %v", err)
	}
	cfg.Execution.Permissions.ReadOnly = ActionPermissionAllow
	cfg.Execution.TraceMode = "invalid"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "trace_mode") {
		t.Fatalf("expected trace_mode validation error, got %v", err)
	}
}

func TestApplyDefaultsYZMAModelDirsNormalization(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := Config{
		Provider: ProviderConfig{
			Type:      ProviderYZMA,
			Model:     "gemma4:e4b",
			ModelsDir: "~/custom-models",
			ModelsDirs: []string{
				"~/custom-models",
				"~/custom-models",
			},
		},
		Shell:      "zsh",
		Output:     OutputConfig{Format: "text"},
		Inspection: InspectionConfig{TimeoutSeconds: 10},
	}
	if err := cfg.ApplyDefaults(); err != nil {
		t.Fatalf("apply defaults: %v", err)
	}
	wantDefault := filepath.Join(home, ".tops", "models")
	wantCustom := filepath.Join(home, "custom-models")
	if len(cfg.Provider.ModelsDirs) < 2 {
		t.Fatalf("expected default+custom model dirs, got %+v", cfg.Provider.ModelsDirs)
	}
	if cfg.Provider.ModelsDirs[0] != wantDefault {
		t.Fatalf("expected default models dir first, got %q", cfg.Provider.ModelsDirs[0])
	}
	if cfg.Provider.ModelsDirs[1] != wantCustom {
		t.Fatalf("expected normalized custom models dir second, got %q", cfg.Provider.ModelsDirs[1])
	}
	if cfg.Provider.ModelsDir != wantDefault {
		t.Fatalf("expected models_dir to mirror canonical first path, got %q", cfg.Provider.ModelsDir)
	}
}

func TestSaveAtomicYZMADropsDeprecatedModelsDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path := filepath.Join(t.TempDir(), "config.json")
	cfg := Config{
		Provider: ProviderConfig{
			Type:      ProviderYZMA,
			Model:     "gemma4:e4b",
			ModelsDir: "~/legacy",
			ModelsDirs: []string{
				"~/legacy",
			},
		},
		Shell:      "zsh",
		Output:     OutputConfig{Format: "text"},
		Inspection: InspectionConfig{TimeoutSeconds: 10},
	}
	if err := SaveAtomic(path, cfg); err != nil {
		t.Fatalf("save failed: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	if strings.Contains(string(raw), "\"models_dir\"") {
		t.Fatalf("expected deprecated models_dir to be omitted from persisted yzma config, got: %s", string(raw))
	}
	if !strings.Contains(string(raw), "\"models_dirs\"") {
		t.Fatalf("expected models_dirs to be persisted, got: %s", string(raw))
	}
}
