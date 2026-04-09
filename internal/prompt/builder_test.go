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
	if !strings.Contains(system, "observed local evidence") {
		t.Fatalf("unexpected system prompt: %s", system)
	}
	if !strings.Contains(user, "df -h") || !strings.Contains(user, "Filesystem 80%") {
		t.Fatalf("evidence missing from user prompt: %s", user)
	}
	if !strings.Contains(user, "keys: answer, observations, inferences, uncertainties, assumptions, notes") {
		t.Fatalf("expected full ask keys in prompt, got: %s", user)
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
	for _, needle := range []string{"must always be JSON arrays of strings", "Do not narrate", "confidence_notes\":[\""} {
		if !strings.Contains(user, needle) {
			t.Fatalf("expected gen prompt to include %q, got: %s", needle, user)
		}
	}
	if !strings.Contains(system, "Do not include self-instruction") {
		t.Fatalf("expected anti-self-instruction rule in system prompt, got: %s", system)
	}
}

func TestBuildAskPlanningPromptIncludesFunctionSchema(t *testing.T) {
	req := model.CoreRequest{Input: "what OS am I using?", AskResponseProfile: model.DefaultAskResponseProfile()}
	system, user := NewBuilder().BuildAskPlanningPrompt(req)
	for _, key := range []string{"function_name", "function_args", "command_name", "args"} {
		if !strings.Contains(user, key) {
			t.Fatalf("expected planning prompt to mention %s", key)
		}
	}
	for _, needle := range []string{"Return exactly one JSON object", "Do not narrate why you are about to produce JSON", "\"notes\":[\"Architecture is arm64.\"]"} {
		if !strings.Contains(user, needle) {
			t.Fatalf("expected ask planning prompt to include %q, got: %s", needle, user)
		}
	}
	if !strings.Contains(system, "Do not include self-instruction") {
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
	if !strings.Contains(user, "keys: answer") {
		t.Fatalf("expected answer-only keys, got: %s", user)
	}
	if strings.Contains(user, "observations") {
		t.Fatalf("did not expect optional ask keys in answer-only prompt, got: %s", user)
	}
	if !strings.Contains(user, `{"answer":"You are running macOS on arm64."}`) {
		t.Fatalf("expected answer-only example, got: %s", user)
	}
}

func TestBuildGenPlanningPromptIncludesIDRule(t *testing.T) {
	req := model.CoreRequest{Input: "generate command"}
	_, user := NewBuilder().BuildGenPlanningPrompt(req)
	for _, needle := range []string{"id must be string or number", "assumptions, ambiguities, and confidence_notes must always be JSON arrays of strings", "emit the final JSON immediately"} {
		if !strings.Contains(user, needle) {
			t.Fatalf("expected planning prompt to include %q, got: %s", needle, user)
		}
	}
}

func TestBuildHelpPromptIncludesArrayRules(t *testing.T) {
	req := model.CoreRequest{Input: "ls"}
	system, user := NewBuilder().BuildHelpPrompt(req, nil)
	for _, needle := range []string{"important_flags, examples, caveats, assumptions, and notes must always be JSON arrays of strings", "notes as [\"...\"] not \"...\"", "\"notes\":[\"Output depends on the target directory.\"]"} {
		if !strings.Contains(user, needle) {
			t.Fatalf("expected help prompt to include %q, got: %s", needle, user)
		}
	}
	if !strings.Contains(system, "Do not include self-instruction") {
		t.Fatalf("expected anti-self-instruction rule in system prompt, got: %s", system)
	}
}
