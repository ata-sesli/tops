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
		Provider: ProviderConfig{Type: ProviderLocal, Model: "llama", Endpoint: "http://localhost:11434/v1/chat/completions"},
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
			Type:     ProviderLocal,
			Model:    "llama3.1",
			Endpoint: "http://localhost:11434/v1/chat/completions",
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
	if loaded.Provider.Type != ProviderLocal || loaded.Provider.Model != "llama3.1" {
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
			Type:     ProviderLocal,
			Model:    "llama3.1",
			Endpoint: "http://localhost:11434",
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
			Type:     ProviderLocal,
			Model:    "llama3.1",
			Endpoint: "http://localhost:11434",
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
