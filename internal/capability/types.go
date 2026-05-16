package capability

import (
	"encoding/json"
	"fmt"
	"strings"

	"tops/internal/model"
	"tops/internal/runtime/jsonutil"
	"tops/internal/runtime/workflow"
)

const (
	ActionUseCapability = "use_capability"
	ActionFinalAnswer   = "final_answer"
	ActionClarify       = "clarify"
	ActionFail          = "fail"
)

type RiskLevel string

const (
	RiskReadOnly RiskLevel = "read_only"
)

type ArgumentSpec struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	Default     any      `json:"default,omitempty"`
}

type Capability struct {
	ID          string                                                    `json:"id"`
	Description string                                                    `json:"description"`
	Arguments   map[string]ArgumentSpec                                   `json:"arguments"`
	Examples    []string                                                  `json:"examples"`
	Risk        RiskLevel                                                 `json:"risk"`
	CompilerID  string                                                    `json:"compiler_id"`
	Compile     func(CompileContext, map[string]any) (CommandPlan, error) `json:"-"`
}

type CapabilityAction struct {
	Action        string         `json:"action"`
	CapabilityID  string         `json:"capability_id,omitempty"`
	Arguments     map[string]any `json:"arguments,omitempty"`
	FinalAnswer   string         `json:"final_answer,omitempty"`
	Clarification string         `json:"clarification,omitempty"`
	Reason        string         `json:"reason,omitempty"`
}

type CompileContext struct {
	Platform         model.PlatformContext
	CommandAvailable func(string) bool
}

type CommandPlan struct {
	Reason      string
	Step        workflow.WorkflowStep
	Unavailable string
	Postprocess func([]workflow.StepResult) model.ToolEvidence
}

func (p CommandPlan) Steps() []workflow.WorkflowStep {
	if strings.TrimSpace(p.Unavailable) != "" || strings.TrimSpace(p.Step.CommandName) == "" {
		return []workflow.WorkflowStep{}
	}
	return []workflow.WorkflowStep{p.Step}
}

func (p CommandPlan) Evidence(results []workflow.StepResult) model.ToolEvidence {
	if p.Postprocess != nil {
		return p.Postprocess(results)
	}
	if strings.TrimSpace(p.Unavailable) != "" {
		return model.ToolEvidence{
			Command:   "capability",
			Stdout:    fmt.Sprintf(`{"status":"capability_unavailable","reason":%q}`, p.Unavailable),
			ExitCode:  0,
			Succeeded: true,
		}
	}
	return truncateLinesEvidence(results, 120)
}

func ParseAction(raw string) (CapabilityAction, error) {
	var action CapabilityAction
	blob, err := jsonutil.FirstValidObject(raw)
	if err != nil {
		return CapabilityAction{}, fmt.Errorf("capability action response did not contain valid JSON object")
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(blob)), &action); err != nil {
		return CapabilityAction{}, fmt.Errorf("invalid capability action JSON: %w", err)
	}
	action.Action = strings.TrimSpace(action.Action)
	action.CapabilityID = strings.TrimSpace(action.CapabilityID)
	switch action.Action {
	case ActionUseCapability:
		if action.CapabilityID == "" {
			return CapabilityAction{}, fmt.Errorf("capability action missing capability_id")
		}
		if action.Arguments == nil {
			action.Arguments = map[string]any{}
		}
	case ActionFinalAnswer:
		if strings.TrimSpace(action.FinalAnswer) == "" {
			return CapabilityAction{}, fmt.Errorf("final_answer action missing final_answer")
		}
	case ActionClarify:
		if strings.TrimSpace(action.Clarification) == "" {
			return CapabilityAction{}, fmt.Errorf("clarify action missing clarification")
		}
	case ActionFail:
		if strings.TrimSpace(action.Reason) == "" {
			return CapabilityAction{}, fmt.Errorf("fail action missing reason")
		}
	default:
		return CapabilityAction{}, fmt.Errorf("unsupported capability action %q", action.Action)
	}
	return action, nil
}
