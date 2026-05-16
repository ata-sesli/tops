package model

import (
	"strings"
	"time"
)

type Mode string

const (
	ModeHelp Mode = "help"
	ModeGen  Mode = "gen"
	ModeAsk  Mode = "ask"
)

type IntelligenceMode string

const (
	IntelligenceModeAuto     IntelligenceMode = "auto"
	IntelligenceModeBlitz    IntelligenceMode = "blitz"
	IntelligenceModeGrounded IntelligenceMode = "grounded"
)

func NormalizeIntelligenceMode(raw string) IntelligenceMode {
	switch IntelligenceMode(strings.ToLower(strings.TrimSpace(raw))) {
	case IntelligenceModeBlitz:
		return IntelligenceModeBlitz
	case IntelligenceModeGrounded:
		return IntelligenceModeGrounded
	default:
		return IntelligenceModeAuto
	}
}

type CoreRequest struct {
	Mode                    Mode               `json:"mode"`
	Input                   string             `json:"input"`
	CWD                     string             `json:"cwd"`
	Shell                   string             `json:"shell"`
	OS                      string             `json:"os"`
	PlatformContext         PlatformContext    `json:"platform_context,omitempty"`
	IntelligenceMode        IntelligenceMode   `json:"intelligence_mode,omitempty"`
	ExecutionReadOnlyPolicy string             `json:"execution_read_only_policy,omitempty"`
	ExecutionWritePolicy    string             `json:"execution_write_policy,omitempty"`
	ExecutionTraceMode      string             `json:"execution_trace_mode,omitempty"`
	AskResponseProfile      AskResponseProfile `json:"ask_response_profile,omitempty"`
}

type PlatformContext struct {
	OSFamily      string `json:"os_family,omitempty"`
	OSName        string `json:"os_name,omitempty"`
	OSVersion     string `json:"os_version,omitempty"`
	KernelName    string `json:"kernel_name,omitempty"`
	KernelVersion string `json:"kernel_version,omitempty"`
	Arch          string `json:"arch,omitempty"`
}

func NormalizeOSFamily(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "darwin", "mac", "macos":
		return "macos"
	case "linux":
		return "linux"
	case "windows", "win32", "win":
		return "windows"
	default:
		return "unknown"
	}
}

func NormalizePlatformContext(ctx PlatformContext) PlatformContext {
	ctx.OSFamily = NormalizeOSFamily(ctx.OSFamily)
	ctx.OSName = strings.TrimSpace(ctx.OSName)
	ctx.OSVersion = strings.TrimSpace(ctx.OSVersion)
	ctx.KernelName = strings.TrimSpace(ctx.KernelName)
	ctx.KernelVersion = strings.TrimSpace(ctx.KernelVersion)
	ctx.Arch = strings.TrimSpace(ctx.Arch)
	if ctx.OSName == "" {
		switch ctx.OSFamily {
		case "macos":
			ctx.OSName = "macOS"
		case "linux":
			ctx.OSName = "Linux"
		case "windows":
			ctx.OSName = "Windows"
		default:
			ctx.OSName = "Unknown"
		}
	}
	return ctx
}

type AskResponseProfile struct {
	Observations  bool `json:"observations"`
	Inferences    bool `json:"inferences"`
	Uncertainties bool `json:"uncertainties"`
	Assumptions   bool `json:"assumptions"`
	Notes         bool `json:"notes"`
}

func DefaultAskResponseProfile() AskResponseProfile {
	return AskResponseProfile{
		Observations:  true,
		Inferences:    true,
		Uncertainties: true,
		Assumptions:   true,
		Notes:         true,
	}
}

func (p AskResponseProfile) EnabledOptionalCount() int {
	count := 0
	if p.Observations {
		count++
	}
	if p.Inferences {
		count++
	}
	if p.Uncertainties {
		count++
	}
	if p.Assumptions {
		count++
	}
	if p.Notes {
		count++
	}
	return count
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

type GenIntent struct {
	Version                        string   `json:"version"`
	Goal                           string   `json:"goal"`
	OutputKind                     string   `json:"output_kind"`
	TargetShell                    string   `json:"target_shell"`
	PlatformScope                  string   `json:"platform_scope"`
	RequiresGrounding              bool     `json:"requires_grounding"`
	RequestedConstraints           []string `json:"requested_constraints"`
	SafetyNotes                    []string `json:"safety_notes"`
	AmbiguityNotes                 []string `json:"ambiguity_notes"`
	NeedsCurrentEnvironmentContext bool     `json:"needs_current_environment_context"`
}

func DefaultGenIntent() GenIntent {
	return GenIntent{
		Version:                        "v1",
		Goal:                           "",
		OutputKind:                     "single_command",
		TargetShell:                    "unknown",
		PlatformScope:                  "unspecified",
		RequiresGrounding:              false,
		RequestedConstraints:           []string{},
		SafetyNotes:                    []string{},
		AmbiguityNotes:                 []string{},
		NeedsCurrentEnvironmentContext: false,
	}
}

type GenResult struct {
	Command         string           `json:"command"`
	Explanation     string           `json:"explanation"`
	Intent          GenerationIntent `json:"intent_struct"`
	OutputKind      string           `json:"output_kind,omitempty"`
	TargetShell     string           `json:"target_shell,omitempty"`
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

type SemanticIntent struct {
	Version              string         `json:"version"`
	Operation            string         `json:"operation"`
	Entity               string         `json:"entity"`
	Scope                string         `json:"scope"`
	TargetPath           string         `json:"target_path"`
	Recursion            string         `json:"recursion"`
	Visibility           string         `json:"visibility"`
	Filters              map[string]any `json:"filters"`
	RequestedFields      []string       `json:"requested_fields"`
	Projection           []string       `json:"projection"`
	Sort                 string         `json:"sort"`
	Limit                int            `json:"limit"`
	RequiresGrounding    bool           `json:"requires_grounding"`
	ExecutionStrategy    string         `json:"execution_strategy"`
	AmbiguityNotes       []string       `json:"ambiguity_notes"`
	EffectiveGrounding   *bool          `json:"effective_requires_grounding,omitempty"`
	GroundingOverrideMsg string         `json:"grounding_override_reason,omitempty"`
}

func DefaultSemanticIntent() SemanticIntent {
	return SemanticIntent{
		Version:           "v1",
		Operation:         "inspect",
		Entity:            "custom",
		Scope:             "current_directory",
		TargetPath:        ".",
		Recursion:         "none",
		Visibility:        "visible_only",
		Filters:           map[string]any{},
		RequestedFields:   []string{},
		Projection:        []string{},
		Sort:              "",
		Limit:             0,
		RequiresGrounding: true,
		ExecutionStrategy: "direct",
		AmbiguityNotes:    []string{},
	}
}

type CommandObservation struct {
	OK                   bool     `json:"ok"`
	ExitCode             int      `json:"exit_code"`
	CommandName          string   `json:"command_name"`
	Args                 []string `json:"args"`
	CWD                  string   `json:"cwd"`
	Stdout               []string `json:"stdout"`
	Stderr               []string `json:"stderr"`
	HasOutput            bool     `json:"has_output"`
	StdoutPreviewCount   int      `json:"stdout_preview_count"`
	StdoutLineCountTotal int      `json:"stdout_line_count_total,omitempty"`
	StdoutLineCountExact bool     `json:"stdout_line_count_exact"`
	StdoutNonemptyCount  int      `json:"stdout_nonempty_line_count"`
	StdoutTruncated      bool     `json:"stdout_truncated"`
	StderrTruncated      bool     `json:"stderr_truncated"`
	MatchCount           int      `json:"match_count,omitempty"`
	MatchCountKnown      bool     `json:"match_count_known"`
	DurationMilliseconds int64    `json:"duration_ms"`
}

type ToolEvidence struct {
	Command   string        `json:"command"`
	Stdout    string        `json:"stdout"`
	Stderr    string        `json:"stderr"`
	ExitCode  int           `json:"exit_code"`
	Duration  time.Duration `json:"duration"`
	Succeeded bool          `json:"succeeded"`
}
