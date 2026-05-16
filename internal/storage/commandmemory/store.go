package commandmemory

import (
	"context"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	envPath       = "TOPS_COMMAND_MEMORY_DB"
	defaultDBName = "command_memory.db"
)

type UpsertInput struct {
	Title              string
	Prompt             string
	CommandText        string
	ScriptText         string
	OutputKind         string
	Shell              string
	Explanation        string
	Risk               string
	CWD                string
	ProjectRoot        string
	ProjectFingerprint string
}

type SearchOptions struct {
	Query       string
	CWD         string
	ProjectRoot string
	Limit       int
}

type Item struct {
	ID                 int64
	Title              string
	Prompt             string
	CommandText        string
	ScriptText         string
	OutputKind         string
	Shell              string
	Explanation        string
	Risk               string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	LastUsedAt         *time.Time
	UseCount           int
	SuccessCount       int
	FailureCount       int
	LastExitCode       *int
	CWD                string
	ProjectRoot        string
	ProjectFingerprint string
	Pinned             bool
	DeletedAt          *time.Time
	Score              float64
}

func (i Item) ArtifactText() string {
	if strings.EqualFold(strings.TrimSpace(i.OutputKind), "shell_script") && strings.TrimSpace(i.ScriptText) != "" {
		return i.ScriptText
	}
	return i.CommandText
}

type Store interface {
	UpsertGenerated(ctx context.Context, in UpsertInput) (Item, error)
	Search(ctx context.Context, opts SearchOptions) ([]Item, error)
	GetByID(ctx context.Context, id int64) (Item, bool, error)
	Hide(ctx context.Context, id int64) error
	SetPinned(ctx context.Context, id int64, pinned bool) error
	RecordRun(ctx context.Context, id int64, exitCode int, success bool) error
	Close() error
}

func DefaultPath() (string, error) {
	if override := strings.TrimSpace(os.Getenv(envPath)); override != "" {
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory for command memory DB: %w", err)
	}
	return filepath.Join(home, ".tops", defaultDBName), nil
}

func NormalizeArtifact(outputKind, commandText, scriptText string) string {
	kind := strings.ToLower(strings.TrimSpace(outputKind))
	text := strings.TrimSpace(commandText)
	if kind == "shell_script" && strings.TrimSpace(scriptText) != "" {
		text = strings.TrimSpace(scriptText)
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	text = strings.TrimSpace(strings.Join(lines, "\n"))
	if kind != "shell_script" {
		text = strings.Join(strings.Fields(text), " ")
	}
	return text
}

func NormalizeContextKey(projectRoot, cwd string) string {
	projectRoot = strings.TrimSpace(projectRoot)
	cwd = strings.TrimSpace(cwd)
	if projectRoot != "" {
		return "project:" + strings.ToLower(projectRoot)
	}
	if cwd != "" {
		return "cwd:" + strings.ToLower(cwd)
	}
	return "global"
}

func DetectProjectContext(cwd string) (projectRoot string, fingerprint string) {
	root := detectGitRoot(cwd)
	if strings.TrimSpace(root) == "" {
		return "", ""
	}
	return root, fingerprintForPath(root)
}

func detectGitRoot(cwd string) string {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return ""
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return ""
	}
	cur := abs
	for {
		gitPath := filepath.Join(cur, ".git")
		if info, statErr := os.Stat(gitPath); statErr == nil {
			if info.IsDir() || !info.IsDir() {
				return cur
			}
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return ""
		}
		cur = parent
	}
}

func fingerprintForPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(strings.ToLower(path)))
	return fmt.Sprintf("%x", hasher.Sum64())
}

func inferTitleFromPrompt(prompt, fallbackArtifact string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt != "" {
		if len(prompt) > 72 {
			return strings.TrimSpace(prompt[:69]) + "..."
		}
		return prompt
	}
	fallbackArtifact = strings.TrimSpace(fallbackArtifact)
	if fallbackArtifact == "" {
		return "Generated command"
	}
	if idx := strings.IndexByte(fallbackArtifact, '\n'); idx > 0 {
		fallbackArtifact = fallbackArtifact[:idx]
	}
	if len(fallbackArtifact) > 72 {
		return strings.TrimSpace(fallbackArtifact[:69]) + "..."
	}
	return fallbackArtifact
}

func normalizeRisk(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, ",")
	seen := map[string]struct{}{}
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		label := strings.ToLower(strings.TrimSpace(part))
		if label == "" {
			continue
		}
		if _, ok := seen[label]; ok {
			continue
		}
		seen[label] = struct{}{}
		out = append(out, label)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}
