package setup

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"tops/internal/config"
)

type Options struct {
	Manual       bool
	ConfigPath   string
	Reader       io.Reader
	Writer       io.Writer
	ProviderType string
	Model        string
	ModelPath    string
	LibPath      string
	ModelsDir    string
	APIKeyEnv    string
	Shell        string
	OutputFormat string
	TimeoutSec   int
	Debug        bool
}

func Run(opts Options) error {
	if opts.Reader == nil {
		opts.Reader = io.NopCloser(strings.NewReader(""))
	}
	if opts.Writer == nil {
		return fmt.Errorf("setup writer is required")
	}
	if opts.ConfigPath == "" {
		path, err := config.DefaultPath()
		if err != nil {
			return err
		}
		opts.ConfigPath = path
	}

	var cfg config.Config
	if opts.Manual {
		cfg = manualConfig(opts)
	} else {
		var err error
		cfg, err = guidedConfig(opts)
		if err != nil {
			return err
		}
	}
	if err := cfg.ApplyDefaults(); err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := config.SaveAtomic(opts.ConfigPath, cfg); err != nil {
		return err
	}
	if cfg.Provider.Type == config.ProviderYZMA {
		if err := os.MkdirAll(config.DefaultModelsDir(), 0o755); err != nil {
			return fmt.Errorf("create default models directory: %w", err)
		}
	}
	_, _ = fmt.Fprintf(opts.Writer, "Saved TOPS configuration to %s\n", opts.ConfigPath)
	return nil
}

func manualConfig(opts Options) config.Config {
	cfg := config.Config{
		Provider: config.ProviderConfig{
			Type:      config.ProviderType(strings.ToLower(strings.TrimSpace(opts.ProviderType))),
			Model:     strings.TrimSpace(opts.Model),
			ModelPath: strings.TrimSpace(opts.ModelPath),
			LibPath:   strings.TrimSpace(opts.LibPath),
			ModelsDir: strings.TrimSpace(opts.ModelsDir),
			ModelsDirs: func() []string {
				if strings.TrimSpace(opts.ModelsDir) == "" {
					return nil
				}
				return []string{strings.TrimSpace(opts.ModelsDir)}
			}(),
			APIKeyEnv: strings.TrimSpace(opts.APIKeyEnv),
		},
		Shell: strings.TrimSpace(opts.Shell),
		Output: config.OutputConfig{
			Format: strings.TrimSpace(opts.OutputFormat),
		},
		Inspection: config.InspectionConfig{TimeoutSeconds: opts.TimeoutSec},
		Debug:      config.DebugConfig{Enabled: opts.Debug},
	}
	return cfg
}

func guidedConfig(opts Options) (config.Config, error) {
	s := bufio.NewScanner(opts.Reader)
	cfg := config.Config{}

	provider, err := promptValue(s, opts.Writer,
		"Provider (openai|anthropic|gemini|yzma)",
		string(config.ProviderOpenAI),
		func(v string) error {
			switch config.ProviderType(v) {
			case config.ProviderOpenAI, config.ProviderAnthropic, config.ProviderGemini, config.ProviderYZMA:
				return nil
			default:
				return fmt.Errorf("unsupported provider %q", v)
			}
		},
	)
	if err != nil {
		return config.Config{}, err
	}
	cfg.Provider.Type = config.ProviderType(provider)

	model, err := promptValue(s, opts.Writer, "Model", "", nonEmpty)
	if err != nil {
		return config.Config{}, err
	}
	cfg.Provider.Model = model

	if cfg.Provider.Type == config.ProviderYZMA {
		modelPath, err := promptValue(s, opts.Writer, "YZMA model path (.gguf)", "", nonEmpty)
		if err != nil {
			return config.Config{}, err
		}
		cfg.Provider.ModelPath = modelPath
		libPath, err := promptValue(s, opts.Writer, "YZMA library path (or set YZMA_LIB)", "", nonEmpty)
		if err != nil {
			return config.Config{}, err
		}
		cfg.Provider.LibPath = libPath
		modelsDir, err := promptValue(s, opts.Writer, "Model discovery directory", "~/.tops/models", nonEmpty)
		if err != nil {
			return config.Config{}, err
		}
		cfg.Provider.ModelsDir = modelsDir
		cfg.Provider.ModelsDirs = []string{modelsDir}
	} else {
		envName, err := promptValue(s, opts.Writer, "API key environment variable", "TOPS_API_KEY", nonEmpty)
		if err != nil {
			return config.Config{}, err
		}
		cfg.Provider.APIKeyEnv = envName
	}

	shell, err := promptValue(s, opts.Writer, "Default shell", "zsh", nonEmpty)
	if err != nil {
		return config.Config{}, err
	}
	cfg.Shell = shell

	outputFmt, err := promptValue(s, opts.Writer, "Output format (text|json)", "text", func(v string) error {
		if v != "text" && v != "json" {
			return fmt.Errorf("output format must be text or json")
		}
		return nil
	})
	if err != nil {
		return config.Config{}, err
	}
	cfg.Output.Format = outputFmt

	timeoutRaw, err := promptValue(s, opts.Writer, "Inspection timeout seconds", "10", nonEmpty)
	if err != nil {
		return config.Config{}, err
	}
	var timeout int
	_, err = fmt.Sscanf(timeoutRaw, "%d", &timeout)
	if err != nil {
		return config.Config{}, fmt.Errorf("invalid timeout value %q", timeoutRaw)
	}
	cfg.Inspection.TimeoutSeconds = timeout

	debugRaw, err := promptValue(s, opts.Writer, "Enable debug logs? (y/N)", "N", func(v string) error {
		if v != "y" && v != "Y" && v != "n" && v != "N" {
			return fmt.Errorf("answer must be y or n")
		}
		return nil
	})
	if err != nil {
		return config.Config{}, err
	}
	cfg.Debug.Enabled = strings.EqualFold(debugRaw, "y")

	_, _ = fmt.Fprintf(opts.Writer, "\nReview configuration:\n")
	_, _ = fmt.Fprintf(opts.Writer, "  Provider: %s\n", cfg.Provider.Type)
	_, _ = fmt.Fprintf(opts.Writer, "  Model: %s\n", cfg.Provider.Model)
	if cfg.Provider.Type == config.ProviderYZMA {
		_, _ = fmt.Fprintf(opts.Writer, "  Model path: %s\n", cfg.Provider.ModelPath)
		_, _ = fmt.Fprintf(opts.Writer, "  Lib path: %s\n", cfg.Provider.LibPath)
		_, _ = fmt.Fprintf(opts.Writer, "  Models dir: %s\n", cfg.Provider.ModelsDir)
	} else {
		_, _ = fmt.Fprintf(opts.Writer, "  API key env: %s\n", cfg.Provider.APIKeyEnv)
	}
	_, _ = fmt.Fprintf(opts.Writer, "  Shell: %s\n", cfg.Shell)
	_, _ = fmt.Fprintf(opts.Writer, "  Output format: %s\n", cfg.Output.Format)
	_, _ = fmt.Fprintf(opts.Writer, "  Inspection timeout: %d\n", cfg.Inspection.TimeoutSeconds)
	_, _ = fmt.Fprintf(opts.Writer, "  Debug: %t\n", cfg.Debug.Enabled)

	confirm, err := promptValue(s, opts.Writer, "Save this configuration? (y/N)", "N", func(v string) error {
		if v != "y" && v != "Y" && v != "n" && v != "N" {
			return fmt.Errorf("answer must be y or n")
		}
		return nil
	})
	if err != nil {
		return config.Config{}, err
	}
	if !strings.EqualFold(confirm, "y") {
		return config.Config{}, fmt.Errorf("setup cancelled")
	}

	return cfg, nil
}

func promptValue(scanner *bufio.Scanner, out io.Writer, label string, def string, validate func(string) error) (string, error) {
	for {
		if def == "" {
			_, _ = fmt.Fprintf(out, "%s: ", label)
		} else {
			_, _ = fmt.Fprintf(out, "%s [%s]: ", label, def)
		}
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return "", err
			}
			if def != "" {
				return def, nil
			}
			return "", fmt.Errorf("input ended before setup finished")
		}
		v := strings.TrimSpace(scanner.Text())
		if v == "" {
			v = def
		}
		if validate != nil {
			if err := validate(v); err != nil {
				_, _ = fmt.Fprintf(out, "Invalid value: %s\n", err.Error())
				continue
			}
		}
		return v, nil
	}
}

func nonEmpty(v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("value cannot be empty")
	}
	return nil
}
