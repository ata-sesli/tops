package workflow

import (
	"context"
	"time"
)

type RunStatus string

const (
	RunStatusCompleted RunStatus = "completed"
	RunStatusDenied    RunStatus = "denied"
	RunStatusBlocked   RunStatus = "blocked"
	RunStatusFailed    RunStatus = "failed"
)

type WorkflowStep struct {
	ID               string   `json:"id"`
	Intent           string   `json:"intent"`
	CommandName      string   `json:"command_name"`
	Args             []string `json:"args"`
	RiskLabels       []string `json:"risk_labels"`
	ExpectedEvidence string   `json:"expected_evidence"`
	OutputLineLimit  int      `json:"output_line_limit,omitempty"`
	TimeoutMS        int      `json:"timeout_ms,omitempty"`
}

type WorkflowPlan struct {
	Reason string         `json:"reason"`
	Steps  []WorkflowStep `json:"steps"`
}

type StepResult struct {
	StepID               string
	Index                int
	Command              string
	Args                 []string
	CWD                  string
	Approved             bool
	StartedAt            time.Time
	EndedAt              time.Time
	Stdout               string
	Stderr               string
	StdoutLineCountTotal int
	StdoutNonemptyCount  int
	StdoutPreviewCount   int
	StdoutLineCountExact bool
	StdoutTruncated      bool
	StderrTruncated      bool
	ExitCode             int
	Duration             time.Duration
	ErrorText            string
}

type ExecutionResult struct {
	Status    RunStatus
	StartedAt time.Time
	EndedAt   time.Time
	StepRuns  []StepResult
	ErrorText string
}

type PlanningDecision struct {
	Plan                       *WorkflowPlan
	FinalRaw                   string
	EffectiveRequiresGrounding *bool
	GroundingOverrideReason    string
}

type WorkflowFunctionCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type WorkflowPlanner interface {
	Decide(ctx context.Context, raw string) (PlanningDecision, error)
}

type WorkflowExecutor interface {
	Execute(ctx context.Context, mode string, input string, plan WorkflowPlan) (ExecutionResult, error)
}

type ApprovalPrompter interface {
	ApproveStep(ctx context.Context, step WorkflowStep) (bool, error)
}

type WorkflowRunRecord struct {
	ChatSessionID *int64
	Mode          string
	Input         string
	Status        RunStatus
	StartedAt     time.Time
	EndedAt       *time.Time
	ErrorText     string
}

type WorkflowStepRecord struct {
	RunID            int64
	StepIndex        int
	StepID           string
	Intent           string
	CommandName      string
	Args             []string
	RiskLabels       []string
	ExpectedEvidence string
	Approved         bool
	Stdout           string
	Stderr           string
	ExitCode         int
	Duration         time.Duration
	ErrorText        string
	Timestamp        time.Time
}

type AuditStore interface {
	CreateWorkflowRun(ctx context.Context, record WorkflowRunRecord) (int64, error)
	UpdateWorkflowRun(ctx context.Context, runID int64, status RunStatus, endedAt time.Time, errorText string) error
	InsertWorkflowStep(ctx context.Context, record WorkflowStepRecord) error
}
