package ask

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"tops/internal/model"
	"tops/internal/ops/benchmetrics"
	"tops/internal/parser"
	"tops/internal/runtime/llm"
	"tops/internal/runtime/optimization"
	"tops/internal/runtime/prompt"
	"tops/internal/runtime/tools"
	"tops/internal/runtime/workflow"
)

type runnerStub struct {
	result tools.ToolResult
	err    error
	calls  int
	specs  []tools.ToolSpec
}

func (r *runnerStub) Run(_ context.Context, spec tools.ToolSpec) (tools.ToolResult, error) {
	r.calls++
	r.specs = append(r.specs, spec)
	return r.result, r.err
}

type nativeProviderStub struct {
	toolChatCalls int
	toolRequests  []llm.ToolChatRequest
}

func (p *nativeProviderStub) Name() string { return "native-mock" }

func (p *nativeProviderStub) Complete(_ context.Context, _ llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{}, nil
}

func (p *nativeProviderStub) ToolChat(_ context.Context, req llm.ToolChatRequest, _ func(string), _ func(string)) (llm.ToolChatResponse, error) {
	p.toolChatCalls++
	p.toolRequests = append(p.toolRequests, req)
	if p.toolChatCalls == 1 {
		return llm.ToolChatResponse{
			ToolCalls: []llm.ToolCall{
				{
					ID:   "call-1",
					Name: "run_readonly_command",
					Arguments: map[string]any{
						"command_name": "uname",
						"args":         []any{"-srm"},
					},
				},
			},
		}, nil
	}
	return llm.ToolChatResponse{
		Content: "Kernel: Darwin 24.0.0 arm64",
	}, nil
}

type capabilityAskProviderStub struct {
	completeCalls int
	toolChatCalls int
	completeReqs  []llm.CompletionRequest
	toolReqs      []llm.ToolChatRequest
	completeText  string
}

func (p *capabilityAskProviderStub) Name() string { return "native-mock" }

func (p *capabilityAskProviderStub) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.completeCalls++
	p.completeReqs = append(p.completeReqs, req)
	if strings.TrimSpace(p.completeText) != "" {
		return llm.CompletionResponse{Content: p.completeText}, nil
	}
	return llm.CompletionResponse{Content: `{
		"action": "use_capability",
		"capability_id": "filesystem.count",
		"arguments": {
			"entity": "directory",
			"scope": "current_directory",
			"visibility": "visible_only",
			"recursion": "none"
		}
	}`}, nil
}

func (p *capabilityAskProviderStub) ToolChat(_ context.Context, req llm.ToolChatRequest, _ func(string), _ func(string)) (llm.ToolChatResponse, error) {
	p.toolChatCalls++
	p.toolReqs = append(p.toolReqs, req)
	return llm.ToolChatResponse{Content: "There are 2 visible directories."}, nil
}

func TestBlitzCapabilityAskFinalizesAfterCompiledToolEvidence(t *testing.T) {
	runner := &runnerStub{
		result: tools.ToolResult{
			Stdout:   "./visible\n./.hidden\n./other\n",
			ExitCode: 0,
			Duration: 5 * time.Millisecond,
		},
	}
	provider := &capabilityAskProviderStub{}
	engine := NewEngine(provider, prompt.NewBuilder(), parser.New(), runner, 10*time.Second)

	result, err := engine.Run(context.Background(), model.CoreRequest{
		Mode:                    model.ModeAsk,
		Input:                   "How many directories are in this folder?",
		ExecutionReadOnlyPolicy: string(workflow.ActionPermissionAllow),
		ExecutionWritePolicy:    string(workflow.ActionPermissionRequest),
		AskResponseProfile:      model.DefaultAskResponseProfile(),
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if !strings.Contains(result.Answer, "2") {
		t.Fatalf("expected LLM final answer from capability evidence, got %q", result.Answer)
	}
	if provider.completeCalls != 1 {
		t.Fatalf("expected one capability planner LLM call, got %d", provider.completeCalls)
	}
	if provider.toolChatCalls != 1 {
		t.Fatalf("expected one LLM finalizer call, got %d", provider.toolChatCalls)
	}
	if runner.calls != 1 {
		t.Fatalf("expected one compiled tool execution, got %d", runner.calls)
	}
	if len(runner.specs) != 1 || runner.specs[0].Name != "find" {
		t.Fatalf("expected compiled find execution, got %+v", runner.specs)
	}
	if args := strings.Join(runner.specs[0].Args, " "); strings.Contains(args, "-name") {
		t.Fatalf("expected no shell hidden filter in find args, got %v", runner.specs[0].Args)
	}
	if len(provider.toolReqs) != 1 || !strings.Contains(provider.toolReqs[0].Messages[len(provider.toolReqs[0].Messages)-1].Content, "count=2") {
		t.Fatalf("expected compact postprocessed evidence in finalizer messages, got %+v", provider.toolReqs)
	}
	if provider.toolReqs[0].MaxTokens != 160 {
		t.Fatalf("expected compact capability finalizer max tokens=160, got %d", provider.toolReqs[0].MaxTokens)
	}
	if !strings.Contains(strings.ToLower(provider.toolReqs[0].Messages[len(provider.toolReqs[0].Messages)-1].Content), "one short sentence") {
		t.Fatalf("expected compact one-sentence finalizer instruction, got %q", provider.toolReqs[0].Messages[len(provider.toolReqs[0].Messages)-1].Content)
	}
}

func TestBlitzCapabilityAskDoesNotReportNativeToolSchemaTokens(t *testing.T) {
	runner := &runnerStub{
		result: tools.ToolResult{
			Stdout:   "./visible\n",
			ExitCode: 0,
			Duration: 5 * time.Millisecond,
		},
	}
	provider := &capabilityAskProviderStub{}
	engine := NewEngine(provider, prompt.NewBuilder(), parser.New(), runner, 10*time.Second)
	collector := benchmetrics.NewCollector()

	_, err := engine.Run(benchmetrics.WithCollector(context.Background(), collector), model.CoreRequest{
		Mode:                    model.ModeAsk,
		Input:                   "How many directories are in this folder?",
		ExecutionReadOnlyPolicy: string(workflow.ActionPermissionAllow),
		ExecutionWritePolicy:    string(workflow.ActionPermissionRequest),
		AskResponseProfile:      model.DefaultAskResponseProfile(),
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if got := collector.Snapshot().ToolSchemaTokens; got != 0 {
		t.Fatalf("expected capability ask to report zero native tool schema tokens, got %d", got)
	}
}

func TestBlitzCapabilityAskRejectsPrematureFinalAnswerForLocalPrompt(t *testing.T) {
	runner := &runnerStub{}
	provider := &capabilityAskProviderStub{
		completeText: `{"action":"final_answer","final_answer":"There are 2 directories."}`,
	}
	engine := NewEngine(provider, prompt.NewBuilder(), parser.New(), runner, 10*time.Second)

	result, err := engine.Run(context.Background(), model.CoreRequest{
		Mode:                    model.ModeAsk,
		Input:                   "How many directories are in this folder?",
		ExecutionReadOnlyPolicy: string(workflow.ActionPermissionAllow),
		ExecutionWritePolicy:    string(workflow.ActionPermissionRequest),
		AskResponseProfile:      model.DefaultAskResponseProfile(),
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if runner.calls != 0 {
		t.Fatalf("expected no tool execution from premature final_answer, got %d", runner.calls)
	}
	if strings.Contains(result.Answer, "2 directories") {
		t.Fatalf("expected premature final_answer to be rejected, got %q", result.Answer)
	}
}

func TestBlitzNativeAskFinalizesAfterToolEvidence(t *testing.T) {
	runner := &runnerStub{
		result: tools.ToolResult{
			Stdout:   "Darwin 24.0.0 arm64",
			ExitCode: 0,
			Duration: 5 * time.Millisecond,
		},
	}
	provider := &nativeProviderStub{}
	engine := NewEngine(provider, prompt.NewBuilder(), parser.New(), runner, 10*time.Second)

	result, err := engine.Run(context.Background(), model.CoreRequest{
		Mode:                    model.ModeAsk,
		Input:                   "What is my operating system kernel and version?",
		ExecutionReadOnlyPolicy: string(workflow.ActionPermissionAllow),
		ExecutionWritePolicy:    string(workflow.ActionPermissionRequest),
		AskResponseProfile:      model.DefaultAskResponseProfile(),
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if !strings.Contains(strings.ToLower(result.Answer), "kernel") {
		t.Fatalf("expected deterministic kernel answer, got %q", result.Answer)
	}
	if provider.toolChatCalls != 2 {
		t.Fatalf("expected planner + finalizing planner call, got %d", provider.toolChatCalls)
	}
	if len(provider.toolRequests) != 2 || provider.toolRequests[0].SamplingProfile != llm.SamplingProfilePlanner {
		t.Fatalf("expected planner sampling profile on compact planner call, got %+v", provider.toolRequests)
	}
	if provider.toolRequests[0].MaxTokens != 256 {
		t.Fatalf("expected blitz planner max tokens=256, got %d", provider.toolRequests[0].MaxTokens)
	}
	if runner.calls != 1 {
		t.Fatalf("expected one tool execution, got %d", runner.calls)
	}
	if len(runner.specs) != 1 || runner.specs[0].Name != "uname" {
		t.Fatalf("expected uname execution, got %+v", runner.specs)
	}
}

type compactAskProfileProviderStub struct {
	callCount int
	requests  []llm.ToolChatRequest
}

func (p *compactAskProfileProviderStub) Name() string { return "native-mock" }

func (p *compactAskProfileProviderStub) Complete(_ context.Context, _ llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{}, nil
}

func (p *compactAskProfileProviderStub) ToolChat(_ context.Context, req llm.ToolChatRequest, _ func(string), _ func(string)) (llm.ToolChatResponse, error) {
	p.callCount++
	p.requests = append(p.requests, req)
	if p.callCount == 1 {
		return llm.ToolChatResponse{
			ToolCalls: []llm.ToolCall{
				{
					ID:   "call-1",
					Name: "run_readonly_command",
					Arguments: map[string]any{
						"command_name": "ls",
						"args":         []string{"-a"},
					},
				},
			},
		}, nil
	}
	return llm.ToolChatResponse{Content: "Current directory contains visible files."}, nil
}

func TestCompactNativeAskFinalizerUsesAskSamplingProfile(t *testing.T) {
	runner := &runnerStub{
		result: tools.ToolResult{
			Stdout:   "a.txt\nb.txt",
			ExitCode: 0,
			Duration: 5 * time.Millisecond,
		},
	}
	provider := &compactAskProfileProviderStub{}
	engine := NewEngine(provider, prompt.NewBuilder(), parser.New(), runner, 10*time.Second)

	_, ok, err := engine.runCompactNative(context.Background(), model.CoreRequest{
		Mode:                    model.ModeAsk,
		Input:                   "Summarize this local command output briefly.",
		ExecutionReadOnlyPolicy: string(workflow.ActionPermissionAllow),
		ExecutionWritePolicy:    string(workflow.ActionPermissionRequest),
		AskResponseProfile:      model.DefaultAskResponseProfile(),
	}, provider)
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if !ok {
		t.Fatal("expected compact native path to produce an answer")
	}
	if provider.callCount != 2 {
		t.Fatalf("expected compact planner + finalizer calls, got %d", provider.callCount)
	}
	if provider.requests[0].SamplingProfile != llm.SamplingProfilePlanner {
		t.Fatalf("expected first call planner profile, got %q", provider.requests[0].SamplingProfile)
	}
	expectedPlannerMax := askCompactPlannerMaxTokens(optimization.Default().TokenBudgets.AskPlanningMaxTokens)
	if provider.requests[0].MaxTokens != expectedPlannerMax {
		t.Fatalf("expected compact planner max tokens=%d, got %d", expectedPlannerMax, provider.requests[0].MaxTokens)
	}
	if provider.requests[1].SamplingProfile != llm.SamplingProfileAsk {
		t.Fatalf("expected second call ask profile, got %q", provider.requests[1].SamplingProfile)
	}
	expectedFinalMax := askCompactSynthesisMaxTokens(model.DefaultAskResponseProfile(), optimization.Default().TokenBudgets)
	if provider.requests[1].MaxTokens != expectedFinalMax {
		t.Fatalf("expected compact synthesis max tokens=%d, got %d", expectedFinalMax, provider.requests[1].MaxTokens)
	}
}

type compactNoToolProviderStub struct {
	requests []llm.ToolChatRequest
	content  string
}

func (p *compactNoToolProviderStub) Name() string { return "native-mock" }

func (p *compactNoToolProviderStub) Complete(_ context.Context, _ llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{}, nil
}

func (p *compactNoToolProviderStub) ToolChat(_ context.Context, req llm.ToolChatRequest, _ func(string), _ func(string)) (llm.ToolChatResponse, error) {
	p.requests = append(p.requests, req)
	return llm.ToolChatResponse{Content: p.content}, nil
}

func TestCompactNativeAskNoToolLocalQuestionEscalates(t *testing.T) {
	provider := &compactNoToolProviderStub{content: "Your current working directory is /tmp."}
	engine := NewEngine(provider, prompt.NewBuilder(), parser.New(), &runnerStub{}, 10*time.Second)

	_, handled, err := engine.runCompactNative(context.Background(), model.CoreRequest{
		Mode:               model.ModeAsk,
		Input:              "What is my current working directory?",
		AskResponseProfile: model.DefaultAskResponseProfile(),
	}, provider)
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if handled {
		t.Fatal("expected compact path to escalate for local grounded question with no tool calls")
	}
	if len(provider.requests) < 1 {
		t.Fatalf("expected at least one planner request, got %d", len(provider.requests))
	}
	if len(provider.requests) > 2 {
		t.Fatalf("expected at most two planner requests after retry hint, got %d", len(provider.requests))
	}
}

func TestCompactNativeAskNoToolConceptualQuestionAllowsDirectAnswer(t *testing.T) {
	provider := &compactNoToolProviderStub{content: "Dependency injection is a design pattern where dependencies are supplied from outside."}
	engine := NewEngine(provider, prompt.NewBuilder(), parser.New(), &runnerStub{}, 10*time.Second)

	result, handled, err := engine.runCompactNative(context.Background(), model.CoreRequest{
		Mode:               model.ModeAsk,
		Input:              "What is dependency injection conceptually?",
		AskResponseProfile: model.DefaultAskResponseProfile(),
	}, provider)
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if !handled {
		t.Fatal("expected compact path to allow direct conceptual answer")
	}
	if strings.TrimSpace(result.Answer) == "" {
		t.Fatal("expected non-empty direct conceptual answer")
	}
}

type groundedAskProfileProviderStub struct {
	callCount        int
	completeRequests []llm.CompletionRequest
}

func (p *groundedAskProfileProviderStub) Name() string { return "native-mock" }

func (p *groundedAskProfileProviderStub) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.callCount++
	p.completeRequests = append(p.completeRequests, req)
	switch p.callCount {
	case 1:
		return llm.CompletionResponse{
			Content: `{"workflow_plan":{"reason":"Gather kernel evidence","steps":[{"id":"s1","intent":"Read os and kernel","function_name":"run_readonly_command","function_args":{"command_name":"uname","args":["-srm"]},"expected_evidence":"uname output"}]},"effective_requires_grounding":true}`,
		}, nil
	default:
		return llm.CompletionResponse{Content: "From evidence: Darwin kernel 24.0.0 on arm64."}, nil
	}
}

func TestDeepNativeAskUsesExplicitPlannerAndSynthesisMaxTokens(t *testing.T) {
	runner := &runnerStub{
		result: tools.ToolResult{
			Stdout:   "Darwin 24.0.0 arm64",
			ExitCode: 0,
			Duration: 5 * time.Millisecond,
		},
	}
	provider := &groundedAskProfileProviderStub{}
	engine := NewEngine(provider, prompt.NewBuilder(), parser.New(), runner, 10*time.Second)
	intent := model.DefaultSemanticIntent()
	intent.Operation = "summarize"
	intent.Entity = "os"
	intent.Scope = "system"
	intent.RequiresGrounding = true
	intent.RequestedFields = []string{"os", "kernel", "version"}

	_, err := engine.runGroundedNative(context.Background(), model.CoreRequest{
		Mode:                    model.ModeAsk,
		Input:                   "Can you infer OS, kernel, and architecture with evidence?",
		ExecutionReadOnlyPolicy: string(workflow.ActionPermissionAllow),
		ExecutionWritePolicy:    string(workflow.ActionPermissionRequest),
		AskResponseProfile:      model.DefaultAskResponseProfile(),
	}, intent, nil)
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if len(provider.completeRequests) != 2 {
		t.Fatalf("expected planner + final completion calls, got %d", len(provider.completeRequests))
	}
	if provider.completeRequests[0].MaxTokens != 256 {
		t.Fatalf("expected grounded planner max tokens=256, got %d", provider.completeRequests[0].MaxTokens)
	}
	last := provider.completeRequests[len(provider.completeRequests)-1]
	if last.SamplingProfile != llm.SamplingProfileAsk {
		t.Fatalf("expected final ask sampling profile, got %q", last.SamplingProfile)
	}
	if last.MaxTokens != 700 {
		t.Fatalf("expected deep synthesis max tokens=700, got %d", last.MaxTokens)
	}
}

type groundedNoToolPlanProviderStub struct {
	completeCalls int
}

func (p *groundedNoToolPlanProviderStub) Name() string { return "native-mock" }

func (p *groundedNoToolPlanProviderStub) Complete(_ context.Context, _ llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.completeCalls++
	return llm.CompletionResponse{
		Content: `{"effective_requires_grounding":true,"grounding_override_reason":"insufficient details"}`,
	}, nil
}

func TestGroundedNativeFailsClosedWhenPlannerProducesNoToolPlan(t *testing.T) {
	runner := &runnerStub{}
	provider := &groundedNoToolPlanProviderStub{}
	engine := NewEngine(provider, prompt.NewBuilder(), parser.New(), runner, 10*time.Second)
	intent := model.DefaultSemanticIntent()
	intent.RequiresGrounding = true
	intent.RequestedFields = []string{"kernel", "version"}

	result, err := engine.runGroundedNative(context.Background(), model.CoreRequest{
		Mode:                    model.ModeAsk,
		Input:                   "What kernel version am I on?",
		ExecutionReadOnlyPolicy: string(workflow.ActionPermissionAllow),
		ExecutionWritePolicy:    string(workflow.ActionPermissionRequest),
		AskResponseProfile:      model.DefaultAskResponseProfile(),
	}, intent, nil)
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if provider.completeCalls != 2 {
		t.Fatalf("expected initial plan + one repair call, got %d", provider.completeCalls)
	}
	if runner.calls != 0 {
		t.Fatalf("expected no tool execution when planner failed to provide plan, got %d", runner.calls)
	}
	if !strings.Contains(strings.ToLower(result.Answer), "approved workflow steps") {
		t.Fatalf("expected fail-closed blocked answer, got %q", result.Answer)
	}
}

func TestShouldUseDeepAskPathSignals(t *testing.T) {
	if shouldUseDeepAskPath("What is my current directory?") {
		t.Fatal("expected simple question to stay on compact path")
	}
	if !shouldUseDeepAskPath("Compare Docker and Podman and explain tradeoffs") {
		t.Fatal("expected comparative/explanatory query to require deep path")
	}
}

func TestDecideAskRoute(t *testing.T) {
	tests := []struct {
		name      string
		mode      model.IntelligenceMode
		question  string
		path      string
		strategy  string
		reasonSub string
	}{
		{
			name:      "grounded mode always grounded",
			mode:      model.IntelligenceModeGrounded,
			question:  "What is my current directory?",
			path:      askPathGrounded,
			strategy:  askStrategyPlannedTools,
			reasonSub: "plan-first strategy",
		},
		{
			name:      "auto simple query uses blitz reactive tools",
			mode:      model.IntelligenceModeAuto,
			question:  "What is my current directory?",
			path:      askPathBlitz,
			strategy:  askStrategyReactiveTools,
			reasonSub: "reactive tools",
		},
		{
			name:      "auto kernel query uses blitz reactive tools",
			mode:      model.IntelligenceModeAuto,
			question:  "What kernel version am I on?",
			path:      askPathBlitz,
			strategy:  askStrategyReactiveTools,
			reasonSub: "reactive tools",
		},
		{
			name:      "auto complex query uses grounded",
			mode:      model.IntelligenceModeAuto,
			question:  "Compare Docker and Podman and explain tradeoffs",
			path:      askPathGrounded,
			strategy:  askStrategyPlannedTools,
			reasonSub: "complexity",
		},
		{
			name:      "auto conceptual query uses conceptual strategy",
			mode:      model.IntelligenceModeAuto,
			question:  "Explain git commit",
			path:      askPathBlitz,
			strategy:  askStrategyConceptual,
			reasonSub: "conceptual",
		},
		{
			name:      "auto diagnostic query uses grounded strategy",
			mode:      model.IntelligenceModeAuto,
			question:  "Inspect this repo and summarize what build system it uses and how tests run",
			path:      askPathGrounded,
			strategy:  askStrategyPlannedTools,
			reasonSub: "complexity",
		},
		{
			name:      "blitz conceptual query stays conceptual",
			mode:      model.IntelligenceModeBlitz,
			question:  "Explain git commit",
			path:      askPathBlitz,
			strategy:  askStrategyConceptual,
			reasonSub: "conceptual",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			route := decideAskRoute(tc.mode, tc.question)
			if route.AskPath != tc.path {
				t.Fatalf("expected path=%q, got %q (reason=%q)", tc.path, route.AskPath, route.Reason)
			}
			if route.Strategy != tc.strategy {
				t.Fatalf("expected strategy=%q, got %q", tc.strategy, route.Strategy)
			}
			if !strings.Contains(strings.ToLower(route.Reason), strings.ToLower(tc.reasonSub)) {
				t.Fatalf("expected reason containing %q, got %q", tc.reasonSub, route.Reason)
			}
		})
	}
}

func TestAskCompactTokenBudgets(t *testing.T) {
	if got := askCompactPlannerMaxTokens(0); got != 160 {
		t.Fatalf("expected compact planner default 160 tokens, got %d", got)
	}
	if got := askCompactPlannerMaxTokens(400); got != 256 {
		t.Fatalf("expected compact planner cap 256, got %d", got)
	}

	defaultProfile := model.DefaultAskResponseProfile()
	budgets := optimization.Default().TokenBudgets
	expectedDefault := askSynthesisMaxTokens(defaultProfile, budgets)
	if expectedDefault < 256 {
		expectedDefault = 256
	}
	if expectedDefault > 512 {
		expectedDefault = 512
	}
	if got := askCompactSynthesisMaxTokens(defaultProfile, budgets); got != expectedDefault {
		t.Fatalf("expected compact synthesis default %d tokens, got %d", expectedDefault, got)
	}
	answerOnly := model.AskResponseProfile{}
	expectedAnswerOnly := askSynthesisMaxTokens(answerOnly, budgets)
	if expectedAnswerOnly < 256 {
		expectedAnswerOnly = 256
	}
	if expectedAnswerOnly > 512 {
		expectedAnswerOnly = 512
	}
	if got := askCompactSynthesisMaxTokens(answerOnly, budgets); got != expectedAnswerOnly {
		t.Fatalf("expected answer-only compact synthesis %d tokens, got %d", expectedAnswerOnly, got)
	}
}

func TestParseGroundingOverrideHandlesNoisyMultiObjectContent(t *testing.T) {
	raw := `prefix {"junk":"ignore"}
{"effective_requires_grounding":false,"grounding_override_reason":"Not needed for conceptual answer"}
<tool_call>{"name":"run_readonly_command"}</tool_call>`

	effective, reason, ok := parseGroundingOverride(raw)
	if !ok {
		t.Fatal("expected override to parse from noisy content")
	}
	if effective {
		t.Fatal("expected effective_requires_grounding=false")
	}
	if reason != "Not needed for conceptual answer" {
		t.Fatalf("unexpected reason: %q", reason)
	}
}

type blitzSequenceProviderStub struct {
	responses []llm.ToolChatResponse
	requests  []llm.ToolChatRequest
	calls     int
}

func (p *blitzSequenceProviderStub) Name() string { return "native-mock" }

func (p *blitzSequenceProviderStub) Complete(_ context.Context, _ llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{}, nil
}

func (p *blitzSequenceProviderStub) ToolChat(_ context.Context, req llm.ToolChatRequest, _ func(string), _ func(string)) (llm.ToolChatResponse, error) {
	p.calls++
	p.requests = append(p.requests, req)
	if p.calls <= len(p.responses) {
		return p.responses[p.calls-1], nil
	}
	return llm.ToolChatResponse{Content: "unexpected extra call"}, nil
}

func TestBlitzLocalNoToolThenRepairNoToolFailsClosedWithoutThirdLLM(t *testing.T) {
	provider := &blitzSequenceProviderStub{
		responses: []llm.ToolChatResponse{
			{Content: ""},
			{Content: ""},
		},
	}
	runner := &runnerStub{}
	engine := NewEngine(provider, prompt.NewBuilder(), parser.New(), runner, 10*time.Second)
	intent := model.DefaultSemanticIntent()
	intent.RequiresGrounding = true
	intent.RequestedFields = []string{"kernel", "version"}

	result, err := engine.runBlitzNative(context.Background(), model.CoreRequest{
		Mode:                    model.ModeAsk,
		Input:                   "What kernel version am I on?",
		ExecutionReadOnlyPolicy: string(workflow.ActionPermissionAllow),
		ExecutionWritePolicy:    string(workflow.ActionPermissionRequest),
		AskResponseProfile:      model.DefaultAskResponseProfile(),
	}, provider, intent, boolPtr(false))
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if provider.calls != 2 {
		t.Fatalf("expected planner + one repair call, got %d", provider.calls)
	}
	if runner.calls != 0 {
		t.Fatalf("expected no tool execution, got %d", runner.calls)
	}
	if !strings.Contains(strings.ToLower(result.Answer), "approved workflow steps") {
		t.Fatalf("expected fail-closed answer, got %q", result.Answer)
	}
}

func TestBlitzDirectoryCountNoToolThenRepairNoToolFailsClosed(t *testing.T) {
	provider := &blitzSequenceProviderStub{
		responses: []llm.ToolChatResponse{
			{Content: ""},
			{Content: ""},
		},
	}
	engine := NewEngine(provider, prompt.NewBuilder(), parser.New(), &runnerStub{}, 10*time.Second)
	intent := model.DefaultSemanticIntent()
	intent.RequiresGrounding = true
	intent.RequestedFields = []string{"directories"}

	result, err := engine.runBlitzNative(context.Background(), model.CoreRequest{
		Mode:                    model.ModeAsk,
		Input:                   "How many directories are in this folder?",
		ExecutionReadOnlyPolicy: string(workflow.ActionPermissionAllow),
		ExecutionWritePolicy:    string(workflow.ActionPermissionRequest),
		AskResponseProfile:      model.DefaultAskResponseProfile(),
	}, provider, intent, boolPtr(false))
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if provider.calls != 2 {
		t.Fatalf("expected planner + one repair call, got %d", provider.calls)
	}
	if !strings.Contains(strings.ToLower(result.Answer), "approved workflow steps") {
		t.Fatalf("expected fail-closed answer, got %q", result.Answer)
	}
}

func TestBlitzLocalRepairProducesToolThenFinalAnswer(t *testing.T) {
	provider := &blitzSequenceProviderStub{
		responses: []llm.ToolChatResponse{
			{Content: ""},
			{
				ToolCalls: []llm.ToolCall{
					{
						ID:   "tc-1",
						Name: "run_readonly_command",
						Arguments: map[string]any{
							"command_name": "uname",
							"args":         []any{"-srm"},
						},
					},
				},
			},
			{Content: "Kernel evidence shows Darwin 24.0.0 arm64."},
		},
	}
	runner := &runnerStub{
		result: tools.ToolResult{
			Stdout:   "Darwin 24.0.0 arm64",
			ExitCode: 0,
			Duration: 5 * time.Millisecond,
		},
	}
	engine := NewEngine(provider, prompt.NewBuilder(), parser.New(), runner, 10*time.Second)
	intent := model.DefaultSemanticIntent()
	intent.RequiresGrounding = true
	intent.RequestedFields = []string{"kernel", "version"}

	result, err := engine.runBlitzNative(context.Background(), model.CoreRequest{
		Mode:                    model.ModeAsk,
		Input:                   "What kernel version am I on?",
		ExecutionReadOnlyPolicy: string(workflow.ActionPermissionAllow),
		ExecutionWritePolicy:    string(workflow.ActionPermissionRequest),
		AskResponseProfile:      model.DefaultAskResponseProfile(),
	}, provider, intent, boolPtr(false))
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if provider.calls != 3 {
		t.Fatalf("expected planner + repair + finalizer calls, got %d", provider.calls)
	}
	if runner.calls != 1 {
		t.Fatalf("expected one tool execution after repair, got %d", runner.calls)
	}
	if !strings.Contains(strings.ToLower(result.Answer), "darwin") {
		t.Fatalf("expected synthesized answer from evidence, got %q", result.Answer)
	}
}

func TestBlitzConceptualUsesSingleLLMCallWithoutTools(t *testing.T) {
	provider := &blitzSequenceProviderStub{
		responses: []llm.ToolChatResponse{
			{Content: "A shell pipeline connects command output to another command input."},
		},
	}
	runner := &runnerStub{}
	engine := NewEngine(provider, prompt.NewBuilder(), parser.New(), runner, 10*time.Second)
	intent := model.DefaultSemanticIntent()
	intent.RequiresGrounding = false

	result, err := engine.runBlitzNative(context.Background(), model.CoreRequest{
		Mode:                    model.ModeAsk,
		Input:                   "What is a shell pipeline?",
		ExecutionReadOnlyPolicy: string(workflow.ActionPermissionAllow),
		ExecutionWritePolicy:    string(workflow.ActionPermissionRequest),
		AskResponseProfile:      model.DefaultAskResponseProfile(),
	}, provider, intent, boolPtr(false))
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if provider.calls != 1 {
		t.Fatalf("expected one llm call for conceptual answer, got %d", provider.calls)
	}
	if runner.calls != 0 {
		t.Fatalf("expected no tool execution for conceptual answer, got %d", runner.calls)
	}
	if strings.TrimSpace(result.Answer) == "" {
		t.Fatal("expected conceptual answer")
	}
}

func TestBlitzLargeEvidenceIsCappedBeforeSynthesis(t *testing.T) {
	provider := &blitzSequenceProviderStub{
		responses: []llm.ToolChatResponse{
			{
				ToolCalls: []llm.ToolCall{
					{
						ID:   "tc-1",
						Name: "run_readonly_command",
						Arguments: map[string]any{
							"command_name": "ls",
							"args":         []any{"-la"},
						},
					},
				},
			},
			{Content: "Summary complete."},
		},
	}
	var lines []string
	for i := 0; i < 400; i++ {
		lines = append(lines, fmt.Sprintf("file_%03d", i))
	}
	runner := &runnerStub{
		result: tools.ToolResult{
			Stdout:   strings.Join(lines, "\n"),
			ExitCode: 0,
			Duration: 10 * time.Millisecond,
		},
	}
	opt := optimization.Default()
	opt.AskEvidenceMaxChars = 200
	engine := NewEngine(provider, prompt.NewBuilder(), parser.New(), runner, 10*time.Second, opt)
	intent := model.DefaultSemanticIntent()
	intent.RequiresGrounding = true
	intent.RequestedFields = []string{"files"}

	collector := benchmetrics.NewCollector()
	ctx := benchmetrics.WithCollector(context.Background(), collector)
	_, err := engine.runBlitzNative(ctx, model.CoreRequest{
		Mode:                    model.ModeAsk,
		Input:                   "How many files are in this folder?",
		ExecutionReadOnlyPolicy: string(workflow.ActionPermissionAllow),
		ExecutionWritePolicy:    string(workflow.ActionPermissionRequest),
		AskResponseProfile:      model.DefaultAskResponseProfile(),
	}, provider, intent, boolPtr(false))
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	snapshot := collector.Snapshot()
	if snapshot.EvidenceBytesRaw <= snapshot.EvidenceBytesUsed {
		t.Fatalf("expected used evidence to be smaller than raw evidence, raw=%d used=%d", snapshot.EvidenceBytesRaw, snapshot.EvidenceBytesUsed)
	}
	if !snapshot.EvidenceTruncated {
		t.Fatal("expected evidence_truncated=true")
	}
	if snapshot.EvidenceRowsUsed <= 0 {
		t.Fatalf("expected evidence rows used > 0, got %d", snapshot.EvidenceRowsUsed)
	}
}

func TestAskTransitionTrackerRejectsLLMRepairLLMSequenceWithoutTransition(t *testing.T) {
	tracker := newAskTransitionTracker()
	if err := tracker.llm(false); err != nil {
		t.Fatalf("unexpected first llm error: %v", err)
	}
	if err := tracker.llm(true); err != nil {
		t.Fatalf("unexpected repair llm error: %v", err)
	}
	if err := tracker.llm(false); err == nil {
		t.Fatal("expected consecutive llm invariant violation on third llm call")
	}
}
