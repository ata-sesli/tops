package genintent

import (
	"context"
	"strings"
	"testing"

	"tops/internal/model"
	"tops/internal/runtime/llm"
	"tops/internal/runtime/prompt"
)

func TestParseGenIntent_AliasNeedsCurrentEnvironment(t *testing.T) {
	raw := `{"version":"v1","goal":"list files","output_kind":"single_command","target_shell":"zsh","platform_scope":"current_platform","requires_grounding":true,"requested_constraints":[],"safety_notes":[],"ambiguity_notes":[],"needs_current_environment":true}`

	intent, err := ParseGenIntent(raw)
	if err != nil {
		t.Fatalf("ParseGenIntent returned error: %v", err)
	}
	if !intent.NeedsCurrentEnvironmentContext {
		t.Fatal("expected needs_current_environment_context=true after alias mapping")
	}
}

func TestParseGenIntent_ParsesFencedJSON(t *testing.T) {
	raw := "```json\n{\"version\":\"v1\",\"goal\":\"list files\",\"output_kind\":\"single_command\",\"target_shell\":\"zsh\",\"platform_scope\":\"current_platform\",\"requires_grounding\":false,\"requested_constraints\":[],\"safety_notes\":[],\"ambiguity_notes\":[],\"needs_current_environment_context\":false}\n```"

	intent, err := ParseGenIntent(raw)
	if err != nil {
		t.Fatalf("ParseGenIntent returned error: %v", err)
	}
	if intent.Goal != "list files" {
		t.Fatalf("expected goal=list files, got %q", intent.Goal)
	}
}

func TestParseGenIntent_ExtractsFirstJSONObjectWithTrailingToolCall(t *testing.T) {
	raw := `{"version":"v1","goal":"print cwd","output_kind":"single_command","target_shell":"zsh","platform_scope":"current_platform","requires_grounding":false,"requested_constraints":[],"safety_notes":[],"ambiguity_notes":[],"needs_current_environment_context":false}
<tool_call>{"name":"run_readonly_command","arguments":{"command_name":"pwd","args":[]}}</tool_call>`

	intent, err := ParseGenIntent(raw)
	if err != nil {
		t.Fatalf("ParseGenIntent returned error: %v", err)
	}
	if intent.Goal != "print cwd" {
		t.Fatalf("expected goal=print cwd, got %q", intent.Goal)
	}
}

func TestParseGenIntent_IgnoresUnknownKeysInTolerantIngress(t *testing.T) {
	raw := `{"version":"v1","goal":"list files","output_kind":"single_command","target_shell":"zsh","platform_scope":"current_platform","requires_grounding":false,"requested_constraints":[],"safety_notes":[],"ambiguity_notes":[],"needs_current_environment_context":false,"unexpected_extra":"value"}`

	intent, err := ParseGenIntent(raw)
	if err != nil {
		t.Fatalf("ParseGenIntent returned error: %v", err)
	}
	if intent.Goal != "list files" {
		t.Fatalf("expected goal=list files, got %q", intent.Goal)
	}
}

func TestParseGenIntent_NormalizesCamelCaseKeys(t *testing.T) {
	raw := `{"version":"v1","goal":"show files","outputKind":"multi_command","targetShell":"zsh","platformScope":"current_platform","requiresGrounding":false,"requestedConstraints":"safe mode","safetyNotes":null,"ambiguityNotes":[],"needsCurrentEnvironmentContext":false}`

	intent, err := ParseGenIntent(raw)
	if err != nil {
		t.Fatalf("ParseGenIntent returned error: %v", err)
	}
	if intent.OutputKind != "multi_command" {
		t.Fatalf("expected output_kind=multi_command, got %q", intent.OutputKind)
	}
	if len(intent.RequestedConstraints) != 1 || intent.RequestedConstraints[0] != "safe mode" {
		t.Fatalf("expected requested_constraints=[safe mode], got %#v", intent.RequestedConstraints)
	}
}

func TestParseGenIntent_RejectsInvalidFieldTypes(t *testing.T) {
	raw := `{"version":"v1","goal":"list files","output_kind":"single_command","target_shell":"zsh","platform_scope":"current_platform","requires_grounding":"sometimes","requested_constraints":[],"safety_notes":[],"ambiguity_notes":[],"needs_current_environment_context":false}`

	_, err := ParseGenIntent(raw)
	if err == nil {
		t.Fatal("expected ParseGenIntent to fail for invalid requires_grounding type")
	}
	if !strings.Contains(err.Error(), "requires_grounding must be boolean") {
		t.Fatalf("expected requires_grounding type error, got: %v", err)
	}
}

func TestNormalizeUsesExplicitPlannerMaxTokens(t *testing.T) {
	var captured llm.CompletionRequest
	provider := llm.MockProvider{
		CompleteFn: func(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
			captured = req
			return llm.CompletionResponse{
				Content: `{"version":"v1","goal":"list files","output_kind":"single_command","target_shell":"zsh","platform_scope":"current_platform","requires_grounding":false,"requested_constraints":[],"safety_notes":[],"ambiguity_notes":[],"needs_current_environment_context":false}`,
			}, nil
		},
	}
	normalizer := NewNormalizer(provider, prompt.NewBuilder())
	_, err := normalizer.Normalize(context.Background(), model.CoreRequest{
		Mode:  model.ModeGen,
		Input: "list files",
	})
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}
	if captured.MaxTokens != 256 {
		t.Fatalf("expected gen intent normalization max tokens=256, got %d", captured.MaxTokens)
	}
	if captured.SamplingProfile != llm.SamplingProfilePlanner {
		t.Fatalf("expected planner profile, got %q", captured.SamplingProfile)
	}
}
