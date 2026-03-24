package app

import (
	"fmt"
	"time"

	"tops/internal/ask"
	"tops/internal/config"
	"tops/internal/core"
	"tops/internal/gen"
	"tops/internal/help"
	"tops/internal/llm"
	"tops/internal/modelprofile"
	"tops/internal/obs"
	"tops/internal/parser"
	"tops/internal/policy"
	"tops/internal/prompt"
	"tops/internal/render"
	"tops/internal/tools"
)

type Runtime struct {
	Config   config.Config
	Router   core.Router
	Renderer render.Renderer
	Logger   *obs.Logger
}

func NewRuntime(cfg config.Config) (Runtime, error) {
	logger := obs.New(cfg.Debug.Enabled, nil)
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
	promptBuilder := prompt.NewBuilder()
	responseParser := parser.New()
	policyEngine := policy.NewEngine()
	probeTimeout := time.Duration(cfg.Inspection.TimeoutSeconds) * time.Second

	helpEngine := help.NewEngine(provider, promptBuilder, responseParser, runner, probeTimeout)
	genEngine := gen.NewEngine(provider, promptBuilder, responseParser, policyEngine, runner)
	askEngine := ask.NewEngine(provider, promptBuilder, responseParser, runner, probeTimeout)

	router := core.NewRouter(helpEngine, genEngine, askEngine)

	return Runtime{
		Config:   cfg,
		Router:   router,
		Renderer: render.New(),
		Logger:   logger,
	}, nil
}
