package localruntime

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const envStatePath = "TOPS_LOCAL_RUNTIME_STATE"

type RuntimeState struct {
	ActiveWarmedModel      string `json:"active_warmed_model,omitempty"`
	LastSuccessfulWarmupAt string `json:"last_successful_warmup_at,omitempty"`
	LastWarmupStatus       string `json:"last_warmup_status,omitempty"`
	LastKnownError         string `json:"last_known_error,omitempty"`
	LastErrorCategory      string `json:"last_error_category,omitempty"`
	LastModelPath          string `json:"last_model_path,omitempty"`
	LastLibPath            string `json:"last_lib_path,omitempty"`
	UpdatedAt              string `json:"updated_at,omitempty"`
}

type StateStore interface {
	Load() (RuntimeState, error)
	Save(state RuntimeState) error
	Path() string
}

type FileStateStore struct {
	path string
}

func NewFileStateStore(path string) (FileStateStore, error) {
	resolved := strings.TrimSpace(path)
	if resolved == "" {
		var err error
		resolved, err = DefaultStatePath()
		if err != nil {
			return FileStateStore{}, err
		}
	}
	return FileStateStore{path: resolved}, nil
}

func DefaultStatePath() (string, error) {
	if override := strings.TrimSpace(os.Getenv(envStatePath)); override != "" {
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".tops", "local_runtime_state.json"), nil
}

func (s FileStateStore) Path() string {
	return s.path
}

func (s FileStateStore) Load() (RuntimeState, error) {
	if strings.TrimSpace(s.path) == "" {
		return RuntimeState{}, errors.New("local runtime state path is empty")
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RuntimeState{}, nil
		}
		return RuntimeState{}, fmt.Errorf("read local runtime state: %w", err)
	}
	var state RuntimeState
	if err := json.Unmarshal(raw, &state); err != nil {
		return RuntimeState{}, fmt.Errorf("parse local runtime state: %w", err)
	}
	return normalizeState(state), nil
}

func (s FileStateStore) Save(state RuntimeState) error {
	if strings.TrimSpace(s.path) == "" {
		return errors.New("local runtime state path is empty")
	}
	state = normalizeState(state)
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create local runtime state directory: %w", err)
	}
	blob, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal local runtime state: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), "local-runtime-state-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary local runtime state file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(blob); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary local runtime state file: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set temporary local runtime state permissions: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary local runtime state file: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("replace local runtime state file: %w", err)
	}
	return nil
}

func normalizeState(state RuntimeState) RuntimeState {
	state.ActiveWarmedModel = strings.TrimSpace(state.ActiveWarmedModel)
	state.LastSuccessfulWarmupAt = normalizeTimestamp(state.LastSuccessfulWarmupAt)
	state.LastWarmupStatus = normalizeWarmupStatus(state.LastWarmupStatus)
	state.LastKnownError = strings.TrimSpace(state.LastKnownError)
	state.LastErrorCategory = strings.TrimSpace(state.LastErrorCategory)
	state.LastModelPath = strings.TrimSpace(state.LastModelPath)
	state.LastLibPath = strings.TrimSpace(state.LastLibPath)
	state.UpdatedAt = normalizeTimestamp(state.UpdatedAt)
	return state
}

func normalizeTimestamp(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	ts, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return ""
	}
	return ts.UTC().Format(time.RFC3339)
}

func normalizeWarmupStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "success":
		return "success"
	case "failed", "failure":
		return "failure"
	default:
		return ""
	}
}
