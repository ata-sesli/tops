package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"tops/internal/app"
	"tops/internal/chatstore"
	"tops/internal/config"
	"tops/internal/model"
	"tops/internal/progress"
	"tops/internal/setup"
	"tops/internal/tui"
	"tops/internal/workflow"
)

type RootOptions struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
}

func NewRootCommand(opts RootOptions) *cobra.Command {
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	if opts.Stdin == nil {
		opts.Stdin = os.Stdin
	}

	var configPath string

	root := &cobra.Command{
		Use:           "tops",
		Short:         "TOPS: Terminal Operations System",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) (runErr error) {
			cfgPath, err := resolveConfigPath(strings.TrimSpace(configPath))
			if err != nil {
				return configError(err)
			}

			var rt app.Runtime
			var startupConfigErr error
			loaded, err := loadRuntime(cfgPath)
			if err != nil {
				startupConfigErr = configError(err)
			} else {
				rt = loaded
			}

			chatDBPath, err := chatstore.DefaultPath()
			if err != nil {
				return fmt.Errorf("chat storage error: %w", err)
			}
			logger := rt.Logger
			store, err := chatstore.OpenSQLite(chatDBPath, logger)
			if err != nil {
				return fmt.Errorf("chat storage error: %w", err)
			}
			defer func() {
				if err := store.Close(); err != nil && runErr == nil {
					runErr = fmt.Errorf("chat storage close error: %w", err)
				}
			}()

			runner := tui.NewSessionWithOptions(tui.SessionOptions{
				Store:            store,
				ConfigPath:       cfgPath,
				RuntimeLoader:    loadRuntime,
				StartupConfigErr: startupConfigErr,
			})
			if err := runner.Run(cmd.Context(), opts.Stdin, cmd.OutOrStdout(), rt); err != nil {
				return fmt.Errorf("tui error: %w", err)
			}
			return nil
		},
	}
	root.SetOut(opts.Stdout)
	root.SetErr(opts.Stderr)
	root.PersistentFlags().StringVar(&configPath, "config", "", "Path to TOPS configuration file")

	root.AddCommand(newSetupCommand(opts, &configPath))
	root.AddCommand(newModeCommand(model.ModeHelp, "help", "Explain commands", &configPath, opts))
	root.AddCommand(newModeCommand(model.ModeGen, "gen", "Generate shell commands", &configPath, opts))
	root.AddCommand(newModeCommand(model.ModeAsk, "ask", "Ask questions about local environment", &configPath, opts))

	return root
}

func newSetupCommand(opts RootOptions, configPath *string) *cobra.Command {
	var manual bool
	var provider string
	var modelName string
	var apiKeyEnv string
	var endpoint string
	var shell string
	var outputFmt string
	var timeout int
	var debug bool

	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Create or update TOPS configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !manual {
				cfgPath, err := resolveConfigPath(strings.TrimSpace(*configPath))
				if err != nil {
					return configError(err)
				}
				var rt app.Runtime
				var startupConfigErr error
				loaded, err := loadRuntime(cfgPath)
				if err != nil {
					startupConfigErr = configError(err)
				} else {
					rt = loaded
				}
				chatDBPath, err := chatstore.DefaultPath()
				if err != nil {
					return fmt.Errorf("chat storage error: %w", err)
				}
				store, err := chatstore.OpenSQLite(chatDBPath, rt.Logger)
				if err != nil {
					return fmt.Errorf("chat storage error: %w", err)
				}
				defer store.Close()
				runner := tui.NewSessionWithOptions(tui.SessionOptions{
					Store:             store,
					ConfigPath:        cfgPath,
					RuntimeLoader:     loadRuntime,
					StartupConfigErr:  startupConfigErr,
					SetupOnly:         true,
					ForceWizardReason: "Interactive setup requested.",
				})
				if err := runner.Run(cmd.Context(), opts.Stdin, cmd.OutOrStdout(), rt); err != nil {
					return fmt.Errorf("tui error: %w", err)
				}
				return nil
			}

			err := setup.Run(setup.Options{
				Manual:       manual,
				ConfigPath:   strings.TrimSpace(*configPath),
				Reader:       opts.Stdin,
				Writer:       cmd.OutOrStdout(),
				ProviderType: provider,
				Model:        modelName,
				APIKeyEnv:    apiKeyEnv,
				Endpoint:     endpoint,
				Shell:        shell,
				OutputFormat: outputFmt,
				TimeoutSec:   timeout,
				Debug:        debug,
			})
			if err != nil {
				return fmt.Errorf("setup error: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&manual, "manual", false, "Use manual setup mode with direct flags")
	cmd.Flags().StringVar(&provider, "provider", "openai", "Provider type (openai|anthropic|gemini|ollama|local)")
	cmd.Flags().StringVar(&modelName, "model", "", "Model name (for ollama/local providers, use an Ollama tag like llama3.1)")
	cmd.Flags().StringVar(&apiKeyEnv, "api-key-env", "TOPS_API_KEY", "Environment variable name containing API key")
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "Provider endpoint (required for ollama/local provider)")
	cmd.Flags().StringVar(&shell, "shell", "zsh", "Default shell")
	cmd.Flags().StringVar(&outputFmt, "output-format", "text", "Output format (text|json)")
	cmd.Flags().IntVar(&timeout, "timeout-seconds", 10, "Inspection timeout in seconds")
	cmd.Flags().BoolVar(&debug, "debug", false, "Enable debug logs in config")
	return cmd
}

func newModeCommand(mode model.Mode, use, short string, configPath *string, opts RootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use + " <input>",
		Short: short,
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := loadRuntime(strings.TrimSpace(*configPath))
			if err != nil {
				return configError(err)
			}
			input := strings.TrimSpace(strings.Join(args, " "))
			ctx := cmd.Context()
			ctx = progress.WithReporter(ctx, progress.NewCLIWithMode(cmd.ErrOrStderr(), string(rt.Config.Execution.TraceMode)))
			if mode == model.ModeAsk || mode == model.ModeGen {
				if isInteractiveReader(opts.Stdin) {
					prompter := workflow.NewTerminalPrompter(opts.Stdin, cmd.ErrOrStderr())
					ctx = workflow.WithApprovalPrompter(ctx, prompter)
				}
				chatDBPath, dbPathErr := chatstore.DefaultPath()
				if dbPathErr != nil {
					return fmt.Errorf("chat storage error: %w", dbPathErr)
				}
				store, storeErr := chatstore.OpenSQLite(chatDBPath, rt.Logger)
				if storeErr != nil {
					return fmt.Errorf("chat storage error: %w", storeErr)
				}
				defer store.Close()
				ctx = workflow.WithAuditStore(ctx, store, nil)
			}
			output, err := app.ExecuteMode(ctx, rt, mode, input, app.ExecuteOptions{})
			if err != nil {
				return app.ClassifyRuntimeError(err)
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), output)
			return nil
		},
	}
	return cmd
}

func isInteractiveReader(in io.Reader) bool {
	file, ok := in.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(file.Fd()))
}

func loadRuntime(path string) (app.Runtime, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return app.Runtime{}, err
	}
	rt, err := app.NewRuntime(cfg)
	if err != nil {
		return app.Runtime{}, fmt.Errorf("provider error: %w", err)
	}
	return rt, nil
}

func resolveConfigPath(path string) (string, error) {
	if strings.TrimSpace(path) != "" {
		return strings.TrimSpace(path), nil
	}
	return config.DefaultPath()
}

func configError(err error) error {
	return fmt.Errorf("configuration error: %w. Run `tops setup` to create or repair your config", err)
}
