package gen

import (
	"context"
	"strings"
	"testing"

	"tops/internal/llm"
	"tops/internal/model"
	"tops/internal/parser"
	"tops/internal/policy"
	"tops/internal/prompt"
	"tops/internal/tools"
	"tops/internal/workflow"
)

type runnerStub struct {
	result tools.ToolResult
	err    error
	calls  int
	specs  []tools.ToolSpec
}

func (r *runnerStub) Run(ctx context.Context, spec tools.ToolSpec) (tools.ToolResult, error) {
	r.calls++
	r.specs = append(r.specs, spec)
	return r.result, r.err
}

func TestRunExecutesReadOnlyWorkflowWithoutLegacyExecutionGate(t *testing.T) {
	runner := &runnerStub{
		result: tools.ToolResult{
			Stdout:   "/Users/test/project",
			ExitCode: 0,
		},
	}
	callCount := 0
	provider := llm.MockProvider{
		CompleteFn: func(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
			callCount++
			switch callCount {
			case 1:
				return llm.CompletionResponse{
					Content: `{"workflow_plan":{"reason":"Need cwd","steps":[{"id":"s1","intent":"Get current working directory","function_name":"get_working_directory","function_args":{},"expected_evidence":"Current working directory path"}]}}`,
				}, nil
			case 2:
				return llm.CompletionResponse{
					Content: `{"command":"ls -la /Users/test/project","explanation":"Use the observed working directory.","intent_struct":{"intent":"List files","constraints":{},"action":"list"},"assumptions":[],"ambiguities":[],"confidence_notes":[]}`,
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
		Input:                   "List files in my current directory",
		ExecutionReadOnlyPolicy: string(workflow.ActionPermissionAllow),
		ExecutionWritePolicy:    string(workflow.ActionPermissionRequest),
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if result.Command != "ls -la /Users/test/project" {
		t.Fatalf("unexpected command: %q", result.Command)
	}
	if runner.calls != 1 {
		t.Fatalf("expected one tool execution, got %d", runner.calls)
	}
	if len(runner.specs) != 1 || runner.specs[0].Name != "pwd" {
		t.Fatalf("expected pwd tool execution, got %+v", runner.specs)
	}
}

func TestRunBlocksReadOnlyRequestWithoutPrompter(t *testing.T) {
	runner := &runnerStub{}
	callCount := 0
	provider := llm.MockProvider{
		CompleteFn: func(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
			callCount++
			if callCount > 1 {
				t.Fatalf("unexpected provider call %d", callCount)
			}
			return llm.CompletionResponse{
				Content: `{"workflow_plan":{"reason":"Need cwd","steps":[{"id":"s1","intent":"Get current working directory","function_name":"get_working_directory","function_args":{},"expected_evidence":"Current working directory path"}]}}`,
			}, nil
		},
	}
	engine := NewEngine(provider, prompt.NewBuilder(), parser.New(), policy.NewEngine(), runner)

	result, err := engine.Run(context.Background(), model.CoreRequest{
		Mode:                    model.ModeGen,
		Input:                   "List files in my current directory",
		ExecutionReadOnlyPolicy: string(workflow.ActionPermissionRequest),
		ExecutionWritePolicy:    string(workflow.ActionPermissionRequest),
	})
	if err != nil {
		t.Fatalf("expected structured blocked result, got err: %v", err)
	}
	if result.Command != "# workflow blocked" {
		t.Fatalf("expected blocked command marker, got %q", result.Command)
	}
	if len(result.Ambiguities) != 1 || !strings.Contains(result.Ambiguities[0], "requires interactive approval") {
		t.Fatalf("expected approval-related ambiguity, got %+v", result.Ambiguities)
	}
	if runner.calls != 0 {
		t.Fatalf("expected no tool execution, got %d", runner.calls)
	}
}
