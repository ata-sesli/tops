package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/phoenix-tui/phoenix/tea"

	"tops/internal/app"
	"tops/internal/model"
	"tops/internal/storage/chatstore"
	"tops/internal/ui/termutil/ansi"
	"tops/internal/ui/tui/render"
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
	modelOut, cmd := m.handleChatKey(tea.KeyMsg{Type: tea.KeyRune, Rune: 'l'})
	m = modelOut
	if cmd == nil {
	}
	modelOut, _ = m.handleChatKey(tea.KeyMsg{Type: tea.KeyRune, Rune: 's'})
	m = modelOut

	if len(shell.writes) != 0 {
		t.Fatalf("expected no shell writes before enter, got %d", len(shell.writes))
	}
	if got := m.chatState[1].ShellDraft; got != "ls" {
		t.Fatalf("expected shell draft ls, got %q", got)
	}

	modelOut, cmd = m.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = modelOut
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

	for _, r := range []rune("python3 --version") {
		modelOut, _ := m.handleChatKey(tea.KeyMsg{Type: tea.KeyRune, Rune: r})
		m = modelOut
		if len(m.chatState[1].Transcript) != 0 {
			t.Fatalf("typing %q mutated transcript: %+v", string(r), m.chatState[1].Transcript)
		}
	}
	if got := m.chatState[1].ShellDraft; got != "python3 --version" {
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
	for _, r := range []rune("python3 --version") {
		modelOut, _ := m.handleChatKey(tea.KeyMsg{Type: tea.KeyRune, Rune: r})
		m = modelOut
	}

	modelOut, cmd := m.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = modelOut
	if cmd == nil {
		t.Fatal("expected shell submit command")
	}
	_ = cmd()
	if len(m.chatState[1].Transcript) != 1 {
		t.Fatalf("expected exactly one transcript entry after submit, got %+v", m.chatState[1].Transcript)
	}
	if got := m.chatState[1].Transcript[0]; got.Source != "shell_user" || got.Output != "python3 --version" {
		t.Fatalf("expected finalized shell command entry, got %+v", got)
	}

	m.handleChatShellOutput(chatShellOutputMsg{SessionID: 1, Text: "tops % python3 --version\r\nPython 3.12.0\r\n"})
	if len(m.chatState[1].Transcript) != 2 {
		t.Fatalf("expected command plus PTY output entries, got %+v", m.chatState[1].Transcript)
	}
	if got := m.chatState[1].Transcript[1].Output; strings.Contains(got, "python3 --version") || !strings.Contains(got, "Python 3.12.0") {
		t.Fatalf("expected PTY echo suppressed and output retained, got %q", got)
	}
	rendered := renderChatTranscript(m.chatState[1].Transcript)
	if strings.Count(rendered, "$ python3 --version") != 1 {
		t.Fatalf("expected rendered command exactly once, got:\n%s", rendered)
	}
}

func TestChatOverlayToggleAndEscape(t *testing.T) {
	session := NewSessionWithOptions(SessionOptions{})
	modelValue := newSessionModel(context.Background(), session, nil, nil)
	m := &modelValue
	m.activeTab = tabChats

	modelOut, _ := m.handleChatKey(tea.KeyMsg{Type: tea.KeyRune, Ctrl: true, Rune: 'o'})
	m = modelOut
	if !m.chatOverlayOpen {
		t.Fatal("expected chat overlay to open")
	}

	modelOut, _ = m.handleChatKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = modelOut
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

	modelOut, _ := m.handleChatOverlayKey(tea.KeyMsg{Type: tea.KeyRune, Rune: 'n'})
	m = modelOut

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

	modelOut, _ := m.handleChatOverlayKey(tea.KeyMsg{Type: tea.KeyRune, Rune: 'd'})
	m = modelOut

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

	modelOut, _ := m.handleChatOverlayKey(tea.KeyMsg{Type: tea.KeyRune, Rune: 'd'})
	m = modelOut

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
		if got := render.Width(line); got != width {
			t.Fatalf("expected overlay line width %d, got %d for %q", width, got, line)
		}
	}
}

func TestChatDraftWithoutPrefixDefaultsToAsk(t *testing.T) {
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
	m = modelOut

	if len(m.chatState[1].Transcript) == 0 {
		t.Fatal("expected submitted ask message in transcript")
	}
	last := m.chatState[1].Transcript[len(m.chatState[1].Transcript)-1]
	if last.Source != "tops_user" || !last.Success {
		t.Fatalf("expected successful ask transcript entry, got %+v", last)
	}
	if strings.TrimSpace(last.RawInput) != "ask hello there" {
		t.Fatalf("expected implicit ask prefix, got raw_input=%q", last.RawInput)
	}
	if got := m.chatState[1].Draft; strings.TrimSpace(got) != "" {
		t.Fatalf("expected draft to clear after submit, got %q", got)
	}
}

func TestShiftTabKeySwitchesToChats(t *testing.T) {
	session := NewSessionWithOptions(SessionOptions{})
	m := newSessionModel(context.Background(), session, nil, nil)

	modelOut, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab, Shift: true})
	m = *modelOut
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
	updated := modelOut
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

	modelOut, cmd := m.handleChatKey(tea.KeyMsg{Type: tea.KeyRune, Ctrl: true, Rune: 'j'})
	m = modelOut
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
	m = modelOut
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
		{Timestamp: time.Now(), Source: "shell_output", Output: "p", Success: true},
		{Timestamp: time.Now(), Source: "shell_output", Output: "ython3", Success: true},
		{Timestamp: time.Now(), Source: "shell_output", Output: " --version", Success: true},
		{Timestamp: time.Now(), Source: "shell_output", Output: "\r\nPython 3.11.0\r\n", Success: true},
	})

	if !strings.Contains(rendered, "python3 --version") {
		t.Fatalf("expected shell fragments to coalesce into one command block, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "\n\no\n\n") || strings.Contains(rendered, "\n\nol\n\n") {
		t.Fatalf("expected no standalone partial shell fragments, got:\n%s", rendered)
	}
}

func TestRenderChatTranscriptShowsRawDebugStreamTokens(t *testing.T) {
	rendered := renderChatTranscript([]chatstore.PersistedMessage{
		{Timestamp: time.Now(), Source: "tops_stream", Kind: "thinking", Output: "We need", Success: true},
		{Timestamp: time.Now(), Source: "tops_stream", Kind: "thinking", Output: " local evidence.", Success: true},
		{Timestamp: time.Now(), Source: "tops_stream", Kind: "answering", Output: "{\"answer\":\"macOS\"}", Success: true},
	})
	if !strings.Contains(rendered, "TOPS thinking: We need local evidence.") {
		t.Fatalf("expected coalesced thinking tokens, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "{\"answer\":\"macOS\"}") {
		t.Fatalf("expected answering payload in transcript, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "TOPS is thinking...") || strings.Contains(rendered, "TOPS is responding...") {
		t.Fatalf("expected raw stream tokens, not placeholder statuses, got:\n%s", rendered)
	}
}

func TestChatTranscriptMouseWheelScroll(t *testing.T) {
	session := NewSessionWithOptions(SessionOptions{})
	modelValue := newSessionModel(context.Background(), session, &app.Runtime{}, nil)
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
	modelOut, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	*m = *modelOut
	var chunk strings.Builder
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&chunk, "line-%03d\n", i)
	}
	m.appendShellOutputFromPTY(1, chunk.String())
	m.chatViewport.YOffset = 15
	start := m.chatViewport.YOffset
	modelOut, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	*m = *modelOut
	if m.chatViewport.YOffset >= start {
		t.Fatalf("expected wheel-up to scroll transcript up, start=%d now=%d", start, m.chatViewport.YOffset)
	}
	afterUp := m.chatViewport.YOffset
	modelOut, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	*m = *modelOut
	if m.chatViewport.YOffset <= afterUp {
		t.Fatalf("expected wheel-down to scroll transcript down, up=%d now=%d", afterUp, m.chatViewport.YOffset)
	}
}

func TestRenderChatTranscriptWrapsLongLinesToWidth(t *testing.T) {
	width := 30
	rendered := renderChatTranscript([]chatstore.PersistedMessage{
		{Timestamp: time.Now(), Source: "shell_user", Output: "averyveryveryveryveryveryverylongcommandwithoutspaces", Success: true},
		{Timestamp: time.Now(), Source: "tops_agent", Output: "This is a deliberately long answer line that should wrap cleanly inside the transcript viewport width.", Success: true},
		{Timestamp: time.Now(), Source: "tops_stream", Kind: "thinking", Output: "token1 token2 token3 token4 token5 token6 token7 token8 token9", Success: true},
	}, width)
	for _, line := range strings.Split(ansi.Strip(rendered), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if render.Width(line) > width {
			t.Fatalf("expected wrapped transcript line width <= %d, got %d for %q", width, render.Width(line), line)
		}
	}
}

func TestChatTranscriptKeyboardScrollKeys(t *testing.T) {
	session := NewSessionWithOptions(SessionOptions{})
	modelValue := newSessionModel(context.Background(), session, &app.Runtime{}, nil)
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
	modelOut, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	*m = *modelOut
	var chunk strings.Builder
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&chunk, "line-%03d\n", i)
	}
	m.appendShellOutputFromPTY(1, chunk.String())
	m.chatViewport.YOffset = 20

	before := m.chatViewport.YOffset
	modelOut, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	*m = *modelOut
	if m.chatViewport.YOffset >= before {
		t.Fatalf("expected up key to scroll transcript up, before=%d now=%d", before, m.chatViewport.YOffset)
	}

	before = m.chatViewport.YOffset
	modelOut, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	*m = *modelOut
	if m.chatViewport.YOffset <= before {
		t.Fatalf("expected page down to scroll transcript down, before=%d now=%d", before, m.chatViewport.YOffset)
	}

	modelOut, _ = m.Update(tea.KeyMsg{Type: tea.KeyHome})
	*m = *modelOut
	if m.chatViewport.YOffset != 0 {
		t.Fatalf("expected home to go top, now=%d", m.chatViewport.YOffset)
	}

	modelOut, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	*m = *modelOut
	if m.chatViewport.YOffset == 0 {
		t.Fatalf("expected end to go bottom, now=%d", m.chatViewport.YOffset)
	}
}

func TestExportCurrentChatTranscriptWritesFile(t *testing.T) {
	session := NewSessionWithOptions(SessionOptions{})
	modelValue := newSessionModel(context.Background(), session, &app.Runtime{}, nil)
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
		Transcript: []chatstore.PersistedMessage{
			{Source: "tops_user", Mode: "ask", Output: "what is my os?"},
			{Source: "tops_agent", Output: "You are on macOS."},
		},
	}
	modelOut, _ := m.Update(tea.KeyMsg{Type: tea.KeyRune, Ctrl: true, Rune: 'e'})
	*m = *modelOut
	transcript := m.chatState[1].Transcript
	if len(transcript) == 0 {
		t.Fatal("expected transcript export status message")
	}
	last := transcript[len(transcript)-1]
	if !strings.HasPrefix(last.Output, "Transcript exported: ") {
		t.Fatalf("expected export status line, got %+v", last)
	}
	path := strings.TrimPrefix(last.Output, "Transcript exported: ")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected exported file to exist, path=%q err=%v", path, err)
	}
	content := string(data)
	if !strings.Contains(content, ">>> ask what is my os?") || !strings.Contains(content, "TOPS:") {
		t.Fatalf("unexpected exported transcript content:\n%s", content)
	}
}

func TestBuildCopyEntriesIncludesTranscriptMessages(t *testing.T) {
	entries := buildCopyEntries([]chatstore.PersistedMessage{
		{Source: "tops_user", Mode: "ask", Output: "What is my OS?"},
		{Source: "tops_stream", Kind: "thinking", Output: "Need local evidence."},
		{Source: "tops_stream", Kind: "answering", Output: "{\"answer\":\"macOS\"}"},
		{Source: "tops_agent", Output: "You are on macOS."},
		{Source: "shell_user", Output: "ls"},
		{Source: "shell_output", Output: "cmd\ngo.mod"},
	})
	joinedLabels := make([]string, 0, len(entries))
	for _, e := range entries {
		joinedLabels = append(joinedLabels, e.Label)
	}
	all := strings.Join(joinedLabels, " | ")
	for _, want := range []string{"TOPS query #1", "TOPS stream #1", "TOPS answer #1", "Shell command #1", "Shell output #1"} {
		if !strings.Contains(all, want) {
			t.Fatalf("expected copy entries to include %q, got labels: %s", want, all)
		}
	}
	var topAnswer string
	for _, e := range entries {
		if e.Kind == "tops_answer" {
			topAnswer = e.Content
			break
		}
	}
	if !strings.Contains(topAnswer, "TOPS:\n{\"answer\":\"macOS\"}") {
		t.Fatalf("expected TOPS answer entry content, got:\n%s", topAnswer)
	}
}

func TestCopyOverlaySupportsSelectAndCopy(t *testing.T) {
	session := NewSessionWithOptions(SessionOptions{})
	modelValue := newSessionModel(context.Background(), session, &app.Runtime{}, nil)
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
		Transcript: []chatstore.PersistedMessage{
			{Source: "shell_user", Output: "pwd"},
			{Source: "shell_output", Output: "/tmp/project"},
		},
	}
	var copied string
	m.copyToClipboard = func(text string) error {
		copied = text
		return nil
	}

	modelOut, _ := m.Update(tea.KeyMsg{Type: tea.KeyRune, Ctrl: true, Rune: 'k'})
	*m = *modelOut
	if !m.copyOverlayOpen {
		t.Fatal("expected copy overlay to open")
	}

	// First entry should be shell command, second shell output. Select second and copy with "c".
	modelOut, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	*m = *modelOut
	modelOut, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	*m = *modelOut
	modelOut, _ = m.Update(tea.KeyMsg{Type: tea.KeyRune, Rune: 'c'})
	*m = *modelOut
	if strings.TrimSpace(copied) != "/tmp/project" {
		t.Fatalf("expected copied text to be selected shell output, got %q", copied)
	}
	if m.copyOverlayOpen {
		t.Fatal("expected copy overlay to close after copy")
	}
	last := m.chatState[1].Transcript[len(m.chatState[1].Transcript)-1]
	if !strings.Contains(last.Output, "Copied") {
		t.Fatalf("expected copy status message, got %+v", last)
	}
}

func TestCopyOverlayRemoveSelectedMessage(t *testing.T) {
	session := NewSessionWithOptions(SessionOptions{})
	modelValue := newSessionModel(context.Background(), session, &app.Runtime{}, nil)
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
		Transcript: []chatstore.PersistedMessage{
			{ID: 11, Source: "tops_user", Mode: "ask", Output: "what is my os"},
			{ID: 12, Source: "tops_agent", Output: "You are on macOS"},
		},
	}
	m.copyOverlayOpen = true
	m.openCopyOverlay()
	if len(m.copyEntries) < 2 {
		t.Fatalf("expected copy overlay entries, got %+v", m.copyEntries)
	}
	// Select second row and remove with "r".
	m.copySelectedIndex = 1
	modelOut, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	*m = *modelOut
	modelOut, _ = m.Update(tea.KeyMsg{Type: tea.KeyRune, Rune: 'r'})
	*m = *modelOut
	if len(m.chatState[1].Transcript) != 2 {
		// 1 remaining user message + 1 status message
		t.Fatalf("expected transcript to remove one row and append status, got %+v", m.chatState[1].Transcript)
	}
	if strings.Contains(m.chatState[1].Transcript[0].Output, "You are on macOS") {
		t.Fatalf("expected selected message removed, transcript=%+v", m.chatState[1].Transcript)
	}
	last := m.chatState[1].Transcript[len(m.chatState[1].Transcript)-1]
	if !strings.Contains(last.Output, "Removed 1 message(s).") {
		t.Fatalf("expected remove status line, got %+v", last)
	}
}

func TestCopyOverlaySelectionAutoScrolls(t *testing.T) {
	session := NewSessionWithOptions(SessionOptions{})
	modelValue := newSessionModel(context.Background(), session, &app.Runtime{}, nil)
	m := &modelValue
	m.activeTab = tabChats
	m.selectedChatID = 1
	m.liveChatID = 1
	transcript := make([]chatstore.PersistedMessage, 0, 30)
	for i := 0; i < 30; i++ {
		transcript = append(transcript, chatstore.PersistedMessage{
			ID:     int64(i + 1),
			Source: "tops_user",
			Mode:   "ask",
			Output: fmt.Sprintf("q-%d", i+1),
		})
	}
	m.chatState[1] = &chatSessionState{
		ID:         1,
		Live:       true,
		Focus:      chatFocusTOPS,
		StickyMode: model.ModeAsk,
		Draft:      "ask ",
		Transcript: transcript,
	}
	m.copyOverlayVP.Height = 10
	m.openCopyOverlay()
	m.copySelectedIndex = len(m.copyEntries) - 1
	m.refreshCopyOverlay()
	if m.copyOverlayVP.YOffset <= 0 {
		t.Fatalf("expected copy overlay to scroll for low selection, yoffset=%d", m.copyOverlayVP.YOffset)
	}
}

func TestCopyEntryColorByKind(t *testing.T) {
	cases := map[string]render.Color{
		"tops_query":    render.Color("111"),
		"action":        render.Color("69"),
		"approval":      render.Color("214"),
		"tops_answer":   render.Color("117"),
		"shell_command": render.Color("42"),
		"shell_output":  render.Color("252"),
		"status":        render.Color("245"),
	}
	for kind, want := range cases {
		got := copyEntryColor(kind)
		if got != want {
			t.Fatalf("copyEntryColor(%q)=%q want %q", kind, got, want)
		}
	}
}

func TestTurnDurationExcludesApprovalWait(t *testing.T) {
	session := NewSessionWithOptions(SessionOptions{})
	base := time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC)
	now := base
	session.now = func() time.Time { return now }

	modelValue := newSessionModel(context.Background(), session, &app.Runtime{}, nil)
	m := &modelValue
	m.activeTab = tabChats
	m.selectedChatID = 1
	m.liveChatID = 1
	m.chatState[1] = &chatSessionState{
		ID:            1,
		Live:          true,
		Focus:         chatFocusTOPS,
		StickyMode:    model.ModeAsk,
		Draft:         "ask ",
		TurnStartedAt: base,
		TurnPausedFor: 20 * time.Second,
	}

	now = base.Add(50 * time.Second)
	m.handleChatTurnDone(chatTurnDoneMsg{
		SessionID: 1,
		Mode:      model.ModeAsk,
		Input:     "what is my os",
		Output:    "You are on macOS.",
		Err:       nil,
	})

	transcript := m.chatState[1].Transcript
	if len(transcript) < 2 {
		t.Fatalf("expected answer and duration records, got %+v", transcript)
	}
	last := transcript[len(transcript)-1]
	if last.Source != "system" || !strings.Contains(last.Output, "Duration: 00:30") {
		t.Fatalf("expected duration status with 30s active time, got %+v", last)
	}
	if !strings.Contains(last.Output, "approval wait excluded: 00:20") {
		t.Fatalf("expected excluded approval wait in status line, got %+v", last)
	}
}
