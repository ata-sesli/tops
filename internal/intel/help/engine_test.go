package help

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"tops/internal/model"
	"tops/internal/parser"
	"tops/internal/runtime/llm"
	"tops/internal/runtime/prompt"
	"tops/internal/runtime/tools"
	"tops/internal/runtime/workflow"
)

type helpRunnerStub struct {
	result  tools.ToolResult
	err     error
	calls   int
	history []tools.ToolSpec
}

func (r *helpRunnerStub) Run(_ context.Context, spec tools.ToolSpec) (tools.ToolResult, error) {
	r.calls++
	r.history = append(r.history, spec)
	return r.result, r.err
}

func allowPolicies() (string, string) {
	return string(workflow.ActionPermissionAllow), string(workflow.ActionPermissionAllow)
}

func newLLMHelpRequestCollector(response string) (llm.MockProvider, *[]llm.CompletionRequest) {
	requests := make([]llm.CompletionRequest, 0, 1)
	return llm.MockProvider{
		CompleteFn: func(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
			requests = append(requests, req)
			return llm.CompletionResponse{Content: response}, nil
		},
	}, &requests
}

func TestHelpEngineUsesHelpSamplingProfile(t *testing.T) {
	runner := &helpRunnerStub{
		result: tools.ToolResult{
			Stdout:   "Usage: du [-a] [-h] [path]\n  -h human-readable\n  -s summary",
			ExitCode: 0,
			Duration: 2 * time.Millisecond,
		},
	}
	provider, requests := newLLMHelpRequestCollector(`{"summary":"Shows disk usage.","syntax":"du [-h] [path]","important_flags":["-h human-readable","-Z invented flag"],"examples":["du -h .","du -sh .","du -s ."],"caveats":[],"assumptions":[],"notes":[]}`)
	engine := NewEngine(provider, prompt.NewBuilder(), parser.New(), runner, 10*time.Second)
	readOnly, write := allowPolicies()

	result, err := engine.Run(context.Background(), model.CoreRequest{
		Mode:                    model.ModeHelp,
		Input:                   "du",
		ExecutionReadOnlyPolicy: readOnly,
		ExecutionWritePolicy:    write,
		PlatformContext:         model.PlatformContext{OSFamily: "macos"},
	})
	if err != nil {
		t.Fatalf("help run failed: %v", err)
	}
	if result.Summary != "Shows disk usage." {
		t.Fatalf("unexpected summary: %q", result.Summary)
	}
	if len(*requests) != 1 {
		t.Fatalf("expected one LLM request, got %d", len(*requests))
	}
	if (*requests)[0].SamplingProfile != llm.SamplingProfileHelp {
		t.Fatalf("expected help sampling profile, got %q", (*requests)[0].SamplingProfile)
	}
	if (*requests)[0].MaxTokens != 700 {
		t.Fatalf("expected help summarization max tokens=700, got %d", (*requests)[0].MaxTokens)
	}
	if runner.calls != 1 {
		t.Fatalf("expected one help tool execution, got %d", runner.calls)
	}
	if len(result.Examples) != 2 {
		t.Fatalf("expected examples to be capped to 2, got %d", len(result.Examples))
	}
	for _, flag := range result.ImportantFlags {
		if strings.Contains(flag, "-Z") {
			t.Fatalf("invented flag leaked into result: %q", flag)
		}
	}
}

func TestHelpRejectsNaturalLanguageInput(t *testing.T) {
	engine := NewEngine(llm.MockProvider{
		CompleteFn: func(_ context.Context, _ llm.CompletionRequest) (llm.CompletionResponse, error) {
			return llm.CompletionResponse{}, fmt.Errorf("unexpected llm call")
		},
	}, prompt.NewBuilder(), parser.New(), &helpRunnerStub{}, 10*time.Second)

	cases := []string{
		"How do I check disk usage?",
		"How can I list hidden files?",
		"What command shows ports?",
		"Explain how to use grep",
	}
	for _, input := range cases {
		_, err := engine.Run(context.Background(), model.CoreRequest{
			Mode:                    model.ModeHelp,
			Input:                   input,
			ExecutionReadOnlyPolicy: string(workflow.ActionPermissionAllow),
			ExecutionWritePolicy:    string(workflow.ActionPermissionAllow),
		})
		if err == nil {
			t.Fatalf("expected rejection for %q", input)
		}
		if !strings.Contains(err.Error(), "help expects a command or tool name, not a natural-language question.") {
			t.Fatalf("unexpected natural-language rejection for %q: %v", input, err)
		}
	}
}

func TestHelpRejectsUnsafeTargets(t *testing.T) {
	engine := NewEngine(llm.MockProvider{
		CompleteFn: func(_ context.Context, _ llm.CompletionRequest) (llm.CompletionResponse, error) {
			return llm.CompletionResponse{}, fmt.Errorf("unexpected llm call")
		},
	}, prompt.NewBuilder(), parser.New(), &helpRunnerStub{}, 10*time.Second)

	cases := []string{
		"--help",
		"du | grep size",
		"$(whoami)",
	}
	for _, input := range cases {
		_, err := engine.Run(context.Background(), model.CoreRequest{
			Mode:                    model.ModeHelp,
			Input:                   input,
			ExecutionReadOnlyPolicy: string(workflow.ActionPermissionAllow),
			ExecutionWritePolicy:    string(workflow.ActionPermissionAllow),
		})
		if err == nil {
			t.Fatalf("expected parse rejection for %q", input)
		}
		if !strings.Contains(err.Error(), "invalid help target:") {
			t.Fatalf("unexpected parse error for %q: %v", input, err)
		}
	}
}

func TestHelpRejectsNotAllowlistedCommand(t *testing.T) {
	engine := NewEngine(llm.MockProvider{
		CompleteFn: func(_ context.Context, _ llm.CompletionRequest) (llm.CompletionResponse, error) {
			return llm.CompletionResponse{}, fmt.Errorf("unexpected llm call")
		},
	}, prompt.NewBuilder(), parser.New(), &helpRunnerStub{}, 10*time.Second)

	_, err := engine.Run(context.Background(), model.CoreRequest{
		Mode:                    model.ModeHelp,
		Input:                   "grep",
		ExecutionReadOnlyPolicy: string(workflow.ActionPermissionAllow),
		ExecutionWritePolicy:    string(workflow.ActionPermissionAllow),
	})
	if err == nil {
		t.Fatal("expected allowlist rejection")
	}
	if !strings.Contains(err.Error(), "`grep` is not currently allowed for help inspection.") {
		t.Fatalf("unexpected allowlist error: %v", err)
	}
}

func TestHelpAcceptsConfiguredCommandTargets(t *testing.T) {
	runner := &helpRunnerStub{
		result: tools.ToolResult{
			Stdout:   "Usage: command --help\n  -h help",
			ExitCode: 0,
			Duration: 2 * time.Millisecond,
		},
	}
	provider := llm.MockProvider{
		CompleteFn: func(_ context.Context, _ llm.CompletionRequest) (llm.CompletionResponse, error) {
			return llm.CompletionResponse{}, fmt.Errorf("skip llm, force deterministic fallback")
		},
	}
	engine := NewEngine(provider, prompt.NewBuilder(), parser.New(), runner, 10*time.Second)

	cases := []string{"du", "python3", "uv", "kubectl", "docker compose", "git status"}
	for _, input := range cases {
		_, err := engine.Run(context.Background(), model.CoreRequest{
			Mode:                    model.ModeHelp,
			Input:                   input,
			ExecutionReadOnlyPolicy: string(workflow.ActionPermissionAllow),
			ExecutionWritePolicy:    string(workflow.ActionPermissionAllow),
			PlatformContext:         model.PlatformContext{OSFamily: "macos"},
		})
		if err != nil {
			t.Fatalf("expected %q to be accepted, got error: %v", input, err)
		}
	}
}

func TestHelpUnavailableMessageIsConcise(t *testing.T) {
	runner := &helpRunnerStub{
		result: tools.ToolResult{
			Stdout:   "",
			Stderr:   "",
			ExitCode: 1,
			Duration: 2 * time.Millisecond,
		},
		err: fmt.Errorf("simulated failure"),
	}
	provider := llm.MockProvider{
		CompleteFn: func(_ context.Context, _ llm.CompletionRequest) (llm.CompletionResponse, error) {
			return llm.CompletionResponse{}, fmt.Errorf("unexpected llm call")
		},
	}
	engine := NewEngine(provider, prompt.NewBuilder(), parser.New(), runner, 10*time.Second)

	result, err := engine.Run(context.Background(), model.CoreRequest{
		Mode:                    model.ModeHelp,
		Input:                   "du",
		ExecutionReadOnlyPolicy: string(workflow.ActionPermissionAllow),
		ExecutionWritePolicy:    string(workflow.ActionPermissionAllow),
		PlatformContext:         model.PlatformContext{OSFamily: "macos"},
	})
	if err != nil {
		t.Fatalf("help run failed: %v", err)
	}
	if !strings.Contains(result.Summary, "Could not retrieve help text for `du`. Tried:") {
		t.Fatalf("unexpected unavailable summary: %q", result.Summary)
	}
	if strings.Contains(result.Summary, "help retrieval failed") {
		t.Fatalf("noisy internal errors leaked: %q", result.Summary)
	}
}

func TestHelpFallsBackToDeterministicSummarizer(t *testing.T) {
	runner := &helpRunnerStub{
		result: tools.ToolResult{
			Stdout:   "Usage: du [-h] [path]\n  -h human-readable output",
			ExitCode: 0,
			Duration: 2 * time.Millisecond,
		},
	}
	provider := llm.MockProvider{
		CompleteFn: func(_ context.Context, _ llm.CompletionRequest) (llm.CompletionResponse, error) {
			return llm.CompletionResponse{Content: "this is not json"}, nil
		},
	}
	engine := NewEngine(provider, prompt.NewBuilder(), parser.New(), runner, 10*time.Second)

	result, err := engine.Run(context.Background(), model.CoreRequest{
		Mode:                    model.ModeHelp,
		Input:                   "du",
		ExecutionReadOnlyPolicy: string(workflow.ActionPermissionAllow),
		ExecutionWritePolicy:    string(workflow.ActionPermissionAllow),
		PlatformContext:         model.PlatformContext{OSFamily: "macos"},
	})
	if err != nil {
		t.Fatalf("help run failed: %v", err)
	}
	if strings.TrimSpace(result.Summary) == "" {
		t.Fatalf("expected deterministic fallback summary, got empty result")
	}
}
