package chatstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"tops/internal/obs"
	"tops/internal/workflow"
)

const schemaVersion = 2

type SQLiteStore struct {
	db     *sql.DB
	logger *obs.Logger
}

func OpenSQLite(path string, logger *obs.Logger) (*SQLiteStore, error) {
	if path == "" {
		return nil, errors.New("chat DB path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create chat DB directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	store := &SQLiteStore{db: db, logger: logger}
	if err := store.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	if logger != nil && logger.Enabled() {
		logger.Printf("chatstore sqlite open path=%s", path)
	}
	return store, nil
}

func (s *SQLiteStore) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		return fmt.Errorf("enable sqlite foreign keys: %w", err)
	}
	var version int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read sqlite schema version: %w", err)
	}
	if version > schemaVersion {
		return fmt.Errorf("chat DB schema version %d is newer than supported %d", version, schemaVersion)
	}
	for version < schemaVersion {
		next := version + 1
		var statements []string
		switch next {
		case 1:
			statements = []string{
				`CREATE TABLE IF NOT EXISTS chat_sessions (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					started_at TEXT NOT NULL,
					ended_at TEXT
				)`,
				`CREATE TABLE IF NOT EXISTS chat_messages (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					session_id INTEGER NOT NULL,
					timestamp TEXT NOT NULL,
					raw_input TEXT NOT NULL,
					kind TEXT NOT NULL,
					mode TEXT,
					payload TEXT,
					output TEXT NOT NULL,
					success INTEGER NOT NULL,
					error_text TEXT,
					FOREIGN KEY(session_id) REFERENCES chat_sessions(id) ON DELETE CASCADE
				)`,
				`CREATE INDEX IF NOT EXISTS idx_chat_messages_session_id_id ON chat_messages(session_id, id)`,
				`CREATE INDEX IF NOT EXISTS idx_chat_messages_timestamp ON chat_messages(timestamp)`,
			}
		case 2:
			statements = []string{
				`CREATE TABLE IF NOT EXISTS workflow_runs (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					chat_session_id INTEGER,
					mode TEXT NOT NULL,
					input TEXT NOT NULL,
					status TEXT NOT NULL,
					started_at TEXT NOT NULL,
					ended_at TEXT,
					error_text TEXT,
					FOREIGN KEY(chat_session_id) REFERENCES chat_sessions(id) ON DELETE SET NULL
				)`,
				`CREATE TABLE IF NOT EXISTS workflow_steps (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					run_id INTEGER NOT NULL,
					step_index INTEGER NOT NULL,
					step_id TEXT NOT NULL,
					intent TEXT,
					command_name TEXT NOT NULL,
					args_json TEXT NOT NULL,
					risk_labels_json TEXT NOT NULL,
					expected_evidence TEXT,
					approved INTEGER NOT NULL,
					stdout TEXT,
					stderr TEXT,
					exit_code INTEGER,
					duration_ms INTEGER,
					error_text TEXT,
					timestamp TEXT NOT NULL,
					FOREIGN KEY(run_id) REFERENCES workflow_runs(id) ON DELETE CASCADE
				)`,
				`CREATE INDEX IF NOT EXISTS idx_workflow_runs_started_at ON workflow_runs(started_at)`,
				`CREATE INDEX IF NOT EXISTS idx_workflow_steps_run_id_step_index ON workflow_steps(run_id, step_index)`,
			}
		default:
			return fmt.Errorf("unsupported migration target version %d", next)
		}
		for _, stmt := range statements {
			if _, err := s.db.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("apply sqlite migration statement %q: %w", stmt, err)
			}
		}
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, next)); err != nil {
			return fmt.Errorf("set sqlite schema version %d: %w", next, err)
		}
		version = next
	}
	if s.logger != nil && s.logger.Enabled() {
		s.logger.Printf("chatstore sqlite migrated version=%d", schemaVersion)
	}
	return nil
}

func (s *SQLiteStore) CreateSession(ctx context.Context, startedAt time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `INSERT INTO chat_sessions(started_at) VALUES (?)`, startedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("create chat session: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read new chat session id: %w", err)
	}
	return id, nil
}

func (s *SQLiteStore) CloseSession(ctx context.Context, sessionID int64, endedAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE chat_sessions SET ended_at = ? WHERE id = ?`, endedAt.UTC().Format(time.RFC3339Nano), sessionID)
	if err != nil {
		return fmt.Errorf("close chat session %d: %w", sessionID, err)
	}
	return nil
}

func (s *SQLiteStore) InsertMessage(ctx context.Context, message MessageRecord) error {
	success := 0
	if message.Success {
		success = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO chat_messages(session_id, timestamp, raw_input, kind, mode, payload, output, success, error_text)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		message.SessionID,
		message.Timestamp.UTC().Format(time.RFC3339Nano),
		message.RawInput,
		message.Kind,
		nullableString(message.Mode),
		nullableString(message.Payload),
		message.Output,
		success,
		nullableString(message.ErrorText),
	)
	if err != nil {
		return fmt.Errorf("insert chat message: %w", err)
	}
	return nil
}

func (s *SQLiteStore) ListRecentMessages(ctx context.Context, limit int) ([]PersistedMessage, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, timestamp, raw_input, kind, COALESCE(mode, ''), COALESCE(payload, ''), output, success, COALESCE(error_text, '')
		FROM chat_messages
		ORDER BY id DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list recent chat messages: %w", err)
	}
	defer rows.Close()

	messages := make([]PersistedMessage, 0, limit)
	for rows.Next() {
		var msg PersistedMessage
		var ts string
		var successInt int
		if err := rows.Scan(&msg.ID, &msg.SessionID, &ts, &msg.RawInput, &msg.Kind, &msg.Mode, &msg.Payload, &msg.Output, &successInt, &msg.ErrorText); err != nil {
			return nil, fmt.Errorf("scan chat message row: %w", err)
		}
		parsedTS, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			return nil, fmt.Errorf("parse chat message timestamp %q: %w", ts, err)
		}
		msg.Timestamp = parsedTS
		msg.Success = successInt == 1
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chat message rows: %w", err)
	}
	return messages, nil
}

func (s *SQLiteStore) ListMessagesBySession(ctx context.Context, sessionID int64, limit int) ([]PersistedMessage, error) {
	if sessionID <= 0 {
		return nil, fmt.Errorf("list chat messages by session: session id must be > 0")
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, timestamp, raw_input, kind, COALESCE(mode, ''), COALESCE(payload, ''), output, success, COALESCE(error_text, '')
		FROM chat_messages
		WHERE session_id = ?
		ORDER BY id ASC
		LIMIT ?
	`, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("list chat messages by session: %w", err)
	}
	defer rows.Close()

	messages := make([]PersistedMessage, 0, limit)
	for rows.Next() {
		var msg PersistedMessage
		var ts string
		var successInt int
		if err := rows.Scan(&msg.ID, &msg.SessionID, &ts, &msg.RawInput, &msg.Kind, &msg.Mode, &msg.Payload, &msg.Output, &successInt, &msg.ErrorText); err != nil {
			return nil, fmt.Errorf("scan chat message row: %w", err)
		}
		parsedTS, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			return nil, fmt.Errorf("parse chat message timestamp %q: %w", ts, err)
		}
		msg.Timestamp = parsedTS
		msg.Success = successInt == 1
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chat message rows: %w", err)
	}
	return messages, nil
}

func (s *SQLiteStore) ListSessions(ctx context.Context, limit int) ([]PersistedSession, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, started_at, ended_at
		FROM chat_sessions
		ORDER BY id DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list chat sessions: %w", err)
	}
	defer rows.Close()

	sessions := make([]PersistedSession, 0, limit)
	for rows.Next() {
		var session PersistedSession
		var startedRaw string
		var endedRaw sql.NullString
		if err := rows.Scan(&session.ID, &startedRaw, &endedRaw); err != nil {
			return nil, fmt.Errorf("scan chat session row: %w", err)
		}
		startedAt, err := time.Parse(time.RFC3339Nano, startedRaw)
		if err != nil {
			return nil, fmt.Errorf("parse session started timestamp %q: %w", startedRaw, err)
		}
		session.StartedAt = startedAt
		if endedRaw.Valid {
			endedAt, err := time.Parse(time.RFC3339Nano, endedRaw.String)
			if err != nil {
				return nil, fmt.Errorf("parse session ended timestamp %q: %w", endedRaw.String, err)
			}
			session.EndedAt = &endedAt
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chat session rows: %w", err)
	}
	return sessions, nil
}

func (s *SQLiteStore) DeleteSession(ctx context.Context, sessionID int64) error {
	if sessionID <= 0 {
		return fmt.Errorf("delete chat session: session id must be > 0")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM chat_sessions WHERE id = ?`, sessionID); err != nil {
		return fmt.Errorf("delete chat session %d: %w", sessionID, err)
	}
	return nil
}

func (s *SQLiteStore) CreateWorkflowRun(ctx context.Context, record workflow.WorkflowRunRecord) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO workflow_runs(chat_session_id, mode, input, status, started_at, ended_at, error_text)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`,
		nullableInt64(record.ChatSessionID),
		record.Mode,
		record.Input,
		string(record.Status),
		record.StartedAt.UTC().Format(time.RFC3339Nano),
		nullableTime(record.EndedAt),
		nullableString(record.ErrorText),
	)
	if err != nil {
		return 0, fmt.Errorf("create workflow run: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read new workflow run id: %w", err)
	}
	return id, nil
}

func (s *SQLiteStore) UpdateWorkflowRun(ctx context.Context, runID int64, status workflow.RunStatus, endedAt time.Time, errorText string) error {
	if runID <= 0 {
		return fmt.Errorf("workflow run id must be > 0")
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE workflow_runs
		SET status = ?, ended_at = ?, error_text = ?
		WHERE id = ?
	`,
		string(status),
		endedAt.UTC().Format(time.RFC3339Nano),
		nullableString(strings.TrimSpace(errorText)),
		runID,
	)
	if err != nil {
		return fmt.Errorf("update workflow run %d: %w", runID, err)
	}
	return nil
}

func (s *SQLiteStore) InsertWorkflowStep(ctx context.Context, record workflow.WorkflowStepRecord) error {
	argsJSON, err := json.Marshal(record.Args)
	if err != nil {
		return fmt.Errorf("marshal workflow step args: %w", err)
	}
	labelsJSON, err := json.Marshal(record.RiskLabels)
	if err != nil {
		return fmt.Errorf("marshal workflow step risk labels: %w", err)
	}
	approved := 0
	if record.Approved {
		approved = 1
	}
	exitCode := any(nil)
	if record.ExitCode != 0 || strings.TrimSpace(record.ErrorText) == "" {
		exitCode = record.ExitCode
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO workflow_steps(run_id, step_index, step_id, intent, command_name, args_json, risk_labels_json, expected_evidence, approved, stdout, stderr, exit_code, duration_ms, error_text, timestamp)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		record.RunID,
		record.StepIndex,
		record.StepID,
		nullableString(record.Intent),
		record.CommandName,
		string(argsJSON),
		string(labelsJSON),
		nullableString(record.ExpectedEvidence),
		approved,
		nullableString(record.Stdout),
		nullableString(record.Stderr),
		exitCode,
		record.Duration.Milliseconds(),
		nullableString(strings.TrimSpace(record.ErrorText)),
		record.Timestamp.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("insert workflow step: %w", err)
	}
	return nil
}

func (s *SQLiteStore) PurgeAll(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin purge transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM workflow_steps`); err != nil {
		return fmt.Errorf("purge workflow steps: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM workflow_runs`); err != nil {
		return fmt.Errorf("purge workflow runs: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM chat_messages`); err != nil {
		return fmt.Errorf("purge chat messages: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM chat_sessions`); err != nil {
		return fmt.Errorf("purge chat sessions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit purge transaction: %w", err)
	}
	if s.logger != nil && s.logger.Enabled() {
		s.logger.Printf("chatstore sqlite purged")
	}
	return nil
}

func (s *SQLiteStore) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}
