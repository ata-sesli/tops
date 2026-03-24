package chatstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"tops/internal/workflow"
)

func TestSQLiteMigrationIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "chats.db")

	store1, err := OpenSQLite(path, nil)
	if err != nil {
		t.Fatalf("open store 1 failed: %v", err)
	}
	sessionID1, err := store1.CreateSession(ctx, time.Now())
	if err != nil {
		t.Fatalf("create session 1 failed: %v", err)
	}
	if sessionID1 <= 0 {
		t.Fatalf("invalid session id 1: %d", sessionID1)
	}
	if err := store1.Close(); err != nil {
		t.Fatalf("close store 1 failed: %v", err)
	}

	store2, err := OpenSQLite(path, nil)
	if err != nil {
		t.Fatalf("open store 2 failed: %v", err)
	}
	defer store2.Close()
	sessionID2, err := store2.CreateSession(ctx, time.Now())
	if err != nil {
		t.Fatalf("create session 2 failed: %v", err)
	}
	if sessionID2 <= sessionID1 {
		t.Fatalf("expected monotonically increasing session ids, got first=%d second=%d", sessionID1, sessionID2)
	}
}

func TestSQLiteInsertListCloseAndPurge(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "chats.db")
	store, err := OpenSQLite(path, nil)
	if err != nil {
		t.Fatalf("open store failed: %v", err)
	}
	defer store.Close()

	sessionID, err := store.CreateSession(ctx, time.Date(2026, 3, 22, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("create session failed: %v", err)
	}

	err = store.InsertMessage(ctx, MessageRecord{
		SessionID: sessionID,
		Timestamp: time.Date(2026, 3, 22, 10, 1, 0, 0, time.UTC),
		RawInput:  "/ask why",
		Kind:      "mode",
		Mode:      "ask",
		Payload:   "why",
		Output:    "answer",
		Success:   true,
	})
	if err != nil {
		t.Fatalf("insert message 1 failed: %v", err)
	}

	err = store.InsertMessage(ctx, MessageRecord{
		SessionID: sessionID,
		Timestamp: time.Date(2026, 3, 22, 10, 2, 0, 0, time.UTC),
		RawInput:  "/history",
		Kind:      "history",
		Output:    "No history yet.",
		Success:   true,
	})
	if err != nil {
		t.Fatalf("insert message 2 failed: %v", err)
	}

	messages, err := store.ListRecentMessages(ctx, 10)
	if err != nil {
		t.Fatalf("list messages failed: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}
	if messages[0].RawInput != "/history" {
		t.Fatalf("expected newest message first, got %q", messages[0].RawInput)
	}

	sessionMessages, err := store.ListMessagesBySession(ctx, sessionID, 10)
	if err != nil {
		t.Fatalf("list messages by session failed: %v", err)
	}
	if len(sessionMessages) != 2 {
		t.Fatalf("expected 2 messages in session list, got %d", len(sessionMessages))
	}
	if sessionMessages[0].RawInput != "/ask why" {
		t.Fatalf("expected oldest message first in session list, got %q", sessionMessages[0].RawInput)
	}

	if err := store.CloseSession(ctx, sessionID, time.Date(2026, 3, 22, 10, 3, 0, 0, time.UTC)); err != nil {
		t.Fatalf("close session failed: %v", err)
	}

	sessions, err := store.ListSessions(ctx, 10)
	if err != nil {
		t.Fatalf("list sessions failed: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].EndedAt == nil {
		t.Fatal("expected ended_at to be populated after CloseSession")
	}

	if err := store.DeleteSession(ctx, sessionID); err != nil {
		t.Fatalf("delete session failed: %v", err)
	}
	messagesAfterDelete, err := store.ListRecentMessages(ctx, 10)
	if err != nil {
		t.Fatalf("list messages after delete failed: %v", err)
	}
	if len(messagesAfterDelete) != 0 {
		t.Fatalf("expected messages to cascade-delete with session, got %d", len(messagesAfterDelete))
	}

	if err := store.PurgeAll(ctx); err != nil {
		t.Fatalf("purge failed: %v", err)
	}

	messagesAfterPurge, err := store.ListRecentMessages(ctx, 10)
	if err != nil {
		t.Fatalf("list messages after purge failed: %v", err)
	}
	if len(messagesAfterPurge) != 0 {
		t.Fatalf("expected 0 messages after purge, got %d", len(messagesAfterPurge))
	}

	sessionsAfterPurge, err := store.ListSessions(ctx, 10)
	if err != nil {
		t.Fatalf("list sessions after purge failed: %v", err)
	}
	if len(sessionsAfterPurge) != 0 {
		t.Fatalf("expected 0 sessions after purge, got %d", len(sessionsAfterPurge))
	}
}

func TestSQLiteWorkflowAuditPersistence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "chats.db")
	store, err := OpenSQLite(path, nil)
	if err != nil {
		t.Fatalf("open store failed: %v", err)
	}
	defer store.Close()

	runID, err := store.CreateWorkflowRun(ctx, workflow.WorkflowRunRecord{
		Mode:      "ask",
		Input:     "what is my os",
		Status:    workflow.RunStatusBlocked,
		StartedAt: time.Date(2026, 3, 24, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create workflow run failed: %v", err)
	}
	if runID <= 0 {
		t.Fatalf("invalid workflow run id: %d", runID)
	}
	if err := store.InsertWorkflowStep(ctx, workflow.WorkflowStepRecord{
		RunID:            runID,
		StepIndex:        1,
		StepID:           "s1",
		CommandName:      "pwd",
		Args:             []string{},
		RiskLabels:       []string{"read-only"},
		Approved:         true,
		ExitCode:         0,
		Duration:         5 * time.Millisecond,
		Stdout:           "/tmp/project",
		ExpectedEvidence: "cwd",
		Timestamp:        time.Date(2026, 3, 24, 10, 0, 1, 0, time.UTC),
	}); err != nil {
		t.Fatalf("insert workflow step failed: %v", err)
	}
	if err := store.UpdateWorkflowRun(ctx, runID, workflow.RunStatusCompleted, time.Date(2026, 3, 24, 10, 0, 2, 0, time.UTC), ""); err != nil {
		t.Fatalf("update workflow run failed: %v", err)
	}

	var runCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_runs`).Scan(&runCount); err != nil {
		t.Fatalf("count workflow runs failed: %v", err)
	}
	if runCount != 1 {
		t.Fatalf("expected 1 workflow run row, got %d", runCount)
	}
	var stepCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_steps`).Scan(&stepCount); err != nil {
		t.Fatalf("count workflow steps failed: %v", err)
	}
	if stepCount != 1 {
		t.Fatalf("expected 1 workflow step row, got %d", stepCount)
	}
}
