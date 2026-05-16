package gen

import (
	"context"
	"strings"
	"testing"

	"tops/internal/model"
	"tops/internal/parser"
	"tops/internal/runtime/llm"
	"tops/internal/runtime/policy"
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

func TestRunNonGroundedLegacyPath(t *testing.T) {
	runner := &runnerStub{}
	callCount := 0
	requests := make([]llm.CompletionRequest, 0, 2)
	provider := llm.MockProvider{
		CompleteFn: func(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
			callCount++
			requests = append(requests, req)
			switch callCount {
			case 1:
				return llm.CompletionResponse{
					Content: `{"version":"v1","goal":"list files","output_kind":"single_command","target_shell":"zsh","platform_scope":"current_platform","requires_grounding":false,"requested_constraints":[],"safety_notes":[],"ambiguity_notes":[],"needs_current_environment_context":false}`,
				}, nil
			case 2:
				return llm.CompletionResponse{
					Content: `{"command":"ls -la","explanation":"List files in the current directory.","intent_struct":{"intent":"List files","constraints":{},"action":"list"},"output_kind":"single_command","target_shell":"zsh","assumptions":[],"ambiguities":[],"confidence_notes":[]}`,
				}, nil
			default:
				t.Fatalf("unexpected provider call %d", callCount)
				return llm.CompletionResponse{}, nil
			}
		},
	}
	engine := NewEngine(provider, prompt.NewBuilder(), parser.New(), policy.NewEngine(), runner)

	result, err := engine.Run(context.Background(), model.CoreRequest{
		Mode:                    model.ModeGen,
		Input:                   "Generate a command to list files in current dir",
		ExecutionReadOnlyPolicy: string(workflow.ActionPermissionAllow),
		ExecutionWritePolicy:    string(workflow.ActionPermissionRequest),
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if result.Command != "ls -la" {
		t.Fatalf("unexpected command: %q", result.Command)
	}
	if callCount != 2 {
		t.Fatalf("expected 2 provider calls (intent + final), got %d", callCount)
	}
	if len(requests) != 2 {
		t.Fatalf("expected 2 captured requests, got %d", len(requests))
	}
	if requests[0].SamplingProfile != llm.SamplingProfilePlanner {
		t.Fatalf("expected intent normalizer planner profile, got %q", requests[0].SamplingProfile)
	}
	if requests[0].MaxTokens != 256 {
		t.Fatalf("expected intent normalizer max tokens=256, got %d", requests[0].MaxTokens)
	}
	if requests[1].SamplingProfile != llm.SamplingProfileGen {
		t.Fatalf("expected final generation gen profile, got %q", requests[1].SamplingProfile)
	}
	if requests[1].MaxTokens != 512 {
		t.Fatalf("expected final generation max tokens=512, got %d", requests[1].MaxTokens)
	}
	if runner.calls != 0 {
		t.Fatalf("expected no tool executions for non-grounded run, got %d", runner.calls)
	}
}

func TestRunGroundedLegacyPathBlockedWithoutPrompter(t *testing.T) {
	runner := &runnerStub{}
	callCount := 0
	requests := make([]llm.CompletionRequest, 0, 2)
	provider := llm.MockProvider{
		CompleteFn: func(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
			callCount++
			requests = append(requests, req)
			switch callCount {
			case 1:
				return llm.CompletionResponse{
					Content: `{"version":"v1","goal":"list files","output_kind":"single_command","target_shell":"zsh","platform_scope":"current_platform","requires_grounding":true,"requested_constraints":[],"safety_notes":[],"ambiguity_notes":[],"needs_current_environment_context":true}`,
				}, nil
			case 2:
				return llm.CompletionResponse{
					Content: `{"workflow_plan":{"reason":"Need cwd","steps":[{"id":"s1","intent":"Get cwd","function_name":"run_readonly_command","function_args":{"command_name":"pwd","args":[]}}]}}`,
				}, nil
			default:
				t.Fatalf("unexpected provider call %d", callCount)
				return llm.CompletionResponse{}, nil
			}
		},
	}
	engine := NewEngine(provider, prompt.NewBuilder(), parser.New(), policy.NewEngine(), runner)

	result, err := engine.Run(context.Background(), model.CoreRequest{
		Mode:                    model.ModeGen,
		Input:                   "Generate a command for the current directory files",
		ExecutionReadOnlyPolicy: string(workflow.ActionPermissionRequest),
		ExecutionWritePolicy:    string(workflow.ActionPermissionRequest),
	})
	if err != nil {
		t.Fatalf("expected structured blocked result, got err: %v", err)
	}
	if result.Command != "# workflow blocked" {
		t.Fatalf("expected blocked marker command, got %q", result.Command)
	}
	if len(result.Ambiguities) == 0 || !strings.Contains(result.Ambiguities[0], "requires interactive approval") {
		t.Fatalf("expected approval ambiguity, got %+v", result.Ambiguities)
	}
	if callCount != 2 {
		t.Fatalf("expected 2 provider calls (intent + planner), got %d", callCount)
	}
	if len(requests) != 2 {
		t.Fatalf("expected 2 captured requests, got %d", len(requests))
	}
	if requests[0].SamplingProfile != llm.SamplingProfilePlanner {
		t.Fatalf("expected intent normalizer planner profile, got %q", requests[0].SamplingProfile)
	}
	if requests[0].MaxTokens != 256 {
		t.Fatalf("expected intent normalizer max tokens=256, got %d", requests[0].MaxTokens)
	}
	if requests[1].SamplingProfile != llm.SamplingProfilePlanner {
		t.Fatalf("expected grounded planner profile, got %q", requests[1].SamplingProfile)
	}
	if requests[1].MaxTokens != 256 {
		t.Fatalf("expected grounded planner max tokens=256, got %d", requests[1].MaxTokens)
	}
	if runner.calls != 0 {
		t.Fatalf("expected no tool execution when blocked, got %d", runner.calls)
	}
}

func TestRunBoundaryGuidanceForAskLikeInput(t *testing.T) {
	runner := &runnerStub{}
	provider := llm.MockProvider{
		CompleteFn: func(_ context.Context, _ llm.CompletionRequest) (llm.CompletionResponse, error) {
			t.Fatal("provider should not be called for boundary guidance")
			return llm.CompletionResponse{}, nil
		},
	}
	engine := NewEngine(provider, prompt.NewBuilder(), parser.New(), policy.NewEngine(), runner)

	result, err := engine.Run(context.Background(), model.CoreRequest{
		Mode:  model.ModeGen,
		Input: "What is my operating system?",
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if !strings.Contains(strings.ToLower(result.Command), "use ask") {
		t.Fatalf("expected ask boundary guidance, got %q", result.Command)
	}
}
