package semantic

import (
	"context"
	"strings"
	"testing"

	"tops/internal/model"
	"tops/internal/runtime/llm"
	"tops/internal/runtime/prompt"
)

func TestParseSemanticIntent_ExtractsFirstJSONObjectWithTrailingToolCall(t *testing.T) {
	raw := `{"version":"v1","operation":"summarize","entity":"os","scope":"system","target_path":".","recursion":"none","visibility":"visible_only","filters":{},"requested_fields":["os","kernel","version"],"projection":[],"sort":"","limit":0,"requires_grounding":true,"execution_strategy":"direct","ambiguity_notes":[]}
<tool_call>{"name":"run_readonly_command","arguments":{"command_name":"uname","args":["-a"]}}</tool_call>`

	intent, err := ParseSemanticIntent(raw)
	if err != nil {
		t.Fatalf("ParseSemanticIntent returned error: %v", err)
	}
	if intent.Entity != "os" {
		t.Fatalf("expected entity=os, got %q", intent.Entity)
	}
	if intent.Operation != "summarize" {
		t.Fatalf("expected operation=summarize, got %q", intent.Operation)
	}
}

func TestParseSemanticIntent_ParsesFencedJSON(t *testing.T) {
	raw := "```json\n{\"version\":\"v1\",\"operation\":\"inspect\",\"entity\":\"path\",\"scope\":\"current_directory\",\"target_path\":\".\",\"recursion\":\"none\",\"visibility\":\"visible_only\",\"filters\":{},\"requested_fields\":[],\"projection\":[],\"sort\":\"\",\"limit\":0,\"requires_grounding\":true,\"execution_strategy\":\"direct\",\"ambiguity_notes\":[]}\n```"

	intent, err := ParseSemanticIntent(raw)
	if err != nil {
		t.Fatalf("ParseSemanticIntent returned error: %v", err)
	}
	if intent.Entity != "path" {
		t.Fatalf("expected entity=path, got %q", intent.Entity)
	}
}

func TestParseSemanticIntent_IgnoresUnknownKeysInTolerantIngress(t *testing.T) {
	raw := `{"version":"v1","operation":"inspect","entity":"path","scope":"current_directory","target_path":".","recursion":"none","visibility":"visible_only","filters":{},"requested_fields":[],"projection":[],"sort":"","limit":0,"requires_grounding":true,"execution_strategy":"direct","ambiguity_notes":[],"unexpected_extra":"value"}`

	intent, err := ParseSemanticIntent(raw)
	if err != nil {
		t.Fatalf("ParseSemanticIntent returned error: %v", err)
	}
	if intent.Operation != "inspect" {
		t.Fatalf("expected operation=inspect, got %q", intent.Operation)
	}
}

func TestParseSemanticIntent_NormalizesCamelCaseKeys(t *testing.T) {
	raw := `{"version":"v1","operation":"inspect","entity":"path","scope":"current_directory","targetPath":"/tmp","recursion":"none","visibility":"visible_only","filters":{},"requestedFields":["os_version"],"projection":[],"sort":"","limit":0,"requiresGrounding":true,"executionStrategy":"direct","ambiguityNotes":[]}`

	intent, err := ParseSemanticIntent(raw)
	if err != nil {
		t.Fatalf("ParseSemanticIntent returned error: %v", err)
	}
	if intent.TargetPath != "/tmp" {
		t.Fatalf("expected target_path=/tmp, got %q", intent.TargetPath)
	}
	if len(intent.RequestedFields) != 1 || intent.RequestedFields[0] != "version" {
		t.Fatalf("expected requested_fields=[version], got %#v", intent.RequestedFields)
	}
}

func TestParseSemanticIntent_CoercesAmbiguityNotesString(t *testing.T) {
	raw := `{"version":"v1","operation":"inspect","entity":"path","scope":"current_directory","target_path":".","recursion":"none","visibility":"visible_only","filters":{},"requested_fields":[],"projection":[],"sort":"","limit":0,"requires_grounding":true,"execution_strategy":"direct","ambiguity_notes":"none"}`

	intent, err := ParseSemanticIntent(raw)
	if err != nil {
		t.Fatalf("ParseSemanticIntent returned error: %v", err)
	}
	if len(intent.AmbiguityNotes) != 1 || intent.AmbiguityNotes[0] != "none" {
		t.Fatalf("expected ambiguity_notes coerced to single-item array, got %#v", intent.AmbiguityNotes)
	}
}

func TestParseSemanticIntent_RejectsInvalidFieldTypes(t *testing.T) {
	raw := `{"version":"v1","operation":"inspect","entity":"path","scope":"current_directory","target_path":".","recursion":"none","visibility":"visible_only","filters":{},"requested_fields":[],"projection":[],"sort":"","limit":"ten","requires_grounding":true,"execution_strategy":"direct","ambiguity_notes":[]}`

	_, err := ParseSemanticIntent(raw)
	if err == nil {
		t.Fatal("expected ParseSemanticIntent to fail for invalid limit type")
	}
	if !strings.Contains(err.Error(), "limit must be integer") {
		t.Fatalf("expected limit type error, got: %v", err)
	}
}

func TestNormalizeUsesExplicitPlannerMaxTokens(t *testing.T) {
	var captured llm.CompletionRequest
	provider := llm.MockProvider{
		CompleteFn: func(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
			captured = req
			return llm.CompletionResponse{
				Content: `{"version":"v1","operation":"inspect","entity":"path","scope":"current_directory","target_path":".","recursion":"none","visibility":"visible_only","filters":{},"requested_fields":[],"projection":[],"sort":"","limit":0,"requires_grounding":true,"execution_strategy":"direct","ambiguity_notes":[]}`,
			}, nil
		},
	}
	normalizer := NewNormalizer(provider, prompt.NewBuilder())
	_, err := normalizer.Normalize(context.Background(), model.CoreRequest{
		Mode:  model.ModeAsk,
		Input: "show current directory",
	})
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}
	if captured.MaxTokens != 256 {
		t.Fatalf("expected semantic normalization max tokens=256, got %d", captured.MaxTokens)
	}
	if captured.SamplingProfile != llm.SamplingProfilePlanner {
		t.Fatalf("expected planner profile, got %q", captured.SamplingProfile)
	}
}

func TestNormalizeSkipsRepairForRoutineFastlaneIntent(t *testing.T) {
	callCount := 0
	provider := llm.MockProvider{
		CompleteFn: func(_ context.Context, _ llm.CompletionRequest) (llm.CompletionResponse, error) {
			callCount++
			return llm.CompletionResponse{Content: `{"broken": true`}, nil
		},
	}
	normalizer := NewNormalizer(provider, prompt.NewBuilder())
	intent, err := normalizer.Normalize(context.Background(), model.CoreRequest{
		Mode:  model.ModeAsk,
		Input: "what is my current directory?",
	})
	if err != nil {
		t.Fatalf("normalize should fall back for routine intent, got error: %v", err)
	}
	if callCount != 1 {
		t.Fatalf("expected one completion call with no repair retry, got %d", callCount)
	}
	if intent.Entity == "" {
		t.Fatal("expected fallback semantic intent to be returned")
	}
}
