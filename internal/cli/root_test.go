package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tops/internal/chatstore"
)

func TestModeCommandFailsFastOnMissingConfig(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := NewRootCommand(RootOptions{Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader("")})
	cmd.SetArgs([]string{"ask", "why", "--config", filepath.Join(t.TempDir(), "missing.json")})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected configuration error")
	}
	if !strings.Contains(err.Error(), "configuration error") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSetupManualWritesConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := NewRootCommand(RootOptions{Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader("")})
	cmd.SetArgs([]string{"setup", "--manual", "--provider", "local", "--model", "llama3.1", "--endpoint", "http://localhost:11434/v1/chat/completions", "--config", path})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
}

func TestRootStartsTUICreatesDBAndPersistsMessages(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	dbPath := filepath.Join(dir, "chats.db")
	t.Setenv("TOPS_CHAT_DB", dbPath)

	createLocalConfig(t, configPath)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := NewRootCommand(RootOptions{Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader("/history\n/exit\n")})
	cmd.SetArgs([]string{"--config", configPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("expected TUI root command to run: %v", err)
	}
	output := stdout.String()
	if !strings.Contains(output, "Config") || !strings.Contains(output, "Chats") {
		t.Fatalf("expected visible TUI tabs in output, got: %s", output)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("expected chat db file to exist: %v", err)
	}

	store, err := chatstore.OpenSQLite(dbPath, nil)
	if err != nil {
		t.Fatalf("failed to reopen chatstore: %v", err)
	}
	defer store.Close()
	messages, err := store.ListRecentMessages(context.Background(), 20)
	if err != nil {
		t.Fatalf("list persisted messages failed: %v", err)
	}
	if len(messages) == 0 {
		t.Fatal("expected persisted messages after running TUI transcript")
	}
}

func TestRootHistoryDBAndSessionsCommands(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	dbPath := filepath.Join(dir, "chats.db")
	t.Setenv("TOPS_CHAT_DB", dbPath)

	createLocalConfig(t, configPath)

	firstRun := NewRootCommand(RootOptions{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("/history\n/exit\n")})
	firstRun.SetArgs([]string{"--config", configPath})
	if err := firstRun.Execute(); err != nil {
		t.Fatalf("first TUI run failed: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	secondRun := NewRootCommand(RootOptions{Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader("/history db 10\n/sessions 10\n/exit\n")})
	secondRun.SetArgs([]string{"--config", configPath})
	if err := secondRun.Execute(); err != nil {
		t.Fatalf("second TUI run failed: %v", err)
	}
	store, err := chatstore.OpenSQLite(dbPath, nil)
	if err != nil {
		t.Fatalf("failed to reopen chatstore: %v", err)
	}
	defer store.Close()
	messages, err := store.ListRecentMessages(context.Background(), 50)
	if err != nil {
		t.Fatalf("list persisted messages failed: %v", err)
	}
	var sawHistoryDB bool
	var sawSessions bool
	for _, msg := range messages {
		if msg.Kind == "history_db" {
			sawHistoryDB = true
		}
		if msg.Kind == "sessions" {
			sawSessions = true
		}
	}
	if !sawHistoryDB || !sawSessions {
		t.Fatalf("expected persisted history_db and sessions commands, got history_db=%t sessions=%t", sawHistoryDB, sawSessions)
	}
}

func TestRootMissingConfigEntersSetupWizard(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	t.Setenv("TOPS_CHAT_DB", filepath.Join(t.TempDir(), "chats.db"))
	transcript := strings.Join([]string{
		"openai",
		"gpt-5-mini",
		"TOPS_API_KEY",
		"zsh",
		"text",
		"10",
		"n",
		"",
		"n",
		"/exit",
		"",
	}, "\n")
	cmd := NewRootCommand(RootOptions{Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader(transcript)})
	cmd.SetArgs([]string{"--config", filepath.Join(t.TempDir(), "missing.json")})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("expected setup wizard flow instead of fail-fast error: %v", err)
	}
	if !strings.Contains(stdout.String(), "TOPS Setup Wizard") {
		t.Fatalf("expected setup wizard output, got: %s", stdout.String())
	}
}

func TestRootFailsFastWhenChatDBCannotOpen(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	createLocalConfig(t, configPath)

	invalidDBPath := t.TempDir()
	t.Setenv("TOPS_CHAT_DB", invalidDBPath)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := NewRootCommand(RootOptions{Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader("/exit\n")})
	cmd.SetArgs([]string{"--config", configPath})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected chat storage error")
	}
	if !strings.Contains(err.Error(), "chat storage error") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func createLocalConfig(t *testing.T, configPath string) {
	t.Helper()
	setup := NewRootCommand(RootOptions{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")})
	setup.SetArgs([]string{"setup", "--manual", "--provider", "local", "--model", "llama3.1", "--endpoint", "http://localhost:11434/v1/chat/completions", "--config", configPath})
	if err := setup.Execute(); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
}
