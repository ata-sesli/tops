package prompt

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"tops/internal/model"
	"tops/internal/runtime/commandcatalog"
	"tops/internal/runtime/optimization"
)

type Builder struct {
	opt optimization.Config
}

func NewBuilder(opts ...optimization.Config) Builder {
	b := Builder{opt: optimization.Default()}
	if len(opts) > 0 {
		b.opt = opts[0]
	}
	return b
}

func (b Builder) BuildHelpPrompt(req model.CoreRequest, evidence []model.ToolEvidence) (string, string) {
	system := strictSystem("TOPS help engine")
	user := fmt.Sprintf("Target: %s\nEvidence:\n%s\nReturn JSON keys: summary, syntax, important_flags, examples, caveats, assumptions, notes.\nRules: list fields must be arrays; [] when empty; max 6 items per list; keep each item under 120 chars.\nStrict grounding: do not invent flags/options/behavior absent from evidence.\nExamples policy: include at most 2 practical examples.\nExample: {\"summary\":\"Lists files.\",\"syntax\":\"ls [flags] [path]\",\"important_flags\":[\"-l long format\"],\"examples\":[\"ls -la\"],\"caveats\":[],\"assumptions\":[],\"notes\":[\"Output depends on the target directory.\"]}", req.Input, renderEvidence(evidence))
	return system, user
}

func (b Builder) BuildGenPrompt(req model.CoreRequest) (string, string) {
	return b.BuildGenFinalPromptWithIntent(req, model.DefaultGenIntent())
}

func (b Builder) BuildGenIntentPrompt(req model.CoreRequest) (string, string) {
	system := strictSystem("TOPS gen intent normalizer")
	user := fmt.Sprintf(
		"Mode: %s\nRequest: %s\nRuntime shell: %s\nRuntime CWD: %s\nPlatformContext JSON: %s\nReturn exactly one GenIntent JSON object with keys:\nversion, goal, output_kind, target_shell, platform_scope, requires_grounding, requested_constraints, safety_notes, ambiguity_notes, needs_current_environment_context.\nAllowed enums:\n- output_kind: single_command|multi_command|shell_script\n- target_shell: bash|zsh|sh|powershell|unknown\n- platform_scope: current_platform|portable|unspecified\nProduct boundary:\n- gen is command/script authoring only.\n- do not treat this as ask (environment fact Q&A).\n- do not treat this as help (built-in help explanation).\nStrict grounding policy:\n- requires_grounding=true only when current local files/paths/state, installed tools/versions, or environment facts materially change the generated artifact.\n- if platform/shell is already covered by runtime context, keep requires_grounding=false.\nOutput-kind policy:\n- single_command when one command is enough.\n- multi_command for short ordered steps.\n- shell_script only for loops/conditionals/reusable dependent flow.\nDo not emit tool_calls, XML tags, or multiple JSON objects.\nNo markdown. No explanation.",
		req.Mode,
		req.Input,
		req.Shell,
		req.CWD,
		renderPlatformContextJSON(req.PlatformContext),
	)
	return system, user
}

func (b Builder) BuildGenPlanningPrompt(req model.CoreRequest) (string, string) {
	return b.BuildGenPlanningPromptWithIntent(req, model.DefaultGenIntent())
}

func (b Builder) BuildGenPlanningPromptWithIntent(req model.CoreRequest, intent model.GenIntent) (string, string) {
	system := strictTaggedSystem("TOPS generation planner")
	user := fmt.Sprintf(
		"Request: %s\nRuntime shell: %s\nRuntime CWD: %s\nPlatformContext JSON: %s\nGenIntent JSON (authoritative): %s\nTOOLS\n%s\nProtocol for planner call:\n- If GenIntent.requires_grounding=true: return native tool_calls only and empty content.\n- If GenIntent.requires_grounding=false: return generation JSON content only with no tool_calls.\n- Never mix prose/content with tool_calls.\nGeneration JSON keys: command, explanation, intent_struct, output_kind, target_shell, assumptions, ambiguities, confidence_notes.\nRules:\n- gen is authoring-only: produce commands/scripts, not environment Q&A and not help explanations.\n- command is required and must be the usable generated artifact (single command, multi-command text, or script text).\n- choose the smallest sufficient artifact for the request.\n- output_kind follows GenIntent unless evidence requires a stricter form.\n- keep explanation concise.\n- no markdown.",
		req.Input,
		req.Shell,
		req.CWD,
		renderPlatformContextJSON(req.PlatformContext),
		renderGenIntentJSON(intent),
		compactAskToolsList(),
	)
	return system, user
}

func (b Builder) BuildGenFinalPromptWithIntent(req model.CoreRequest, intent model.GenIntent) (string, string) {
	system := strictSystem("TOPS generation finalizer")
	user := fmt.Sprintf(
		"Request: %s\nRuntime shell: %s\nRuntime CWD: %s\nPlatformContext JSON: %s\nGenIntent JSON (authoritative): %s\nReturn one JSON object only with keys: command, explanation, intent_struct, output_kind, target_shell, assumptions, ambiguities, confidence_notes.\nRules:\n- gen is authoring-only: do not answer as ask/help.\n- command is required and is the generated artifact.\n- choose the smallest sufficient artifact for the request.\n- output_kind in {single_command,multi_command,shell_script}.\n- target_shell in {bash,zsh,sh,powershell,unknown}.\n- concise explanation only.\n- no markdown, no tool calls, no extra narration.",
		req.Input,
		req.Shell,
		req.CWD,
		renderPlatformContextJSON(req.PlatformContext),
		renderGenIntentJSON(intent),
	)
	return system, user
}

func (b Builder) BuildGenLegacyPlanningPromptWithIntent(req model.CoreRequest, intent model.GenIntent) (string, string) {
	system := strictSystem("TOPS generation planner (legacy)")
	user := fmt.Sprintf(
		"Request: %s\nRuntime shell: %s\nRuntime CWD: %s\nPlatformContext JSON: %s\nGenIntent JSON (authoritative): %s\nFor legacy non-native providers return one JSON object only:\nA) final generation JSON keys command, explanation, intent_struct, output_kind, target_shell, assumptions, ambiguities, confidence_notes\nor\nB) workflow_plan JSON with steps that call run_readonly_command.\nWorkflow step schema: {\"id\":\"s1\",\"intent\":\"...\",\"function_name\":\"run_readonly_command\",\"function_args\":{\"command_name\":\"find\",\"args\":[\".\",\"-maxdepth\",\"1\"],\"output_line_limit\":200},\"expected_evidence\":\"...\"}\nRules:\n- gen is authoring-only: do not answer as ask/help.\n- If GenIntent.requires_grounding=true prefer workflow_plan.\n- command must be generated artifact text.\n- no markdown.",
		req.Input,
		req.Shell,
		req.CWD,
		renderPlatformContextJSON(req.PlatformContext),
		renderGenIntentJSON(intent),
	)
	return system, user
}

func (b Builder) BuildAskPrompt(req model.CoreRequest, evidence []model.ToolEvidence) (string, string) {
	return b.BuildAskPromptWithIntent(req, model.DefaultSemanticIntent(), evidence)
}

func (b Builder) BuildAskPromptWithIntent(req model.CoreRequest, intent model.SemanticIntent, evidence []model.ToolEvidence) (string, string) {
	profile := req.AskResponseProfile
	system := strictTaggedSystem("TOPS ask engine")
	user := fmt.Sprintf("Question: %s\nRuntime CWD: %s\nPlatformContext JSON: %s\nSemanticIntent JSON (authoritative): %s\nTOOLS\n%s\nEvidence JSON (compact): %s\nReturn only a direct answer text.\nRules: no JSON, no tags, no markdown, no tool calls, no extra scaffolding.%s", req.Input, req.CWD, renderPlatformContextJSON(req.PlatformContext), renderSemanticIntentJSON(intent), compactAskToolsList(), renderEvidenceJSONCompact(evidence), askCompositionGuidance(profile))
	_ = profile
	return system, user
}

func (b Builder) BuildAskPlanningPrompt(req model.CoreRequest) (string, string) {
	return b.BuildAskLoopPromptWithIntent(req, model.DefaultSemanticIntent(), nil, 6)
}

func (b Builder) BuildAskCompactPlanningPrompt(req model.CoreRequest) (string, string) {
	system := strictTaggedSystem("TOPS ask planner (compact)")
	user := fmt.Sprintf(
		"Question: %s\nRuntime CWD: %s\nPlatformContext JSON: %s\nYou answer local environment questions using local evidence.\nWhen evidence is needed, call run_readonly_command via native tool_calls.\nTool-call protocol:\n- Return tool_calls only with empty content.\n- No prose before tool calls.\n- Prefer one minimal command that directly grounds the question.\nIf no tool call is needed, return a concise plain-text answer.\nAnswer only from runtime context and tool evidence.\nNo markdown.",
		req.Input,
		req.CWD,
		renderPlatformContextJSON(req.PlatformContext),
	)
	return system, user
}

func (b Builder) BuildAskLoopPrompt(req model.CoreRequest, evidence []model.ToolEvidence, remainingSteps int) (string, string) {
	return b.BuildAskLoopPromptWithIntent(req, model.DefaultSemanticIntent(), evidence, remainingSteps)
}

func (b Builder) BuildAskGroundedPlanPrompt(req model.CoreRequest, intent model.SemanticIntent, evidence []model.ToolEvidence, plannerContext string) (string, string) {
	system := strictSystem("TOPS ask grounded planner")
	evidenceText := "[]"
	if len(evidence) > 0 {
		evidenceText = renderEvidenceJSONCompact(evidence)
	}
	contextText := strings.TrimSpace(plannerContext)
	if contextText == "" {
		contextText = "(none)"
	}
	user := fmt.Sprintf(
		"Question: %s\nRuntime CWD: %s\nPlatformContext JSON: %s\nSemanticIntent JSON (authoritative): %s\nEvidence JSON (compact): %s\nPlanner context: %s\nCreate an upfront workflow plan for grounded ask.\nReturn exactly one JSON object with shape:\n{\"workflow_plan\":{\"reason\":\"...\",\"steps\":[{\"id\":\"s1\",\"intent\":\"...\",\"function_name\":\"run_readonly_command\",\"function_args\":{\"command_name\":\"uname\",\"args\":[\"-srm\"],\"output_line_limit\":200},\"expected_evidence\":\"...\"}]},\"effective_requires_grounding\":true}\nRules:\n- Plan-first behavior: provide tool steps before final answering.\n- For local/evidence questions include at least one step.\n- Use only function_name=run_readonly_command.\n- function_args must be a JSON object.\n- command_name is executable only; flags/tokens go in args.\n- You may set effective_requires_grounding=false only if no local evidence is needed and provide grounding_override_reason.\n- No markdown. No prose. No tool_calls.",
		req.Input,
		req.CWD,
		renderPlatformContextJSON(req.PlatformContext),
		renderSemanticIntentJSON(intent),
		evidenceText,
		contextText,
	)
	return system, user
}

func (b Builder) BuildAskLoopPromptWithIntent(req model.CoreRequest, intent model.SemanticIntent, evidence []model.ToolEvidence, remainingSteps int) (string, string) {
	profile := req.AskResponseProfile
	system := strictTaggedSystem("TOPS ask planner")
	if remainingSteps <= 0 {
		remainingSteps = 1
	}
	evidenceText := "[]"
	if len(evidence) > 0 {
		evidenceText = renderEvidenceJSONCompact(evidence)
	}
	toolHint := ""
	if len(evidence) == 0 {
		toolHint = askLocalToolSelectionHint(req.Input)
	}
	if toolHint != "" {
		toolHint = "\nTool-selection hints:\n" + toolHint
	}
	user := fmt.Sprintf(
		"Question: %s\nRuntime CWD: %s\nPlatformContext JSON: %s\nSemanticIntent JSON (authoritative): %s\nTOOLS\n%s\nEvidence JSON (compact): %s\nRemaining steps: %d\nPlanner behavior for /ask:\n- Evidence-first grounding.\n- Select run_readonly_command tool calls when environment evidence is needed.\n- If requires_grounding is true, at least one tool call must execute unless explicitly overridden.\n- If requested_fields has multiple attributes, continue selecting tool_calls until each requested field is grounded.\n- Prefer one command that grounds all missing requested_fields when possible (example: uname with args [-srm] for os/kernel/version).\n- Override format (only when no tool call is necessary): emit JSON content with keys effective_requires_grounding=false and grounding_override_reason.\n- Return native tool_calls only when executing tools.\n- Do not write FINAL.\n- Do not write prose.\nProtocol rules:\n- function_name must be run_readonly_command.\n- arguments must be a JSON object.\n- command_name is only the executable name (example: uname); place flags/tokens in args array.\n- no markdown, no explanation.%s",
		req.Input,
		req.CWD,
		renderPlatformContextJSON(req.PlatformContext),
		renderSemanticIntentJSON(intent),
		compactAskToolsList(),
		evidenceText,
		remainingSteps,
		toolHint,
	)
	_ = profile
	return system, user
}

func (b Builder) BuildAskFinalPrompt(req model.CoreRequest) (string, string) {
	return b.BuildAskFinalPromptWithIntent(req, model.DefaultSemanticIntent())
}

func (b Builder) BuildAskCompactFinalPrompt(req model.CoreRequest, evidence []model.ToolEvidence) (string, string) {
	profile := req.AskResponseProfile
	system := strictTaggedSystem("TOPS ask finalizer (compact)")
	user := fmt.Sprintf(
		"Question: %s\nRuntime CWD: %s\nPlatformContext JSON: %s\nEvidence JSON (compact): %s\nReturn only the final answer text.\nRules:\n- Use only collected evidence and runtime context.\n- Do not fabricate missing facts.\n- If evidence is insufficient, say it could not be determined from collected evidence.\n- Keep it concise.\n- No JSON, no markdown, no tool calls.%s",
		req.Input,
		req.CWD,
		renderPlatformContextJSON(req.PlatformContext),
		renderEvidenceJSONCompact(evidence),
		askCompositionGuidance(profile),
	)
	_ = profile
	return system, user
}

func (b Builder) BuildAskFinalPromptWithIntent(req model.CoreRequest, intent model.SemanticIntent) (string, string) {
	profile := req.AskResponseProfile
	system := strictTaggedSystem("TOPS ask finalizer")
	user := fmt.Sprintf("Question: %s\nRuntime CWD: %s\nPlatformContext JSON: %s\nSemanticIntent JSON (authoritative): %s\nReturn only the final answer text.\nRules: no JSON, no tags, no markdown, no tool calls, no extra scaffolding.\nGrounding rules:\n- Use only collected tool evidence.\n- Do not fabricate missing fields.\n- If a requested field is unresolved, explicitly say it could not be determined from the collected evidence.%s", req.Input, req.CWD, renderPlatformContextJSON(req.PlatformContext), renderSemanticIntentJSON(intent), askCompositionGuidance(profile))
	_ = profile
	return system, user
}

func (b Builder) BuildSemanticIntentPrompt(req model.CoreRequest) (string, string) {
	system := strictSystem("TOPS semantic normalizer")
	user := fmt.Sprintf("Mode: %s\nRequest: %s\nRuntime CWD: %s\nPlatformContext JSON: %s\nReturn exactly one SemanticIntent JSON object with keys:\nversion, operation, entity, scope, target_path, recursion, visibility, filters, requested_fields, projection, sort, limit, requires_grounding, execution_strategy, ambiguity_notes.\nAllowed enums:\n- operation: count|list|inspect|summarize|explain|generate\n- entity: directory|file|process|port|os|path|disk|custom\n- scope: current_directory|explicit_path|system\n- recursion: none|recursive\n- visibility: visible_only|include_hidden\n- execution_strategy: direct|enumerate_then_aggregate\nDefaults when user does not specify:\n- scope=current_directory\n- target_path=.\n- recursion=none\n- visibility=visible_only\n- requires_grounding=true\nrequested_fields must explicitly list requested attributes when present (example: [\"os\",\"kernel\",\"version\"]).\nUse include_hidden only when user explicitly asks for hidden/all/dot directories.\nDo not emit tool_calls, XML tags, or multiple JSON objects.\nNo markdown. No explanation.", req.Mode, req.Input, req.CWD, renderPlatformContextJSON(req.PlatformContext))
	return system, user
}

func askCompositionGuidance(profile model.AskResponseProfile) string {
	parts := make([]string, 0, 5)
	if profile.Observations {
		parts = append(parts, "observations")
	}
	if profile.Inferences {
		parts = append(parts, "inferences")
	}
	if profile.Uncertainties {
		parts = append(parts, "uncertainties")
	}
	if profile.Assumptions {
		parts = append(parts, "assumptions")
	}
	if profile.Notes {
		parts = append(parts, "notes")
	}
	if len(parts) == 0 {
		return " Keep it concise and answer-only."
	}
	return " Compose the answer naturally and include relevant " + strings.Join(parts, ", ") + " inline when useful."
}

func strictSystem(role string) string {
	return fmt.Sprintf("You are %s. Output exactly one JSON object. No markdown. No self-instruction. No reasoning narration. No schema commentary.", role)
}

func strictTaggedSystem(role string) string {
	return fmt.Sprintf("You are %s. Output exactly one response matching the required protocol. No markdown. No self-instruction. No reasoning narration. No schema commentary.", role)
}

func compactAskToolsList() string {
	catalog := commandcatalog.Default()
	names := catalog.Names()
	lines := make([]string, 0, len(names)+1)
	lines = append(lines, "- run_readonly_command(command_name, args[], output_line_limit?)")
	for _, name := range names {
		entry, ok := catalog.Get(name)
		if !ok {
			continue
		}
		lines = append(lines, fmt.Sprintf("  - %s: %s", entry.Name, entry.Description))
	}
	return strings.Join(lines, "\n")
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

func renderEvidenceJSONCompact(evidence []model.ToolEvidence) string {
	type entry struct {
		Index      int    `json:"index"`
		Command    string `json:"command"`
		ExitCode   int    `json:"exit_code"`
		Stdout     string `json:"stdout,omitempty"`
		Stderr     string `json:"stderr,omitempty"`
		DurationMS int64  `json:"duration_ms"`
		Succeeded  bool   `json:"succeeded"`
	}
	if len(evidence) == 0 {
		return "[]"
	}
	compact := make([]entry, 0, len(evidence))
	for i, item := range evidence {
		stdout := item.Stdout
		if len(stdout) > 1200 {
			stdout = stdout[:1200] + "..."
		}
		stderr := item.Stderr
		if len(stderr) > 600 {
			stderr = stderr[:600] + "..."
		}
		compact = append(compact, entry{
			Index:      i + 1,
			Command:    strings.TrimSpace(item.Command),
			ExitCode:   item.ExitCode,
			Stdout:     stdout,
			Stderr:     stderr,
			DurationMS: item.Duration.Round(time.Millisecond).Milliseconds(),
			Succeeded:  item.Succeeded,
		})
	}
	b, err := json.Marshal(compact)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func indent(input, prefix string) string {
	lines := strings.Split(input, "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}

func renderSemanticIntentJSON(intent model.SemanticIntent) string {
	blob, err := json.Marshal(intent)
	if err != nil {
		return "{}"
	}
	return string(blob)
}

func renderGenIntentJSON(intent model.GenIntent) string {
	blob, err := json.Marshal(intent)
	if err != nil {
		return "{}"
	}
	return string(blob)
}

func renderPlatformContextJSON(platform model.PlatformContext) string {
	blob, err := json.Marshal(model.NormalizePlatformContext(platform))
	if err != nil {
		return "{}"
	}
	return string(blob)
}

func askLocalToolSelectionHint(question string) string {
	q := strings.ToLower(strings.TrimSpace(question))
	if q == "" {
		return ""
	}
	hints := make([]string, 0, 3)
	appendUnique := func(line string) {
		for _, existing := range hints {
			if existing == line {
				return
			}
		}
		hints = append(hints, line)
	}
	if containsAnyTerm(q, "kernel", "operating system", " os ", "architecture", "arch") {
		appendUnique("- OS/kernel/arch: prefer uname with args [-srm] (or sw_vers for macOS version details).")
	}
	if containsAnyTerm(q, "current directory", "working directory", "cwd") {
		appendUnique("- Current directory: prefer pwd.")
	}
	if containsAnyTerm(q, "how many directories", "directory count") {
		appendUnique("- Directory count: prefer find or ls/stat combinations allowed by catalog.")
	}
	if containsAnyTerm(q, "how many files", "file count", "including hidden", "hidden files") {
		appendUnique("- File count/list hidden: prefer ls -a or find with safe counting flags.")
	}
	if containsAnyTerm(q, "open ports", "listening ports", "ports") {
		appendUnique("- Open ports: prefer lsof (macOS) or ss/netstat (Linux), depending on catalog support.")
	}
	if containsAnyTerm(q, "disk usage", "directory size") {
		appendUnique("- Disk usage: prefer du with safe flags.")
	}
	if containsAnyTerm(q, "hostname") {
		appendUnique("- Hostname: prefer hostname.")
	}
	if containsAnyTerm(q, "python version", "version of python", "python is installed") {
		appendUnique("- Python version: prefer python3 --version (or python --version when appropriate).")
	}
	return strings.Join(hints, "\n")
}

func containsAnyTerm(value string, terms ...string) bool {
	for _, term := range terms {
		term = strings.ToLower(strings.TrimSpace(term))
		if term == "" {
			continue
		}
		if strings.Contains(value, term) {
			return true
		}
	}
	return false
}
