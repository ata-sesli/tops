package ask

import (
	"context"
	"strings"
	"testing"
	"time"

	"tops/internal/llm"
	"tops/internal/model"
	"tops/internal/parser"
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

func TestSelectProbesPortLinux(t *testing.T) {
	probes := selectProbes("what process is using port 3000", "linux")
	foundSS := false
	for _, p := range probes {
		if p.name == "ss" {
			foundSS = true
		}
	}
	if !foundSS {
		t.Fatalf("expected linux probe to include ss, got %+v", probes)
	}
}

func TestSelectProbesDisk(t *testing.T) {
	probes := selectProbes("why is disk usage high", "darwin")
	foundDF := false
	for _, p := range probes {
		if p.name == "df" {
			foundDF = true
		}
	}
	if !foundDF {
		t.Fatalf("expected disk probe to include df, got %+v", probes)
	}
}

func TestRunExecutesReadOnlyWorkflowWithoutLegacyExecutionGate(t *testing.T) {
	runner := &runnerStub{
		result: tools.ToolResult{
			Stdout:   "Darwin 24.0.0 arm64",
			ExitCode: 0,
			Duration: 5 * time.Millisecond,
		},
	}
	callCount := 0
	provider := llm.MockProvider{
		CompleteFn: func(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
			callCount++
			switch callCount {
			case 1:
				return llm.CompletionResponse{
					Content: `{"workflow_plan":{"reason":"Need local OS info","steps":[{"id":"s1","intent":"Get OS info","function_name":"get_os_info","function_args":{},"expected_evidence":"OS name and version"}]}}`,
				}, nil
			case 2:
				return llm.CompletionResponse{
					Content: `{"answer":"You are running Darwin on arm64.","observations":["uname reported Darwin 24.0.0 arm64"],"inferences":[],"uncertainties":[],"assumptions":[],"notes":[]}`,
				}, nil
			default:
				t.Fatalf("unexpected provider call %d", callCount)
				return llm.CompletionResponse{}, nil
			}
		},
	}
	engine := NewEngine(provider, prompt.NewBuilder(), parser.New(), runner, 10*time.Second)

	result, err := engine.Run(context.Background(), model.CoreRequest{
		Mode:                    model.ModeAsk,
		Input:                   "What is my operating system?",
		ExecutionReadOnlyPolicy: string(workflow.ActionPermissionAllow),
		ExecutionWritePolicy:    string(workflow.ActionPermissionRequest),
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if got := result.Answer; got != "You are running Darwin on arm64." {
		t.Fatalf("unexpected answer: %q", got)
	}
	if len(result.Provenance) != 1 || !strings.Contains(result.Provenance[0].Detail, "uname -srm") {
		t.Fatalf("expected uname provenance, got %+v", result.Provenance)
	}
	if runner.calls != 1 {
		t.Fatalf("expected one tool execution, got %d", runner.calls)
	}
	if len(runner.specs) != 1 || runner.specs[0].Name != "uname" {
		t.Fatalf("expected uname tool execution, got %+v", runner.specs)
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
				Content: `{"workflow_plan":{"reason":"Need local OS info","steps":[{"id":"s1","intent":"Get OS info","function_name":"get_os_info","function_args":{},"expected_evidence":"OS name and version"}]}}`,
			}, nil
		},
	}
	engine := NewEngine(provider, prompt.NewBuilder(), parser.New(), runner, 10*time.Second)

	result, err := engine.Run(context.Background(), model.CoreRequest{
		Mode:                    model.ModeAsk,
		Input:                   "What is my operating system?",
		ExecutionReadOnlyPolicy: string(workflow.ActionPermissionRequest),
		ExecutionWritePolicy:    string(workflow.ActionPermissionRequest),
	})
	if err != nil {
		t.Fatalf("expected structured blocked result, got err: %v", err)
	}
	if !strings.Contains(result.Answer, "execution is currently blocked") {
		t.Fatalf("expected blocked answer, got %q", result.Answer)
	}
	if len(result.Uncertainties) != 1 || !strings.Contains(result.Uncertainties[0], "requires interactive approval") {
		t.Fatalf("expected approval-related uncertainty, got %+v", result.Uncertainties)
	}
	if runner.calls != 0 {
		t.Fatalf("expected no tool execution, got %d", runner.calls)
	}
}
