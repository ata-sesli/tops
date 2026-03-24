package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"tops/internal/policy"
	"tops/internal/progress"
	"tops/internal/tools"
)

const defaultStepTimeout = 15 * time.Second

type Executor struct {
	runner tools.ToolRunner
	policy policy.Engine
}

func NewExecutor(runner tools.ToolRunner, policyEngine policy.Engine) Executor {
	return Executor{
		runner: runner,
		policy: policyEngine,
	}
}

func (e Executor) Execute(ctx context.Context, mode string, input string, plan WorkflowPlan) (ExecutionResult, error) {
	result := ExecutionResult{
		Status:    RunStatusBlocked,
		StartedAt: time.Now(),
		StepRuns:  make([]StepResult, 0, len(plan.Steps)),
	}
	defer func() {
		result.EndedAt = time.Now()
	}()

	audit := AuditFromContext(ctx)
	prompter := ApprovalPrompterFromContext(ctx)
	executionPolicy := ExecutionPolicyFromContext(ctx)
	runID := int64(0)
	if audit.Store != nil {
		id, err := audit.Store.CreateWorkflowRun(ctx, WorkflowRunRecord{
			ChatSessionID: audit.ChatSessionID,
			Mode:          mode,
			Input:         input,
			Status:        RunStatusBlocked,
			StartedAt:     result.StartedAt,
		})
		if err == nil {
			runID = id
		}
	}
	finalizeAudit := func(status RunStatus, errText string) {
		if audit.Store == nil || runID == 0 {
			return
		}
		_ = audit.Store.UpdateWorkflowRun(ctx, runID, status, result.EndedAt, errText)
	}

	progress.UpdatePhase(ctx, "tools")
	for i, step := range plan.Steps {
		step.ID = strings.TrimSpace(step.ID)
		if step.ID == "" {
			step.ID = fmt.Sprintf("step-%d", i+1)
		}

		commandLine := strings.TrimSpace(step.CommandName + " " + strings.Join(step.Args, " "))
		classified := e.policy.Classify(commandLine)
		step.RiskLabels = classified
		actionClass := ClassifyActionClass(classified)
		progress.EmitActionStarted(ctx, step.ID, commandLine, string(actionClass))

		stepRun := StepResult{
			StepID:    step.ID,
			Index:     i + 1,
			Command:   step.CommandName,
			Args:      append([]string(nil), step.Args...),
			StartedAt: time.Now(),
			EndedAt:   time.Now(),
		}

		permission := executionPolicy.PermissionFor(actionClass)
		switch permission {
		case ActionPermissionDisallow:
			stepRun.ErrorText = fmt.Sprintf("workflow step %q blocked: %s actions are disallowed by policy", step.ID, actionClass)
			stepRun.Approved = false
			result.StepRuns = append(result.StepRuns, stepRun)
			result.Status = RunStatusBlocked
			result.ErrorText = stepRun.ErrorText
			progress.EmitPermissionDecision(ctx, step.ID, commandLine, string(actionClass), false, "policy")
			if runID != 0 && audit.Store != nil {
				_ = audit.Store.InsertWorkflowStep(ctx, toStepRecord(runID, stepRun, step))
			}
			finalizeAudit(result.Status, result.ErrorText)
			return result, errors.New(stepRun.ErrorText)
		case ActionPermissionRequest:
			progress.EmitPermissionRequested(ctx, step.ID, commandLine, string(actionClass))
			if prompter == nil {
				stepRun.ErrorText = fmt.Sprintf("workflow step %q requires interactive approval, but no approval prompter is available", step.ID)
				result.StepRuns = append(result.StepRuns, stepRun)
				result.Status = RunStatusBlocked
				result.ErrorText = stepRun.ErrorText
				progress.EmitPermissionDecision(ctx, step.ID, commandLine, string(actionClass), false, "unavailable")
				if runID != 0 && audit.Store != nil {
					_ = audit.Store.InsertWorkflowStep(ctx, toStepRecord(runID, stepRun, step))
				}
				finalizeAudit(result.Status, result.ErrorText)
				return result, ErrApprovalUnavailable
			}
			approved, err := prompter.ApproveStep(ctx, step)
			if err != nil {
				stepRun.ErrorText = err.Error()
				result.StepRuns = append(result.StepRuns, stepRun)
				result.Status = RunStatusBlocked
				result.ErrorText = err.Error()
				progress.EmitPermissionDecision(ctx, step.ID, commandLine, string(actionClass), false, "error")
				if runID != 0 && audit.Store != nil {
					_ = audit.Store.InsertWorkflowStep(ctx, toStepRecord(runID, stepRun, step))
				}
				finalizeAudit(result.Status, result.ErrorText)
				return result, err
			}
			stepRun.Approved = approved
			progress.EmitPermissionDecision(ctx, step.ID, commandLine, string(actionClass), approved, "prompt")
			if !approved {
				stepRun.ErrorText = "denied by user"
				result.StepRuns = append(result.StepRuns, stepRun)
				result.Status = RunStatusDenied
				result.ErrorText = stepRun.ErrorText
				if runID != 0 && audit.Store != nil {
					_ = audit.Store.InsertWorkflowStep(ctx, toStepRecord(runID, stepRun, step))
				}
				finalizeAudit(result.Status, result.ErrorText)
				return result, nil
			}
		case ActionPermissionAllow:
			stepRun.Approved = true
			progress.EmitPermissionDecision(ctx, step.ID, commandLine, string(actionClass), true, "policy")
		default:
			stepRun.ErrorText = fmt.Sprintf("workflow step %q blocked: unknown permission policy %q", step.ID, permission)
			stepRun.Approved = false
			result.StepRuns = append(result.StepRuns, stepRun)
			result.Status = RunStatusBlocked
			result.ErrorText = stepRun.ErrorText
			if runID != 0 && audit.Store != nil {
				_ = audit.Store.InsertWorkflowStep(ctx, toStepRecord(runID, stepRun, step))
			}
			finalizeAudit(result.Status, result.ErrorText)
			return result, errors.New(stepRun.ErrorText)
		}

		runStart := time.Now()
		toolResult, runErr := e.runner.Run(ctx, tools.ToolSpec{
			Name:    step.CommandName,
			Args:    step.Args,
			Timeout: defaultStepTimeout,
		})
		stepRun.StartedAt = runStart
		stepRun.EndedAt = time.Now()
		stepRun.Duration = toolResult.Duration
		stepRun.Stdout = truncateOutputLines(toolResult.Stdout, step.OutputLineLimit)
		stepRun.Stderr = toolResult.Stderr
		stepRun.ExitCode = toolResult.ExitCode
		if runErr != nil {
			stepRun.ErrorText = runErr.Error()
		}
		result.StepRuns = append(result.StepRuns, stepRun)
		progress.EmitActionCompleted(ctx, step.ID, commandLine, string(actionClass), stepRun.ExitCode, stepRun.ErrorText)
		if runID != 0 && audit.Store != nil {
			_ = audit.Store.InsertWorkflowStep(ctx, toStepRecord(runID, stepRun, step))
		}

		if runErr != nil {
			result.Status = RunStatusFailed
			result.ErrorText = runErr.Error()
			finalizeAudit(result.Status, result.ErrorText)
			return result, runErr
		}
	}

	result.Status = RunStatusCompleted
	result.ErrorText = ""
	finalizeAudit(result.Status, "")
	return result, nil
}

func truncateOutputLines(raw string, limit int) string {
	if limit <= 0 {
		return raw
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw
	}
	lines := strings.Split(raw, "\n")
	if len(lines) <= limit {
		return raw
	}
	truncated := append([]string{}, lines[:limit]...)
	truncated = append(truncated, fmt.Sprintf("... (%d lines omitted)", len(lines)-limit))
	return strings.Join(truncated, "\n")
}

func toStepRecord(runID int64, run StepResult, step WorkflowStep) WorkflowStepRecord {
	return WorkflowStepRecord{
		RunID:            runID,
		StepIndex:        run.Index,
		StepID:           run.StepID,
		Intent:           step.Intent,
		CommandName:      run.Command,
		Args:             run.Args,
		RiskLabels:       step.RiskLabels,
		ExpectedEvidence: step.ExpectedEvidence,
		Approved:         run.Approved,
		Stdout:           run.Stdout,
		Stderr:           run.Stderr,
		ExitCode:         run.ExitCode,
		Duration:         run.Duration,
		ErrorText:        run.ErrorText,
		Timestamp:        run.EndedAt,
	}
}

func isReadOnlyOnly(labels []string) bool {
	if len(labels) == 0 {
		return false
	}
	for _, label := range labels {
		if strings.EqualFold(label, "read-only") {
			continue
		}
		return false
	}
	return true
}
