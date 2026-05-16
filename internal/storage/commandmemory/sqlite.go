package commandmemory

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"tops/internal/obs"
)

const schemaVersion = 1

type SQLiteStore struct {
	db     *sql.DB
	logger *obs.Logger
}

func OpenSQLite(path string, logger *obs.Logger) (*SQLiteStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("command memory DB path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create command memory DB directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open command memory sqlite database: %w", err)
	}
	store := &SQLiteStore{db: db, logger: logger}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	if logger != nil && logger.Enabled() {
		logger.Printf("commandmemory sqlite open path=%s", path)
	}
	return store, nil
}

func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *SQLiteStore) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		return fmt.Errorf("enable sqlite foreign keys: %w", err)
	}
	var version int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read command memory schema version: %w", err)
	}
	if version > schemaVersion {
		return fmt.Errorf("command memory DB schema version %d is newer than supported %d", version, schemaVersion)
	}
	for version < schemaVersion {
		next := version + 1
		switch next {
		case 1:
			stmts := []string{
				`CREATE TABLE IF NOT EXISTS command_memory (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					title TEXT NOT NULL,
					prompt TEXT NOT NULL,
					command_text TEXT NOT NULL DEFAULT '',
					script_text TEXT NOT NULL DEFAULT '',
					output_kind TEXT NOT NULL DEFAULT 'single_command',
					shell TEXT NOT NULL DEFAULT 'unknown',
					explanation TEXT NOT NULL DEFAULT '',
					risk TEXT NOT NULL DEFAULT '',
					normalized_artifact TEXT NOT NULL DEFAULT '',
					context_key TEXT NOT NULL DEFAULT '',
					created_at TEXT NOT NULL,
					updated_at TEXT NOT NULL,
					last_used_at TEXT,
					use_count INTEGER NOT NULL DEFAULT 0,
					success_count INTEGER NOT NULL DEFAULT 0,
					failure_count INTEGER NOT NULL DEFAULT 0,
					last_exit_code INTEGER,
					cwd TEXT NOT NULL DEFAULT '',
					project_root TEXT NOT NULL DEFAULT '',
					project_fingerprint TEXT NOT NULL DEFAULT '',
					pinned INTEGER NOT NULL DEFAULT 0,
					deleted_at TEXT
				)`,
				`CREATE INDEX IF NOT EXISTS idx_command_memory_active_updated
					ON command_memory(deleted_at, pinned DESC, updated_at DESC, id DESC)`,
				`CREATE INDEX IF NOT EXISTS idx_command_memory_artifact
					ON command_memory(normalized_artifact, shell, context_key)`,
			}
			for _, stmt := range stmts {
				if _, err := s.db.ExecContext(ctx, stmt); err != nil {
					return fmt.Errorf("apply command memory migration statement %q: %w", stmt, err)
				}
			}
		default:
			return fmt.Errorf("unsupported command memory migration target version %d", next)
		}
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, next)); err != nil {
			return fmt.Errorf("set command memory schema version %d: %w", next, err)
		}
		version = next
	}
	if s.logger != nil && s.logger.Enabled() {
		s.logger.Printf("commandmemory sqlite migrated version=%d", schemaVersion)
	}
	return nil
}

func (s *SQLiteStore) UpsertGenerated(ctx context.Context, in UpsertInput) (Item, error) {
	now := time.Now().UTC()
	in.CommandText = strings.TrimSpace(in.CommandText)
	in.ScriptText = strings.TrimSpace(in.ScriptText)
	in.OutputKind = normalizeOutputKind(in.OutputKind)
	in.Shell = normalizeShell(in.Shell)
	in.Explanation = strings.TrimSpace(in.Explanation)
	in.Prompt = strings.TrimSpace(in.Prompt)
	in.CWD = strings.TrimSpace(in.CWD)
	in.ProjectRoot = strings.TrimSpace(in.ProjectRoot)
	in.ProjectFingerprint = strings.TrimSpace(in.ProjectFingerprint)
	in.Risk = normalizeRisk(in.Risk)

	normalizedArtifact := NormalizeArtifact(in.OutputKind, in.CommandText, in.ScriptText)
	contextKey := NormalizeContextKey(in.ProjectRoot, in.CWD)
	artifactForTitle := in.CommandText
	if in.OutputKind == "shell_script" && in.ScriptText != "" {
		artifactForTitle = in.ScriptText
	}
	title := inferTitleFromPrompt(in.Title, artifactForTitle)
	if strings.TrimSpace(title) == "" {
		title = inferTitleFromPrompt(in.Prompt, artifactForTitle)
	}
	if strings.TrimSpace(title) == "" {
		title = "Generated command"
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Item{}, fmt.Errorf("start command memory transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	var existingID int64
	err = tx.QueryRowContext(ctx, `
		SELECT id
		FROM command_memory
		WHERE deleted_at IS NULL
		  AND normalized_artifact = ?
		  AND shell = ?
		  AND context_key = ?
		ORDER BY id DESC
		LIMIT 1
	`, normalizedArtifact, in.Shell, contextKey).Scan(&existingID)

	switch {
	case err == nil:
		_, err = tx.ExecContext(ctx, `
			UPDATE command_memory
			SET title = ?,
			    prompt = ?,
			    command_text = ?,
			    script_text = ?,
			    output_kind = ?,
			    shell = ?,
			    explanation = ?,
			    risk = ?,
			    normalized_artifact = ?,
			    context_key = ?,
			    cwd = ?,
			    project_root = ?,
			    project_fingerprint = ?,
			    updated_at = ?,
			    deleted_at = NULL
			WHERE id = ?
		`,
			title,
			in.Prompt,
			in.CommandText,
			in.ScriptText,
			in.OutputKind,
			in.Shell,
			in.Explanation,
			in.Risk,
			normalizedArtifact,
			contextKey,
			in.CWD,
			in.ProjectRoot,
			in.ProjectFingerprint,
			now.Format(time.RFC3339Nano),
			existingID,
		)
		if err != nil {
			return Item{}, fmt.Errorf("update command memory item: %w", err)
		}
	case err == sql.ErrNoRows:
		result, insertErr := tx.ExecContext(ctx, `
			INSERT INTO command_memory(
				title, prompt, command_text, script_text, output_kind, shell, explanation, risk,
				normalized_artifact, context_key, created_at, updated_at,
				cwd, project_root, project_fingerprint
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			title,
			in.Prompt,
			in.CommandText,
			in.ScriptText,
			in.OutputKind,
			in.Shell,
			in.Explanation,
			in.Risk,
			normalizedArtifact,
			contextKey,
			now.Format(time.RFC3339Nano),
			now.Format(time.RFC3339Nano),
			in.CWD,
			in.ProjectRoot,
			in.ProjectFingerprint,
		)
		if insertErr != nil {
			return Item{}, fmt.Errorf("insert command memory item: %w", insertErr)
		}
		existingID, err = result.LastInsertId()
		if err != nil {
			return Item{}, fmt.Errorf("read inserted command memory id: %w", err)
		}
	default:
		return Item{}, fmt.Errorf("check duplicate command memory item: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Item{}, fmt.Errorf("commit command memory transaction: %w", err)
	}
	item, found, err := s.GetByID(ctx, existingID)
	if err != nil {
		return Item{}, err
	}
	if !found {
		return Item{}, fmt.Errorf("command memory item %d not found after upsert", existingID)
	}
	return item, nil
}

func (s *SQLiteStore) GetByID(ctx context.Context, id int64) (Item, bool, error) {
	if id <= 0 {
		return Item{}, false, nil
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT id, title, prompt, command_text, script_text, output_kind, shell, explanation, risk,
		       created_at, updated_at, COALESCE(last_used_at, ''), use_count, success_count, failure_count,
		       COALESCE(last_exit_code, ''), cwd, project_root, project_fingerprint, pinned, COALESCE(deleted_at, '')
		FROM command_memory
		WHERE id = ?
	`, id)
	item, ok, err := scanItem(row.Scan)
	if err != nil {
		if err == sql.ErrNoRows {
			return Item{}, false, nil
		}
		return Item{}, false, fmt.Errorf("get command memory item %d: %w", id, err)
	}
	return item, ok, nil
}

func (s *SQLiteStore) Search(ctx context.Context, opts SearchOptions) ([]Item, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, title, prompt, command_text, script_text, output_kind, shell, explanation, risk,
		       created_at, updated_at, COALESCE(last_used_at, ''), use_count, success_count, failure_count,
		       COALESCE(last_exit_code, ''), cwd, project_root, project_fingerprint, pinned, COALESCE(deleted_at, '')
		FROM command_memory
		WHERE deleted_at IS NULL
		ORDER BY pinned DESC, updated_at DESC, id DESC
		LIMIT 800
	`)
	if err != nil {
		return nil, fmt.Errorf("query command memory items: %w", err)
	}
	defer rows.Close()

	items := make([]Item, 0, 128)
	for rows.Next() {
		item, ok, scanErr := scanItem(rows.Scan)
		if scanErr != nil {
			return nil, fmt.Errorf("scan command memory item: %w", scanErr)
		}
		if !ok {
			continue
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate command memory items: %w", err)
	}

	query := strings.ToLower(strings.TrimSpace(opts.Query))
	queryTokens := strings.Fields(query)
	now := time.Now().UTC()
	filtered := make([]Item, 0, len(items))
	for _, item := range items {
		score := scoreItem(item, query, queryTokens, strings.TrimSpace(opts.CWD), strings.TrimSpace(opts.ProjectRoot), now)
		if query != "" && score <= 0 {
			continue
		}
		item.Score = score
		filtered = append(filtered, item)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].Score != filtered[j].Score {
			return filtered[i].Score > filtered[j].Score
		}
		if filtered[i].Pinned != filtered[j].Pinned {
			return filtered[i].Pinned
		}
		if filtered[i].UseCount != filtered[j].UseCount {
			return filtered[i].UseCount > filtered[j].UseCount
		}
		leftLast := time.Time{}
		rightLast := time.Time{}
		if filtered[i].LastUsedAt != nil {
			leftLast = filtered[i].LastUsedAt.UTC()
		}
		if filtered[j].LastUsedAt != nil {
			rightLast = filtered[j].LastUsedAt.UTC()
		}
		if !leftLast.Equal(rightLast) {
			return leftLast.After(rightLast)
		}
		if !filtered[i].UpdatedAt.Equal(filtered[j].UpdatedAt) {
			return filtered[i].UpdatedAt.After(filtered[j].UpdatedAt)
		}
		return filtered[i].ID > filtered[j].ID
	})
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, nil
}

func (s *SQLiteStore) Hide(ctx context.Context, id int64) error {
	if id <= 0 {
		return fmt.Errorf("invalid command memory id")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
		UPDATE command_memory
		SET deleted_at = ?, updated_at = ?
		WHERE id = ?
	`, now, now, id)
	if err != nil {
		return fmt.Errorf("hide command memory item %d: %w", id, err)
	}
	return nil
}

func (s *SQLiteStore) SetPinned(ctx context.Context, id int64, pinned bool) error {
	if id <= 0 {
		return fmt.Errorf("invalid command memory id")
	}
	pin := 0
	if pinned {
		pin = 1
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE command_memory
		SET pinned = ?, updated_at = ?
		WHERE id = ?
	`, pin, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("set command memory pin state for id %d: %w", id, err)
	}
	return nil
}

func (s *SQLiteStore) RecordRun(ctx context.Context, id int64, exitCode int, success bool) error {
	if id <= 0 {
		return fmt.Errorf("invalid command memory id")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	successDelta := 0
	failureDelta := 0
	if success {
		successDelta = 1
	} else {
		failureDelta = 1
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE command_memory
		SET updated_at = ?,
		    last_used_at = ?,
		    use_count = use_count + 1,
		    success_count = success_count + ?,
		    failure_count = failure_count + ?,
		    last_exit_code = ?
		WHERE id = ?
	`, now, now, successDelta, failureDelta, exitCode, id)
	if err != nil {
		return fmt.Errorf("record command memory run for id %d: %w", id, err)
	}
	return nil
}

func scanItem(scan func(dest ...any) error) (Item, bool, error) {
	var (
		item           Item
		createdRaw     string
		updatedRaw     string
		lastUsedRaw    string
		lastExitRaw    string
		deletedAtRaw   string
		pinnedInt      int
	)
	if err := scan(
		&item.ID,
		&item.Title,
		&item.Prompt,
		&item.CommandText,
		&item.ScriptText,
		&item.OutputKind,
		&item.Shell,
		&item.Explanation,
		&item.Risk,
		&createdRaw,
		&updatedRaw,
		&lastUsedRaw,
		&item.UseCount,
		&item.SuccessCount,
		&item.FailureCount,
		&lastExitRaw,
		&item.CWD,
		&item.ProjectRoot,
		&item.ProjectFingerprint,
		&pinnedInt,
		&deletedAtRaw,
	); err != nil {
		return Item{}, false, err
	}
	createdAt, err := parseTime(createdRaw)
	if err != nil {
		return Item{}, false, fmt.Errorf("parse created_at %q: %w", createdRaw, err)
	}
	updatedAt, err := parseTime(updatedRaw)
	if err != nil {
		return Item{}, false, fmt.Errorf("parse updated_at %q: %w", updatedRaw, err)
	}
	item.CreatedAt = createdAt
	item.UpdatedAt = updatedAt
	item.Pinned = pinnedInt == 1
	item.OutputKind = normalizeOutputKind(item.OutputKind)
	item.Shell = normalizeShell(item.Shell)
	item.Risk = normalizeRisk(item.Risk)
	if strings.TrimSpace(lastUsedRaw) != "" {
		parsed, parseErr := parseTime(lastUsedRaw)
		if parseErr != nil {
			return Item{}, false, fmt.Errorf("parse last_used_at %q: %w", lastUsedRaw, parseErr)
		}
		item.LastUsedAt = &parsed
	}
	if strings.TrimSpace(deletedAtRaw) != "" {
		parsed, parseErr := parseTime(deletedAtRaw)
		if parseErr != nil {
			return Item{}, false, fmt.Errorf("parse deleted_at %q: %w", deletedAtRaw, parseErr)
		}
		item.DeletedAt = &parsed
	}
	if strings.TrimSpace(lastExitRaw) != "" {
		value, atoiErr := strconv.Atoi(strings.TrimSpace(lastExitRaw))
		if atoiErr != nil {
			return Item{}, false, fmt.Errorf("parse last_exit_code %q: %w", lastExitRaw, atoiErr)
		}
		item.LastExitCode = &value
	}
	return item, true, nil
}

func parseTime(raw string) (time.Time, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return time.Time{}, fmt.Errorf("empty time string")
	}
	return time.Parse(time.RFC3339Nano, trimmed)
}

func normalizeOutputKind(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "single_command", "multi_command", "shell_script":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return "single_command"
	}
}

func normalizeShell(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch value {
	case "bash", "zsh", "sh", "fish", "powershell", "pwsh":
		if value == "pwsh" {
			return "powershell"
		}
		return value
	default:
		return "unknown"
	}
}

func scoreItem(item Item, query string, queryTokens []string, cwd string, projectRoot string, now time.Time) float64 {
	score := 0.0
	if item.Pinned {
		score += 1000
	}
	if projectRoot != "" && strings.EqualFold(strings.TrimSpace(item.ProjectRoot), projectRoot) {
		score += 80
	} else if cwd != "" && strings.EqualFold(strings.TrimSpace(item.CWD), cwd) {
		score += 40
	}
	if item.UseCount > 0 {
		score += minFloat(float64(item.UseCount)*4, 60)
	}
	if item.SuccessCount > 0 {
		score += minFloat(float64(item.SuccessCount)*2, 40)
	}
	if item.FailureCount > 0 {
		score -= minFloat(float64(item.FailureCount)*4, 80)
	}
	riskSet := labelsFromRisk(item.Risk)
	if _, ok := riskSet["high-risk"]; ok {
		score -= 30
	}
	if _, ok := riskSet["destructive"]; ok {
		score -= 20
	}
	if _, ok := riskSet["irreversible"]; ok {
		score -= 20
	}
	if _, ok := riskSet["privileged"]; ok {
		score -= 10
	}
	if _, ok := riskSet["networked"]; ok {
		score -= 8
	}

	if item.LastUsedAt != nil {
		age := now.Sub(item.LastUsedAt.UTC())
		switch {
		case age <= 24*time.Hour:
			score += 30
		case age <= 7*24*time.Hour:
			score += 20
		case age <= 30*24*time.Hour:
			score += 10
		}
	}
	createdAge := now.Sub(item.CreatedAt.UTC())
	switch {
	case createdAge <= 24*time.Hour:
		score += 8
	case createdAge <= 7*24*time.Hour:
		score += 5
	}

	if query == "" {
		return score
	}
	title := strings.ToLower(item.Title)
	prompt := strings.ToLower(item.Prompt)
	commandText := strings.ToLower(item.CommandText)
	scriptText := strings.ToLower(item.ScriptText)
	explanation := strings.ToLower(item.Explanation)
	shell := strings.ToLower(item.Shell)
	textScore := 0.0

	if strings.Contains(title, query) {
		textScore += 50
	}
	if strings.Contains(commandText, query) || strings.Contains(scriptText, query) {
		textScore += 45
	}
	if strings.Contains(prompt, query) {
		textScore += 35
	}
	if strings.Contains(explanation, query) {
		textScore += 20
	}
	if strings.Contains(shell, query) {
		textScore += 10
	}
	for _, token := range queryTokens {
		if token == "" {
			continue
		}
		if strings.Contains(title, token) {
			textScore += 18
		}
		if strings.Contains(commandText, token) || strings.Contains(scriptText, token) {
			textScore += 16
		}
		if strings.Contains(prompt, token) {
			textScore += 12
		}
		if strings.Contains(explanation, token) {
			textScore += 6
		}
	}
	if textScore == 0 {
		return -1
	}
	return score + textScore
}

func labelsFromRisk(raw string) map[string]struct{} {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(raw)), ",")
	out := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		label := strings.TrimSpace(part)
		if label == "" {
			continue
		}
		out[label] = struct{}{}
	}
	return out
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
