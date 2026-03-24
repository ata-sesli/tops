package model

import "time"

type Mode string

const (
	ModeHelp Mode = "help"
	ModeGen  Mode = "gen"
	ModeAsk  Mode = "ask"
)

type CoreRequest struct {
	Mode                    Mode   `json:"mode"`
	Input                   string `json:"input"`
	CWD                     string `json:"cwd"`
	Shell                   string `json:"shell"`
	OS                      string `json:"os"`
	ExecutionEnabled        bool   `json:"execution_enabled"`
	ExecutionReadOnlyPolicy string `json:"execution_read_only_policy,omitempty"`
	ExecutionWritePolicy    string `json:"execution_write_policy,omitempty"`
	ExecutionTraceMode      string `json:"execution_trace_mode,omitempty"`
}

type Provenance struct {
	Source string `json:"source"`
	Detail string `json:"detail,omitempty"`
}

type HelpResult struct {
	Target         string       `json:"target"`
	Summary        string       `json:"summary"`
	Syntax         string       `json:"syntax"`
	ImportantFlags []string     `json:"important_flags"`
	Examples       []string     `json:"examples"`
	Caveats        []string     `json:"caveats"`
	Assumptions    []string     `json:"assumptions"`
	Notes          []string     `json:"notes"`
	Provenance     []Provenance `json:"provenance"`
}

type GenerationIntent struct {
	Intent      string            `json:"intent"`
	Constraints map[string]string `json:"constraints"`
	Action      string            `json:"action"`
}

type GenResult struct {
	Command         string           `json:"command"`
	Explanation     string           `json:"explanation"`
	Intent          GenerationIntent `json:"intent_struct"`
	Assumptions     []string         `json:"assumptions"`
	Ambiguities     []string         `json:"ambiguities"`
	RiskLabels      []string         `json:"risk_labels"`
	ConfidenceNotes []string         `json:"confidence_notes"`
}

type AskResult struct {
	Answer        string       `json:"answer"`
	Observations  []string     `json:"observations"`
	Inferences    []string     `json:"inferences"`
	Uncertainties []string     `json:"uncertainties"`
	Assumptions   []string     `json:"assumptions"`
	Notes         []string     `json:"notes"`
	Provenance    []Provenance `json:"provenance"`
}

type ToolEvidence struct {
	Command   string        `json:"command"`
	Stdout    string        `json:"stdout"`
	Stderr    string        `json:"stderr"`
	ExitCode  int           `json:"exit_code"`
	Duration  time.Duration `json:"duration"`
	Succeeded bool          `json:"succeeded"`
}
