package chatstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tops/internal/workflow"
)

type ChatStore interface {
	CreateSession(ctx context.Context, record SessionRecord) (int64, error)
	CloseSession(ctx context.Context, sessionID int64, endedAt time.Time) error
	UpdateSessionTitle(ctx context.Context, sessionID int64, title string) error
	InsertMessage(ctx context.Context, message MessageRecord) error
	ListRecentMessages(ctx context.Context, limit int) ([]PersistedMessage, error)
	ListMessagesBySession(ctx context.Context, sessionID int64, limit int) ([]PersistedMessage, error)
	ListSessions(ctx context.Context, limit int) ([]PersistedSession, error)
	DeleteSession(ctx context.Context, sessionID int64) error
	CreateWorkflowRun(ctx context.Context, record workflow.WorkflowRunRecord) (int64, error)
	UpdateWorkflowRun(ctx context.Context, runID int64, status workflow.RunStatus, endedAt time.Time, errorText string) error
	InsertWorkflowStep(ctx context.Context, record workflow.WorkflowStepRecord) error
	PurgeAll(ctx context.Context) error
	Close() error
}

type PersistedSession struct {
	ID        int64
	Kind      SessionKind
	Title     string
	StartedAt time.Time
	UpdatedAt time.Time
	EndedAt   *time.Time
}

type PersistedMessage struct {
	ID        int64
	SessionID int64
	Timestamp time.Time
	Source    string
	RawInput  string
	Kind      string
	Mode      string
	Payload   string
	Output    string
	Success   bool
	ErrorText string
}

type MessageRecord struct {
	SessionID int64
	Timestamp time.Time
	Source    string
	RawInput  string
	Kind      string
	Mode      string
	Payload   string
	Output    string
	Success   bool
	ErrorText string
}

type SessionKind string

const (
	SessionKindManager SessionKind = "manager"
	SessionKindChat    SessionKind = "chat"
)

type SessionRecord struct {
	Kind      SessionKind
	Title     string
	StartedAt time.Time
	UpdatedAt time.Time
}

func DefaultPath() (string, error) {
	if override := strings.TrimSpace(os.Getenv("TOPS_CHAT_DB")); override != "" {
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory for chat DB: %w", err)
	}
	return filepath.Join(home, ".tops", "chats.db"), nil
}
