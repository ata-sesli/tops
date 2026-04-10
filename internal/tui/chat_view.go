package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"tops/internal/chatstore"
	"tops/internal/model"
)

type chatSessionsLoadedMsg struct {
	Sessions []chatstore.PersistedSession
	Err      error
}

func refreshChatSessionsCmd(ctx context.Context, store chatstore.ChatStore) tea.Cmd {
	return func() tea.Msg {
		if store == nil {
			return chatSessionsLoadedMsg{}
		}
		sessions, err := store.ListSessions(ctx, 200)
		if err != nil {
			return chatSessionsLoadedMsg{Err: err}
		}
		filtered := make([]chatstore.PersistedSession, 0, len(sessions))
		for _, session := range sessions {
			if session.Kind == chatstore.SessionKindChat {
				filtered = append(filtered, session)
			}
		}
		return chatSessionsLoadedMsg{Sessions: filtered}
	}
}

func waitForChatEventCmd(events <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-events
		if !ok {
			return nil
		}
		return msg
	}
}

func waitForShellOutputCmd(shell ShellController, sessionID int64) tea.Cmd {
	return func() tea.Msg {
		if shell == nil {
			return nil
		}
		event, ok := <-shell.Events()
		if !ok {
			return nil
		}
		return chatShellOutputMsg{SessionID: sessionID, Text: event.Data, Err: event.Err}
	}
}

func (m *sessionModel) toggleTab() {
	if m.activeTab == tabConfig {
		m.activeTab = tabChats
		m.configureInputForChat()
		return
	}
	m.activeTab = tabConfig
	m.configureInputForManager()
}

func (m *sessionModel) debugf(format string, args ...any) {
	if m == nil || m.runtime == nil || m.runtime.Logger == nil || !m.runtime.Logger.Enabled() {
		return
	}
	m.runtime.Logger.Printf(format, args...)
}

func (m *sessionModel) logChatEnterState(state *chatSessionState) {
	focus := "<none>"
	approval := false
	shellPaused := false
	if state != nil {
		focus = string(state.Focus)
		approval = state.Approval != nil
		shellPaused = state.ShellPaused
	}
	m.debugf(
		"tui chat enter pressed tab=%s overlay=%t selected_chat=%d focus=%s approval=%t shell_paused=%t input=%q",
		m.activeTab,
		m.chatOverlayOpen,
		m.selectedChatID,
		focus,
		approval,
		shellPaused,
		m.input.Value(),
	)
}

func (m *sessionModel) handleChatKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		if m.shell != nil {
			_ = m.shell.Close()
		}
		return m, tea.Quit
	case tea.KeyCtrlO:
		if m.chatOverlayOpen {
			m.chatOverlayOpen = false
		} else {
			m.chatOverlayOpen = true
			m.syncOverlayIndexFromSelection()
		}
		m.configureInputForChat()
		return m, refreshChatSessionsCmd(m.ctx, m.session.store)
	case tea.KeyEsc:
		if m.chatOverlayOpen {
			m.chatOverlayOpen = false
			m.configureInputForChat()
			return m, nil
		}
	case tea.KeyTab:
		if !m.chatOverlayOpen {
			m.toggleChatFocus()
		}
		return m, nil
	}

	if m.chatOverlayOpen {
		return m.handleChatOverlayKey(msg)
	}

	state := m.currentChatState()
	switch msg.Type {
	case tea.KeyEnter, tea.KeyCtrlJ:
		m.logChatEnterState(state)
		switch {
		case state != nil && state.Focus == chatFocusApproval:
			return m.submitApprovalResponse()
		case state != nil && state.Focus == chatFocusTOPS:
			return m.submitChatDraft()
		case state != nil && state.Focus == chatFocusShell:
			return m.submitShellDraft()
		default:
			return m, nil
		}
	case tea.KeyUp:
		if state != nil && state.Focus == chatFocusShell {
			m.cycleShellHistory(-1)
			return m, nil
		}
	case tea.KeyDown:
		if state != nil && state.Focus == chatFocusShell {
			m.cycleShellHistory(1)
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if state != nil {
		switch state.Focus {
		case chatFocusTOPS:
			state.Draft = m.input.Value()
		case chatFocusApproval:
			state.Draft = m.input.Value()
		case chatFocusShell:
			state.ShellDraft = m.input.Value()
		}
	}
	return m, cmd
}

func (m *sessionModel) handleChatOverlayKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyUp:
		if m.selectedChatIndex > 0 {
			m.selectedChatIndex--
		}
	case tea.KeyDown:
		if m.selectedChatIndex < len(m.chatSessions) {
			m.selectedChatIndex++
		}
	case tea.KeyRunes:
		if len(msg.Runes) == 1 {
			switch strings.ToLower(string(msg.Runes[0])) {
			case "n":
				return m.startNewChat()
			case "d":
				return m.deleteSelectedChatFromOverlay()
			case "k":
				if m.selectedChatIndex > 0 {
					m.selectedChatIndex--
				}
			case "j":
				if m.selectedChatIndex < len(m.chatSessions) {
					m.selectedChatIndex++
				}
			}
		}
	case tea.KeyEnter:
		if m.selectedChatIndex == 0 {
			return m.startNewChat()
		}
		if m.selectedChatIndex-1 < len(m.chatSessions) {
			return m.openChatSession(m.chatSessions[m.selectedChatIndex-1].ID)
		}
	}
	return m, nil
}

func (m *sessionModel) submitShellDraft() (tea.Model, tea.Cmd) {
	state := m.currentChatState()
	if state == nil {
		m.debugf("tui shell submit skipped: no active chat state")
		return m, nil
	}
	if state.ShellPaused || state.Focus == chatFocusApproval {
		m.debugf("tui shell submit skipped: shell paused=%t focus=%s", state.ShellPaused, state.Focus)
		return m, nil
	}
	command := strings.TrimRight(m.input.Value(), "\r\n")
	state.ShellDraft = command
	m.debugf("tui shell submit buffer=%q", command)
	state.ShellDraft = ""
	m.input.SetValue("")
	if command != "" {
		state.ShellHistory = append(state.ShellHistory, command)
		state.ShellHistPos = len(state.ShellHistory)
		state.ShellEchoQueue = append(state.ShellEchoQueue, command)
		m.appendShellCommandFromSubmit(state.ID, command)
	}
	data := []byte(command + "\r")
	m.debugf("tui shell submit prepared pty bytes=%q", string(data))
	waitCmd, ok := m.ensureShellForChat(state)
	if !ok {
		m.debugf("tui shell submit skipped: shell could not be started")
		return m, nil
	}
	if waitCmd != nil {
		return m, writeShellThenWaitCmd(m.shell, state.ID, data, m.debugf)
	}
	return m, writeShellCmd(m.shell, data, m.debugf)
}

func (m *sessionModel) cycleShellHistory(direction int) {
	state := m.currentChatState()
	if state == nil || len(state.ShellHistory) == 0 {
		return
	}
	if state.ShellHistPos < 0 || state.ShellHistPos > len(state.ShellHistory) {
		state.ShellHistPos = len(state.ShellHistory)
	}
	state.ShellHistPos += direction
	if state.ShellHistPos < 0 {
		state.ShellHistPos = 0
	}
	if state.ShellHistPos > len(state.ShellHistory) {
		state.ShellHistPos = len(state.ShellHistory)
	}
	if state.ShellHistPos == len(state.ShellHistory) {
		state.ShellDraft = ""
	} else {
		state.ShellDraft = state.ShellHistory[state.ShellHistPos]
	}
	m.input.SetValue(state.ShellDraft)
}

func writeShellCmd(shell ShellController, data []byte, logf func(string, ...any)) tea.Cmd {
	return func() tea.Msg {
		if shell == nil {
			if logf != nil {
				logf("tui shell pty write skipped: shell controller is nil")
			}
			return nil
		}
		if logf != nil {
			logf("tui shell pty write called bytes=%q", string(data))
		}
		if err := shell.Write(data); err != nil && logf != nil {
			logf("tui shell pty write failed: %v", err)
		}
		return nil
	}
}

func writeShellThenWaitCmd(shell ShellController, sessionID int64, data []byte, logf func(string, ...any)) tea.Cmd {
	return func() tea.Msg {
		if shell == nil {
			if logf != nil {
				logf("tui shell pty write skipped: shell controller is nil")
			}
			return nil
		}
		if logf != nil {
			logf("tui shell pty write called bytes=%q", string(data))
		}
		if err := shell.Write(data); err != nil {
			if logf != nil {
				logf("tui shell pty write failed: %v", err)
			}
			return chatShellOutputMsg{SessionID: sessionID, Err: err}
		}
		event, ok := <-shell.Events()
		if !ok {
			return nil
		}
		return chatShellOutputMsg{SessionID: sessionID, Text: event.Data, Err: event.Err}
	}
}

func (m *sessionModel) ensureShellForChat(state *chatSessionState) (tea.Cmd, bool) {
	if state == nil || state.ID <= 0 {
		m.debugf("tui shell ensure skipped: no active chat state")
		return nil, false
	}
	if m.shell != nil && m.liveChatID == state.ID {
		return nil, true
	}
	if m.shell != nil {
		_ = m.shell.Close()
		m.shell = nil
	}
	factory := m.shellFactory
	if factory == nil {
		factory = func() ShellController { return NewPTYShellController() }
	}
	controller := factory()
	if controller == nil {
		m.debugf("tui shell ensure failed: shell factory returned nil")
		return nil, false
	}
	shellName := ""
	if m.runtime != nil {
		shellName = m.runtime.Config.Shell
	}
	m.debugf("tui shell ensure starting shell=%q chat=%d", shellName, state.ID)
	if err := controller.Start(m.ctx, shellName, m.chatViewport.Width, m.chatViewport.Height); err != nil {
		m.debugf("tui shell ensure failed: %v", err)
		m.appendChatMessage(state.ID, chatstore.MessageRecord{
			SessionID: state.ID,
			Timestamp: m.session.now(),
			Source:    "system",
			Kind:      "status",
			Output:    fmt.Sprintf("Failed to start shell: %s", err),
			Success:   false,
		})
		return nil, false
	}
	m.shell = controller
	m.liveChatID = state.ID
	state.Live = true
	return waitForShellOutputCmd(m.shell, state.ID), true
}

func (m *sessionModel) submitChatDraft() (tea.Model, tea.Cmd) {
	state := m.currentChatState()
	if state == nil || m.runtime == nil {
		return m, nil
	}
	mode, input, err := parseChatDraft(m.input.Value())
	if err != nil {
		m.appendChatMessage(state.ID, chatstore.MessageRecord{
			SessionID: state.ID,
			Timestamp: m.session.now(),
			Source:    "system",
			Kind:      "status",
			Output:    "Start your message with ask or gen.",
			Success:   false,
		})
		state.Draft = nextDraftForMode(state.StickyMode)
		m.configureInputForChat()
		return m, nil
	}
	if strings.TrimSpace(input) == "" {
		return m, nil
	}
	state.StickyMode = mode
	state.Draft = nextDraftForMode(mode)
	state.TopsStatus = topsStatusThinking
	state.Waiting = true
	state.ShellPaused = true
	rawInput := fmt.Sprintf("%s %s", mode, input)
	m.appendChatMessage(state.ID, chatstore.MessageRecord{
		SessionID: state.ID,
		Timestamp: m.session.now(),
		Source:    "tops_user",
		RawInput:  rawInput,
		Kind:      string(mode),
		Mode:      string(mode),
		Output:    input,
		Success:   true,
	})
	if !state.TitleAssigned {
		title := deriveChatTitle(input)
		if m.session.store != nil && state.ID > 0 {
			_ = m.session.store.UpdateSessionTitle(m.ctx, state.ID, title)
		}
		state.Title = title
		state.TitleAssigned = true
	}
	m.configureInputForChat()
	return m, tea.Batch(
		runChatTurnCmd(m.ctx, m.events, m.session, *m.runtime, state.ID, mode, input),
		refreshChatSessionsCmd(m.ctx, m.session.store),
	)
}

func (m *sessionModel) submitApprovalResponse() (tea.Model, tea.Cmd) {
	state := m.currentChatState()
	if state == nil || state.Approval == nil {
		return m, nil
	}
	answer := strings.ToLower(strings.TrimSpace(m.input.Value()))
	approved := answer == "y" || answer == "yes"
	state.Approval.Response <- approved
	output := "Denied."
	if approved {
		output = "Approved."
	}
	m.appendChatMessage(state.ID, chatstore.MessageRecord{
		SessionID: state.ID,
		Timestamp: m.session.now(),
		Source:    "approval",
		Kind:      "approval_decision",
		Output:    output,
		Success:   approved,
	})
	state.Focus = chatFocusTOPS
	state.TopsStatus = topsStatusThinking
	state.Approval = nil
	state.Draft = nextDraftForMode(state.StickyMode)
	m.configureInputForChat()
	return m, nil
}

func (m *sessionModel) openChatSession(sessionID int64) (tea.Model, tea.Cmd) {
	if sessionID <= 0 {
		return m, nil
	}
	m.selectedChatID = sessionID
	m.chatOverlayOpen = false

	state, ok := m.chatState[sessionID]
	if !ok {
		title := fmt.Sprintf("Chat %d", sessionID)
		for _, session := range m.chatSessions {
			if session.ID == sessionID && strings.TrimSpace(session.Title) != "" {
				title = session.Title
				break
			}
		}
		state = &chatSessionState{
			ID:         sessionID,
			Title:      title,
			Live:       true,
			StickyMode: model.ModeAsk,
			Draft:      "ask ",
			TopsStatus: topsStatusIdle,
			Focus:      chatFocusTOPS,
		}
		m.chatState[sessionID] = state
	}
	state.Live = true
	state.Focus = chatFocusTOPS

	if err := m.loadTranscriptForSelectedChat(); err != nil {
		m.appendChatMessage(sessionID, chatstore.MessageRecord{
			SessionID: sessionID,
			Timestamp: m.session.now(),
			Source:    "system",
			Kind:      "status",
			Output:    fmt.Sprintf("Failed to load chat transcript: %s", err),
			Success:   false,
		})
	}

	waitCmd, ok := m.ensureShellForChat(state)
	if !ok {
		m.configureInputForChat()
		return m, refreshChatSessionsCmd(m.ctx, m.session.store)
	}
	m.configureInputForChat()
	return m, tea.Batch(
		waitCmd,
		refreshChatSessionsCmd(m.ctx, m.session.store),
	)
}

func (m *sessionModel) startNewChat() (tea.Model, tea.Cmd) {
	if m.session.store == nil {
		return m, nil
	}
	sessionID, err := m.session.createChatSession(m.ctx, "New Chat")
	if err != nil {
		return m, nil
	}
	return m.openChatSession(sessionID)
}

func (m *sessionModel) deleteSelectedChatFromOverlay() (tea.Model, tea.Cmd) {
	if m.session.store == nil || m.selectedChatIndex <= 0 || m.selectedChatIndex-1 >= len(m.chatSessions) {
		return m, nil
	}
	session := m.chatSessions[m.selectedChatIndex-1]
	if session.ID <= 0 {
		return m, nil
	}
	if err := m.session.store.DeleteSession(m.ctx, session.ID); err != nil {
		state := m.currentChatState()
		if state != nil {
			m.appendChatMessage(state.ID, chatstore.MessageRecord{
				SessionID: state.ID,
				Timestamp: m.session.now(),
				Source:    "system",
				Kind:      "status",
				Output:    fmt.Sprintf("Failed to delete chat: %s", err),
				Success:   false,
			})
		}
		return m, nil
	}
	delete(m.chatState, session.ID)
	if m.liveChatID == session.ID {
		if m.shell != nil {
			_ = m.shell.Close()
			m.shell = nil
		}
		m.liveChatID = 0
	}
	if m.selectedChatID == session.ID {
		m.selectedChatID = 0
	}
	m.chatSessions = removeChatSessionByID(m.chatSessions, session.ID)
	if len(m.chatSessions) == 0 {
		m.selectedChatIndex = 0
		m.chatOverlayOpen = true
		m.refreshChatOverlay()
		m.refreshChatTranscript()
		m.configureInputForChat()
		return m, refreshChatSessionsCmd(m.ctx, m.session.store)
	}
	if m.selectedChatIndex > len(m.chatSessions) {
		m.selectedChatIndex = len(m.chatSessions)
	}
	if m.selectedChatIndex <= 0 {
		m.selectedChatIndex = 1
	}
	return m.openChatSession(m.chatSessions[m.selectedChatIndex-1].ID)
}

func removeChatSessionByID(sessions []chatstore.PersistedSession, sessionID int64) []chatstore.PersistedSession {
	filtered := sessions[:0]
	for _, session := range sessions {
		if session.ID != sessionID {
			filtered = append(filtered, session)
		}
	}
	return filtered
}

func (m *sessionModel) handleChatShellOutput(msg chatShellOutputMsg) {
	if msg.Err != nil {
		m.appendChatMessage(msg.SessionID, chatstore.MessageRecord{
			SessionID: msg.SessionID,
			Timestamp: m.session.now(),
			Source:    "system",
			Kind:      "status",
			Output:    fmt.Sprintf("Shell error: %s", msg.Err),
			Success:   false,
		})
		return
	}
	if _, ok := m.chatState[msg.SessionID]; !ok {
		return
	}
	text := strings.ReplaceAll(msg.Text, "\r", "")
	if text == "" {
		return
	}
	m.appendShellOutputFromPTY(msg.SessionID, text)
}

func (m *sessionModel) handleChatProgress(msg chatProgressMsg) {
	state := m.currentChatState()
	if state == nil {
		return
	}
	if msg.Done {
		state.TopsStatus = topsStatusIdle
		state.Waiting = false
		state.ShellPaused = false
		return
	}
	switch strings.TrimSpace(msg.Phase) {
	case "tools":
		state.TopsStatus = "running tool"
	case "provider", "planning", "rendering":
		state.TopsStatus = topsStatusThinking
	default:
		state.TopsStatus = topsStatusThinking
	}
}

func (m *sessionModel) handleChatStream(msg chatStreamMsg) {
	if strings.TrimSpace(msg.Text) == "" {
		return
	}
	state := m.currentChatState()
	if state == nil {
		return
	}
	label := "TOPS is thinking..."
	if msg.Kind == "answering" {
		label = "TOPS is responding..."
	}
	m.appendChatMessage(state.ID, chatstore.MessageRecord{
		SessionID: state.ID,
		Timestamp: m.session.now(),
		Source:    "system",
		Kind:      "status",
		Output:    label,
		Success:   true,
	})
}

func (m *sessionModel) handleChatWorkflow(msg chatWorkflowMsg) {
	state := m.currentChatState()
	if state == nil {
		return
	}
	output := ""
	source := "system"
	switch msg.Kind {
	case "action_started":
		output = fmt.Sprintf("%s", strings.TrimSpace(msg.CommandLine))
		source = "action"
	case "permission_requested":
		output = fmt.Sprintf("Approve %s?", strings.TrimSpace(msg.CommandLine))
		source = "approval"
	case "permission_decision":
		if msg.Approved {
			output = "Approved."
		} else {
			output = "Denied."
		}
		source = "approval"
	case "action_completed":
		output = fmt.Sprintf("Completed with exit code %d.", msg.ExitCode)
		if strings.TrimSpace(msg.ErrText) != "" {
			output = fmt.Sprintf("Completed with error: %s", strings.TrimSpace(msg.ErrText))
		}
		source = "action"
	}
	if output == "" {
		return
	}
	m.appendChatMessage(state.ID, chatstore.MessageRecord{
		SessionID: state.ID,
		Timestamp: m.session.now(),
		Source:    source,
		Kind:      msg.Kind,
		Output:    output,
		Success:   msg.ErrText == "",
		ErrorText: msg.ErrText,
	})
}

func (m *sessionModel) handleChatApprovalRequest(msg chatApprovalRequestMsg) {
	state := m.currentChatState()
	if state == nil {
		return
	}
	state.Approval = &msg.Request
	state.Focus = chatFocusApproval
	state.TopsStatus = topsStatusWaitingApproval
	state.Waiting = true
	state.ShellPaused = true
	state.Draft = ""
	m.input.SetValue("")
	m.input.Prompt = "Approve? "
	m.input.Placeholder = "y / N"
}

func (m *sessionModel) handleChatTurnDone(msg chatTurnDoneMsg) {
	state, ok := m.chatState[msg.SessionID]
	if !ok {
		return
	}
	state.TopsStatus = topsStatusIdle
	state.Waiting = false
	state.ShellPaused = false
	state.Focus = chatFocusTOPS
	state.Draft = nextDraftForMode(state.StickyMode)
	m.configureInputForChat()
	if msg.Err != nil {
		m.appendChatMessage(msg.SessionID, chatstore.MessageRecord{
			SessionID: msg.SessionID,
			Timestamp: m.session.now(),
			Source:    "tops_agent",
			Kind:      "error",
			Mode:      string(msg.Mode),
			Output:    msg.Err.Error(),
			Success:   false,
			ErrorText: msg.Err.Error(),
		})
		return
	}
	m.appendChatMessage(msg.SessionID, chatstore.MessageRecord{
		SessionID: msg.SessionID,
		Timestamp: m.session.now(),
		Source:    "tops_agent",
		Kind:      "answer",
		Mode:      string(msg.Mode),
		Output:    msg.Output,
		Success:   true,
	})
}

func (m *sessionModel) currentChatState() *chatSessionState {
	if m.selectedChatID == 0 {
		return nil
	}
	return m.chatState[m.selectedChatID]
}

func (m *sessionModel) syncSelectedChat() {
	if m.selectedChatID == 0 {
		if m.liveChatID != 0 {
			m.selectedChatID = m.liveChatID
		} else if len(m.chatSessions) > 0 {
			m.selectedChatID = m.chatSessions[0].ID
		}
	}
	if m.chatOverlayOpen {
		m.syncOverlayIndexFromSelection()
	}
}

func (m *sessionModel) syncOverlayIndexFromSelection() {
	m.selectedChatIndex = 0
	for i, session := range m.chatSessions {
		if session.ID == m.selectedChatID {
			m.selectedChatIndex = i + 1
			return
		}
	}
}

func (m *sessionModel) refreshChatOverlay() {
	width := chatOverlayContentWidth(m)
	lines := []string{
		overlayTextLine("Chats", width, overlayLineOptions{Bold: true, Foreground: lipgloss.Color("252")}),
		overlayTextLine(strings.Repeat("─", width), width, overlayLineOptions{Foreground: lipgloss.Color("238")}),
		overlayTextLine("", width, overlayLineOptions{}),
		overlayItemLine(0 == m.selectedChatIndex, "New Chat", "", width),
	}
	for i, session := range m.chatSessions {
		title := strings.TrimSpace(session.Title)
		if title == "" {
			title = fmt.Sprintf("Chat %d", session.ID)
		}
		meta := ""
		if session.ID == m.selectedChatID {
			meta = "current"
		}
		lines = append(lines, overlayItemLine(i+1 == m.selectedChatIndex, truncateOverlayTitle(title, width-12), meta, width))
	}
	lines = append(
		lines,
		overlayTextLine("", width, overlayLineOptions{}),
		overlayTextLine(strings.Repeat("─", width), width, overlayLineOptions{Foreground: lipgloss.Color("238")}),
		overlayTextLine("Enter: Open  n: New  d: Delete  Esc: Close", width, overlayLineOptions{Foreground: lipgloss.Color("245")}),
	)
	m.chatOverlayVP.SetContent(strings.Join(lines, "\n"))
}

type overlayLineOptions struct {
	Bold       bool
	Foreground lipgloss.Color
	Background lipgloss.Color
}

func chatOverlayContentWidth(m *sessionModel) int {
	if m == nil {
		return 32
	}
	return max(32, m.chatOverlayVP.Width)
}

func overlayTextLine(text string, width int, opts overlayLineOptions) string {
	text = truncateOverlayTitle(text, width)
	text += strings.Repeat(" ", max(0, width-lipgloss.Width(text)))
	style := overlayBaseLineStyle(width)
	if opts.Foreground != "" {
		style = style.Foreground(opts.Foreground)
	}
	if opts.Background != "" {
		style = style.Background(opts.Background)
	}
	if opts.Bold {
		style = style.Bold(true)
	}
	return style.Render(text)
}

func overlayItemLine(selected bool, label string, meta string, width int) string {
	prefix := "  "
	if selected {
		prefix = "▶ "
	}
	text := prefix + label
	if meta != "" {
		padding := max(1, width-lipgloss.Width(text)-lipgloss.Width(meta)-3)
		text += strings.Repeat(" ", padding) + "(" + meta + ")"
	}
	text += strings.Repeat(" ", max(0, width-lipgloss.Width(text)))
	base := overlayBaseLineStyle(width).Foreground(lipgloss.Color("245"))
	if selected {
		base = base.
			Bold(true).
			Foreground(lipgloss.Color("230")).
			Background(lipgloss.Color("63"))
	}
	return base.Render(text)
}

func overlayBaseLineStyle(width int) lipgloss.Style {
	return lipgloss.NewStyle().
		Width(width).
		Foreground(lipgloss.Color("245")).
		Background(lipgloss.Color("235"))
}

func truncateOverlayTitle(title string, width int) string {
	title = strings.TrimSpace(title)
	if width <= 1 || lipgloss.Width(title) <= width {
		return title
	}
	runes := []rune(title)
	if len(runes) <= width {
		return title
	}
	return string(runes[:max(1, width-1)]) + "…"
}

func (m *sessionModel) refreshChatTranscript() {
	state := m.currentChatState()
	if state == nil {
		m.chatViewport.SetContent("No active chat.\nPress Ctrl+O to open chats and create a new one.")
		return
	}
	m.chatViewport.SetContent(renderChatTranscript(state.Transcript))
	m.chatViewport.GotoBottom()
}

func (m *sessionModel) loadTranscriptForSelectedChat() error {
	if m.session.store == nil || m.selectedChatID == 0 {
		m.refreshChatTranscript()
		return nil
	}
	messages, err := m.session.store.ListMessagesBySession(m.ctx, m.selectedChatID, 1000)
	if err != nil {
		return err
	}
	state, ok := m.chatState[m.selectedChatID]
	if !ok {
		state = &chatSessionState{
			ID:         m.selectedChatID,
			StickyMode: model.ModeAsk,
			Draft:      "ask ",
			TopsStatus: topsStatusIdle,
			Focus:      chatFocusTOPS,
		}
		m.chatState[m.selectedChatID] = state
	}
	state.Transcript = messages
	state.TitleAssigned = len(messages) > 0
	m.refreshChatTranscript()
	return nil
}

func (m *sessionModel) appendChatMessage(sessionID int64, record chatstore.MessageRecord) {
	switch record.Source {
	case "shell_output", "shell_user":
		m.debugf("blocked generic shell transcript append source=%q kind=%q output=%q", record.Source, record.Kind, record.Output)
		return
	}
	m.appendTranscriptRecord(sessionID, record)
}

func (m *sessionModel) appendShellCommandFromSubmit(sessionID int64, command string) {
	command = strings.TrimSpace(command)
	if command == "" {
		return
	}
	m.appendTranscriptRecord(sessionID, chatstore.MessageRecord{
		SessionID: sessionID,
		Timestamp: m.session.now(),
		Source:    "shell_user",
		RawInput:  command,
		Kind:      "command",
		Output:    command,
		Success:   true,
	})
}

func (m *sessionModel) appendShellOutputFromPTY(sessionID int64, output string) {
	if output == "" {
		return
	}
	state, ok := m.chatState[sessionID]
	if !ok {
		return
	}
	output = m.consumeShellPTYOutput(state, output)
	if output == "" {
		return
	}
	m.appendTranscriptRecord(sessionID, chatstore.MessageRecord{
		SessionID: sessionID,
		Timestamp: m.session.now(),
		Source:    "shell_output",
		Kind:      "output",
		Output:    output,
		Success:   true,
	})
}

func (m *sessionModel) consumeShellPTYOutput(state *chatSessionState, chunk string) string {
	state.ShellPTYBuffer += chunk
	if !strings.Contains(state.ShellPTYBuffer, "\n") {
		return ""
	}
	parts := strings.SplitAfter(state.ShellPTYBuffer, "\n")
	state.ShellPTYBuffer = ""
	if len(parts) > 0 && !strings.HasSuffix(parts[len(parts)-1], "\n") {
		state.ShellPTYBuffer = parts[len(parts)-1]
		parts = parts[:len(parts)-1]
	}
	var out strings.Builder
	for _, line := range parts {
		if line == "" {
			continue
		}
		if len(state.ShellEchoQueue) > 0 && isSubmittedCommandEcho(line, state.ShellEchoQueue[0]) {
			state.ShellEchoQueue = state.ShellEchoQueue[1:]
			continue
		}
		out.WriteString(line)
	}
	return out.String()
}

func isSubmittedCommandEcho(line string, command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	trimmed := strings.TrimSpace(strings.ReplaceAll(line, "\r", ""))
	if trimmed == "" {
		return false
	}
	return trimmed == command || strings.HasSuffix(trimmed, " "+command)
}

func (m *sessionModel) appendTranscriptRecord(sessionID int64, record chatstore.MessageRecord) {
	state, ok := m.chatState[sessionID]
	if !ok {
		return
	}
	record.Timestamp = m.session.now()
	state.Transcript = append(state.Transcript, chatstore.PersistedMessage{
		SessionID: sessionID,
		Timestamp: record.Timestamp,
		Source:    record.Source,
		RawInput:  record.RawInput,
		Kind:      record.Kind,
		Mode:      record.Mode,
		Payload:   record.Payload,
		Output:    record.Output,
		Success:   record.Success,
		ErrorText: record.ErrorText,
	})
	if m.session.store != nil {
		_ = m.session.store.InsertMessage(m.ctx, record)
	}
	if sessionID == m.selectedChatID {
		m.refreshChatTranscript()
	}
}

func (m *sessionModel) configureInputForChat() {
	state := m.currentChatState()
	m.input.Focus()
	switch {
	case m.chatOverlayOpen:
		m.input.Prompt = ""
		m.input.Placeholder = ""
		m.input.SetValue("")
	case state == nil:
		m.input.Prompt = ">>> "
		m.input.Placeholder = "Press Ctrl+O to open chats"
		m.input.SetValue("")
	case state.Focus == chatFocusApproval:
		m.input.Prompt = "Approve? "
		m.input.Placeholder = "y / N"
		m.input.SetValue("")
	case state.Focus == chatFocusTOPS:
		m.input.Prompt = ">>> "
		m.input.Placeholder = "ask ... or gen ..."
		m.input.SetValue(state.Draft)
	default:
		m.input.Prompt = "$ "
		m.input.Placeholder = "Type a shell command and press Enter"
		m.input.SetValue(state.ShellDraft)
	}
}

func (m *sessionModel) toggleChatFocus() {
	state := m.currentChatState()
	if state == nil || state.Focus == chatFocusApproval {
		return
	}
	if state.Focus == chatFocusShell {
		state.Focus = chatFocusTOPS
	} else {
		state.Focus = chatFocusShell
	}
	m.configureInputForChat()
}

func (m sessionModel) renderChatView() string {
	state := m.currentChatState()
	title := "Current Chat"
	if state != nil && strings.TrimSpace(state.Title) != "" {
		title = state.Title
	}
	header := renderChatHeader(m)
	main := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("238")).
		Padding(0, 1).
		Width(m.chatViewport.Width + 2).
		Height(m.chatViewport.Height + 2).
		Render(renderPaneTitle(title) + "\n" + m.chatViewport.View())

	view := strings.Join([]string{
		header,
		main,
		renderChatFooter(m),
	}, "\n")

	if !m.chatOverlayOpen {
		return view
	}
	m.refreshChatOverlay()
	contentWidth := chatOverlayContentWidth(&m)
	contentHeight := max(1, m.chatOverlayVP.Height)
	content := lipgloss.NewStyle().
		Width(contentWidth).
		Height(contentHeight).
		Foreground(lipgloss.Color("245")).
		Background(lipgloss.Color("235")).
		Render(m.chatOverlayVP.View())
	overlay := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("69")).
		Background(lipgloss.Color("235")).
		Padding(0, 1).
		Width(contentWidth + 4).
		Height(contentHeight + 2).
		Render(content)
	return lipgloss.Place(
		m.width,
		m.height,
		lipgloss.Center,
		lipgloss.Center,
		overlay,
		lipgloss.WithWhitespaceChars("░"),
		lipgloss.WithWhitespaceForeground(lipgloss.Color("238")),
	)
}

func renderChatFooter(m sessionModel) string {
	state := m.currentChatState()
	label := "TOPS"
	color := lipgloss.Color("42")
	if state != nil {
		switch state.Focus {
		case chatFocusShell:
			label = "Shell"
			color = lipgloss.Color("69")
		case chatFocusApproval:
			label = "Approval"
			color = lipgloss.Color("214")
		}
	}
	if state != nil && state.ShellPaused && state.Focus == chatFocusShell {
		label = "Shell paused while TOPS is busy"
	}
	promptHint := inputHintForState(state)
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(color).
		Padding(0, 1).
		Render(lipgloss.NewStyle().Bold(true).Foreground(color).Render(label) + lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("  "+promptHint) + "\n" + m.input.View())
}

func renderChatHeader(m sessionModel) string {
	state := m.currentChatState()
	status := topsStatusLabel(state)
	focus := chatFocusLabel(state)
	statusColor := lipgloss.Color("245")
	switch status {
	case string(topsStatusThinking), "running tool":
		statusColor = lipgloss.Color("214")
	case string(topsStatusWaitingApproval):
		statusColor = lipgloss.Color("203")
	case string(topsStatusIdle):
		statusColor = lipgloss.Color("42")
	}
	meta := lipgloss.JoinHorizontal(
		lipgloss.Top,
		renderPill("Focus", focus, lipgloss.Color("69")),
		" ",
		renderPill("TOPS", status, statusColor),
	)
	help := lipgloss.NewStyle().
		Foreground(lipgloss.Color("245")).
		Render("Shift+Tab tabs  Tab focus  Ctrl+O chats  Esc close  Ctrl+C quit")
	return strings.Join([]string{
		renderTabs(m.activeTab),
		meta,
		help,
	}, "\n")
}

func renderPill(label string, value string, color lipgloss.Color) string {
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color("252")).
		Render(label+": ") +
		lipgloss.NewStyle().
			Bold(true).
			Foreground(color).
			Render(value)
}

func renderPaneTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Current Chat"
	}
	return lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("252")).
		Render(title)
}

func inputHintForState(state *chatSessionState) string {
	if state == nil {
		return "Ctrl+O to open chats"
	}
	switch state.Focus {
	case chatFocusShell:
		if state.ShellPaused {
			return "waiting for TOPS"
		}
		return "$ command, Enter runs"
	case chatFocusApproval:
		return "y approves, Enter denies"
	default:
		return "ask ... or gen ..."
	}
}

func renderTabs(active chatTab) string {
	inactiveStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("246")).
		Border(lipgloss.NormalBorder(), false, false, true, false).
		BorderForeground(lipgloss.Color("238")).
		Padding(0, 2)
	activeStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("230")).
		Background(lipgloss.Color("63")).
		Padding(0, 2)
	configLabel := inactiveStyle.Render("Config")
	chatsLabel := inactiveStyle.Render("Chats")
	if active == tabConfig {
		configLabel = activeStyle.Render("Config")
	} else {
		chatsLabel = activeStyle.Render("Chats")
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, configLabel, " ", chatsLabel)
}

func chatFocusLabel(state *chatSessionState) string {
	if state == nil {
		return "TOPS"
	}
	switch state.Focus {
	case chatFocusShell:
		return "Shell"
	case chatFocusApproval:
		return "Approval"
	default:
		return "TOPS"
	}
}

func topsStatusLabel(state *chatSessionState) string {
	if state == nil {
		return string(topsStatusIdle)
	}
	return string(state.TopsStatus)
}

func renderChatTranscript(messages []chatstore.PersistedMessage) string {
	if len(messages) == 0 {
		return lipgloss.NewStyle().
			Foreground(lipgloss.Color("245")).
			Render("No messages yet.\nPress Ctrl+O to create or select a chat.")
	}
	messages = coalesceShellOutputMessages(messages)
	var b strings.Builder
	for _, message := range messages {
		text := strings.TrimSpace(message.Output)
		if text == "" {
			text = strings.TrimSpace(message.RawInput)
		}
		if text == "" {
			continue
		}
		switch message.Source {
		case "shell_user":
			b.WriteString(renderShellCommand(text))
		case "shell_output":
			b.WriteString(renderShellOutput(text))
		case "tops_user":
			mode := strings.TrimSpace(message.Mode)
			if mode == "" {
				mode = "ask"
			}
			b.WriteString(renderTOPSInput(mode, text))
		case "tops_agent":
			b.WriteString(renderTOPSBlock(text))
		case "approval":
			b.WriteString(renderNotice("Approval", text, lipgloss.Color("214")))
		case "action":
			b.WriteString(renderNotice("Action", text, lipgloss.Color("69")))
		default:
			b.WriteString(renderNotice("Status", text, lipgloss.Color("245")))
		}
	}
	return strings.TrimSpace(b.String())
}

func coalesceShellOutputMessages(messages []chatstore.PersistedMessage) []chatstore.PersistedMessage {
	coalesced := make([]chatstore.PersistedMessage, 0, len(messages))
	for _, message := range messages {
		if message.Source == "shell_output" && len(coalesced) > 0 && coalesced[len(coalesced)-1].Source == "shell_output" {
			previous := &coalesced[len(coalesced)-1]
			previous.Output = appendTerminalChunk(previous.Output, message.Output)
			if previous.RawInput == "" {
				previous.RawInput = message.RawInput
			}
			continue
		}
		coalesced = append(coalesced, message)
	}
	return coalesced
}

func appendTerminalChunk(existing string, chunk string) string {
	if existing == "" {
		return chunk
	}
	if chunk == "" {
		return existing
	}
	if strings.HasPrefix(chunk, existing) {
		return chunk
	}
	return existing + chunk
}

func renderShellCommand(command string) string {
	command = strings.TrimRight(command, "\n")
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color("42")).
		Render("$ "+command) + "\n\n"
}

func renderShellOutput(output string) string {
	output = strings.TrimRight(output, "\n")
	if strings.TrimSpace(output) == "" {
		return ""
	}
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color("252")).
		Render(output) + "\n\n"
}

func renderTOPSInput(mode string, input string) string {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		mode = "ask"
	}
	line := fmt.Sprintf(">>> %s %s", mode, strings.TrimSpace(input))
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color("111")).
		Render(line) + "\n\n"
}

func renderTOPSBlock(output string) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return ""
	}
	body := indentBlock(output, "  ")
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder(), false, false, false, true).
		BorderForeground(lipgloss.Color("63")).
		PaddingLeft(1).
		Render(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("111")).Render("TOPS:")+"\n"+body) + "\n\n"
}

func renderNotice(label string, text string, color lipgloss.Color) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return lipgloss.NewStyle().
		Foreground(color).
		Render(label+": "+text) + "\n\n"
}

func indentBlock(input string, prefix string) string {
	lines := strings.Split(strings.TrimSpace(input), "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}

func deriveChatTitle(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return "New Chat"
	}
	runes := []rune(input)
	if len(runes) > 48 {
		return strings.TrimSpace(string(runes[:48])) + "..."
	}
	return input
}
