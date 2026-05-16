package gen

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"tops/internal/intel/genintent"
	"tops/internal/model"
	"tops/internal/ops/benchmetrics"
	"tops/internal/parser"
	"tops/internal/runtime/commandcatalog"
	"tops/internal/runtime/llm"
	"tops/internal/runtime/optimization"
	"tops/internal/runtime/policy"
	"tops/internal/runtime/progress"
	"tops/internal/runtime/prompt"
	"tops/internal/runtime/tools"
	"tops/internal/runtime/workflow"
	"tops/internal/runtime/workflow/functions"
)

type Engine struct {
	provider   llm.LLMProvider
	prompts    prompt.Builder
	parser     parser.Parser
	policy     policy.Engine
	planner    workflow.WorkflowPlanner
	executor   workflow.WorkflowExecutor
	opt        optimization.Config
	funcs      functions.FunctionRegistry
	normalizer genintent.Normalizer
	catalog    commandcatalog.Catalog
}

var (
	reGrepIncludeDoubleQuoted = regexp.MustCompile(`--include="\.([A-Za-z0-9_-]+)"`)
	reGrepIncludeSingleQuoted = regexp.MustCompile(`--include='\.([A-Za-z0-9_-]+)'`)
	reGrepIncludeUnquoted     = regexp.MustCompile(`--include=\.([A-Za-z0-9_-]+)`)
)

func boolPtr(v bool) *bool {
	return &v
}

func thinkOverrideForMode(intelligenceMode model.IntelligenceMode) *bool {
	switch intelligenceMode {
	case model.IntelligenceModeBlitz:
		return boolPtr(false)
	case model.IntelligenceModeGrounded:
		return boolPtr(true)
	default:
		return nil
	}
}

func NewEngine(provider llm.LLMProvider, prompts prompt.Builder, responseParser parser.Parser, policyEngine policy.Engine, runner tools.ToolRunner, opts ...optimization.Config) Engine {
	opt := optimization.Default()
	if len(opts) > 0 {
		opt = opts[0]
	}
	return Engine{
		provider:   provider,
		prompts:    prompts,
		parser:     responseParser,
		policy:     policyEngine,
		planner:    workflow.NewJSONPlanner(),
		executor:   workflow.NewExecutor(runner, policyEngine),
		opt:        opt,
		funcs:      functions.NewDefaultRegistry(),
		normalizer: genintent.NewNormalizer(provider, prompts, opt),
		catalog:    commandcatalog.Default(),
	}
}

func (e Engine) Run(ctx context.Context, req model.CoreRequest) (model.GenResult, error) {
	input := strings.TrimSpace(req.Input)
	if input == "" {
		return model.GenResult{}, fmt.Errorf("generation input is required")
	}

	routeDone := benchmetrics.StartStage(ctx, benchmetrics.StageRoute)
	if guidance := classifyGenBoundary(input); guidance != "" {
		routeDone()
		return boundaryGuidanceGenResult(guidance), nil
	}
	routeDone()

	intelligenceMode := model.NormalizeIntelligenceMode(string(req.IntelligenceMode))
	switch intelligenceMode {
	case model.IntelligenceModeGrounded:
		progress.EmitStatusLine(ctx, "gen intelligence mode: grounded")
	case model.IntelligenceModeBlitz:
		progress.EmitStatusLine(ctx, "gen intelligence mode: blitz")
	default:
		progress.EmitStatusLine(ctx, "gen intelligence mode: auto")
	}

	thinkOverride := thinkOverrideForMode(intelligenceMode)
	progress.UpdatePhase(ctx, "planning")
	normalizeDone := benchmetrics.StartStage(ctx, benchmetrics.StageNormalize)
	intent, intentErr := e.normalizer.NormalizeWithOptions(ctx, req, genintent.NormalizeOptions{Think: thinkOverride})
	normalizeDone()
	if intentErr != nil {
		return model.GenResult{}, fmt.Errorf("gen intent normalization failed: %w", intentErr)
	}

	if native, ok := e.provider.(llm.NativeToolCallingProvider); ok {
		return e.runNative(ctx, req, native, intent, thinkOverride)
	}
	return e.runLegacy(ctx, req, intent, thinkOverride)
}

func (e Engine) runNative(ctx context.Context, req model.CoreRequest, native llm.NativeToolCallingProvider, intent model.GenIntent, thinkOverride *bool) (model.GenResult, error) {
	ctx = workflow.WithExecutionPolicy(ctx, workflow.ExecutionPolicy{
		ReadOnly: workflow.ActionPermission(req.ExecutionReadOnlyPolicy),
		Write:    workflow.ActionPermission(req.ExecutionWritePolicy),
	})

	if !intent.RequiresGrounding {
		progress.UpdatePhase(ctx, "provider")
		finalSystem, finalUser := e.prompts.BuildGenFinalPromptWithIntent(req, intent)
		finalPlannerDone := benchmetrics.StartStage(ctx, benchmetrics.StagePlanner)
		finalResp, finalErr := native.ToolChat(progress.WithStreamEmission(ctx, false), llm.ToolChatRequest{
			SystemPrompt:    finalSystem,
			Messages:        []llm.ChatMessage{{Role: "user", Content: finalUser}},
			Temperature:     -1,
			MaxTokens:       512,
			Stream:          false,
			Think:           thinkOverride,
			SamplingProfile: llm.SamplingProfileGen,
		}, nil, nil)
		finalPlannerDone()
		if finalErr != nil {
			return model.GenResult{}, finalErr
		}
		if len(finalResp.ToolCalls) > 0 {
			return blockedWorkflowGenResult("gen finalizer protocol violation: non-grounded generation must not emit tool_calls", intent), nil
		}
		parsed, parseErr := e.parseAndValidateGenResult(ctx, req, strings.TrimSpace(finalResp.Content), intent, thinkOverride)
		if parseErr != nil {
			return model.GenResult{}, parseErr
		}
		return parsed, nil
	}

	toolDefs := functionRegistryToToolDefinitions(e.funcs)
	systemPrompt, userPrompt := e.prompts.BuildGenPlanningPromptWithIntent(req, intent)
	messages := []llm.ChatMessage{{Role: "user", Content: userPrompt}}
	repairUsed := false
	plannerHint := ""

	for {
		progress.UpdatePhase(ctx, "provider")
		plannerMessages := append([]llm.ChatMessage{}, messages...)
		if strings.TrimSpace(plannerHint) != "" {
			plannerMessages = append(plannerMessages, llm.ChatMessage{Role: "user", Content: plannerHint})
		}
		plannerDone := benchmetrics.StartStage(ctx, benchmetrics.StagePlanner)
		planningResp, err := native.ToolChat(progress.WithStreamEmission(ctx, false), llm.ToolChatRequest{
			SystemPrompt:    systemPrompt,
			Messages:        plannerMessages,
			Tools:           toolDefs,
			Temperature:     -1,
			MaxTokens:       256,
			Stream:          false,
			Think:           thinkOverride,
			SamplingProfile: llm.SamplingProfilePlanner,
		}, nil, nil)
		plannerDone()
		if err != nil {
			return model.GenResult{}, err
		}

		contentTrimmed := strings.TrimSpace(planningResp.Content)

		if !intent.RequiresGrounding && len(planningResp.ToolCalls) > 0 {
			if canElevateGrounding(req.Input, planningResp.ToolCalls) {
				intent.RequiresGrounding = true
				progress.EmitStatusLine(ctx, "gen planner requested grounding-relevant tool calls; elevating to grounded path")
				benchmetrics.MarkFallback(ctx)
			} else {
				violation := "gen planner protocol violation: non-grounded intent must not return tool_calls"
				progress.EmitStatusLine(ctx, violation)
				if !repairUsed && e.opt.RepairMaxRetries > 0 {
					repairUsed = true
					benchmetrics.IncrementRepair(ctx)
					plannerHint = "Protocol violation: requires_grounding=false. Return generation JSON content only with no tool_calls."
					continue
				}
				return blockedWorkflowGenResult(violation, intent), nil
			}
		}

		if intent.RequiresGrounding {
			if len(planningResp.ToolCalls) == 0 || contentTrimmed != "" {
				violation := "gen planner protocol violation: grounded intent requires tool_calls-only response"
				progress.EmitStatusLine(ctx, violation)
				if !repairUsed && e.opt.RepairMaxRetries > 0 {
					repairUsed = true
					benchmetrics.IncrementRepair(ctx)
					plannerHint = "Protocol violation: requires_grounding=true. Return tool_calls only and empty content."
					continue
				}
				return blockedWorkflowGenResult(violation, intent), nil
			}

			toolCalls := normalizePlannerToolCalls(planningResp.ToolCalls)
			messages = append(messages, llm.ChatMessage{Role: "assistant", ToolCalls: append([]llm.ToolCall(nil), toolCalls...)})

			progress.UpdatePhase(ctx, "tools")
			toolMessages, observations, toolErr := e.executeNativeToolCalls(ctx, req, toolCalls)
			if toolErr != nil {
				return blockedWorkflowGenResult(toolErr.Error(), intent), nil
			}
			messages = append(messages, toolMessages...)

			finalSystem, finalUser := e.prompts.BuildGenFinalPromptWithIntent(req, intent)
			finalMessages := append([]llm.ChatMessage{}, messages...)
			if len(observations) > 0 {
				finalUser = finalUser + "\nUse the attached tool observations to finalize the generated artifact."
			}
			finalMessages = append(finalMessages, llm.ChatMessage{Role: "user", Content: finalUser})

			progress.UpdatePhase(ctx, "provider")
			finalPlannerDone := benchmetrics.StartStage(ctx, benchmetrics.StagePlanner)
			finalResp, finalErr := native.ToolChat(progress.WithStreamEmission(ctx, false), llm.ToolChatRequest{
				SystemPrompt:    finalSystem,
				Messages:        finalMessages,
				Temperature:     -1,
				MaxTokens:       512,
				Stream:          false,
				Think:           thinkOverride,
				SamplingProfile: llm.SamplingProfileGen,
			}, nil, nil)
			finalPlannerDone()
			if finalErr != nil {
				return model.GenResult{}, finalErr
			}
			if len(finalResp.ToolCalls) > 0 {
				return blockedWorkflowGenResult("gen finalizer protocol violation: final response must not include tool_calls", intent), nil
			}
			if strings.TrimSpace(finalResp.Content) == "" {
				return blockedWorkflowGenResult("gen finalizer produced empty content", intent), nil
			}
			parsed, parseErr := e.parseAndValidateGenResult(ctx, req, finalResp.Content, intent, thinkOverride)
			if parseErr != nil {
				return model.GenResult{}, parseErr
			}
			return parsed, nil
		}

		if len(planningResp.ToolCalls) > 0 && contentTrimmed != "" {
			violation := "gen planner protocol violation: mixed content with tool_calls"
			progress.EmitStatusLine(ctx, violation)
			if !repairUsed && e.opt.RepairMaxRetries > 0 {
				repairUsed = true
				benchmetrics.IncrementRepair(ctx)
				plannerHint = "Protocol violation: requires_grounding=false. Return generation JSON content only and no tool_calls."
				continue
			}
			return blockedWorkflowGenResult(violation, intent), nil
		}
		if contentTrimmed == "" {
			violation := "gen planner protocol violation: non-grounded intent returned empty content"
			progress.EmitStatusLine(ctx, violation)
			if !repairUsed && e.opt.RepairMaxRetries > 0 {
				repairUsed = true
				benchmetrics.IncrementRepair(ctx)
				plannerHint = "Protocol violation: requires_grounding=false. Return generation JSON content only."
				continue
			}
			return blockedWorkflowGenResult(violation, intent), nil
		}

		parsed, parseErr := e.parseAndValidateGenResult(ctx, req, contentTrimmed, intent, thinkOverride)
		if parseErr != nil {
			return model.GenResult{}, parseErr
		}
		return parsed, nil
	}
}

func (e Engine) runLegacy(ctx context.Context, req model.CoreRequest, intent model.GenIntent, thinkOverride *bool) (model.GenResult, error) {
	ctx = workflow.WithExecutionPolicy(ctx, workflow.ExecutionPolicy{
		ReadOnly: workflow.ActionPermission(req.ExecutionReadOnlyPolicy),
		Write:    workflow.ActionPermission(req.ExecutionWritePolicy),
	})

	if !intent.RequiresGrounding {
		progress.UpdatePhase(ctx, "provider")
		systemPrompt, userPrompt := e.prompts.BuildGenFinalPromptWithIntent(req, intent)
		plannerDone := benchmetrics.StartStage(ctx, benchmetrics.StagePlanner)
		completion, err := e.provider.Complete(progress.WithStreamEmission(ctx, false), llm.CompletionRequest{
			SystemPrompt:    systemPrompt,
			UserPrompt:      userPrompt,
			Temperature:     -1,
			MaxTokens:       512,
			Think:           thinkOverride,
			SamplingProfile: llm.SamplingProfileGen,
		})
		plannerDone()
		if err != nil {
			return model.GenResult{}, err
		}
		return e.parseAndValidateGenResult(ctx, req, completion.Content, intent, thinkOverride)
	}

	progress.UpdatePhase(ctx, "planning")
	systemPrompt, userPrompt := e.prompts.BuildGenLegacyPlanningPromptWithIntent(req, intent)
	plannerDone := benchmetrics.StartStage(ctx, benchmetrics.StagePlanner)
	completion, err := e.provider.Complete(progress.WithStreamEmission(ctx, false), llm.CompletionRequest{
		SystemPrompt:    systemPrompt,
		UserPrompt:      userPrompt,
		Temperature:     -1,
		MaxTokens:       256,
		Think:           thinkOverride,
		SamplingProfile: llm.SamplingProfilePlanner,
	})
	plannerDone()
	if err != nil {
		return model.GenResult{}, err
	}
	decision, err := e.planner.Decide(ctx, completion.Content)
	if err != nil {
		return model.GenResult{}, err
	}
	if decision.Plan == nil {
		return blockedWorkflowGenResult("gen legacy planner returned no workflow steps for grounded intent", intent), nil
	}

	progress.UpdatePhase(ctx, "tools")
	runResult, runErr := e.executor.Execute(ctx, string(req.Mode), req.Input, *decision.Plan)
	if runErr != nil {
		if runResult.Status == workflow.RunStatusDenied || runResult.Status == workflow.RunStatusBlocked {
			return blockedWorkflowGenResult(runResult.ErrorText, intent), nil
		}
		return model.GenResult{}, fmt.Errorf("workflow execution failed: %w", runErr)
	}
	if runResult.Status != workflow.RunStatusCompleted {
		return blockedWorkflowGenResult(runResult.ErrorText, intent), nil
	}

	progress.UpdatePhase(ctx, "provider")
	finalSystem, finalUser := e.prompts.BuildGenFinalPromptWithIntent(req, intent)
	evidence := stepRunsObservationJSON(runResult.StepRuns, e.catalog)
	if evidence != "" {
		finalUser = finalUser + "\nObserved local evidence JSON:\n" + evidence
	}
	finalPlannerDone := benchmetrics.StartStage(ctx, benchmetrics.StagePlanner)
	synthesized, synthErr := e.provider.Complete(progress.WithStreamEmission(ctx, false), llm.CompletionRequest{
		SystemPrompt:    finalSystem,
		UserPrompt:      finalUser,
		Temperature:     -1,
		MaxTokens:       512,
		Think:           thinkOverride,
		SamplingProfile: llm.SamplingProfileGen,
	})
	finalPlannerDone()
	if synthErr != nil {
		return model.GenResult{}, synthErr
	}
	return e.parseAndValidateGenResult(ctx, req, synthesized.Content, intent, thinkOverride)
}

func (e Engine) parseAndValidateGenResult(ctx context.Context, req model.CoreRequest, raw string, intent model.GenIntent, thinkOverride *bool) (model.GenResult, error) {
	parsed, err := e.parser.ParseGenWithRepair(ctx, raw, func(ctx context.Context, mode string, raw string, parseErr error) (string, error) {
		return e.repairParse(ctx, mode, raw, parseErr, thinkOverride)
	})
	if err != nil {
		if yzmaRawDebugEnabled() {
			_, _ = fmt.Fprintf(os.Stdout, "[tops yzma raw gen parse error] %v\n", err)
			_, _ = fmt.Fprintf(os.Stdout, "[tops yzma raw gen output]\n%s\n", strings.TrimSpace(raw))
		}
		return model.GenResult{}, err
	}
	parsed = normalizeGenResult(parsed, req, intent)
	parsed.RiskLabels = e.policy.Classify(parsed.Command)
	return parsed, nil
}

func yzmaRawDebugEnabled() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("TOPS_YZMA_DEBUG_RAW")))
	switch value {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func normalizeGenResult(result model.GenResult, req model.CoreRequest, intent model.GenIntent) model.GenResult {
	result.Command = strings.TrimSpace(result.Command)
	result.Command = normalizeCommonCommandQuality(result.Command)
	result.Explanation = strings.TrimSpace(result.Explanation)
	if strings.TrimSpace(result.OutputKind) == "" {
		result.OutputKind = intent.OutputKind
	}
	result.OutputKind = normalizeOutputKind(result.OutputKind)
	if strings.TrimSpace(result.TargetShell) == "" {
		result.TargetShell = intent.TargetShell
	}
	result.TargetShell = normalizeTargetShell(result.TargetShell)

	inferred := inferOutputKindFromArtifact(result.Command)
	if inferred != "" && inferred != result.OutputKind {
		result.Ambiguities = append(result.Ambiguities, fmt.Sprintf("Adjusted output_kind from %s to %s based on artifact shape.", result.OutputKind, inferred))
		result.OutputKind = inferred
	}

	if result.TargetShell == "powershell" && strings.Contains(result.Command, "#!/usr/bin/env") {
		result.Ambiguities = append(result.Ambiguities, "Artifact contains POSIX shebang but target_shell is powershell.")
	}
	if result.TargetShell != "powershell" && strings.Contains(strings.ToLower(req.Input), "powershell") {
		result.Ambiguities = append(result.Ambiguities, "Input requested PowerShell but generated target shell differs.")
	}

	result.Assumptions = dedupeList(result.Assumptions)
	result.Ambiguities = dedupeList(result.Ambiguities)
	result.ConfidenceNotes = dedupeList(result.ConfidenceNotes)
	return result
}

func normalizeCommonCommandQuality(command string) string {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return ""
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "grep ") {
		trimmed = reGrepIncludeDoubleQuoted.ReplaceAllString(trimmed, `--include="*.$1"`)
		trimmed = reGrepIncludeSingleQuoted.ReplaceAllString(trimmed, `--include='*.$1'`)
		trimmed = reGrepIncludeUnquoted.ReplaceAllString(trimmed, `--include=*.$1`)
	}
	return strings.TrimSpace(trimmed)
}

func normalizeOutputKind(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	switch raw {
	case "single_command", "multi_command", "shell_script":
		return raw
	default:
		return "single_command"
	}
}

func normalizeTargetShell(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	switch raw {
	case "bash", "zsh", "sh", "powershell", "unknown":
		return raw
	case "pwsh":
		return "powershell"
	default:
		return "unknown"
	}
}

func inferOutputKindFromArtifact(command string) string {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return "single_command"
	}
	if !strings.Contains(trimmed, "\n") {
		return "single_command"
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(trimmed, "#!") || strings.Contains(lower, "\nfor ") || strings.Contains(lower, "\nwhile ") || strings.Contains(lower, "\nif ") || strings.Contains(lower, "set -e") {
		return "shell_script"
	}
	return "multi_command"
}

func classifyGenBoundary(input string) string {
	lower := strings.ToLower(strings.TrimSpace(input))
	if lower == "" {
		return ""
	}
	if looksLikeAuthoringRequest(lower) {
		return ""
	}
	if looksLikeHelpRequest(lower) {
		return "Use help to explain a tool's built-in help text."
	}
	if looksLikeFactualRequest(lower) {
		return "Use ask for grounded questions about your current environment."
	}
	return ""
}

func looksLikeAuthoringRequest(lower string) bool {
	return containsAny(lower,
		"generate", "write", "create", "build", "compose", "give me a command", "make a script", "command to", "script to")
}

func looksLikeHelpRequest(lower string) bool {
	if strings.HasPrefix(lower, "help ") || strings.HasPrefix(lower, "explain ") || strings.HasPrefix(lower, "show me help") {
		return true
	}
	if strings.HasPrefix(lower, "what does ") && strings.Contains(lower, " do") {
		return true
	}
	if strings.Contains(lower, " --help") || strings.Contains(lower, " -h") {
		return true
	}
	return false
}

func looksLikeFactualRequest(lower string) bool {
	if !strings.Contains(lower, "?") && !strings.HasPrefix(lower, "what") && !strings.HasPrefix(lower, "which") && !strings.HasPrefix(lower, "who") {
		return false
	}
	if containsAny(lower,
		"what is my", "which directory", "where am i", "who am i", "operating system", "kernel", "hostname", "current folder", "current directory") {
		return true
	}
	return false
}

func boundaryGuidanceGenResult(msg string) model.GenResult {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		msg = "Use ask for grounded questions about your current environment."
	}
	return model.GenResult{
		Command:     msg,
		Explanation: "gen is focused on command/script authoring.",
		OutputKind:  "single_command",
		TargetShell: "unknown",
		Intent: model.GenerationIntent{
			Intent:      "mode-boundary-guidance",
			Constraints: map[string]string{},
			Action:      "guide",
		},
		Assumptions:     []string{},
		Ambiguities:     []string{},
		RiskLabels:      []string{},
		ConfidenceNotes: []string{},
	}
}

func blockedWorkflowGenResult(reason string, intent model.GenIntent) model.GenResult {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "workflow could not run"
	}
	return model.GenResult{
		Command:     "# workflow blocked",
		Explanation: "I need approved workflow steps to inspect local context before generating a grounded artifact.",
		OutputKind:  normalizeOutputKind(intent.OutputKind),
		TargetShell: normalizeTargetShell(intent.TargetShell),
		Intent: model.GenerationIntent{
			Intent:      "grounded-generation-blocked",
			Constraints: map[string]string{"requires_grounding": "true"},
			Action:      "generate",
		},
		Assumptions: []string{"Execution remains read-only in TOPS v1."},
		Ambiguities: []string{reason},
		RiskLabels:  []string{"high-risk"},
		ConfidenceNotes: []string{
			"Adjust execution policy if needed and rerun in an interactive terminal when approval is required.",
		},
	}
}

func (e Engine) repairParse(ctx context.Context, mode string, raw string, parseErr error, thinkOverride *bool) (string, error) {
	if e.opt.RepairMaxRetries < 1 {
		return "", parseErr
	}
	progress.UpdatePhase(ctx, "parser-retry")
	benchmetrics.IncrementRepair(ctx)
	systemPrompt := "Return exactly one valid JSON object matching the requested schema. No extra text."
	userPrompt := fmt.Sprintf(
		"Mode: %s\nParsing failed with: %s\nRewrite this into valid JSON only, preserving meaning:\n%s",
		mode,
		parseErr.Error(),
		raw,
	)
	if strings.EqualFold(strings.TrimSpace(mode), "gen") {
		systemPrompt = "Return exactly one JSON object for generation output. No markdown. No prose."
		userPrompt = fmt.Sprintf(
			"Parsing failed with: %s\nReturn JSON keys only: command, explanation, intent_struct, output_kind, target_shell, assumptions, ambiguities, confidence_notes.\nRules: command must be a string shell artifact; list fields must be arrays of strings; no extra keys.\nInvalid output:\n%s",
			parseErr.Error(),
			raw,
		)
	}
	resp, err := e.provider.Complete(progress.WithStreamEmission(ctx, false), llm.CompletionRequest{
		SystemPrompt:    systemPrompt,
		UserPrompt:      userPrompt,
		Temperature:     -1,
		MaxTokens:       260,
		Think:           thinkOverride,
		SamplingProfile: llm.SamplingProfilePlanner,
	})
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

func normalizePlannerToolCalls(calls []llm.ToolCall) []llm.ToolCall {
	out := make([]llm.ToolCall, 0, len(calls))
	for i, call := range calls {
		normalized := call
		if strings.TrimSpace(normalized.ID) == "" {
			normalized.ID = fmt.Sprintf("gtc-%d", i+1)
		}
		out = append(out, normalized)
	}
	return out
}

func canElevateGrounding(input string, calls []llm.ToolCall) bool {
	lower := strings.ToLower(strings.TrimSpace(input))
	if !containsAny(lower,
		"in this directory",
		"in my current directory",
		"in the current directory",
		"current directory contents",
		"current folder contents",
		"this folder",
		"this repo",
		"this repository",
		"this project",
		"my files",
		"local files",
		"existing files",
		"on this machine",
		"on my machine",
		"currently installed",
		"installed version",
		"installed tool",
		"installed tools",
		"tool version installed",
		"available on this machine",
		"available locally",
		"local state",
	) {
		return false
	}
	if len(calls) == 0 {
		return false
	}
	for _, call := range calls {
		if strings.TrimSpace(strings.ToLower(call.Name)) != "run_readonly_command" {
			return false
		}
	}
	return true
}

func functionRegistryToToolDefinitions(reg functions.FunctionRegistry) []llm.ToolDefinition {
	if reg == nil {
		return nil
	}
	defs := reg.List()
	out := make([]llm.ToolDefinition, 0, len(defs))
	for _, def := range defs {
		props := map[string]llm.ToolProperty{}
		required := make([]string, 0, len(def.Arguments))
		if len(def.Arguments) == 0 {
			for key, desc := range def.ArgSchema {
				props[key] = llm.ToolProperty{Type: inferArgTypeFromSchema(desc), Description: desc}
				if strings.Contains(strings.ToLower(desc), "required") {
					required = append(required, key)
				}
			}
		} else {
			for key, arg := range def.Arguments {
				prop := llm.ToolProperty{Type: strings.TrimSpace(arg.Type), Description: strings.TrimSpace(arg.Description), Enum: append([]string(nil), arg.Enum...)}
				if prop.Type == "" {
					prop.Type = "string"
				}
				if strings.EqualFold(prop.Type, "array") {
					itemsType := strings.TrimSpace(arg.ItemsType)
					if itemsType == "" {
						itemsType = "string"
					}
					prop.Items = &llm.ToolProperty{Type: itemsType}
				}
				props[key] = prop
				if arg.Required {
					required = append(required, key)
				}
			}
		}
		out = append(out, llm.ToolDefinition{Name: def.Name, Description: def.Description, Properties: props, Required: required})
	}
	return out
}

func inferArgTypeFromSchema(schema string) string {
	lower := strings.ToLower(strings.TrimSpace(schema))
	switch {
	case strings.Contains(lower, "bool"):
		return "boolean"
	case strings.Contains(lower, "int"):
		return "integer"
	case strings.Contains(lower, "number"):
		return "number"
	case strings.Contains(lower, "array"):
		return "array"
	default:
		return "string"
	}
}

func (e Engine) executeNativeToolCalls(ctx context.Context, req model.CoreRequest, calls []llm.ToolCall) ([]llm.ChatMessage, []model.CommandObservation, error) {
	toolMessages := make([]llm.ChatMessage, 0, len(calls))
	observations := make([]model.CommandObservation, 0, len(calls))

	for i, call := range calls {
		functionName := strings.TrimSpace(call.Name)
		if functionName == "" {
			return nil, nil, fmt.Errorf("tool call %d has empty function name", i+1)
		}
		args := call.Arguments
		if args == nil {
			args = map[string]any{}
		}
		step, err := e.resolveFunctionStep(i+1, functionName, args)
		if err != nil {
			return nil, nil, err
		}
		runResult, runErr := e.executor.Execute(ctx, string(req.Mode), req.Input, workflow.WorkflowPlan{
			Reason: "gen native tool call",
			Steps:  []workflow.WorkflowStep{step},
		})
		if runErr != nil {
			if runResult.Status == workflow.RunStatusDenied || runResult.Status == workflow.RunStatusBlocked {
				return nil, nil, fmt.Errorf("%s", runResult.ErrorText)
			}
			return nil, nil, fmt.Errorf("workflow execution failed: %w", runErr)
		}
		if runResult.Status != workflow.RunStatusCompleted || len(runResult.StepRuns) == 0 {
			return nil, nil, fmt.Errorf("tool call %q did not complete", functionName)
		}

		last := runResult.StepRuns[len(runResult.StepRuns)-1]
		observation := buildCommandObservation(last, e.catalog)
		contentBlob, marshalErr := json.Marshal(observation)
		if marshalErr != nil {
			return nil, nil, fmt.Errorf("failed to marshal command observation: %w", marshalErr)
		}
		observations = append(observations, observation)
		toolMessages = append(toolMessages, llm.ChatMessage{
			Role:       "tool",
			Name:       functionName,
			Content:    string(contentBlob),
			ToolCallID: strings.TrimSpace(call.ID),
		})
	}

	return toolMessages, observations, nil
}

func (e Engine) resolveFunctionStep(idx int, functionName string, functionArgs map[string]any) (workflow.WorkflowStep, error) {
	def, ok := e.funcs.Get(functionName)
	if !ok {
		return workflow.WorkflowStep{}, fmt.Errorf("workflow step references unknown function %q", functionName)
	}
	command, argv, expected, outputLineLimit, err := def.Resolve(functionArgs)
	if err != nil {
		return workflow.WorkflowStep{}, fmt.Errorf("invalid function arguments for %q: %w", functionName, err)
	}
	if strings.TrimSpace(command) == "" {
		return workflow.WorkflowStep{}, fmt.Errorf("function %q resolved to empty command", functionName)
	}
	step := workflow.WorkflowStep{
		ID:               fmt.Sprintf("s%d", idx),
		Intent:           fmt.Sprintf("Run %s", functionName),
		CommandName:      command,
		Args:             append([]string(nil), argv...),
		ExpectedEvidence: expected,
		OutputLineLimit:  outputLineLimit,
	}
	if entry, ok := e.catalog.Get(command); ok && entry.DefaultTimeout > 0 {
		step.TimeoutMS = int(entry.DefaultTimeout / time.Millisecond)
	}
	return step, nil
}

func buildCommandObservation(step workflow.StepResult, catalog commandcatalog.Catalog) model.CommandObservation {
	stdoutLines := splitLines(step.Stdout)
	stderrLines := splitLines(step.Stderr)
	entry, ok := catalog.Get(step.Command)
	matchKnown := ok && entry.MatchCountKnownFromLines && step.StdoutLineCountExact
	matchCount := 0
	if matchKnown {
		matchCount = step.StdoutNonemptyCount
	}
	return model.CommandObservation{
		OK:                   step.ErrorText == "" && step.ExitCode == 0,
		ExitCode:             step.ExitCode,
		CommandName:          strings.TrimSpace(step.Command),
		Args:                 append([]string(nil), step.Args...),
		CWD:                  strings.TrimSpace(step.CWD),
		Stdout:               stdoutLines,
		Stderr:               stderrLines,
		HasOutput:            len(stdoutLines) > 0,
		StdoutPreviewCount:   step.StdoutPreviewCount,
		StdoutLineCountTotal: step.StdoutLineCountTotal,
		StdoutLineCountExact: step.StdoutLineCountExact,
		StdoutNonemptyCount:  step.StdoutNonemptyCount,
		StdoutTruncated:      step.StdoutTruncated,
		StderrTruncated:      step.StderrTruncated,
		MatchCount:           matchCount,
		MatchCountKnown:      matchKnown,
		DurationMilliseconds: step.Duration.Round(time.Millisecond).Milliseconds(),
	}
}

func stepRunsObservationJSON(steps []workflow.StepResult, catalog commandcatalog.Catalog) string {
	observations := make([]model.CommandObservation, 0, len(steps))
	for _, step := range steps {
		observations = append(observations, buildCommandObservation(step, catalog))
	}
	if len(observations) == 0 {
		return "[]"
	}
	blob, err := json.Marshal(observations)
	if err != nil {
		return "[]"
	}
	return string(blob)
}

func splitLines(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return []string{}
	}
	return strings.Split(trimmed, "\n")
}

func containsAny(value string, parts ...string) bool {
	for _, part := range parts {
		part = strings.ToLower(strings.TrimSpace(part))
		if part == "" {
			continue
		}
		if strings.Contains(strings.ToLower(value), part) {
			return true
		}
	}
	return false
}

func dedupeList(items []string) []string {
	if len(items) == 0 {
		return []string{}
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		norm := strings.TrimSpace(item)
		if norm == "" {
			continue
		}
		key := strings.ToLower(norm)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, norm)
	}
	return out
}
