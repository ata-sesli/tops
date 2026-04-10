package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"tops/internal/app"
	"tops/internal/chatstore"
	"tops/internal/config"
	"tops/internal/modelprofile"
	"tops/internal/ollama"
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

type ollamaStatusState struct {
	Applicable bool
	Available  bool
	Detail     string
}

type ollamaStatusMsg struct {
	State ollamaStatusState
}

type ollamaStatusTickMsg struct{}
type pendingTickMsg struct{}

type managerCommandResultMsg struct {
	Parsed         ParseResult
	Output         string
	Err            error
	UpdatedRuntime *app.Runtime
	RefreshStatus  bool
}

const ollamaStatusRefreshInterval = 5 * time.Second
const pendingRefreshInterval = 200 * time.Millisecond

type sessionModel struct {
	ctx     context.Context
	session *Session
	runtime *app.Runtime
	ollama  ollama.Manager

	mode      uiMode
	activeTab chatTab

	width  int
	height int

	input textinput.Model

	outputViewport viewport.Model
	outputContent  string
	chatViewport   viewport.Model
	chatOverlayVP  viewport.Model

	setup        SetupWizardState
	ollamaStatus ollamaStatusState
	pending      bool
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
	chatState         map[int64]*chatSessionState
}

func newSessionModel(ctx context.Context, session *Session, rt *app.Runtime, ollamaManager ollama.Manager) sessionModel {
	input := textinput.New()
	input.Prompt = "tops> "
	input.Placeholder = guidanceMessage()
	input.Focus()
	input.CharLimit = 0
	input.Width = 80

	outputVP := viewport.New(1, 1)
	chatVP := viewport.New(1, 1)
	overlayVP := viewport.New(1, 1)

	m := sessionModel{
		ctx:            ctx,
		session:        session,
		runtime:        rt,
		ollama:         ollamaManager,
		mode:           uiModeManager,
		activeTab:      tabConfig,
		input:          input,
		outputViewport: outputVP,
		chatViewport:   chatVP,
		chatOverlayVP:  overlayVP,
		ollamaStatus:   deriveOllamaStatus(rt, nil, nil),
		events:         make(chan tea.Msg, 256),
		shellFactory:   func() ShellController { return NewPTYShellController() },
		chatState:      map[int64]*chatSessionState{},
	}
	m.appendBanner()
	return m
}

func (m sessionModel) Init() tea.Cmd {
	return tea.Batch(
		ollamaStatusTickCmd(),
		checkOllamaStatusCmd(m.ctx, m.ollama, m.runtime),
		waitForChatEventCmd(m.events),
		refreshChatSessionsCmd(m.ctx, m.session.store),
	)
}

func (m sessionModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.applyLayout()
		return m, nil
	case ollamaStatusMsg:
		m.ollamaStatus = msg.State
		return m, nil
	case ollamaStatusTickMsg:
		return m, tea.Batch(
			ollamaStatusTickCmd(),
			checkOllamaStatusCmd(m.ctx, m.ollama, m.runtime),
		)
	case setupModelDiscoveryMsg:
		m.stopPending()
		if msg.Err != nil {
			m.setup.InfoMessage = fmt.Sprintf("Ollama model discovery failed: %s", msg.Err)
		} else {
			m.setup.AvailableModels = msg.Models
			if len(msg.Models) > 0 {
				m.setup.InfoMessage = fmt.Sprintf("Discovered %d Ollama models. Enter an index or model name.", len(msg.Models))
			} else {
				m.setup.InfoMessage = "No Ollama models discovered. You can still type any model name."
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
		m.configureInputForManager()
		m.appendOutputBlock("Setup completed. Runtime reloaded.")
		return m, checkOllamaStatusCmd(m.ctx, m.ollama, m.runtime)
	case chatSessionsLoadedMsg:
		m.chatSessions = msg.Sessions
		m.syncSelectedChat()
		m.refreshChatOverlay()
		if m.selectedChatID != 0 {
			if err := m.loadTranscriptForSelectedChat(); err == nil {
				m.configureInputForChat()
			}
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
		if msg.Err != nil {
			m.renderCommandResult(msg.Parsed, false, msg.Err.Error(), msg.Err.Error())
		} else {
			m.renderCommandResult(msg.Parsed, true, msg.Output, "")
		}
		if msg.RefreshStatus {
			if m.pendingQuit {
				m.pendingQuit = false
				return m, tea.Quit
			}
			return m, checkOllamaStatusCmd(m.ctx, m.ollama, m.runtime)
		}
		if m.pendingQuit {
			m.pendingQuit = false
			return m, tea.Quit
		}
		return m, nil
	case pendingTickMsg:
		if !m.pending {
			return m, nil
		}
		return m, pendingTickCmd()
	case tea.KeyMsg:
		if m.mode == uiModeSetup {
			return m.handleSetupKey(msg)
		}
		if msg.Type == tea.KeyShiftTab {
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
	}
	return m, nil
}

func (m sessionModel) View() string {
	if m.mode == uiModeSetup {
		return m.renderSetupView()
	}
	if m.activeTab == tabChats {
		return m.renderChatView()
	}
	return m.renderManagerView()
}

func (m *sessionModel) handleManagerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyEnter:
		line := strings.TrimSpace(m.input.Value())
		m.input.SetValue("")
		if line == "" {
			return m, nil
		}
		return m.processManagerLine(line)
	default:
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
}

func (m *sessionModel) processManagerLine(line string) (tea.Model, tea.Cmd) {
	parsed := ParseInput(line)
	if m.runtime != nil && m.runtime.Logger != nil && m.runtime.Logger.Enabled() {
		m.runtime.Logger.Printf("tui input kind=%s raw=%q", parsed.Kind, line)
	}

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
		output := "TUI manager mode does not run /help, /gen, or /ask. Use CLI commands like: tops help \"...\", tops gen \"...\", tops ask \"...\""
		m.renderCommandResult(parsed, false, output, output)
		return m, nil
	case KindModels:
		if m.runtime == nil {
			output := "configuration error: TOPS runtime is not configured. Use /setup to create or repair configuration."
			m.renderCommandResult(parsed, false, output, output)
			return m, nil
		}
		m.startPending("provider")
		return m, tea.Batch(pendingTickCmd(), listModelsCmd(m.ctx, m.ollama, *m.runtime, parsed))
	case KindModelUse:
		if m.runtime == nil {
			output := "configuration error: TOPS runtime is not configured. Use /setup to create or repair configuration."
			m.renderCommandResult(parsed, false, output, output)
			return m, nil
		}
		m.startPending("provider")
		return m, tea.Batch(
			pendingTickCmd(),
			switchModelCmd(m.ctx, m.ollama, *m.runtime, m.session.configPath, m.session.runtimeLoader, parsed),
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
		return m, checkOllamaStatusCmd(m.ctx, m.ollama, m.runtime)
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
		return m, checkOllamaStatusCmd(m.ctx, m.ollama, m.runtime)
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
		m.outputViewport.SetContent("")
		m.renderCommandResult(parsed, true, "Session cleared.", "")
		return m, nil
	case KindExit:
		if m.pending {
			m.pendingQuit = true
			m.renderCommandResult(parsed, true, "Exit requested. Waiting for the active operation to finish...", "")
			return m, nil
		}
		m.renderCommandResult(parsed, true, "Exiting TOPS manager TUI.", "")
		return m, tea.Quit
	default:
		output := guidanceMessage()
		m.renderCommandResult(parsed, false, output, output)
		return m, nil
	}
}

func (m *sessionModel) handleSetupKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.mode = uiModeManager
		m.configureInputForManager()
		m.appendOutputBlock("Setup wizard closed.")
		return m, nil
	case tea.KeyCtrlC:
		return m, tea.Quit
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
		if result.NeedModelDiscovery {
			m.startPending("provider")
			return m, discoverModelsCmd(m.ctx, m.ollama, result.DiscoveryEndpoint)
		}
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
		"Ollama status is shown live in the header (green=available, red=unavailable).",
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
	m.outputViewport.SetContent(m.outputContent)
	m.outputViewport.GotoBottom()
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
	m.input.Focus()
	m.input.Prompt = "tops> "
	m.input.Placeholder = guidanceMessage()
	m.input.SetValue("")
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
	m.outputViewport.Width = max(1, m.width-2)
	m.outputViewport.Height = max(1, viewHeight-2)
	m.chatViewport.Width = max(20, m.width-2)
	m.chatViewport.Height = max(1, viewHeight)
	m.chatOverlayVP.Width = max(28, min(64, m.width-12))
	m.chatOverlayVP.Height = max(8, min(18, viewHeight-4))
	m.input.Width = max(20, m.width-12)
	if m.shell != nil {
		_ = m.shell.Resize(m.chatViewport.Width, m.chatViewport.Height)
	}
}

func (m sessionModel) renderManagerView() string {
	header := []string{
		renderTabs(m.activeTab),
		fmt.Sprintf("Config    Runtime: %t    %s    %s", m.runtime != nil, m.renderOllamaStatusLine(), m.renderPendingLine()),
		"Shift+Tab switches tabs  /setup configures TOPS  /models manages Ollama  /history shows records  /exit quits",
	}
	headerText := strings.Join(header, "\n")

	mainPane := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("63")).
		Padding(0, 1).
		Width(m.outputViewport.Width + 2).
		Height(m.outputViewport.Height + 2).
		Render("Manager Output\n" + m.outputViewport.View())

	return strings.Join([]string{
		headerText,
		mainPane,
		m.input.View(),
	}, "\n")
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
		b.WriteString("\nDiscovered Ollama models:\n")
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

func discoverModelsCmd(ctx context.Context, manager ollama.Manager, endpoint string) tea.Cmd {
	return func() tea.Msg {
		if manager == nil {
			manager = ollama.NewManager(ollama.Options{})
		}
		if err := manager.EnsureRunning(ctx, endpoint); err != nil {
			return setupModelDiscoveryMsg{Err: err}
		}
		models, err := manager.ListModels(ctx, endpoint)
		return setupModelDiscoveryMsg{Models: models, Err: err}
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

func listModelsCmd(ctx context.Context, manager ollama.Manager, rt app.Runtime, parsed ParseResult) tea.Cmd {
	return func() tea.Msg {
		output, _, err := listModelsOutput(ctx, manager, rt)
		return managerCommandResultMsg{
			Parsed:        parsed,
			Output:        output,
			Err:           err,
			RefreshStatus: false,
		}
	}
}

func switchModelCmd(ctx context.Context, manager ollama.Manager, rt app.Runtime, configPath string, loader RuntimeLoader, parsed ParseResult) tea.Cmd {
	return func() tea.Msg {
		output, _, updated, err := switchModel(ctx, manager, rt, configPath, loader, parsed.Payload)
		return managerCommandResultMsg{
			Parsed:         parsed,
			Output:         output,
			Err:            err,
			UpdatedRuntime: updated,
			RefreshStatus:  true,
		}
	}
}

func listModelsOutput(ctx context.Context, manager ollama.Manager, rt app.Runtime) (string, string, error) {
	if !isOllamaProvider(rt.Config.Provider.Type) {
		return "", "configuration", fmt.Errorf("configuration error: /models is only available for ollama/local providers")
	}
	if manager == nil {
		manager = ollama.NewManager(ollama.Options{Logger: rt.Logger})
	}
	if err := manager.EnsureRunning(ctx, rt.Config.Provider.Endpoint); err != nil {
		return "", "provider", fmt.Errorf("provider error: %w", err)
	}
	models, err := manager.ListModels(ctx, rt.Config.Provider.Endpoint)
	if err != nil {
		return "", "provider", fmt.Errorf("provider error: %w", err)
	}
	if len(models) == 0 {
		return "No Ollama models found. Pull one with `ollama pull <model>` and run /models again.", "provider", nil
	}
	var b strings.Builder
	b.WriteString("Available Ollama models:\n")
	for i, name := range models {
		fmt.Fprintf(&b, "%d. %s\n", i+1, name)
	}
	fmt.Fprintf(&b, "\nCurrent model: %s", rt.Config.Provider.Model)
	return strings.TrimSpace(b.String()), "provider", nil
}

func switchModel(ctx context.Context, manager ollama.Manager, rt app.Runtime, configPath string, loader RuntimeLoader, choice string) (string, string, *app.Runtime, error) {
	if !isOllamaProvider(rt.Config.Provider.Type) {
		return "", "configuration", nil, fmt.Errorf("configuration error: /model use is only available for ollama/local providers")
	}
	if manager == nil {
		manager = ollama.NewManager(ollama.Options{Logger: rt.Logger})
	}
	if err := manager.EnsureRunning(ctx, rt.Config.Provider.Endpoint); err != nil {
		return "", "provider", nil, fmt.Errorf("provider error: %w", err)
	}
	selected := strings.TrimSpace(choice)
	if _, parseErr := strconv.Atoi(selected); parseErr == nil {
		models, err := manager.ListModels(ctx, rt.Config.Provider.Endpoint)
		if err != nil {
			return "", "provider", nil, fmt.Errorf("provider error: %w", err)
		}
		selected, err = resolveModelChoice(selected, models)
		if err != nil {
			return "", "configuration", nil, fmt.Errorf("configuration error: %w", err)
		}
	}
	if strings.TrimSpace(selected) == "" {
		return "", "configuration", nil, fmt.Errorf("configuration error: missing model selection")
	}
	if strings.TrimSpace(configPath) == "" {
		path, err := config.DefaultPath()
		if err != nil {
			return "", "configuration", nil, fmt.Errorf("configuration error: resolve config path: %w", err)
		}
		configPath = path
	}

	cfg := rt.Config
	cfg.Provider.Model = selected
	if err := config.SaveAtomic(configPath, cfg); err != nil {
		return "", "configuration", nil, fmt.Errorf("configuration error: failed to save config: %w", err)
	}

	reloaded, err := reloadRuntime(cfg, configPath, loader)
	if err != nil {
		return "", "provider", nil, err
	}

	warmErr := manager.WarmModel(ctx, rt.Config.Provider.Endpoint, selected)
	output := fmt.Sprintf("Model switched to %q and saved to config.", selected)
	if warmErr != nil {
		output += fmt.Sprintf("\nWarning: model warm-up failed: %s", warmErr)
	} else {
		output += "\nOllama warmed the selected model."
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
	if !isOllamaProvider(rt.Config.Provider.Type) {
		return "", "configuration", fmt.Errorf("configuration error: /model config is only available for ollama/local providers")
	}
	profiles, err := modelprofile.Load("")
	if err != nil {
		return "", "configuration", fmt.Errorf("configuration error: load model profiles: %w", err)
	}
	profile, ok := profiles.Get(rt.Config.Provider.Type, rt.Config.Provider.Model)
	if !ok {
		return fmt.Sprintf("No model profile found for %s:%s.\nUse /model config set <context|max_length|system_prompt> <value> to create one.", rt.Config.Provider.Type, rt.Config.Provider.Model), "configuration", nil
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
	thinkVal := "unset"
	if strings.TrimSpace(profile.Think) != "" {
		thinkVal = profile.Think
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Model profile for %s:%s\n", profile.Provider, profile.Model)
	fmt.Fprintf(&b, "context: %s\n", contextVal)
	fmt.Fprintf(&b, "max_length: %s\n", maxLengthVal)
	fmt.Fprintf(&b, "system_prompt: %s\n", systemPromptVal)
	fmt.Fprintf(&b, "think: %s", thinkVal)
	return b.String(), "configuration", nil
}

func showModelResponseProfile(rt app.Runtime) (string, string, error) {
	if !isOllamaProvider(rt.Config.Provider.Type) {
		return "", "configuration", fmt.Errorf("configuration error: /model response is only available for ollama/local providers")
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
	if !isOllamaProvider(rt.Config.Provider.Type) {
		return "", "configuration", nil, fmt.Errorf("configuration error: /model response is only available for ollama/local providers")
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
	if !isOllamaProvider(rt.Config.Provider.Type) {
		return "", "configuration", nil, fmt.Errorf("configuration error: /model config is only available for ollama/local providers")
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
	case "think":
		think := normalizeThinkSetting(value)
		if think == "" {
			return "", "configuration", nil, fmt.Errorf("configuration error: think must be one of on|off|low|medium|high")
		}
		profile.Think = think
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
	if !isOllamaProvider(rt.Config.Provider.Type) {
		return "", "configuration", nil, fmt.Errorf("configuration error: /model config is only available for ollama/local providers")
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

func normalizeThinkSetting(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "on", "true":
		return "on"
	case "off", "false":
		return "off"
	case "low", "medium", "high":
		return strings.ToLower(strings.TrimSpace(raw))
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

func resolveModelChoice(choice string, available []string) (string, error) {
	if choice == "" {
		return "", fmt.Errorf("missing model selection")
	}
	if idx, err := strconv.Atoi(choice); err == nil {
		if idx < 1 || idx > len(available) {
			return "", fmt.Errorf("model index %d is out of range", idx)
		}
		return available[idx-1], nil
	}
	return choice, nil
}

func isOllamaProvider(providerType config.ProviderType) bool {
	return providerType == config.ProviderOllama || providerType == config.ProviderLocal
}

func (m sessionModel) renderOllamaStatusLine() string {
	status := m.ollamaStatus
	if !status.Applicable {
		return "Ollama status: n/a"
	}
	detail := strings.TrimSpace(status.Detail)
	if detail == "" {
		detail = "checking..."
	}
	if status.Available {
		dot := lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Render("●")
		return fmt.Sprintf("Ollama status: %s %s", dot, detail)
	}
	dot := lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render("●")
	return fmt.Sprintf("Ollama status: %s %s", dot, detail)
}

func ollamaStatusTickCmd() tea.Cmd {
	return tea.Tick(ollamaStatusRefreshInterval, func(time.Time) tea.Msg {
		return ollamaStatusTickMsg{}
	})
}

func pendingTickCmd() tea.Cmd {
	return tea.Tick(pendingRefreshInterval, func(time.Time) tea.Msg {
		return pendingTickMsg{}
	})
}

func checkOllamaStatusCmd(ctx context.Context, manager ollama.Manager, rt *app.Runtime) tea.Cmd {
	return func() tea.Msg {
		if rt == nil || !isOllamaProvider(rt.Config.Provider.Type) {
			return ollamaStatusMsg{State: deriveOllamaStatus(rt, nil, nil)}
		}
		if manager == nil {
			manager = ollama.NewManager(ollama.Options{})
		}
		checkCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		defer cancel()
		models, err := manager.ListModels(checkCtx, rt.Config.Provider.Endpoint)
		return ollamaStatusMsg{State: deriveOllamaStatus(rt, models, err)}
	}
}

func deriveOllamaStatus(rt *app.Runtime, models []string, err error) ollamaStatusState {
	if rt == nil {
		return ollamaStatusState{Applicable: false, Available: false, Detail: "runtime not configured"}
	}
	if !isOllamaProvider(rt.Config.Provider.Type) {
		return ollamaStatusState{Applicable: false, Available: false, Detail: "not using ollama/local"}
	}
	if err != nil {
		return ollamaStatusState{Applicable: true, Available: false, Detail: "not available (ollama not serving)"}
	}
	selected := strings.TrimSpace(rt.Config.Provider.Model)
	if selected == "" {
		return ollamaStatusState{Applicable: true, Available: false, Detail: "model not configured"}
	}
	for _, modelName := range models {
		if strings.EqualFold(strings.TrimSpace(modelName), selected) {
			return ollamaStatusState{
				Applicable: true,
				Available:  true,
				Detail:     fmt.Sprintf("serving, model %q available", selected),
			}
		}
	}
	return ollamaStatusState{
		Applicable: true,
		Available:  false,
		Detail:     fmt.Sprintf("serving, model %q not found", selected),
	}
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
	m.pending = true
	m.pendingSince = time.Now()
	m.pendingPhase = strings.TrimSpace(phase)
	if m.pendingPhase == "" {
		m.pendingPhase = "working"
	}
}

func (m *sessionModel) stopPending() {
	m.pending = false
	m.pendingSince = time.Time{}
	m.pendingPhase = ""
}
