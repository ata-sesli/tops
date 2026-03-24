package tui

import "testing"

func TestSetupWizardLocalProviderRequestsModelDiscovery(t *testing.T) {
	state := NewSetupWizardState("/tmp/config.json", nil)
	res := state.Submit("ollama")
	if state.Step != setupStepEndpoint {
		t.Fatalf("expected endpoint step, got %v", state.Step)
	}
	if res.NeedModelDiscovery {
		t.Fatal("provider step should not trigger model discovery")
	}
	res = state.Submit("http://localhost:11434")
	if !res.NeedModelDiscovery {
		t.Fatal("expected endpoint step to trigger model discovery")
	}
	if state.Step != setupStepModel {
		t.Fatalf("expected model step, got %v", state.Step)
	}
}

func TestSetupWizardBuildConfigOllama(t *testing.T) {
	state := NewSetupWizardState("/tmp/config.json", nil)
	state.Provider = "ollama"
	state.Endpoint = "http://localhost:11434"
	state.Model = "llama3.1"
	state.Shell = "zsh"
	state.Output = "text"
	state.Timeout = "10"
	state.Debug = "N"

	cfg, err := state.BuildConfig()
	if err != nil {
		t.Fatalf("build config failed: %v", err)
	}
	if cfg.Provider.Type != "ollama" {
		t.Fatalf("expected provider ollama, got %q", cfg.Provider.Type)
	}
	if cfg.Provider.APIKeyEnv != "" {
		t.Fatalf("expected empty api key env for ollama, got %q", cfg.Provider.APIKeyEnv)
	}
}

func TestSetupWizardModelSelectionByIndex(t *testing.T) {
	state := NewSetupWizardState("/tmp/config.json", nil)
	state.Provider = "ollama"
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
	state.Provider = "ollama"
	state.Step = setupStepModel
	state.AvailableModels = []string{"llama3.1"}

	state.Submit("999")
	if state.Model != "999" {
		t.Fatalf("expected manual fallback model text, got %q", state.Model)
	}
}
