package ask

import (
	"context"
	"fmt"
	"strings"
	"time"

	"tops/internal/llm"
	"tops/internal/model"
	"tops/internal/parser"
	"tops/internal/policy"
	"tops/internal/progress"
	"tops/internal/prompt"
	"tops/internal/tools"
	"tops/internal/workflow"
)

type Engine struct {
	provider llm.LLMProvider
	prompts  prompt.Builder
	parser   parser.Parser
	runner   tools.ToolRunner
	timeout  time.Duration
	planner  workflow.WorkflowPlanner
	executor workflow.WorkflowExecutor
}

func NewEngine(provider llm.LLMProvider, prompts prompt.Builder, responseParser parser.Parser, runner tools.ToolRunner, timeout time.Duration) Engine {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return Engine{
		provider: provider,
		prompts:  prompts,
		parser:   responseParser,
		runner:   runner,
		timeout:  timeout,
		planner:  workflow.NewJSONPlanner(),
		executor: workflow.NewExecutor(runner, policy.NewEngine()),
	}
}

func (e Engine) Run(ctx context.Context, req model.CoreRequest) (model.AskResult, error) {
	question := strings.TrimSpace(req.Input)
	if question == "" {
		return model.AskResult{}, fmt.Errorf("ask input is required")
	}
	progress.UpdatePhase(ctx, "planning")
	systemPrompt, userPrompt := e.prompts.BuildAskPlanningPrompt(req)
	progress.UpdatePhase(ctx, "provider")
	completion, err := e.provider.Complete(ctx, llm.CompletionRequest{
		SystemPrompt: systemPrompt,
		UserPrompt:   userPrompt,
		Temperature:  0.1,
		MaxTokens:    900,
	})
	if err != nil {
		return model.AskResult{}, err
	}
	decision, err := e.planner.Decide(ctx, completion.Content)
	if err != nil {
		return model.AskResult{}, err
	}
	if decision.Plan == nil {
		parsed, err := e.parser.ParseAsk(decision.FinalRaw)
		if err != nil {
			return model.AskResult{}, err
		}
		return parsed, nil
	}
	if !req.ExecutionEnabled {
		return blockedWorkflowAskResult("workflow execution is disabled (set execution.enabled=true to allow approved read-only steps)"), nil
	}
	if workflow.ApprovalPrompterFromContext(ctx) == nil {
		return blockedWorkflowAskResult("workflow execution requires interactive approvals (TTY stdin)"), nil
	}

	progress.UpdatePhase(ctx, "tools")
	ctx = workflow.WithExecutionPolicy(ctx, workflow.ExecutionPolicy{
		ReadOnly: workflow.ActionPermission(req.ExecutionReadOnlyPolicy),
		Write:    workflow.ActionPermission(req.ExecutionWritePolicy),
	})
	runResult, runErr := e.executor.Execute(ctx, string(req.Mode), req.Input, *decision.Plan)
	if runErr != nil {
		if runResult.Status == workflow.RunStatusDenied || runResult.Status == workflow.RunStatusBlocked {
			return blockedWorkflowAskResult(runResult.ErrorText), nil
		}
		return model.AskResult{}, fmt.Errorf("workflow execution failed: %w", runErr)
	}
	if runResult.Status != workflow.RunStatusCompleted {
		return blockedWorkflowAskResult(runResult.ErrorText), nil
	}

	evidence, provenance := stepResultsToEvidence(runResult.StepRuns)
	progress.UpdatePhase(ctx, "provider")
	systemPrompt, userPrompt = e.prompts.BuildAskPrompt(req, evidence)
	synthesized, err := e.provider.Complete(ctx, llm.CompletionRequest{
		SystemPrompt: systemPrompt,
		UserPrompt:   userPrompt,
		Temperature:  0.1,
		MaxTokens:    900,
	})
	if err != nil {
		return model.AskResult{}, err
	}
	parsed, err := e.parser.ParseAsk(synthesized.Content)
	if err != nil {
		return model.AskResult{}, err
	}
	parsed.Provenance = provenance
	return parsed, nil
}

type probe struct {
	name string
	args []string
}

func selectProbes(question string, osName string) []probe {
	q := strings.ToLower(question)
	probes := []probe{{name: "pwd"}, {name: "ls", args: []string{"-la"}}}

	if strings.Contains(q, "disk") || strings.Contains(q, "space") || strings.Contains(q, "usage") {
		probes = append(probes,
			probe{name: "df", args: []string{"-h"}},
			probe{name: "du", args: []string{"-sh", "."}},
		)
	}
	if strings.Contains(q, "memory") || strings.Contains(q, "ram") {
		probes = append(probes, probe{name: "ps", args: []string{"-Ao", "pid,comm,%mem,%cpu", "--sort=-%mem"}})
	}
	if strings.Contains(q, "port") || strings.Contains(q, "listen") {
		switch osName {
		case "linux":
			probes = append(probes, probe{name: "ss", args: []string{"-lntp"}})
		default:
			probes = append(probes, probe{name: "lsof", args: []string{"-nP", "-iTCP", "-sTCP:LISTEN"}})
		}
	}
	if strings.Contains(q, "file") || strings.Contains(q, "what is") {
		probes = append(probes,
			probe{name: "find", args: []string{".", "-maxdepth", "1", "-type", "f"}},
		)
	}
	return dedupeProbes(probes)
}

func blockedWorkflowAskResult(reason string) model.AskResult {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "workflow could not run"
	}
	return model.AskResult{
		Answer:        "I need approved workflow steps to gather evidence, but execution is currently blocked.",
		Observations:  []string{"No approved workflow steps were executed."},
		Inferences:    []string{},
		Uncertainties: []string{reason},
		Assumptions:   []string{"Execution remains read-only in TOPS v1."},
		Notes:         []string{"Enable execution in config and rerun in an interactive terminal to approve each step."},
	}
}

func stepResultsToEvidence(steps []workflow.StepResult) ([]model.ToolEvidence, []model.Provenance) {
	evidence := make([]model.ToolEvidence, 0, len(steps))
	provenance := make([]model.Provenance, 0, len(steps))
	for _, step := range steps {
		item := model.ToolEvidence{
			Command:   strings.TrimSpace(step.Command + " " + strings.Join(step.Args, " ")),
			Stdout:    step.Stdout,
			Stderr:    step.Stderr,
			ExitCode:  step.ExitCode,
			Duration:  step.Duration,
			Succeeded: step.ErrorText == "" && step.ExitCode == 0,
		}
		evidence = append(evidence, item)
		if item.Succeeded {
			provenance = append(provenance, model.Provenance{
				Source: "approved workflow step",
				Detail: item.Command,
			})
		}
	}
	return evidence, provenance
}

func dedupeProbes(items []probe) []probe {
	seen := map[string]struct{}{}
	out := make([]probe, 0, len(items))
	for _, item := range items {
		key := item.name + " " + strings.Join(item.args, " ")
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	return out
}
