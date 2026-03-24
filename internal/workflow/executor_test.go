package workflow

import (
	"context"
	"strings"
	"testing"
	"time"

	"tops/internal/policy"
	"tops/internal/tools"
)

type runnerStub struct {
	result tools.ToolResult
	err    error
	calls  int
}

func (r *runnerStub) Run(ctx context.Context, spec tools.ToolSpec) (tools.ToolResult, error) {
	r.calls++
	return r.result, r.err
}

type auditStub struct {
	created int
	updated int
	steps   int
}

func (a *auditStub) CreateWorkflowRun(ctx context.Context, record WorkflowRunRecord) (int64, error) {
	a.created++
	return 1, nil
}

func (a *auditStub) UpdateWorkflowRun(ctx context.Context, runID int64, status RunStatus, endedAt time.Time, errorText string) error {
	a.updated++
	return nil
}

func (a *auditStub) InsertWorkflowStep(ctx context.Context, record WorkflowStepRecord) error {
	a.steps++
	return nil
}

func TestExecutorRunsApprovedReadOnlyStep(t *testing.T) {
	runner := &runnerStub{
		result: tools.ToolResult{Stdout: "/tmp/project", ExitCode: 0, Duration: 10 * time.Millisecond},
	}
	exec := NewExecutor(runner, policy.NewEngine())
	audit := &auditStub{}

	ctx := context.Background()
	ctx = WithAuditStore(ctx, audit, nil)

	result, err := exec.Execute(ctx, "ask", "where am i", WorkflowPlan{
		Reason: "Need cwd",
		Steps: []WorkflowStep{
			{ID: "s1", CommandName: "pwd"},
		},
	})
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	if result.Status != RunStatusCompleted {
		t.Fatalf("expected completed status, got %s", result.Status)
	}
	if len(result.StepRuns) != 1 {
		t.Fatalf("expected one step run, got %d", len(result.StepRuns))
	}
	if !result.StepRuns[0].Approved {
		t.Fatal("expected approved step")
	}
	if runner.calls != 1 {
		t.Fatalf("expected one runner call, got %d", runner.calls)
	}
	if audit.created != 1 || audit.updated != 1 || audit.steps != 1 {
		t.Fatalf("unexpected audit counters: created=%d updated=%d steps=%d", audit.created, audit.updated, audit.steps)
	}
}

func TestExecutorBlocksWriteWhenDisallowedByPolicy(t *testing.T) {
	exec := NewExecutor(&runnerStub{}, policy.NewEngine())
	ctx := context.Background()
	ctx = WithExecutionPolicy(ctx, ExecutionPolicy{
		ReadOnly: ActionPermissionAllow,
		Write:    ActionPermissionDisallow,
	})

	result, err := exec.Execute(ctx, "gen", "delete tmp files", WorkflowPlan{
		Reason: "bad idea",
		Steps: []WorkflowStep{
			{ID: "s1", CommandName: "rm", Args: []string{"-rf", "/tmp/x"}},
		},
	})
	if err == nil {
		t.Fatal("expected blocking error for disallowed write step")
	}
	if result.Status != RunStatusBlocked {
		t.Fatalf("expected blocked status, got %s", result.Status)
	}
}

func TestExecutorWriteRequestDeniedByUser(t *testing.T) {
	runner := &runnerStub{}
	exec := NewExecutor(runner, policy.NewEngine())
	ctx := context.Background()
	ctx = WithExecutionPolicy(ctx, ExecutionPolicy{
		ReadOnly: ActionPermissionAllow,
		Write:    ActionPermissionRequest,
	})
	ctx = WithApprovalPrompter(ctx, StaticPrompter{Approve: false})

	result, err := exec.Execute(ctx, "gen", "delete tmp files", WorkflowPlan{
		Reason: "bad idea",
		Steps: []WorkflowStep{
			{ID: "s1", CommandName: "rm", Args: []string{"-rf", "/tmp/x"}},
		},
	})
	if err != nil {
		t.Fatalf("expected deny path without hard error, got: %v", err)
	}
	if result.Status != RunStatusDenied {
		t.Fatalf("expected denied status, got %s", result.Status)
	}
	if runner.calls != 0 {
		t.Fatalf("expected no runner call for denied step, got %d", runner.calls)
	}
}

func TestExecutorWriteRequestWithoutPrompterBlocked(t *testing.T) {
	exec := NewExecutor(&runnerStub{}, policy.NewEngine())
	ctx := context.Background()
	ctx = WithExecutionPolicy(ctx, ExecutionPolicy{
		ReadOnly: ActionPermissionAllow,
		Write:    ActionPermissionRequest,
	})

	result, err := exec.Execute(ctx, "gen", "delete tmp files", WorkflowPlan{
		Reason: "bad idea",
		Steps: []WorkflowStep{
			{ID: "s1", CommandName: "rm", Args: []string{"-rf", "/tmp/x"}},
		},
	})
	if err == nil {
		t.Fatal("expected error when request policy has no prompter")
	}
	if result.Status != RunStatusBlocked {
		t.Fatalf("expected blocked status, got %s", result.Status)
	}
}

func TestExecutorWriteAllowRunsWithoutPrompter(t *testing.T) {
	runner := &runnerStub{
		result: tools.ToolResult{Stdout: "ok", ExitCode: 0, Duration: 5 * time.Millisecond},
	}
	exec := NewExecutor(runner, policy.NewEngine())
	ctx := context.Background()
	ctx = WithExecutionPolicy(ctx, ExecutionPolicy{
		ReadOnly: ActionPermissionAllow,
		Write:    ActionPermissionAllow,
	})

	result, err := exec.Execute(ctx, "gen", "delete tmp files", WorkflowPlan{
		Reason: "test",
		Steps: []WorkflowStep{
			{ID: "s1", CommandName: "rm", Args: []string{"-rf", "/tmp/x"}},
		},
	})
	if err != nil {
		t.Fatalf("expected write allow path to execute, got err: %v", err)
	}
	if result.Status != RunStatusCompleted {
		t.Fatalf("expected completed status, got %s", result.Status)
	}
	if runner.calls != 1 {
		t.Fatalf("expected runner call, got %d", runner.calls)
	}
}

func TestExecutorTruncatesStdoutByOutputLineLimit(t *testing.T) {
	runner := &runnerStub{
		result: tools.ToolResult{
			Stdout:   strings.Join([]string{"h", "1", "2", "3", "4", "5"}, "\n"),
			ExitCode: 0,
			Duration: 5 * time.Millisecond,
		},
	}
	exec := NewExecutor(runner, policy.NewEngine())
	ctx := context.Background()

	result, err := exec.Execute(ctx, "ask", "list top procs", WorkflowPlan{
		Reason: "Need process list",
		Steps: []WorkflowStep{
			{ID: "s1", CommandName: "ps", OutputLineLimit: 3},
		},
	})
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	if result.Status != RunStatusCompleted {
		t.Fatalf("expected completed status, got %s", result.Status)
	}
	got := result.StepRuns[0].Stdout
	if !strings.Contains(got, "... (3 lines omitted)") {
		t.Fatalf("expected truncation marker, got %q", got)
	}
}
