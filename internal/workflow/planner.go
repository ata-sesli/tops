package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"tops/internal/workflow/functions"
)

type JSONPlanner struct {
	registry functions.FunctionRegistry
}

func NewJSONPlanner() JSONPlanner {
	return JSONPlanner{registry: functions.NewDefaultRegistry()}
}

func NewJSONPlannerWithRegistry(reg functions.FunctionRegistry) JSONPlanner {
	if reg == nil {
		reg = functions.NewDefaultRegistry()
	}
	return JSONPlanner{registry: reg}
}

func (p JSONPlanner) Decide(_ context.Context, raw string) (PlanningDecision, error) {
	blob, err := extractJSONObject(raw)
	if err != nil {
		return PlanningDecision{}, err
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(blob), &fields); err != nil {
		return PlanningDecision{}, fmt.Errorf("invalid model JSON: %w", err)
	}
	planRaw, hasPlan := fields["workflow_plan"]
	if !hasPlan {
		return PlanningDecision{FinalRaw: raw}, nil
	}

	var payload struct {
		Reason string    `json:"reason"`
		Steps  []rawStep `json:"steps"`
	}
	dec := json.NewDecoder(strings.NewReader(string(planRaw)))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	if err := dec.Decode(&payload); err != nil {
		return PlanningDecision{}, fmt.Errorf("invalid workflow plan JSON: %w", err)
	}
	if len(payload.Steps) == 0 {
		return PlanningDecision{}, fmt.Errorf("workflow plan must include at least one step")
	}
	steps := make([]WorkflowStep, 0, len(payload.Steps))
	for i := range payload.Steps {
		step, err := p.normalizeStep(payload.Steps[i], i)
		if err != nil {
			return PlanningDecision{}, err
		}
		steps = append(steps, step)
	}

	return PlanningDecision{
		Plan: &WorkflowPlan{
			Reason: strings.TrimSpace(payload.Reason),
			Steps:  steps,
		},
	}, nil
}

type rawStep struct {
	ID               json.RawMessage `json:"id"`
	Intent           string          `json:"intent"`
	CommandName      string          `json:"command_name"`
	Args             []string        `json:"args"`
	FunctionName     string          `json:"function_name"`
	FunctionArgs     json.RawMessage `json:"function_args"`
	ExpectedEvidence string          `json:"expected_evidence"`
	RiskLabels       []string        `json:"risk_labels"`
	OutputLineLimit  int             `json:"output_line_limit,omitempty"`
}

func (p JSONPlanner) normalizeStep(raw rawStep, i int) (WorkflowStep, error) {
	stepID, err := normalizeStepID(raw.ID, i)
	if err != nil {
		return WorkflowStep{}, fmt.Errorf("invalid workflow step %d id: %w", i+1, err)
	}

	step := WorkflowStep{
		ID:               stepID,
		Intent:           strings.TrimSpace(raw.Intent),
		ExpectedEvidence: strings.TrimSpace(raw.ExpectedEvidence),
		RiskLabels:       append([]string(nil), raw.RiskLabels...),
		OutputLineLimit:  raw.OutputLineLimit,
	}

	functionName := strings.TrimSpace(raw.FunctionName)
	commandName := strings.TrimSpace(raw.CommandName)
	if functionName != "" && commandName != "" {
		return WorkflowStep{}, fmt.Errorf("workflow step %s cannot define both function_name and command_name", step.ID)
	}
	if functionName == "" && commandName == "" {
		return WorkflowStep{}, fmt.Errorf("workflow step %s is missing function_name or command_name", step.ID)
	}
	if functionName != "" {
		if p.registry == nil {
			p.registry = functions.NewDefaultRegistry()
		}
		def, ok := p.registry.Get(functionName)
		if !ok {
			return WorkflowStep{}, fmt.Errorf("workflow step %s references unknown function %q", step.ID, functionName)
		}
		functionArgs, err := normalizeFunctionArgs(raw.FunctionArgs)
		if err != nil {
			return WorkflowStep{}, fmt.Errorf("workflow step %s invalid function_args: %w", step.ID, err)
		}
		command, argv, expected, outputLineLimit, resolveErr := def.Resolve(functionArgs)
		if resolveErr != nil {
			return WorkflowStep{}, fmt.Errorf("workflow step %s invalid function_args: %w", step.ID, resolveErr)
		}
		step.CommandName = strings.TrimSpace(command)
		step.Args = append([]string(nil), argv...)
		if step.ExpectedEvidence == "" {
			step.ExpectedEvidence = strings.TrimSpace(expected)
		}
		if step.OutputLineLimit <= 0 {
			step.OutputLineLimit = outputLineLimit
		}
	} else {
		step.CommandName = commandName
		step.Args = append([]string(nil), raw.Args...)
	}

	if step.CommandName == "" {
		return WorkflowStep{}, fmt.Errorf("workflow step %s is missing command_name", step.ID)
	}
	for _, arg := range step.Args {
		if strings.ContainsAny(arg, "\n\r") {
			return WorkflowStep{}, fmt.Errorf("workflow step %s has invalid multiline argument", step.ID)
		}
	}
	if step.OutputLineLimit < 0 {
		return WorkflowStep{}, fmt.Errorf("workflow step %s has invalid output_line_limit", step.ID)
	}
	return step, nil
}

func normalizeFunctionArgs(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 || isJSONNull(raw) {
		return map[string]any{}, nil
	}

	var asMap map[string]any
	if err := json.Unmarshal(raw, &asMap); err == nil {
		if asMap == nil {
			return map[string]any{}, nil
		}
		return asMap, nil
	}

	var asArray []any
	if err := json.Unmarshal(raw, &asArray); err == nil {
		if len(asArray) == 0 {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("function_args must be an object; only an empty array may be used as a zero-arg fallback")
	}

	return nil, fmt.Errorf("function_args must be an object or empty array")
}

func normalizeStepID(raw json.RawMessage, idx int) (string, error) {
	if len(raw) == 0 {
		return fmt.Sprintf("step-%d", idx+1), nil
	}

	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		asString = strings.TrimSpace(asString)
		if asString == "" {
			return fmt.Sprintf("step-%d", idx+1), nil
		}
		return asString, nil
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var asNumber json.Number
	if err := dec.Decode(&asNumber); err == nil {
		out := strings.TrimSpace(asNumber.String())
		if out == "" {
			return fmt.Sprintf("step-%d", idx+1), nil
		}
		return out, nil
	}

	return "", fmt.Errorf("id must be string or number")
}

func extractJSONObject(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "```") {
		trimmed = strings.TrimPrefix(trimmed, "```json")
		trimmed = strings.TrimPrefix(trimmed, "```")
		trimmed = strings.TrimSuffix(trimmed, "```")
		trimmed = strings.TrimSpace(trimmed)
	}
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start == -1 || end == -1 || end <= start {
		return "", fmt.Errorf("model response did not contain valid JSON object")
	}
	return trimmed[start : end+1], nil
}

func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}
