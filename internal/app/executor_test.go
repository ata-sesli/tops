package app

import (
	"context"
	"strings"
	"testing"

	"tops/internal/config"
	"tops/internal/core"
	"tops/internal/model"
	"tops/internal/render"
)

type helpStub struct{}

func (helpStub) Run(ctx context.Context, req model.CoreRequest) (model.HelpResult, error) {
	return model.HelpResult{Target: req.Input, Summary: "summary"}, nil
}

type genStub struct{}

func (genStub) Run(ctx context.Context, req model.CoreRequest) (model.GenResult, error) {
	return model.GenResult{Command: "echo hi", Explanation: "prints"}, nil
}

type askStub struct{}

func (askStub) Run(ctx context.Context, req model.CoreRequest) (model.AskResult, error) {
	return model.AskResult{Answer: "answer", Observations: []string{"obs"}}, nil
}

func TestExecuteModeForceTextOverridesJSONConfig(t *testing.T) {
	rt := Runtime{
		Config:   config.Config{Output: config.OutputConfig{Format: "json"}, Shell: "zsh"},
		Router:   core.NewRouter(helpStub{}, genStub{}, askStub{}),
		Renderer: render.New(),
	}
	out, err := ExecuteMode(context.Background(), rt, model.ModeAsk, "question", ExecuteOptions{ForceText: true})
	if err != nil {
		t.Fatalf("execute mode failed: %v", err)
	}
	if strings.Contains(out, "\"answer\"") {
		t.Fatalf("expected text output, got json: %s", out)
	}
	if !strings.Contains(out, "Result") {
		t.Fatalf("expected text renderer sections, got: %s", out)
	}
}

func TestClassifyRuntimeError(t *testing.T) {
	err := ClassifyRuntimeError(context.DeadlineExceeded)
	if err == nil {
		t.Fatal("expected classified error")
	}
}
