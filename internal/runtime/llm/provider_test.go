package llm

import (
	"fmt"
	"strings"
	"testing"

	anyllm "github.com/mozilla-ai/any-llm-go"

	"tops/internal/config"
	"tops/internal/storage/modelprofile"
)

func boolPtr(v bool) *bool {
	return &v
}

func TestNewFromConfigRequiresHostedKeyEnv(t *testing.T) {
	cfg := config.Config{
		Provider:   config.ProviderConfig{Type: config.ProviderOpenAI, Model: "gpt-5", APIKeyEnv: "MISSING_TOPS_KEY"},
		Shell:      "zsh",
		Output:     config.OutputConfig{Format: "text"},
		Inspection: config.InspectionConfig{TimeoutSeconds: 10},
	}
	if _, err := NewFromConfig(cfg, nil); err == nil {
		t.Fatal("expected missing env error")
	}
}

func TestNewFromConfigLocalUsesYZMAProvider(t *testing.T) {
	cfg := config.Config{
		Provider:   config.ProviderConfig{Type: config.ProviderLocal, Model: "llama3.1"},
		Shell:      "zsh",
		Output:     config.OutputConfig{Format: "text"},
		Inspection: config.InspectionConfig{TimeoutSeconds: 10},
	}
	provider, err := NewFromConfig(cfg, nil)
	if err != nil {
		t.Fatalf("expected local provider to initialize with default endpoint: %v", err)
	}
	if provider.Name() != "yzma" {
		t.Fatalf("expected yzma provider name, got %q", provider.Name())
	}
}

func TestYZMAEffectiveThinkState(t *testing.T) {
	if enabled, _ := effectiveThinkState("off", boolPtr(true)); enabled {
		t.Fatal("expected think=off to hard disable request think")
	}
	if enabled, tier := effectiveThinkState("low", nil); !enabled || tier != "low" {
		t.Fatalf("expected think low tier to enable by default, got enabled=%t tier=%s", enabled, tier)
	}
	if enabled, _ := effectiveThinkState("on", nil); enabled {
		t.Fatal("expected think=on to defer without forcing")
	}
	if enabled, _ := effectiveThinkState("medium", boolPtr(false)); enabled {
		t.Fatal("expected explicit request think=false to remain disabled")
	}
}

func TestYZMASamplingConfigUsesThinkBudgetTierFallback(t *testing.T) {
	provider := yzmaProvider{
		profile: modelprofile.ModelProfile{
			MaxLength: 1200,
			Think:     "low",
		},
	}
	cfg := provider.effectiveSamplingConfig(SamplingProfileAsk, -1, 0, nil)
	if cfg.MaxTokens != 128 {
		t.Fatalf("expected low think tier budget cap 128, got %d", cfg.MaxTokens)
	}
}

func TestYZMASamplingProfilePlannerDeterministic(t *testing.T) {
	provider := yzmaProvider{}
	cfg := provider.effectiveSamplingConfig(SamplingProfilePlanner, -1, 0, nil)
	if cfg.ProfileName != SamplingProfilePlanner {
		t.Fatalf("expected planner profile name, got %q", cfg.ProfileName)
	}
	if !cfg.HasTemperature || cfg.Temperature != 0 {
		t.Fatalf("expected planner temperature=0, got has=%t temp=%.4f", cfg.HasTemperature, cfg.Temperature)
	}
	if cfg.MaxTokens != 256 {
		t.Fatalf("expected planner max tokens 256, got %d", cfg.MaxTokens)
	}
	if !cfg.HasTopK || cfg.TopK != 0 {
		t.Fatalf("expected planner top_k=0, got has=%t top_k=%d", cfg.HasTopK, cfg.TopK)
	}
	if !cfg.HasTopP || cfg.TopP != 1.0 {
		t.Fatalf("expected planner top_p=1.0, got has=%t top_p=%.4f", cfg.HasTopP, cfg.TopP)
	}
	if !cfg.HasMinP || cfg.MinP != 0 {
		t.Fatalf("expected planner min_p=0, got has=%t min_p=%.4f", cfg.HasMinP, cfg.MinP)
	}
	if !cfg.HasRepeatPenalty || cfg.RepeatPenalty != 1.0 {
		t.Fatalf("expected planner repeat_penalty=1.0, got has=%t repeat=%.4f", cfg.HasRepeatPenalty, cfg.RepeatPenalty)
	}
}

func TestYZMASamplingProfileGenDeterministic(t *testing.T) {
	provider := yzmaProvider{}
	cfg := provider.effectiveSamplingConfig(SamplingProfileGen, -1, 0, nil)
	if cfg.ProfileName != SamplingProfileGen {
		t.Fatalf("expected gen profile name, got %q", cfg.ProfileName)
	}
	if !cfg.HasTemperature || cfg.Temperature != 0 {
		t.Fatalf("expected gen temperature=0, got has=%t temp=%.4f", cfg.HasTemperature, cfg.Temperature)
	}
	if cfg.MaxTokens != 512 {
		t.Fatalf("expected gen max tokens 512, got %d", cfg.MaxTokens)
	}
	if !cfg.HasTopK || cfg.TopK != 0 {
		t.Fatalf("expected gen top_k=0, got has=%t top_k=%d", cfg.HasTopK, cfg.TopK)
	}
	if !cfg.HasTopP || cfg.TopP != 1.0 {
		t.Fatalf("expected gen top_p=1.0, got has=%t top_p=%.4f", cfg.HasTopP, cfg.TopP)
	}
	if !cfg.HasMinP || cfg.MinP != 0 {
		t.Fatalf("expected gen min_p=0, got has=%t min_p=%.4f", cfg.HasMinP, cfg.MinP)
	}
	if !cfg.HasRepeatPenalty || cfg.RepeatPenalty != 1.03 {
		t.Fatalf("expected gen repeat_penalty=1.03, got has=%t repeat=%.4f", cfg.HasRepeatPenalty, cfg.RepeatPenalty)
	}
}

func TestYZMASamplingProfilesAskAndHelp(t *testing.T) {
	provider := yzmaProvider{}
	askCfg := provider.effectiveSamplingConfig(SamplingProfileAsk, -1, 0, nil)
	if askCfg.ProfileName != SamplingProfileAsk {
		t.Fatalf("expected ask profile name, got %q", askCfg.ProfileName)
	}
	if askCfg.MaxTokens != 700 {
		t.Fatalf("expected ask max tokens 700, got %d", askCfg.MaxTokens)
	}
	if askCfg.Temperature != 0.4 || askCfg.TopP != 0.9 || askCfg.TopK != 40 || askCfg.MinP != 0.02 || askCfg.RepeatPenalty != 1.05 {
		t.Fatalf("unexpected ask sampling config: %+v", askCfg)
	}

	helpCfg := provider.effectiveSamplingConfig(SamplingProfileHelp, -1, 0, nil)
	if helpCfg.ProfileName != SamplingProfileHelp {
		t.Fatalf("expected help profile name, got %q", helpCfg.ProfileName)
	}
	if helpCfg.MaxTokens != 700 {
		t.Fatalf("expected help max tokens 700, got %d", helpCfg.MaxTokens)
	}
	if helpCfg.Temperature != 0.3 || helpCfg.TopP != 0.9 || helpCfg.TopK != 40 || helpCfg.MinP != 0.02 || helpCfg.RepeatPenalty != 1.05 {
		t.Fatalf("unexpected help sampling config: %+v", helpCfg)
	}
}

func TestYZMASamplingFallbackForUnsetProfileFields(t *testing.T) {
	provider := yzmaProvider{
		profile: modelprofile.ModelProfile{
			TopK:          25,
			TopP:          0.77,
			MinP:          0.11,
			RepeatPenalty: 1.09,
			MaxLength:     333,
		},
	}
	cfg := provider.defaultSamplingConfig(-1, 0)
	applyYZMASamplingPreset(&cfg, yzmaSamplingPreset{
		Temperature: float64Ptr(0),
	})
	if !cfg.HasTopK || cfg.TopK != 25 {
		t.Fatalf("expected fallback top_k=25, got has=%t top_k=%d", cfg.HasTopK, cfg.TopK)
	}
	if !cfg.HasTopP || cfg.TopP != 0.77 {
		t.Fatalf("expected fallback top_p=0.77, got has=%t top_p=%.4f", cfg.HasTopP, cfg.TopP)
	}
	if !cfg.HasMinP || cfg.MinP != 0.11 {
		t.Fatalf("expected fallback min_p=0.11, got has=%t min_p=%.4f", cfg.HasMinP, cfg.MinP)
	}
	if !cfg.HasRepeatPenalty || cfg.RepeatPenalty != 1.09 {
		t.Fatalf("expected fallback repeat penalty 1.09, got has=%t repeat=%.4f", cfg.HasRepeatPenalty, cfg.RepeatPenalty)
	}
	if cfg.MaxTokens != 333 {
		t.Fatalf("expected fallback max tokens 333, got %d", cfg.MaxTokens)
	}
	if !cfg.HasTemperature || cfg.Temperature != 0 {
		t.Fatalf("expected preset temperature override to 0, got has=%t temp=%.4f", cfg.HasTemperature, cfg.Temperature)
	}
}

func TestNormalizeHostedBaseURL(t *testing.T) {
	openAIBase, err := normalizeHostedBaseURL(config.ProviderOpenAI, "https://api.openai.com/v1/chat/completions")
	if err != nil {
		t.Fatalf("normalize openai endpoint failed: %v", err)
	}
	if openAIBase != "https://api.openai.com/v1" {
		t.Fatalf("unexpected openai base URL: %q", openAIBase)
	}

	anthropicBase, err := normalizeHostedBaseURL(config.ProviderAnthropic, "https://api.anthropic.com/v1/messages")
	if err != nil {
		t.Fatalf("normalize anthropic endpoint failed: %v", err)
	}
	if anthropicBase != "https://api.anthropic.com/v1" {
		t.Fatalf("unexpected anthropic base URL: %q", anthropicBase)
	}
}

func TestCompletionContentStringAndParts(t *testing.T) {
	resp1 := &anyllm.ChatCompletion{
		Choices: []anyllm.Choice{
			{Message: anyllm.Message{Content: "plain text"}},
		},
	}
	content, err := completionContent(resp1)
	if err != nil {
		t.Fatalf("completion content failed: %v", err)
	}
	if content != "plain text" {
		t.Fatalf("unexpected content: %q", content)
	}

	resp2 := &anyllm.ChatCompletion{
		Choices: []anyllm.Choice{
			{Message: anyllm.Message{
				Content: []anyllm.ContentPart{
					{Type: "text", Text: "hello "},
					{Type: "text", Text: "world"},
				},
			}},
		},
	}
	content, err = completionContent(resp2)
	if err != nil {
		t.Fatalf("completion content failed: %v", err)
	}
	if strings.TrimSpace(content) != "hello world" {
		t.Fatalf("unexpected content parts output: %q", content)
	}
}

func TestYZMAInitErrorFormatting(t *testing.T) {
	err := &YZMAInitError{
		Stage:    "backend_init",
		Reason:   "Metal backend could not initialize",
		Evidence: []string{"ggml_metal_init: failed to create command queue"},
	}
	msg := err.Error()
	if !strings.Contains(msg, "backend_init_failed") {
		t.Fatalf("expected stage marker in error, got %q", msg)
	}
	if !strings.Contains(msg, "ggml_metal_init: failed to create command queue") {
		t.Fatalf("expected evidence marker in error, got %q", msg)
	}
}

func TestAsYZMAInitError(t *testing.T) {
	source := &YZMAInitError{Stage: "backend_init", Reason: "failed"}
	wrapped := fmt.Errorf("outer: %w", source)
	unwrapped, ok := AsYZMAInitError(wrapped)
	if !ok {
		t.Fatal("expected wrapped error to expose YZMAInitError")
	}
	if unwrapped.Stage != "backend_init" {
		t.Fatalf("unexpected stage: %q", unwrapped.Stage)
	}
}
