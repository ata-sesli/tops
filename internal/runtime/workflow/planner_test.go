package workflow

import (
	"context"
	"strings"
	"testing"
)

func TestJSONPlannerFinalResponsePath(t *testing.T) {
	planner := NewJSONPlanner()
	raw := `{"answer":"Linux","observations":[],"inferences":[],"uncertainties":[],"assumptions":[],"notes":[]}`

	decision, err := planner.Decide(context.Background(), raw)
	if err != nil {
		t.Fatalf("decide failed: %v", err)
	}
	if decision.Plan != nil {
		t.Fatalf("expected no plan for final response, got %+v", decision.Plan)
	}
	if decision.FinalRaw == "" {
		t.Fatal("expected final raw payload to be returned")
	}
}

func TestJSONPlannerWorkflowPlanPath(t *testing.T) {
	planner := NewJSONPlanner()
	raw := `{
		"workflow_plan": {
			"reason": "Need local evidence",
			"steps": [
				{"id":"s1","intent":"inspect cwd","command_name":"pwd","args":[],"expected_evidence":"cwd"}
			]
		}
	}`

	decision, err := planner.Decide(context.Background(), raw)
	if err != nil {
		t.Fatalf("decide failed: %v", err)
	}
	if decision.Plan == nil {
		t.Fatal("expected workflow plan")
	}
	if len(decision.Plan.Steps) != 1 {
		t.Fatalf("expected one step, got %d", len(decision.Plan.Steps))
	}
	if decision.Plan.Steps[0].CommandName != "pwd" {
		t.Fatalf("unexpected command name: %q", decision.Plan.Steps[0].CommandName)
	}
}

func TestJSONPlannerWorkflowPlanFunctionStep(t *testing.T) {
	planner := NewJSONPlanner()
	raw := `{
		"workflow_plan": {
			"reason": "Need OS information",
			"steps": [
				{"id":1,"intent":"inspect os","function_name":"run_readonly_command","function_args":{"command_name":"uname","args":["-srm"]},"expected_evidence":"os"}
			]
		}
	}`

	decision, err := planner.Decide(context.Background(), raw)
	if err != nil {
		t.Fatalf("decide failed: %v", err)
	}
	if decision.Plan == nil || len(decision.Plan.Steps) != 1 {
		t.Fatalf("expected one resolved workflow step, got %+v", decision.Plan)
	}
	step := decision.Plan.Steps[0]
	if step.ID != "1" {
		t.Fatalf("expected normalized id=1, got %q", step.ID)
	}
	if step.CommandName != "uname" {
		t.Fatalf("expected resolved command uname, got %q", step.CommandName)
	}
	if strings.Join(step.Args, " ") != "-srm" {
		t.Fatalf("unexpected resolved args: %#v", step.Args)
	}
}

func TestJSONPlannerWorkflowPlanFunctionStepNormalizesEmptyArrayArgs(t *testing.T) {
	planner := NewJSONPlanner()
	raw := `{
		"workflow_plan": {
			"reason": "Need OS information",
			"steps": [
				{"id":"s1","intent":"inspect os","function_name":"run_readonly_command","function_args":[],"expected_evidence":"os"}
			]
		}
	}`

	_, err := planner.Decide(context.Background(), raw)
	if err == nil {
		t.Fatal("expected error because command_name is required when function_args is empty")
	}
	if !strings.Contains(err.Error(), "command_name is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestJSONPlannerWorkflowPlanFunctionStepRejectsNonEmptyArrayArgs(t *testing.T) {
	planner := NewJSONPlanner()
	raw := `{
		"workflow_plan": {
			"reason": "Need OS information",
			"steps": [
				{"id":"s1","intent":"inspect os","function_name":"run_readonly_command","function_args":[1],"expected_evidence":"os"}
			]
		}
	}`

	_, err := planner.Decide(context.Background(), raw)
	if err == nil {
		t.Fatal("expected error for non-empty array function_args")
	}
	if !strings.Contains(err.Error(), "function_args") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestJSONPlannerWorkflowPlanFunctionStepNormalizesNullArgs(t *testing.T) {
	planner := NewJSONPlanner()
	raw := `{
		"workflow_plan": {
			"reason": "Need OS information",
			"steps": [
				{"id":"s1","intent":"inspect os","function_name":"run_readonly_command","function_args":null,"expected_evidence":"os"}
			]
		}
	}`

	_, err := planner.Decide(context.Background(), raw)
	if err == nil {
		t.Fatal("expected error because null function_args omit required command_name")
	}
	if !strings.Contains(err.Error(), "command_name is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestJSONPlannerRejectsEmptyStepList(t *testing.T) {
	planner := NewJSONPlanner()
	raw := `{"workflow_plan":{"reason":"x","steps":[]}}`
	if _, err := planner.Decide(context.Background(), raw); err == nil {
		t.Fatal("expected error for empty step list")
	}
}

func TestJSONPlannerRejectsUnknownFunction(t *testing.T) {
	planner := NewJSONPlanner()
	raw := `{
		"workflow_plan": {
			"reason":"x",
			"steps":[
				{"id":"s1","intent":"x","function_name":"does_not_exist","function_args":{}}
			]
		}
	}`
	_, err := planner.Decide(context.Background(), raw)
	if err == nil {
		t.Fatal("expected error for unknown function_name")
	}
	if !strings.Contains(err.Error(), "unknown function") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestJSONPlannerDecideHandlesNoisyMultiObjectResponse(t *testing.T) {
	planner := NewJSONPlanner()
	raw := `prefix {"note":"ignore me"} middle
{"workflow_plan":{"reason":"Need cwd","steps":[{"id":"s1","intent":"read cwd","function_name":"run_readonly_command","function_args":{"command_name":"pwd","args":[]}}]}}
suffix`

	decision, err := planner.Decide(context.Background(), raw)
	if err != nil {
		t.Fatalf("decide failed: %v", err)
	}
	if decision.Plan == nil {
		t.Fatal("expected workflow plan to be selected from noisy response")
	}
	if len(decision.Plan.Steps) != 1 || decision.Plan.Steps[0].CommandName != "pwd" {
		t.Fatalf("unexpected plan steps: %+v", decision.Plan.Steps)
	}
}
