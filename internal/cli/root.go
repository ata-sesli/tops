package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"tops/internal/app"
	"tops/internal/config"
	"tops/internal/model"
	"tops/internal/obs"
	"tops/internal/ops/bench"
	"tops/internal/runtime/llm"
	"tops/internal/runtime/localruntime"
	"tops/internal/runtime/progress"
	"tops/internal/runtime/workflow"
	"tops/internal/setup"
	"tops/internal/storage/chatstore"
	"tops/internal/storage/modelprofile"
	"tops/internal/ui/tui"
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
		Use:           "tps",
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
				defer func() {
					_ = rt.Close(cmd.Context())
				}()
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
	root.AddCommand(newRunCommand(opts, &configPath))
	root.AddCommand(newLocalCommand(&configPath))
	root.AddCommand(newBenchCommand(opts, &configPath))

	return root
}

func newSetupCommand(opts RootOptions, configPath *string) *cobra.Command {
	var manual bool
	var provider string
	var modelName string
	var modelPath string
	var libPath string
	var modelsDir string
	var apiKeyEnv string
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
				ModelPath:    modelPath,
				LibPath:      libPath,
				ModelsDir:    modelsDir,
				APIKeyEnv:    apiKeyEnv,
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
	cmd.Flags().StringVar(&provider, "provider", "openai", "Provider type (openai|anthropic|gemini|yzma)")
	cmd.Flags().StringVar(&modelName, "model", "", "Model name")
	cmd.Flags().StringVar(&modelPath, "model-path", "", "Local GGUF model path for yzma provider")
	cmd.Flags().StringVar(&libPath, "lib-path", "", "Directory containing llama.cpp shared libraries for yzma provider")
	cmd.Flags().StringVar(&modelsDir, "models-dir", "", "Directory used for local model discovery (default ~/.tops/models)")
	cmd.Flags().StringVar(&apiKeyEnv, "api-key-env", "TOPS_API_KEY", "Environment variable name containing API key")
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
			defer func() {
				_ = rt.Close(cmd.Context())
			}()
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

func newBenchCommand(opts RootOptions, configPath *string) *cobra.Command {
	var workflowName string
	var datasetPath string
	var runs int
	var cold bool
	var warm bool
	var all bool
	var jsonOut string

	cmd := &cobra.Command{
		Use:   "bench [ask|help|gen]",
		Short: "Run repeatable benchmarks for ask/help/gen",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			argWorkflow := ""
			if len(args) == 1 {
				argWorkflow = strings.ToLower(strings.TrimSpace(args[0]))
			}
			if argWorkflow != "" {
				if strings.TrimSpace(workflowName) != "" && strings.ToLower(strings.TrimSpace(workflowName)) != argWorkflow {
					return fmt.Errorf("workflow mismatch: positional workflow %q conflicts with --workflow %q", argWorkflow, workflowName)
				}
				workflowName = argWorkflow
			}

			workflows := make([]model.Mode, 0, 3)
			switch {
			case all:
				workflows = append(workflows, model.ModeAsk, model.ModeHelp, model.ModeGen)
			case strings.TrimSpace(workflowName) != "":
				switch strings.ToLower(strings.TrimSpace(workflowName)) {
				case string(model.ModeAsk):
					workflows = append(workflows, model.ModeAsk)
				case string(model.ModeHelp):
					workflows = append(workflows, model.ModeHelp)
				case string(model.ModeGen):
					workflows = append(workflows, model.ModeGen)
				default:
					return fmt.Errorf("invalid --workflow %q (expected ask|help|gen)", workflowName)
				}
			default:
				return fmt.Errorf("specify --workflow ask|help|gen or use --all")
			}

			if strings.TrimSpace(datasetPath) != "" && len(workflows) != 1 {
				return fmt.Errorf("--dataset may only be used with a single workflow")
			}

			cfgPath, err := resolveConfigPath(strings.TrimSpace(*configPath))
			if err != nil {
				return configError(err)
			}
			rt, err := loadRuntime(cfgPath)
			if err != nil {
				return configError(err)
			}
			defer func() {
				_ = rt.Close(cmd.Context())
			}()

			if cold {
				return fmt.Errorf("cold benchmark profile is disabled; benchmarks now run warm-only")
			}
			profiles := []bench.Profile{bench.ProfileWarm}

			datasets := map[model.Mode]string{}
			if len(workflows) == 1 && strings.TrimSpace(datasetPath) != "" {
				datasets[workflows[0]] = strings.TrimSpace(datasetPath)
			}
			report, benchErr := bench.Run(cmd.Context(), bench.Options{
				Runtime:      rt,
				Workflows:    workflows,
				DatasetPaths: datasets,
				Runs:         runs,
				Profiles:     profiles,
				ProgressOut:  cmd.OutOrStdout(),
			})
			if benchErr != nil {
				return fmt.Errorf("benchmark failed: %w", benchErr)
			}

			_, _ = fmt.Fprintln(cmd.OutOrStdout(), bench.RenderHumanSummary(report))
			jsonBlob, marshalErr := json.MarshalIndent(report, "", "  ")
			if marshalErr != nil {
				return fmt.Errorf("marshal benchmark JSON: %w", marshalErr)
			}
			if outPath := strings.TrimSpace(jsonOut); outPath != "" {
				if writeErr := os.WriteFile(outPath, append(jsonBlob, '\n'), 0o644); writeErr != nil {
					return fmt.Errorf("write benchmark JSON: %w", writeErr)
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\nJSON report written to %s\n", outPath)
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout())
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(jsonBlob))
			return nil
		},
	}
	cmd.Flags().StringVar(&workflowName, "workflow", "", "Workflow to benchmark (ask|help|gen)")
	cmd.Flags().StringVar(&datasetPath, "dataset", "", "Path to dataset JSON for the selected workflow")
	cmd.Flags().IntVar(&runs, "runs", bench.DefaultRuns, "Number of runs per benchmark case (default: 1)")
	cmd.Flags().BoolVar(&cold, "cold", false, "Disabled: cold profile is no longer supported")
	cmd.Flags().BoolVar(&warm, "warm", false, "Run warm benchmarks (default behavior)")
	cmd.Flags().BoolVar(&all, "all", false, "Benchmark ask, help, and gen workflows")
	cmd.Flags().StringVar(&jsonOut, "json-out", "", "Write JSON report to file path")
	return cmd
}

func newLocalCommand(configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "local",
		Short: "Inspect and manage local YZMA runtime readiness",
	}

	var jsonStatus bool
	var probeStatus bool
	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show local runtime readiness status",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigOnly(strings.TrimSpace(*configPath))
			if err != nil {
				return configError(err)
			}
			svc, svcErr := newLocalRuntimeService(cfg)
			if svcErr != nil {
				return svcErr
			}
			status, statusErr := svc.StatusWithOptions(cmd.Context(), cfg, localruntime.StatusOptions{Probe: probeStatus})
			if statusErr != nil {
				return statusErr
			}
			if jsonStatus {
				blob, marshalErr := json.MarshalIndent(status, "", "  ")
				if marshalErr != nil {
					return marshalErr
				}
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(blob))
				return nil
			}
			for _, line := range formatLocalStatus(status) {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), line)
			}
			return nil
		},
	}
	statusCmd.Flags().BoolVar(&jsonStatus, "json", false, "Render status as JSON")
	statusCmd.Flags().BoolVar(&probeStatus, "probe", false, "Run live backend probe before reporting ready")

	var jsonDoctor bool
	var yzmaDoctor bool
	var doctorGenerate bool
	doctorCmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run deep local runtime diagnostics",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yzmaDoctor {
				return fmt.Errorf("specify --yzma to run the YZMA backend initialization doctor")
			}
			cfg, err := loadConfigOnly(strings.TrimSpace(*configPath))
			if err != nil {
				return configError(err)
			}
			svc, svcErr := newLocalRuntimeService(cfg)
			if svcErr != nil {
				return svcErr
			}
			result, probeErr := svc.ProbeYZMAWithOptions(cmd.Context(), cfg, localruntime.ProbeOptions{
				Source:             "doctor",
				DisableCPUFallback: true,
				Generate:           doctorGenerate,
				Attempts:           1,
			})
			if probeErr != nil {
				return probeErr
			}
			if jsonDoctor {
				blob, marshalErr := json.MarshalIndent(result, "", "  ")
				if marshalErr != nil {
					return marshalErr
				}
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(blob))
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "status=%s stage=%s reason=%s\n", strings.TrimSpace(result.Status), strings.TrimSpace(result.Stage), strings.TrimSpace(result.Reason))
				for _, line := range formatProbeContextLines(result.ProbeCtx) {
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), line)
				}
			}
			return nil
		},
	}
	doctorCmd.Flags().BoolVar(&yzmaDoctor, "yzma", false, "Run a real YZMA load+context probe")
	doctorCmd.Flags().BoolVar(&doctorGenerate, "generate", true, "Include tiny real generation checks (Completion + ToolChat)")
	doctorCmd.Flags().BoolVar(&jsonDoctor, "json", true, "Render doctor output as JSON")

	loadCmd := &cobra.Command{
		Use:   "load",
		Short: "Warm and mark configured local model as active",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigOnly(strings.TrimSpace(*configPath))
			if err != nil {
				return configError(err)
			}
			svc, svcErr := newLocalRuntimeService(cfg)
			if svcErr != nil {
				return svcErr
			}
			result, loadErr := svc.Load(cmd.Context(), cfg)
			if loadErr != nil {
				return loadErr
			}
			for _, line := range result.Messages {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), line)
			}
			if !result.Success {
				if result.Category == localruntime.CategoryNone || result.Category == localruntime.CategoryProviderNotLocal {
					return nil
				}
				return fmt.Errorf("%s", result.Category)
			}
			return nil
		},
	}

	unloadCmd := &cobra.Command{
		Use:   "unload",
		Short: "Release active local warm state",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigOnly(strings.TrimSpace(*configPath))
			if err != nil {
				return configError(err)
			}
			svc, svcErr := newLocalRuntimeService(cfg)
			if svcErr != nil {
				return svcErr
			}
			result, unloadErr := svc.Unload(cmd.Context(), cfg)
			if unloadErr != nil {
				return unloadErr
			}
			for _, line := range result.Messages {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), line)
			}
			if !result.Success {
				if result.Category == localruntime.CategoryNone || result.Category == localruntime.CategoryProviderNotLocal {
					return nil
				}
				return fmt.Errorf("%s", result.Category)
			}
			return nil
		},
	}

	var jsonModels bool
	modelsCmd := &cobra.Command{
		Use:   "models",
		Short: "List local GGUF models from configured model scan paths",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigOnly(strings.TrimSpace(*configPath))
			if err != nil {
				return configError(err)
			}
			if err := ensureDefaultModelsDirForConfig(&cfg); err != nil {
				return fmt.Errorf("failed to prepare default models directory: %w", err)
			}
			svc, svcErr := newLocalRuntimeService(cfg)
			if svcErr != nil {
				return svcErr
			}
			result, listErr := svc.ListModelsDetailed(cfg)
			if listErr != nil {
				return listErr
			}
			if jsonModels {
				blob, marshalErr := json.MarshalIndent(result, "", "  ")
				if marshalErr != nil {
					return marshalErr
				}
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(blob))
				return nil
			}
			if len(result.ModelsDirs) > 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Model scan paths:")
				for i, dir := range result.ModelsDirs {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %d. %s\n", i+1, dir)
				}
			}
			for _, warning := range result.Warnings {
				warning = strings.TrimSpace(warning)
				if warning == "" {
					continue
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Warning: %s\n", warning)
			}
			if len(result.Models) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No GGUF models found across configured model scan paths.")
				return nil
			}
			for i, entry := range result.Models {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%d. %s (%s)\n", i+1, entry.Name, entry.Path)
			}
			return nil
		},
	}
	modelsCmd.Flags().BoolVar(&jsonModels, "json", false, "Render models as JSON")

	pathsCmd := &cobra.Command{
		Use:   "paths",
		Short: "Manage local model scan paths",
	}

	var buildBackend string
	var buildInstallDir string
	var buildLlamaRef string
	var buildClean bool
	var buildJobs int
	var buildJSON bool
	var metalCheckJSON bool
	var sampleTemperature float64
	var sampleMaxTokens int
	buildCmd := &cobra.Command{
		Use:   "build-yzma-libs",
		Short: "Build/install TOPS-owned llama.cpp dylib runtime for YZMA (macOS arm64 Metal-only)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigOnly(strings.TrimSpace(*configPath))
			if err != nil {
				return configError(err)
			}
			svc, svcErr := newLocalRuntimeService(cfg)
			if svcErr != nil {
				return svcErr
			}
			result, buildErr := svc.BuildYZMALibs(cmd.Context(), cfg, localruntime.BuildYZMALibsOptions{
				Backend:    buildBackend,
				InstallDir: buildInstallDir,
				LlamaRef:   buildLlamaRef,
				Clean:      buildClean,
				Jobs:       buildJobs,
			})
			if buildErr != nil {
				return buildErr
			}

			if buildJSON {
				blob, marshalErr := json.MarshalIndent(result, "", "  ")
				if marshalErr != nil {
					return marshalErr
				}
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(blob))
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "status=%s stage=%s reason=%s\n", strings.TrimSpace(result.Status), strings.TrimSpace(result.Stage), strings.TrimSpace(result.Reason))
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "platform=%s arch=%s backend=%s\n", strings.TrimSpace(result.Platform), strings.TrimSpace(result.Arch), strings.TrimSpace(result.Backend))
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "install_dir=%s\n", strings.TrimSpace(result.InstallDir))
				if strings.TrimSpace(result.LlamaRef) != "" {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "llama_ref=%s\n", strings.TrimSpace(result.LlamaRef))
				}
				if strings.TrimSpace(result.LlamaCommit) != "" {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "llama_commit=%s\n", strings.TrimSpace(result.LlamaCommit))
				}
				if len(result.InstalledDylibs) > 0 {
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), "installed_dylibs:")
					for _, name := range result.InstalledDylibs {
						_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", name)
					}
				}
				if len(result.MissingDeps) > 0 {
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), "missing_dependencies:")
					for _, dep := range result.MissingDeps {
						_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  - %s (required_by=%s)\n", dep.Dependency, strings.Join(dep.RequiredBy, ","))
					}
				}
				if result.Probe != nil {
					for _, line := range formatProbeContextLines(result.Probe.ProbeCtx) {
						_, _ = fmt.Fprintln(cmd.OutOrStdout(), line)
					}
				}
				if strings.TrimSpace(result.Message) != "" {
					_, _ = fmt.Fprintln(cmd.OutOrStdout())
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), strings.TrimSpace(result.Message))
				}
			}
			if !strings.EqualFold(strings.TrimSpace(result.Status), "ok") {
				return fmt.Errorf("%s: %s", strings.TrimSpace(result.Stage), strings.TrimSpace(result.Reason))
			}
			return nil
		},
	}
	buildCmd.Flags().StringVar(&buildBackend, "backend", "metal", "Backend (metal|cpu|cuda|vulkan); this milestone supports metal only")
	buildCmd.Flags().StringVar(&buildInstallDir, "install-dir", "", "Install directory for runtime dylib bundle (default ~/.local/share/tops/yzma/lib)")
	buildCmd.Flags().StringVar(&buildLlamaRef, "llama-ref", "", "Pinned llama.cpp git tag/commit (defaults from yzma-version pin map)")
	buildCmd.Flags().BoolVar(&buildClean, "clean", false, "Remove .build/tops-yzma before building")
	buildCmd.Flags().IntVar(&buildJobs, "jobs", 0, "Build parallelism (default: CPU count)")
	buildCmd.Flags().BoolVar(&buildJSON, "json", false, "Render build result as JSON")

	sampleCmd := &cobra.Command{
		Use:   "sample <prompt>",
		Short: "Run a raw local YZMA sample completion (no ask/gen/help protocol)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigOnly(strings.TrimSpace(*configPath))
			if err != nil {
				return configError(err)
			}
			if !isLocalProviderType(cfg.Provider.Type) {
				return fmt.Errorf("configuration error: `tps local sample` is only available for yzma provider")
			}
			provider, providerErr := newProviderForLocalSample(cfg)
			if providerErr != nil {
				return providerErr
			}
			defer func() {
				if lifecycle, ok := provider.(llm.LocalModelLifecycle); ok {
					_ = lifecycle.Unload(context.Background())
				}
			}()
			resp, completionErr := provider.Complete(cmd.Context(), llm.CompletionRequest{
				UserPrompt:      strings.TrimSpace(strings.Join(args, " ")),
				Temperature:     sampleTemperature,
				MaxTokens:       sampleMaxTokens,
				SystemPrompt:    "",
				SamplingProfile: llm.SamplingProfileSample,
			})
			if completionErr != nil {
				return completionErr
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), strings.TrimSpace(resp.Content))
			return nil
		},
	}
	sampleCmd.Flags().Float64Var(&sampleTemperature, "temperature", -1, "Sampling temperature (-1 uses profile/default)")
	sampleCmd.Flags().IntVar(&sampleMaxTokens, "max-tokens", 0, "Maximum response tokens (0 uses sample/default profile)")

	metalCheckCmd := &cobra.Command{
		Use:   "metal-check",
		Short: "Probe Metal backend/device availability without model load",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigOnly(strings.TrimSpace(*configPath))
			if err != nil {
				return configError(err)
			}
			svc, svcErr := newLocalRuntimeService(cfg)
			if svcErr != nil {
				return svcErr
			}
			result, probeErr := svc.ProbeYZMAMetal(cmd.Context(), cfg)
			if probeErr != nil {
				return probeErr
			}
			if metalCheckJSON {
				blob, marshalErr := json.MarshalIndent(result, "", "  ")
				if marshalErr != nil {
					return marshalErr
				}
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(blob))
				return nil
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "status=%s stage=%s reason=%s\n", strings.TrimSpace(result.Status), strings.TrimSpace(result.Stage), strings.TrimSpace(result.Reason))
			if strings.TrimSpace(result.SubReason) != "" {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "sub_reason=%s\n", strings.TrimSpace(result.SubReason))
			}
			if strings.TrimSpace(result.EnvironmentSignal) != "" {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), strings.TrimSpace(result.EnvironmentSignal))
			}
			for _, hint := range result.Hints {
				hint = strings.TrimSpace(hint)
				if hint == "" {
					continue
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "hint=%s\n", hint)
			}
			for _, line := range formatProbeContextLines(result.ProbeCtx) {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), line)
			}
			return nil
		},
	}
	metalCheckCmd.Flags().BoolVar(&metalCheckJSON, "json", true, "Render Metal probe output as JSON")

	pathsListCmd := &cobra.Command{
		Use:   "list",
		Short: "List configured local model scan paths",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, cfgPath, err := loadConfigForMutation(strings.TrimSpace(*configPath))
			if err != nil {
				return configError(err)
			}
			if !isLocalProviderType(cfg.Provider.Type) {
				return fmt.Errorf("configuration error: `tps local paths` is only available for yzma provider")
			}
			if err := ensureDefaultModelsDirForConfig(&cfg); err != nil {
				return fmt.Errorf("failed to prepare default models directory: %w", err)
			}
			if err := config.SaveAtomic(cfgPath, cfg); err != nil {
				return fmt.Errorf("failed to save normalized config: %w", err)
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Model scan paths:")
			for i, dir := range cfg.Provider.ModelsDirs {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%d. %s\n", i+1, dir)
			}
			return nil
		},
	}

	pathsAddCmd := &cobra.Command{
		Use:   "add <dir>",
		Short: "Add a model scan path",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, cfgPath, err := loadConfigForMutation(strings.TrimSpace(*configPath))
			if err != nil {
				return configError(err)
			}
			if !isLocalProviderType(cfg.Provider.Type) {
				return fmt.Errorf("configuration error: `tps local paths` is only available for yzma provider")
			}
			addPath := canonicalCLIPath(args[0])
			if strings.TrimSpace(addPath) == "" {
				return fmt.Errorf("path is empty")
			}
			cfg.Provider.ModelsDirs = append(cfg.Provider.ModelsDirs, addPath)
			if err := cfg.ApplyDefaults(); err != nil {
				return err
			}
			if err := ensureDefaultModelsDirForConfig(&cfg); err != nil {
				return fmt.Errorf("failed to prepare default models directory: %w", err)
			}
			if err := os.MkdirAll(addPath, 0o755); err != nil {
				return fmt.Errorf("failed to create path %q: %w", addPath, err)
			}
			if err := config.SaveAtomic(cfgPath, cfg); err != nil {
				return fmt.Errorf("failed to save config: %w", err)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Added model scan path: %s\n", addPath)
			return nil
		},
	}

	pathsRemoveCmd := &cobra.Command{
		Use:   "remove <dir>",
		Short: "Remove a model scan path",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, cfgPath, err := loadConfigForMutation(strings.TrimSpace(*configPath))
			if err != nil {
				return configError(err)
			}
			if !isLocalProviderType(cfg.Provider.Type) {
				return fmt.Errorf("configuration error: `tps local paths` is only available for yzma provider")
			}
			removePath := canonicalCLIPath(args[0])
			defaultPath := canonicalCLIPath(config.DefaultModelsDir())
			if strings.EqualFold(removePath, defaultPath) {
				return fmt.Errorf("cannot remove required default model path %q", defaultPath)
			}
			filtered := make([]string, 0, len(cfg.Provider.ModelsDirs))
			removed := false
			for _, dir := range cfg.Provider.ModelsDirs {
				if strings.EqualFold(canonicalCLIPath(dir), removePath) {
					removed = true
					continue
				}
				filtered = append(filtered, dir)
			}
			if !removed {
				return fmt.Errorf("path %q is not configured", removePath)
			}
			cfg.Provider.ModelsDirs = filtered
			if err := cfg.ApplyDefaults(); err != nil {
				return err
			}
			if err := ensureDefaultModelsDirForConfig(&cfg); err != nil {
				return fmt.Errorf("failed to prepare default models directory: %w", err)
			}
			if err := config.SaveAtomic(cfgPath, cfg); err != nil {
				return fmt.Errorf("failed to save config: %w", err)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Removed model scan path: %s\n", removePath)
			return nil
		},
	}

	pathsCmd.AddCommand(pathsListCmd)
	pathsCmd.AddCommand(pathsAddCmd)
	pathsCmd.AddCommand(pathsRemoveCmd)

	cmd.AddCommand(statusCmd)
	cmd.AddCommand(doctorCmd)
	cmd.AddCommand(loadCmd)
	cmd.AddCommand(unloadCmd)
	cmd.AddCommand(modelsCmd)
	cmd.AddCommand(pathsCmd)
	cmd.AddCommand(buildCmd)
	cmd.AddCommand(sampleCmd)
	cmd.AddCommand(metalCheckCmd)
	return cmd
}

func newLocalRuntimeService(cfg config.Config) (localruntime.Service, error) {
	store, err := localruntime.NewFileStateStore("")
	if err != nil {
		return localruntime.Service{}, err
	}
	logger := obs.New(cfg.Debug.Enabled, nil)
	return localruntime.NewService(logger, store), nil
}

func formatLocalStatus(status localruntime.StatusResult) []string {
	lines := make([]string, 0, 24)
	lines = append(lines, fmt.Sprintf("Provider: %s", valueOrUnknown(status.ProviderType)))
	lines = append(lines, fmt.Sprintf("Configured model: %s", valueOrNotConfigured(status.Model)))
	lines = append(lines, fmt.Sprintf("Model path: %s", valueOrNotConfigured(status.ModelPath)))
	lines = append(lines, fmt.Sprintf("Lib path: %s", valueOrNotConfigured(status.LibPath)))
	lines = append(lines, fmt.Sprintf("Models dir: %s", valueOrNotConfigured(status.ModelsDir)))
	if len(status.ModelsDirs) > 0 {
		lines = append(lines, fmt.Sprintf("Model scan paths: %s", strings.Join(status.ModelsDirs, ", ")))
	}
	lines = append(lines, fmt.Sprintf("Model path exists: %s", yesNo(status.ModelPathExists)))
	lines = append(lines, fmt.Sprintf("Lib path exists: %s", yesNo(status.LibPathExists)))
	lines = append(lines, fmt.Sprintf("Models dir exists: %s", yesNo(status.ModelsDirExists)))
	lines = append(lines, fmt.Sprintf("Ready: %s", yesNo(status.Ready)))
	lines = append(lines, fmt.Sprintf("Warm state: %s", valueOrUnknown(status.WarmState)))
	if status.ProbeRan {
		lines = append(lines, fmt.Sprintf("Probe status: %s", valueOrUnknown(status.ProbeStatus)))
		if strings.TrimSpace(status.ProbeStage) != "" {
			lines = append(lines, fmt.Sprintf("Probe stage: %s", status.ProbeStage))
		}
		if strings.TrimSpace(status.ProbeReason) != "" {
			lines = append(lines, fmt.Sprintf("Probe reason: %s", status.ProbeReason))
		}
		if status.ProbeCategory != localruntime.CategoryNone {
			lines = append(lines, fmt.Sprintf("Probe category: %s", status.ProbeCategory))
		}
		if strings.TrimSpace(status.ProbeSubReason) != "" {
			lines = append(lines, fmt.Sprintf("Probe sub-reason: %s", status.ProbeSubReason))
		}
		if strings.TrimSpace(status.ProbeEnvSignal) != "" {
			lines = append(lines, fmt.Sprintf("Probe environment signal: %s", status.ProbeEnvSignal))
		}
		for _, hint := range status.ProbeHints {
			hint = strings.TrimSpace(hint)
			if hint == "" {
				continue
			}
			lines = append(lines, "Probe hint: "+hint)
		}
		if status.ProbeDurationMs > 0 {
			lines = append(lines, fmt.Sprintf("Probe duration: %dms", status.ProbeDurationMs))
		}
		for _, evidence := range status.ProbeEvidence {
			evidence = strings.TrimSpace(evidence)
			if evidence == "" {
				continue
			}
			lines = append(lines, "Probe evidence: "+evidence)
		}
		for _, line := range formatProbeContextLines(status.ProbeContext) {
			lines = append(lines, "Probe context: "+line)
		}
	}
	if status.Category != localruntime.CategoryNone {
		lines = append(lines, fmt.Sprintf("Category: %s", status.Category))
	}
	if strings.TrimSpace(status.LastError) != "" {
		lines = append(lines, fmt.Sprintf("Status error: %s", status.LastError))
	}
	if strings.TrimSpace(status.LikelyFix) != "" {
		lines = append(lines, fmt.Sprintf("Likely fix: %s", status.LikelyFix))
	}
	if strings.TrimSpace(status.ActiveWarmedModel) != "" {
		lines = append(lines, fmt.Sprintf("Active warmed model: %s", status.ActiveWarmedModel))
	}
	if strings.TrimSpace(status.LastWarmAt) != "" {
		lines = append(lines, fmt.Sprintf("Last warm-up: %s", prettyTimestamp(status.LastWarmAt)))
	}
	if strings.TrimSpace(status.LastWarmupStatus) != "" {
		lines = append(lines, fmt.Sprintf("Last warm-up status: %s", status.LastWarmupStatus))
	}
	if strings.TrimSpace(status.LastKnownError) != "" {
		lines = append(lines, fmt.Sprintf("Last known error: %s", status.LastKnownError))
	}
	if strings.TrimSpace(string(status.LastErrorCategory)) != "" {
		lines = append(lines, fmt.Sprintf("Last error category: %s", status.LastErrorCategory))
	}
	for _, warning := range status.Warnings {
		warning = strings.TrimSpace(warning)
		if warning == "" {
			continue
		}
		lines = append(lines, "Migration warning: "+warning)
	}
	if strings.TrimSpace(status.StatePath) != "" {
		lines = append(lines, fmt.Sprintf("State file: %s", status.StatePath))
	}
	return lines
}

func formatProbeContextLines(ctx localruntime.ProbeContext) []string {
	lines := make([]string, 0, 16)
	lines = append(lines, fmt.Sprintf("probe_source=%s", valueOrUnknown(ctx.ProbeSource)))
	lines = append(lines, fmt.Sprintf("probe_model_path=%s", valueOrNotConfigured(ctx.ModelPath)))
	lines = append(lines, fmt.Sprintf("probe_lib_path=%s", valueOrNotConfigured(ctx.LibPath)))
	lines = append(lines, fmt.Sprintf("probe_yzma_lib_env=%s", valueOrNotConfigured(ctx.YZMALibEnv)))
	lines = append(lines, fmt.Sprintf("probe_cpu_fallback=%t", ctx.CPUFallback))
	lines = append(lines, fmt.Sprintf("probe_debug_log=%t", ctx.DebugLog))
	lines = append(lines, fmt.Sprintf("probe_debug_raw=%t", ctx.DebugRaw))
	if ctx.ProcessPID > 0 {
		lines = append(lines, fmt.Sprintf("probe_pid=%d", ctx.ProcessPID))
	}
	if ctx.ProcessPPID > 0 {
		lines = append(lines, fmt.Sprintf("probe_ppid=%d", ctx.ProcessPPID))
	}
	lines = append(lines, fmt.Sprintf("probe_is_tty=%t", ctx.ProcessIsTTY))
	if strings.TrimSpace(ctx.ProcessGOOS) != "" {
		lines = append(lines, fmt.Sprintf("probe_goos=%s", strings.TrimSpace(ctx.ProcessGOOS)))
	}
	if strings.TrimSpace(ctx.ProcessGOARCH) != "" {
		lines = append(lines, fmt.Sprintf("probe_goarch=%s", strings.TrimSpace(ctx.ProcessGOARCH)))
	}
	if strings.TrimSpace(ctx.TERM) != "" {
		lines = append(lines, fmt.Sprintf("probe_term=%s", strings.TrimSpace(ctx.TERM)))
	}
	if strings.TrimSpace(ctx.SSHConnection) != "" {
		lines = append(lines, fmt.Sprintf("probe_ssh_connection=%s", strings.TrimSpace(ctx.SSHConnection)))
	}
	if strings.TrimSpace(ctx.TMUX) != "" {
		lines = append(lines, fmt.Sprintf("probe_tmux=%s", strings.TrimSpace(ctx.TMUX)))
	}
	if strings.TrimSpace(ctx.CI) != "" {
		lines = append(lines, fmt.Sprintf("probe_ci=%s", strings.TrimSpace(ctx.CI)))
	}
	if strings.TrimSpace(ctx.Uname) != "" {
		lines = append(lines, fmt.Sprintf("probe_uname=%s", strings.TrimSpace(ctx.Uname)))
	}
	if strings.TrimSpace(ctx.CPUBrand) != "" {
		lines = append(lines, fmt.Sprintf("probe_cpu_brand=%s", strings.TrimSpace(ctx.CPUBrand)))
	}
	if strings.TrimSpace(ctx.RosettaTranslated) != "" {
		lines = append(lines, fmt.Sprintf("probe_rosetta_translated=%s", strings.TrimSpace(ctx.RosettaTranslated)))
	}
	if strings.TrimSpace(ctx.ProviderInstanceID) != "" {
		lines = append(lines, fmt.Sprintf("probe_provider_instance=%s", ctx.ProviderInstanceID))
	}
	if ctx.EnsureLoadCalls > 0 {
		lines = append(lines, fmt.Sprintf("probe_ensure_load_calls=%d", ctx.EnsureLoadCalls))
	}
	lines = append(lines, fmt.Sprintf("probe_ensure_load_reused=%t", ctx.EnsureLoadReused))
	if ctx.LoadCount > 0 {
		lines = append(lines, fmt.Sprintf("probe_load_count=%d", ctx.LoadCount))
	}
	if ctx.UnloadCount > 0 {
		lines = append(lines, fmt.Sprintf("probe_unload_count=%d", ctx.UnloadCount))
	}
	if strings.TrimSpace(ctx.LifecycleState) != "" {
		lines = append(lines, fmt.Sprintf("probe_lifecycle_state=%s", strings.TrimSpace(ctx.LifecycleState)))
	}
	if strings.TrimSpace(ctx.LastCallType) != "" {
		lines = append(lines, fmt.Sprintf("probe_last_call_type=%s", strings.TrimSpace(ctx.LastCallType)))
	}
	if strings.TrimSpace(ctx.LastSamplingProfile) != "" {
		lines = append(lines, fmt.Sprintf("probe_last_sampling_profile=%s", strings.TrimSpace(ctx.LastSamplingProfile)))
	}
	if ctx.LastMaxTokens > 0 {
		lines = append(lines, fmt.Sprintf("probe_last_max_tokens=%d", ctx.LastMaxTokens))
	}
	if strings.TrimSpace(ctx.ResolvedLlamaDylibPath) != "" {
		lines = append(lines, fmt.Sprintf("probe_resolved_llama_dylib=%s", ctx.ResolvedLlamaDylibPath))
	}
	if ctx.BackendCount > 0 {
		lines = append(lines, fmt.Sprintf("probe_backend_count=%d", ctx.BackendCount))
	}
	if len(ctx.BackendNames) > 0 {
		lines = append(lines, fmt.Sprintf("probe_backend_names=%s", strings.Join(ctx.BackendNames, ",")))
	}
	if ctx.DeviceCount > 0 {
		lines = append(lines, fmt.Sprintf("probe_device_count=%d", ctx.DeviceCount))
	}
	if len(ctx.DeviceNames) > 0 {
		lines = append(lines, fmt.Sprintf("probe_device_names=%s", strings.Join(ctx.DeviceNames, ",")))
	}
	if strings.TrimSpace(ctx.GPUDeviceName) != "" {
		lines = append(lines, fmt.Sprintf("probe_gpu_device_name=%s", strings.TrimSpace(ctx.GPUDeviceName)))
	}
	if ctx.GPUDeviceMemoryFree > 0 || ctx.GPUDeviceMemoryTotal > 0 {
		lines = append(lines, fmt.Sprintf("probe_gpu_memory=free:%d total:%d", ctx.GPUDeviceMemoryFree, ctx.GPUDeviceMemoryTotal))
	}
	lines = append(lines, fmt.Sprintf("probe_context_params=n_ctx:%d n_batch:%d n_ubatch:%d n_seq_max:%d n_threads:%d n_threads_batch:%d n_gpu_layers:%d offload_kqv:%t op_offload:%t",
		ctx.ContextParams.NCtx,
		ctx.ContextParams.NBatch,
		ctx.ContextParams.NUbatch,
		ctx.ContextParams.NSeqMax,
		ctx.ContextParams.NThreads,
		ctx.ContextParams.NThreadsBatch,
		ctx.ContextParams.NGPULayers,
		ctx.ContextParams.OffloadKQV,
		ctx.ContextParams.OpOffload,
	))
	return lines
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func valueOrUnknown(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	return value
}

func valueOrNotConfigured(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "(not configured)"
	}
	return value
}

func prettyTimestamp(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return value
	}
	return parsed.Local().Format("2006-01-02 15:04:05 MST")
}

func loadConfigOnly(path string) (config.Config, error) {
	if strings.TrimSpace(path) == "" {
		return config.Load("")
	}
	return config.Load(path)
}

func loadConfigForMutation(path string) (config.Config, string, error) {
	resolvedPath, err := resolveConfigPath(path)
	if err != nil {
		return config.Config{}, "", err
	}
	cfg, err := config.Load(resolvedPath)
	if err != nil {
		return config.Config{}, "", err
	}
	return cfg, resolvedPath, nil
}

func ensureDefaultModelsDirForConfig(cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	if !isLocalProviderType(cfg.Provider.Type) {
		return nil
	}
	if err := cfg.ApplyDefaults(); err != nil {
		return err
	}
	defaultDir := canonicalCLIPath(config.DefaultModelsDir())
	if defaultDir == "" {
		return fmt.Errorf("default models directory resolved empty")
	}
	if len(cfg.Provider.ModelsDirs) == 0 || !strings.EqualFold(cfg.Provider.ModelsDirs[0], defaultDir) {
		modelsDirs := make([]string, 0, len(cfg.Provider.ModelsDirs)+1)
		modelsDirs = append(modelsDirs, defaultDir)
		for _, dir := range cfg.Provider.ModelsDirs {
			normalized := canonicalCLIPath(dir)
			if normalized == "" || strings.EqualFold(normalized, defaultDir) {
				continue
			}
			modelsDirs = append(modelsDirs, normalized)
		}
		cfg.Provider.ModelsDirs = modelsDirs
		cfg.Provider.ModelsDir = defaultDir
	}
	return os.MkdirAll(defaultDir, 0o755)
}

func isLocalProviderType(provider config.ProviderType) bool {
	return provider == config.ProviderYZMA || provider == config.ProviderLocal || provider == config.ProviderOllama
}

func canonicalCLIPath(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if value == "~" || strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			if value == "~" {
				value = home
			} else {
				value = filepath.Join(home, strings.TrimPrefix(value, "~/"))
			}
		}
	}
	value = filepath.Clean(value)
	if abs, err := filepath.Abs(value); err == nil {
		return abs
	}
	return value
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

func newProviderForLocalSample(cfg config.Config) (llm.LLMProvider, error) {
	profiles, err := modelprofile.Load("")
	if err != nil {
		return nil, fmt.Errorf("load model profiles failed: %w", err)
	}
	profile, _ := profiles.Get(cfg.Provider.Type, cfg.Provider.Model)
	logger := obs.New(cfg.Debug.Enabled, nil)
	provider, err := llm.NewFromConfig(cfg, logger, llm.ProviderOptions{ModelProfile: profile})
	if err != nil {
		return nil, fmt.Errorf("provider initialization failed: %w", err)
	}
	return provider, nil
}

func resolveConfigPath(path string) (string, error) {
	if strings.TrimSpace(path) != "" {
		return strings.TrimSpace(path), nil
	}
	return config.DefaultPath()
}

func configError(err error) error {
	return fmt.Errorf("configuration error: %w. Run `tps setup` to create or repair your config", err)
}
