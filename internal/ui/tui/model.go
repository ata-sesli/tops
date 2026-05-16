package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/phoenix-tui/phoenix/tea"
	"golang.org/x/term"

	"tops/internal/ui/tui/render"
	"tops/internal/ui/tui/ui"

	"tops/internal/app"
	"tops/internal/config"
	"tops/internal/runtime/localruntime"
	"tops/internal/storage/chatstore"
	"tops/internal/storage/modelprofile"
)

type uiMode string

const (
	uiModeManager uiMode = "manager"
	uiModeSetup   uiMode = "setup"
)

type setupSaveMsg struct {
	Runtime app.Runtime
	Err     error
}

type setupModelDiscoveryMsg struct {
	Models []string
	Err    error
}

type localStatusState struct {
	Applicable bool
	Available  bool
	Detail     string
}

type localStatusMsg struct {
	State localStatusState
}

type localStatusTickMsg struct{}
type pendingTickMsg struct{}

type managerCommandResultMsg struct {
	Parsed         ParseResult
	Output         string
	Err            error
	UpdatedRuntime *app.Runtime
	RefreshStatus  bool
}

const localStatusRefreshInterval = 5 * time.Second
const pendingRefreshInterval = 200 * time.Millisecond

type sessionModel struct {
	ctx     context.Context
	session *Session
	runtime *app.Runtime

	mode      uiMode
	activeTab chatTab

	width  int
	height int

	input ui.InputField

	outputViewport ui.ScrollViewport
	outputContent  string
	configViewport ui.ScrollViewport
	chatViewport   ui.ScrollViewport
	chatOverlayVP  ui.ScrollViewport
	copyOverlayVP  ui.ScrollViewport

	setup        SetupWizardState
	localStatus  localStatusState
	pending      bool
	pendingCount int
	pendingPhase string
	pendingSince time.Time
	pendingQuit  bool

	events            chan tea.Msg
	shell             ShellController
	shellFactory      func() ShellController
	chatSessions      []chatstore.PersistedSession
	selectedChatIndex int
	selectedChatID    int64
	liveChatID        int64
	chatOverlayOpen   bool
	copyOverlayOpen   bool
	copyEntries       []chatCopyEntry
	copySelectedIndex int
	copySelectedRows  map[int]struct{}
	chatState         map[int64]*chatSessionState
	configMenu        configMenuState
	configInputActive bool
	mouseCapture      bool
	escSeqState       int
	copyToClipboard   func(string) error
}

func newSessionModel(ctx context.Context, session *Session, rt *app.Runtime, _ ...any) sessionModel {
	input := ui.NewInputField()
	input.Prompt = "tps> "
	input.Placeholder = guidanceMessage()
	input.Focus()
	input.CharLimit = 0
	input.Width = 80

	outputVP := ui.NewScrollViewport(1, 1)
	configVP := ui.NewScrollViewport(1, 1)
	chatVP := ui.NewScrollViewport(1, 1)
	overlayVP := ui.NewScrollViewport(1, 1)
	copyOverlayVP := ui.NewScrollViewport(1, 1)

	m := sessionModel{
		ctx:              ctx,
		session:          session,
		runtime:          rt,
		mode:             uiModeManager,
		activeTab:        tabConfig,
		input:            input,
		outputViewport:   outputVP,
		configViewport:   configVP,
		chatViewport:     chatVP,
		chatOverlayVP:    overlayVP,
		copyOverlayVP:    copyOverlayVP,
		localStatus:      deriveLocalStatus(rt, localruntime.StatusResult{}, nil),
		events:           make(chan tea.Msg, 256),
		shellFactory:     func() ShellController { return NewPTYShellController() },
		chatState:        map[int64]*chatSessionState{},
		copySelectedRows: map[int]struct{}{},
		copyToClipboard: func(text string) error {
			return copyTextToClipboard(text)
		},
	}
	m.width, m.height = detectTerminalSize()
	m.applyLayout()
	m.appendBanner()
	m.syncInputForActiveSurface()
	return m
}

func (m *sessionModel) Init() tea.Cmd {
	return tea.Batch(
		initialWindowSizeCmd(),
		localStatusTickCmd(),
		checkLocalStatusCmd(m.ctx, m.runtime),
		waitForChatEventCmd(m.events),
		refreshChatSessionsCmd(m.ctx, m.session.store),
	)
}

func (m *sessionModel) Update(msg tea.Msg) (*sessionModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.applyLayout()
		return m, nil
	case localStatusMsg:
		m.localStatus = msg.State
		return m, nil
	case localStatusTickMsg:
		return m, tea.Batch(
			localStatusTickCmd(),
			checkLocalStatusCmd(m.ctx, m.runtime),
		)
	case setupModelDiscoveryMsg:
		m.stopPending()
		if msg.Err != nil {
			m.setup.InfoMessage = fmt.Sprintf("local runtime model discovery failed: %s", msg.Err)
		} else {
			m.setup.AvailableModels = msg.Models
			if len(msg.Models) > 0 {
				m.setup.InfoMessage = fmt.Sprintf("Discovered %d local runtime models. Enter an index or model name.", len(msg.Models))
			} else {
				m.setup.InfoMessage = "No local runtime models discovered. You can still type any model name."
			}
		}
		return m, nil
	case setupSaveMsg:
		m.stopPending()
		if msg.Err != nil {
			m.setup.ErrorMessage = msg.Err.Error()
			m.setup.InfoMessage = "Fix setup values and press Enter to continue."
			return m, nil
		}
		m.runtime = &msg.Runtime
		m.mode = uiModeManager
		m.syncInputForActiveSurface()
		m.appendOutputBlock("Setup completed. Runtime reloaded.")
		return m, checkLocalStatusCmd(m.ctx, m.runtime)
	case chatSessionsLoadedMsg:
		preserveConfigInput := m.activeTab == tabConfig && (m.configInputActive || m.configMenu.Edit != nil)
		m.chatSessions = msg.Sessions
		m.syncSelectedChat()
		m.refreshChatOverlay()
		if m.selectedChatID != 0 {
			_ = m.loadTranscriptForSelectedChat()
		}
		if !preserveConfigInput {
			m.syncInputForActiveSurface()
		}
		if m.activeTab == tabChats {
			if waitCmd, ok := m.ensureShellForChat(m.currentChatState()); ok && waitCmd != nil {
				return m, waitCmd
			}
		}
		return m, nil
	case chatShellOutputMsg:
		m.handleChatShellOutput(msg)
		if m.shell != nil && msg.SessionID == m.liveChatID {
			return m, waitForShellOutputCmd(m.shell, msg.SessionID)
		}
		return m, nil
	case chatProgressMsg:
		m.handleChatProgress(msg)
		return m, waitForChatEventCmd(m.events)
	case chatStreamMsg:
		m.handleChatStream(msg)
		return m, waitForChatEventCmd(m.events)
	case chatWorkflowMsg:
		m.handleChatWorkflow(msg)
		return m, waitForChatEventCmd(m.events)
	case chatApprovalRequestMsg:
		m.handleChatApprovalRequest(msg)
		return m, waitForChatEventCmd(m.events)
	case chatTurnDoneMsg:
		m.handleChatTurnDone(msg)
		return m, waitForChatEventCmd(m.events)
	case managerCommandResultMsg:
		m.stopPending()
		if msg.UpdatedRuntime != nil {
			m.runtime = msg.UpdatedRuntime
		}
		m.refreshConfigMenu()
		if msg.Err != nil {
			m.renderCommandResult(msg.Parsed, false, msg.Err.Error(), msg.Err.Error())
		} else {
			m.renderCommandResult(msg.Parsed, true, msg.Output, "")
		}
		if msg.RefreshStatus {
			if m.pendingQuit && !m.pending {
				m.pendingQuit = false
				return m, tea.Quit()
			}
			return m, checkLocalStatusCmd(m.ctx, m.runtime)
		}
		if m.pendingQuit && !m.pending {
			m.pendingQuit = false
			return m, tea.Quit()
		}
		return m, nil
	case pendingTickMsg:
		if !m.pending {
			return m, nil
		}
		return m, pendingTickCmd()
	case tea.KeyMsg:
		normalized, consumed := m.normalizeKeySequence(msg)
		if consumed {
			return m, nil
		}
		msg = normalized
		if m.mode == uiModeSetup {
			return m.handleSetupKey(msg)
		}
		if isCtrlRune(msg, 'y') {
			return m, m.toggleMouseCaptureCmd()
		}
		if isShiftTab(msg) {
			m.toggleTab()
			if m.activeTab == tabChats {
				return m, refreshChatSessionsCmd(m.ctx, m.session.store)
			}
			return m, nil
		}
		if m.activeTab == tabChats {
			return m.handleChatKey(msg)
		}
		return m.handleManagerKey(msg)
	case tea.MouseMsg:
		if m.mode == uiModeSetup {
			return m, nil
		}
		if m.activeTab == tabChats {
			return m.handleChatMouse(msg)
		}
		return m.handleConfigMouse(msg)
	}
	return m, nil
}

func (m *sessionModel) normalizeKeySequence(msg tea.KeyMsg) (tea.KeyMsg, bool) {
	if isShiftTab(msg) {
		m.escSeqState = 0
		return msg, false
	}
	if msg.Type == tea.KeyEsc {
		m.escSeqState = 1
		return msg, false
	}

	switch m.escSeqState {
	case 1:
		if r, ok := keyRune(msg); ok && r == '[' {
			m.escSeqState = 2
			return tea.KeyMsg{}, true
		}
		m.escSeqState = 0
	case 2:
		m.escSeqState = 0
		if r, ok := keyRune(msg); ok && (r == 'Z' || r == 'z') {
			return tea.KeyMsg{Type: tea.KeyTab, Shift: true}, false
		}
	}

	return msg, false
}

func (m *sessionModel) handleConfigMouse(msg tea.MouseMsg) (*sessionModel, tea.Cmd) {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.outputViewport.LineUp(3)
	case tea.MouseButtonWheelDown:
		m.outputViewport.LineDown(3)
	}
	return m, nil
}

func (m *sessionModel) toggleMouseCaptureCmd() tea.Cmd {
	m.mouseCapture = !m.mouseCapture
	if m.mouseCapture {
		m.appendOutputBlock("Mouse capture enabled. Wheel scrolling is active; text selection may be limited by terminal.")
		return nil
	}
	m.appendOutputBlock("Mouse capture disabled. Text selection is available in the terminal.")
	return nil
}

func (m sessionModel) View() string {
	var out string
	if m.mode == uiModeSetup {
		out = m.renderSetupView()
	} else {
		out = m.renderMainLayout()
	}
	return normalizeRenderedView(out, m.width, m.height)
}

func (m *sessionModel) handleManagerKey(msg tea.KeyMsg) (*sessionModel, tea.Cmd) {
	if m.activeTab != tabConfig {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}

	if m.configMenu.Edit != nil {
		if isQuitKey(msg) {
			return m, tea.Quit()
		}
		switch msg.Type {
		case tea.KeyEsc:
			m.cancelConfigMenuEdit()
			return m, nil
		case tea.KeyEnter:
			return m.applyConfigMenuEdit()
		default:
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			if m.configMenu.Edit != nil {
				m.configMenu.Edit.Value = m.input.Value()
			}
			return m, cmd
		}
	}

	if !m.configInputActive {
		if isQuitKey(msg) {
			return m, tea.Quit()
		}
		switch msg.Type {
		case tea.KeyPgUp:
			m.outputViewport.HalfViewUp()
			return m, nil
		case tea.KeyPgDown:
			m.outputViewport.HalfViewDown()
			return m, nil
		case tea.KeyHome:
			m.outputViewport.GotoTop()
			return m, nil
		case tea.KeyEnd:
			m.outputViewport.GotoBottom()
			return m, nil
		case tea.KeyUp:
			m.moveConfigMenu(-1)
			return m, nil
		case tea.KeyDown:
			m.moveConfigMenu(1)
			return m, nil
		case tea.KeySpace:
			return m.applyConfigMenuCurrent(true)
		default:
			if r, ok := keyRune(msg); ok {
				switch strings.ToLower(string(r)) {
				case " ":
					return m.applyConfigMenuCurrent(true)
				case "/":
					m.configInputActive = true
					m.configureInputForManager()
					m.input.SetValue("/")
					return m, nil
				}
			}
		case tea.KeyEnter:
			return m.applyConfigMenuCurrent(false)
		case tea.KeyEsc:
			return m, nil
		}
		return m, nil
	}

	if isQuitKey(msg) {
		return m, tea.Quit()
	}
	switch msg.Type {
	case tea.KeyUp:
		return m, nil
	case tea.KeyDown:
		return m, nil
	case tea.KeyEnter:
		line := strings.TrimSpace(m.input.Value())
		m.input.SetValue("")
		m.configInputActive = false
		m.configureInputForManager()
		if line == "" || line == "/" {
			return m, nil
		}
		return m.processManagerLine(line)
	case tea.KeyEsc:
		m.configInputActive = false
		m.input.SetValue("")
		m.configureInputForManager()
		return m, nil
	default:
		if isEnterLike(msg) {
			line := strings.TrimSpace(m.input.Value())
			m.input.SetValue("")
			m.configInputActive = false
			m.configureInputForManager()
			if line == "" || line == "/" {
				return m, nil
			}
			return m.processManagerLine(line)
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
}

func (m *sessionModel) processManagerLine(line string) (*sessionModel, tea.Cmd) {
	parsed := ParseInput(line)
	defer m.refreshConfigMenu()

	switch parsed.Kind {
	case KindGuidance:
		m.renderCommandResult(parsed, false, parsed.Message, parsed.Message)
		return m, nil
	case KindInvalid:
		output := "Input error: " + parsed.Message
		m.renderCommandResult(parsed, false, output, output)
		return m, nil
	case KindSetup:
		m.renderCommandResult(parsed, true, "Opening setup wizard.", "")
		m.enterSetupMode("Setup wizard opened from /setup.")
		return m, nil
	case KindMode:
		output := "TUI manager mode does not run /help, /gen, or /ask. Use CLI commands like: tps help \"...\", tps gen \"...\", tps ask \"...\""
		m.renderCommandResult(parsed, false, output, output)
		return m, nil
	case KindModels:
		if m.runtime == nil {
			output := "configuration error: TOPS runtime is not configured. Use /setup to create or repair configuration."
			m.renderCommandResult(parsed, false, output, output)
			return m, nil
		}
		m.startPending("provider")
		return m, tea.Batch(pendingTickCmd(), listModelsCmd(m.ctx, *m.runtime, parsed))
	case KindModelUse:
		if m.runtime == nil {
			output := "configuration error: TOPS runtime is not configured. Use /setup to create or repair configuration."
			m.renderCommandResult(parsed, false, output, output)
			return m, nil
		}
		m.startPending("provider")
		return m, tea.Batch(
			pendingTickCmd(),
			switchModelCmd(m.ctx, *m.runtime, m.session.configPath, m.session.runtimeLoader, parsed),
		)
	case KindModelConfigShow:
		if m.runtime == nil {
			output := "configuration error: TOPS runtime is not configured. Use /setup to create or repair configuration."
			m.renderCommandResult(parsed, false, output, output)
			return m, nil
		}
		output, _, err := showModelConfig(*m.runtime)
		if err != nil {
			m.renderCommandResult(parsed, false, err.Error(), err.Error())
			return m, nil
		}
		m.renderCommandResult(parsed, true, output, "")
		return m, nil
	case KindModelConfigSet:
		if m.runtime == nil {
			output := "configuration error: TOPS runtime is not configured. Use /setup to create or repair configuration."
			m.renderCommandResult(parsed, false, output, output)
			return m, nil
		}
		output, _, updated, err := setModelConfig(*m.runtime, m.session.configPath, m.session.runtimeLoader, parsed.ConfigField, parsed.Payload)
		if err != nil {
			m.renderCommandResult(parsed, false, err.Error(), err.Error())
			return m, nil
		}
		if updated != nil {
			m.runtime = updated
		}
		m.renderCommandResult(parsed, true, output, "")
		return m, checkLocalStatusCmd(m.ctx, m.runtime)
	case KindModelConfigReset:
		if m.runtime == nil {
			output := "configuration error: TOPS runtime is not configured. Use /setup to create or repair configuration."
			m.renderCommandResult(parsed, false, output, output)
			return m, nil
		}
		output, _, updated, err := resetModelConfig(*m.runtime, m.session.configPath, m.session.runtimeLoader)
		if err != nil {
			m.renderCommandResult(parsed, false, err.Error(), err.Error())
			return m, nil
		}
		if updated != nil {
			m.runtime = updated
		}
		m.renderCommandResult(parsed, true, output, "")
		return m, checkLocalStatusCmd(m.ctx, m.runtime)
	case KindModelResponseShow:
		if m.runtime == nil {
			output := "configuration error: TOPS runtime is not configured. Use /setup to create or repair configuration."
			m.renderCommandResult(parsed, false, output, output)
			return m, nil
		}
		output, _, err := showModelResponseProfile(*m.runtime)
		if err != nil {
			m.renderCommandResult(parsed, false, err.Error(), err.Error())
			return m, nil
		}
		m.renderCommandResult(parsed, true, output, "")
		return m, nil
	case KindModelResponseSet:
		if m.runtime == nil {
			output := "configuration error: TOPS runtime is not configured. Use /setup to create or repair configuration."
			m.renderCommandResult(parsed, false, output, output)
			return m, nil
		}
		output, _, updated, err := setModelResponseProfile(*m.runtime, m.session.configPath, m.session.runtimeLoader, parsed.ConfigField, parsed.Payload)
		if err != nil {
			m.renderCommandResult(parsed, false, err.Error(), err.Error())
			return m, nil
		}
		if updated != nil {
			m.runtime = updated
		}
		m.renderCommandResult(parsed, true, output, "")
		return m, nil
	case KindExecutionPolicyShow:
		if m.runtime == nil {
			output := "configuration error: TOPS runtime is not configured. Use /setup to create or repair configuration."
			m.renderCommandResult(parsed, false, output, output)
			return m, nil
		}
		output, _, err := showExecutionPolicy(*m.runtime)
		if err != nil {
			m.renderCommandResult(parsed, false, err.Error(), err.Error())
			return m, nil
		}
		m.renderCommandResult(parsed, true, output, "")
		return m, nil
	case KindExecutionPolicySet:
		if m.runtime == nil {
			output := "configuration error: TOPS runtime is not configured. Use /setup to create or repair configuration."
			m.renderCommandResult(parsed, false, output, output)
			return m, nil
		}
		output, _, updated, err := setExecutionPolicy(*m.runtime, m.session.configPath, m.session.runtimeLoader, parsed.ConfigField, parsed.Payload)
		if err != nil {
			m.renderCommandResult(parsed, false, err.Error(), err.Error())
			return m, nil
		}
		if updated != nil {
			m.runtime = updated
		}
		m.renderCommandResult(parsed, true, output, "")
		return m, nil
	case KindExecutionTraceShow:
		if m.runtime == nil {
			output := "configuration error: TOPS runtime is not configured. Use /setup to create or repair configuration."
			m.renderCommandResult(parsed, false, output, output)
			return m, nil
		}
		output, _, err := showExecutionTrace(*m.runtime)
		if err != nil {
			m.renderCommandResult(parsed, false, err.Error(), err.Error())
			return m, nil
		}
		m.renderCommandResult(parsed, true, output, "")
		return m, nil
	case KindExecutionTraceSet:
		if m.runtime == nil {
			output := "configuration error: TOPS runtime is not configured. Use /setup to create or repair configuration."
			m.renderCommandResult(parsed, false, output, output)
			return m, nil
		}
		output, _, updated, err := setExecutionTrace(*m.runtime, m.session.configPath, m.session.runtimeLoader, parsed.Payload)
		if err != nil {
			m.renderCommandResult(parsed, false, err.Error(), err.Error())
			return m, nil
		}
		if updated != nil {
			m.runtime = updated
		}
		m.renderCommandResult(parsed, true, output, "")
		return m, nil
	case KindHistory:
		m.renderCommandResult(parsed, true, renderHistory(m.session.history), "")
		return m, nil
	case KindHistoryDB:
		if m.session.store == nil {
			output := "Persistent chat storage unavailable."
			m.renderCommandResult(parsed, false, output, output)
			return m, nil
		}
		messages, err := m.session.store.ListRecentMessages(m.ctx, parsed.Limit)
		if err != nil {
			output := fmt.Sprintf("Failed to load persisted history: %s", err)
			m.renderCommandResult(parsed, false, output, output)
			return m, nil
		}
		m.renderCommandResult(parsed, true, renderPersistedMessages(messages), "")
		return m, nil
	case KindSessions:
		if m.session.store == nil {
			output := "Persistent chat storage unavailable."
			m.renderCommandResult(parsed, false, output, output)
			return m, nil
		}
		sessions, err := m.session.store.ListSessions(m.ctx, parsed.Limit)
		if err != nil {
			output := fmt.Sprintf("Failed to load persisted sessions: %s", err)
			m.renderCommandResult(parsed, false, output, output)
			return m, nil
		}
		m.renderCommandResult(parsed, true, renderPersistedSessions(sessions), "")
		return m, nil
	case KindSessionRead:
		if m.session.store == nil {
			output := "Persistent chat storage unavailable."
			m.renderCommandResult(parsed, false, output, output)
			return m, nil
		}
		messages, err := m.session.store.ListMessagesBySession(m.ctx, parsed.SessionID, parsed.Limit)
		if err != nil {
			output := fmt.Sprintf("Failed to load session %d: %s", parsed.SessionID, err)
			m.renderCommandResult(parsed, false, output, output)
			return m, nil
		}
		m.renderCommandResult(parsed, true, renderPersistedSessionMessages(parsed.SessionID, messages), "")
		return m, nil
	case KindSessionDelete:
		if m.session.store == nil {
			output := "Persistent chat storage unavailable."
			m.renderCommandResult(parsed, false, output, output)
			return m, nil
		}
		if parsed.SessionID == m.session.sessionID {
			newSessionID, err := m.session.store.CreateSession(m.ctx, chatstore.SessionRecord{
				Kind:      chatstore.SessionKindManager,
				Title:     "Manager",
				StartedAt: m.session.now(),
				UpdatedAt: m.session.now(),
			})
			if err != nil {
				output := fmt.Sprintf("Failed to rotate active session before delete: %s", err)
				m.renderCommandResult(parsed, false, output, output)
				return m, nil
			}
			m.session.sessionID = newSessionID
		}
		if err := m.session.store.DeleteSession(m.ctx, parsed.SessionID); err != nil {
			output := fmt.Sprintf("Failed to delete session %d: %s", parsed.SessionID, err)
			m.renderCommandResult(parsed, false, output, output)
			return m, nil
		}
		m.renderCommandResult(parsed, true, fmt.Sprintf("Deleted session %d.", parsed.SessionID), "")
		return m, nil
	case KindPurge:
		if m.session.store == nil {
			output := "Persistent chat storage unavailable."
			m.renderCommandResult(parsed, false, output, output)
			return m, nil
		}
		if err := m.session.store.PurgeAll(m.ctx); err != nil {
			output := fmt.Sprintf("Failed to purge persisted history: %s", err)
			m.renderCommandResult(parsed, false, output, output)
			return m, nil
		}
		m.session.history = nil
		newSessionID, err := m.session.store.CreateSession(m.ctx, chatstore.SessionRecord{
			Kind:      chatstore.SessionKindManager,
			Title:     "Manager",
			StartedAt: m.session.now(),
			UpdatedAt: m.session.now(),
		})
		if err != nil {
			output := fmt.Sprintf("Purged history but failed to create a new persistence session: %s", err)
			m.renderCommandResult(parsed, false, output, output)
			return m, nil
		}
		m.session.sessionID = newSessionID
		m.renderCommandResult(parsed, true, "Persisted chat history purged.", "")
		return m, nil
	case KindClear:
		m.session.history = nil
		m.outputContent = ""
		m.syncOutputViewport()
		m.renderCommandResult(parsed, true, "Session cleared.", "")
		return m, nil
	case KindExit:
		if m.pending {
			m.pendingQuit = true
			m.renderCommandResult(parsed, true, "Exit requested. Waiting for the active operation to finish...", "")
			return m, nil
		}
		m.renderCommandResult(parsed, true, "Exiting TOPS manager TUI.", "")
		return m, tea.Quit()
	default:
		output := guidanceMessage()
		m.renderCommandResult(parsed, false, output, output)
		return m, nil
	}
}

func (m *sessionModel) handleSetupKey(msg tea.KeyMsg) (*sessionModel, tea.Cmd) {
	if isQuitKey(msg) {
		return m, tea.Quit()
	}
	switch msg.Type {
	case tea.KeyEsc:
		m.mode = uiModeManager
		m.configureInputForManager()
		m.appendOutputBlock("Setup wizard closed.")
		return m, nil
	case tea.KeyEnter:
		raw := strings.TrimSpace(m.input.Value())
		m.input.SetValue("")
		result := m.setup.Submit(raw)
		if result.Output != "" {
			m.setup.InfoMessage = result.Output
		}
		if result.Cancelled {
			m.mode = uiModeManager
			m.configureInputForManager()
			m.appendOutputBlock(result.Output)
			return m, nil
		}
		m.configureInputForSetup()
		if result.SavedConfig != nil {
			m.startPending("provider")
			return m, saveSetupCmd(m.ctx, m.setup.ConfigPath, *result.SavedConfig, m.session.runtimeLoader)
		}
		return m, nil
	default:
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
}

func (m *sessionModel) renderCommandResult(parsed ParseResult, success bool, output string, errorText string) {
	if strings.TrimSpace(parsed.Raw) != "" {
		m.appendOutputBlock(fmt.Sprintf("> %s\n%s", strings.TrimSpace(parsed.Raw), strings.TrimSpace(output)))
	} else {
		m.appendOutputBlock(strings.TrimSpace(output))
	}
	if strings.TrimSpace(parsed.Raw) != "" {
		m.session.appendHistory(parsed.Raw, success, output)
	}
	m.session.persist(m.ctx, parsed, success, output, errorText, m.runtime)
}

func (m *sessionModel) appendBanner() {
	lines := []string{
		"TOPS Manager TUI (v1, non-executing)",
		"This interface is for model/chat management. Use CLI for /help /gen /ask.",
		"Commands: /setup, /models, /model use <index|name>, /model config <show|set|reset>, /model response <show|set>, /execution policy <show|set>, /execution trace <show|set>",
		"Chat: /history, /history db [N], /sessions [N], /session read <id> [N], /session delete <id> confirm, /purge confirm, /clear, /exit",
		"Local runtime status is shown live in the header (green=ready, red=not ready).",
	}
	m.appendOutputBlock(strings.Join(lines, "\n"))
}

func (m *sessionModel) appendOutputBlock(block string) {
	block = strings.TrimSpace(block)
	if block == "" {
		return
	}
	if m.outputContent != "" {
		m.outputContent += "\n\n"
	}
	m.outputContent += block
	m.syncOutputViewport()
	m.outputViewport.GotoBottom()
}

func (m *sessionModel) syncOutputViewport() {
	if m.outputViewport.Width <= 0 || m.outputViewport.Height <= 0 {
		return
	}
	content := strings.TrimSpace(m.outputContent)
	if content == "" {
		content = render.NewStyle().Foreground(render.Color("245")).Render("No output yet.")
	} else {
		content = wrapTextBlock(content, m.outputViewport.Width)
	}
	m.outputViewport.SetContent(content)
}

func (m *sessionModel) enterSetupMode(reason string) {
	var existing *config.Config
	if m.runtime != nil {
		cfg := m.runtime.Config
		existing = &cfg
	}
	m.setup = NewSetupWizardState(m.session.configPath, existing)
	m.setup.InfoMessage = strings.TrimSpace(reason)
	m.setup.ErrorMessage = ""
	m.mode = uiModeSetup
	m.configureInputForSetup()
}

func (m *sessionModel) configureInputForSetup() {
	m.input.Focus()
	m.input.Prompt = "setup> "
	m.input.Placeholder = m.setup.PromptLabel()
	m.input.SetValue("")
}

func (m *sessionModel) configureInputForManager() {
	m.input.Prompt = "tps> "
	m.input.Placeholder = "Press / to enter command mode"
	if !m.configInputActive {
		m.input.Blur()
		m.input.SetValue("")
	}
	if m.configInputActive {
		m.input.Focus()
		m.input.Prompt = "tps> "
		m.input.Placeholder = guidanceMessage()
	}
	if m.configMenu.Edit != nil {
		m.input.Focus()
		m.configInputActive = false
		m.input.Prompt = "value> "
		m.input.Placeholder = "Press Enter to apply, Esc to cancel"
		m.input.SetValue(m.configMenu.Edit.Value)
		return
	}
	if m.configInputActive {
		return
	}
	m.input.SetValue("")
}

func (m *sessionModel) setConfigInputActive(active bool) {
	m.configInputActive = active
	m.input.Prompt = "tps> "
	if active {
		m.input.Focus()
		m.input.Placeholder = guidanceMessage()
		return
	}
	m.input.Blur()
	m.input.Placeholder = "Press / to enter command mode"
	m.input.SetValue("")
}

func (m *sessionModel) syncInputForActiveSurface() {
	switch m.mode {
	case uiModeSetup:
		m.configureInputForSetup()
		return
	case uiModeManager:
		if m.activeTab == tabChats {
			m.configureInputForChat()
			return
		}
		m.setConfigInputActive(false)
		m.refreshConfigMenu()
		m.configureInputForManager()
		m.syncOutputViewport()
	}
}

func (m *sessionModel) applyLayout() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	headerHeight := 8
	footerHeight := 3
	viewHeight := m.height - headerHeight - footerHeight
	if viewHeight < 5 {
		viewHeight = 5
	}
	configHeaderHeight := min(14, max(9, viewHeight/2))
	outputHeight := viewHeight - configHeaderHeight - 1
	if outputHeight < 4 {
		outputHeight = 4
		configHeaderHeight = max(5, viewHeight-outputHeight-1)
	}
	m.outputViewport.Width = max(1, m.width-6)
	m.outputViewport.Height = max(1, outputHeight)
	m.configViewport.Width = max(1, m.width-6)
	m.configViewport.Height = max(1, configHeaderHeight)
	m.chatViewport.Width = max(20, m.width-2)
	m.chatViewport.Height = max(1, viewHeight)
	m.chatOverlayVP.Width = max(28, min(64, m.width-12))
	m.chatOverlayVP.Height = max(8, min(18, viewHeight-4))
	m.copyOverlayVP.Width = max(36, min(76, m.width-12))
	m.copyOverlayVP.Height = max(10, min(22, viewHeight-4))
	m.input.Width = max(20, m.width-12)
	if m.shell != nil {
		_ = m.shell.Resize(m.chatViewport.Width, m.chatViewport.Height)
	}
	m.syncOutputViewport()
}

func (m sessionModel) renderMainLayout() string {
	header := m.renderMainHeader()
	body := m.renderConfigBody()
	footer := m.renderConfigFooter()
	if m.activeTab == tabChats {
		body = m.renderChatBody()
		footer = renderChatFooter(m)
	}
	if m.height > 0 {
		headerLines := countRenderedLines(header)
		footerLines := countRenderedLines(footer)
		availableBodyLines := m.height - headerLines - footerLines
		if availableBodyLines < 1 {
			availableBodyLines = 1
		}
		body = clampRenderedLines(body, availableBodyLines)
	}
	return strings.Join([]string{
		header,
		body,
		footer,
	}, "\n")
}

func (m sessionModel) renderConfigBody() string {
	menuPane := render.NewStyle().
		Border(render.RoundedBorder()).
		BorderForeground(render.Color("63")).
		Padding(0, 1).
		Width(m.configViewport.Width + 2).
		Height(m.configViewport.Height + 2).
		Render("Config Menu\n" + m.renderConfigMenuColumns(m.configViewport.Width))

	outputPane := render.NewStyle().
		Border(render.RoundedBorder()).
		BorderForeground(render.Color("63")).
		Padding(0, 1).
		Width(m.outputViewport.Width + 2).
		Height(m.outputViewport.Height + 2).
		Render("Manager Output\n" + m.outputViewport.View())
	return render.JoinVertical(render.Left, menuPane, outputPane)
}

func (m sessionModel) renderConfigFooter() string {
	hint := "Shift+Tab tabs  ↑/↓ select  Space toggle  Enter apply  PgUp/PgDn output  / command"
	if m.configInputActive || m.configMenu.Edit != nil {
		hint = "Enter runs command / applies edit, Esc cancels"
	}
	return render.NewStyle().
		Border(render.RoundedBorder()).
		BorderForeground(render.Color("63")).
		Padding(0, 1).
		Render(render.NewStyle().Bold(true).Foreground(render.Color("63")).Render("Config Input") +
			render.NewStyle().Foreground(render.Color("245")).Render("  "+hint) + "\n" + m.input.View())
}

func (m sessionModel) renderMainHeader() string {
	lines := []string{renderTabs(m.activeTab)}
	if m.activeTab == tabChats {
		state := m.currentChatState()
		lines = append(lines, strings.Join([]string{
			renderPill("Focus", chatFocusLabel(state), render.Color("69")),
			renderPill("TOPS", topsStatusLabel(state), chatStatusColor(topsStatusLabel(state))),
			m.renderLocalStatusLine(),
		}, "   "))
		lines = append(lines, fmt.Sprintf("Shift+Tab tabs  Tab focus  Esc back  ↑/↓ PgUp/PgDn Home/End scroll  Ctrl+K copy items  Ctrl+E export  Ctrl+Y mouse:%s", onOff(m.mouseCapture)))
		return strings.Join(lines, "\n")
	}
	lines = append(lines, fmt.Sprintf("Config    Runtime: %t    %s    %s", m.runtime != nil, m.renderLocalStatusLine(), m.renderPendingLine()))
	lines = append(lines, fmt.Sprintf("Shift+Tab tabs  ↑/↓ select  Space toggle  Enter apply  PgUp/PgDn output  / command  Ctrl+Y mouse:%s", onOff(m.mouseCapture)))
	return strings.Join(lines, "\n")
}

func chatStatusColor(status string) render.Color {
	switch status {
	case string(topsStatusThinking), "running tool":
		return render.Color("214")
	case string(topsStatusWaitingApproval):
		return render.Color("203")
	default:
		return render.Color("42")
	}
}

func (m sessionModel) renderPendingLine() string {
	if !m.pending {
		return "Operation: idle"
	}
	elapsed := time.Since(m.pendingSince)
	if elapsed < 0 {
		elapsed = 0
	}
	return fmt.Sprintf("Operation: in progress (%s, %s elapsed)", m.pendingPhase, elapsed.Round(time.Second))
}

func (m sessionModel) renderSetupView() string {
	var b strings.Builder
	b.WriteString(m.setup.Header())
	b.WriteString("\n")
	b.WriteString("Complete each step and press Enter. Press Esc to cancel setup.\n\n")
	if strings.TrimSpace(m.setup.InfoMessage) != "" {
		b.WriteString("Info: ")
		b.WriteString(strings.TrimSpace(m.setup.InfoMessage))
		b.WriteString("\n")
	}
	if strings.TrimSpace(m.setup.ErrorMessage) != "" {
		b.WriteString("Error: ")
		b.WriteString(strings.TrimSpace(m.setup.ErrorMessage))
		b.WriteString("\n")
	}
	if m.setup.Step == setupStepReview {
		b.WriteString("\n")
		b.WriteString(m.setup.ReviewText())
		b.WriteString("\n\n")
	}
	if m.setup.Step == setupStepModel && len(m.setup.AvailableModels) > 0 {
		b.WriteString("\nDiscovered local models:\n")
		for i, modelName := range m.setup.AvailableModels {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, modelName)
			if i >= 9 {
				break
			}
		}
	}
	prompt := m.setup.PromptLabel()
	def := m.setup.PromptDefault()
	if def != "" {
		prompt = fmt.Sprintf("%s [%s]", prompt, def)
	}
	if prompt != "" {
		b.WriteString("\n")
		b.WriteString(prompt)
		b.WriteString("\n")
	}
	return strings.Join([]string{
		b.String(),
		m.input.View(),
	}, "\n")
}

func discoverModelsCmd(modelsDir string) tea.Cmd {
	return func() tea.Msg {
		entries, err := localruntime.DiscoverModels(modelsDir)
		if err != nil {
			return setupModelDiscoveryMsg{Err: err}
		}
		models := make([]string, 0, len(entries))
		for _, entry := range entries {
			models = append(models, entry.Name)
		}
		return setupModelDiscoveryMsg{Models: models, Err: nil}
	}
}

func saveSetupCmd(ctx context.Context, configPath string, cfg config.Config, loader RuntimeLoader) tea.Cmd {
	return func() tea.Msg {
		if err := config.SaveAtomic(configPath, cfg); err != nil {
			return setupSaveMsg{Err: fmt.Errorf("save configuration: %w", err)}
		}
		if loader == nil {
			rt, err := app.NewRuntime(cfg)
			if err != nil {
				return setupSaveMsg{Err: fmt.Errorf("reload runtime: %w", err)}
			}
			return setupSaveMsg{Runtime: rt}
		}
		rt, err := loader(configPath)
		if err != nil {
			return setupSaveMsg{Err: fmt.Errorf("reload runtime: %w", err)}
		}
		return setupSaveMsg{Runtime: rt}
	}
}

func listModelsCmd(ctx context.Context, rt app.Runtime, parsed ParseResult) tea.Cmd {
	return func() tea.Msg {
		output, _, err := listModelsOutput(ctx, rt)
		return managerCommandResultMsg{
			Parsed:        parsed,
			Output:        output,
			Err:           err,
			RefreshStatus: false,
		}
	}
}

func switchModelCmd(ctx context.Context, rt app.Runtime, configPath string, loader RuntimeLoader, parsed ParseResult) tea.Cmd {
	return func() tea.Msg {
		output, _, updated, err := switchModel(ctx, rt, configPath, loader, parsed.Payload)
		return managerCommandResultMsg{
			Parsed:         parsed,
			Output:         output,
			Err:            err,
			UpdatedRuntime: updated,
			RefreshStatus:  true,
		}
	}
}

func listModelsOutput(ctx context.Context, rt app.Runtime) (string, string, error) {
	_ = ctx
	if !isLocalProvider(rt.Config.Provider.Type) {
		return "", "configuration", fmt.Errorf("configuration error: /models is only available for yzma provider")
	}
	svc := localruntime.NewService(rt.Logger, nil)
	result, err := svc.ListModelsDetailed(rt.Config)
	if err != nil {
		return "", "provider", fmt.Errorf("provider error: %w", err)
	}
	var b strings.Builder
	b.WriteString("Model scan paths:\n")
	if len(result.ModelsDirs) == 0 {
		b.WriteString("  (none configured)\n")
	} else {
		for i, dir := range result.ModelsDirs {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, dir)
		}
	}
	for _, warning := range result.Warnings {
		warning = strings.TrimSpace(warning)
		if warning == "" {
			continue
		}
		fmt.Fprintf(&b, "Warning: %s\n", warning)
	}
	if len(result.Models) == 0 {
		b.WriteString("\nNo local GGUF models found across configured model scan paths.")
	} else {
		b.WriteString("\nAvailable local models:\n")
		for i, entry := range result.Models {
			fmt.Fprintf(&b, "%d. %s (%s)\n", i+1, entry.Name, entry.Path)
		}
	}
	fmt.Fprintf(&b, "\nCurrent model: %s", rt.Config.Provider.Model)
	if strings.TrimSpace(rt.Config.Provider.ModelPath) != "" {
		fmt.Fprintf(&b, "\nCurrent model path: %s", rt.Config.Provider.ModelPath)
	}
	return strings.TrimSpace(b.String()), "provider", nil
}

func switchModel(ctx context.Context, rt app.Runtime, configPath string, loader RuntimeLoader, choice string) (string, string, *app.Runtime, error) {
	if !isLocalProvider(rt.Config.Provider.Type) {
		return "", "configuration", nil, fmt.Errorf("configuration error: /model use is only available for yzma provider")
	}
	svc := localruntime.NewService(rt.Logger, nil)
	result, listErr := svc.ListModelsDetailed(rt.Config)
	if listErr != nil {
		return "", "provider", nil, fmt.Errorf("provider error: %w", listErr)
	}

	selected := strings.TrimSpace(choice)
	if strings.TrimSpace(selected) == "" {
		return "", "configuration", nil, fmt.Errorf("configuration error: missing model selection")
	}
	selectedName, selectedPath, err := resolveLocalModelSelection(selected, result.Models)
	if err != nil {
		return "", "configuration", nil, fmt.Errorf("configuration error: %w", err)
	}

	if strings.TrimSpace(configPath) == "" {
		path, err := config.DefaultPath()
		if err != nil {
			return "", "configuration", nil, fmt.Errorf("configuration error: resolve config path: %w", err)
		}
		configPath = path
	}

	cfg := rt.Config
	cfg.Provider.Model = selectedName
	cfg.Provider.ModelPath = selectedPath
	if err := config.SaveAtomic(configPath, cfg); err != nil {
		return "", "configuration", nil, fmt.Errorf("configuration error: failed to save config: %w", err)
	}

	reloaded, err := reloadRuntime(cfg, configPath, loader)
	if err != nil {
		return "", "provider", nil, err
	}

	warmErr := reloaded.WarmLocalModel(ctx)
	output := fmt.Sprintf("Model switched to %q and saved to config.", selectedName)
	output += fmt.Sprintf("\nModel path: %s", selectedPath)
	if warmErr != nil {
		output += fmt.Sprintf("\nWarning: model warm-up failed: %s", warmErr)
	} else {
		output += "\nLocal runtime warmed the selected model."
	}
	return output, "provider", &reloaded, nil
}

func showExecutionPolicy(rt app.Runtime) (string, string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Execution policy\n")
	fmt.Fprintf(&b, "read_only: %s\n", rt.Config.Execution.Permissions.ReadOnly)
	fmt.Fprintf(&b, "write: %s", rt.Config.Execution.Permissions.Write)
	return b.String(), "configuration", nil
}

func setExecutionPolicy(rt app.Runtime, configPath string, loader RuntimeLoader, target string, value string) (string, string, *app.Runtime, error) {
	target = strings.TrimSpace(strings.ToLower(target))
	value = strings.TrimSpace(strings.ToLower(value))
	permission := config.ActionPermission(value)
	switch permission {
	case config.ActionPermissionAllow, config.ActionPermissionRequest, config.ActionPermissionDisallow:
	default:
		return "", "configuration", nil, fmt.Errorf("configuration error: permission value must be allow|request|disallow")
	}
	cfg := rt.Config
	switch target {
	case "read-only":
		cfg.Execution.Permissions.ReadOnly = permission
	case "write":
		cfg.Execution.Permissions.Write = permission
	default:
		return "", "configuration", nil, fmt.Errorf("configuration error: unknown policy target %q", target)
	}
	if strings.TrimSpace(configPath) == "" {
		path, err := config.DefaultPath()
		if err != nil {
			return "", "configuration", nil, fmt.Errorf("configuration error: resolve config path: %w", err)
		}
		configPath = path
	}
	if err := config.SaveAtomic(configPath, cfg); err != nil {
		return "", "configuration", nil, fmt.Errorf("configuration error: failed to save config: %w", err)
	}
	reloaded, err := reloadRuntime(cfg, configPath, loader)
	if err != nil {
		return "", "provider", nil, err
	}
	return fmt.Sprintf("Execution policy updated: %s=%s", target, permission), "configuration", &reloaded, nil
}

func showExecutionTrace(rt app.Runtime) (string, string, error) {
	return fmt.Sprintf("Execution trace\ntrace_mode: %s", rt.Config.Execution.TraceMode), "configuration", nil
}

func setExecutionTrace(rt app.Runtime, configPath string, loader RuntimeLoader, value string) (string, string, *app.Runtime, error) {
	mode := config.TraceMode(strings.ToLower(strings.TrimSpace(value)))
	switch mode {
	case config.TraceModeRelease, config.TraceModeDebug:
	default:
		return "", "configuration", nil, fmt.Errorf("configuration error: trace mode must be release|debug")
	}
	cfg := rt.Config
	cfg.Execution.TraceMode = mode
	if strings.TrimSpace(configPath) == "" {
		path, err := config.DefaultPath()
		if err != nil {
			return "", "configuration", nil, fmt.Errorf("configuration error: resolve config path: %w", err)
		}
		configPath = path
	}
	if err := config.SaveAtomic(configPath, cfg); err != nil {
		return "", "configuration", nil, fmt.Errorf("configuration error: failed to save config: %w", err)
	}
	reloaded, err := reloadRuntime(cfg, configPath, loader)
	if err != nil {
		return "", "provider", nil, err
	}
	return fmt.Sprintf("Execution trace updated: trace_mode=%s", mode), "configuration", &reloaded, nil
}

func showModelConfig(rt app.Runtime) (string, string, error) {
	if !isLocalProvider(rt.Config.Provider.Type) {
		return "", "configuration", fmt.Errorf("configuration error: /model config is only available for yzma provider")
	}
	profiles, err := modelprofile.Load("")
	if err != nil {
		return "", "configuration", fmt.Errorf("configuration error: load model profiles: %w", err)
	}
	profile, ok := profiles.Get(rt.Config.Provider.Type, rt.Config.Provider.Model)
	if !ok {
		return fmt.Sprintf("No model profile found for %s:%s.\nUse /model config set <context|max_length|system_prompt|intelligence_mode|think|temperature|top_k|top_p|min_p|repeat_penalty|think_budget_tokens> <value> to create one.", rt.Config.Provider.Type, rt.Config.Provider.Model), "configuration", nil
	}
	contextVal := "unset"
	if profile.Context > 0 {
		contextVal = strconv.Itoa(profile.Context)
	}
	maxLengthVal := "unset"
	if profile.MaxLength > 0 {
		maxLengthVal = strconv.Itoa(profile.MaxLength)
	}
	systemPromptVal := "unset"
	if profile.SystemPrompt != "" {
		systemPromptVal = profile.SystemPrompt
	}
	intelligenceVal := "auto"
	if strings.TrimSpace(profile.IntelligenceMode) != "" {
		intelligenceVal = strings.ToLower(strings.TrimSpace(profile.IntelligenceMode))
	}
	thinkVal := "unset"
	if strings.TrimSpace(profile.Think) != "" {
		thinkVal = strings.ToLower(strings.TrimSpace(profile.Think))
	}
	temperatureVal := "unset"
	if profile.Temperature > 0 {
		temperatureVal = formatFloat(profile.Temperature)
	}
	topKVal := "unset"
	if profile.TopK > 0 {
		topKVal = strconv.Itoa(profile.TopK)
	}
	topPVal := "unset"
	if profile.TopP > 0 {
		topPVal = formatFloat(profile.TopP)
	}
	minPVal := "unset"
	if profile.MinP > 0 {
		minPVal = formatFloat(profile.MinP)
	}
	repeatPenaltyVal := "unset"
	if profile.RepeatPenalty > 0 {
		repeatPenaltyVal = formatFloat(profile.RepeatPenalty)
	}
	thinkBudgetVal := "unset"
	if profile.ThinkBudgetTokens > 0 {
		thinkBudgetVal = strconv.Itoa(profile.ThinkBudgetTokens)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Model profile for %s:%s\n", profile.Provider, profile.Model)
	fmt.Fprintf(&b, "context: %s\n", contextVal)
	fmt.Fprintf(&b, "max_length: %s\n", maxLengthVal)
	fmt.Fprintf(&b, "system_prompt: %s\n", systemPromptVal)
	fmt.Fprintf(&b, "intelligence_mode: %s\n", intelligenceVal)
	fmt.Fprintf(&b, "think: %s\n", thinkVal)
	fmt.Fprintf(&b, "temperature: %s\n", temperatureVal)
	fmt.Fprintf(&b, "top_k: %s\n", topKVal)
	fmt.Fprintf(&b, "top_p: %s\n", topPVal)
	fmt.Fprintf(&b, "min_p: %s\n", minPVal)
	fmt.Fprintf(&b, "repeat_penalty: %s\n", repeatPenaltyVal)
	fmt.Fprintf(&b, "think_budget_tokens: %s", thinkBudgetVal)
	return b.String(), "configuration", nil
}

func showModelResponseProfile(rt app.Runtime) (string, string, error) {
	if !isLocalProvider(rt.Config.Provider.Type) {
		return "", "configuration", fmt.Errorf("configuration error: /model response is only available for yzma provider")
	}
	profiles, err := modelprofile.Load("")
	if err != nil {
		return "", "configuration", fmt.Errorf("configuration error: load model profiles: %w", err)
	}
	profile, ok := profiles.Get(rt.Config.Provider.Type, rt.Config.Provider.Model)
	if !ok {
		profile = modelprofile.ModelProfile{
			Provider: rt.Config.Provider.Type,
			Model:    rt.Config.Provider.Model,
		}
	}
	askProfile := profile.EffectiveAskResponseProfile()
	var b strings.Builder
	fmt.Fprintf(&b, "Ask response profile for %s:%s\n", rt.Config.Provider.Type, rt.Config.Provider.Model)
	fmt.Fprintf(&b, "answer: on (always)\n")
	fmt.Fprintf(&b, "observations: %s\n", onOff(askProfile.Observations))
	fmt.Fprintf(&b, "inferences: %s\n", onOff(askProfile.Inferences))
	fmt.Fprintf(&b, "uncertainties: %s\n", onOff(askProfile.Uncertainties))
	fmt.Fprintf(&b, "assumptions: %s\n", onOff(askProfile.Assumptions))
	fmt.Fprintf(&b, "notes: %s\n", onOff(askProfile.Notes))
	fmt.Fprintf(&b, "\nThese settings reduce ask response size and affect both prompt shape and rendering.")
	return b.String(), "configuration", nil
}

func setModelResponseProfile(rt app.Runtime, configPath string, loader RuntimeLoader, field string, value string) (string, string, *app.Runtime, error) {
	if !isLocalProvider(rt.Config.Provider.Type) {
		return "", "configuration", nil, fmt.Errorf("configuration error: /model response is only available for yzma provider")
	}
	profiles, err := modelprofile.Load("")
	if err != nil {
		return "", "configuration", nil, fmt.Errorf("configuration error: load model profiles: %w", err)
	}
	profile, ok := profiles.Get(rt.Config.Provider.Type, rt.Config.Provider.Model)
	if !ok {
		profile = modelprofile.ModelProfile{
			Provider: rt.Config.Provider.Type,
			Model:    rt.Config.Provider.Model,
		}
	}
	enabled, ok := parseOnOff(value)
	if !ok {
		return "", "configuration", nil, fmt.Errorf("configuration error: response value must be on|off")
	}
	switch field {
	case "observations":
		profile.AskResponse.Observations = boolPtr(enabled)
	case "inferences":
		profile.AskResponse.Inferences = boolPtr(enabled)
	case "uncertainties":
		profile.AskResponse.Uncertainties = boolPtr(enabled)
	case "assumptions":
		profile.AskResponse.Assumptions = boolPtr(enabled)
	case "notes":
		profile.AskResponse.Notes = boolPtr(enabled)
	default:
		return "", "configuration", nil, fmt.Errorf("configuration error: unsupported response field %q", field)
	}
	if err := profiles.Upsert(profile); err != nil {
		return "", "configuration", nil, fmt.Errorf("configuration error: %w", err)
	}
	if err := modelprofile.SaveAtomic("", profiles); err != nil {
		return "", "configuration", nil, fmt.Errorf("configuration error: save model profiles: %w", err)
	}
	reloaded, err := reloadRuntime(rt.Config, configPath, loader)
	if err != nil {
		return "", "provider", nil, err
	}
	return fmt.Sprintf("Model response profile updated for %s:%s (%s=%s).", rt.Config.Provider.Type, rt.Config.Provider.Model, field, onOff(enabled)), "configuration", &reloaded, nil
}

func setModelConfig(rt app.Runtime, configPath string, loader RuntimeLoader, field string, value string) (string, string, *app.Runtime, error) {
	if !isLocalProvider(rt.Config.Provider.Type) {
		return "", "configuration", nil, fmt.Errorf("configuration error: /model config is only available for yzma provider")
	}
	profiles, err := modelprofile.Load("")
	if err != nil {
		return "", "configuration", nil, fmt.Errorf("configuration error: load model profiles: %w", err)
	}
	profile, ok := profiles.Get(rt.Config.Provider.Type, rt.Config.Provider.Model)
	if !ok {
		profile = modelprofile.ModelProfile{
			Provider: rt.Config.Provider.Type,
			Model:    rt.Config.Provider.Model,
		}
	}
	switch field {
	case "context":
		parsed, err := parsePositiveInt(value)
		if err != nil {
			return "", "configuration", nil, fmt.Errorf("configuration error: %w", err)
		}
		profile.Context = parsed
	case "max_length":
		parsed, err := parsePositiveInt(value)
		if err != nil {
			return "", "configuration", nil, fmt.Errorf("configuration error: %w", err)
		}
		profile.MaxLength = parsed
	case "system_prompt":
		prompt := strings.TrimSpace(value)
		if prompt == "" {
			return "", "configuration", nil, fmt.Errorf("configuration error: system_prompt cannot be empty")
		}
		profile.SystemPrompt = prompt
	case "intelligence_mode":
		intelligenceMode := normalizeIntelligenceModeSetting(value)
		if intelligenceMode == "" {
			return "", "configuration", nil, fmt.Errorf("configuration error: intelligence_mode must be one of blitz|grounded|auto")
		}
		profile.IntelligenceMode = intelligenceMode
	case "think":
		think := normalizeThinkSetting(value)
		if think == "" {
			return "", "configuration", nil, fmt.Errorf("configuration error: think must be one of on|off|low|medium|high")
		}
		profile.Think = think
	case "temperature":
		parsed, err := parseNonNegativeFloat(value)
		if err != nil {
			return "", "configuration", nil, fmt.Errorf("configuration error: %w", err)
		}
		profile.Temperature = parsed
	case "top_k":
		parsed, err := parseNonNegativeInt(value)
		if err != nil {
			return "", "configuration", nil, fmt.Errorf("configuration error: %w", err)
		}
		profile.TopK = parsed
	case "top_p":
		parsed, err := parseUnitInterval(value)
		if err != nil {
			return "", "configuration", nil, fmt.Errorf("configuration error: %w", err)
		}
		profile.TopP = parsed
	case "min_p":
		parsed, err := parseUnitInterval(value)
		if err != nil {
			return "", "configuration", nil, fmt.Errorf("configuration error: %w", err)
		}
		profile.MinP = parsed
	case "repeat_penalty":
		parsed, err := parseNonNegativeFloat(value)
		if err != nil {
			return "", "configuration", nil, fmt.Errorf("configuration error: %w", err)
		}
		profile.RepeatPenalty = parsed
	case "think_budget_tokens":
		parsed, err := parsePositiveInt(value)
		if err != nil {
			return "", "configuration", nil, fmt.Errorf("configuration error: %w", err)
		}
		profile.ThinkBudgetTokens = parsed
	default:
		return "", "configuration", nil, fmt.Errorf("configuration error: unsupported model config field %q", field)
	}
	if err := profiles.Upsert(profile); err != nil {
		return "", "configuration", nil, fmt.Errorf("configuration error: %w", err)
	}
	if err := modelprofile.SaveAtomic("", profiles); err != nil {
		return "", "configuration", nil, fmt.Errorf("configuration error: save model profiles: %w", err)
	}
	reloaded, err := reloadRuntime(rt.Config, configPath, loader)
	if err != nil {
		return "", "provider", nil, err
	}
	output := fmt.Sprintf("Model profile updated for %s:%s (%s).", rt.Config.Provider.Type, rt.Config.Provider.Model, field)
	return output, "configuration", &reloaded, nil
}

func resetModelConfig(rt app.Runtime, configPath string, loader RuntimeLoader) (string, string, *app.Runtime, error) {
	if !isLocalProvider(rt.Config.Provider.Type) {
		return "", "configuration", nil, fmt.Errorf("configuration error: /model config is only available for yzma provider")
	}
	profiles, err := modelprofile.Load("")
	if err != nil {
		return "", "configuration", nil, fmt.Errorf("configuration error: load model profiles: %w", err)
	}
	removed := profiles.Delete(rt.Config.Provider.Type, rt.Config.Provider.Model)
	if !removed {
		return "No model profile to reset for current provider/model.", "configuration", nil, nil
	}
	if err := modelprofile.SaveAtomic("", profiles); err != nil {
		return "", "configuration", nil, fmt.Errorf("configuration error: save model profiles: %w", err)
	}
	reloaded, err := reloadRuntime(rt.Config, configPath, loader)
	if err != nil {
		return "", "provider", nil, err
	}
	return fmt.Sprintf("Model profile reset for %s:%s.", rt.Config.Provider.Type, rt.Config.Provider.Model), "configuration", &reloaded, nil
}

func reloadRuntime(cfg config.Config, configPath string, loader RuntimeLoader) (app.Runtime, error) {
	if loader != nil {
		if strings.TrimSpace(configPath) == "" {
			path, err := config.DefaultPath()
			if err != nil {
				return app.Runtime{}, fmt.Errorf("configuration error: resolve config path: %w", err)
			}
			configPath = path
		}
		rt, err := loader(configPath)
		if err != nil {
			return app.Runtime{}, fmt.Errorf("provider error: failed to reload runtime: %w", err)
		}
		return rt, nil
	}
	rt, err := app.NewRuntime(cfg)
	if err != nil {
		return app.Runtime{}, fmt.Errorf("provider error: failed to reload runtime: %w", err)
	}
	return rt, nil
}

func parsePositiveInt(raw string) (int, error) {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("value %q must be a positive integer", raw)
	}
	return value, nil
}

func parseNonNegativeInt(raw string) (int, error) {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 0 {
		return 0, fmt.Errorf("value %q must be a non-negative integer", raw)
	}
	return value, nil
}

func parseNonNegativeFloat(raw string) (float64, error) {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("value %q must be a non-negative number", raw)
	}
	return value, nil
}

func parseUnitInterval(raw string) (float64, error) {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || value < 0 || value > 1 {
		return 0, fmt.Errorf("value %q must be between 0 and 1", raw)
	}
	return value, nil
}

func formatFloat(value float64) string {
	formatted := strconv.FormatFloat(value, 'f', -1, 64)
	if formatted == "" {
		return "0"
	}
	return formatted
}

func normalizeIntelligenceModeSetting(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "blitz":
		return "blitz"
	case "grounded":
		return "grounded"
	case "auto":
		return "auto"
	default:
		return ""
	}
}

func normalizeThinkSetting(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "on", "true":
		return "on"
	case "off", "false":
		return "off"
	case "low":
		return "low"
	case "medium":
		return "medium"
	case "high":
		return "high"
	default:
		return ""
	}
}

func parseOnOff(raw string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "on", "true":
		return true, true
	case "off", "false":
		return false, true
	default:
		return false, false
	}
}

func onOff(value bool) string {
	if value {
		return "on"
	}
	return "off"
}

func boolPtr(v bool) *bool {
	return &v
}

func resolveLocalModelSelection(choice string, available []localruntime.ModelEntry) (string, string, error) {
	choice = strings.TrimSpace(choice)
	if choice == "" {
		return "", "", fmt.Errorf("missing model selection")
	}
	if idx, err := strconv.Atoi(choice); err == nil {
		if idx < 1 || idx > len(available) {
			return "", "", fmt.Errorf("model index %d is out of range", idx)
		}
		entry := available[idx-1]
		return entry.Name, entry.Path, nil
	}
	for _, entry := range available {
		if strings.EqualFold(entry.Path, choice) {
			return entry.Name, entry.Path, nil
		}
	}
	nameMatches := make([]localruntime.ModelEntry, 0, 2)
	for _, entry := range available {
		if strings.EqualFold(entry.Name, choice) {
			nameMatches = append(nameMatches, entry)
		}
	}
	if len(nameMatches) == 1 {
		return nameMatches[0].Name, nameMatches[0].Path, nil
	}
	if len(nameMatches) > 1 {
		var b strings.Builder
		b.WriteString(fmt.Sprintf("model name %q is ambiguous; use /model use <index> or /model use <full-path>. Matches:\n", choice))
		for i, entry := range available {
			if !strings.EqualFold(entry.Name, choice) {
				continue
			}
			fmt.Fprintf(&b, "%d. %s (%s)\n", i+1, entry.Name, entry.Path)
		}
		return "", "", fmt.Errorf("%s", strings.TrimSpace(b.String()))
	}
	if strings.HasSuffix(strings.ToLower(choice), ".gguf") {
		modelPath := choice
		if abs, err := filepath.Abs(choice); err == nil {
			modelPath = abs
		}
		if _, err := os.Stat(modelPath); err != nil {
			return "", "", fmt.Errorf("model path %q is not readable: %w", modelPath, err)
		}
		name := strings.TrimSuffix(filepath.Base(modelPath), filepath.Ext(modelPath))
		return name, modelPath, nil
	}
	return "", "", fmt.Errorf("unknown model %q; use /models to list available local models or pass a .gguf path", choice)
}

func isLocalProvider(providerType config.ProviderType) bool {
	return providerType == config.ProviderYZMA
}

func (m sessionModel) renderLocalStatusLine() string {
	status := m.localStatus
	if !status.Applicable {
		return "Local runtime status: n/a"
	}
	detail := strings.TrimSpace(status.Detail)
	if detail == "" {
		detail = "checking..."
	}
	if status.Available {
		dot := render.NewStyle().Foreground(render.Color("42")).Render("●")
		return fmt.Sprintf("Local runtime status: %s %s", dot, detail)
	}
	dot := render.NewStyle().Foreground(render.Color("196")).Render("●")
	return fmt.Sprintf("Local runtime status: %s %s", dot, detail)
}

func localStatusTickCmd() tea.Cmd {
	return func() tea.Msg {
		time.Sleep(localStatusRefreshInterval)
		return localStatusTickMsg{}
	}
}

func pendingTickCmd() tea.Cmd {
	return func() tea.Msg {
		time.Sleep(pendingRefreshInterval)
		return pendingTickMsg{}
	}
}

func checkLocalStatusCmd(ctx context.Context, rt *app.Runtime) tea.Cmd {
	return func() tea.Msg {
		if rt == nil || !isLocalProvider(rt.Config.Provider.Type) {
			return localStatusMsg{State: deriveLocalStatus(rt, localruntime.StatusResult{}, nil)}
		}
		_, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		defer cancel()
		svc := localruntime.NewService(rt.Logger, nil)
		status, err := svc.Status(rt.Config)
		return localStatusMsg{State: deriveLocalStatus(rt, status, err)}
	}
}

func initialWindowSizeCmd() tea.Cmd {
	return func() tea.Msg {
		width, height := detectTerminalSize()
		return tea.WindowSizeMsg{Width: width, Height: height}
	}
}

func detectTerminalSize() (int, int) {
	if width, height := terminalSizeFromEnv(); width > 0 && height > 0 {
		return width, height
	}
	files := []*os.File{os.Stdout, os.Stderr, os.Stdin}
	for _, file := range files {
		if file == nil {
			continue
		}
		fd := int(file.Fd())
		if !term.IsTerminal(fd) {
			continue
		}
		width, height, err := term.GetSize(fd)
		if err == nil && width > 0 && height > 0 {
			return width, height
		}
	}
	return 120, 36
}

func terminalSizeFromEnv() (int, int) {
	widthRaw := strings.TrimSpace(os.Getenv("COLUMNS"))
	heightRaw := strings.TrimSpace(os.Getenv("LINES"))
	if widthRaw == "" || heightRaw == "" {
		return 0, 0
	}
	width, wErr := strconv.Atoi(widthRaw)
	height, hErr := strconv.Atoi(heightRaw)
	if wErr != nil || hErr != nil || width <= 0 || height <= 0 {
		return 0, 0
	}
	return width, height
}

func deriveLocalStatus(rt *app.Runtime, status localruntime.StatusResult, err error) localStatusState {
	if rt == nil {
		return localStatusState{Applicable: false, Available: false, Detail: "runtime not configured"}
	}
	if !isLocalProvider(rt.Config.Provider.Type) {
		return localStatusState{Applicable: false, Available: false, Detail: "not using yzma"}
	}
	if err != nil {
		return localStatusState{Applicable: true, Available: false, Detail: "status check failed"}
	}
	if status.Ready {
		switch strings.TrimSpace(status.WarmState) {
		case "ready":
			return localStatusState{Applicable: true, Available: true, Detail: "ready (model warmed)"}
		default:
			return localStatusState{Applicable: true, Available: true, Detail: "ready (paths validated)"}
		}
	}
	detail := strings.TrimSpace(status.WarmState)
	if detail == "" {
		detail = "not ready"
	}
	if strings.TrimSpace(status.LastError) != "" {
		detail = detail + ": " + strings.TrimSpace(status.LastError)
	}
	return localStatusState{Applicable: true, Available: false, Detail: detail}
}

func normalizeRenderedView(view string, width int, height int) string {
	view = strings.ReplaceAll(view, "\r\n", "\n")
	view = strings.ReplaceAll(view, "\r", "")
	if width <= 0 && height <= 0 {
		return view
	}
	lines := strings.Split(view, "\n")
	if width > 0 {
		for i := range lines {
			lines[i] = clampDisplayWidth(lines[i], width)
		}
	}
	if height > 0 && len(lines) > height {
		lines = lines[:height]
	}
	return strings.Join(lines, "\n")
}

func clampDisplayWidth(line string, width int) string {
	if width <= 0 || render.Width(line) <= width {
		return line
	}
	var b strings.Builder
	currentWidth := 0
	for _, r := range line {
		rw := render.Width(string(r))
		if rw < 0 {
			rw = 0
		}
		if currentWidth+rw > width {
			break
		}
		b.WriteRune(r)
		currentWidth += rw
	}
	return b.String()
}

func countRenderedLines(view string) int {
	if strings.TrimSpace(view) == "" {
		return 0
	}
	return strings.Count(view, "\n") + 1
}

func clampRenderedLines(view string, maxLines int) string {
	if maxLines <= 0 {
		return ""
	}
	lines := strings.Split(view, "\n")
	if len(lines) <= maxLines {
		return view
	}
	return strings.Join(lines[:maxLines], "\n")
}

func max(a int, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func (m *sessionModel) startPending(phase string) {
	m.pendingCount++
	m.pending = m.pendingCount > 0
	if m.pendingCount == 1 {
		m.pendingSince = time.Now()
	}
	m.pendingPhase = strings.TrimSpace(phase)
	if m.pendingPhase == "" {
		m.pendingPhase = "working"
	}
}

func (m *sessionModel) stopPending() {
	if m.pendingCount > 0 {
		m.pendingCount--
	}
	m.pending = m.pendingCount > 0
	if !m.pending {
		m.pendingSince = time.Time{}
		m.pendingPhase = ""
	}
}
