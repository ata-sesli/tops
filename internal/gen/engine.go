package gen

import (
	"context"
	"fmt"
	"strings"

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
	policy   policy.Engine
	planner  workflow.WorkflowPlanner
	executor workflow.WorkflowExecutor
}

func NewEngine(provider llm.LLMProvider, prompts prompt.Builder, responseParser parser.Parser, policyEngine policy.Engine, runner tools.ToolRunner) Engine {
	return Engine{
		provider: provider,
		prompts:  prompts,
		parser:   responseParser,
		policy:   policyEngine,
		planner:  workflow.NewJSONPlanner(),
		executor: workflow.NewExecutor(runner, policyEngine),
	}
}

func (e Engine) Run(ctx context.Context, req model.CoreRequest) (model.GenResult, error) {
	if strings.TrimSpace(req.Input) == "" {
		return model.GenResult{}, fmt.Errorf("generation input is required")
	}
	progress.UpdatePhase(ctx, "planning")
	systemPrompt, userPrompt := e.prompts.BuildGenPlanningPrompt(req)
	progress.UpdatePhase(ctx, "provider")
	completion, err := e.provider.Complete(ctx, llm.CompletionRequest{
		SystemPrompt: systemPrompt,
		UserPrompt:   userPrompt,
		Temperature:  0.2,
		MaxTokens:    900,
	})
	if err != nil {
		return model.GenResult{}, err
	}
	decision, err := e.planner.Decide(ctx, completion.Content)
	if err != nil {
		return model.GenResult{}, err
	}
	if decision.Plan == nil {
		parsed, err := e.parser.ParseGen(decision.FinalRaw)
		if err != nil {
			return model.GenResult{}, err
		}
		parsed.RiskLabels = e.policy.Classify(parsed.Command)
		return parsed, nil
	}
	if !req.ExecutionEnabled {
		return blockedWorkflowGenResult("workflow execution is disabled (set execution.enabled=true to allow approved read-only steps)"), nil
	}
	if workflow.ApprovalPrompterFromContext(ctx) == nil {
		return blockedWorkflowGenResult("workflow execution requires interactive approvals (TTY stdin)"), nil
	}

	progress.UpdatePhase(ctx, "tools")
	ctx = workflow.WithExecutionPolicy(ctx, workflow.ExecutionPolicy{
		ReadOnly: workflow.ActionPermission(req.ExecutionReadOnlyPolicy),
		Write:    workflow.ActionPermission(req.ExecutionWritePolicy),
	})
	runResult, runErr := e.executor.Execute(ctx, string(req.Mode), req.Input, *decision.Plan)
	if runErr != nil {
		if runResult.Status == workflow.RunStatusDenied || runResult.Status == workflow.RunStatusBlocked {
			return blockedWorkflowGenResult(runResult.ErrorText), nil
		}
		return model.GenResult{}, fmt.Errorf("workflow execution failed: %w", runErr)
	}
	if runResult.Status != workflow.RunStatusCompleted {
		return blockedWorkflowGenResult(runResult.ErrorText), nil
	}

	progress.UpdatePhase(ctx, "provider")
	evidence := stepRunsSummary(runResult.StepRuns)
	systemPrompt, userPrompt = e.prompts.BuildGenPrompt(req)
	if evidence != "" {
		userPrompt = userPrompt + "\n\nObserved local evidence from approved workflow steps:\n" + evidence
	}
	synthesized, err := e.provider.Complete(ctx, llm.CompletionRequest{
		SystemPrompt: systemPrompt,
		UserPrompt:   userPrompt,
		Temperature:  0.2,
		MaxTokens:    900,
	})
	if err != nil {
		return model.GenResult{}, err
	}
	parsed, err := e.parser.ParseGen(synthesized.Content)
	if err != nil {
		return model.GenResult{}, err
	}
	parsed.RiskLabels = e.policy.Classify(parsed.Command)
	parsed.ConfidenceNotes = append(parsed.ConfidenceNotes, fmt.Sprintf("Used %d approved workflow step(s) for grounding.", len(runResult.StepRuns)))
	return parsed, nil
}

func blockedWorkflowGenResult(reason string) model.GenResult {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "workflow could not run"
	}
	return model.GenResult{
		Command:     "# workflow blocked",
		Explanation: "I need approved workflow steps to inspect the environment before generating a grounded command.",
		Assumptions: []string{"Execution remains read-only in TOPS v1."},
		Ambiguities: []string{reason},
		RiskLabels:  []string{"high-risk"},
		ConfidenceNotes: []string{
			"Enable execution in config and rerun in an interactive terminal to approve each step.",
		},
	}
}

func stepRunsSummary(steps []workflow.StepResult) string {
	var b strings.Builder
	for _, step := range steps {
		command := strings.TrimSpace(step.Command + " " + strings.Join(step.Args, " "))
		if command == "" {
			command = step.Command
		}
		fmt.Fprintf(&b, "- command: %s\n", command)
		fmt.Fprintf(&b, "  exit_code: %d\n", step.ExitCode)
		if out := strings.TrimSpace(step.Stdout); out != "" {
			if len(out) > 800 {
				out = out[:800] + "..."
			}
			fmt.Fprintf(&b, "  stdout: %s\n", strings.ReplaceAll(out, "\n", "\\n"))
		}
		if errText := strings.TrimSpace(step.Stderr); errText != "" {
			if len(errText) > 400 {
				errText = errText[:400] + "..."
			}
			fmt.Fprintf(&b, "  stderr: %s\n", strings.ReplaceAll(errText, "\n", "\\n"))
		}
	}
	return strings.TrimSpace(b.String())
}
