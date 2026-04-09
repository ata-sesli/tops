package modelprofile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"tops/internal/config"
	"tops/internal/model"
)

const schemaVersion = 1
const envPath = "TOPS_MODEL_PROFILES"

type ModelProfile struct {
	Provider     config.ProviderType `json:"provider"`
	Model        string              `json:"model"`
	Context      int                 `json:"context,omitempty"`
	MaxLength    int                 `json:"max_length,omitempty"`
	SystemPrompt string              `json:"system_prompt,omitempty"`
	Think        string              `json:"think,omitempty"`
	AskResponse  AskResponseConfig   `json:"ask_response,omitempty"`
}

type AskResponseConfig struct {
	Observations  *bool `json:"observations,omitempty"`
	Inferences    *bool `json:"inferences,omitempty"`
	Uncertainties *bool `json:"uncertainties,omitempty"`
	Assumptions   *bool `json:"assumptions,omitempty"`
	Notes         *bool `json:"notes,omitempty"`
}

type ModelProfiles struct {
	Version int                     `json:"version"`
	Entries map[string]ModelProfile `json:"entries"`
}

func Empty() ModelProfiles {
	return ModelProfiles{
		Version: schemaVersion,
		Entries: map[string]ModelProfile{},
	}
}

func DefaultPath() (string, error) {
	if override := strings.TrimSpace(os.Getenv(envPath)); override != "" {
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".tops", "models.json"), nil
}

func Load(path string) (ModelProfiles, error) {
	if strings.TrimSpace(path) == "" {
		resolved, err := DefaultPath()
		if err != nil {
			return ModelProfiles{}, err
		}
		path = resolved
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Empty(), nil
		}
		return ModelProfiles{}, fmt.Errorf("read model profiles: %w", err)
	}
	var profiles ModelProfiles
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&profiles); err != nil {
		return ModelProfiles{}, fmt.Errorf("parse model profiles JSON: %w", err)
	}
	if err := profiles.normalize(); err != nil {
		return ModelProfiles{}, err
	}
	return profiles, nil
}

func SaveAtomic(path string, profiles ModelProfiles) error {
	if strings.TrimSpace(path) == "" {
		resolved, err := DefaultPath()
		if err != nil {
			return err
		}
		path = resolved
	}
	if err := profiles.normalize(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create model profile directory: %w", err)
	}
	data, err := json.MarshalIndent(profiles, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal model profiles: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "models-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp model profile file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp model profile file: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set model profile file permissions: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp model profile file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace model profile file: %w", err)
	}
	return nil
}

func (m ModelProfiles) Get(provider config.ProviderType, model string) (ModelProfile, bool) {
	key, err := profileKey(provider, model)
	if err != nil {
		return ModelProfile{}, false
	}
	if m.Entries == nil {
		return ModelProfile{}, false
	}
	profile, ok := m.Entries[key]
	return profile, ok
}

func (m *ModelProfiles) Upsert(profile ModelProfile) error {
	if m == nil {
		return errors.New("model profiles container is nil")
	}
	if err := validateProfile(profile); err != nil {
		return err
	}
	m.ensureDefaults()
	key, err := profileKey(profile.Provider, profile.Model)
	if err != nil {
		return err
	}
	profile.Provider = normalizedProvider(profile.Provider)
	profile.Model = strings.TrimSpace(profile.Model)
	profile.SystemPrompt = strings.TrimSpace(profile.SystemPrompt)
	profile.Think = normalizeThink(profile.Think)
	m.Entries[key] = profile
	return nil
}

func (m *ModelProfiles) Delete(provider config.ProviderType, model string) bool {
	if m == nil || m.Entries == nil {
		return false
	}
	key, err := profileKey(provider, model)
	if err != nil {
		return false
	}
	if _, ok := m.Entries[key]; !ok {
		return false
	}
	delete(m.Entries, key)
	return true
}

func (m *ModelProfiles) normalize() error {
	if m == nil {
		return errors.New("model profiles is nil")
	}
	m.ensureDefaults()
	if m.Version != schemaVersion {
		return fmt.Errorf("model profiles schema version %d is unsupported", m.Version)
	}
	normalized := make(map[string]ModelProfile, len(m.Entries))
	for _, profile := range m.Entries {
		if err := validateProfile(profile); err != nil {
			return err
		}
		key, err := profileKey(profile.Provider, profile.Model)
		if err != nil {
			return err
		}
		if _, exists := normalized[key]; exists {
			return fmt.Errorf("model profile duplicate key %q", key)
		}
		profile.Provider = normalizedProvider(profile.Provider)
		profile.Model = strings.TrimSpace(profile.Model)
		profile.SystemPrompt = strings.TrimSpace(profile.SystemPrompt)
		profile.Think = normalizeThink(profile.Think)
		normalized[key] = profile
	}
	m.Entries = normalized
	return nil
}

func (m *ModelProfiles) ensureDefaults() {
	if m.Version == 0 {
		m.Version = schemaVersion
	}
	if m.Entries == nil {
		m.Entries = map[string]ModelProfile{}
	}
}

func (p ModelProfile) EffectiveAskResponseProfile() model.AskResponseProfile {
	defaults := model.DefaultAskResponseProfile()
	return model.AskResponseProfile{
		Observations:  boolOrDefault(p.AskResponse.Observations, defaults.Observations),
		Inferences:    boolOrDefault(p.AskResponse.Inferences, defaults.Inferences),
		Uncertainties: boolOrDefault(p.AskResponse.Uncertainties, defaults.Uncertainties),
		Assumptions:   boolOrDefault(p.AskResponse.Assumptions, defaults.Assumptions),
		Notes:         boolOrDefault(p.AskResponse.Notes, defaults.Notes),
	}
}

func boolOrDefault(v *bool, fallback bool) bool {
	if v == nil {
		return fallback
	}
	return *v
}

func validateProfile(profile ModelProfile) error {
	key, err := profileKey(profile.Provider, profile.Model)
	if err != nil {
		return err
	}
	if key == "" {
		return errors.New("model profile key cannot be empty")
	}
	if profile.Context < 0 {
		return fmt.Errorf("model profile invalid: context must be >= 0")
	}
	if profile.MaxLength < 0 {
		return fmt.Errorf("model profile invalid: max_length must be >= 0")
	}
	if !isValidThink(profile.Think) {
		return fmt.Errorf("model profile invalid: think must be one of on|off|low|medium|high")
	}
	return nil
}

func profileKey(provider config.ProviderType, model string) (string, error) {
	provider = normalizedProvider(provider)
	model = strings.TrimSpace(model)
	if provider == "" {
		return "", errors.New("model profile invalid: provider is required")
	}
	if model == "" {
		return "", errors.New("model profile invalid: model is required")
	}
	switch provider {
	case config.ProviderOpenAI, config.ProviderAnthropic, config.ProviderGemini, config.ProviderOllama, config.ProviderLocal:
	default:
		return "", fmt.Errorf("model profile invalid: unsupported provider %q", provider)
	}
	return string(provider) + ":" + model, nil
}

func normalizedProvider(provider config.ProviderType) config.ProviderType {
	return config.ProviderType(strings.ToLower(strings.TrimSpace(string(provider))))
}

func normalizeThink(value string) string {
	v := strings.ToLower(strings.TrimSpace(value))
	switch v {
	case "true":
		return "on"
	case "false":
		return "off"
	default:
		return v
	}
}

func isValidThink(value string) bool {
	switch normalizeThink(value) {
	case "", "on", "off", "low", "medium", "high":
		return true
	default:
		return false
	}
}
