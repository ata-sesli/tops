package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"tops/internal/config"
	"tops/internal/intel/ask"
	"tops/internal/intel/core"
	"tops/internal/intel/gen"
	"tops/internal/intel/help"
	"tops/internal/model"
	"tops/internal/obs"
	"tops/internal/parser"
	"tops/internal/runtime/llm"
	"tops/internal/runtime/optimization"
	"tops/internal/runtime/policy"
	"tops/internal/runtime/prompt"
	"tops/internal/runtime/systemcontext"
	"tops/internal/runtime/tools"
	"tops/internal/storage/commandmemory"
	"tops/internal/storage/modelprofile"
	"tops/internal/ui/render"
)

type Runtime struct {
	Config             config.Config
	Optimization       optimization.Config
	AskResponseProfile model.AskResponseProfile
	IntelligenceMode   model.IntelligenceMode
	PlatformContext    model.PlatformContext
	Router             core.Router
	Renderer           render.Renderer
	Logger             *obs.Logger
	CommandMemory      commandmemory.Store
	localProvider      llm.LocalModelLifecycle
	localWarmer        localModelWarmer
}

func NewRuntime(cfg config.Config) (Runtime, error) {
	logger := obs.New(cfg.Debug.Enabled, nil)
	opt := optimization.Default()
	for _, warning := range cfg.Provider.MigrationWarnings {
		if strings.TrimSpace(warning) == "" {
			continue
		}
		_, _ = fmt.Fprintf(os.Stderr, "TOPS config migration warning: %s\n", warning)
		if logger != nil {
			logger.Printf("config migration warning: %s", warning)
		}
	}
	modelProfiles, err := modelprofile.Load("")
	if err != nil {
		return Runtime{}, fmt.Errorf("load model profiles failed: %w", err)
	}
	profile, _ := modelProfiles.Get(cfg.Provider.Type, cfg.Provider.Model)
	provider, err := llm.NewFromConfig(cfg, logger, llm.ProviderOptions{ModelProfile: profile})
	if err != nil {
		return Runtime{}, fmt.Errorf("provider initialization failed: %w", err)
	}
	runner := tools.NewRunner(logger)
	promptBuilder := prompt.NewBuilder(opt)
	responseParser := parser.New()
	policyEngine := policy.NewEngine()
	probeTimeout := time.Duration(cfg.Inspection.TimeoutSeconds) * time.Second
	askResponseProfile := profile.EffectiveAskResponseProfile()
	intelligenceMode := profile.EffectiveIntelligenceMode()
	platformContext := systemcontext.Detect()

	helpEngine := help.NewEngine(provider, promptBuilder, responseParser, runner, probeTimeout, opt)
	genEngine := gen.NewEngine(provider, promptBuilder, responseParser, policyEngine, runner, opt)
	askEngine := ask.NewEngine(provider, promptBuilder, responseParser, runner, probeTimeout, opt)

	router := core.NewRouter(helpEngine, genEngine, askEngine)
	commandStore := openCommandMemoryStore(logger)

	return Runtime{
		Config:             cfg,
		Optimization:       opt,
		AskResponseProfile: askResponseProfile,
		IntelligenceMode:   intelligenceMode,
		PlatformContext:    platformContext,
		Router:             router,
		Renderer:           render.New(),
		Logger:             logger,
		CommandMemory:      commandStore,
		localProvider:      localProviderLifecycle(provider),
		localWarmer:        localProviderWarmer(provider),
	}, nil
}

type localModelWarmer interface {
	Warm(ctx context.Context) error
}

func localProviderLifecycle(provider llm.LLMProvider) llm.LocalModelLifecycle {
	lifecycle, ok := provider.(llm.LocalModelLifecycle)
	if !ok {
		return nil
	}
	return lifecycle
}

func localProviderWarmer(provider llm.LLMProvider) localModelWarmer {
	warmer, ok := provider.(localModelWarmer)
	if !ok {
		return nil
	}
	return warmer
}

func (r Runtime) WarmLocalModel(ctx context.Context) error {
	if !localruntimeIsApplicable(r.Config.Provider.Type) {
		return nil
	}
	if r.localWarmer == nil {
		return nil
	}
	return r.localWarmer.Warm(ctx)
}

func (r Runtime) UnloadLocalModel(ctx context.Context) error {
	if r.localProvider == nil {
		return nil
	}
	return r.localProvider.Unload(ctx)
}

func (r Runtime) Close(ctx context.Context) error {
	var firstErr error
	if err := r.UnloadLocalModel(ctx); err != nil {
		firstErr = err
	}
	if r.CommandMemory != nil {
		if err := r.CommandMemory.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("close command memory store: %w", err)
		}
	}
	return firstErr
}

func localruntimeIsApplicable(providerType config.ProviderType) bool {
	switch providerType {
	case config.ProviderYZMA, config.ProviderLocal, config.ProviderOllama:
		return true
	default:
		return false
	}
}

func openCommandMemoryStore(logger *obs.Logger) commandmemory.Store {
	path, err := commandmemory.DefaultPath()
	if err != nil {
		if logger != nil {
			logger.Printf("command memory disabled: resolve default path failed: %v", err)
		}
		return nil
	}
	store, err := commandmemory.OpenSQLite(path, logger)
	if err != nil {
		if logger != nil {
			logger.Printf("command memory disabled: open sqlite failed path=%s err=%v", path, err)
		}
		return nil
	}
	return store
}
