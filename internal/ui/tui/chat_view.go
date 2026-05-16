package tui

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
	"unicode"

	tea "github.com/phoenix-tui/phoenix/tea"
	"tops/internal/ui/tui/render"

	"tops/internal/model"
	"tops/internal/storage/chatstore"
)

type chatSessionsLoadedMsg struct {
	Sessions []chatstore.PersistedSession
	Err      error
}

type chatCopyEntry struct {
	Kind       string
	Label      string
	Preview    string
	Content    string
	GroupIndex int
	MessageIDs []int64
	RawIndexes []int
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
		m.syncInputForActiveSurface()
		return
	}
	m.activeTab = tabConfig
	m.syncInputForActiveSurface()
}

func (m *sessionModel) handleChatKey(msg tea.KeyMsg) (*sessionModel, tea.Cmd) {
	if isQuitKey(msg) {
		if m.shell != nil {
			_ = m.shell.Close()
		}
		return m, tea.Quit()
	}
	switch msg.Type {
	case tea.KeyEsc:
		if m.copyOverlayOpen {
			m.copyOverlayOpen = false
			m.copySelectedRows = map[int]struct{}{}
			m.configureInputForChat()
			return m, nil
		}
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
	if isCtrlRune(msg, 'k') {
		m.openCopyOverlay()
		return m, nil
	}
	if isCtrlRune(msg, 'e') {
		return m.exportCurrentChatTranscript()
	}
	if isCtrlRune(msg, 'o') {
		if m.copyOverlayOpen {
			m.copyOverlayOpen = false
			m.copySelectedRows = map[int]struct{}{}
		}
		if m.chatOverlayOpen {
			m.chatOverlayOpen = false
		} else {
			m.chatOverlayOpen = true
			m.syncOverlayIndexFromSelection()
		}
		m.configureInputForChat()
		return m, refreshChatSessionsCmd(m.ctx, m.session.store)
	}

	if m.copyOverlayOpen {
		return m.handleCopyOverlayKey(msg)
	}

	if m.chatOverlayOpen {
		return m.handleChatOverlayKey(msg)
	}

	state := m.currentChatState()
	if state != nil && state.Focus == chatFocusTOPS {
		if isBackspaceLike(msg) {
			if strings.TrimSpace(m.input.Value()) == "" && state.SelectedMode != topsInputModeUnset {
				state.SelectedMode = topsInputModeUnset
				state.Draft = ""
				m.configureInputForChat()
				return m, nil
			}
		}
		if state.SelectedMode == topsInputModeUnset {
			r, ok := keyRune(msg)
			if ok {
				if selected, ok := shortcutToMode(r); ok {
					state.SelectedMode = selected
					state.Draft = ""
					m.configureInputForChat()
					return m, nil
				}
				if !unicode.IsControl(r) {
					state.SelectedMode = topsInputModeAsk
					state.StickyMode = model.ModeAsk
					state.Draft = string(r)
					m.configureInputForChat()
					return m, nil
				}
			}
		}
	}
	switch msg.Type {
	case tea.KeyPgUp:
		m.chatViewport.LineUp(max(1, m.chatViewport.Height-2))
		return m, nil
	case tea.KeyPgDown:
		m.chatViewport.LineDown(max(1, m.chatViewport.Height-2))
		return m, nil
	case tea.KeyHome:
		m.chatViewport.GotoTop()
		return m, nil
	case tea.KeyEnd:
		m.chatViewport.GotoBottom()
		return m, nil
	case tea.KeyEnter:
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
		if state != nil && state.Focus == chatFocusShell && strings.TrimSpace(m.input.Value()) != "" {
			m.cycleShellHistory(-1)
			return m, nil
		}
		m.chatViewport.LineUp(1)
		return m, nil
	case tea.KeyDown:
		if state != nil && state.Focus == chatFocusShell && strings.TrimSpace(m.input.Value()) != "" {
			m.cycleShellHistory(1)
			return m, nil
		}
		m.chatViewport.LineDown(1)
		return m, nil
	}
	if isEnterLike(msg) {
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

func (m *sessionModel) handleChatMouse(msg tea.MouseMsg) (*sessionModel, tea.Cmd) {
	if m.copyOverlayOpen {
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			if m.copySelectedIndex > 0 {
				m.copySelectedIndex--
				m.refreshCopyOverlay()
			}
			return m, nil
		case tea.MouseButtonWheelDown:
			if m.copySelectedIndex < len(m.copyEntries)-1 {
				m.copySelectedIndex++
				m.refreshCopyOverlay()
			}
			return m, nil
		default:
			return m, nil
		}
	}
	if m.chatOverlayOpen {
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			if m.selectedChatIndex > 0 {
				m.selectedChatIndex--
			}
			return m, nil
		case tea.MouseButtonWheelDown:
			if m.selectedChatIndex < len(m.chatSessions) {
				m.selectedChatIndex++
			}
			return m, nil
		default:
			return m, nil
		}
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.chatViewport.LineUp(3)
	case tea.MouseButtonWheelDown:
		m.chatViewport.LineDown(3)
	}
	return m, nil
}

func (m *sessionModel) openCopyOverlay() {
	state := m.currentChatState()
	if state == nil {
		return
	}
	m.copyEntries = buildCopyEntries(state.Transcript)
	m.copySelectedIndex = 0
	m.copySelectedRows = map[int]struct{}{}
	m.copyOverlayOpen = true
	m.chatOverlayOpen = false
	m.refreshCopyOverlay()
	m.configureInputForChat()
}

func (m *sessionModel) handleCopyOverlayKey(msg tea.KeyMsg) (*sessionModel, tea.Cmd) {
	if len(m.copyEntries) == 0 {
		switch msg.Type {
		case tea.KeyEsc, tea.KeyEnter:
			m.copyOverlayOpen = false
			m.configureInputForChat()
		}
		return m, nil
	}
	switch msg.Type {
	case tea.KeyUp:
		if m.copySelectedIndex > 0 {
			m.copySelectedIndex--
			m.refreshCopyOverlay()
		}
	case tea.KeyDown:
		if m.copySelectedIndex < len(m.copyEntries)-1 {
			m.copySelectedIndex++
			m.refreshCopyOverlay()
		}
	case tea.KeySpace:
		m.toggleCopySelection(m.copySelectedIndex)
		m.refreshCopyOverlay()
	default:
		if r, ok := keyRune(msg); ok {
			switch strings.ToLower(string(r)) {
			case "k":
				if m.copySelectedIndex > 0 {
					m.copySelectedIndex--
					m.refreshCopyOverlay()
				}
			case "j":
				if m.copySelectedIndex < len(m.copyEntries)-1 {
					m.copySelectedIndex++
					m.refreshCopyOverlay()
				}
			case " ":
				m.toggleCopySelection(m.copySelectedIndex)
				m.refreshCopyOverlay()
			case "c":
				return m.copySelectedOverlayEntries()
			case "r":
				return m.removeSelectedOverlayEntries()
			}
		}
	case tea.KeyEnter:
		return m.goToSelectedOverlayEntry()
	case tea.KeyEsc:
		m.copyOverlayOpen = false
		m.copySelectedRows = map[int]struct{}{}
		m.configureInputForChat()
	}
	return m, nil
}

func (m *sessionModel) toggleCopySelection(index int) {
	if index < 0 || index >= len(m.copyEntries) {
		return
	}
	if m.copySelectedRows == nil {
		m.copySelectedRows = map[int]struct{}{}
	}
	if _, ok := m.copySelectedRows[index]; ok {
		delete(m.copySelectedRows, index)
		return
	}
	m.copySelectedRows[index] = struct{}{}
}

func (m *sessionModel) selectedOverlayIndexes() []int {
	indexes := make([]int, 0, len(m.copySelectedRows))
	for idx := range m.copySelectedRows {
		if idx >= 0 && idx < len(m.copyEntries) {
			indexes = append(indexes, idx)
		}
	}
	if len(indexes) == 0 && m.copySelectedIndex >= 0 && m.copySelectedIndex < len(m.copyEntries) {
		indexes = append(indexes, m.copySelectedIndex)
	}
	slices.Sort(indexes)
	return indexes
}

func (m *sessionModel) copySelectedOverlayEntries() (*sessionModel, tea.Cmd) {
	state := m.currentChatState()
	if state == nil || len(m.copyEntries) == 0 {
		return m, nil
	}
	indexes := m.selectedOverlayIndexes()
	if len(indexes) == 0 {
		return m, nil
	}
	selectedContents := make([]string, 0, len(indexes))
	for _, idx := range indexes {
		selectedContents = append(selectedContents, strings.TrimSpace(m.copyEntries[idx].Content))
	}
	entryContent := strings.TrimSpace(strings.Join(selectedContents, "\n\n"))
	if entryContent == "" {
		return m, nil
	}
	copyFn := m.copyToClipboard
	if copyFn == nil {
		copyFn = copyTextToClipboard
	}
	err := copyFn(entryContent)
	output := fmt.Sprintf("Copied %d message(s) to clipboard.", len(indexes))
	success := true
	if err != nil {
		output = fmt.Sprintf("Copy failed: %s", err)
		success = false
	}
	m.appendChatMessage(state.ID, chatstore.MessageRecord{
		SessionID: state.ID,
		Timestamp: m.session.now(),
		Source:    "system",
		Kind:      "status",
		Output:    output,
		Success:   success,
		ErrorText: errorString(err),
	})
	m.copyOverlayOpen = false
	m.copySelectedRows = map[int]struct{}{}
	m.configureInputForChat()
	return m, nil
}

func (m *sessionModel) goToSelectedOverlayEntry() (*sessionModel, tea.Cmd) {
	state := m.currentChatState()
	if state == nil || len(m.copyEntries) == 0 || m.copySelectedIndex < 0 || m.copySelectedIndex >= len(m.copyEntries) {
		return m, nil
	}
	target := m.copyEntries[m.copySelectedIndex]
	m.copyOverlayOpen = false
	m.copySelectedRows = map[int]struct{}{}
	m.configureInputForChat()
	m.scrollChatToGroup(state.Transcript, target.GroupIndex)
	return m, nil
}

func (m *sessionModel) removeSelectedOverlayEntries() (*sessionModel, tea.Cmd) {
	state := m.currentChatState()
	if state == nil || len(m.copyEntries) == 0 {
		return m, nil
	}
	indexes := m.selectedOverlayIndexes()
	if len(indexes) == 0 {
		return m, nil
	}
	rawIndexSet := map[int]struct{}{}
	messageIDSet := map[int64]struct{}{}
	for _, idx := range indexes {
		entry := m.copyEntries[idx]
		for _, rawIdx := range entry.RawIndexes {
			rawIndexSet[rawIdx] = struct{}{}
		}
		for _, id := range entry.MessageIDs {
			if id > 0 {
				messageIDSet[id] = struct{}{}
			}
		}
	}
	rawIndexes := make([]int, 0, len(rawIndexSet))
	for idx := range rawIndexSet {
		rawIndexes = append(rawIndexes, idx)
	}
	slices.Sort(rawIndexes)
	slices.Reverse(rawIndexes)
	for _, idx := range rawIndexes {
		if idx >= 0 && idx < len(state.Transcript) {
			state.Transcript = append(state.Transcript[:idx], state.Transcript[idx+1:]...)
		}
	}
	if m.session.store != nil && len(messageIDSet) > 0 {
		messageIDs := make([]int64, 0, len(messageIDSet))
		for id := range messageIDSet {
			messageIDs = append(messageIDs, id)
		}
		slices.Sort(messageIDs)
		if err := m.session.store.DeleteMessages(m.ctx, state.ID, messageIDs); err != nil {
			m.appendChatMessage(state.ID, chatstore.MessageRecord{
				SessionID: state.ID,
				Timestamp: m.session.now(),
				Source:    "system",
				Kind:      "status",
				Output:    fmt.Sprintf("Failed to remove selected messages: %s", err),
				Success:   false,
				ErrorText: err.Error(),
			})
			return m, nil
		}
	}
	m.copyOverlayOpen = false
	m.copySelectedRows = map[int]struct{}{}
	m.refreshChatTranscript()
	m.configureInputForChat()
	m.appendChatMessage(state.ID, chatstore.MessageRecord{
		SessionID: state.ID,
		Timestamp: m.session.now(),
		Source:    "system",
		Kind:      "status",
		Output:    fmt.Sprintf("Removed %d message(s).", len(indexes)),
		Success:   true,
	})
	return m, nil
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (m *sessionModel) exportCurrentChatTranscript() (*sessionModel, tea.Cmd) {
	state := m.currentChatState()
	if state == nil {
		return m, nil
	}
	plain := renderChatTranscriptPlain(state.Transcript)
	file, err := os.CreateTemp("", fmt.Sprintf("tops-chat-%d-*.txt", state.ID))
	if err != nil {
		m.appendChatMessage(state.ID, chatstore.MessageRecord{
			SessionID: state.ID,
			Timestamp: m.session.now(),
			Source:    "system",
			Kind:      "status",
			Output:    fmt.Sprintf("Failed to export transcript: %s", err),
			Success:   false,
			ErrorText: err.Error(),
		})
		return m, nil
	}
	defer file.Close()
	if _, err := file.WriteString(plain); err != nil {
		m.appendChatMessage(state.ID, chatstore.MessageRecord{
			SessionID: state.ID,
			Timestamp: m.session.now(),
			Source:    "system",
			Kind:      "status",
			Output:    fmt.Sprintf("Failed to write transcript export: %s", err),
			Success:   false,
			ErrorText: err.Error(),
		})
		return m, nil
	}
	m.appendChatMessage(state.ID, chatstore.MessageRecord{
		SessionID: state.ID,
		Timestamp: m.session.now(),
		Source:    "system",
		Kind:      "status",
		Output:    fmt.Sprintf("Transcript exported: %s", file.Name()),
		Success:   true,
	})
	return m, nil
}

func (m *sessionModel) handleChatOverlayKey(msg tea.KeyMsg) (*sessionModel, tea.Cmd) {
	switch msg.Type {
	case tea.KeyUp:
		if m.selectedChatIndex > 0 {
			m.selectedChatIndex--
		}
	case tea.KeyDown:
		if m.selectedChatIndex < len(m.chatSessions) {
			m.selectedChatIndex++
		}
	default:
		if r, ok := keyRune(msg); ok {
			switch strings.ToLower(string(r)) {
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

func (m *sessionModel) submitShellDraft() (*sessionModel, tea.Cmd) {
	state := m.currentChatState()
	if state == nil {
		return m, nil
	}
	if state.ShellPaused || state.Focus == chatFocusApproval {
		return m, nil
	}
	command := strings.TrimRight(m.input.Value(), "\r\n")
	state.ShellDraft = command
	state.ShellDraft = ""
	m.input.SetValue("")
	if command != "" {
		state.ShellHistory = append(state.ShellHistory, command)
		state.ShellHistPos = len(state.ShellHistory)
		state.ShellEchoQueue = append(state.ShellEchoQueue, command)
		m.appendShellCommandFromSubmit(state.ID, command)
	}
	data := []byte(command + "\r")
	waitCmd, ok := m.ensureShellForChat(state)
	if !ok {
		return m, nil
	}
	if waitCmd != nil {
		return m, writeShellThenWaitCmd(m.shell, state.ID, data)
	}
	return m, writeShellCmd(m.shell, data)
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

func writeShellCmd(shell ShellController, data []byte) tea.Cmd {
	return func() tea.Msg {
		if shell == nil {
			return nil
		}
		_ = shell.Write(data)
		return nil
	}
}

func writeShellThenWaitCmd(shell ShellController, sessionID int64, data []byte) tea.Cmd {
	return func() tea.Msg {
		if shell == nil {
			return nil
		}
		if err := shell.Write(data); err != nil {
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
		return nil, false
	}
	shellName := ""
	if m.runtime != nil {
		shellName = m.runtime.Config.Shell
	}
	if err := controller.Start(m.ctx, shellName, m.chatViewport.Width, m.chatViewport.Height); err != nil {
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

func (m *sessionModel) submitChatDraft() (*sessionModel, tea.Cmd) {
	state := m.currentChatState()
	if state == nil || m.runtime == nil {
		return m, nil
	}
	selected := state.SelectedMode
	rawDraft := strings.TrimSpace(m.input.Value())
	var (
		mode        model.Mode
		displayMode string
		input       string
	)
	switch selected {
	case topsInputModeBye:
		if rawDraft != "" {
			m.appendChatMessage(state.ID, chatstore.MessageRecord{
				SessionID: state.ID,
				Timestamp: m.session.now(),
				Source:    "system",
				Kind:      "status",
				Output:    "BYE mode does not take text. Press Enter to unload the local model.",
				Success:   false,
			})
			state.Draft = ""
			m.configureInputForChat()
			return m, nil
		}
		mode = model.ModeAsk
		displayMode = "bye"
		input = "bye"
	case topsInputModeAsk, topsInputModeGen, topsInputModeHelp:
		if rawDraft == "" {
			return m, nil
		}
		switch selected {
		case topsInputModeAsk:
			mode = model.ModeAsk
			displayMode = "ask"
		case topsInputModeGen:
			mode = model.ModeGen
			displayMode = "gen"
		default:
			mode = model.ModeHelp
			displayMode = "help"
		}
		input = rawDraft
		state.StickyMode = mode
	default:
		m.appendChatMessage(state.ID, chatstore.MessageRecord{
			SessionID: state.ID,
			Timestamp: m.session.now(),
			Source:    "system",
			Kind:      "status",
			Output:    "Select a mode first: A=ASK, G=GEN, H=HELP, B=BYE.",
			Success:   false,
		})
		m.configureInputForChat()
		return m, nil
	}
	state.Draft = nextDraftForMode(state.StickyMode)
	state.TopsStatus = topsStatusThinking
	state.Waiting = true
	state.ShellPaused = true
	state.TurnStartedAt = time.Time{}
	state.TurnPausedAt = time.Time{}
	state.TurnPausedFor = 0
	state.TurnHadAnswer = false
	rawInput := strings.TrimSpace(fmt.Sprintf("%s %s", displayMode, input))
	m.appendChatMessage(state.ID, chatstore.MessageRecord{
		SessionID: state.ID,
		Timestamp: m.session.now(),
		Source:    "tops_user",
		RawInput:  rawInput,
		Kind:      displayMode,
		Mode:      displayMode,
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

func (m *sessionModel) submitApprovalResponse() (*sessionModel, tea.Cmd) {
	state := m.currentChatState()
	if state == nil || state.Approval == nil {
		return m, nil
	}
	answer := strings.ToLower(strings.TrimSpace(m.input.Value()))
	approved := answer == "y" || answer == "yes"
	state.Approval.Response <- approved
	m.resumeTurnTimer(state)
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

func (m *sessionModel) openChatSession(sessionID int64) (*sessionModel, tea.Cmd) {
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
			ID:           sessionID,
			Title:        title,
			Live:         true,
			StickyMode:   model.ModeAsk,
			SelectedMode: topsInputModeAsk,
			Draft:        "",
			TopsStatus:   topsStatusIdle,
			Focus:        chatFocusTOPS,
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

func (m *sessionModel) startNewChat() (*sessionModel, tea.Cmd) {
	if m.session.store == nil {
		return m, nil
	}
	sessionID, err := m.session.createChatSession(m.ctx, "New Chat")
	if err != nil {
		return m, nil
	}
	return m.openChatSession(sessionID)
}

func (m *sessionModel) deleteSelectedChatFromOverlay() (*sessionModel, tea.Cmd) {
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
	m.startTurnTimer(state)
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
	state, ok := m.chatState[msg.SessionID]
	if !ok {
		return
	}
	m.startTurnTimer(state)
	if strings.TrimSpace(msg.Kind) == "status" {
		m.appendChatMessage(msg.SessionID, chatstore.MessageRecord{
			SessionID: msg.SessionID,
			Timestamp: m.session.now(),
			Source:    "system",
			Kind:      "status",
			Output:    strings.TrimSpace(msg.Text),
			Success:   true,
		})
		return
	}
	if strings.TrimSpace(msg.Kind) == "answering" {
		state.TurnHadAnswer = true
	}
	m.appendChatMessage(msg.SessionID, chatstore.MessageRecord{
		SessionID: msg.SessionID,
		Timestamp: m.session.now(),
		Source:    "tops_stream",
		Kind:      strings.TrimSpace(msg.Kind),
		Output:    msg.Text,
		Success:   true,
	})
}

func (m *sessionModel) handleChatWorkflow(msg chatWorkflowMsg) {
	if _, ok := m.chatState[msg.SessionID]; !ok {
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
	m.appendChatMessage(msg.SessionID, chatstore.MessageRecord{
		SessionID: msg.SessionID,
		Timestamp: m.session.now(),
		Source:    source,
		Kind:      msg.Kind,
		Output:    output,
		Success:   msg.ErrText == "",
		ErrorText: msg.ErrText,
	})
	if msg.Kind == "action_completed" {
		m.appendChatMessage(msg.SessionID, chatstore.MessageRecord{
			SessionID: msg.SessionID,
			Timestamp: m.session.now(),
			Source:    "system",
			Kind:      "status",
			Output:    "Tool execution duration: " + formatDurationMMSS(msg.Duration),
			Success:   strings.TrimSpace(msg.ErrText) == "",
		})
	}
}

func (m *sessionModel) handleChatApprovalRequest(msg chatApprovalRequestMsg) {
	state := m.currentChatState()
	if state == nil {
		return
	}
	m.startTurnTimer(state)
	m.pauseTurnTimer(state)
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
	state.SelectedMode = modeToInputMode(state.StickyMode)
	m.configureInputForChat()
	elapsed, paused := m.finishTurnTimer(state)
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
		if elapsed > 0 {
			m.appendChatMessage(msg.SessionID, chatstore.MessageRecord{
				SessionID: msg.SessionID,
				Timestamp: m.session.now(),
				Source:    "system",
				Kind:      "status",
				Output:    formatTurnDurationLine(elapsed, paused),
				Success:   true,
			})
		}
		state.TurnHadAnswer = false
		return
	}
	if !state.TurnHadAnswer && !hasTrailingAnswerStream(state.Transcript, msg.Output) {
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
	state.TurnHadAnswer = false
	if elapsed > 0 {
		m.appendChatMessage(msg.SessionID, chatstore.MessageRecord{
			SessionID: msg.SessionID,
			Timestamp: m.session.now(),
			Source:    "system",
			Kind:      "status",
			Output:    formatTurnDurationLine(elapsed, paused),
			Success:   true,
		})
	}
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
		overlayTextLine("Chats", width, overlayLineOptions{Bold: true, Foreground: render.Color("252")}),
		overlayTextLine(strings.Repeat("─", width), width, overlayLineOptions{Foreground: render.Color("238")}),
		overlayTextLine("", width, overlayLineOptions{}),
		overlayItemLine(0 == m.selectedChatIndex, "New Chat", "", width, render.Color("245")),
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
		lines = append(lines, overlayItemLine(i+1 == m.selectedChatIndex, truncateOverlayTitle(title, width-12), meta, width, render.Color("245")))
	}
	lines = append(
		lines,
		overlayTextLine("", width, overlayLineOptions{}),
		overlayTextLine(strings.Repeat("─", width), width, overlayLineOptions{Foreground: render.Color("238")}),
		overlayTextLine("Enter: Open  n: New  d: Delete  Esc: Close", width, overlayLineOptions{Foreground: render.Color("245")}),
	)
	m.chatOverlayVP.SetContent(strings.Join(lines, "\n"))
}

func (m *sessionModel) refreshCopyOverlay() {
	width := max(36, m.copyOverlayVP.Width)
	selectedLine := -1
	currentLine := 0
	lines := []string{
		overlayTextLine("Messages", width, overlayLineOptions{Bold: true, Foreground: render.Color("252")}),
		overlayTextLine(strings.Repeat("─", width), width, overlayLineOptions{Foreground: render.Color("238")}),
	}
	currentLine += len(lines)
	if len(m.copyEntries) == 0 {
		lines = append(lines,
			overlayTextLine("", width, overlayLineOptions{}),
			overlayTextLine("No messages in this chat yet.", width, overlayLineOptions{Foreground: render.Color("245")}),
		)
		currentLine += 2
	} else {
		lines = append(lines, overlayTextLine("", width, overlayLineOptions{}))
		currentLine++
		for i, entry := range m.copyEntries {
			label := fmt.Sprintf("%d. %s", i+1, entry.Label)
			if _, ok := m.copySelectedRows[i]; ok {
				label = "[x] " + label
			} else {
				label = "[ ] " + label
			}
			isSelected := i == m.copySelectedIndex
			if isSelected {
				selectedLine = currentLine
			}
			lines = append(lines, overlayItemLine(isSelected, truncateOverlayTitle(label, width), "", width, copyEntryColor(entry.Kind)))
			currentLine++
			if strings.TrimSpace(entry.Preview) != "" {
				preview := "   " + truncateOverlayTitle(entry.Preview, max(8, width-3))
				lines = append(lines, overlayTextLine(preview, width, overlayLineOptions{Foreground: render.Color("245")}))
				currentLine++
			}
		}
	}
	lines = append(lines,
		overlayTextLine("", width, overlayLineOptions{}),
		overlayTextLine(strings.Repeat("─", width), width, overlayLineOptions{Foreground: render.Color("238")}),
		overlayTextLine("Enter: Go  Space: Select  c: Copy  r: Remove  Esc: Close", width, overlayLineOptions{Foreground: render.Color("245")}),
	)
	m.copyOverlayVP.SetContent(strings.Join(lines, "\n"))
	m.ensureCopySelectionVisible(selectedLine)
}

type overlayLineOptions struct {
	Bold       bool
	Foreground render.Color
	Background render.Color
}

func chatOverlayContentWidth(m *sessionModel) int {
	if m == nil {
		return 32
	}
	return max(32, m.chatOverlayVP.Width)
}

func overlayTextLine(text string, width int, opts overlayLineOptions) string {
	text = truncateOverlayTitle(text, width)
	text += strings.Repeat(" ", max(0, width-render.Width(text)))
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

func overlayItemLine(selected bool, label string, meta string, width int, fg render.Color) string {
	prefix := "  "
	if selected {
		prefix = "▶ "
	}
	text := prefix + label
	if meta != "" {
		padding := max(1, width-render.Width(text)-render.Width(meta)-3)
		text += strings.Repeat(" ", padding) + "(" + meta + ")"
	}
	text += strings.Repeat(" ", max(0, width-render.Width(text)))
	if fg == "" {
		fg = render.Color("245")
	}
	base := overlayBaseLineStyle(width).Foreground(fg)
	if selected {
		base = base.
			Bold(true).
			Foreground(render.Color("230")).
			Background(render.Color("63"))
	}
	return base.Render(text)
}

func copyEntryColor(kind string) render.Color {
	switch strings.TrimSpace(kind) {
	case "tops_query":
		return render.Color("111")
	case "action":
		return render.Color("69")
	case "approval":
		return render.Color("214")
	case "tops_answer":
		return render.Color("117")
	case "shell_command":
		return render.Color("42")
	case "shell_output":
		return render.Color("252")
	default:
		return render.Color("245")
	}
}

func (m *sessionModel) ensureCopySelectionVisible(selectedLine int) {
	if selectedLine < 0 || m.copyOverlayVP.Height <= 0 {
		return
	}
	top := m.copyOverlayVP.YOffset
	bottom := top + m.copyOverlayVP.Height - 1
	switch {
	case selectedLine < top:
		m.copyOverlayVP.YOffset = selectedLine
	case selectedLine > bottom:
		m.copyOverlayVP.YOffset = selectedLine - m.copyOverlayVP.Height + 1
	}
	if m.copyOverlayVP.YOffset < 0 {
		m.copyOverlayVP.YOffset = 0
	}
}

func overlayBaseLineStyle(width int) render.Style {
	return render.NewStyle().
		Width(width).
		Foreground(render.Color("245")).
		Background(render.Color("235"))
}

func truncateOverlayTitle(title string, width int) string {
	title = strings.TrimSpace(title)
	if width <= 1 || render.Width(title) <= width {
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
	m.chatViewport.SetContent(renderChatTranscript(state.Transcript, m.chatViewport.Width))
	m.chatViewport.GotoBottom()
}

func (m *sessionModel) loadTranscriptForSelectedChat() error {
	if m.session.store == nil || m.selectedChatID == 0 {
		m.refreshChatTranscript()
		return nil
	}
	messages, err := m.session.store.ListMessagesBySession(m.ctx, m.selectedChatID, 5000)
	if err != nil {
		return err
	}
	state, ok := m.chatState[m.selectedChatID]
	if !ok {
		state = &chatSessionState{
			ID:           m.selectedChatID,
			StickyMode:   model.ModeAsk,
			SelectedMode: topsInputModeAsk,
			Draft:        "",
			TopsStatus:   topsStatusIdle,
			Focus:        chatFocusTOPS,
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
	case m.chatOverlayOpen || m.copyOverlayOpen:
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
		normalizeLegacyChatDraft(state)
		m.input.Prompt = modePromptBlockForInputMode(state.SelectedMode)
		if state.SelectedMode == topsInputModeUnset {
			m.input.Placeholder = "Select mode: A=ASK, G=GEN, H=HELP, B=BYE"
		} else if state.SelectedMode == topsInputModeBye {
			m.input.Placeholder = "Press Enter to unload local model"
		} else {
			m.input.Placeholder = "Type message and press Enter"
		}
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

func (m sessionModel) renderChatBody() string {
	state := m.currentChatState()
	title := "Current Chat"
	if state != nil && strings.TrimSpace(state.Title) != "" {
		title = state.Title
	}
	main := render.NewStyle().
		Border(render.RoundedBorder()).
		BorderForeground(render.Color("238")).
		Padding(0, 1).
		Width(m.chatViewport.Width + 2).
		Height(m.chatViewport.Height + 2).
		Render(renderPaneTitle(title) + "\n" + m.chatViewport.View())

	if !m.copyOverlayOpen && !m.chatOverlayOpen {
		return main
	}
	contentWidth := chatOverlayContentWidth(&m)
	contentHeight := max(1, m.chatOverlayVP.Height)
	contentView := ""
	if m.copyOverlayOpen {
		m.refreshCopyOverlay()
		contentWidth = max(36, m.copyOverlayVP.Width)
		contentHeight = max(1, m.copyOverlayVP.Height)
		contentView = m.copyOverlayVP.View()
	} else {
		m.refreshChatOverlay()
		contentView = m.chatOverlayVP.View()
	}
	content := render.NewStyle().
		Width(contentWidth).
		Height(contentHeight).
		Foreground(render.Color("245")).
		Background(render.Color("235")).
		Render(contentView)
	overlay := render.NewStyle().
		Border(render.RoundedBorder()).
		BorderForeground(render.Color("69")).
		Background(render.Color("235")).
		Padding(0, 1).
		Width(contentWidth + 4).
		Height(contentHeight + 2).
		Render(content)
	return render.Place(
		m.width,
		m.chatViewport.Height+2,
		render.Center,
		render.Center,
		overlay,
		render.WithWhitespaceChars("░"),
		render.WithWhitespaceForeground(render.Color("238")),
	)
}

func renderChatFooter(m sessionModel) string {
	state := m.currentChatState()
	label := "TOPS"
	color := render.Color("42")
	if state != nil {
		switch state.Focus {
		case chatFocusShell:
			label = "Shell"
			color = render.Color("69")
		case chatFocusApproval:
			label = "Approval"
			color = render.Color("214")
		}
	}
	if state != nil && state.ShellPaused && state.Focus == chatFocusShell {
		label = "Shell paused while TOPS is busy"
	}
	promptHint := inputHintForState(state)
	intelligenceLabel := "Auto"
	if m.runtime != nil {
		switch model.NormalizeIntelligenceMode(string(m.runtime.IntelligenceMode)) {
		case model.IntelligenceModeBlitz:
			intelligenceLabel = "Blitz"
		case model.IntelligenceModeGrounded:
			intelligenceLabel = "Grounded"
		default:
			intelligenceLabel = "Auto"
		}
	}
	return render.NewStyle().
		Border(render.RoundedBorder()).
		BorderForeground(color).
		Width(max(1, m.width-4)).
		Padding(0, 1).
		Render(
			render.NewStyle().Bold(true).Foreground(color).Render(label) +
				render.NewStyle().Foreground(render.Color("245")).Render("  "+promptHint+"  Intelligence: "+intelligenceLabel) +
				"\n" + m.input.View(),
		)
}

func renderPill(label string, value string, color render.Color) string {
	return render.NewStyle().
		Foreground(render.Color("252")).
		Render(label+": ") +
		render.NewStyle().
			Bold(true).
			Foreground(color).
			Render(value)
}

func renderPaneTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Current Chat"
	}
	return render.NewStyle().
		Bold(true).
		Foreground(render.Color("252")).
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
		return "$ command, Enter runs, ↑/↓ Pg scroll"
	case chatFocusApproval:
		return "y approves, Enter denies"
	default:
		if state.SelectedMode == topsInputModeUnset {
			return "select mode: A/G/H/B"
		}
		if state.SelectedMode == topsInputModeBye {
			return "press Enter to unload model"
		}
		return "type message, Backspace clears mode, ↑/↓ Pg scroll"
	}
}

func modePromptBlockForInputMode(mode topsInputMode) string {
	switch mode {
	case topsInputModeGen:
		return "| GEN | "
	case topsInputModeHelp:
		return "| HELP | "
	case topsInputModeBye:
		return "| BYE | "
	case topsInputModeUnset:
		return "| MODE | "
	default:
		return "| ASK | "
	}
}

func modeToInputMode(mode model.Mode) topsInputMode {
	switch mode {
	case model.ModeGen:
		return topsInputModeGen
	case model.ModeHelp:
		return topsInputModeHelp
	default:
		return topsInputModeAsk
	}
}

func shortcutToMode(r rune) (topsInputMode, bool) {
	switch strings.ToUpper(strings.TrimSpace(string(r))) {
	case "A":
		return topsInputModeAsk, true
	case "G":
		return topsInputModeGen, true
	case "H":
		return topsInputModeHelp, true
	case "B":
		return topsInputModeBye, true
	default:
		return topsInputModeUnset, false
	}
}

func normalizeLegacyChatDraft(state *chatSessionState) {
	if state == nil {
		return
	}
	trimmed := strings.TrimSpace(state.Draft)
	if trimmed == "" {
		return
	}
	switch {
	case strings.HasPrefix(trimmed, "ask "):
		state.StickyMode = model.ModeAsk
		state.SelectedMode = topsInputModeAsk
		state.Draft = strings.TrimSpace(strings.TrimPrefix(trimmed, "ask "))
	case trimmed == "ask" || trimmed == "a":
		state.StickyMode = model.ModeAsk
		state.SelectedMode = topsInputModeAsk
		state.Draft = ""
	case strings.HasPrefix(trimmed, "gen "):
		state.StickyMode = model.ModeGen
		state.SelectedMode = topsInputModeGen
		state.Draft = strings.TrimSpace(strings.TrimPrefix(trimmed, "gen "))
	case trimmed == "gen" || trimmed == "g":
		state.StickyMode = model.ModeGen
		state.SelectedMode = topsInputModeGen
		state.Draft = ""
	case strings.HasPrefix(trimmed, "help "):
		state.StickyMode = model.ModeHelp
		state.SelectedMode = topsInputModeHelp
		state.Draft = strings.TrimSpace(strings.TrimPrefix(trimmed, "help "))
	case trimmed == "help" || trimmed == "h":
		state.StickyMode = model.ModeHelp
		state.SelectedMode = topsInputModeHelp
		state.Draft = ""
	case trimmed == "bye" || trimmed == "b":
		state.SelectedMode = topsInputModeBye
		state.Draft = ""
	}
}

func renderTabs(active chatTab) string {
	inactiveStyle := render.NewStyle().
		Foreground(render.Color("246")).
		Border(render.NormalBorder(), false, false, true, false).
		BorderForeground(render.Color("238")).
		Padding(0, 2)
	activeStyle := render.NewStyle().
		Bold(true).
		Foreground(render.Color("230")).
		Background(render.Color("63")).
		Padding(0, 2)
	configLabel := inactiveStyle.Render("Config")
	chatsLabel := inactiveStyle.Render("Chats")
	if active == tabConfig {
		configLabel = activeStyle.Render("Config")
	} else {
		chatsLabel = activeStyle.Render("Chats")
	}
	return render.JoinHorizontal(render.Top, configLabel, " ", chatsLabel)
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

type transcriptGroup struct {
	Message    chatstore.PersistedMessage
	RawIndexes []int
}

func renderChatTranscript(messages []chatstore.PersistedMessage, widths ...int) string {
	width := 0
	if len(widths) > 0 {
		width = widths[0]
	}
	if width > 2 {
		width -= 2
	}
	if len(messages) == 0 {
		return render.NewStyle().
			Foreground(render.Color("245")).
			Render(wrapTextBlock("No messages yet.\nPress Ctrl+O to create or select a chat.", width))
	}
	groups := coalesceTranscriptGroups(messages)
	var b strings.Builder
	for _, group := range groups {
		b.WriteString(renderTranscriptMessage(group.Message, width))
	}
	return strings.TrimSpace(b.String())
}

func renderChatTranscriptPlain(messages []chatstore.PersistedMessage) string {
	if len(messages) == 0 {
		return "No messages yet.\n"
	}
	groups := coalesceTranscriptGroups(messages)
	var b strings.Builder
	for _, group := range groups {
		text := strings.TrimSpace(firstNonEmpty(group.Message.Output, group.Message.RawInput))
		if text == "" {
			continue
		}
		switch group.Message.Source {
		case "shell_user":
			b.WriteString("$ " + text + "\n\n")
		case "shell_output":
			b.WriteString(text + "\n\n")
		case "tops_user":
			mode := strings.TrimSpace(group.Message.Mode)
			if mode == "" {
				mode = "ask"
			}
			b.WriteString(fmt.Sprintf(">>> %s %s\n\n", mode, text))
		case "tops_agent":
			b.WriteString("TOPS:\n" + text + "\n\n")
		case "tops_stream":
			if strings.TrimSpace(group.Message.Kind) == "answering" {
				b.WriteString("TOPS:\n" + text + "\n\n")
			} else {
				b.WriteString("TOPS thinking: " + text + "\n\n")
			}
		case "approval":
			b.WriteString("Approval: " + text + "\n\n")
		case "action":
			b.WriteString("Action: " + text + "\n\n")
		default:
			b.WriteString("Status: " + text + "\n\n")
		}
	}
	return strings.TrimSpace(b.String()) + "\n"
}

func coalesceTranscriptGroups(messages []chatstore.PersistedMessage) []transcriptGroup {
	groups := make([]transcriptGroup, 0, len(messages))
	for idx, message := range messages {
		if len(groups) > 0 {
			previous := &groups[len(groups)-1]
			if message.Source == "shell_output" && previous.Message.Source == "shell_output" {
				previous.Message.Output = appendTerminalChunk(previous.Message.Output, message.Output)
				if previous.Message.RawInput == "" {
					previous.Message.RawInput = message.RawInput
				}
				previous.RawIndexes = append(previous.RawIndexes, idx)
				continue
			}
			if message.Source == "tops_stream" && previous.Message.Source == "tops_stream" && previous.Message.Kind == message.Kind {
				previous.Message.Output += message.Output
				previous.RawIndexes = append(previous.RawIndexes, idx)
				continue
			}
			if message.Source == "system" && previous.Message.Source == "system" &&
				message.Kind == "status" && previous.Message.Kind == "status" &&
				strings.HasPrefix(strings.TrimSpace(message.Output), "Model is being loaded") &&
				strings.HasPrefix(strings.TrimSpace(previous.Message.Output), "Model is being loaded") {
				previous.Message.Output = message.Output
				previous.RawIndexes = append(previous.RawIndexes, idx)
				continue
			}
		}
		groups = append(groups, transcriptGroup{
			Message:    message,
			RawIndexes: []int{idx},
		})
	}
	return groups
}

func coalesceTranscriptMessages(messages []chatstore.PersistedMessage) []chatstore.PersistedMessage {
	groups := coalesceTranscriptGroups(messages)
	out := make([]chatstore.PersistedMessage, 0, len(groups))
	for _, group := range groups {
		out = append(out, group.Message)
	}
	return out
}

func buildCopyEntries(messages []chatstore.PersistedMessage) []chatCopyEntry {
	groups := coalesceTranscriptGroups(messages)
	entries := make([]chatCopyEntry, 0, len(groups))
	counters := map[string]int{}
	for idx, group := range groups {
		msg := group.Message
		text := strings.TrimSpace(firstNonEmpty(msg.Output, msg.RawInput))
		if text == "" {
			continue
		}
		key := messageEntryKind(msg)
		counters[key]++
		ids := make([]int64, 0, len(group.RawIndexes))
		for _, rawIdx := range group.RawIndexes {
			if rawIdx >= 0 && rawIdx < len(messages) && messages[rawIdx].ID > 0 {
				ids = append(ids, messages[rawIdx].ID)
			}
		}
		entries = append(entries, chatCopyEntry{
			Kind:       key,
			Label:      fmt.Sprintf("%s #%d", messageEntryLabel(msg), counters[key]),
			Preview:    previewText(text, 72),
			Content:    messageCopyContent(msg),
			GroupIndex: idx,
			MessageIDs: ids,
			RawIndexes: append([]int(nil), group.RawIndexes...),
		})
	}
	return entries
}

func messageEntryKind(msg chatstore.PersistedMessage) string {
	switch msg.Source {
	case "shell_user":
		return "shell_command"
	case "shell_output":
		return "shell_output"
	case "tops_user":
		return "tops_query"
	case "tops_agent":
		return "tops_answer"
	case "tops_stream":
		if strings.TrimSpace(msg.Kind) == "answering" {
			return "tops_answer"
		}
		return "tops_stream"
	case "approval":
		return "approval"
	case "action":
		return "action"
	default:
		return "status"
	}
}

func messageEntryLabel(msg chatstore.PersistedMessage) string {
	switch msg.Source {
	case "shell_user":
		return "Shell command"
	case "shell_output":
		return "Shell output"
	case "tops_user":
		return "TOPS query"
	case "tops_agent":
		return "TOPS answer"
	case "tops_stream":
		if strings.TrimSpace(msg.Kind) == "answering" {
			return "TOPS answer"
		}
		return "TOPS stream"
	case "approval":
		return "Approval"
	case "action":
		return "Action"
	default:
		return "Status"
	}
}

func messageCopyContent(msg chatstore.PersistedMessage) string {
	text := strings.TrimSpace(firstNonEmpty(msg.Output, msg.RawInput))
	switch msg.Source {
	case "shell_user":
		return "$ " + text
	case "shell_output":
		return text
	case "tops_user":
		mode := strings.TrimSpace(msg.Mode)
		if mode == "" {
			mode = "ask"
		}
		return fmt.Sprintf(">>> %s %s", mode, text)
	case "tops_agent":
		return "TOPS:\n" + text
	case "tops_stream":
		if strings.TrimSpace(msg.Kind) == "answering" {
			return "TOPS:\n" + text
		}
		return "TOPS thinking: " + text
	case "approval":
		return "Approval: " + text
	case "action":
		return "Action: " + text
	default:
		return "Status: " + text
	}
}

func firstNonEmpty(primary string, fallback string) string {
	if strings.TrimSpace(primary) != "" {
		return primary
	}
	return fallback
}

func renderTranscriptMessage(message chatstore.PersistedMessage, width int) string {
	text := strings.TrimSpace(firstNonEmpty(message.Output, message.RawInput))
	if text == "" {
		return ""
	}
	switch message.Source {
	case "shell_user":
		return renderShellCommand(text, width)
	case "shell_output":
		return renderShellOutput(text, width)
	case "tops_user":
		mode := strings.TrimSpace(message.Mode)
		if mode == "" {
			mode = "ask"
		}
		return renderTOPSInput(mode, text, width)
	case "tops_agent":
		return renderTOPSBlock(text, width)
	case "tops_stream":
		if strings.TrimSpace(message.Kind) == "answering" {
			return renderTOPSBlock(text, width)
		}
		return renderTOPSStream(message.Kind, text, width)
	case "approval":
		return renderNotice("Approval", text, render.Color("214"), width)
	case "action":
		return renderNotice("Action", text, render.Color("69"), width)
	default:
		return renderNotice("Status", text, render.Color("245"), width)
	}
}

func (m *sessionModel) scrollChatToGroup(messages []chatstore.PersistedMessage, targetGroup int) {
	if targetGroup < 0 {
		return
	}
	groups := coalesceTranscriptGroups(messages)
	if targetGroup >= len(groups) {
		return
	}
	width := m.chatViewport.Width
	if width > 2 {
		width -= 2
	}
	lineOffset := 0
	for idx := 0; idx < targetGroup; idx++ {
		block := strings.TrimSpace(renderTranscriptMessage(groups[idx].Message, width))
		if block == "" {
			continue
		}
		lineOffset += strings.Count(block, "\n") + 2
	}
	if lineOffset < 0 {
		lineOffset = 0
	}
	m.chatViewport.YOffset = lineOffset
}

func previewText(text string, limit int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if limit <= 0 || len([]rune(text)) <= limit {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:max(1, limit-1)]) + "…"
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

func renderShellCommand(command string, width int) string {
	command = strings.TrimRight(command, "\n")
	command = wrapTextBlock("$ "+command, width)
	return render.NewStyle().
		Foreground(render.Color("42")).
		Render(command) + "\n\n"
}

func renderShellOutput(output string, width int) string {
	output = strings.TrimRight(output, "\n")
	if strings.TrimSpace(output) == "" {
		return ""
	}
	output = wrapTextBlock(output, width)
	return render.NewStyle().
		Foreground(render.Color("252")).
		Render(output) + "\n\n"
}

func renderTOPSInput(mode string, input string, width int) string {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		mode = "ask"
	}
	line := fmt.Sprintf(">>> %s %s", mode, strings.TrimSpace(input))
	line = wrapTextBlock(line, width)
	return render.NewStyle().
		Foreground(render.Color("111")).
		Render(line) + "\n\n"
}

func renderTOPSBlock(output string, width int) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return ""
	}
	wrapWidth := 0
	if width > 0 {
		wrapWidth = max(10, width-4)
	}
	wrapped := wrapTextBlock(output, wrapWidth)
	body := indentBlock(wrapped, "  ")
	return render.NewStyle().
		Border(render.RoundedBorder(), false, false, false, true).
		BorderForeground(render.Color("63")).
		PaddingLeft(1).
		Render(render.NewStyle().Bold(true).Foreground(render.Color("111")).Render("TOPS:")+"\n"+body) + "\n\n"
}

func renderTOPSStream(kind string, output string, width int) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return ""
	}
	label := "TOPS stream"
	if strings.TrimSpace(kind) == "thinking" {
		label = "TOPS thinking"
	} else if strings.TrimSpace(kind) == "answering" {
		label = "TOPS answering"
	}
	line := wrapTextBlock(label+": "+output, width)
	return render.NewStyle().
		Foreground(render.Color("245")).
		Render(line) + "\n\n"
}

func hasTrailingAnswerStream(messages []chatstore.PersistedMessage, finalOutput string) bool {
	trimmedFinal := strings.TrimSpace(finalOutput)
	if trimmedFinal == "" || len(messages) == 0 {
		return false
	}
	groups := coalesceTranscriptGroups(messages)
	if len(groups) == 0 {
		return false
	}
	last := groups[len(groups)-1].Message
	if last.Source != "tops_stream" || strings.TrimSpace(last.Kind) != "answering" {
		return false
	}
	return strings.TrimSpace(last.Output) == trimmedFinal
}

func renderNotice(label string, text string, color render.Color, width int) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	line := wrapTextBlock(label+": "+text, width)
	return render.NewStyle().
		Foreground(color).
		Render(line) + "\n\n"
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

func (m *sessionModel) startTurnTimer(state *chatSessionState) {
	if state == nil || !state.TurnStartedAt.IsZero() {
		return
	}
	state.TurnStartedAt = m.session.now()
}

func (m *sessionModel) pauseTurnTimer(state *chatSessionState) {
	if state == nil || state.TurnStartedAt.IsZero() || !state.TurnPausedAt.IsZero() {
		return
	}
	state.TurnPausedAt = m.session.now()
}

func (m *sessionModel) resumeTurnTimer(state *chatSessionState) {
	if state == nil || state.TurnStartedAt.IsZero() || state.TurnPausedAt.IsZero() {
		return
	}
	state.TurnPausedFor += m.session.now().Sub(state.TurnPausedAt)
	state.TurnPausedAt = time.Time{}
}

func (m *sessionModel) finishTurnTimer(state *chatSessionState) (time.Duration, time.Duration) {
	if state == nil || state.TurnStartedAt.IsZero() {
		return 0, 0
	}
	end := m.session.now()
	paused := state.TurnPausedFor
	if !state.TurnPausedAt.IsZero() {
		paused += end.Sub(state.TurnPausedAt)
	}
	elapsed := end.Sub(state.TurnStartedAt) - paused
	if elapsed < 0 {
		elapsed = 0
	}
	state.TurnStartedAt = time.Time{}
	state.TurnPausedAt = time.Time{}
	state.TurnPausedFor = 0
	return elapsed, paused
}

func formatTurnDurationLine(elapsed time.Duration, paused time.Duration) string {
	base := "Duration: " + formatDurationMMSS(elapsed)
	if paused > 0 {
		return base + " (approval wait excluded: " + formatDurationMMSS(paused) + ")"
	}
	return base
}

func formatDurationMMSS(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	seconds := int(d.Round(time.Second) / time.Second)
	minutes := seconds / 60
	secs := seconds % 60
	if minutes >= 60 {
		hours := minutes / 60
		minutes = minutes % 60
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, secs)
	}
	return fmt.Sprintf("%02d:%02d", minutes, secs)
}
