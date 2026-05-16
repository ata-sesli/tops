package ask

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"tops/internal/capability"
	"tops/internal/intel/fastlane"
	"tops/internal/intel/semantic"
	"tops/internal/model"
	"tops/internal/ops/benchmetrics"
	"tops/internal/parser"
	"tops/internal/runtime/commandcatalog"
	"tops/internal/runtime/jsonutil"
	"tops/internal/runtime/llm"
	"tops/internal/runtime/optimization"
	"tops/internal/runtime/policy"
	"tops/internal/runtime/progress"
	"tops/internal/runtime/prompt"
	"tops/internal/runtime/tools"
	"tops/internal/runtime/workflow"
	"tops/internal/runtime/workflow/functions"
)

const askMaxSteps = 6

const (
	askPathBlitz    = "blitz"
	askPathGrounded = "grounded"
)

const (
	askStrategyConceptual    = "conceptual"
	askStrategyReactiveTools = "reactive_tools"
	askStrategyPlannedTools  = "planned_tools"
)

const (
	blitzMaxToolSteps       = 3
	blitzMaxPlannerRepairs  = 1
	groundedMaxAdaptiveRuns = 1
)

type askRouteDecision struct {
	AskPath  string
	Strategy string
	Reason   string
}

type askTransitionTracker struct {
	consecutiveLLM int
	repairUsed     bool
	violation      bool
	lastState      string
}

func newAskTransitionTracker() *askTransitionTracker {
	return &askTransitionTracker{lastState: "start"}
}

func (t *askTransitionTracker) llm(isRepair bool) error {
	if t == nil {
		return nil
	}
	if isRepair {
		if t.repairUsed || t.consecutiveLLM != 1 {
			t.violation = true
			return fmt.Errorf("consecutive llm invariant violated: invalid repair transition")
		}
		t.repairUsed = true
		t.consecutiveLLM++
		t.lastState = "repair"
		return nil
	}
	if t.consecutiveLLM >= 1 {
		t.violation = true
		return fmt.Errorf("consecutive llm invariant violated: planner called again without state transition")
	}
	t.consecutiveLLM = 1
	t.lastState = "llm"
	return nil
}

func (t *askTransitionTracker) tool() {
	if t == nil {
		return
	}
	t.consecutiveLLM = 0
	t.repairUsed = false
	t.lastState = "tool"
}

func (t *askTransitionTracker) final() {
	if t == nil {
		return
	}
	t.consecutiveLLM = 0
	t.repairUsed = false
	t.lastState = "final"
}

func (t *askTransitionTracker) fail() {
	if t == nil {
		return
	}
	t.lastState = "fail"
}

func boolPtr(v bool) *bool {
	return &v
}

func renderPlatformContextForCapability(platform model.PlatformContext) string {
	blob, err := json.Marshal(model.NormalizePlatformContext(platform))
	if err != nil {
		return "{}"
	}
	return string(blob)
}

func renderSemanticIntentForCapability(intent model.SemanticIntent) string {
	blob, err := json.Marshal(intent)
	if err != nil {
		return "{}"
	}
	return string(blob)
}

func thinkOverrideForRouting(intelligenceMode model.IntelligenceMode, blitzSelected bool) *bool {
	switch intelligenceMode {
	case model.IntelligenceModeBlitz:
		return boolPtr(false)
	case model.IntelligenceModeGrounded:
		return boolPtr(true)
	default:
		if blitzSelected {
			return boolPtr(false)
		}
		return nil
	}
}

type Engine struct {
	provider   llm.LLMProvider
	prompts    prompt.Builder
	runner     tools.ToolRunner
	timeout    time.Duration
	executor   workflow.WorkflowExecutor
	opt        optimization.Config
	funcs      functions.FunctionRegistry
	normalizer semantic.Normalizer
	catalog    commandcatalog.Catalog
}

func NewEngine(provider llm.LLMProvider, prompts prompt.Builder, _ parser.Parser, runner tools.ToolRunner, timeout time.Duration, opts ...optimization.Config) Engine {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	opt := optimization.Default()
	if len(opts) > 0 {
		opt = opts[0]
	}
	return Engine{
		provider:   provider,
		prompts:    prompts,
		runner:     runner,
		timeout:    timeout,
		executor:   workflow.NewExecutor(runner, policy.NewEngine()),
		opt:        opt,
		funcs:      functions.NewDefaultRegistry(),
		normalizer: semantic.NewNormalizer(provider, prompts, opt),
		catalog:    commandcatalog.Default(),
	}
}

func (e Engine) Run(ctx context.Context, req model.CoreRequest) (model.AskResult, error) {
	question := strings.TrimSpace(req.Input)
	if question == "" {
		return model.AskResult{}, fmt.Errorf("ask input is required")
	}

	ctx = workflow.WithExecutionPolicy(ctx, workflow.ExecutionPolicy{
		ReadOnly: workflow.ActionPermission(req.ExecutionReadOnlyPolicy),
		Write:    workflow.ActionPermission(req.ExecutionWritePolicy),
	})
	intelligenceMode := model.NormalizeIntelligenceMode(string(req.IntelligenceMode))

	switch intelligenceMode {
	case model.IntelligenceModeGrounded:
		progress.EmitStatusLine(ctx, "ask intelligence mode: grounded")
	case model.IntelligenceModeBlitz:
		progress.EmitStatusLine(ctx, "ask intelligence mode: blitz")
	default:
		progress.EmitStatusLine(ctx, "ask intelligence mode: auto")
	}

	routeDone := benchmetrics.StartStage(ctx, benchmetrics.StageRoute)
	route := decideAskRoute(intelligenceMode, question)
	routeDone()
	progress.EmitStatusLine(ctx, fmt.Sprintf("ask lane router: lane=%s strategy=%s reason=%s", route.AskPath, route.Strategy, route.Reason))
	benchmetrics.SetAskMode(ctx, string(intelligenceMode))
	benchmetrics.SetAskStrategy(ctx, route.Strategy)

	progress.UpdatePhase(ctx, "planning")
	thinkOverride := thinkOverrideForRouting(intelligenceMode, route.AskPath == askPathBlitz)
	if native, ok := e.provider.(llm.NativeToolCallingProvider); ok {
		switch route.AskPath {
		case askPathBlitz:
			benchmetrics.SetAskPath(ctx, askPathBlitz)
			if route.Strategy == askStrategyConceptual {
				compactResult, handled, compactErr := e.runCompactNative(ctx, req, native)
				if compactErr != nil {
					return model.AskResult{}, compactErr
				}
				if handled {
					return compactResult, nil
				}
				benchmetrics.MarkFallback(ctx)
				benchmetrics.SetAskEscalationReasonIfEmpty(ctx, "blitz_conceptual_compact_not_handled")
				return cleanupAskResult(blockedWorkflowAskResult("blitz ask failed: conceptual response could not be finalized"), req.AskResponseProfile), nil
			}
			intent := defaultSemanticIntentForStrategy(req.Input, route.Strategy)
			return e.runBlitzNative(ctx, req, native, intent, thinkOverride)
		default:
			benchmetrics.SetAskPath(ctx, askPathGrounded)
			intent, intentErr := e.normalizeSemanticIntent(ctx, req, thinkOverride)
			if intentErr != nil {
				return model.AskResult{}, intentErr
			}
			return e.runGroundedNative(ctx, req, intent, thinkOverride)
		}
	}

	switch route.AskPath {
	case askPathBlitz:
		benchmetrics.SetAskPath(ctx, askPathBlitz)
		intent := defaultSemanticIntentForStrategy(req.Input, route.Strategy)
		return e.runLegacy(ctx, req, intent, thinkOverride)
	default:
		benchmetrics.SetAskPath(ctx, askPathGrounded)
		intent, intentErr := e.normalizeSemanticIntent(ctx, req, thinkOverride)
		if intentErr != nil {
			return model.AskResult{}, intentErr
		}
		return e.runGroundedNative(ctx, req, intent, thinkOverride)
	}
}

func (e Engine) normalizeSemanticIntent(ctx context.Context, req model.CoreRequest, thinkOverride *bool) (model.SemanticIntent, error) {
	normalizeDone := benchmetrics.StartStage(ctx, benchmetrics.StageNormalize)
	intent, intentErr := e.normalizer.NormalizeWithOptions(ctx, req, semantic.NormalizeOptions{Think: thinkOverride})
	normalizeDone()
	if intentErr != nil {
		return model.SemanticIntent{}, fmt.Errorf("semantic normalization failed: %w", intentErr)
	}
	return intent, nil
}

func markTransition(ctx context.Context, tracker *askTransitionTracker, state string) {
	if tracker == nil {
		return
	}
	switch strings.TrimSpace(state) {
	case "tool":
		tracker.tool()
	case "final":
		tracker.final()
	case "fail":
		tracker.fail()
	}
	benchmetrics.SetLastStateTransition(ctx, tracker.lastState)
	if tracker.violation {
		benchmetrics.MarkConsecutiveLLMViolation(ctx)
	}
}

func markLLMTransition(ctx context.Context, tracker *askTransitionTracker, isRepair bool) error {
	if tracker == nil {
		return nil
	}
	if err := tracker.llm(isRepair); err != nil {
		benchmetrics.MarkConsecutiveLLMViolation(ctx)
		benchmetrics.SetLastStateTransition(ctx, tracker.lastState)
		return err
	}
	benchmetrics.SetLastStateTransition(ctx, tracker.lastState)
	return nil
}

func failClosedAskResult(ctx context.Context, tracker *askTransitionTracker, profile model.AskResponseProfile, reason string) model.AskResult {
	markTransition(ctx, tracker, "fail")
	return cleanupAskResult(blockedWorkflowAskResult(reason), profile)
}

func (e Engine) runCompactNative(ctx context.Context, req model.CoreRequest, native llm.NativeToolCallingProvider) (model.AskResult, bool, error) {
	profile := req.AskResponseProfile
	thinkOff := boolPtr(false)
	messages := []llm.ChatMessage{
		{Role: "user", Content: strings.TrimSpace(req.Input)},
	}
	toolDefs := functionRegistryToToolDefinitions(e.funcs)
	plannerMaxTokens := askCompactPlannerMaxTokens(e.opt.TokenBudgets.AskPlanningMaxTokens)
	finalMaxTokens := askCompactSynthesisMaxTokens(profile, e.opt.TokenBudgets)
	needsLocalEvidence := likelyRequiresLocalEvidence(strings.TrimSpace(req.Input))

	progress.UpdatePhase(ctx, "planning")
	plannerSystem, plannerUser := e.prompts.BuildAskCompactPlanningPrompt(req)
	plannerMessages := append([]llm.ChatMessage{}, messages...)
	plannerMessages = append(plannerMessages, llm.ChatMessage{Role: "user", Content: plannerUser})
	callPlanner := func(messages []llm.ChatMessage) (llm.ToolChatResponse, error) {
		progress.UpdatePhase(ctx, "provider")
		plannerDone := benchmetrics.StartStage(ctx, benchmetrics.StagePlanner)
		resp, err := native.ToolChat(progress.WithStreamEmission(ctx, false), llm.ToolChatRequest{
			SystemPrompt:    plannerSystem,
			Messages:        messages,
			Tools:           toolDefs,
			Temperature:     -1,
			MaxTokens:       plannerMaxTokens,
			Stream:          false,
			Think:           thinkOff,
			SamplingProfile: llm.SamplingProfilePlanner,
		}, nil, nil)
		plannerDone()
		return resp, err
	}

	planningResp, planErr := callPlanner(plannerMessages)
	if planErr != nil {
		return model.AskResult{}, false, planErr
	}

	contentTrimmed := strings.TrimSpace(planningResp.Content)
	if len(planningResp.ToolCalls) == 0 {
		if needsLocalEvidence {
			retryHint := compactPlannerRetryHint(req)
			if strings.TrimSpace(retryHint) != "" {
				progress.EmitStatusLine(ctx, "ask blitz planner returned no tool calls; retrying with explicit local grounding hint")
				benchmetrics.SetAskEscalationReason(ctx, "compact_no_toolcalls_retry")
				retryMessages := append([]llm.ChatMessage{}, plannerMessages...)
				retryMessages = append(retryMessages, llm.ChatMessage{Role: "user", Content: retryHint})
				retryResp, retryErr := callPlanner(retryMessages)
				if retryErr == nil {
					planningResp = retryResp
					contentTrimmed = strings.TrimSpace(planningResp.Content)
				}
			}
		}
	}
	if len(planningResp.ToolCalls) == 0 {
		if contentTrimmed == "" {
			if needsLocalEvidence {
				benchmetrics.SetAskEscalationReason(ctx, "compact_no_toolcalls_empty")
			}
			return model.AskResult{}, false, nil
		}
		if !canCompactReturnDirect(req, contentTrimmed) {
			progress.EmitStatusLine(ctx, "ask blitz planner returned no tools for a grounded/local query; escalating to grounded strategy")
			benchmetrics.SetAskEscalationReason(ctx, "compact_no_toolcalls_local_query")
			return model.AskResult{}, false, nil
		}
		return cleanupAskResult(model.AskResult{Answer: contentTrimmed}, profile), true, nil
	}
	if contentTrimmed != "" {
		progress.EmitStatusLine(ctx, "ask blitz planner protocol violation: mixed content with tool_calls")
		benchmetrics.SetAskEscalationReason(ctx, "compact_mixed_content_toolcalls")
		return model.AskResult{}, false, nil
	}

	toolCalls := normalizePlannerToolCalls(planningResp.ToolCalls, 1)
	if len(toolCalls) > 3 {
		progress.EmitStatusLine(ctx, "ask blitz planner selected too many tool calls; escalating to grounded strategy")
		benchmetrics.SetAskEscalationReason(ctx, "compact_too_many_toolcalls")
		return model.AskResult{}, false, nil
	}

	progress.UpdatePhase(ctx, "tools")
	messages = append(messages, llm.ChatMessage{
		Role:      "assistant",
		ToolCalls: append([]llm.ToolCall(nil), toolCalls...),
	})
	toolMessages, evidence, provenance, _, _, toolErr := e.executeNativeToolCalls(ctx, req, 1, toolCalls)
	if toolErr != nil {
		progress.EmitStatusLine(ctx, "ask blitz compact tool execution failed: "+toolErr.Error())
		return model.AskResult{}, false, nil
	}
	evidence = trimEvidenceTotal(evidence, e.opt.AskEvidenceMaxChars)
	messages = append(messages, toolMessages...)

	progress.UpdatePhase(ctx, "provider")
	finalSystem, finalUser := e.prompts.BuildAskCompactFinalPrompt(req, evidence)
	messages = append(messages, llm.ChatMessage{Role: "user", Content: finalUser})

	finalDone := benchmetrics.StartStage(ctx, benchmetrics.StagePlanner)
	finalResp, finalErr := native.ToolChat(ctx, llm.ToolChatRequest{
		SystemPrompt:    finalSystem,
		Messages:        messages,
		Temperature:     -1,
		MaxTokens:       finalMaxTokens,
		Stream:          true,
		Think:           thinkOff,
		SamplingProfile: llm.SamplingProfileAsk,
	}, nil, nil)
	finalDone()
	if finalErr != nil {
		return model.AskResult{}, false, finalErr
	}
	if len(finalResp.ToolCalls) > 0 {
		progress.EmitStatusLine(ctx, "ask blitz finalizer returned tool_calls unexpectedly; escalating to grounded strategy")
		benchmetrics.SetAskEscalationReason(ctx, "compact_finalizer_toolcalls")
		return model.AskResult{}, false, nil
	}
	answer := strings.TrimSpace(finalResp.Content)
	if answer == "" {
		benchmetrics.SetAskEscalationReason(ctx, "compact_finalizer_empty")
		return model.AskResult{}, false, nil
	}
	return cleanupAskResult(model.AskResult{
		Answer:     answer,
		Provenance: provenance,
	}, profile), true, nil
}

func (e Engine) runLegacy(ctx context.Context, req model.CoreRequest, intent model.SemanticIntent, thinkOverride *bool) (model.AskResult, error) {
	profile := req.AskResponseProfile

	evidence := make([]model.ToolEvidence, 0, askMaxSteps)
	provenance := make([]model.Provenance, 0, askMaxSteps)

	for i := 0; i < askMaxSteps; i++ {
		remaining := askMaxSteps - i
		progress.UpdatePhase(ctx, "planning")
		systemPrompt, userPrompt := e.prompts.BuildAskLoopPromptWithIntent(req, intent, evidence, remaining)

		progress.UpdatePhase(ctx, "provider")
		planningCtx := progress.WithStreamEmission(ctx, false)
		request := llm.CompletionRequest{
			SystemPrompt:    systemPrompt,
			UserPrompt:      userPrompt,
			Temperature:     -1,
			MaxTokens:       256,
			Think:           thinkOverride,
			SamplingProfile: llm.SamplingProfilePlanner,
		}
		collector := &taggedFinalStreamCollector{}
		completionContent := ""
		var err error
		plannerDone := benchmetrics.StartStage(ctx, benchmetrics.StagePlanner)
		if streamer, ok := e.provider.(llm.StreamingProvider); ok {
			resp, streamErr := streamer.CompleteStream(planningCtx, request, nil, func(chunk string) {
				collector.Feed(chunk, func(delta string) {
					progress.UpdatePhase(ctx, "provider-answering")
					progress.EmitResponseChunk(ctx, delta)
				})
			})
			err = streamErr
			completionContent = resp.Content
		} else {
			var resp llm.CompletionResponse
			resp, err = e.provider.Complete(planningCtx, request)
			completionContent = resp.Content
		}
		plannerDone()
		if err != nil {
			return model.AskResult{}, err
		}
		if strings.TrimSpace(completionContent) == "" {
			completionContent = collector.Raw()
		}

		cmd, err := e.parseAskLoopWithRepair(ctx, completionContent, req, evidence, remaining, thinkOverride)
		if err != nil {
			return model.AskResult{}, err
		}

		switch cmd.Kind {
		case askLoopCommandFinal:
			answer, parseErr := e.parseTaggedFinalWithRepair(ctx, cmd.FinalRaw, thinkOverride)
			if parseErr != nil {
				return model.AskResult{}, parseErr
			}
			parsed := model.AskResult{Answer: answer}
			parsed = cleanupAskResult(parsed, profile)
			parsed.Provenance = provenance
			return parsed, nil
		case askLoopCommandCall:
			step, _, stepErr := e.resolveFunctionStep(i+1, cmd.FunctionName, cmd.Args)
			if stepErr != nil {
				return model.AskResult{}, stepErr
			}

			progress.UpdatePhase(ctx, "tools")
			runResult, runErr := e.executor.Execute(ctx, string(req.Mode), req.Input, workflow.WorkflowPlan{
				Reason: "Iterative ask CALL step",
				Steps:  []workflow.WorkflowStep{step},
			})
			if runErr != nil {
				if runResult.Status == workflow.RunStatusDenied || runResult.Status == workflow.RunStatusBlocked {
					return cleanupAskResult(blockedWorkflowAskResult(runResult.ErrorText), profile), nil
				}
				return model.AskResult{}, fmt.Errorf("workflow execution failed: %w", runErr)
			}
			if runResult.Status != workflow.RunStatusCompleted {
				return cleanupAskResult(blockedWorkflowAskResult(runResult.ErrorText), profile), nil
			}

			stepEvidence, stepProvenance := stepResultsToEvidence(runResult.StepRuns)
			evidence = append(evidence, stepEvidence...)
			evidence = trimEvidenceTotal(evidence, e.opt.AskEvidenceMaxChars)
			provenance = append(provenance, stepProvenance...)
		default:
			return model.AskResult{}, fmt.Errorf("invalid ask loop command kind %q", cmd.Kind)
		}
	}

	return cleanupAskResult(blockedWorkflowAskResult("workflow did not return FINAL within step limit"), profile), nil
}

func (e Engine) runCapabilityNative(ctx context.Context, req model.CoreRequest, native llm.NativeToolCallingProvider, intent model.SemanticIntent, tracker *askTransitionTracker, thinkOverride *bool) (model.AskResult, bool, error) {
	registry := capability.NewCoreRegistry()
	retrieved := registry.Retrieve(req.Input, 8)
	if len(retrieved) == 0 {
		return model.AskResult{}, false, nil
	}

	systemPrompt := "TOPS ask capability planner. Return exactly one JSON object. No markdown. No prose."
	userPrompt := fmt.Sprintf(
		"Question: %s\nRuntime CWD: %s\nPlatformContext JSON: %s\nSemanticIntent JSON: %s\nAvailable capabilities:\n%s\nAction schema:\n{\"action\":\"use_capability|final_answer|clarify|fail\",\"capability_id\":\"...\",\"arguments\":{},\"final_answer\":\"...\",\"clarification\":\"...\",\"reason\":\"...\"}\nRules:\n- For local/evidence questions choose use_capability.\n- Do not produce shell commands.\n- Fill only arguments defined by the chosen capability.\n- final_answer is invalid before local evidence is collected.",
		req.Input,
		req.CWD,
		renderPlatformContextForCapability(req.PlatformContext),
		renderSemanticIntentForCapability(intent),
		capability.RenderCapabilities(retrieved),
	)
	progress.UpdatePhase(ctx, "provider")
	plannerDone := benchmetrics.StartStage(ctx, benchmetrics.StagePlanner)
	resp, err := e.provider.Complete(progress.WithStreamEmission(ctx, false), llm.CompletionRequest{
		SystemPrompt:    systemPrompt,
		UserPrompt:      userPrompt,
		Temperature:     -1,
		MaxTokens:       320,
		Think:           thinkOverride,
		SamplingProfile: llm.SamplingProfilePlanner,
	})
	plannerDone()
	if err != nil {
		return model.AskResult{}, false, err
	}
	action, parseErr := capability.ParseAction(resp.Content)
	if parseErr != nil {
		benchmetrics.SetAskEscalationReasonIfEmpty(ctx, "capability_action_parse_failed")
		return model.AskResult{}, false, nil
	}
	if action.Action != capability.ActionUseCapability {
		benchmetrics.SetAskEscalationReasonIfEmpty(ctx, "capability_non_tool_action")
		return model.AskResult{}, false, nil
	}
	plan, compileErr := registry.Compile(action, model.NormalizePlatformContext(req.PlatformContext))
	if compileErr != nil {
		benchmetrics.SetAskEscalationReasonIfEmpty(ctx, "capability_compile_failed")
		return model.AskResult{}, false, nil
	}

	var stepRuns []workflow.StepResult
	var evidence []model.ToolEvidence
	provenance := []model.Provenance{}
	steps := plan.Steps()
	if len(steps) > 0 {
		progress.UpdatePhase(ctx, "tools")
		runResult, runErr := e.executor.Execute(ctx, string(req.Mode), req.Input, workflow.WorkflowPlan{
			Reason: plan.Reason,
			Steps:  steps,
		})
		if runErr != nil || runResult.Status != workflow.RunStatusCompleted || len(runResult.StepRuns) == 0 {
			benchmetrics.SetAskEscalationReasonIfEmpty(ctx, "capability_step_failed")
			return model.AskResult{}, false, nil
		}
		stepRuns = append(stepRuns, runResult.StepRuns...)
		markTransition(ctx, tracker, "tool")
		for _, step := range runResult.StepRuns {
			provenance = append(provenance, model.Provenance{
				Source: "core capability",
				Detail: strings.TrimSpace(step.Command + " " + strings.Join(step.Args, " ")),
			})
		}
	} else {
		benchmetrics.MarkGrounded(ctx)
		benchmetrics.SetAskEscalationReasonIfEmpty(ctx, "capability_unavailable")
		markTransition(ctx, tracker, "tool")
		provenance = append(provenance, model.Provenance{Source: "core capability", Detail: "capability unavailable evidence"})
	}

	capEvidence := plan.Evidence(stepRuns)
	evidence = append(evidence, capEvidence)
	evidence = trimEvidenceTotal(evidence, e.opt.AskEvidenceMaxChars)
	rawBytes := evidencePayloadBytes([]model.ToolEvidence{capEvidence})
	benchmetrics.SetEvidenceMetrics(ctx, rawBytes, evidencePayloadBytes(evidence), evidencePayloadBytes(evidence) < rawBytes, evidenceRowsUsed(evidence))

	progress.UpdatePhase(ctx, "provider")
	finalSystem, finalUser := e.prompts.BuildAskPromptWithIntent(req, intent, evidence)
	finalUser += "\nCapability answer style: answer in one short sentence using only the compact evidence."
	finalPromptTokens := estimatePromptTokens(finalSystem + "\n" + finalUser)
	benchmetrics.SetPromptSizeMetrics(ctx, finalPromptTokens, 0, estimateAskContextTokens(req, intent))
	if err := markLLMTransition(ctx, tracker, false); err != nil {
		benchmetrics.SetAskEscalationReasonIfEmpty(ctx, "consecutive_llm_violation")
		return failClosedAskResult(ctx, tracker, req.AskResponseProfile, "capability ask failed: consecutive llm calls without state transition"), true, nil
	}
	finalDone := benchmetrics.StartStage(ctx, benchmetrics.StagePlanner)
	finalResp, finalErr := native.ToolChat(ctx, llm.ToolChatRequest{
		SystemPrompt:    finalSystem,
		Messages:        []llm.ChatMessage{{Role: "user", Content: finalUser}},
		Temperature:     -1,
		MaxTokens:       160,
		Stream:          true,
		Think:           thinkOverride,
		SamplingProfile: llm.SamplingProfileAsk,
	}, nil, nil)
	finalDone()
	if finalErr != nil {
		return model.AskResult{}, false, finalErr
	}
	answer := strings.TrimSpace(finalResp.Content)
	if answer == "" {
		return failClosedAskResult(ctx, tracker, req.AskResponseProfile, "capability final answer was empty"), true, nil
	}
	markTransition(ctx, tracker, "final")
	return cleanupAskResult(model.AskResult{
		Answer:     answer,
		Provenance: provenance,
	}, req.AskResponseProfile), true, nil
}

func (e Engine) resolveFunctionStep(idx int, functionName string, functionArgs map[string]any) (workflow.WorkflowStep, functions.FunctionDefinition, error) {
	def, ok := e.funcs.Get(functionName)
	if !ok {
		return workflow.WorkflowStep{}, functions.FunctionDefinition{}, fmt.Errorf("workflow step references unknown function %q", functionName)
	}
	command, argv, expected, outputLineLimit, err := def.Resolve(functionArgs)
	if err != nil {
		return workflow.WorkflowStep{}, functions.FunctionDefinition{}, fmt.Errorf("invalid function arguments for %q: %w", functionName, err)
	}
	if strings.TrimSpace(command) == "" {
		return workflow.WorkflowStep{}, functions.FunctionDefinition{}, fmt.Errorf("function %q resolved to empty command", functionName)
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
	return step, def, nil
}

func (e Engine) parseAskLoopWithRepair(ctx context.Context, raw string, req model.CoreRequest, evidence []model.ToolEvidence, remaining int, thinkOverride *bool) (askLoopCommand, error) {
	parsed, err := parseAskLoopCommand(raw, e.funcs)
	if err == nil {
		return parsed, nil
	}
	if e.opt.RepairMaxRetries < 1 {
		return askLoopCommand{}, err
	}

	progress.UpdatePhase(ctx, "parser-retry")
	benchmetrics.IncrementRepair(ctx)
	systemPrompt := "Output using only one of these formats:\nCALL <function_name> <optional_json_args_object>\nOR\nFINAL a: <answer>\nOptional tagged lines only when needed: o:, i:, u:, s:, n:"
	userPrompt := fmt.Sprintf("Your previous output was invalid: %s\nRewrite it using only protocol output, no extra text.\nInvalid output:\n%s", err.Error(), raw)
	repaired, repairErr := e.provider.Complete(progress.WithStreamEmission(ctx, false), llm.CompletionRequest{
		SystemPrompt:    systemPrompt,
		UserPrompt:      userPrompt,
		Temperature:     -1,
		MaxTokens:       220,
		Think:           thinkOverride,
		SamplingProfile: llm.SamplingProfilePlanner,
	})
	if repairErr != nil {
		return askLoopCommand{}, err
	}
	parsed, parseErr := parseAskLoopCommand(repaired.Content, e.funcs)
	if parseErr != nil {
		return askLoopCommand{}, err
	}
	return parsed, nil
}

func (e Engine) runBlitzNative(ctx context.Context, req model.CoreRequest, native llm.NativeToolCallingProvider, intent model.SemanticIntent, thinkOverride *bool) (model.AskResult, error) {
	profile := req.AskResponseProfile
	evidence := make([]model.ToolEvidence, 0, blitzMaxToolSteps)
	provenance := make([]model.Provenance, 0, blitzMaxToolSteps)
	observations := make([]model.CommandObservation, 0, blitzMaxToolSteps)
	requestedFields := deriveRequestedFields(intent, req.Input)
	messages := []llm.ChatMessage{
		{Role: "user", Content: strings.TrimSpace(req.Input)},
	}
	contextTokens := estimateAskContextTokens(req, intent)
	benchmetrics.SetPromptSizeMetrics(ctx, 0, 0, contextTokens)
	repairUsed := false
	executedSignatures := map[string]struct{}{}
	plannerHint := ""
	executedToolCount := 0
	repairBudget := blitzMaxPlannerRepairs
	plannerRepairs := 0
	defer benchmetrics.SetPlannerRepairs(ctx, plannerRepairs)
	tracker := newAskTransitionTracker()
	benchmetrics.SetLastStateTransition(ctx, tracker.lastState)
	consumeRepair := func() bool {
		if repairBudget <= 0 || plannerRepairs >= repairBudget {
			return false
		}
		plannerRepairs++
		benchmetrics.IncrementRepair(ctx)
		benchmetrics.IncrementPlannerRepair(ctx)
		return true
	}
	requiresToolForAnswer := intent.RequiresGrounding || likelyRequiresLocalEvidence(req.Input) || len(requestedFields) > 0
	nextPlannerIsRepair := false
	evidenceRawBytes := 0

	if requiresToolForAnswer {
		if result, handled, capErr := e.runCapabilityNative(ctx, req, native, intent, tracker, thinkOverride); capErr != nil {
			return model.AskResult{}, capErr
		} else if handled {
			return result, nil
		}
	}

	toolDefs := functionRegistryToToolDefinitions(e.funcs)
	toolSchemaTokens := estimateToolSchemaTokens(toolDefs)
	benchmetrics.SetPromptSizeMetrics(ctx, 0, toolSchemaTokens, contextTokens)

	for i := 0; i < blitzMaxToolSteps; i++ {
		remaining := blitzMaxToolSteps - i
		missingFields := missingRequestedFields(requestedFields, observations)
		progress.UpdatePhase(ctx, "planning")
		systemPrompt, userPrompt := e.prompts.BuildAskLoopPromptWithIntent(req, intent, evidence, remaining)
		plannerMessages := append([]llm.ChatMessage{}, messages...)
		plannerMessages = append(plannerMessages, llm.ChatMessage{Role: "user", Content: userPrompt})
		combinedPlannerHint := strings.TrimSpace(plannerHint)
		if coverageHint := requestedFieldCoverageHint(missingFields); coverageHint != "" {
			if combinedPlannerHint == "" {
				combinedPlannerHint = coverageHint
			} else {
				combinedPlannerHint = combinedPlannerHint + "\n" + coverageHint
			}
		}
		if strings.TrimSpace(combinedPlannerHint) != "" {
			plannerMessages = append(plannerMessages, llm.ChatMessage{Role: "user", Content: combinedPlannerHint})
		}
		if err := markLLMTransition(ctx, tracker, nextPlannerIsRepair); err != nil {
			benchmetrics.SetAskEscalationReasonIfEmpty(ctx, "consecutive_llm_violation")
			return failClosedAskResult(ctx, tracker, profile, "blitz ask failed: consecutive llm calls without state transition"), nil
		}
		isRepairTurn := nextPlannerIsRepair
		nextPlannerIsRepair = false

		progress.UpdatePhase(ctx, "provider")
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
			return model.AskResult{}, err
		}

		contentTrimmed := strings.TrimSpace(planningResp.Content)
		if len(planningResp.ToolCalls) == 0 {
			if requiresToolForAnswer && executedToolCount == 0 {
				if !isRepairTurn && !repairUsed && consumeRepair() {
					repairUsed = true
					benchmetrics.SetAskEscalationReason(ctx, "blitz_no_toolcalls_repair")
					plannerHint = "Local evidence is required. Return tool_calls only (empty content) and select a relevant run_readonly_command."
					nextPlannerIsRepair = true
					continue
				}
				benchmetrics.SetAskEscalationReasonIfEmpty(ctx, "blitz_no_toolcalls_fail_closed")
				return failClosedAskResult(ctx, tracker, profile, "blitz ask failed: planner did not produce required tool call for local/evidence question"), nil
			}
			if requiresToolForAnswer && executedToolCount > 0 && contentTrimmed != "" {
				markTransition(ctx, tracker, "final")
				benchmetrics.SetEvidenceMetrics(ctx, evidenceRawBytes, evidencePayloadBytes(evidence), evidencePayloadBytes(evidence) < evidenceRawBytes, evidenceRowsUsed(evidence))
				return cleanupAskResult(model.AskResult{Answer: contentTrimmed, Provenance: provenance}, profile), nil
			}
			if contentTrimmed != "" && canCompactReturnDirect(req, contentTrimmed) {
				markTransition(ctx, tracker, "final")
				benchmetrics.SetEvidenceMetrics(ctx, evidenceRawBytes, evidenceRawBytes, false, evidenceRowsUsed(evidence))
				return cleanupAskResult(model.AskResult{Answer: contentTrimmed, Provenance: provenance}, profile), nil
			}
			if !isRepairTurn && !repairUsed && consumeRepair() {
				repairUsed = true
				benchmetrics.SetAskEscalationReason(ctx, "blitz_protocol_repair")
				plannerHint = "Protocol reminder: return either tool_calls-only with empty content, or a concise final direct answer when no tool is needed."
				nextPlannerIsRepair = true
				continue
			}
			return failClosedAskResult(ctx, tracker, profile, "blitz ask failed: planner response was invalid"), nil
		}
		if contentTrimmed != "" {
			violation := "ask blitz planner protocol violation: mixed prose with tool_calls"
			progress.EmitStatusLine(ctx, violation)
			if !isRepairTurn && !repairUsed && consumeRepair() {
				repairUsed = true
				benchmetrics.SetAskEscalationReason(ctx, "blitz_protocol_repair")
				plannerHint = "Protocol violation: when returning tool_calls, content must be empty. Return tool_calls only."
				nextPlannerIsRepair = true
				continue
			}
			return failClosedAskResult(ctx, tracker, profile, violation), nil
		}
		toolCalls := normalizePlannerToolCalls(planningResp.ToolCalls, i+1)
		repeated := true
		for _, call := range toolCalls {
			sig := toolCallSignature(call)
			if _, ok := executedSignatures[sig]; !ok {
				repeated = false
				break
			}
		}
		if repeated {
			if len(missingFields) > 0 && !isRepairTurn && !repairUsed && consumeRepair() {
				repairUsed = true
				benchmetrics.SetAskEscalationReason(ctx, "blitz_repeated_tool_repair")
				plannerHint = "You selected already-executed tool_calls while fields are unresolved. Select a different relevant tool_call."
				nextPlannerIsRepair = true
				continue
			}
			benchmetrics.SetAskEscalationReason(ctx, "blitz_repeated_tool_signature")
			return failClosedAskResult(ctx, tracker, profile, "blitz ask failed: planner repeated tool call without new evidence"), nil
		}
		plannerHint = ""

		progress.UpdatePhase(ctx, "tools")
		messages = append(messages, llm.ChatMessage{
			Role:      "assistant",
			ToolCalls: append([]llm.ToolCall(nil), toolCalls...),
		})
		toolMessages, newEvidence, newProv, newObservations, _, toolErr := e.executeNativeToolCalls(ctx, req, i+1, toolCalls)
		if toolErr != nil {
			if !isRepairTurn && !repairUsed && consumeRepair() {
				repairUsed = true
				benchmetrics.SetAskEscalationReason(ctx, "blitz_tool_selection_repair")
				plannerHint = fmt.Sprintf("Previous tool selection failed validation: %s. Select valid tool_calls only.", toolErr.Error())
				nextPlannerIsRepair = true
				continue
			}
			return failClosedAskResult(ctx, tracker, profile, toolErr.Error()), nil
		}

		for _, call := range toolCalls {
			executedSignatures[toolCallSignature(call)] = struct{}{}
		}
		markTransition(ctx, tracker, "tool")
		executedToolCount += len(toolCalls)
		messages = append(messages, toolMessages...)
		evidenceRawBytes += evidencePayloadBytes(newEvidence)
		evidence = append(evidence, newEvidence...)
		evidence = trimEvidenceTotal(evidence, e.opt.AskEvidenceMaxChars)
		provenance = append(provenance, newProv...)
		observations = append(observations, newObservations...)
		benchmetrics.SetEvidenceMetrics(ctx, evidenceRawBytes, evidencePayloadBytes(evidence), evidencePayloadBytes(evidence) < evidenceRawBytes, evidenceRowsUsed(evidence))
	}
	if requiresToolForAnswer && executedToolCount == 0 {
		benchmetrics.SetAskEscalationReasonIfEmpty(ctx, "blitz_requires_grounding_no_tools")
		return failClosedAskResult(ctx, tracker, profile, "blitz ask failed: planner did not produce required tool call for local/evidence question"), nil
	}
	unresolvedFields := missingRequestedFields(requestedFields, observations)
	if len(unresolvedFields) > 0 {
		progress.EmitStatusLine(ctx, "ask unresolved requested fields: "+strings.Join(unresolvedFields, ", "))
	}
	if executedToolCount == 0 {
		return failClosedAskResult(ctx, tracker, profile, "blitz ask failed: no evidence was collected"), nil
	}

	progress.UpdatePhase(ctx, "provider")
	finalSystem, finalUser := e.prompts.BuildAskFinalPromptWithIntent(req, intent)
	if len(requestedFields) > 0 {
		finalUser += "\nRequested fields: " + strings.Join(requestedFields, ", ")
	}
	if len(unresolvedFields) > 0 {
		finalUser += "\nUnresolved requested fields: " + strings.Join(unresolvedFields, ", ")
		finalUser += "\nYou must explicitly state unresolved fields as undetermined from collected evidence."
	}
	messages = append(messages, llm.ChatMessage{Role: "user", Content: finalUser})
	finalPromptTokens := estimateChatPromptTokens(finalSystem, messages)
	benchmetrics.SetPromptSizeMetrics(ctx, finalPromptTokens, toolSchemaTokens, contextTokens)
	if err := markLLMTransition(ctx, tracker, false); err != nil {
		benchmetrics.SetAskEscalationReasonIfEmpty(ctx, "consecutive_llm_violation")
		return failClosedAskResult(ctx, tracker, profile, "blitz ask failed: consecutive llm calls without state transition"), nil
	}
	finalPlannerDone := benchmetrics.StartStage(ctx, benchmetrics.StagePlanner)
	finalResp, finalErr := native.ToolChat(ctx, llm.ToolChatRequest{
		SystemPrompt:    finalSystem,
		Messages:        messages,
		Temperature:     -1,
		MaxTokens:       700,
		Stream:          true,
		Think:           thinkOverride,
		SamplingProfile: llm.SamplingProfileAsk,
	}, nil, nil)
	finalPlannerDone()
	if finalErr != nil {
		return model.AskResult{}, finalErr
	}
	answer := strings.TrimSpace(finalResp.Content)
	if answer == "" {
		return failClosedAskResult(ctx, tracker, profile, "final answer was empty"), nil
	}
	markTransition(ctx, tracker, "final")
	benchmetrics.SetEvidenceMetrics(ctx, evidenceRawBytes, evidencePayloadBytes(evidence), evidencePayloadBytes(evidence) < evidenceRawBytes, evidenceRowsUsed(evidence))
	parsed := model.AskResult{Answer: answer, Provenance: provenance}
	return cleanupAskResult(parsed, profile), nil
}

func (e Engine) runGroundedNative(ctx context.Context, req model.CoreRequest, intent model.SemanticIntent, thinkOverride *bool) (model.AskResult, error) {
	profile := req.AskResponseProfile
	planner := workflow.NewJSONPlannerWithRegistry(e.funcs)
	evidence := make([]model.ToolEvidence, 0, askMaxSteps)
	provenance := make([]model.Provenance, 0, askMaxSteps)
	observations := make([]model.CommandObservation, 0, askMaxSteps)
	requestedFields := deriveRequestedFields(intent, req.Input)
	requiresToolPlan := intent.RequiresGrounding || likelyRequiresLocalEvidence(req.Input) || len(requestedFields) > 0
	contextTokens := estimateAskContextTokens(req, intent)
	benchmetrics.SetPromptSizeMetrics(ctx, 0, 0, contextTokens)
	plannerRepairs := 0
	adaptiveReplans := 0
	evidenceRawBytes := 0
	tracker := newAskTransitionTracker()
	benchmetrics.SetLastStateTransition(ctx, tracker.lastState)
	defer benchmetrics.SetPlannerRepairs(ctx, plannerRepairs)
	defer benchmetrics.SetAdaptiveReplans(ctx, adaptiveReplans)
	defer func() {
		benchmetrics.SetEvidenceMetrics(ctx, evidenceRawBytes, evidencePayloadBytes(evidence), evidencePayloadBytes(evidence) < evidenceRawBytes, evidenceRowsUsed(evidence))
	}()

	generatePlan := func(plannerContext string, isRepair bool) (workflow.PlanningDecision, error) {
		if err := markLLMTransition(ctx, tracker, isRepair); err != nil {
			benchmetrics.SetAskEscalationReasonIfEmpty(ctx, "consecutive_llm_violation")
			return workflow.PlanningDecision{}, fmt.Errorf("grounded ask failed: consecutive llm calls without state transition")
		}
		systemPrompt, userPrompt := e.prompts.BuildAskGroundedPlanPrompt(req, intent, evidence, plannerContext)
		progress.UpdatePhase(ctx, "provider")
		plannerDone := benchmetrics.StartStage(ctx, benchmetrics.StagePlanner)
		resp, err := e.provider.Complete(progress.WithStreamEmission(ctx, false), llm.CompletionRequest{
			SystemPrompt:    systemPrompt,
			UserPrompt:      userPrompt,
			Temperature:     -1,
			MaxTokens:       256,
			Think:           thinkOverride,
			SamplingProfile: llm.SamplingProfilePlanner,
		})
		plannerDone()
		if err != nil {
			return workflow.PlanningDecision{}, err
		}
		return planner.Decide(ctx, resp.Content)
	}

	decision, planErr := generatePlan("", false)
	if planErr != nil {
		if plannerRepairs >= 1 {
			return failClosedAskResult(ctx, tracker, profile, "grounded ask failed: planner did not produce required tool steps"), nil
		}
		plannerRepairs++
		benchmetrics.IncrementRepair(ctx)
		benchmetrics.IncrementPlannerRepair(ctx)
		repaired, repairErr := generatePlan("Previous plan was invalid: "+planErr.Error()+". Return valid workflow_plan JSON with executable run_readonly_command steps.", true)
		if repairErr != nil {
			return failClosedAskResult(ctx, tracker, profile, "grounded ask failed: planner did not produce required tool steps"), nil
		}
		decision = repaired
	}

	if requiresToolPlan && (decision.Plan == nil || len(decision.Plan.Steps) == 0) {
		if plannerRepairs < 1 {
			plannerRepairs++
			benchmetrics.IncrementRepair(ctx)
			benchmetrics.IncrementPlannerRepair(ctx)
			repaired, repairErr := generatePlan("Local evidence is required. Return workflow_plan with at least one run_readonly_command step.", true)
			if repairErr == nil && repaired.Plan != nil && len(repaired.Plan.Steps) > 0 {
				decision = repaired
			}
		}
		if decision.Plan == nil || len(decision.Plan.Steps) == 0 {
			benchmetrics.SetAskEscalationReason(ctx, "grounded_missing_tool_plan")
			return failClosedAskResult(ctx, tracker, profile, "grounded ask failed: planner did not produce required tool steps"), nil
		}
	}

	steps := []workflow.WorkflowStep{}
	if decision.Plan != nil {
		steps = append(steps, decision.Plan.Steps...)
	}
	if len(steps) == 0 {
		if answer, ok := extractDirectAnswerFromPlanningDecision(decision.FinalRaw); ok {
			markTransition(ctx, tracker, "final")
			return cleanupAskResult(model.AskResult{
				Answer:     answer,
				Provenance: provenance,
			}, profile), nil
		}
		benchmetrics.SetAskEscalationReasonIfEmpty(ctx, "grounded_no_plan_no_direct_answer")
		return failClosedAskResult(ctx, tracker, profile, "grounded ask failed: planner did not produce executable tool steps"), nil
	}
	for idx := 0; idx < len(steps); idx++ {
		step := steps[idx]
		progress.UpdatePhase(ctx, "tools")
		runResult, runErr := e.executor.Execute(ctx, string(req.Mode), req.Input, workflow.WorkflowPlan{
			Reason: "Grounded ask planned step",
			Steps:  []workflow.WorkflowStep{step},
		})
		if runErr != nil || runResult.Status != workflow.RunStatusCompleted || len(runResult.StepRuns) == 0 {
			markTransition(ctx, tracker, "tool")
			if adaptiveReplans < groundedMaxAdaptiveRuns {
				adaptiveReplans++
				benchmetrics.IncrementAdaptiveReplan(ctx)
				replanned, replErr := generatePlan(fmt.Sprintf("Planned step failed: command=%s args=%v error=%v. Provide replacement workflow_plan steps.", step.CommandName, step.Args, runErr), false)
				if replErr == nil && replanned.Plan != nil && len(replanned.Plan.Steps) > 0 {
					before := append([]workflow.WorkflowStep{}, steps[:idx]...)
					after := []workflow.WorkflowStep{}
					if idx+1 < len(steps) {
						after = append(after, steps[idx+1:]...)
					}
					steps = append(before, append(replanned.Plan.Steps, after...)...)
					idx--
					continue
				}
			}
			benchmetrics.SetAskEscalationReasonIfEmpty(ctx, "grounded_step_failed")
			return failClosedAskResult(ctx, tracker, profile, "grounded ask failed: planned step execution failed"), nil
		}

		stepEvidence, stepProvenance := stepResultsToEvidence(runResult.StepRuns)
		markTransition(ctx, tracker, "tool")
		evidenceRawBytes += evidencePayloadBytes(stepEvidence)
		evidence = append(evidence, stepEvidence...)
		evidence = trimEvidenceTotal(evidence, e.opt.AskEvidenceMaxChars)
		provenance = append(provenance, stepProvenance...)
		for _, executed := range runResult.StepRuns {
			observations = append(observations, buildCommandObservation(executed, e.catalog))
		}

		last := runResult.StepRuns[len(runResult.StepRuns)-1]
		if adaptiveReplans < groundedMaxAdaptiveRuns && last.ExitCode == 0 && strings.TrimSpace(last.Stdout) == "" && strings.TrimSpace(last.Stderr) == "" && strings.TrimSpace(step.ExpectedEvidence) != "" {
			adaptiveReplans++
			benchmetrics.IncrementAdaptiveReplan(ctx)
			replanned, replErr := generatePlan(fmt.Sprintf("Planned step returned empty output: command=%s args=%v expected=%s. Provide replacement workflow_plan steps.", step.CommandName, step.Args, step.ExpectedEvidence), false)
			if replErr == nil && replanned.Plan != nil && len(replanned.Plan.Steps) > 0 {
				steps = append(steps, replanned.Plan.Steps...)
			}
		}
	}

	if requiresToolPlan && len(observations) == 0 {
		benchmetrics.SetAskEscalationReasonIfEmpty(ctx, "grounded_no_tools_executed")
		return failClosedAskResult(ctx, tracker, profile, "grounded ask failed: planner did not produce required tool steps"), nil
	}

	unresolvedFields := missingRequestedFields(requestedFields, observations)
	if len(unresolvedFields) > 0 {
		progress.EmitStatusLine(ctx, "ask unresolved requested fields: "+strings.Join(unresolvedFields, ", "))
	}

	progress.UpdatePhase(ctx, "provider")
	finalSystem, finalUser := e.prompts.BuildAskPromptWithIntent(req, intent, evidence)
	if len(requestedFields) > 0 {
		finalUser += "\nRequested fields: " + strings.Join(requestedFields, ", ")
	}
	if len(unresolvedFields) > 0 {
		finalUser += "\nUnresolved requested fields: " + strings.Join(unresolvedFields, ", ")
		finalUser += "\nYou must explicitly state unresolved fields as undetermined from collected evidence."
	}
	finalPromptTokens := estimatePromptTokens(finalSystem + "\n" + finalUser)
	benchmetrics.SetPromptSizeMetrics(ctx, finalPromptTokens, 0, contextTokens)
	if err := markLLMTransition(ctx, tracker, false); err != nil {
		benchmetrics.SetAskEscalationReasonIfEmpty(ctx, "consecutive_llm_violation")
		return failClosedAskResult(ctx, tracker, profile, "grounded ask failed: consecutive llm calls without state transition"), nil
	}
	finalDone := benchmetrics.StartStage(ctx, benchmetrics.StagePlanner)
	finalResp, finalErr := e.provider.Complete(ctx, llm.CompletionRequest{
		SystemPrompt:    finalSystem,
		UserPrompt:      finalUser,
		Temperature:     -1,
		MaxTokens:       700,
		Think:           thinkOverride,
		SamplingProfile: llm.SamplingProfileAsk,
	})
	finalDone()
	if finalErr != nil {
		return model.AskResult{}, finalErr
	}
	answer := strings.TrimSpace(finalResp.Content)
	if answer == "" {
		return failClosedAskResult(ctx, tracker, profile, "final answer was empty"), nil
	}
	markTransition(ctx, tracker, "final")
	return cleanupAskResult(model.AskResult{
		Answer:     answer,
		Provenance: provenance,
	}, profile), nil
}

func normalizePlannerToolCalls(calls []llm.ToolCall, turn int) []llm.ToolCall {
	out := make([]llm.ToolCall, 0, len(calls))
	for i, call := range calls {
		normalized := call
		if strings.TrimSpace(normalized.ID) == "" {
			normalized.ID = fmt.Sprintf("tc-%d-%d", turn, i+1)
		}
		out = append(out, normalized)
	}
	return out
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
				prop := llm.ToolProperty{
					Type:        strings.TrimSpace(arg.Type),
					Description: strings.TrimSpace(arg.Description),
					Enum:        append([]string(nil), arg.Enum...),
				}
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
		out = append(out, llm.ToolDefinition{
			Name:        def.Name,
			Description: def.Description,
			Properties:  props,
			Required:    required,
		})
	}
	return out
}

func (e Engine) executeNativeToolCalls(ctx context.Context, req model.CoreRequest, stepOffset int, calls []llm.ToolCall) ([]llm.ChatMessage, []model.ToolEvidence, []model.Provenance, []model.CommandObservation, []workflow.StepResult, error) {
	toolMessages := make([]llm.ChatMessage, 0, len(calls))
	evidence := make([]model.ToolEvidence, 0, len(calls))
	provenance := make([]model.Provenance, 0, len(calls))
	observations := make([]model.CommandObservation, 0, len(calls))
	stepRunsOut := make([]workflow.StepResult, 0, len(calls))

	type preparedCall struct {
		call         llm.ToolCall
		functionName string
		step         workflow.WorkflowStep
	}
	prepared := make([]preparedCall, 0, len(calls))

	for i, call := range calls {
		functionName := strings.TrimSpace(call.Name)
		if functionName == "" {
			return nil, nil, nil, nil, nil, fmt.Errorf("tool call %d has empty function name", i+1)
		}
		args := call.Arguments
		if args == nil {
			args = map[string]any{}
		}

		step, _, err := e.resolveFunctionStep(stepOffset+i, functionName, args)
		if err != nil {
			return nil, nil, nil, nil, nil, err
		}
		prepared = append(prepared, preparedCall{
			call:         call,
			functionName: functionName,
			step:         step,
		})
	}

	for _, item := range prepared {
		runResult, runErr := e.executor.Execute(ctx, string(req.Mode), req.Input, workflow.WorkflowPlan{
			Reason: "YZMA native tool call",
			Steps:  []workflow.WorkflowStep{item.step},
		})
		if runErr != nil {
			if runResult.Status == workflow.RunStatusDenied || runResult.Status == workflow.RunStatusBlocked {
				return nil, nil, nil, nil, nil, fmt.Errorf("%s", runResult.ErrorText)
			}
			return nil, nil, nil, nil, nil, fmt.Errorf("workflow execution failed: %w", runErr)
		}
		if runResult.Status != workflow.RunStatusCompleted || len(runResult.StepRuns) == 0 {
			return nil, nil, nil, nil, nil, fmt.Errorf("tool call %q did not complete", item.functionName)
		}
		stepRunsOut = append(stepRunsOut, runResult.StepRuns...)

		stepEvidence, stepProvenance := stepResultsToEvidence(runResult.StepRuns)
		evidence = append(evidence, stepEvidence...)
		provenance = append(provenance, stepProvenance...)

		last := runResult.StepRuns[len(runResult.StepRuns)-1]
		observation := buildCommandObservation(last, e.catalog)
		contentBlob, marshalErr := json.Marshal(observation)
		if marshalErr != nil {
			return nil, nil, nil, nil, nil, fmt.Errorf("failed to marshal command observation: %w", marshalErr)
		}
		observations = append(observations, observation)
		toolMessages = append(toolMessages, llm.ChatMessage{
			Role:       "tool",
			Name:       item.functionName,
			Content:    string(contentBlob),
			ToolCallID: strings.TrimSpace(item.call.ID),
		})
	}

	return toolMessages, evidence, provenance, observations, stepRunsOut, nil
}

func toolCallSignature(call llm.ToolCall) string {
	args := call.Arguments
	if args == nil {
		args = map[string]any{}
	}
	blob, err := json.Marshal(args)
	if err != nil {
		return strings.TrimSpace(call.Name) + "|{}"
	}
	return strings.TrimSpace(call.Name) + "|" + string(blob)
}

func trimEvidenceTotal(evidence []model.ToolEvidence, limit int) []model.ToolEvidence {
	if limit <= 0 || len(evidence) == 0 {
		return evidence
	}
	total := 0
	out := make([]model.ToolEvidence, 0, len(evidence))
	for _, item := range evidence {
		clone := item
		chunk := len(clone.Command) + len(clone.Stdout) + len(clone.Stderr)
		if total+chunk > limit {
			remaining := limit - total
			if remaining <= 0 {
				break
			}
			if len(clone.Stdout) > remaining {
				clone.Stdout = strings.TrimSpace(clone.Stdout[:remaining]) + " ...[truncated]"
				clone.Stderr = ""
			}
		}
		total += len(clone.Command) + len(clone.Stdout) + len(clone.Stderr)
		out = append(out, clone)
		if total >= limit {
			break
		}
	}
	return out
}

func blockedWorkflowAskResult(reason string) model.AskResult {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "workflow could not run"
	}
	return model.AskResult{
		Answer:        "I need approved workflow steps to gather evidence, but execution is currently blocked.",
		Observations:  []string{"No approved workflow steps were executed."},
		Inferences:    []string{},
		Uncertainties: []string{reason},
		Assumptions:   []string{"Execution remains read-only in TOPS v1."},
		Notes:         []string{"Adjust execution policy if needed and rerun in an interactive terminal when approval is required."},
	}
}

func stepResultsToEvidence(steps []workflow.StepResult) ([]model.ToolEvidence, []model.Provenance) {
	evidence := make([]model.ToolEvidence, 0, len(steps))
	provenance := make([]model.Provenance, 0, len(steps))
	for _, step := range steps {
		item := model.ToolEvidence{
			Command:   strings.TrimSpace(step.Command + " " + strings.Join(step.Args, " ")),
			Stdout:    step.Stdout,
			Stderr:    step.Stderr,
			ExitCode:  step.ExitCode,
			Duration:  step.Duration,
			Succeeded: step.ErrorText == "" && step.ExitCode == 0,
		}
		evidence = append(evidence, item)
		if item.Succeeded {
			provenance = append(provenance, model.Provenance{
				Source: "approved workflow step",
				Detail: item.Command,
			})
		}
	}
	return evidence, provenance
}

func askSynthesisMaxTokens(profile model.AskResponseProfile, budgets optimization.TokenBudgets) int {
	count := profile.EnabledOptionalCount()
	base := budgets.AskSynthesisBase
	per := budgets.AskSynthesisPerField
	capVal := budgets.AskSynthesisCap
	if base <= 0 {
		base = 140
	}
	if per <= 0 {
		per = 70
	}
	if capVal <= 0 {
		capVal = 490
	}
	maxTokens := base + (per * count)
	if maxTokens > capVal {
		return capVal
	}
	return maxTokens
}

func askCompactPlannerMaxTokens(configured int) int {
	if configured <= 0 {
		configured = 160
	}
	if configured < 128 {
		return 128
	}
	if configured > 256 {
		return 256
	}
	return configured
}

func askCompactSynthesisMaxTokens(profile model.AskResponseProfile, budgets optimization.TokenBudgets) int {
	maxTokens := askSynthesisMaxTokens(profile, budgets)
	if maxTokens < 256 {
		return 256
	}
	if maxTokens > 512 {
		return 512
	}
	return maxTokens
}

func decideAskRoute(intelligenceMode model.IntelligenceMode, question string) askRouteDecision {
	switch intelligenceMode {
	case model.IntelligenceModeGrounded:
		return askRouteDecision{
			AskPath:  askPathGrounded,
			Strategy: askStrategyPlannedTools,
			Reason:   "grounded intelligence mode forces plan-first strategy",
		}
	case model.IntelligenceModeBlitz:
		if shouldUseConceptualAskPath(question) {
			return askRouteDecision{
				AskPath:  askPathBlitz,
				Strategy: askStrategyConceptual,
				Reason:   "blitz mode selected conceptual no-tool response strategy",
			}
		}
		return askRouteDecision{
			AskPath:  askPathBlitz,
			Strategy: askStrategyReactiveTools,
			Reason:   "blitz mode uses bounded reactive tool strategy",
		}
	default:
		if shouldUseConceptualAskPath(question) && !requiresPlanFirstGrounded(question) {
			return askRouteDecision{
				AskPath:  askPathBlitz,
				Strategy: askStrategyConceptual,
				Reason:   "query is conceptual and does not require local evidence",
			}
		}
		if requiresPlanFirstGrounded(question) {
			return askRouteDecision{
				AskPath:  askPathGrounded,
				Strategy: askStrategyPlannedTools,
				Reason:   "query complexity requires grounded plan-first strategy",
			}
		}
		return askRouteDecision{
			AskPath:  askPathBlitz,
			Strategy: askStrategyReactiveTools,
			Reason:   "query is suitable for bounded reactive tools",
		}
	}
}

func shouldUseDeepAskPath(question string) bool {
	lower := strings.ToLower(strings.TrimSpace(question))
	if lower == "" {
		return false
	}
	if len(lower) > 220 || len(strings.Fields(lower)) > 36 {
		return true
	}
	if strings.Count(lower, "?") > 1 {
		return true
	}
	deepSignals := []string{
		"compare",
		"difference",
		"different between",
		"versus",
		" vs ",
		"tradeoff",
		"why ",
		"explain",
		"summarize",
		"summary",
		"analyze",
		"analysis",
		"recommend",
		"best option",
		"which is better",
		"across",
		"top ",
		"table",
		"then ",
		"after that",
	}
	for _, signal := range deepSignals {
		if strings.Contains(lower, signal) {
			return true
		}
	}
	return false
}

func shouldUseConceptualAskPath(question string) bool {
	lower := strings.ToLower(strings.TrimSpace(question))
	if lower == "" {
		return false
	}
	if likelyRequiresLocalEvidence(lower) {
		return false
	}
	conceptualSignals := []string{
		"what is",
		"what does",
		"how does",
		"explain",
		"concept",
		"meaning of",
		"difference between",
		"when should",
		"why does",
	}
	for _, signal := range conceptualSignals {
		if strings.Contains(lower, signal) {
			return true
		}
	}
	return false
}

func requiresPlanFirstGrounded(question string) bool {
	if !shouldUseDeepAskPath(question) {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(question))
	if lower == "" {
		return false
	}
	if len(lower) > 220 || len(strings.Fields(lower)) > 36 || strings.Count(lower, "?") > 1 {
		return true
	}
	return containsAny(lower,
		"compare",
		"difference",
		"tradeoff",
		"which is better",
		"recommend",
		"diagnostic",
		"diagnose",
		"why ",
		"across",
		"in this repo",
		"in this project",
		"summarize",
		"analysis",
		"analyze",
	)
}

func defaultSemanticIntentForStrategy(question string, strategy string) model.SemanticIntent {
	intent := model.DefaultSemanticIntent()
	intent.RequestedFields = deriveRequestedFields(intent, question)
	switch strings.TrimSpace(strategy) {
	case askStrategyConceptual:
		intent.RequiresGrounding = false
		intent.Operation = "explain"
		intent.Entity = "custom"
	case askStrategyReactiveTools:
		intent.RequiresGrounding = true
		intent.Operation = "inspect"
	default:
		intent.RequiresGrounding = true
	}
	return intent
}

func cleanupAskResult(result model.AskResult, profile model.AskResponseProfile) model.AskResult {
	result.Answer = normalizeAnswerSentence(strings.TrimSpace(result.Answer))
	result.Observations = dedupeList(result.Observations)
	result.Inferences = dedupeList(result.Inferences)
	result.Uncertainties = dedupeList(result.Uncertainties)
	result.Assumptions = dedupeList(result.Assumptions)
	result.Notes = dedupeList(result.Notes)

	excludedFromNotes := make(map[string]struct{}, len(result.Observations)+len(result.Inferences)+len(result.Uncertainties))
	for _, item := range result.Observations {
		excludedFromNotes[item] = struct{}{}
	}
	for _, item := range result.Inferences {
		excludedFromNotes[item] = struct{}{}
	}
	for _, item := range result.Uncertainties {
		excludedFromNotes[item] = struct{}{}
	}
	filteredNotes := make([]string, 0, len(result.Notes))
	for _, note := range result.Notes {
		if _, ok := excludedFromNotes[note]; ok {
			continue
		}
		filteredNotes = append(filteredNotes, note)
	}
	result.Notes = filteredNotes

	if !profile.Observations {
		result.Observations = nil
	}
	if !profile.Inferences {
		result.Inferences = nil
	}
	if !profile.Uncertainties {
		result.Uncertainties = nil
	}
	if !profile.Assumptions {
		result.Assumptions = nil
	}
	if !profile.Notes {
		result.Notes = nil
	}

	return result
}

func (e Engine) parseTaggedFinalWithRepair(ctx context.Context, raw string, thinkOverride *bool) (string, error) {
	answer, err := parseTaggedFinalAnswer(raw)
	if err == nil {
		return answer, nil
	}
	if e.opt.RepairMaxRetries < 1 {
		return "", err
	}

	progress.UpdatePhase(ctx, "parser-retry")
	benchmetrics.IncrementRepair(ctx)
	repaired, repairErr := e.provider.Complete(progress.WithStreamEmission(ctx, false), llm.CompletionRequest{
		SystemPrompt:    "Return only this format:\nFINAL a: <answer>\nOptional tagged lines only when needed: o:, i:, u:, s:, n:",
		UserPrompt:      fmt.Sprintf("Previous output was invalid: %s\nRewrite it strictly in the required tagged format.\nInvalid output:\n%s", err.Error(), raw),
		Temperature:     -1,
		MaxTokens:       180,
		Think:           thinkOverride,
		SamplingProfile: llm.SamplingProfilePlanner,
	})
	if repairErr != nil {
		return "", err
	}
	answer, parseErr := parseTaggedFinalAnswer(repaired.Content)
	if parseErr != nil {
		return "", err
	}
	return answer, nil
}

func normalizeAnswerSentence(answer string) string {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return answer
	}
	lower := strings.ToLower(answer)
	if !strings.ContainsAny(answer, ".!?") {
		if strings.HasPrefix(lower, "you ") || strings.HasPrefix(lower, "the ") || strings.HasPrefix(lower, "your ") || strings.HasPrefix(lower, "it ") {
			return answer + "."
		}
		return "Based on the collected evidence, " + answer + "."
	}
	return answer
}

func dedupeList(items []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func deriveRequestedFields(intent model.SemanticIntent, question string) []string {
	fields := make([]string, 0, len(intent.RequestedFields)+4)
	fields = append(fields, intent.RequestedFields...)
	if len(fields) == 0 {
		fields = append(fields, intent.Projection...)
	}
	lower := strings.ToLower(strings.TrimSpace(question))
	if lower != "" {
		if strings.Contains(lower, "operating system") || containsWord(lower, "os") {
			fields = append(fields, "os")
		}
		if strings.Contains(lower, "kernel") {
			fields = append(fields, "kernel")
		}
		if strings.Contains(lower, "version") || strings.Contains(lower, "release") {
			fields = append(fields, "version")
		}
	}
	if len(fields) == 0 && strings.EqualFold(intent.Entity, "os") {
		fields = append(fields, "os")
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		norm := normalizeRequestedFieldName(field)
		if norm == "" {
			continue
		}
		if _, ok := seen[norm]; ok {
			continue
		}
		seen[norm] = struct{}{}
		out = append(out, norm)
	}
	return out
}

func missingRequestedFields(requested []string, observations []model.CommandObservation) []string {
	if len(requested) == 0 {
		return []string{}
	}
	covered := map[string]struct{}{}
	for _, obs := range observations {
		for _, field := range coveredRequestedFieldsFromObservation(obs) {
			covered[field] = struct{}{}
		}
	}
	missing := make([]string, 0, len(requested))
	for _, raw := range requested {
		field := normalizeRequestedFieldName(raw)
		if field == "" {
			continue
		}
		if _, ok := covered[field]; ok {
			continue
		}
		missing = append(missing, field)
	}
	return missing
}

func requestedFieldCoverageHint(missing []string) string {
	if len(missing) == 0 {
		return ""
	}
	msg := "Coverage gap: requested fields still unresolved: " + strings.Join(missing, ", ") + ". Select additional tool_calls only."
	if includesField(missing, "os") && includesField(missing, "kernel") && includesField(missing, "version") {
		msg += " Prefer uname with args [-srm] to ground all three together."
	}
	return msg
}

func coveredRequestedFieldsFromObservation(obs model.CommandObservation) []string {
	command := strings.ToLower(strings.TrimSpace(obs.CommandName))
	switch command {
	case "uname":
		return coveredFieldsFromUnameObservation(obs)
	case "sw_vers", "cat":
		return []string{"os", "version"}
	case "cmd":
		for _, arg := range obs.Args {
			if strings.Contains(strings.ToLower(strings.TrimSpace(arg)), "ver") {
				return []string{"os", "kernel", "version"}
			}
		}
		return []string{}
	default:
		return []string{}
	}
}

func coveredFieldsFromUnameObservation(obs model.CommandObservation) []string {
	flags := map[string]struct{}{}
	if len(obs.Args) == 0 {
		flags["-s"] = struct{}{}
	}
	for _, arg := range obs.Args {
		trimmed := strings.TrimSpace(arg)
		if trimmed == "" {
			continue
		}
		flags[trimmed] = struct{}{}
	}
	covered := map[string]struct{}{}
	if _, ok := flags["-a"]; ok {
		covered["os"] = struct{}{}
		covered["kernel"] = struct{}{}
		covered["version"] = struct{}{}
	}
	if _, ok := flags["-srm"]; ok {
		covered["os"] = struct{}{}
		covered["kernel"] = struct{}{}
		covered["version"] = struct{}{}
	}
	if _, ok := flags["-s"]; ok {
		covered["os"] = struct{}{}
		covered["kernel"] = struct{}{}
	}
	if _, ok := flags["-r"]; ok {
		covered["version"] = struct{}{}
	}
	out := make([]string, 0, len(covered))
	for _, key := range []string{"os", "kernel", "version"} {
		if _, ok := covered[key]; ok {
			out = append(out, key)
		}
	}
	return out
}

func normalizeRequestedFieldName(field string) string {
	f := strings.ToLower(strings.TrimSpace(field))
	if f == "" {
		return ""
	}
	f = strings.NewReplacer("-", "_", " ", "_").Replace(f)
	for strings.Contains(f, "__") {
		f = strings.ReplaceAll(f, "__", "_")
	}
	f = strings.Trim(f, "_")
	switch f {
	case "operating_system":
		return "os"
	case "kernel_name":
		return "kernel"
	case "kernel_release", "kernel_version", "release", "os_version":
		return "version"
	default:
		return f
	}
}

func includesField(fields []string, target string) bool {
	target = normalizeRequestedFieldName(target)
	for _, field := range fields {
		if normalizeRequestedFieldName(field) == target {
			return true
		}
	}
	return false
}

func containsWord(input, word string) bool {
	input = strings.TrimSpace(strings.ToLower(input))
	word = strings.TrimSpace(strings.ToLower(word))
	if input == "" || word == "" {
		return false
	}
	for _, boundary := range []string{" ", "\t", "\n", "\r", ".", ",", "!", "?", ":", ";", "(", ")", "[", "]", "{", "}", "\"", "'"} {
		input = strings.ReplaceAll(input, boundary, " ")
	}
	for _, token := range strings.Fields(input) {
		if token == word {
			return true
		}
	}
	return false
}

func countNonEmptyLines(lines []string) int {
	count := 0
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

func splitLines(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return []string{}
	}
	return strings.Split(trimmed, "\n")
}

func evidencePayloadBytes(evidence []model.ToolEvidence) int {
	total := 0
	for _, item := range evidence {
		total += len(item.Command) + len(item.Stdout) + len(item.Stderr)
	}
	return total
}

func evidenceRowsUsed(evidence []model.ToolEvidence) int {
	total := 0
	for _, item := range evidence {
		total += countNonEmptyLines(splitLines(item.Stdout))
		total += countNonEmptyLines(splitLines(item.Stderr))
	}
	return total
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

func parseGroundingOverride(content string) (effective bool, reason string, ok bool) {
	content = strings.TrimSpace(content)
	if content == "" {
		return false, "", false
	}
	for _, blob := range jsonutil.Candidates(content) {
		var parsed struct {
			Effective bool   `json:"effective_requires_grounding"`
			Reason    string `json:"grounding_override_reason"`
		}
		if err := json.Unmarshal([]byte(blob), &parsed); err != nil {
			continue
		}
		if strings.TrimSpace(parsed.Reason) == "" {
			continue
		}
		return parsed.Effective, strings.TrimSpace(parsed.Reason), true
	}
	return false, "", false
}

func estimatePromptTokens(text string) int {
	if strings.TrimSpace(text) == "" {
		return 0
	}
	chars := len(text)
	if chars <= 0 {
		return 0
	}
	// Conservative rough estimate for diagnostic telemetry only.
	estimate := chars / 4
	if estimate <= 0 {
		return 1
	}
	return estimate
}

func estimateChatPromptTokens(system string, messages []llm.ChatMessage) int {
	total := estimatePromptTokens(system)
	for _, msg := range messages {
		total += estimatePromptTokens(msg.Role)
		total += estimatePromptTokens(msg.Name)
		total += estimatePromptTokens(msg.Content)
		for _, tc := range msg.ToolCalls {
			total += estimatePromptTokens(tc.Name)
			if tc.Arguments != nil {
				if blob, err := json.Marshal(tc.Arguments); err == nil {
					total += estimatePromptTokens(string(blob))
				}
			}
		}
	}
	return total
}

func estimateToolSchemaTokens(defs []llm.ToolDefinition) int {
	if len(defs) == 0 {
		return 0
	}
	blob, err := json.Marshal(defs)
	if err != nil {
		return 0
	}
	return estimatePromptTokens(string(blob))
}

func estimateAskContextTokens(req model.CoreRequest, intent model.SemanticIntent) int {
	total := estimatePromptTokens(req.Input) + estimatePromptTokens(req.CWD) + estimatePromptTokens(req.OS)
	if platformBlob, err := json.Marshal(model.NormalizePlatformContext(req.PlatformContext)); err == nil {
		total += estimatePromptTokens(string(platformBlob))
	}
	if intentBlob, err := json.Marshal(intent); err == nil {
		total += estimatePromptTokens(string(intentBlob))
	}
	return total
}

func extractDirectAnswerFromPlanningDecision(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	for _, blob := range jsonutil.Candidates(raw) {
		var payload map[string]any
		if err := json.Unmarshal([]byte(blob), &payload); err != nil {
			continue
		}
		for _, key := range []string{"answer", "final_answer", "response"} {
			value, ok := payload[key]
			if !ok {
				continue
			}
			text := strings.TrimSpace(fmt.Sprintf("%v", value))
			if text == "" {
				continue
			}
			return text, true
		}
	}
	if strings.HasPrefix(raw, "{") || strings.HasPrefix(raw, "[") {
		return "", false
	}
	return raw, true
}

func canCompactReturnDirect(req model.CoreRequest, plannerContent string) bool {
	input := strings.ToLower(strings.TrimSpace(req.Input))
	if input == "" {
		return false
	}
	if likelyRequiresLocalEvidence(input) {
		return false
	}
	content := strings.ToLower(strings.TrimSpace(plannerContent))
	if containsAny(content,
		"cannot access",
		"run command",
		"tool call",
		"local environment",
		"on your machine",
	) {
		return false
	}
	return true
}

func likelyRequiresLocalEvidence(input string) bool {
	input = strings.ToLower(strings.TrimSpace(input))
	if input == "" {
		return false
	}
	if fastlane.ExtractIntent(input).Type != fastlane.IntentUnknown {
		return true
	}
	return containsAny(input,
		"current directory",
		"current folder",
		"working directory",
		"cwd",
		"in this directory",
		"in this folder",
		"in this repo",
		"in this repository",
		"in this project",
		"on this machine",
		"on my machine",
		"with evidence",
		"evidence",
		"my os",
		"my operating system",
		"kernel version",
		"machine architecture",
		"system architecture",
		"os kernel",
		"hostname",
		"current user",
		"files",
		"directories",
		"git state",
		"git status",
		"installed tools",
		"installed versions",
		"env var",
		"environment variable",
		"ports",
		"processes",
		"disk usage",
		"network",
		"system state",
	)
}

func compactPlannerRetryHint(req model.CoreRequest) string {
	intent := fastlane.ExtractIntent(req.Input)
	if intent.Type == fastlane.IntentUnknown {
		return ""
	}
	template, ok := fastlane.BuildTemplateCommand(intent, model.NormalizePlatformContext(req.PlatformContext))
	if !ok || strings.TrimSpace(template.CommandName) == "" {
		return ""
	}
	argsBlob, err := json.Marshal(template.Args)
	if err != nil {
		return ""
	}
	return fmt.Sprintf(
		"Local evidence is required for this request. Return native tool_calls only with empty content. Use run_readonly_command with command_name=%q and args=%s. Do not return prose.",
		template.CommandName,
		string(argsBlob),
	)
}

func askGroundedRepairBudget(req model.CoreRequest, configured int) int {
	if configured < 0 {
		return 0
	}
	if configured == 0 {
		return 0
	}
	input := strings.TrimSpace(req.Input)
	if input == "" {
		return configured
	}
	intent := fastlane.ExtractIntent(input)
	if intent.Type != fastlane.IntentUnknown && !shouldUseDeepAskPath(input) {
		return 1
	}
	return configured
}

func containsAny(value string, parts ...string) bool {
	for _, part := range parts {
		part = strings.ToLower(strings.TrimSpace(part))
		if part == "" {
			continue
		}
		if strings.Contains(value, part) {
			return true
		}
	}
	return false
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
