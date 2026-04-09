package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"tops/internal/model"
	"tops/internal/progress"
)

type ExecuteOptions struct {
	ForceText bool
}

func ExecuteMode(ctx context.Context, rt Runtime, mode model.Mode, input string, opts ExecuteOptions) (output string, err error) {
	reporter := progress.FromContext(ctx)
	reporter.Start("planning")
	defer func() {
		reporter.Finish(err)
	}()
	reporter.Update("planning")

	result, err := rt.Router.Dispatch(ctx, mode, input, rt.Config, rt.AskResponseProfile)
	if err != nil {
		return "", err
	}
	reporter.Update("rendering")
	format := rt.Config.Output.Format
	if opts.ForceText {
		format = "text"
	}
	switch mode {
	case model.ModeHelp:
		payload, ok := result.(model.HelpResult)
		if !ok {
			return "", errors.New("invalid help response type")
		}
		return rt.Renderer.RenderHelp(payload, format)
	case model.ModeGen:
		payload, ok := result.(model.GenResult)
		if !ok {
			return "", errors.New("invalid generation response type")
		}
		return rt.Renderer.RenderGen(payload, format)
	case model.ModeAsk:
		payload, ok := result.(model.AskResult)
		if !ok {
			return "", errors.New("invalid ask response type")
		}
		return rt.Renderer.RenderAsk(payload, format)
	default:
		return "", fmt.Errorf("unsupported mode %q", mode)
	}
}

func ClassifyRuntimeError(err error) error {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "model response"), strings.Contains(msg, "json"):
		return fmt.Errorf("parser error: %w", err)
	case strings.Contains(msg, "tool"):
		return fmt.Errorf("tool-runner error: %w", err)
	case strings.Contains(msg, "provider") || strings.Contains(msg, "http"):
		return fmt.Errorf("provider error: %w", err)
	default:
		return err
	}
}
