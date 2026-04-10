package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"tops/internal/app"
	"tops/internal/chatstore"
	"tops/internal/model"
	"tops/internal/progress"
	"tops/internal/workflow"
)

type chatFocus string
type chatTab string
type topsStatus string

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
	Kind string
	Text string
}

type chatWorkflowMsg struct {
	Kind        string
	StepID      string
	CommandLine string
	ActionClass string
	Approved    bool
	Source      string
	ExitCode    int
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
}

type chatReporter struct {
	events chan tea.Msg
	mode   string
}

func newChatReporter(events chan tea.Msg, mode string) *chatReporter {
	return &chatReporter{events: events, mode: strings.ToLower(strings.TrimSpace(mode))}
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
	r.events <- chatStreamMsg{Kind: "thinking", Text: chunk}
}

func (r *chatReporter) ResponseChunk(chunk string) {
	if strings.TrimSpace(chunk) == "" || r.mode == "release" {
		return
	}
	r.events <- chatStreamMsg{Kind: "answering", Text: chunk}
}

func (r *chatReporter) ActionStarted(stepID string, commandLine string, actionClass string) {
	r.events <- chatWorkflowMsg{Kind: "action_started", StepID: stepID, CommandLine: commandLine, ActionClass: actionClass}
}

func (r *chatReporter) PermissionRequested(stepID string, commandLine string, actionClass string) {
	r.events <- chatWorkflowMsg{Kind: "permission_requested", StepID: stepID, CommandLine: commandLine, ActionClass: actionClass}
}

func (r *chatReporter) PermissionDecision(stepID string, commandLine string, actionClass string, approved bool, source string) {
	r.events <- chatWorkflowMsg{Kind: "permission_decision", StepID: stepID, CommandLine: commandLine, ActionClass: actionClass, Approved: approved, Source: source}
}

func (r *chatReporter) ActionCompleted(stepID string, commandLine string, actionClass string, exitCode int, errText string) {
	r.events <- chatWorkflowMsg{Kind: "action_completed", StepID: stepID, CommandLine: commandLine, ActionClass: actionClass, ExitCode: exitCode, ErrText: errText}
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
		runCtx := progress.WithReporter(ctx, newChatReporter(events, string(rt.Config.Execution.TraceMode)))
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

func parseChatDraft(raw string) (model.Mode, string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", "", fmt.Errorf("chat input cannot be empty")
	}
	switch {
	case strings.HasPrefix(trimmed, "ask "):
		return model.ModeAsk, strings.TrimSpace(strings.TrimPrefix(trimmed, "ask ")), nil
	case strings.HasPrefix(trimmed, "gen "):
		return model.ModeGen, strings.TrimSpace(strings.TrimPrefix(trimmed, "gen ")), nil
	default:
		return "", "", fmt.Errorf("chat messages must start with \"ask \" or \"gen \"")
	}
}

func nextDraftForMode(mode model.Mode) string {
	switch mode {
	case model.ModeGen:
		return "gen "
	default:
		return "ask "
	}
}
