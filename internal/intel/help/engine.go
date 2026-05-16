package help

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

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
	provider  llm.LLMProvider
	prompts   prompt.Builder
	parser    parser.Parser
	opt       optimization.Config
	timeout   time.Duration
	executor  workflow.WorkflowExecutor
	funcs     functions.FunctionRegistry
	catalog   commandcatalog.Catalog
	resolver  Resolver
	summarize Summarizer
}

func NewEngine(provider llm.LLMProvider, prompts prompt.Builder, responseParser parser.Parser, runner tools.ToolRunner, timeout time.Duration, opts ...optimization.Config) Engine {
	opt := optimization.Default()
	if len(opts) > 0 {
		opt = opts[0]
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return Engine{
		provider:  provider,
		prompts:   prompts,
		parser:    responseParser,
		opt:       opt,
		timeout:   timeout,
		executor:  workflow.NewExecutor(runner, policy.NewEngine()),
		funcs:     functions.NewDefaultRegistry(),
		catalog:   commandcatalog.Default(),
		resolver:  NewResolver(),
		summarize: NewSummarizer(),
	}
}

func (e Engine) Run(ctx context.Context, req model.CoreRequest) (model.HelpResult, error) {
	rawInput := strings.TrimSpace(req.Input)
	if rawInput == "" {
		return model.HelpResult{}, fmt.Errorf("help input is required")
	}

	ctx = workflow.WithExecutionPolicy(ctx, workflow.ExecutionPolicy{
		ReadOnly: workflow.ActionPermission(req.ExecutionReadOnlyPolicy),
		Write:    workflow.ActionPermission(req.ExecutionWritePolicy),
	})

	routeDone := benchmetrics.StartStage(ctx, benchmetrics.StageRoute)
	if looksLikeNaturalLanguageQuestion(rawInput) {
		routeDone()
		return model.HelpResult{}, fmt.Errorf("%s", naturalLanguageHelpRejectionMessage())
	}
	routeDone()

	normalizeDone := benchmetrics.StartStage(ctx, benchmetrics.StageNormalize)
	target, err := ParseTarget(rawInput)
	normalizeDone()
	if err != nil {
		return model.HelpResult{}, fmt.Errorf("invalid help target: %w", err)
	}
	if _, ok := e.catalog.Get(target.RootCommand); !ok {
		return model.HelpResult{}, fmt.Errorf("`%s` is not currently allowed for help inspection.", target.RootCommand)
	}
	helpDebugf("help target=%q root=%q subcommands=%q", rawInput, target.RootCommand, strings.Join(target.Subcommands, " "))

	progress.UpdatePhase(ctx, "planning")
	invocations := e.resolver.Resolve(target, req.PlatformContext)
	if len(invocations) == 0 {
		result := BuildUnavailableResult(target, "TOPS could not build a safe help invocation for this target.")
		return result, nil
	}
	triedPatterns := uniqueInvocationPatterns(invocations)
	helpDebugf("help patterns=%s", strings.Join(triedPatterns, ","))

	progress.UpdatePhase(ctx, "tools")
	provenance := make([]model.Provenance, 0, len(invocations))
	for _, invocation := range invocations {
		helpDebugf("help try pattern=%s command=%s args=%q", invocation.Pattern, invocation.CommandName, strings.Join(invocation.Args, " "))
		observation, source, runErr := e.executeInvocation(ctx, req, invocation)
		if source.Source != "" {
			provenance = append(provenance, source)
		}
		if runErr != nil {
			benchmetrics.MarkFallback(ctx)
			helpDebugf("help fail pattern=%s err=%v", invocation.Pattern, runErr)
			continue
		}
		if !observation.HasOutput {
			benchmetrics.MarkFallback(ctx)
			helpDebugf("help empty pattern=%s exit=%d output_bytes=0", invocation.Pattern, observation.ExitCode)
			continue
		}
		helpDebugf("help success pattern=%s exit=%d output_bytes=%d", invocation.Pattern, observation.ExitCode, observedOutputSize(observation))

		progress.UpdatePhase(ctx, "synthesis")
		result, usedLLM := e.summarizeWithLLM(ctx, req, target, observation)
		if !usedLLM {
			result = e.summarize.Summarize(target, observation)
		}
		result = normalizeHelpResult(result, observation, target)
		result.Provenance = append(result.Provenance, source)
		return result, nil
	}

	reason := fmt.Sprintf("Could not retrieve help text for `%s`. Tried: %s", target.Display(), strings.Join(triedPatterns, ", "))
	result := BuildUnavailableResult(target, reason)
	result.Provenance = provenance
	return result, nil
}

func (e Engine) executeInvocation(ctx context.Context, req model.CoreRequest, invocation Invocation) (model.CommandObservation, model.Provenance, error) {
	step, err := e.resolveStep(invocation)
	if err != nil {
		return model.CommandObservation{}, model.Provenance{}, err
	}
	plan := workflow.WorkflowPlan{
		Reason: "Retrieve built-in help text",
		Steps:  []workflow.WorkflowStep{step},
	}
	runResult, runErr := e.executor.Execute(ctx, string(req.Mode), req.Input, plan)
	if runErr != nil {
		if runResult.Status == workflow.RunStatusDenied || runResult.Status == workflow.RunStatusBlocked {
			return model.CommandObservation{}, model.Provenance{}, fmt.Errorf("help retrieval blocked for %s: %s", renderInvocation(invocation), strings.TrimSpace(runResult.ErrorText))
		}
		if len(runResult.StepRuns) > 0 {
			last := runResult.StepRuns[len(runResult.StepRuns)-1]
			obs := stepToObservation(last)
			if obs.HasOutput {
				return obs, model.Provenance{Source: "command help", Detail: renderInvocation(invocation)}, nil
			}
		}
		return model.CommandObservation{}, model.Provenance{}, fmt.Errorf("help retrieval failed for %s: %w", renderInvocation(invocation), runErr)
	}
	if runResult.Status != workflow.RunStatusCompleted {
		return model.CommandObservation{}, model.Provenance{}, fmt.Errorf("help retrieval failed for %s: %s", renderInvocation(invocation), strings.TrimSpace(runResult.ErrorText))
	}
	if len(runResult.StepRuns) == 0 {
		return model.CommandObservation{}, model.Provenance{}, fmt.Errorf("help retrieval produced no execution result for %s", renderInvocation(invocation))
	}
	last := runResult.StepRuns[len(runResult.StepRuns)-1]
	obs := stepToObservation(last)
	return obs, model.Provenance{Source: "command help", Detail: renderInvocation(invocation)}, nil
}

func (e Engine) resolveStep(invocation Invocation) (workflow.WorkflowStep, error) {
	def, ok := e.funcs.Get("run_readonly_command")
	if !ok {
		return workflow.WorkflowStep{}, fmt.Errorf("run_readonly_command function registry entry is missing")
	}
	functionArgs := map[string]any{
		"command_name": invocation.CommandName,
		"args":         invocation.Args,
	}
	command, argv, expected, outputLineLimit, err := def.Resolve(functionArgs)
	if err != nil {
		return workflow.WorkflowStep{}, err
	}
	if strings.TrimSpace(command) == "" {
		return workflow.WorkflowStep{}, fmt.Errorf("help invocation resolved to empty command")
	}
	step := workflow.WorkflowStep{
		ID:               "s1",
		Intent:           "Fetch command help text",
		CommandName:      command,
		Args:             append([]string(nil), argv...),
		ExpectedEvidence: expected,
		OutputLineLimit:  outputLineLimit,
	}
	if entry, ok := e.catalog.Get(command); ok {
		timeout := entry.DefaultTimeout
		if timeout <= 0 || (e.timeout > 0 && e.timeout < timeout) {
			timeout = e.timeout
		}
		if timeout > 0 {
			step.TimeoutMS = int(timeout / time.Millisecond)
		}
	}
	return step, nil
}

func stepToObservation(step workflow.StepResult) model.CommandObservation {
	stdoutLines := splitOutputLines(step.Stdout)
	stderrLines := splitOutputLines(step.Stderr)
	obs := model.CommandObservation{
		OK:                   step.ExitCode == 0 && strings.TrimSpace(step.ErrorText) == "",
		ExitCode:             step.ExitCode,
		CommandName:          strings.TrimSpace(step.Command),
		Args:                 append([]string(nil), step.Args...),
		CWD:                  strings.TrimSpace(step.CWD),
		Stdout:               stdoutLines,
		Stderr:               stderrLines,
		HasOutput:            len(stdoutLines) > 0 || len(stderrLines) > 0,
		StdoutPreviewCount:   step.StdoutPreviewCount,
		StdoutLineCountExact: step.StdoutLineCountExact,
		StdoutNonemptyCount:  step.StdoutNonemptyCount,
		StdoutTruncated:      step.StdoutTruncated,
		StderrTruncated:      step.StderrTruncated,
		MatchCountKnown:      false,
		DurationMilliseconds: step.Duration.Round(time.Millisecond).Milliseconds(),
	}
	if step.StdoutLineCountExact {
		obs.StdoutLineCountTotal = step.StdoutLineCountTotal
	}
	return obs
}

func splitOutputLines(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return []string{}
	}
	parts := strings.Split(trimmed, "\n")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}

func renderInvocation(invocation Invocation) string {
	if len(invocation.Args) == 0 {
		return invocation.CommandName
	}
	return strings.TrimSpace(invocation.CommandName + " " + strings.Join(invocation.Args, " "))
}

func looksLikeNaturalLanguageQuestion(input string) bool {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	if strings.Contains(lower, "?") {
		return true
	}
	prefixes := []string{
		"how ", "how do ", "how can ", "what ", "what command ", "why ",
		"can ", "could ", "should ", "explain ", "show me ", "tell me ",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	questionWords := map[string]struct{}{
		"how": {}, "what": {}, "why": {}, "when": {}, "where": {}, "which": {},
		"can": {}, "could": {}, "should": {}, "explain": {}, "show": {}, "tell": {},
	}
	normalized := strings.NewReplacer("?", " ", "!", " ", ",", " ", ".", " ", ":", " ", ";", " ").Replace(lower)
	tokens := strings.Fields(normalized)
	if len(tokens) >= 3 {
		if _, ok := questionWords[tokens[0]]; ok {
			return true
		}
	}
	return false
}

func naturalLanguageHelpRejectionMessage() string {
	return "help expects a command or tool name, not a natural-language question.\nUse ask for environment questions or gen to create a command."
}

func (e Engine) summarizeWithLLM(ctx context.Context, req model.CoreRequest, target Target, observation model.CommandObservation) (model.HelpResult, bool) {
	if e.provider == nil {
		return model.HelpResult{}, false
	}
	systemPrompt, userPrompt := e.prompts.BuildHelpPrompt(model.CoreRequest{
		Mode:            req.Mode,
		Input:           target.Display(),
		CWD:             req.CWD,
		Shell:           req.Shell,
		OS:              req.OS,
		PlatformContext: req.PlatformContext,
	}, []model.ToolEvidence{observationToToolEvidence(observation)})
	resp, err := e.provider.Complete(progress.WithStreamEmission(ctx, false), llm.CompletionRequest{
		SystemPrompt:    systemPrompt,
		UserPrompt:      userPrompt,
		Temperature:     -1,
		MaxTokens:       700,
		SamplingProfile: llm.SamplingProfileHelp,
	})
	if err != nil {
		return model.HelpResult{}, false
	}
	helpDebugf("help summarization profile=%s", llm.SamplingProfileHelp)
	helpDebugf("help summarizer_raw=%s", strings.TrimSpace(resp.Content))
	parsed, parseErr := e.parser.ParseHelpWithRepair(ctx, resp.Content, target.Display(), e.repairHelpParse)
	if parseErr != nil {
		return model.HelpResult{}, false
	}
	return parsed, true
}

func (e Engine) repairHelpParse(ctx context.Context, _ string, raw string, parseErr error) (string, error) {
	if e.opt.RepairMaxRetries < 1 {
		return "", parseErr
	}
	resp, err := e.provider.Complete(progress.WithStreamEmission(ctx, false), llm.CompletionRequest{
		SystemPrompt:    "Return exactly one JSON object with keys: summary, syntax, important_flags, examples, caveats, assumptions, notes. No markdown. No prose.",
		UserPrompt:      fmt.Sprintf("Previous help JSON was invalid: %s\nRewrite as strict JSON only.\nInvalid output:\n%s", parseErr.Error(), raw),
		Temperature:     -1,
		MaxTokens:       256,
		SamplingProfile: llm.SamplingProfilePlanner,
	})
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

func observationToToolEvidence(observation model.CommandObservation) model.ToolEvidence {
	command := strings.TrimSpace(observation.CommandName)
	if len(observation.Args) > 0 {
		command = strings.TrimSpace(command + " " + strings.Join(observation.Args, " "))
	}
	return model.ToolEvidence{
		Command:   command,
		Stdout:    strings.TrimSpace(strings.Join(observation.Stdout, "\n")),
		Stderr:    strings.TrimSpace(strings.Join(observation.Stderr, "\n")),
		ExitCode:  observation.ExitCode,
		Duration:  time.Duration(observation.DurationMilliseconds) * time.Millisecond,
		Succeeded: observation.OK,
	}
}

func observedOutputSize(observation model.CommandObservation) int {
	return len(strings.Join(observation.Stdout, "\n")) + len(strings.Join(observation.Stderr, "\n"))
}

func uniqueInvocationPatterns(invocations []Invocation) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(invocations))
	for _, invocation := range invocations {
		pattern := strings.TrimSpace(invocation.Pattern)
		if pattern == "" {
			continue
		}
		if _, ok := seen[pattern]; ok {
			continue
		}
		seen[pattern] = struct{}{}
		out = append(out, pattern)
	}
	return out
}

func normalizeHelpResult(result model.HelpResult, observation model.CommandObservation, target Target) model.HelpResult {
	result.Target = target.Display()
	result.Summary = strings.TrimSpace(result.Summary)
	result.Syntax = strings.TrimSpace(result.Syntax)
	result.ImportantFlags = filterObservedFlags(result.ImportantFlags, observation)
	result.Examples = trimHelpExamples(result.Examples, 2)
	return result
}

func filterObservedFlags(flags []string, observation model.CommandObservation) []string {
	if len(flags) == 0 {
		return []string{}
	}
	visible := strings.ToLower(strings.Join(append(append([]string(nil), observation.Stdout...), observation.Stderr...), "\n"))
	out := make([]string, 0, len(flags))
	for _, flagLine := range flags {
		trimmed := strings.TrimSpace(flagLine)
		if trimmed == "" {
			continue
		}
		tokens := strings.Fields(trimmed)
		matched := false
		for _, token := range tokens {
			token = strings.Trim(token, ",.;:()[]{}")
			if strings.HasPrefix(token, "-") && strings.Contains(visible, strings.ToLower(token)) {
				matched = true
				break
			}
		}
		if matched {
			out = append(out, trimmed)
		}
	}
	return out
}

func trimHelpExamples(examples []string, capVal int) []string {
	if capVal <= 0 || len(examples) == 0 {
		return []string{}
	}
	out := make([]string, 0, capVal)
	for _, example := range examples {
		example = strings.TrimSpace(example)
		if example == "" {
			continue
		}
		out = append(out, example)
		if len(out) >= capVal {
			break
		}
	}
	return out
}

func helpDebugEnabled() bool {
	return isTruthyEnv("TOPS_YZMA_DEBUG_LOG") || isTruthyEnv("TOPS_YZMA_DEBUG_RAW")
}

func helpDebugf(format string, args ...any) {
	if !helpDebugEnabled() {
		return
	}
	_, _ = fmt.Fprintf(os.Stdout, "[tops help] "+strings.TrimSpace(format)+"\n", args...)
}

func isTruthyEnv(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
