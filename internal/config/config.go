package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type ProviderType string

const (
	ProviderOpenAI    ProviderType = "openai"
	ProviderAnthropic ProviderType = "anthropic"
	ProviderGemini    ProviderType = "gemini"
	ProviderOllama    ProviderType = "ollama"
	ProviderLocal     ProviderType = "local"
)

type Config struct {
	Provider   ProviderConfig   `json:"provider"`
	Shell      string           `json:"shell"`
	Output     OutputConfig     `json:"output"`
	Inspection InspectionConfig `json:"inspection"`
	Execution  ExecutionConfig  `json:"execution"`
	Debug      DebugConfig      `json:"debug"`
}

type ProviderConfig struct {
	Type       ProviderType `json:"type"`
	Model      string       `json:"model"`
	APIKeyEnv  string       `json:"api_key_env,omitempty"`
	Endpoint   string       `json:"endpoint,omitempty"`
	LocalModel string       `json:"local_model,omitempty"`
}

type OutputConfig struct {
	Format string `json:"format"`
}

type InspectionConfig struct {
	TimeoutSeconds int `json:"timeout_seconds"`
}

type ExecutionConfig struct {
	Enabled     bool                       `json:"enabled"`
	Permissions ExecutionPermissionsConfig `json:"permissions"`
	TraceMode   TraceMode                  `json:"trace_mode"`
}

type ExecutionPermissionsConfig struct {
	ReadOnly ActionPermission `json:"read_only"`
	Write    ActionPermission `json:"write"`
}

type ActionPermission string

const (
	ActionPermissionAllow    ActionPermission = "allow"
	ActionPermissionRequest  ActionPermission = "request"
	ActionPermissionDisallow ActionPermission = "disallow"
)

type TraceMode string

const (
	TraceModeDebug   TraceMode = "debug"
	TraceModeRelease TraceMode = "release"
)

type DebugConfig struct {
	Enabled bool `json:"enabled"`
}

func DefaultPath() (string, error) {
	if override := strings.TrimSpace(os.Getenv("TOPS_CONFIG")); override != "" {
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".tops", "config.json"), nil
}

func Load(path string) (Config, error) {
	if path == "" {
		p, err := DefaultPath()
		if err != nil {
			return Config{}, err
		}
		path = p
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("configuration not found at %s", path)
		}
		return Config{}, fmt.Errorf("read configuration: %w", err)
	}
	var cfg Config
	dec := json.NewDecoder(strings.NewReader(string(contents)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse configuration JSON: %w", err)
	}
	if err := cfg.ApplyDefaults(); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func SaveAtomic(path string, cfg Config) error {
	if path == "" {
		return errors.New("configuration path is required")
	}
	if err := cfg.ApplyDefaults(); err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal configuration: %w", err)
	}
	tmpFile, err := os.CreateTemp(dir, "config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp config file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)
	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		return fmt.Errorf("write temp config file: %w", err)
	}
	if err := tmpFile.Chmod(0o600); err != nil {
		tmpFile.Close()
		return fmt.Errorf("set config permissions: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp config file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace config file: %w", err)
	}
	return nil
}

func (c *Config) ApplyDefaults() error {
	if c.Shell == "" {
		if shell := strings.TrimSpace(os.Getenv("SHELL")); shell != "" {
			c.Shell = filepath.Base(shell)
		} else {
			c.Shell = "sh"
		}
	}
	if c.Output.Format == "" {
		c.Output.Format = "text"
	}
	if c.Inspection.TimeoutSeconds == 0 {
		c.Inspection.TimeoutSeconds = 10
	}
	if (c.Provider.Type == ProviderOllama || c.Provider.Type == ProviderLocal) && strings.TrimSpace(c.Provider.Endpoint) == "" {
		c.Provider.Endpoint = "http://localhost:11434"
	}
	if c.Execution.Permissions.ReadOnly == "" {
		c.Execution.Permissions.ReadOnly = ActionPermissionAllow
	}
	if c.Execution.Permissions.Write == "" {
		c.Execution.Permissions.Write = ActionPermissionRequest
	}
	if c.Execution.TraceMode == "" {
		c.Execution.TraceMode = TraceModeDebug
	}
	return nil
}

func (c Config) Validate() error {
	if c.Provider.Type == "" {
		return errors.New("configuration invalid: provider.type is required")
	}
	switch c.Provider.Type {
	case ProviderOpenAI, ProviderAnthropic, ProviderGemini:
		if strings.TrimSpace(c.Provider.APIKeyEnv) == "" {
			return fmt.Errorf("configuration invalid: provider.api_key_env is required for provider %q", c.Provider.Type)
		}
	case ProviderOllama, ProviderLocal:
		if strings.TrimSpace(c.Provider.Endpoint) == "" {
			return fmt.Errorf("configuration invalid: provider.endpoint is required for provider %q", c.Provider.Type)
		}
	default:
		return fmt.Errorf("configuration invalid: unknown provider.type %q", c.Provider.Type)
	}
	if strings.TrimSpace(c.Provider.Model) == "" {
		return errors.New("configuration invalid: provider.model is required")
	}
	if strings.TrimSpace(c.Shell) == "" {
		return errors.New("configuration invalid: shell is required")
	}
	if c.Output.Format != "text" && c.Output.Format != "json" {
		return fmt.Errorf("configuration invalid: output.format must be \"text\" or \"json\", got %q", c.Output.Format)
	}
	if c.Inspection.TimeoutSeconds < 1 || c.Inspection.TimeoutSeconds > 120 {
		return errors.New("configuration invalid: inspection.timeout_seconds must be between 1 and 120")
	}
	switch c.Execution.Permissions.ReadOnly {
	case "", ActionPermissionAllow, ActionPermissionRequest, ActionPermissionDisallow:
	default:
		return fmt.Errorf("configuration invalid: execution.permissions.read_only must be allow|request|disallow, got %q", c.Execution.Permissions.ReadOnly)
	}
	switch c.Execution.Permissions.Write {
	case "", ActionPermissionAllow, ActionPermissionRequest, ActionPermissionDisallow:
	default:
		return fmt.Errorf("configuration invalid: execution.permissions.write must be allow|request|disallow, got %q", c.Execution.Permissions.Write)
	}
	switch c.Execution.TraceMode {
	case "", TraceModeDebug, TraceModeRelease:
	default:
		return fmt.Errorf("configuration invalid: execution.trace_mode must be debug|release, got %q", c.Execution.TraceMode)
	}
	return nil
}
