package tui

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"tops/internal/app"
	"tops/internal/chatstore"
	"tops/internal/config"
	"tops/internal/workflow"
)

type fakeStore struct {
	nextSessionID    int64
	created          []int64
	closed           []int64
	inserted         []chatstore.MessageRecord
	purgeCount       int
	deletedSessionID []int64
	messages         []chatstore.PersistedMessage
	sessionMessages  map[int64][]chatstore.PersistedMessage
	sessions         []chatstore.PersistedSession
}

func (f *fakeStore) CreateSession(ctx context.Context, record chatstore.SessionRecord) (int64, error) {
	f.nextSessionID++
	f.created = append(f.created, f.nextSessionID)
	return f.nextSessionID, nil
}

func (f *fakeStore) CloseSession(ctx context.Context, sessionID int64, endedAt time.Time) error {
	f.closed = append(f.closed, sessionID)
	return nil
}

func (f *fakeStore) UpdateSessionTitle(ctx context.Context, sessionID int64, title string) error {
	return nil
}

func (f *fakeStore) InsertMessage(ctx context.Context, message chatstore.MessageRecord) error {
	f.inserted = append(f.inserted, message)
	return nil
}

func (f *fakeStore) ListRecentMessages(ctx context.Context, limit int) ([]chatstore.PersistedMessage, error) {
	return f.messages, nil
}

func (f *fakeStore) ListMessagesBySession(ctx context.Context, sessionID int64, limit int) ([]chatstore.PersistedMessage, error) {
	if f.sessionMessages == nil {
		return nil, nil
	}
	return f.sessionMessages[sessionID], nil
}

func (f *fakeStore) ListSessions(ctx context.Context, limit int) ([]chatstore.PersistedSession, error) {
	return f.sessions, nil
}

func (f *fakeStore) DeleteSession(ctx context.Context, sessionID int64) error {
	f.deletedSessionID = append(f.deletedSessionID, sessionID)
	return nil
}

func (f *fakeStore) CreateWorkflowRun(ctx context.Context, record workflow.WorkflowRunRecord) (int64, error) {
	return 1, nil
}

func (f *fakeStore) UpdateWorkflowRun(ctx context.Context, runID int64, status workflow.RunStatus, endedAt time.Time, errorText string) error {
	return nil
}

func (f *fakeStore) InsertWorkflowStep(ctx context.Context, record workflow.WorkflowStepRecord) error {
	return nil
}

func (f *fakeStore) PurgeAll(ctx context.Context) error {
	f.purgeCount++
	return nil
}

func (f *fakeStore) Close() error {
	return nil
}

func TestSessionManagerHistoryAndClear(t *testing.T) {
	store := &fakeStore{}
	var out bytes.Buffer
	session := NewSessionWithOptions(SessionOptions{
		Store:      store,
		ConfigPath: "/tmp/test-config.json",
	})
	session.now = func() time.Time { return time.Date(2026, 3, 22, 10, 0, 0, 0, time.UTC) }

	rt := app.Runtime{
		Config: config.Config{
			Provider: config.ProviderConfig{Type: config.ProviderLocal, Model: "llama3.1", Endpoint: "http://localhost:11434"},
			Shell:    "zsh",
		},
	}
	in := strings.NewReader("/ask why is disk usage high\n/history\n/clear\n/history\n/exit\n")
	if err := session.Run(context.Background(), in, &out, rt); err != nil {
		t.Fatalf("session run failed: %v", err)
	}

	if len(store.inserted) == 0 {
		t.Fatal("expected persisted records to be inserted")
	}
	if len(store.created) == 0 || len(store.closed) == 0 {
		t.Fatalf("expected persistence session create/close, got created=%d closed=%d", len(store.created), len(store.closed))
	}
	if store.purgeCount != 0 {
		t.Fatalf("expected no purge calls, got %d", store.purgeCount)
	}
}

func TestSessionPersistentCommandsAndPurge(t *testing.T) {
	store := &fakeStore{
		messages: []chatstore.PersistedMessage{{ID: 7, SessionID: 2, Timestamp: time.Date(2026, 3, 22, 9, 0, 0, 0, time.UTC), Kind: "mode", Mode: "ask", RawInput: "/ask x", Output: "x", Success: true}},
		sessions: []chatstore.PersistedSession{{ID: 2, StartedAt: time.Date(2026, 3, 22, 8, 0, 0, 0, time.UTC)}},
	}
	var out bytes.Buffer
	session := NewSessionWithOptions(SessionOptions{
		Store:      store,
		ConfigPath: "/tmp/test-config.json",
	})
	session.now = time.Now

	rt := app.Runtime{
		Config: config.Config{
			Provider: config.ProviderConfig{Type: config.ProviderLocal, Model: "llama3.1", Endpoint: "http://localhost:11434"},
			Shell:    "zsh",
		},
	}

	in := strings.NewReader("/history db 5\n/sessions 5\n/purge confirm\n/history db 5\n/exit\n")
	if err := session.Run(context.Background(), in, &out, rt); err != nil {
		t.Fatalf("session run failed: %v", err)
	}

	if store.purgeCount != 1 {
		t.Fatalf("expected one purge call, got %d", store.purgeCount)
	}
	if len(store.created) < 2 {
		t.Fatalf("expected a new persistence session after purge, got created=%d", len(store.created))
	}
}

func TestSessionReadAndDeleteCommands(t *testing.T) {
	store := &fakeStore{
		sessionMessages: map[int64][]chatstore.PersistedMessage{
			2: {
				{ID: 11, SessionID: 2, Timestamp: time.Date(2026, 3, 22, 9, 0, 0, 0, time.UTC), Kind: "history", RawInput: "/history", Output: "ok", Success: true},
			},
		},
	}
	var out bytes.Buffer
	session := NewSessionWithOptions(SessionOptions{
		Store:      store,
		ConfigPath: "/tmp/test-config.json",
	})
	session.now = time.Now

	rt := app.Runtime{
		Config: config.Config{
			Provider: config.ProviderConfig{Type: config.ProviderLocal, Model: "llama3.1", Endpoint: "http://localhost:11434"},
			Shell:    "zsh",
		},
	}

	in := strings.NewReader("/session read 2\n/session delete 2 confirm\n/exit\n")
	if err := session.Run(context.Background(), in, &out, rt); err != nil {
		t.Fatalf("session run failed: %v", err)
	}
	if len(store.deletedSessionID) != 1 || store.deletedSessionID[0] != 2 {
		t.Fatalf("expected deleted session 2, got %v", store.deletedSessionID)
	}
}

func TestSessionNonSlashInputShowsGuidance(t *testing.T) {
	var out bytes.Buffer
	session := NewSessionWithOptions(SessionOptions{
		Store:      nil,
		ConfigPath: "/tmp/test-config.json",
	})
	session.now = time.Now

	in := strings.NewReader("hello there\n/exit\n")
	if err := session.Run(context.Background(), in, &out, app.Runtime{}); err != nil {
		t.Fatalf("session run failed: %v", err)
	}
	if len(session.history) == 0 {
		t.Fatal("expected guidance entry in in-memory history")
	}
}

func TestSessionModelsAndModelUseCommands(t *testing.T) {
	store := &fakeStore{}
	configPath := t.TempDir() + "/config.json"
	seedCfg := config.Config{
		Provider: config.ProviderConfig{
			Type:     config.ProviderLocal,
			Model:    "llama3.1",
			Endpoint: "http://localhost:11434",
		},
		Shell:      "zsh",
		Output:     config.OutputConfig{Format: "text"},
		Inspection: config.InspectionConfig{TimeoutSeconds: 10},
	}
	if err := config.SaveAtomic(configPath, seedCfg); err != nil {
		t.Fatalf("seed config failed: %v", err)
	}

	session := NewSessionWithOptions(SessionOptions{
		Store:         store,
		ConfigPath:    configPath,
		OllamaManager: fakeOllamaManager{models: []string{"llama3.1", "qwen2.5"}},
		RuntimeLoader: func(configPath string) (app.Runtime, error) {
			cfg, err := config.Load(configPath)
			if err != nil {
				return app.Runtime{}, err
			}
			return app.NewRuntime(cfg)
		},
	})

	rt, err := app.NewRuntime(seedCfg)
	if err != nil {
		t.Fatalf("new runtime failed: %v", err)
	}
	var out bytes.Buffer
	in := strings.NewReader("/models\n/model use 2\n/exit\n")
	if err := session.Run(context.Background(), in, &out, rt); err != nil {
		t.Fatalf("session run failed: %v", err)
	}

	var sawModels bool
	var sawModelUse bool
	for _, rec := range store.inserted {
		if rec.Kind == string(KindModels) {
			sawModels = true
		}
		if rec.Kind == string(KindModelUse) {
			sawModelUse = true
		}
	}
	if !sawModels || !sawModelUse {
		t.Fatalf("expected persisted models/model_use commands, got models=%t model_use=%t", sawModels, sawModelUse)
	}

	loadedCfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load config failed: %v", err)
	}
	if loadedCfg.Provider.Model != "qwen2.5" {
		t.Fatalf("expected persisted model qwen2.5, got %q", loadedCfg.Provider.Model)
	}
}
