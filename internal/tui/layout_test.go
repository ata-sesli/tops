package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"tops/internal/app"
	"tops/internal/chatstore"
	"tops/internal/config"
)

func TestConfigPromptIsolationOnStartupAndSessionLoad(t *testing.T) {
	session := NewSessionWithOptions(SessionOptions{})
	m := newSessionModel(context.Background(), session, nil, nil)
	if m.activeTab != tabConfig {
		t.Fatalf("expected startup tab %q, got %q", tabConfig, m.activeTab)
	}
	if got := m.input.Prompt; got != "tops> " {
		t.Fatalf("expected manager prompt tops>, got %q", got)
	}
	if got := strings.TrimSpace(m.input.Value()); got != "" {
		t.Fatalf("expected empty startup input, got %q", got)
	}
	if strings.Contains(m.input.Value(), "ask") {
		t.Fatalf("did not expect chat draft in config input, got %q", m.input.Value())
	}

	modelOut, _ := m.Update(chatSessionsLoadedMsg{Sessions: []chatstore.PersistedSession{{ID: 7, Kind: chatstore.SessionKindChat, Title: "chat"}}})
	updated := modelOut.(sessionModel)
	if updated.activeTab != tabConfig {
		t.Fatalf("expected to remain on config tab, got %q", updated.activeTab)
	}
	if got := updated.input.Prompt; got != "tops> " {
		t.Fatalf("expected manager prompt to remain tops>, got %q", got)
	}
	if got := strings.TrimSpace(updated.input.Value()); got != "" {
		t.Fatalf("expected empty config input after chat load, got %q", got)
	}
}

func TestCentralLayoutShowsTabsInConfigChatsAndOverlay(t *testing.T) {
	session := NewSessionWithOptions(SessionOptions{})
	m := newSessionModel(context.Background(), session, &app.Runtime{}, nil)
	modelOut, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	m = modelOut.(sessionModel)

	configView := m.View()
	for _, want := range []string{"Config", "Chats", "Manager Output", "Config Menu"} {
		if !strings.Contains(configView, want) {
			t.Fatalf("expected config view to contain %q, got:\n%s", want, configView)
		}
	}

	modelOut, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	m = modelOut.(sessionModel)
	m.selectedChatID = 1
	m.liveChatID = 1
	m.chatState[1] = &chatSessionState{ID: 1, Live: true, Focus: chatFocusTOPS, Draft: "ask "}
	m.configureInputForChat()
	chatView := m.View()
	for _, want := range []string{"Config", "Chats", "Focus:", "TOPS:", "Current Chat"} {
		if !strings.Contains(chatView, want) {
			t.Fatalf("expected chat view to contain %q, got:\n%s", want, chatView)
		}
	}

	m.chatOverlayOpen = true
	m.chatSessions = []chatstore.PersistedSession{{ID: 1, Kind: chatstore.SessionKindChat, Title: "chat one"}}
	m.selectedChatIndex = 1
	m.refreshChatOverlay()
	overlayView := m.View()
	for _, want := range []string{"Config", "Chats", "Chats", "Enter: Open", "Esc: Close"} {
		if !strings.Contains(overlayView, want) {
			t.Fatalf("expected overlay view to keep shared layout and contain %q, got:\n%s", want, overlayView)
		}
	}
}

func TestConfigPanelArrowKeysScrollSingleViewport(t *testing.T) {
	session := NewSessionWithOptions(SessionOptions{})
	m := newSessionModel(context.Background(), session, &app.Runtime{}, nil)
	modelOut, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 14})
	m = modelOut.(sessionModel)

	startSelected := m.configMenu.Selected
	for range 20 {
		modelOut, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
		switch updated := modelOut.(type) {
		case sessionModel:
			m = updated
		case *sessionModel:
			m = *updated
		default:
			t.Fatalf("unexpected model type %T", modelOut)
		}
	}
	if m.configMenu.Selected <= startSelected {
		t.Fatalf("expected arrow down to move selection, start=%d now=%d", startSelected, m.configMenu.Selected)
	}
	if m.configViewport.YOffset == 0 {
		t.Fatalf("expected viewport to auto-follow selection and scroll, yoffset=%d", m.configViewport.YOffset)
	}

	afterDownSelected := m.configMenu.Selected
	afterDownOffset := m.configViewport.YOffset
	modelOut, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	switch updated := modelOut.(type) {
	case sessionModel:
		m = updated
	case *sessionModel:
		m = *updated
	default:
		t.Fatalf("unexpected model type %T", modelOut)
	}
	if m.configMenu.Selected >= afterDownSelected {
		t.Fatalf("expected arrow up to move selection up, downSelected=%d now=%d", afterDownSelected, m.configMenu.Selected)
	}
	if m.configViewport.YOffset > afterDownOffset {
		t.Fatalf("expected viewport to not scroll further down on up key, before=%d now=%d", afterDownOffset, m.configViewport.YOffset)
	}
}

func TestConfigInputRequiresSlashActivation(t *testing.T) {
	session := NewSessionWithOptions(SessionOptions{})
	m := newSessionModel(context.Background(), session, &app.Runtime{}, nil)
	modelOut, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m = modelOut.(sessionModel)

	if m.configInputActive {
		t.Fatal("expected config input to be inactive by default")
	}

	modelOut, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	switch updated := modelOut.(type) {
	case sessionModel:
		m = updated
	case *sessionModel:
		m = *updated
	default:
		t.Fatalf("unexpected model type %T", modelOut)
	}
	if m.configInputActive {
		t.Fatal("expected plain keypress not to activate command input")
	}
	if got := strings.TrimSpace(m.input.Value()); got != "" {
		t.Fatalf("expected input to remain empty before slash activation, got %q", got)
	}

	modelOut, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	switch updated := modelOut.(type) {
	case sessionModel:
		m = updated
	case *sessionModel:
		m = *updated
	default:
		t.Fatalf("unexpected model type %T", modelOut)
	}
	if !m.configInputActive {
		t.Fatal("expected slash to activate command input")
	}
	if got := m.input.Value(); got != "/" {
		t.Fatalf("expected slash seed in input, got %q", got)
	}

	modelOut, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})
	switch updated := modelOut.(type) {
	case sessionModel:
		m = updated
	case *sessionModel:
		m = *updated
	default:
		t.Fatalf("unexpected model type %T", modelOut)
	}
	if got := m.input.Value(); got != "/m" {
		t.Fatalf("expected command-mode editing to continue after slash, got %q", got)
	}
}

func TestConfigSpaceTogglesSelectedCycleItem(t *testing.T) {
	configPath := t.TempDir() + "/config.json"
	cfg := config.Config{
		Provider: config.ProviderConfig{
			Type:     config.ProviderLocal,
			Model:    "llama3.1",
			Endpoint: "http://localhost:11434",
		},
		Shell: "zsh",
		Execution: config.ExecutionConfig{
			Permissions: config.ExecutionPermissionsConfig{
				ReadOnly: config.ActionPermissionAllow,
				Write:    config.ActionPermissionRequest,
			},
			TraceMode: config.TraceModeRelease,
		},
	}
	if err := config.SaveAtomic(configPath, cfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	rt, err := app.NewRuntime(cfg)
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	session := NewSessionWithOptions(SessionOptions{
		ConfigPath: configPath,
		RuntimeLoader: func(path string) (app.Runtime, error) {
			loaded, err := config.Load(path)
			if err != nil {
				return app.Runtime{}, err
			}
			return app.NewRuntime(loaded)
		},
	})
	m := newSessionModel(context.Background(), session, &rt, nil)
	modelOut, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m = modelOut.(sessionModel)

	target := -1
	for i, item := range m.configMenu.Items {
		if item.Key == "execution.read_only" {
			target = i
			break
		}
	}
	if target < 0 {
		t.Fatal("expected execution.read_only menu item")
	}
	m.configMenu.Selected = target
	m.rebuildConfigViewportContent()
	before := m.configMenu.Items[target].RawValue

	modelOut, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	switch updated := modelOut.(type) {
	case sessionModel:
		m = updated
	case *sessionModel:
		m = *updated
	default:
		t.Fatalf("unexpected model type %T", modelOut)
	}
	after := m.configMenu.Items[target].RawValue
	if before == after {
		t.Fatalf("expected space to toggle/cycle selected item, before=%q after=%q", before, after)
	}
}
