package core

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"

	"tops/internal/config"
	"tops/internal/model"
)

type HelpEngine interface {
	Run(ctx context.Context, req model.CoreRequest) (model.HelpResult, error)
}

type GenEngine interface {
	Run(ctx context.Context, req model.CoreRequest) (model.GenResult, error)
}

type AskEngine interface {
	Run(ctx context.Context, req model.CoreRequest) (model.AskResult, error)
}

type Router struct {
	helpEngine HelpEngine
	genEngine  GenEngine
	askEngine  AskEngine
}

func NewRouter(helpEngine HelpEngine, genEngine GenEngine, askEngine AskEngine) Router {
	return Router{helpEngine: helpEngine, genEngine: genEngine, askEngine: askEngine}
}

func (r Router) Dispatch(ctx context.Context, mode model.Mode, input string, cfg config.Config) (any, error) {
	request, err := normalizeRequest(mode, input, cfg)
	if err != nil {
		return nil, err
	}
	switch request.Mode {
	case model.ModeHelp:
		return r.helpEngine.Run(ctx, request)
	case model.ModeGen:
		return r.genEngine.Run(ctx, request)
	case model.ModeAsk:
		return r.askEngine.Run(ctx, request)
	default:
		return nil, fmt.Errorf("unsupported mode %q", request.Mode)
	}
}

func normalizeRequest(mode model.Mode, input string, cfg config.Config) (model.CoreRequest, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return model.CoreRequest{}, fmt.Errorf("input cannot be empty")
	}
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	shell := cfg.Shell
	if shell == "" {
		shell = "sh"
	}
	return model.CoreRequest{
		Mode:                    mode,
		Input:                   trimmed,
		CWD:                     cwd,
		Shell:                   shell,
		OS:                      runtime.GOOS,
		ExecutionEnabled:        cfg.Execution.Enabled,
		ExecutionReadOnlyPolicy: string(cfg.Execution.Permissions.ReadOnly),
		ExecutionWritePolicy:    string(cfg.Execution.Permissions.Write),
		ExecutionTraceMode:      string(cfg.Execution.TraceMode),
	}, nil
}
