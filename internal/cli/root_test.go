package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tops/internal/config"
	"tops/internal/storage/chatstore"
	"tops/internal/storage/commandmemory"
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
	cmd.SetArgs([]string{"setup", "--manual", "--provider", "yzma", "--model", "llama3.1", "--config", path})
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

func TestLocalPathsListAddRemove(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configPath := filepath.Join(t.TempDir(), "config.json")
	createLocalConfig(t, configPath)

	var out bytes.Buffer
	cmd := NewRootCommand(RootOptions{Stdout: &out, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")})
	cmd.SetArgs([]string{"local", "paths", "list", "--config", configPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("paths list failed: %v", err)
	}
	defaultDir := filepath.Join(home, ".tops", "models")
	if !strings.Contains(out.String(), defaultDir) {
		t.Fatalf("expected default models path in output, got: %s", out.String())
	}

	extraDir := filepath.Join(t.TempDir(), "extra-models")
	out.Reset()
	cmd = NewRootCommand(RootOptions{Stdout: &out, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")})
	cmd.SetArgs([]string{"local", "paths", "add", extraDir, "--config", configPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("paths add failed: %v", err)
	}
	if !strings.Contains(out.String(), "Added model scan path") {
		t.Fatalf("expected add confirmation, got: %s", out.String())
	}

	out.Reset()
	cmd = NewRootCommand(RootOptions{Stdout: &out, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")})
	cmd.SetArgs([]string{"local", "paths", "remove", extraDir, "--config", configPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("paths remove failed: %v", err)
	}
	if !strings.Contains(out.String(), "Removed model scan path") {
		t.Fatalf("expected remove confirmation, got: %s", out.String())
	}
}

func TestLocalDoctorYZMAReturnsStructuredFailure(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	createLocalConfig(t, configPath)

	var out bytes.Buffer
	cmd := NewRootCommand(RootOptions{Stdout: &out, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")})
	cmd.SetArgs([]string{"local", "doctor", "--yzma", "--json", "--config", configPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("local doctor failed: %v", err)
	}
	body := out.String()
	if !strings.Contains(body, `"status": "failed"`) {
		t.Fatalf("expected failed doctor status output, got: %s", body)
	}
	if !strings.Contains(body, "missing model_path") {
		t.Fatalf("expected missing model_path reason in doctor output, got: %s", body)
	}
}

func TestLocalStatusProbeMarksNotReadyOnLiveProbeFailure(t *testing.T) {
	modelPath := filepath.Join(t.TempDir(), "model.gguf")
	if err := os.WriteFile(modelPath, []byte("gguf"), 0o644); err != nil {
		t.Fatalf("seed model failed: %v", err)
	}
	libPath := filepath.Join(t.TempDir(), "empty-libs")
	if err := os.MkdirAll(libPath, 0o755); err != nil {
		t.Fatalf("mkdir libs failed: %v", err)
	}
	cfg := config.Config{
		Provider: config.ProviderConfig{
			Type:      config.ProviderYZMA,
			Model:     "gemma4:e4b",
			ModelPath: modelPath,
			LibPath:   libPath,
		},
		Shell:      "zsh",
		Output:     config.OutputConfig{Format: "text"},
		Inspection: config.InspectionConfig{TimeoutSeconds: 10},
	}
	if err := cfg.ApplyDefaults(); err != nil {
		t.Fatalf("apply defaults failed: %v", err)
	}
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := config.SaveAtomic(configPath, cfg); err != nil {
		t.Fatalf("save config failed: %v", err)
	}

	var out bytes.Buffer
	cmd := NewRootCommand(RootOptions{Stdout: &out, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")})
	cmd.SetArgs([]string{"local", "status", "--probe", "--json", "--config", configPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("local status --probe failed: %v", err)
	}
	var status map[string]any
	if err := json.Unmarshal(out.Bytes(), &status); err != nil {
		t.Fatalf("unmarshal status output failed: %v, output: %s", err, out.String())
	}
	ready, _ := status["ready"].(bool)
	if ready {
		t.Fatalf("expected ready=false when live probe fails, got output: %s", out.String())
	}
	probeRan, _ := status["probe_ran"].(bool)
	if !probeRan {
		t.Fatalf("expected probe_ran=true, got output: %s", out.String())
	}
	probeStatus, _ := status["probe_status"].(string)
	if !strings.EqualFold(strings.TrimSpace(probeStatus), "failed") {
		t.Fatalf("expected probe_status=failed, got output: %s", out.String())
	}
}

func TestLocalBuildYZMALibsRejectsNonMetalBackend(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	createLocalConfig(t, configPath)

	var out bytes.Buffer
	cmd := NewRootCommand(RootOptions{Stdout: &out, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")})
	cmd.SetArgs([]string{"local", "build-yzma-libs", "--backend", "cpu", "--json", "--config", configPath})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected non-metal backend to fail")
	}
	body := out.String()
	if !strings.Contains(body, `"status": "failed"`) {
		t.Fatalf("expected failed status output, got: %s", body)
	}
	if !strings.Contains(strings.ToLower(body), "metal") {
		t.Fatalf("expected metal-only guidance in output, got: %s", body)
	}
}

func TestLocalMetalCheckReturnsStructuredOutput(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	createLocalConfig(t, configPath)

	var out bytes.Buffer
	cmd := NewRootCommand(RootOptions{Stdout: &out, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")})
	cmd.SetArgs([]string{"local", "metal-check", "--json", "--config", configPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("local metal-check failed: %v", err)
	}
	body := out.String()
	if !strings.Contains(body, `"status":`) {
		t.Fatalf("expected structured status output, got: %s", body)
	}
	if !strings.Contains(body, `"probe_source": "metal_check"`) {
		t.Fatalf("expected metal_check probe source in output, got: %s", body)
	}
}

func TestRunCommandShowsEmptyMemoryHint(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	createLocalConfig(t, configPath)
	dbPath := filepath.Join(t.TempDir(), "command-memory.db")
	t.Setenv("TOPS_COMMAND_MEMORY_DB", dbPath)

	var out bytes.Buffer
	cmd := NewRootCommand(RootOptions{Stdout: &out, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("q\n")})
	cmd.SetArgs([]string{"run", "--config", configPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("run command failed: %v", err)
	}
	body := out.String()
	if !strings.Contains(body, "No command memory yet. Use `tps gen` to generate commands first.") {
		t.Fatalf("expected empty memory hint, got: %s", body)
	}
}

func TestRunCommandExecutesSelectedReadOnlyCommand(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	createLocalConfig(t, configPath)
	store := openTestCommandMemory(t)
	defer store.Close()
	seed, err := store.UpsertGenerated(context.Background(), commandmemory.UpsertInput{
		Prompt:      "show current directory",
		CommandText: "pwd",
		OutputKind:  "single_command",
		Shell:       "zsh",
	})
	if err != nil {
		t.Fatalf("seed command memory failed: %v", err)
	}
	_ = seed

	var out bytes.Buffer
	cmd := NewRootCommand(RootOptions{Stdout: &out, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("1\nq\n")})
	cmd.SetArgs([]string{"run", "--config", configPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("run command failed: %v", err)
	}
	cwd, _ := os.Getwd()
	if !strings.Contains(out.String(), strings.TrimSpace(cwd)) {
		t.Fatalf("expected pwd output in run output, got: %s", out.String())
	}
	items, err := store.Search(context.Background(), commandmemory.SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("search after run failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one command memory item, got %d", len(items))
	}
	if items[0].UseCount != 1 || items[0].SuccessCount != 1 || items[0].FailureCount != 0 {
		t.Fatalf("unexpected run stats: %+v", items[0])
	}
}

func TestRunCommandRiskySelectionRequiresConfirmation(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	createLocalConfig(t, configPath)
	store := openTestCommandMemory(t)
	defer store.Close()
	item, err := store.UpsertGenerated(context.Background(), commandmemory.UpsertInput{
		Prompt:      "clean build artifacts",
		CommandText: "rm -rf build/",
		OutputKind:  "single_command",
		Shell:       "zsh",
	})
	if err != nil {
		t.Fatalf("seed risky item failed: %v", err)
	}
	_ = item

	var out bytes.Buffer
	cmd := NewRootCommand(RootOptions{Stdout: &out, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("1\nn\nq\n")})
	cmd.SetArgs([]string{"run", "--config", configPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("run command failed: %v", err)
	}
	body := out.String()
	if !strings.Contains(body, "Run? [y/N]:") {
		t.Fatalf("expected confirmation prompt, got: %s", body)
	}
	got, found, err := store.GetByID(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("get by id failed: %v", err)
	}
	if !found {
		t.Fatalf("expected item %d to exist", item.ID)
	}
	if got.UseCount != 0 {
		t.Fatalf("expected canceled run not to update use count, got %d", got.UseCount)
	}
}

func TestRunCommandHideAndPinActions(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	createLocalConfig(t, configPath)
	store := openTestCommandMemory(t)
	defer store.Close()
	_, err := store.UpsertGenerated(context.Background(), commandmemory.UpsertInput{
		Prompt:      "list files",
		CommandText: "ls -a",
		OutputKind:  "single_command",
		Shell:       "zsh",
	})
	if err != nil {
		t.Fatalf("seed first item failed: %v", err)
	}
	_, err = store.UpsertGenerated(context.Background(), commandmemory.UpsertInput{
		Prompt:      "show git status",
		CommandText: "git status --short",
		OutputKind:  "single_command",
		Shell:       "zsh",
	})
	if err != nil {
		t.Fatalf("seed second item failed: %v", err)
	}
	initial, err := store.Search(context.Background(), commandmemory.SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("initial search failed: %v", err)
	}
	if len(initial) != 2 {
		t.Fatalf("expected two initial items, got %d", len(initial))
	}
	pinID := initial[0].ID
	hideID := initial[1].ID

	var out bytes.Buffer
	cmd := NewRootCommand(RootOptions{Stdout: &out, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("p1\nd2\nq\n")})
	cmd.SetArgs([]string{"run", "--config", configPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("run command failed: %v", err)
	}
	items, err := store.Search(context.Background(), commandmemory.SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("search after pin/hide failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one visible item after hide, got %d", len(items))
	}
	if items[0].ID != pinID {
		t.Fatalf("expected pinned item %d to remain visible, got id=%d (hidden id=%d)", pinID, items[0].ID, hideID)
	}
	if !items[0].Pinned {
		t.Fatalf("expected second item to be pinned")
	}
}

func createLocalConfig(t *testing.T, configPath string) {
	t.Helper()
	setup := NewRootCommand(RootOptions{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")})
	setup.SetArgs([]string{"setup", "--manual", "--provider", "yzma", "--model", "llama3.1", "--config", configPath})
	if err := setup.Execute(); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
}

func openTestCommandMemory(t *testing.T) *commandmemory.SQLiteStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "command-memory.db")
	t.Setenv("TOPS_COMMAND_MEMORY_DB", path)
	store, err := commandmemory.OpenSQLite(path, nil)
	if err != nil {
		t.Fatalf("open command memory failed: %v", err)
	}
	return store
}
