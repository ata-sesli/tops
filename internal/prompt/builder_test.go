package prompt

import (
	"strings"
	"testing"

	"tops/internal/model"
)

func TestBuildAskPromptIncludesEvidence(t *testing.T) {
	req := model.CoreRequest{Input: "why is disk usage high?"}
	evidence := []model.ToolEvidence{{Command: "df -h", Stdout: "Filesystem 80%"}}
	system, user := NewBuilder().BuildAskPrompt(req, evidence)
	if !strings.Contains(system, "observed local evidence") {
		t.Fatalf("unexpected system prompt: %s", system)
	}
	if !strings.Contains(user, "df -h") || !strings.Contains(user, "Filesystem 80%") {
		t.Fatalf("evidence missing from user prompt: %s", user)
	}
}

func TestBuildGenPromptSchemaInstruction(t *testing.T) {
	req := model.CoreRequest{Input: "find .log files"}
	_, user := NewBuilder().BuildGenPrompt(req)
	for _, key := range []string{"command", "explanation", "intent_struct"} {
		if !strings.Contains(user, key) {
			t.Fatalf("expected key %s in prompt", key)
		}
	}
}

func TestBuildAskPlanningPromptIncludesFunctionSchema(t *testing.T) {
	req := model.CoreRequest{Input: "what OS am I using?"}
	_, user := NewBuilder().BuildAskPlanningPrompt(req)
	for _, key := range []string{"function_name", "function_args", "command_name", "args"} {
		if !strings.Contains(user, key) {
			t.Fatalf("expected planning prompt to mention %s", key)
		}
	}
}

func TestBuildGenPlanningPromptIncludesIDRule(t *testing.T) {
	req := model.CoreRequest{Input: "generate command"}
	_, user := NewBuilder().BuildGenPlanningPrompt(req)
	if !strings.Contains(user, "id must be string or number") {
		t.Fatalf("expected id normalization rule in planning prompt, got: %s", user)
	}
}
