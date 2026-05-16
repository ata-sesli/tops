package tui

import (
	"fmt"
	"strconv"
	"strings"

	"tops/internal/config"
)

type setupStep int

const (
	setupStepProvider setupStep = iota
	setupStepModel
	setupStepModelPath
	setupStepLibPath
	setupStepModelsDir
	setupStepAPIKeyEnv
	setupStepShell
	setupStepOutput
	setupStepTimeout
	setupStepDebug
	setupStepReview
	setupStepConfirm
)

type SetupWizardState struct {
	ConfigPath string
	Step       setupStep

	Provider  string
	Model     string
	ModelPath string
	LibPath   string
	ModelsDir string
	APIKeyEnv string
	Shell     string
	Output    string
	Timeout   string
	Debug     string

	AvailableModels []string
	InfoMessage     string
	ErrorMessage    string
}

type SetupWizardSubmitResult struct {
	Output             string
	NeedModelDiscovery bool
	DiscoveryEndpoint  string
	SavedConfig        *config.Config
	Cancelled          bool
}

func NewSetupWizardState(configPath string, existing *config.Config) SetupWizardState {
	state := SetupWizardState{
		ConfigPath: configPath,
		Step:       setupStepProvider,
		Provider:   string(config.ProviderOpenAI),
		ModelsDir:  "~/.tops/models",
		APIKeyEnv:  "TOPS_API_KEY",
		Shell:      "zsh",
		Output:     "text",
		Timeout:    "10",
		Debug:      "N",
	}
	if existing == nil {
		return state
	}
	if provider := strings.TrimSpace(string(existing.Provider.Type)); provider != "" {
		state.Provider = provider
	}
	if model := strings.TrimSpace(existing.Provider.Model); model != "" {
		state.Model = model
	}
	if modelPath := strings.TrimSpace(existing.Provider.ModelPath); modelPath != "" {
		state.ModelPath = modelPath
	}
	if libPath := strings.TrimSpace(existing.Provider.LibPath); libPath != "" {
		state.LibPath = libPath
	}
	if len(existing.Provider.ModelsDirs) > 0 {
		if first := strings.TrimSpace(existing.Provider.ModelsDirs[0]); first != "" {
			state.ModelsDir = first
		}
	}
	if modelsDir := strings.TrimSpace(existing.Provider.ModelsDir); modelsDir != "" {
		state.ModelsDir = modelsDir
	}
	if apiKeyEnv := strings.TrimSpace(existing.Provider.APIKeyEnv); apiKeyEnv != "" {
		state.APIKeyEnv = apiKeyEnv
	}
	if shell := strings.TrimSpace(existing.Shell); shell != "" {
		state.Shell = shell
	}
	if output := strings.TrimSpace(existing.Output.Format); output != "" {
		state.Output = output
	}
	if existing.Inspection.TimeoutSeconds > 0 {
		state.Timeout = strconv.Itoa(existing.Inspection.TimeoutSeconds)
	}
	if existing.Debug.Enabled {
		state.Debug = "Y"
	}
	return state
}

func (s SetupWizardState) IsLocalProvider() bool {
	return strings.EqualFold(s.Provider, string(config.ProviderYZMA))
}

func (s SetupWizardState) Header() string {
	return "TOPS Setup Wizard (step-by-step)"
}

func (s SetupWizardState) PromptLabel() string {
	switch s.Step {
	case setupStepProvider:
		return "Provider (openai|anthropic|gemini|yzma)"
	case setupStepModel:
		return "Model name"
	case setupStepModelPath:
		return "YZMA model path (.gguf)"
	case setupStepLibPath:
		return "YZMA library path (or set YZMA_LIB)"
	case setupStepModelsDir:
		return "Model discovery directory"
	case setupStepAPIKeyEnv:
		return "API key environment variable"
	case setupStepShell:
		return "Default shell"
	case setupStepOutput:
		return "Output format (text|json)"
	case setupStepTimeout:
		return "Inspection timeout seconds (1-120)"
	case setupStepDebug:
		return "Enable debug logs? (y/N)"
	case setupStepReview:
		return "Press Enter to continue to confirmation"
	case setupStepConfirm:
		return "Save this configuration? (y/N)"
	default:
		return ""
	}
}

func (s SetupWizardState) PromptDefault() string {
	switch s.Step {
	case setupStepProvider:
		return s.Provider
	case setupStepModel:
		if s.Model != "" {
			return s.Model
		}
		if len(s.AvailableModels) > 0 {
			return s.AvailableModels[0]
		}
		return ""
	case setupStepModelPath:
		return s.ModelPath
	case setupStepLibPath:
		return s.LibPath
	case setupStepModelsDir:
		return s.ModelsDir
	case setupStepAPIKeyEnv:
		return s.APIKeyEnv
	case setupStepShell:
		return s.Shell
	case setupStepOutput:
		return s.Output
	case setupStepTimeout:
		return s.Timeout
	case setupStepDebug:
		return s.Debug
	case setupStepConfirm:
		return "N"
	default:
		return ""
	}
}

func (s SetupWizardState) ReviewText() string {
	var b strings.Builder
	b.WriteString("Review configuration:\n")
	fmt.Fprintf(&b, "  Provider: %s\n", s.Provider)
	fmt.Fprintf(&b, "  Model: %s\n", s.Model)
	if s.IsLocalProvider() {
		fmt.Fprintf(&b, "  Model path: %s\n", s.ModelPath)
		fmt.Fprintf(&b, "  Lib path: %s\n", s.LibPath)
		fmt.Fprintf(&b, "  Models dir: %s\n", s.ModelsDir)
	} else {
		fmt.Fprintf(&b, "  API key env: %s\n", s.APIKeyEnv)
	}
	fmt.Fprintf(&b, "  Shell: %s\n", s.Shell)
	fmt.Fprintf(&b, "  Output format: %s\n", s.Output)
	fmt.Fprintf(&b, "  Inspection timeout: %s\n", s.Timeout)
	debugEnabled := strings.EqualFold(strings.TrimSpace(s.Debug), "y")
	fmt.Fprintf(&b, "  Debug: %t\n", debugEnabled)
	return strings.TrimSpace(b.String())
}

func (s *SetupWizardState) Submit(raw string) SetupWizardSubmitResult {
	input := strings.TrimSpace(raw)
	if input == "" {
		input = s.PromptDefault()
	}
	s.ErrorMessage = ""

	switch s.Step {
	case setupStepProvider:
		provider := strings.ToLower(input)
		switch config.ProviderType(provider) {
		case config.ProviderOpenAI, config.ProviderAnthropic, config.ProviderGemini, config.ProviderYZMA:
			s.Provider = provider
			s.Step = setupStepModel
			return SetupWizardSubmitResult{Output: fmt.Sprintf("Provider set to %s.", s.Provider)}
		default:
			s.ErrorMessage = fmt.Sprintf("Unsupported provider %q.", input)
			return SetupWizardSubmitResult{Output: s.ErrorMessage}
		}
	case setupStepModel:
		if input == "" {
			s.ErrorMessage = "Model cannot be empty."
			return SetupWizardSubmitResult{Output: s.ErrorMessage}
		}
		if idx, err := strconv.Atoi(input); err == nil && len(s.AvailableModels) > 0 && idx >= 1 && idx <= len(s.AvailableModels) {
			s.Model = s.AvailableModels[idx-1]
		} else {
			s.Model = input
		}
		if s.IsLocalProvider() {
			s.Step = setupStepModelPath
		} else {
			s.Step = setupStepAPIKeyEnv
		}
		return SetupWizardSubmitResult{Output: fmt.Sprintf("Model set to %s.", s.Model)}
	case setupStepModelPath:
		if input == "" {
			s.ErrorMessage = "Model path cannot be empty."
			return SetupWizardSubmitResult{Output: s.ErrorMessage}
		}
		s.ModelPath = input
		s.Step = setupStepLibPath
		return SetupWizardSubmitResult{Output: fmt.Sprintf("Model path set to %s.", s.ModelPath)}
	case setupStepLibPath:
		if input == "" {
			s.ErrorMessage = "Lib path cannot be empty."
			return SetupWizardSubmitResult{Output: s.ErrorMessage}
		}
		s.LibPath = input
		s.Step = setupStepModelsDir
		return SetupWizardSubmitResult{Output: fmt.Sprintf("Lib path set to %s.", s.LibPath)}
	case setupStepModelsDir:
		if input == "" {
			input = "~/.tops/models"
		}
		s.ModelsDir = input
		s.Step = setupStepShell
		return SetupWizardSubmitResult{Output: fmt.Sprintf("Models dir set to %s.", s.ModelsDir)}
	case setupStepAPIKeyEnv:
		if input == "" {
			s.ErrorMessage = "API key environment variable cannot be empty."
			return SetupWizardSubmitResult{Output: s.ErrorMessage}
		}
		s.APIKeyEnv = input
		s.Step = setupStepShell
		return SetupWizardSubmitResult{Output: fmt.Sprintf("API key env set to %s.", s.APIKeyEnv)}
	case setupStepShell:
		if input == "" {
			s.ErrorMessage = "Shell cannot be empty."
			return SetupWizardSubmitResult{Output: s.ErrorMessage}
		}
		s.Shell = input
		s.Step = setupStepOutput
		return SetupWizardSubmitResult{Output: fmt.Sprintf("Shell set to %s.", s.Shell)}
	case setupStepOutput:
		format := strings.ToLower(input)
		if format != "text" && format != "json" {
			s.ErrorMessage = "Output format must be text or json."
			return SetupWizardSubmitResult{Output: s.ErrorMessage}
		}
		s.Output = format
		s.Step = setupStepTimeout
		return SetupWizardSubmitResult{Output: fmt.Sprintf("Output format set to %s.", s.Output)}
	case setupStepTimeout:
		n, err := strconv.Atoi(input)
		if err != nil || n < 1 || n > 120 {
			s.ErrorMessage = "Timeout must be an integer between 1 and 120."
			return SetupWizardSubmitResult{Output: s.ErrorMessage}
		}
		s.Timeout = strconv.Itoa(n)
		s.Step = setupStepDebug
		return SetupWizardSubmitResult{Output: fmt.Sprintf("Inspection timeout set to %s seconds.", s.Timeout)}
	case setupStepDebug:
		if !isYesNo(input) {
			s.ErrorMessage = "Answer must be y or n."
			return SetupWizardSubmitResult{Output: s.ErrorMessage}
		}
		s.Debug = normalizeYesNo(input)
		s.Step = setupStepReview
		return SetupWizardSubmitResult{Output: "Debug option captured."}
	case setupStepReview:
		s.Step = setupStepConfirm
		return SetupWizardSubmitResult{Output: s.ReviewText()}
	case setupStepConfirm:
		if !isYesNo(input) {
			s.ErrorMessage = "Answer must be y or n."
			return SetupWizardSubmitResult{Output: s.ErrorMessage}
		}
		if !strings.EqualFold(input, "y") {
			return SetupWizardSubmitResult{Cancelled: true, Output: "Setup cancelled. Existing configuration was not changed."}
		}
		cfg, err := s.BuildConfig()
		if err != nil {
			s.ErrorMessage = err.Error()
			return SetupWizardSubmitResult{Output: s.ErrorMessage}
		}
		return SetupWizardSubmitResult{Output: "Saving configuration...", SavedConfig: &cfg}
	default:
		s.ErrorMessage = "Wizard is in an unknown state."
		return SetupWizardSubmitResult{Output: s.ErrorMessage}
	}
}

func (s SetupWizardState) BuildConfig() (config.Config, error) {
	timeout, err := strconv.Atoi(strings.TrimSpace(s.Timeout))
	if err != nil {
		return config.Config{}, fmt.Errorf("invalid timeout value %q", s.Timeout)
	}
	cfg := config.Config{
		Provider: config.ProviderConfig{
			Type:      config.ProviderType(strings.ToLower(strings.TrimSpace(s.Provider))),
			Model:     strings.TrimSpace(s.Model),
			ModelPath: strings.TrimSpace(s.ModelPath),
			LibPath:   strings.TrimSpace(s.LibPath),
			ModelsDir: strings.TrimSpace(s.ModelsDir),
			ModelsDirs: func() []string {
				if strings.TrimSpace(s.ModelsDir) == "" {
					return nil
				}
				return []string{strings.TrimSpace(s.ModelsDir)}
			}(),
		},
		Shell: strings.TrimSpace(s.Shell),
		Output: config.OutputConfig{
			Format: strings.ToLower(strings.TrimSpace(s.Output)),
		},
		Inspection: config.InspectionConfig{TimeoutSeconds: timeout},
		Debug: config.DebugConfig{
			Enabled: strings.EqualFold(strings.TrimSpace(s.Debug), "y"),
		},
	}
	if s.IsLocalProvider() {
		cfg.Provider.ModelsDir = strings.TrimSpace(s.ModelsDir)
		cfg.Provider.ModelsDirs = []string{strings.TrimSpace(s.ModelsDir)}
	} else {
		cfg.Provider.APIKeyEnv = strings.TrimSpace(s.APIKeyEnv)
	}
	if err := cfg.ApplyDefaults(); err != nil {
		return config.Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return config.Config{}, err
	}
	return cfg, nil
}

func isYesNo(raw string) bool {
	raw = strings.ToLower(strings.TrimSpace(raw))
	return raw == "y" || raw == "n"
}

func normalizeYesNo(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), "y") {
		return "Y"
	}
	return "N"
}
