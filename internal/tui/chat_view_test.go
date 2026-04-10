package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"tops/internal/app"
	"tops/internal/chatstore"
	"tops/internal/model"
)

type mockShellController struct {
	writes [][]byte
	events chan ShellEvent
	starts int
}

func newMockShellController() *mockShellController {
	return &mockShellController{events: make(chan ShellEvent, 8)}
}

func (m *mockShellController) Start(ctx context.Context, shell string, width int, height int) error {
	m.starts++
	return nil
}

func (m *mockShellController) Write(data []byte) error {
	cp := append([]byte(nil), data...)
	m.writes = append(m.writes, cp)
	return nil
}

func (m *mockShellController) Resize(width int, height int) error { return nil }
func (m *mockShellController) Events() <-chan ShellEvent          { return m.events }
func (m *mockShellController) Close() error                       { return nil }

func TestShellInputBufferedUntilEnter(t *testing.T) {
	session := NewSessionWithOptions(SessionOptions{})
	modelValue := newSessionModel(context.Background(), session, nil, nil)
	m := &modelValue
	m.activeTab = tabChats
	m.selectedChatID = 1
	m.liveChatID = 1
	rt := app.Runtime{}
	m.runtime = &rt
	m.chatState[1] = &chatSessionState{
		ID:         1,
		Live:       true,
		Focus:      chatFocusShell,
		StickyMode: model.ModeAsk,
		Draft:      "ask ",
	}
	shell := newMockShellController()
	m.shell = shell
	m.configureInputForChat()

	var cmd tea.Cmd
	modelOut, cmd := m.handleChatKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	m = modelOut.(*sessionModel)
	if cmd == nil {
	}
	modelOut, _ = m.handleChatKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	m = modelOut.(*sessionModel)

	if len(shell.writes) != 0 {
		t.Fatalf("expected no shell writes before enter, got %d", len(shell.writes))
	}
	if got := m.chatState[1].ShellDraft; got != "ls" {
		t.Fatalf("expected shell draft ls, got %q", got)
	}

	modelOut, cmd = m.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = modelOut.(*sessionModel)
	if cmd == nil {
		t.Fatal("expected enter to produce shell write command")
	}
	msg := cmd()
	if msg != nil {
		t.Fatalf("expected nil tea msg from shell write, got %#v", msg)
	}
	if len(shell.writes) != 1 || string(shell.writes[0]) != "ls\r" {
		t.Fatalf("expected one shell write ls\\r, got %#v", shell.writes)
	}
	if len(m.chatState[1].Transcript) != 1 || m.chatState[1].Transcript[0].Source != "shell_user" {
		t.Fatalf("expected one finalized shell command entry before PTY output, got %+v", m.chatState[1].Transcript)
	}
	if got := m.chatState[1].Transcript[0].Output; got != "ls" {
		t.Fatalf("expected finalized command ls, got %q", got)
	}

	m.handleChatShellOutput(chatShellOutputMsg{SessionID: 1, Text: "$ ls\r\ncmd go.mod\r\n"})
	if len(m.chatState[1].Transcript) != 2 || m.chatState[1].Transcript[1].Source != "shell_output" {
		t.Fatalf("expected PTY output to be the shell transcript source, got %+v", m.chatState[1].Transcript)
	}
	if got := m.chatState[1].Transcript[1].Output; strings.Contains(got, "ls") || !strings.Contains(got, "cmd go.mod") {
		t.Fatalf("expected PTY command echo to be suppressed and output preserved, got %q", got)
	}
}

func TestShellTypingNeverMutatesTranscript(t *testing.T) {
	session := NewSessionWithOptions(SessionOptions{})
	modelValue := newSessionModel(context.Background(), session, nil, nil)
	m := &modelValue
	m.activeTab = tabChats
	m.selectedChatID = 1
	m.liveChatID = 1
	m.runtime = &app.Runtime{}
	m.chatState[1] = &chatSessionState{
		ID:         1,
		Live:       true,
		Focus:      chatFocusShell,
		StickyMode: model.ModeAsk,
		Draft:      "ask ",
	}
	m.shell = newMockShellController()
	m.configureInputForChat()

	for _, r := range []rune("ollama -v") {
		modelOut, _ := m.handleChatKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = modelOut.(*sessionModel)
		if len(m.chatState[1].Transcript) != 0 {
			t.Fatalf("typing %q mutated transcript: %+v", string(r), m.chatState[1].Transcript)
		}
	}
	if got := m.chatState[1].ShellDraft; got != "ollama -v" {
		t.Fatalf("expected footer draft only, got %q", got)
	}
}

func TestGenericAppendRejectsShellTranscriptSources(t *testing.T) {
	session := NewSessionWithOptions(SessionOptions{})
	modelValue := newSessionModel(context.Background(), session, nil, nil)
	m := &modelValue
	m.selectedChatID = 1
	m.chatState[1] = &chatSessionState{ID: 1}

	m.appendChatMessage(1, chatstore.MessageRecord{
		SessionID: 1,
		Source:    "shell_output",
		Kind:      "output",
		Output:    "local echo must not be accepted",
		Success:   true,
	})
	m.appendChatMessage(1, chatstore.MessageRecord{
		SessionID: 1,
		Source:    "shell_user",
		Kind:      "command",
		Output:    "ls",
		Success:   true,
	})
	if len(m.chatState[1].Transcript) != 0 {
		t.Fatalf("expected generic shell transcript appends to be blocked, got %+v", m.chatState[1].Transcript)
	}

	m.appendShellOutputFromPTY(1, "$ ls\r\ncmd go.mod\r\n")
	if len(m.chatState[1].Transcript) != 1 || m.chatState[1].Transcript[0].Source != "shell_output" {
		t.Fatalf("expected PTY-only shell append to succeed, got %+v", m.chatState[1].Transcript)
	}
}

func TestShellSubmitAddsOneCommandEntryAndSuppressesPTYEcho(t *testing.T) {
	session := NewSessionWithOptions(SessionOptions{})
	modelValue := newSessionModel(context.Background(), session, nil, nil)
	m := &modelValue
	m.activeTab = tabChats
	m.selectedChatID = 1
	m.liveChatID = 1
	m.runtime = &app.Runtime{}
	m.chatState[1] = &chatSessionState{
		ID:         1,
		Live:       true,
		Focus:      chatFocusShell,
		StickyMode: model.ModeAsk,
		Draft:      "ask ",
	}
	shell := newMockShellController()
	m.shell = shell
	m.configureInputForChat()
	for _, r := range []rune("ollama -v") {
		modelOut, _ := m.handleChatKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = modelOut.(*sessionModel)
	}

	modelOut, cmd := m.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = modelOut.(*sessionModel)
	if cmd == nil {
		t.Fatal("expected shell submit command")
	}
	_ = cmd()
	if len(m.chatState[1].Transcript) != 1 {
		t.Fatalf("expected exactly one transcript entry after submit, got %+v", m.chatState[1].Transcript)
	}
	if got := m.chatState[1].Transcript[0]; got.Source != "shell_user" || got.Output != "ollama -v" {
		t.Fatalf("expected finalized shell command entry, got %+v", got)
	}

	m.handleChatShellOutput(chatShellOutputMsg{SessionID: 1, Text: "tops % ollama -v\r\nollama version is 0.20.4\r\n"})
	if len(m.chatState[1].Transcript) != 2 {
		t.Fatalf("expected command plus PTY output entries, got %+v", m.chatState[1].Transcript)
	}
	if got := m.chatState[1].Transcript[1].Output; strings.Contains(got, "ollama -v") || !strings.Contains(got, "ollama version is 0.20.4") {
		t.Fatalf("expected PTY echo suppressed and output retained, got %q", got)
	}
	rendered := renderChatTranscript(m.chatState[1].Transcript)
	if strings.Count(rendered, "$ ollama -v") != 1 {
		t.Fatalf("expected rendered command exactly once, got:\n%s", rendered)
	}
}

func TestChatOverlayToggleAndEscape(t *testing.T) {
	session := NewSessionWithOptions(SessionOptions{})
	modelValue := newSessionModel(context.Background(), session, nil, nil)
	m := &modelValue
	m.activeTab = tabChats

	modelOut, _ := m.handleChatKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = modelOut.(*sessionModel)
	if !m.chatOverlayOpen {
		t.Fatal("expected chat overlay to open")
	}

	modelOut, _ = m.handleChatKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = modelOut.(*sessionModel)
	if m.chatOverlayOpen {
		t.Fatal("expected chat overlay to close on escape")
	}
}

func TestChatOverlayNewChatShortcut(t *testing.T) {
	store := &fakeStore{}
	session := NewSessionWithOptions(SessionOptions{Store: store})
	modelValue := newSessionModel(context.Background(), session, nil, nil)
	m := &modelValue
	m.activeTab = tabChats
	m.chatOverlayOpen = true
	shell := newMockShellController()
	m.shellFactory = func() ShellController { return shell }

	modelOut, _ := m.handleChatOverlayKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = modelOut.(*sessionModel)

	if m.chatOverlayOpen {
		t.Fatal("expected new chat shortcut to close overlay")
	}
	if m.selectedChatID == 0 {
		t.Fatal("expected new chat shortcut to select created chat")
	}
	if shell.starts != 1 {
		t.Fatalf("expected new chat shortcut to start shell, got %d starts", shell.starts)
	}
}

func TestChatOverlayDeleteSelectedChat(t *testing.T) {
	store := &fakeStore{
		sessions: []chatstore.PersistedSession{
			{ID: 1, Kind: chatstore.SessionKindChat, Title: "first"},
			{ID: 2, Kind: chatstore.SessionKindChat, Title: "second"},
		},
	}
	session := NewSessionWithOptions(SessionOptions{Store: store})
	modelValue := newSessionModel(context.Background(), session, nil, nil)
	m := &modelValue
	m.activeTab = tabChats
	m.chatOverlayOpen = true
	m.selectedChatID = 1
	m.liveChatID = 1
	m.selectedChatIndex = 1
	m.chatSessions = append([]chatstore.PersistedSession(nil), store.sessions...)
	m.chatState[1] = &chatSessionState{ID: 1, Live: true, Focus: chatFocusTOPS, StickyMode: model.ModeAsk, Draft: "ask "}
	m.chatState[2] = &chatSessionState{ID: 2, Live: true, Focus: chatFocusTOPS, StickyMode: model.ModeAsk, Draft: "ask "}
	shell := newMockShellController()
	m.shell = shell
	m.shellFactory = func() ShellController { return newMockShellController() }

	modelOut, _ := m.handleChatOverlayKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	m = modelOut.(*sessionModel)

	if len(store.deletedSessionID) != 1 || store.deletedSessionID[0] != 1 {
		t.Fatalf("expected selected session 1 to be deleted, got %+v", store.deletedSessionID)
	}
	if _, ok := m.chatState[1]; ok {
		t.Fatal("expected deleted chat state to be removed")
	}
	if m.selectedChatID != 2 {
		t.Fatalf("expected selection to move to remaining chat 2, got %d", m.selectedChatID)
	}
}

func TestChatOverlayDeleteIgnoresNewChatRow(t *testing.T) {
	store := &fakeStore{
		sessions: []chatstore.PersistedSession{{ID: 1, Kind: chatstore.SessionKindChat, Title: "first"}},
	}
	session := NewSessionWithOptions(SessionOptions{Store: store})
	modelValue := newSessionModel(context.Background(), session, nil, nil)
	m := &modelValue
	m.activeTab = tabChats
	m.chatOverlayOpen = true
	m.selectedChatIndex = 0
	m.chatSessions = append([]chatstore.PersistedSession(nil), store.sessions...)

	modelOut, _ := m.handleChatOverlayKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	m = modelOut.(*sessionModel)

	if len(store.deletedSessionID) != 0 {
		t.Fatalf("expected New Chat row not to be deletable, got %+v", store.deletedSessionID)
	}
}

func TestChatOverlayRenderingIsStructured(t *testing.T) {
	session := NewSessionWithOptions(SessionOptions{})
	modelValue := newSessionModel(context.Background(), session, nil, nil)
	m := &modelValue
	m.selectedChatID = 2
	m.selectedChatIndex = 1
	m.chatOverlayVP.Width = 44
	m.chatOverlayVP.Height = 10
	m.chatSessions = []chatstore.PersistedSession{
		{ID: 2, Title: "what is my operating system and architecture"},
	}

	m.refreshChatOverlay()
	rendered := m.chatOverlayVP.View()

	for _, want := range []string{"Chats", "─", "New Chat", "current", "Enter: Open", "n: New", "d: Delete", "Esc: Close"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected overlay to contain %q, got:\n%s", want, rendered)
		}
	}
}

func TestChatOverlayLinesFillContentWidth(t *testing.T) {
	session := NewSessionWithOptions(SessionOptions{})
	modelValue := newSessionModel(context.Background(), session, nil, nil)
	m := &modelValue
	m.selectedChatID = 2
	m.selectedChatIndex = 1
	m.chatOverlayVP.Width = 44
	m.chatOverlayVP.Height = 10
	m.chatSessions = []chatstore.PersistedSession{
		{ID: 2, Title: "what is my operating system and architecture"},
	}

	m.refreshChatOverlay()
	width := chatOverlayContentWidth(m)
	for _, line := range strings.Split(ansi.Strip(m.chatOverlayVP.View()), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if got := lipgloss.Width(line); got != width {
			t.Fatalf("expected overlay line width %d, got %d for %q", width, got, line)
		}
	}
}

func TestChatDraftWithoutPrefixShowsGuidance(t *testing.T) {
	session := NewSessionWithOptions(SessionOptions{})
	modelValue := newSessionModel(context.Background(), session, nil, nil)
	m := &modelValue
	m.activeTab = tabChats
	m.selectedChatID = 1
	m.liveChatID = 1
	rt := app.Runtime{}
	m.runtime = &rt
	m.chatState[1] = &chatSessionState{
		ID:         1,
		Live:       true,
		Focus:      chatFocusTOPS,
		StickyMode: model.ModeAsk,
		Draft:      "ask ",
	}
	m.configureInputForChat()
	m.input.SetValue("hello there")
	m.chatState[1].Draft = "hello there"

	modelOut, _ := m.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = modelOut.(*sessionModel)

	if len(m.chatState[1].Transcript) == 0 {
		t.Fatal("expected guidance message in transcript")
	}
	last := m.chatState[1].Transcript[len(m.chatState[1].Transcript)-1]
	if last.Source != "system" || last.Success {
		t.Fatalf("expected failed system guidance entry, got %+v", last)
	}
	if got := m.chatState[1].Draft; got != "ask " {
		t.Fatalf("expected draft reset to ask prefix, got %q", got)
	}
}

func TestShiftTabKeySwitchesToChats(t *testing.T) {
	session := NewSessionWithOptions(SessionOptions{})
	m := newSessionModel(context.Background(), session, nil, nil)

	modelOut, _ := m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	m = modelOut.(sessionModel)
	if m.activeTab != tabChats {
		t.Fatalf("expected active tab chats, got %s", m.activeTab)
	}
}

func TestTabKeyTogglesChatFocus(t *testing.T) {
	session := NewSessionWithOptions(SessionOptions{})
	modelValue := newSessionModel(context.Background(), session, nil, nil)
	m := &modelValue
	m.activeTab = tabChats
	m.selectedChatID = 1
	m.liveChatID = 1
	m.chatState[1] = &chatSessionState{
		ID:         1,
		Live:       true,
		Focus:      chatFocusTOPS,
		StickyMode: model.ModeAsk,
		Draft:      "ask ",
	}
	m.configureInputForChat()

	modelOut, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated := modelOut.(*sessionModel)
	if got := updated.chatState[1].Focus; got != chatFocusShell {
		t.Fatalf("expected tab to switch chat focus to shell, got %s", got)
	}
}

func TestLineFeedSubmitsShellDraft(t *testing.T) {
	session := NewSessionWithOptions(SessionOptions{})
	modelValue := newSessionModel(context.Background(), session, nil, nil)
	m := &modelValue
	m.activeTab = tabChats
	m.selectedChatID = 1
	m.liveChatID = 1
	m.runtime = &app.Runtime{}
	m.chatState[1] = &chatSessionState{
		ID:         1,
		Live:       true,
		Focus:      chatFocusShell,
		StickyMode: model.ModeAsk,
		Draft:      "ask ",
		ShellDraft: "pwd",
	}
	shell := newMockShellController()
	m.shell = shell
	m.configureInputForChat()

	modelOut, cmd := m.handleChatKey(tea.KeyMsg{Type: tea.KeyCtrlJ})
	m = modelOut.(*sessionModel)
	if cmd == nil {
		t.Fatal("expected ctrl+j/line-feed to submit shell command")
	}
	_ = cmd()
	if len(shell.writes) != 1 || string(shell.writes[0]) != "pwd\r" {
		t.Fatalf("expected one shell write pwd\\r, got %#v", shell.writes)
	}
	if got := m.chatState[1].Focus; got != chatFocusShell {
		t.Fatalf("expected line-feed submit not to toggle focus, got %s", got)
	}
}

func TestShellEnterStartsMissingPTYAndSubmits(t *testing.T) {
	session := NewSessionWithOptions(SessionOptions{})
	modelValue := newSessionModel(context.Background(), session, nil, nil)
	m := &modelValue
	m.activeTab = tabChats
	m.selectedChatID = 1
	m.chatState[1] = &chatSessionState{
		ID:         1,
		Live:       true,
		Focus:      chatFocusShell,
		StickyMode: model.ModeAsk,
		Draft:      "ask ",
		ShellDraft: "ls",
	}
	shell := newMockShellController()
	m.shellFactory = func() ShellController { return shell }
	m.configureInputForChat()

	modelOut, cmd := m.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = modelOut.(*sessionModel)
	if cmd == nil {
		t.Fatal("expected enter to start shell and submit command")
	}
	shell.events <- ShellEvent{Data: "$ ls\r\n"}
	_ = cmd()
	if shell.starts != 1 {
		t.Fatalf("expected shell to be started once, got %d", shell.starts)
	}
	if len(shell.writes) != 1 || string(shell.writes[0]) != "ls\r" {
		t.Fatalf("expected shell write ls\\r, got %#v", shell.writes)
	}
	if len(m.chatState[1].Transcript) != 1 || m.chatState[1].Transcript[0].Source != "shell_user" {
		t.Fatalf("expected one finalized shell command entry, got %+v", m.chatState[1].Transcript)
	}
}

func TestRenderChatTranscriptUsesProductFacingPrefixes(t *testing.T) {
	rendered := renderChatTranscript([]chatstore.PersistedMessage{
		{Timestamp: time.Now(), Source: "shell_user", RawInput: "pwd", Output: "pwd", Success: true},
		{Timestamp: time.Now(), Source: "shell_output", Output: "/tmp/project", Success: true},
		{Timestamp: time.Now(), Source: "tops_user", Mode: "ask", Output: "where am I?", Success: true},
		{Timestamp: time.Now(), Source: "tops_agent", Output: "You are in /tmp/project.", Success: true},
		{Timestamp: time.Now(), Source: "system", Output: "Shell paused.", Success: true},
	})

	for _, want := range []string{"$ pwd", "/tmp/project", ">>> ask where am I?", "TOPS:", "You are in /tmp/project.", "Status: Shell paused."} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected rendered transcript to contain %q, got:\n%s", want, rendered)
		}
	}
	for _, unwanted := range []string{"shell_user", "tops_agent", "[system]"} {
		if strings.Contains(rendered, unwanted) {
			t.Fatalf("expected no internal label %q in transcript, got:\n%s", unwanted, rendered)
		}
	}
}

func TestRenderChatTranscriptCoalescesShellOutputFragments(t *testing.T) {
	rendered := renderChatTranscript([]chatstore.PersistedMessage{
		{Timestamp: time.Now(), Source: "shell_output", Output: "o", Success: true},
		{Timestamp: time.Now(), Source: "shell_output", Output: "l", Success: true},
		{Timestamp: time.Now(), Source: "shell_output", Output: "lama", Success: true},
		{Timestamp: time.Now(), Source: "shell_output", Output: " -v\r\nollama version 0.1.0\r\n", Success: true},
	})

	if !strings.Contains(rendered, "ollama -v") {
		t.Fatalf("expected shell fragments to coalesce into one command block, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "\n\no\n\n") || strings.Contains(rendered, "\n\nol\n\n") {
		t.Fatalf("expected no standalone partial shell fragments, got:\n%s", rendered)
	}
}
