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
	system := "You are TOPS help engine. Summarize only from supplied local docs. Return exactly one JSON object and nothing else. Do not invent unsupported flags. Do not include self-instruction, reasoning narration, schema commentary, or prompt restatement."
	user := fmt.Sprintf("Target command/snippet: %s\n\nLocal evidence:\n%s\n\nReturn exactly one JSON object with keys: summary, syntax, important_flags, examples, caveats, assumptions, notes.\nRules:\n- important_flags, examples, caveats, assumptions, and notes must always be JSON arrays of strings.\n- Use [] for empty lists. Never use null, a string, a number, or an object for list fields.\n- If there is one note, return notes as [\"...\"] not \"...\".\n- Keep values concise and evidence-grounded.\nExample:\n{\"summary\":\"Lists files.\",\"syntax\":\"ls [flags] [path]\",\"important_flags\":[\"-l long format\"],\"examples\":[\"ls -la\"],\"caveats\":[],\"assumptions\":[],\"notes\":[\"Output depends on the target directory.\"]}", req.Input, renderEvidence(evidence))
	return system, user
}

func (Builder) BuildGenPrompt(req model.CoreRequest) (string, string) {
	system := "You are TOPS generation engine. Convert natural language into safe shell command candidates. Return exactly one JSON object and nothing else. Do not include self-instruction, reasoning narration, schema commentary, or prompt restatement."
	user := fmt.Sprintf("User request: %s\n\nReturn exactly one JSON object with keys: command, explanation, intent_struct, assumptions, ambiguities, confidence_notes. intent_struct keys: intent, constraints, action.\nRules:\n- assumptions, ambiguities, and confidence_notes must always be JSON arrays of strings.\n- Use [] for empty lists. Never use null, a string, a number, or an object for list fields.\n- If enough information is available, emit the final JSON immediately.\n- Do not narrate that you are about to output JSON.\nExample:\n{\"command\":\"find . -name '*.log'\",\"explanation\":\"Find log files under the current directory.\",\"intent_struct\":{\"intent\":\"find log files\",\"constraints\":{},\"action\":\"search\"},\"assumptions\":[\"Run from the target directory.\"],\"ambiguities\":[],\"confidence_notes\":[\"Uses standard find syntax.\"]}", req.Input)
	return system, user
}

func (Builder) BuildGenPlanningPrompt(req model.CoreRequest) (string, string) {
	system := "You are TOPS generation planner. Return exactly one JSON object and nothing else. Do not include self-instruction, reasoning narration, schema commentary, or prompt restatement."
	user := fmt.Sprintf(
		"User request: %s\n\nReturn exactly one JSON object that is either:\n1) Final generation JSON with keys: command, explanation, intent_struct, assumptions, ambiguities, confidence_notes.\nOR\n2) Workflow plan JSON with key workflow_plan.\n\nWorkflow plan schema (preferred):\n{\n  \"workflow_plan\": {\n    \"reason\": \"Need local evidence.\",\n    \"steps\": [\n      {\n        \"id\": \"s1\",\n        \"intent\": \"Get operating system information\",\n        \"function_name\": \"get_os_info\",\n        \"function_args\": {},\n        \"expected_evidence\": \"OS name and version\"\n      }\n    ]\n  }\n}\n\nFinal generation example:\n{\"command\":\"pwd\",\"explanation\":\"Show the current directory.\",\"intent_struct\":{\"intent\":\"show current directory\",\"constraints\":{},\"action\":\"inspect\"},\"assumptions\":[\"pwd is available.\"],\"ambiguities\":[],\"confidence_notes\":[\"Does not modify the system.\"]}\n\nLegacy command-step schema (still accepted):\n{\n  \"id\": \"s1\",\n  \"intent\": \"...\",\n  \"command_name\": \"pwd\",\n  \"args\": [],\n  \"expected_evidence\": \"...\"\n}\n\nRules:\n- id must be string or number.\n- args must be array of strings when using command_name.\n- assumptions, ambiguities, and confidence_notes must always be JSON arrays of strings.\n- Prefer function_name/function_args for workflow steps.\n- Use workflow plans only when local read-only evidence is required to improve correctness.\n- If enough information is available, emit the final JSON immediately.\n- Do not narrate why you are about to produce JSON.",
		req.Input,
	)
	return system, user
}

func (Builder) BuildAskPrompt(req model.CoreRequest, evidence []model.ToolEvidence) (string, string) {
	system := "You are TOPS ask engine. Answer using observed local evidence first, and clearly separate observations from inference. Return exactly one JSON object and nothing else. Do not include self-instruction, reasoning narration, schema commentary, or prompt restatement."
	user := fmt.Sprintf("Question: %s\n\nLocal inspection evidence:\n%s\n\nReturn exactly one JSON object with keys: answer, observations, inferences, uncertainties, assumptions, notes.\nRules:\n- observations, inferences, uncertainties, assumptions, and notes must always be JSON arrays of strings.\n- Use [] for empty lists. Never use null, a string, a number, or an object for list fields.\n- If there is one note, return notes as [\"...\"] not \"...\".\n- If enough evidence is available, emit the final JSON immediately.\n- Do not narrate that you are about to output JSON.\nExample:\n{\"answer\":\"You are running macOS on arm64.\",\"observations\":[\"uname reported Darwin 24.5.0 arm64\"],\"inferences\":[\"Darwin indicates macOS.\"],\"uncertainties\":[],\"assumptions\":[],\"notes\":[\"Architecture is arm64.\"]}", req.Input, renderEvidence(evidence))
	return system, user
}

func (Builder) BuildAskPlanningPrompt(req model.CoreRequest) (string, string) {
	system := "You are TOPS ask planner. Return exactly one JSON object and nothing else. Do not include self-instruction, reasoning narration, schema commentary, or prompt restatement."
	user := fmt.Sprintf(
		"Question: %s\n\nReturn exactly one JSON object that is either:\n1) Final ask JSON with keys: answer, observations, inferences, uncertainties, assumptions, notes.\nOR\n2) Workflow plan JSON with key workflow_plan.\n\nWorkflow plan schema (preferred):\n{\n  \"workflow_plan\": {\n    \"reason\": \"Need local evidence.\",\n    \"steps\": [\n      {\n        \"id\": \"s1\",\n        \"intent\": \"Get operating system information\",\n        \"function_name\": \"get_os_info\",\n        \"function_args\": {},\n        \"expected_evidence\": \"OS name and version\"\n      }\n    ]\n  }\n}\n\nFinal ask example:\n{\"answer\":\"You are running macOS on arm64.\",\"observations\":[\"uname reported Darwin 24.5.0 arm64\"],\"inferences\":[\"Darwin indicates macOS.\"],\"uncertainties\":[],\"assumptions\":[],\"notes\":[\"Architecture is arm64.\"]}\n\nLegacy command-step schema (still accepted):\n{\n  \"id\": \"s1\",\n  \"intent\": \"...\",\n  \"command_name\": \"pwd\",\n  \"args\": [],\n  \"expected_evidence\": \"...\"\n}\n\nRules:\n- id must be string or number.\n- args must be array of strings when using command_name.\n- observations, inferences, uncertainties, assumptions, and notes must always be JSON arrays of strings.\n- Prefer function_name/function_args for workflow steps.\n- Use workflow plans only when local read-only evidence is required.\n- If enough information is available, emit the final JSON immediately.\n- Do not narrate why you are about to produce JSON.",
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
