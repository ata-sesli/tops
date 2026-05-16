package tui

import "testing"

func TestSetupWizardYZMAProviderFlow(t *testing.T) {
	state := NewSetupWizardState("/tmp/config.json", nil)

	res := state.Submit("yzma")
	if state.Step != setupStepModel {
		t.Fatalf("expected model step, got %v", state.Step)
	}
	if res.NeedModelDiscovery {
		t.Fatal("provider step should not trigger model discovery")
	}

	state.Submit("gemma4:e4b")
	if state.Step != setupStepModelPath {
		t.Fatalf("expected model path step, got %v", state.Step)
	}
	state.Submit("/models/gemma4:e4b.gguf")
	if state.Step != setupStepLibPath {
		t.Fatalf("expected lib path step, got %v", state.Step)
	}
	state.Submit("/opt/llama/lib")
	if state.Step != setupStepModelsDir {
		t.Fatalf("expected models dir step, got %v", state.Step)
	}
}

func TestSetupWizardBuildConfigYZMA(t *testing.T) {
	state := NewSetupWizardState("/tmp/config.json", nil)
	state.Provider = "yzma"
	state.Model = "llama3.1"
	state.ModelPath = "/models/llama3.1.gguf"
	state.LibPath = "/opt/llama/lib"
	state.ModelsDir = "/models"
	state.Shell = "zsh"
	state.Output = "text"
	state.Timeout = "10"
	state.Debug = "N"

	cfg, err := state.BuildConfig()
	if err != nil {
		t.Fatalf("build config failed: %v", err)
	}
	if cfg.Provider.Type != "yzma" {
		t.Fatalf("expected provider yzma, got %q", cfg.Provider.Type)
	}
	if cfg.Provider.APIKeyEnv != "" {
		t.Fatalf("expected empty api key env for yzma, got %q", cfg.Provider.APIKeyEnv)
	}
	if cfg.Provider.ModelPath != "/models/llama3.1.gguf" {
		t.Fatalf("unexpected model path %q", cfg.Provider.ModelPath)
	}
	if cfg.Provider.LibPath != "/opt/llama/lib" {
		t.Fatalf("unexpected lib path %q", cfg.Provider.LibPath)
	}
}

func TestSetupWizardModelSelectionByIndex(t *testing.T) {
	state := NewSetupWizardState("/tmp/config.json", nil)
	state.Provider = "yzma"
	state.Step = setupStepModel
	state.AvailableModels = []string{"llama3.1", "qwen2.5"}

	result := state.Submit("2")
	if result.Output == "" {
		t.Fatal("expected model set message")
	}
	if state.Model != "qwen2.5" {
		t.Fatalf("expected indexed model selection, got %q", state.Model)
	}
}

func TestSetupWizardModelSelectionFallsBackToManualText(t *testing.T) {
	state := NewSetupWizardState("/tmp/config.json", nil)
	state.Provider = "yzma"
	state.Step = setupStepModel
	state.AvailableModels = []string{"llama3.1"}

	state.Submit("999")
	if state.Model != "999" {
		t.Fatalf("expected manual fallback model text, got %q", state.Model)
	}
}
