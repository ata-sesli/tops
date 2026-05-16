package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"tops/internal/config"
	"tops/internal/intel/core"
	"tops/internal/model"
	"tops/internal/storage/commandmemory"
	"tops/internal/ui/render"
)

type helpStub struct{}

func (helpStub) Run(ctx context.Context, req model.CoreRequest) (model.HelpResult, error) {
	return model.HelpResult{Target: req.Input, Summary: "summary"}, nil
}

type genStub struct{}

func (genStub) Run(ctx context.Context, req model.CoreRequest) (model.GenResult, error) {
	return model.GenResult{Command: "echo hi", Explanation: "prints"}, nil
}

type genBlockedStub struct{}

func (genBlockedStub) Run(ctx context.Context, req model.CoreRequest) (model.GenResult, error) {
	return model.GenResult{
		Command: "# workflow blocked",
		Intent: model.GenerationIntent{
			Intent: "grounded-generation-blocked",
		},
	}, nil
}

type genScriptStub struct{}

func (genScriptStub) Run(ctx context.Context, req model.CoreRequest) (model.GenResult, error) {
	return model.GenResult{
		Command:    "#!/bin/sh\necho hi",
		OutputKind: "shell_script",
	}, nil
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

func TestExecuteModeGenStoresSuccessfulCommandMemoryEntry(t *testing.T) {
	mem := &memoryStoreFake{}
	rt := Runtime{
		Config:        config.Config{Output: config.OutputConfig{Format: "text"}, Shell: "zsh"},
		Router:        core.NewRouter(helpStub{}, genStub{}, askStub{}),
		Renderer:      render.New(),
		CommandMemory: mem,
	}
	if _, err := ExecuteMode(context.Background(), rt, model.ModeGen, "list hidden files", ExecuteOptions{}); err != nil {
		t.Fatalf("execute mode failed: %v", err)
	}
	if mem.upsertCalls != 1 {
		t.Fatalf("expected 1 memory upsert, got %d", mem.upsertCalls)
	}
	if strings.TrimSpace(mem.lastInput.Prompt) != "list hidden files" {
		t.Fatalf("unexpected stored prompt: %q", mem.lastInput.Prompt)
	}
	if strings.TrimSpace(mem.lastInput.CommandText) != "echo hi" {
		t.Fatalf("unexpected stored command: %q", mem.lastInput.CommandText)
	}
}

func TestExecuteModeGenSkipsBlockedCommandMemoryEntry(t *testing.T) {
	mem := &memoryStoreFake{}
	rt := Runtime{
		Config:        config.Config{Output: config.OutputConfig{Format: "text"}, Shell: "zsh"},
		Router:        core.NewRouter(helpStub{}, genBlockedStub{}, askStub{}),
		Renderer:      render.New(),
		CommandMemory: mem,
	}
	if _, err := ExecuteMode(context.Background(), rt, model.ModeGen, "question", ExecuteOptions{}); err != nil {
		t.Fatalf("execute mode failed: %v", err)
	}
	if mem.upsertCalls != 0 {
		t.Fatalf("expected no memory upsert for blocked payload, got %d", mem.upsertCalls)
	}
}

func TestExecuteModeGenStoresScriptInScriptText(t *testing.T) {
	mem := &memoryStoreFake{}
	rt := Runtime{
		Config:        config.Config{Output: config.OutputConfig{Format: "text"}, Shell: "zsh"},
		Router:        core.NewRouter(helpStub{}, genScriptStub{}, askStub{}),
		Renderer:      render.New(),
		CommandMemory: mem,
	}
	if _, err := ExecuteMode(context.Background(), rt, model.ModeGen, "write a script", ExecuteOptions{}); err != nil {
		t.Fatalf("execute mode failed: %v", err)
	}
	if mem.upsertCalls != 1 {
		t.Fatalf("expected 1 memory upsert, got %d", mem.upsertCalls)
	}
	if strings.TrimSpace(mem.lastInput.OutputKind) != "shell_script" {
		t.Fatalf("unexpected output kind: %q", mem.lastInput.OutputKind)
	}
	if strings.TrimSpace(mem.lastInput.ScriptText) == "" {
		t.Fatalf("expected script_text to be populated")
	}
}

type memoryStoreFake struct {
	upsertCalls int
	lastInput   commandmemory.UpsertInput
	upsertErr   error
}

func (m *memoryStoreFake) UpsertGenerated(ctx context.Context, in commandmemory.UpsertInput) (commandmemory.Item, error) {
	m.upsertCalls++
	m.lastInput = in
	if m.upsertErr != nil {
		return commandmemory.Item{}, m.upsertErr
	}
	return commandmemory.Item{ID: int64(m.upsertCalls)}, nil
}

func (m *memoryStoreFake) Search(context.Context, commandmemory.SearchOptions) ([]commandmemory.Item, error) {
	return nil, errors.New("not implemented")
}

func (m *memoryStoreFake) GetByID(context.Context, int64) (commandmemory.Item, bool, error) {
	return commandmemory.Item{}, false, errors.New("not implemented")
}

func (m *memoryStoreFake) Hide(context.Context, int64) error {
	return errors.New("not implemented")
}

func (m *memoryStoreFake) SetPinned(context.Context, int64, bool) error {
	return errors.New("not implemented")
}

func (m *memoryStoreFake) RecordRun(context.Context, int64, int, bool) error {
	return errors.New("not implemented")
}

func (m *memoryStoreFake) Close() error {
	return nil
}
