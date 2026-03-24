package prompt

import (
	"fmt"
	"strings"

	"tops/internal/model"
)

type Builder struct{}

func NewBuilder() Builder {
	return Builder{}
}

func (Builder) BuildHelpPrompt(req model.CoreRequest, evidence []model.ToolEvidence) (string, string) {
	system := "You are TOPS help engine. Summarize only from supplied local docs. Return strict JSON. Do not invent unsupported flags."
	user := fmt.Sprintf("Target command/snippet: %s\n\nLocal evidence:\n%s\n\nReturn JSON object with keys: summary, syntax, important_flags, examples, caveats, assumptions, notes.", req.Input, renderEvidence(evidence))
	return system, user
}

func (Builder) BuildGenPrompt(req model.CoreRequest) (string, string) {
	system := "You are TOPS generation engine. Convert natural language into safe shell command candidates. Return strict JSON only."
	user := fmt.Sprintf("User request: %s\n\nReturn JSON object with keys: command, explanation, intent_struct, assumptions, ambiguities, confidence_notes. intent_struct keys: intent, constraints, action.", req.Input)
	return system, user
}

func (Builder) BuildGenPlanningPrompt(req model.CoreRequest) (string, string) {
	system := "You are TOPS generation planner. Return strict JSON only."
	user := fmt.Sprintf(
		"User request: %s\n\nReturn either:\n1) Final generation JSON object with keys: command, explanation, intent_struct, assumptions, ambiguities, confidence_notes.\nOR\n2) Workflow plan JSON object with key workflow_plan.\n\nWorkflow plan schema (preferred):\n{\n  \"workflow_plan\": {\n    \"reason\": \"...\",\n    \"steps\": [\n      {\n        \"id\": \"s1\",\n        \"intent\": \"...\",\n        \"function_name\": \"get_os_info\",\n        \"function_args\": {},\n        \"expected_evidence\": \"...\"\n      }\n    ]\n  }\n}\n\nLegacy command-step schema (still accepted):\n{\n  \"id\": \"s1\",\n  \"intent\": \"...\",\n  \"command_name\": \"pwd\",\n  \"args\": [],\n  \"expected_evidence\": \"...\"\n}\n\nRules:\n- id must be string or number.\n- args must be array of strings when using command_name.\n- Prefer function_name/function_args for workflow steps.\n- Use workflow plans only when local read-only evidence is required to improve correctness.",
		req.Input,
	)
	return system, user
}

func (Builder) BuildAskPrompt(req model.CoreRequest, evidence []model.ToolEvidence) (string, string) {
	system := "You are TOPS ask engine. Answer using observed local evidence first, and clearly separate observations from inference. Return strict JSON only."
	user := fmt.Sprintf("Question: %s\n\nLocal inspection evidence:\n%s\n\nReturn JSON object with keys: answer, observations, inferences, uncertainties, assumptions, notes.", req.Input, renderEvidence(evidence))
	return system, user
}

func (Builder) BuildAskPlanningPrompt(req model.CoreRequest) (string, string) {
	system := "You are TOPS ask planner. Return strict JSON only."
	user := fmt.Sprintf(
		"Question: %s\n\nReturn either:\n1) Final ask JSON object with keys: answer, observations, inferences, uncertainties, assumptions, notes.\nOR\n2) Workflow plan JSON object with key workflow_plan.\n\nWorkflow plan schema (preferred):\n{\n  \"workflow_plan\": {\n    \"reason\": \"...\",\n    \"steps\": [\n      {\n        \"id\": \"s1\",\n        \"intent\": \"...\",\n        \"function_name\": \"get_os_info\",\n        \"function_args\": {},\n        \"expected_evidence\": \"...\"\n      }\n    ]\n  }\n}\n\nLegacy command-step schema (still accepted):\n{\n  \"id\": \"s1\",\n  \"intent\": \"...\",\n  \"command_name\": \"pwd\",\n  \"args\": [],\n  \"expected_evidence\": \"...\"\n}\n\nRules:\n- id must be string or number.\n- args must be array of strings when using command_name.\n- Prefer function_name/function_args for workflow steps.\n- Use workflow plans only when local read-only evidence is required.",
		req.Input,
	)
	return system, user
}

func renderEvidence(evidence []model.ToolEvidence) string {
	if len(evidence) == 0 {
		return "(no evidence captured)"
	}
	var b strings.Builder
	for _, item := range evidence {
		fmt.Fprintf(&b, "- command: %s\n", item.Command)
		fmt.Fprintf(&b, "  exit_code: %d\n", item.ExitCode)
		if item.Stdout != "" {
			stdout := item.Stdout
			if len(stdout) > 1200 {
				stdout = stdout[:1200] + "..."
			}
			fmt.Fprintf(&b, "  stdout:\n%s\n", indent(stdout, "    "))
		}
		if item.Stderr != "" {
			stderr := item.Stderr
			if len(stderr) > 600 {
				stderr = stderr[:600] + "..."
			}
			fmt.Fprintf(&b, "  stderr:\n%s\n", indent(stderr, "    "))
		}
	}
	return b.String()
}

func indent(input, prefix string) string {
	lines := strings.Split(input, "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}
