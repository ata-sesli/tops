package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/phoenix-tui/phoenix/tea"

	"tops/internal/app"
	"tops/internal/config"
	"tops/internal/storage/chatstore"
)

func TestConfigPromptIsolationOnStartupAndSessionLoad(t *testing.T) {
	session := NewSessionWithOptions(SessionOptions{})
	m := newSessionModel(context.Background(), session, nil, nil)
	if m.activeTab != tabConfig {
		t.Fatalf("expected startup tab %q, got %q", tabConfig, m.activeTab)
	}
	if got := m.input.Prompt; got != "tps> " {
		t.Fatalf("expected manager prompt tps>, got %q", got)
	}
	if got := strings.TrimSpace(m.input.Value()); got != "" {
		t.Fatalf("expected empty startup input, got %q", got)
	}

	updated, _ := m.Update(chatSessionsLoadedMsg{
		Sessions: []chatstore.PersistedSession{{ID: 7, Kind: chatstore.SessionKindChat, Title: "chat"}},
	})
	m = *updated
	if m.activeTab != tabConfig {
		t.Fatalf("expected to remain on config tab, got %q", m.activeTab)
	}
	if got := m.input.Prompt; got != "tps> " {
		t.Fatalf("expected manager prompt to remain tps>, got %q", got)
	}
	if got := strings.TrimSpace(m.input.Value()); got != "" {
		t.Fatalf("expected empty config input after chat load, got %q", got)
	}
}

func TestCentralLayoutShowsTabsInConfigChatsAndOverlay(t *testing.T) {
	session := NewSessionWithOptions(SessionOptions{})
	m := newSessionModel(context.Background(), session, &app.Runtime{}, nil)

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	m = *updated
	configView := m.View()
	for _, want := range []string{"Config", "Chats", "Manager Output", "Config Menu"} {
		if !strings.Contains(configView, want) {
			t.Fatalf("expected config view to contain %q, got:\n%s", want, configView)
		}
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab, Shift: true})
	m = *updated
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
	for _, want := range []string{"Config", "Chats", "Enter: Open", "Esc: Close"} {
		if !strings.Contains(overlayView, want) {
			t.Fatalf("expected overlay view to keep shared layout and contain %q, got:\n%s", want, overlayView)
		}
	}
}

func TestConfigPanelArrowKeysScrollSingleViewport(t *testing.T) {
	session := NewSessionWithOptions(SessionOptions{})
	m := newSessionModel(context.Background(), session, &app.Runtime{}, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 14})
	m = *updated

	startSelected := m.configMenu.Selected
	for range 20 {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = *updated
	}
	if m.configMenu.Selected <= startSelected {
		t.Fatalf("expected arrow down to move selection, start=%d now=%d", startSelected, m.configMenu.Selected)
	}
	if m.configViewport.YOffset < 0 {
		t.Fatalf("expected non-negative viewport offset, got %d", m.configViewport.YOffset)
	}

	afterDownSelected := m.configMenu.Selected
	afterDownOffset := m.configViewport.YOffset
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = *updated
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
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m = *updated

	if m.configInputActive {
		t.Fatal("expected config input to be inactive by default")
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRune, Rune: 'a'})
	m = *updated
	if m.configInputActive {
		t.Fatal("expected plain keypress not to activate command input")
	}
	if got := strings.TrimSpace(m.input.Value()); got != "" {
		t.Fatalf("expected input to remain empty before slash activation, got %q", got)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRune, Rune: '/'})
	m = *updated
	if !m.configInputActive {
		t.Fatal("expected slash to activate command input")
	}
	if got := m.input.Value(); got != "/" {
		t.Fatalf("expected slash seed in input, got %q", got)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRune, Rune: 'm'})
	m = *updated
	if got := m.input.Value(); got != "/m" {
		t.Fatalf("expected command-mode editing to continue after slash, got %q", got)
	}
}

func TestConfigSpaceTogglesSelectedCycleItem(t *testing.T) {
	configPath := t.TempDir() + "/config.json"
	cfg := config.Config{
		Provider: config.ProviderConfig{
			Type:      config.ProviderYZMA,
			Model:     "llama3.1",
			ModelPath: "/tmp/llama3.1.gguf",
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
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m = *updated

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
	before := m.configMenu.Items[target].RawValue

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = *updated
	after := m.configMenu.Items[target].RawValue
	if before == after {
		t.Fatalf("expected space to toggle/cycle selected item, before=%q after=%q", before, after)
	}
}
