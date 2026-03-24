package core

import (
	"context"
	"testing"

	"tops/internal/config"
	"tops/internal/model"
)

type helpStub struct {
	last model.CoreRequest
}

func (s *helpStub) Run(_ context.Context, req model.CoreRequest) (model.HelpResult, error) {
	s.last = req
	return model.HelpResult{Target: req.Input, Summary: "ok"}, nil
}

type genStub struct{}

func (genStub) Run(_ context.Context, req model.CoreRequest) (model.GenResult, error) {
	return model.GenResult{Command: req.Input, Explanation: "ok"}, nil
}

type askStub struct{}

func (askStub) Run(_ context.Context, req model.CoreRequest) (model.AskResult, error) {
	return model.AskResult{Answer: req.Input}, nil
}

func TestRouterDispatchHelp(t *testing.T) {
	h := &helpStub{}
	r := NewRouter(h, genStub{}, askStub{})
	cfg := config.Config{Shell: "zsh"}
	out, err := r.Dispatch(context.Background(), model.ModeHelp, "  grep  ", cfg)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if _, ok := out.(model.HelpResult); !ok {
		t.Fatalf("unexpected output type: %T", out)
	}
	if h.last.Input != "grep" || h.last.Shell != "zsh" {
		t.Fatalf("request not normalized: %+v", h.last)
	}
}

func TestRouterRejectsEmptyInput(t *testing.T) {
	r := NewRouter(&helpStub{}, genStub{}, askStub{})
	_, err := r.Dispatch(context.Background(), model.ModeAsk, "   ", config.Config{Shell: "zsh"})
	if err == nil {
		t.Fatal("expected error")
	}
}
