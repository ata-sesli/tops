package llm

import (
	"strings"
	"testing"

	anyllm "github.com/mozilla-ai/any-llm-go"

	"tops/internal/config"
	"tops/internal/modelprofile"
)

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

func TestNewFromConfigLocalUsesOllamaAdapterDefaults(t *testing.T) {
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
	if provider.Name() != "local" {
		t.Fatalf("expected local provider name, got %q", provider.Name())
	}
}

func TestBuildOllamaChatRequestAppliesProfileOptions(t *testing.T) {
	req := buildOllamaChatRequest("llama3.1", CompletionRequest{
		SystemPrompt: "Base prompt",
		UserPrompt:   "hello",
		Temperature:  0.2,
		MaxTokens:    1200,
	}, modelprofile.ModelProfile{
		Provider:     config.ProviderOllama,
		Model:        "llama3.1",
		Context:      8192,
		MaxLength:    900,
		SystemPrompt: "Custom guidance",
		Think:        "off",
	})

	if got := req.Messages[0].Content; got != "Base prompt\n\nCustom guidance" {
		t.Fatalf("unexpected system prompt merge: %v", got)
	}
	if got := req.Options["num_ctx"]; got != 8192 {
		t.Fatalf("expected num_ctx=8192, got %v", got)
	}
	if got := req.Options["num_predict"]; got != 900 {
		t.Fatalf("expected num_predict=900 (profile override), got %v", got)
	}
	if req.Think == nil || req.Think.Value != false {
		t.Fatalf("expected think=false, got %#v", req.Think)
	}
	if req.Stream == nil || !*req.Stream {
		t.Fatalf("expected stream=true, got %#v", req.Stream)
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
