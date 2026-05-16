package tui

import (
	"context"
	"strings"
	"time"

	tea "github.com/phoenix-tui/phoenix/tea"

	"tops/internal/app"
	"tops/internal/model"
	"tops/internal/runtime/progress"
	"tops/internal/runtime/workflow"
	"tops/internal/storage/chatstore"
)

type chatFocus string
type chatTab string
type topsStatus string
type topsInputMode string

const (
	tabConfig chatTab = "config"
	tabChats  chatTab = "chats"
)

const (
	chatFocusShell    chatFocus = "shell"
	chatFocusTOPS     chatFocus = "tops"
	chatFocusApproval chatFocus = "approval"
)

const (
	topsStatusIdle            topsStatus = "idle"
	topsStatusThinking        topsStatus = "thinking"
	topsStatusWaitingApproval topsStatus = "waiting approval"
)

const (
	topsInputModeUnset topsInputMode = ""
	topsInputModeAsk   topsInputMode = "ASK"
	topsInputModeGen   topsInputMode = "GEN"
	topsInputModeHelp  topsInputMode = "HELP"
	topsInputModeBye   topsInputMode = "BYE"
)

type chatApprovalRequest struct {
	Step        workflow.WorkflowStep
	CommandLine string
	ActionClass string
	Response    chan bool
}

type chatProgressMsg struct {
	Phase   string
	Elapsed time.Duration
	Done    bool
	Err     error
}

type chatStreamMsg struct {
	SessionID int64
	Kind      string
	Text      string
}

type chatWorkflowMsg struct {
	SessionID   int64
	Kind        string
	StepID      string
	CommandLine string
	ActionClass string
	Approved    bool
	Source      string
	ExitCode    int
	Duration    time.Duration
	ErrText     string
}

type chatApprovalRequestMsg struct {
	Request chatApprovalRequest
}

type chatTurnDoneMsg struct {
	SessionID int64
	Mode      model.Mode
	Input     string
	Output    string
	Err       error
}

type chatShellOutputMsg struct {
	SessionID int64
	Text      string
	Err       error
}

type chatSessionState struct {
	ID             int64
	Title          string
	Live           bool
	Transcript     []chatstore.PersistedMessage
	StickyMode     model.Mode
	SelectedMode   topsInputMode
	Draft          string
	ShellDraft     string
	ShellHistory   []string
	ShellHistPos   int
	ShellEchoQueue []string
	ShellPTYBuffer string
	TopsStatus     topsStatus
	Focus          chatFocus
	Waiting        bool
	Approval       *chatApprovalRequest
	ShellPaused    bool
	TitleAssigned  bool
	TurnStartedAt  time.Time
	TurnPausedAt   time.Time
	TurnPausedFor  time.Duration
	TurnHadAnswer  bool
}

type chatReporter struct {
	events    chan tea.Msg
	mode      string
	sessionID int64
}

func newChatReporter(events chan tea.Msg, mode string, sessionID int64) *chatReporter {
	return &chatReporter{events: events, mode: strings.ToLower(strings.TrimSpace(mode)), sessionID: sessionID}
}

func (r *chatReporter) Start(phase string) {
	r.events <- chatProgressMsg{Phase: strings.TrimSpace(phase)}
}

func (r *chatReporter) Update(phase string) {
	r.events <- chatProgressMsg{Phase: strings.TrimSpace(phase)}
}

func (r *chatReporter) Finish(err error) {
	r.events <- chatProgressMsg{Done: true, Err: err}
}

func (r *chatReporter) ThinkingChunk(chunk string) {
	if strings.TrimSpace(chunk) == "" || r.mode == "release" {
		return
	}
	r.events <- chatStreamMsg{SessionID: r.sessionID, Kind: "thinking", Text: chunk}
}

func (r *chatReporter) ResponseChunk(chunk string) {
	if strings.TrimSpace(chunk) == "" {
		return
	}
	r.events <- chatStreamMsg{SessionID: r.sessionID, Kind: "answering", Text: chunk}
}

func (r *chatReporter) StatusLine(line string) {
	if strings.TrimSpace(line) == "" {
		return
	}
	r.events <- chatStreamMsg{SessionID: r.sessionID, Kind: "status", Text: line}
}

func (r *chatReporter) ActionStarted(stepID string, commandLine string, actionClass string) {
	r.events <- chatWorkflowMsg{SessionID: r.sessionID, Kind: "action_started", StepID: stepID, CommandLine: commandLine, ActionClass: actionClass}
}

func (r *chatReporter) PermissionRequested(stepID string, commandLine string, actionClass string) {
	r.events <- chatWorkflowMsg{SessionID: r.sessionID, Kind: "permission_requested", StepID: stepID, CommandLine: commandLine, ActionClass: actionClass}
}

func (r *chatReporter) PermissionDecision(stepID string, commandLine string, actionClass string, approved bool, source string) {
	r.events <- chatWorkflowMsg{SessionID: r.sessionID, Kind: "permission_decision", StepID: stepID, CommandLine: commandLine, ActionClass: actionClass, Approved: approved, Source: source}
}

func (r *chatReporter) ActionCompleted(stepID string, commandLine string, actionClass string, exitCode int, duration time.Duration, errText string) {
	r.events <- chatWorkflowMsg{SessionID: r.sessionID, Kind: "action_completed", StepID: stepID, CommandLine: commandLine, ActionClass: actionClass, ExitCode: exitCode, Duration: duration, ErrText: errText}
}

type chatApprovalPrompter struct {
	events chan tea.Msg
}

func (p *chatApprovalPrompter) ApproveStep(ctx context.Context, step workflow.WorkflowStep) (bool, error) {
	actionClass := workflow.ClassifyActionClass(step.RiskLabels)
	commandLine := strings.TrimSpace(step.CommandName + " " + strings.Join(step.Args, " "))
	request := chatApprovalRequest{
		Step:        step,
		CommandLine: commandLine,
		ActionClass: string(actionClass),
		Response:    make(chan bool, 1),
	}
	select {
	case p.events <- chatApprovalRequestMsg{Request: request}:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	select {
	case approved := <-request.Response:
		return approved, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

func runChatTurnCmd(ctx context.Context, events chan tea.Msg, session *Session, rt app.Runtime, sessionID int64, mode model.Mode, input string) tea.Cmd {
	return func() tea.Msg {
		runCtx := progress.WithReporter(ctx, newChatReporter(events, string(rt.Config.Execution.TraceMode), sessionID))
		runCtx = workflow.WithApprovalPrompter(runCtx, &chatApprovalPrompter{events: events})
		if session.store != nil && sessionID > 0 {
			runCtx = workflow.WithAuditStore(runCtx, session.store, &sessionID)
		}
		output, err := session.exec(runCtx, rt, mode, input)
		return chatTurnDoneMsg{
			SessionID: sessionID,
			Mode:      mode,
			Input:     input,
			Output:    output,
			Err:       err,
		}
	}
}

func nextDraftForMode(mode model.Mode) string {
	return ""
}
