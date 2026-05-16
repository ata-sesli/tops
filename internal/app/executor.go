package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"tops/internal/model"
	"tops/internal/ops/benchmetrics"
	"tops/internal/runtime/progress"
	"tops/internal/storage/commandmemory"
)

type ExecuteOptions struct {
	ForceText bool
}

func ExecuteMode(ctx context.Context, rt Runtime, mode model.Mode, input string, opts ExecuteOptions) (output string, err error) {
	if mode == model.ModeAsk && strings.EqualFold(strings.TrimSpace(input), "bye") {
		if unloadErr := rt.UnloadLocalModel(ctx); unloadErr != nil {
			return "", fmt.Errorf("provider error: failed to unload local model: %w", unloadErr)
		}
		return "Session closed. Local model was unloaded.", nil
	}

	reporter := progress.FromContext(ctx)
	reporter.Start("planning")
	defer func() {
		reporter.Finish(err)
	}()
	reporter.Update("planning")

	result, err := rt.Router.Dispatch(ctx, mode, input, rt.Config, rt.AskResponseProfile, rt.IntelligenceMode, rt.PlatformContext)
	if err != nil {
		return "", err
	}
	reporter.Update("rendering")
	renderDone := benchmetrics.StartStage(ctx, benchmetrics.StageRender)
	defer renderDone()
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
		storeGeneratedCommand(ctx, rt, input, payload)
		benchmetrics.SetOutputKind(ctx, payload.OutputKind)
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

func storeGeneratedCommand(ctx context.Context, rt Runtime, prompt string, payload model.GenResult) {
	if !shouldStoreGeneratedCommand(payload) {
		return
	}
	cwd, err := os.Getwd()
	if err != nil {
		cwd = ""
	}
	projectRoot, projectFingerprint := commandmemory.DetectProjectContext(cwd)
	store := rt.CommandMemory
	openedStore := false
	if store == nil {
		path, pathErr := commandmemory.DefaultPath()
		if pathErr != nil {
			if rt.Logger != nil {
				rt.Logger.Printf("command memory write skipped: default path unavailable: %v", pathErr)
			}
			return
		}
		sqliteStore, openErr := commandmemory.OpenSQLite(path, rt.Logger)
		if openErr != nil {
			if rt.Logger != nil {
				rt.Logger.Printf("command memory write skipped: open failed: %v", openErr)
			}
			return
		}
		store = sqliteStore
		openedStore = true
	}
	defer func() {
		if openedStore && store != nil {
			_ = store.Close()
		}
	}()

	commandText := strings.TrimSpace(payload.Command)
	scriptText := ""
	if strings.EqualFold(strings.TrimSpace(payload.OutputKind), "shell_script") {
		scriptText = commandText
	}
	if _, err := store.UpsertGenerated(ctx, commandmemory.UpsertInput{
		Title:              strings.TrimSpace(prompt),
		Prompt:             strings.TrimSpace(prompt),
		CommandText:        commandText,
		ScriptText:         scriptText,
		OutputKind:         strings.TrimSpace(payload.OutputKind),
		Shell:              strings.TrimSpace(rt.Config.Shell),
		Explanation:        strings.TrimSpace(payload.Explanation),
		Risk:               strings.Join(payload.RiskLabels, ","),
		CWD:                strings.TrimSpace(cwd),
		ProjectRoot:        strings.TrimSpace(projectRoot),
		ProjectFingerprint: strings.TrimSpace(projectFingerprint),
	}); err != nil && rt.Logger != nil {
		rt.Logger.Printf("command memory write skipped: %v", err)
	}
}

func shouldStoreGeneratedCommand(payload model.GenResult) bool {
	command := strings.TrimSpace(payload.Command)
	if command == "" {
		return false
	}
	intent := strings.ToLower(strings.TrimSpace(payload.Intent.Intent))
	switch intent {
	case "mode-boundary-guidance", "grounded-generation-blocked":
		return false
	}
	if strings.HasPrefix(strings.ToLower(command), "# workflow blocked") {
		return false
	}
	return true
}
