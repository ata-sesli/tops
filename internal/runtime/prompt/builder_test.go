package prompt

import (
	"strings"
	"testing"

	"tops/internal/model"
)

func TestBuildAskPromptIncludesEvidence(t *testing.T) {
	req := model.CoreRequest{Input: "why is disk usage high?", AskResponseProfile: model.DefaultAskResponseProfile()}
	evidence := []model.ToolEvidence{{Command: "df -h", Stdout: "Filesystem 80%"}}
	system, user := NewBuilder().BuildAskPrompt(req, evidence)
	if !strings.Contains(system, "TOPS ask engine") {
		t.Fatalf("unexpected system prompt: %s", system)
	}
	if !strings.Contains(user, "df -h") || !strings.Contains(user, "Filesystem 80%") {
		t.Fatalf("evidence missing from user prompt: %s", user)
	}
	if !strings.Contains(user, "Return only a direct answer text") {
		t.Fatalf("expected answer-only ask rule in prompt, got: %s", user)
	}
}

func TestBuildGenPromptSchemaInstruction(t *testing.T) {
	req := model.CoreRequest{Input: "find .log files"}
	system, user := NewBuilder().BuildGenPrompt(req)
	for _, key := range []string{"command", "explanation", "intent_struct"} {
		if !strings.Contains(user, key) {
			t.Fatalf("expected key %s in prompt", key)
		}
	}
	for _, needle := range []string{"Return one JSON object only", "no markdown, no tool calls", "confidence_notes"} {
		if !strings.Contains(user, needle) {
			t.Fatalf("expected gen prompt to include %q, got: %s", needle, user)
		}
	}
	if !strings.Contains(system, "No self-instruction") {
		t.Fatalf("expected anti-self-instruction rule in system prompt, got: %s", system)
	}
}

func TestBuildAskPlanningPromptIncludesFunctionSchema(t *testing.T) {
	req := model.CoreRequest{Input: "what OS am I using?", AskResponseProfile: model.DefaultAskResponseProfile()}
	system, user := NewBuilder().BuildAskPlanningPrompt(req)
	for _, key := range []string{"run_readonly_command", "command_name", "args", "arguments must be a JSON object"} {
		if !strings.Contains(user, key) {
			t.Fatalf("expected planning prompt to mention %s", key)
		}
	}
	for _, needle := range []string{"Return native tool_calls only when executing tools", "Do not write prose", "run_readonly_command"} {
		if !strings.Contains(user, needle) {
			t.Fatalf("expected ask planning prompt to include %q, got: %s", needle, user)
		}
	}
	if !strings.Contains(system, "No self-instruction") {
		t.Fatalf("expected anti-self-instruction rule in system prompt, got: %s", system)
	}
}

func TestBuildAskPromptAnswerOnlyProfile(t *testing.T) {
	req := model.CoreRequest{
		Input: "what OS am I using?",
		AskResponseProfile: model.AskResponseProfile{
			Observations:  false,
			Inferences:    false,
			Uncertainties: false,
			Assumptions:   false,
			Notes:         false,
		},
	}
	_, user := NewBuilder().BuildAskPrompt(req, []model.ToolEvidence{{Command: "uname -srm", Stdout: "Darwin 24.5.0 arm64"}})
	if !strings.Contains(user, "Keep it concise and answer-only.") {
		t.Fatalf("expected answer-only composition guidance, got: %s", user)
	}
}

func TestBuildGenPlanningPromptIncludesIDRule(t *testing.T) {
	req := model.CoreRequest{Input: "generate command"}
	_, user := NewBuilder().BuildGenPlanningPrompt(req)
	for _, needle := range []string{"Protocol for planner call", "Generation JSON keys", "Never mix prose/content with tool_calls"} {
		if !strings.Contains(user, needle) {
			t.Fatalf("expected planning prompt to include %q, got: %s", needle, user)
		}
	}
}

func TestBuildHelpPromptIncludesArrayRules(t *testing.T) {
	req := model.CoreRequest{Input: "ls"}
	system, user := NewBuilder().BuildHelpPrompt(req, nil)
	for _, needle := range []string{"list fields must be arrays", "max 6 items per list", "\"notes\":[\"Output depends on the target directory.\"]"} {
		if !strings.Contains(user, needle) {
			t.Fatalf("expected help prompt to include %q, got: %s", needle, user)
		}
	}
	if !strings.Contains(system, "No self-instruction") {
		t.Fatalf("expected anti-self-instruction rule in system prompt, got: %s", system)
	}
}
