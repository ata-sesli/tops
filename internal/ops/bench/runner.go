package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"tops/internal/app"
	"tops/internal/config"
	"tops/internal/model"
	"tops/internal/obs"
	"tops/internal/ops/benchmetrics"
	"tops/internal/runtime/localruntime"
	"tops/internal/runtime/progress"
	"tops/internal/runtime/workflow"
)

const (
	DefaultRuns = 1
)

type Profile string

const (
	ProfileWarm Profile = "warm"
	ProfileCold Profile = "cold"
)

type DatasetCase struct {
	ID     string `json:"id"`
	Prompt string `json:"prompt"`
	Mode   string `json:"mode,omitempty"`
}

type RunRecord struct {
	Workflow                string  `json:"workflow"`
	CaseID                  string  `json:"case_id"`
	Mode                    string  `json:"mode"`
	Profile                 string  `json:"profile"`
	RunIndex                int     `json:"run_index"`
	Prompt                  string  `json:"prompt"`
	Success                 bool    `json:"success"`
	TotalMS                 int64   `json:"total_ms"`
	RouteMS                 int64   `json:"route_ms"`
	NormalizeMS             int64   `json:"normalize_ms"`
	PlannerMS               int64   `json:"planner_ms"`
	ToolExecMS              int64   `json:"tool_exec_ms"`
	RenderMS                int64   `json:"render_ms"`
	LLMCalls                int     `json:"llm_calls"`
	LLMCallMS               []int64 `json:"llm_call_ms"`
	LLMPromptTokens         []int   `json:"llm_prompt_tokens"`
	LLMCompletionTokens     []int   `json:"llm_completion_tokens"`
	ToolCalls               int     `json:"tool_calls"`
	Grounded                bool    `json:"grounded"`
	Fallback                bool    `json:"fallback"`
	RepairCount             int     `json:"repair_count"`
	AskPath                 string  `json:"ask_path"`
	AskMode                 string  `json:"ask_mode,omitempty"`
	AskStrategy             string  `json:"ask_strategy,omitempty"`
	AskEscalationReason     string  `json:"ask_escalation_reason,omitempty"`
	PlannerRepairs          int     `json:"planner_repairs,omitempty"`
	AdaptiveReplans         int     `json:"adaptive_replans,omitempty"`
	ConsecutiveLLMViolation bool    `json:"consecutive_llm_violation,omitempty"`
	LastStateTransition     string  `json:"last_state_transition,omitempty"`
	EvidenceBytesRaw        int     `json:"evidence_bytes_raw,omitempty"`
	EvidenceBytesUsed       int     `json:"evidence_bytes_used,omitempty"`
	EvidenceTruncated       bool    `json:"evidence_truncated,omitempty"`
	EvidenceRowsUsed        int     `json:"evidence_rows_used,omitempty"`
	FinalPromptTokens       int     `json:"final_prompt_tokens,omitempty"`
	ToolSchemaTokens        int     `json:"tool_schema_tokens,omitempty"`
	ContextTokens           int     `json:"context_tokens,omitempty"`
	OutputKind              string  `json:"output_kind,omitempty"`
	ErrorType               string  `json:"error_type,omitempty"`
	Error                   string  `json:"error,omitempty"`
}

type SummaryRecord struct {
	Workflow      string  `json:"workflow"`
	Mode          string  `json:"mode"`
	Profile       string  `json:"profile"`
	Count         int     `json:"count"`
	SuccessRate   float64 `json:"success_rate"`
	MeanMS        int64   `json:"mean_ms"`
	MedianMS      int64   `json:"median_ms"`
	P95MS         int64   `json:"p95_ms"`
	MinMS         int64   `json:"min_ms"`
	MaxMS         int64   `json:"max_ms"`
	AvgLLMCalls   float64 `json:"avg_llm_calls"`
	AvgToolCalls  float64 `json:"avg_tool_calls"`
	GroundingRate float64 `json:"grounding_rate"`
	FallbackRate  float64 `json:"fallback_rate"`
}

type Report struct {
	GeneratedAt string          `json:"generated_at"`
	Runs        []RunRecord     `json:"runs"`
	Summaries   []SummaryRecord `json:"summaries"`
}

type Options struct {
	Runtime      app.Runtime
	Workflows    []model.Mode
	DatasetPaths map[model.Mode]string
	Runs         int
	Profiles     []Profile
	ProgressOut  io.Writer
}

func LoadDataset(path string) ([]DatasetCase, error) {
	raw, err := os.ReadFile(strings.TrimSpace(path))
	if err != nil {
		return nil, fmt.Errorf("read dataset: %w", err)
	}
	var dataset []DatasetCase
	if err := json.Unmarshal(raw, &dataset); err != nil {
		return nil, fmt.Errorf("parse dataset: %w", err)
	}
	out := make([]DatasetCase, 0, len(dataset))
	for i, entry := range dataset {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			id = fmt.Sprintf("case_%d", i+1)
		}
		prompt := strings.TrimSpace(entry.Prompt)
		if prompt == "" {
			return nil, fmt.Errorf("dataset case %q has empty prompt", id)
		}
		out = append(out, DatasetCase{
			ID:     id,
			Prompt: prompt,
			Mode:   strings.TrimSpace(strings.ToLower(entry.Mode)),
		})
	}
	return out, nil
}

func Run(ctx context.Context, opts Options) (Report, error) {
	if len(opts.Workflows) == 0 {
		return Report{}, fmt.Errorf("at least one workflow is required")
	}
	runs := opts.Runs
	if runs <= 0 {
		runs = DefaultRuns
	}
	profiles := opts.Profiles
	if len(profiles) == 0 {
		profiles = []Profile{ProfileWarm}
	}

	report := Report{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Runs:        make([]RunRecord, 0, runs*len(opts.Workflows)*8),
	}
	for _, wf := range opts.Workflows {
		if opts.ProgressOut != nil {
			_, _ = fmt.Fprintf(opts.ProgressOut, "[bench] workflow=%s started\n", wf)
		}
		path := strings.TrimSpace(opts.DatasetPaths[wf])
		if path == "" {
			path = defaultDatasetPath(wf)
		}
		dataset, err := LoadDataset(path)
		if err != nil {
			return Report{}, fmt.Errorf("%s dataset error: %w", wf, err)
		}
		expanded := expandCases(wf, dataset, opts.Runtime.IntelligenceMode)

		for _, profile := range profiles {
			for _, benchCase := range expanded {
				for i := 1; i <= runs; i++ {
					if profile == ProfileCold {
						_ = opts.Runtime.UnloadLocalModel(ctx)
					}
					record := runOne(ctx, opts.Runtime, wf, profile, benchCase, i)
					report.Runs = append(report.Runs, record)
					if opts.ProgressOut != nil {
						_, _ = fmt.Fprintf(
							opts.ProgressOut,
							"[bench] done workflow=%s case=%s mode=%s profile=%s run=%d/%d success=%t duration=%s\n",
							record.Workflow,
							record.CaseID,
							record.Mode,
							record.Profile,
							record.RunIndex,
							runs,
							record.Success,
							time.Duration(record.TotalMS)*time.Millisecond,
						)
					}
				}
			}
		}
	}

	report.Summaries = summarizeRuns(report.Runs)
	return report, nil
}

type expandedCase struct {
	ID         string
	PromptRaw  string
	PromptExec string
	Mode       model.IntelligenceMode
}

func expandCases(workflowMode model.Mode, cases []DatasetCase, defaultMode model.IntelligenceMode) []expandedCase {
	out := make([]expandedCase, 0, len(cases)*3)
	for _, entry := range cases {
		promptExec := stripWorkflowPrefix(workflowMode, entry.Prompt)
		if workflowMode == model.ModeAsk {
			modes := []model.IntelligenceMode{}
			if mode := model.NormalizeIntelligenceMode(entry.Mode); mode != "" {
				modes = append(modes, mode)
			} else {
				modes = append(modes, model.NormalizeIntelligenceMode(string(defaultMode)))
			}
			if len(modes) == 0 {
				modes = append(modes, model.IntelligenceModeAuto)
			}
			for _, mode := range modes {
				out = append(out, expandedCase{
					ID:         entry.ID,
					PromptRaw:  entry.Prompt,
					PromptExec: promptExec,
					Mode:       mode,
				})
			}
			continue
		}

		mode := model.NormalizeIntelligenceMode(entry.Mode)
		out = append(out, expandedCase{
			ID:         entry.ID,
			PromptRaw:  entry.Prompt,
			PromptExec: promptExec,
			Mode:       mode,
		})
	}
	return out
}

func stripWorkflowPrefix(workflowMode model.Mode, prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return prompt
	}
	lower := strings.ToLower(prompt)
	prefixes := []string{
		string(workflowMode) + " ",
		"/" + string(workflowMode) + " ",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(lower, prefix) {
			return strings.TrimSpace(prompt[len(prefix):])
		}
	}
	return prompt
}

func runOne(ctx context.Context, rt app.Runtime, workflowMode model.Mode, profile Profile, benchCase expandedCase, runIndex int) RunRecord {
	collector := benchmetrics.NewCollector()
	runCtx := benchmetrics.WithCollector(ctx, collector)
	runCtx = workflow.WithApprovalPrompter(runCtx, workflow.StaticPrompter{Approve: true})
	runCtx = progress.WithReporter(runCtx, progress.NewNoop())

	runRT := rt
	runRT.IntelligenceMode = benchCase.Mode

	started := time.Now()
	_, err := app.ExecuteMode(runCtx, runRT, workflowMode, benchCase.PromptExec, app.ExecuteOptions{ForceText: true})
	total := time.Since(started)
	snapshot := collector.Snapshot()

	record := RunRecord{
		Workflow:                string(workflowMode),
		CaseID:                  benchCase.ID,
		Mode:                    string(benchCase.Mode),
		Profile:                 string(profile),
		RunIndex:                runIndex,
		Prompt:                  benchCase.PromptRaw,
		Success:                 err == nil,
		TotalMS:                 toMillis(total),
		RouteMS:                 toMillis(snapshot.Stages[benchmetrics.StageRoute]),
		NormalizeMS:             toMillis(snapshot.Stages[benchmetrics.StageNormalize]),
		PlannerMS:               toMillis(snapshot.Stages[benchmetrics.StagePlanner]),
		ToolExecMS:              toMillis(snapshot.ToolExecTime),
		RenderMS:                toMillis(snapshot.Stages[benchmetrics.StageRender]),
		LLMCalls:                snapshot.LLMCalls,
		LLMCallMS:               durationsToMillis(snapshot.LLMCallDurations),
		LLMPromptTokens:         intsOrEmpty(snapshot.LLMPromptTokens),
		LLMCompletionTokens:     intsOrEmpty(snapshot.LLMCompletionTokens),
		ToolCalls:               snapshot.ToolCalls,
		Grounded:                snapshot.Grounded,
		Fallback:                snapshot.Fallback,
		RepairCount:             snapshot.RepairCount,
		AskPath:                 snapshot.AskPath,
		AskMode:                 snapshot.AskMode,
		AskStrategy:             snapshot.AskStrategy,
		AskEscalationReason:     snapshot.AskEscalationReason,
		PlannerRepairs:          snapshot.PlannerRepairs,
		AdaptiveReplans:         snapshot.AdaptiveReplans,
		ConsecutiveLLMViolation: snapshot.ConsecutiveLLMViolation,
		LastStateTransition:     snapshot.LastStateTransition,
		EvidenceBytesRaw:        snapshot.EvidenceBytesRaw,
		EvidenceBytesUsed:       snapshot.EvidenceBytesUsed,
		EvidenceTruncated:       snapshot.EvidenceTruncated,
		EvidenceRowsUsed:        snapshot.EvidenceRowsUsed,
		FinalPromptTokens:       snapshot.FinalPromptTokens,
		ToolSchemaTokens:        snapshot.ToolSchemaTokens,
		ContextTokens:           snapshot.ContextTokens,
		OutputKind:              snapshot.OutputKind,
	}
	if err != nil {
		record.Error = err.Error()
		record.ErrorType = classifyError(err)
	}
	if workflowMode == model.ModeAsk {
		if reason := askBenchmarkFailureReason(record, benchCase.PromptExec); reason != "" {
			record.Success = false
			if strings.TrimSpace(record.Error) == "" {
				record.Error = reason
			}
			if strings.TrimSpace(record.ErrorType) == "" {
				record.ErrorType = "protocol"
			}
		}
	}
	return record
}

func askBenchmarkFailureReason(record RunRecord, promptExec string) string {
	if strings.TrimSpace(record.LastStateTransition) == "fail" {
		return "ask failed internal transition invariant: last_state_transition=fail"
	}
	if record.ConsecutiveLLMViolation {
		return "ask failed protocol invariant: consecutive_llm_violation=true"
	}
	if strings.TrimSpace(record.AskStrategy) == "conceptual" {
		return ""
	}
	if !askPromptLikelyRequiresLocalEvidence(promptExec) {
		return ""
	}
	switch strings.TrimSpace(record.AskStrategy) {
	case "reactive_tools", "planned_tools":
		if record.ToolCalls == 0 {
			if strings.Contains(strings.ToLower(promptExec), "ports") && strings.TrimSpace(record.AskEscalationReason) == "capability_unavailable" {
				return ""
			}
			return "ask failed grounding invariant: local/evidence strategy completed with zero tool calls"
		}
	}
	return ""
}

func askPromptLikelyRequiresLocalEvidence(prompt string) bool {
	lower := strings.ToLower(strings.TrimSpace(prompt))
	if lower == "" {
		return false
	}
	localSignals := []string{
		"current directory", "current folder", "working directory", "cwd",
		"in this directory", "in this folder", "in this repo", "in this repository", "in this project",
		"on this machine", "with evidence", "evidence",
		"operating system", " my os", "kernel", "hostname", "current user",
		"how many files", "how many directories", "file count", "directory count",
		"open ports", "listening ports", "disk usage",
		"python version", "installed tools", "environment variable", "env var", "git state", "git status",
	}
	for _, signal := range localSignals {
		if strings.Contains(lower, signal) {
			return true
		}
	}
	return false
}

func classifyError(err error) string {
	if err == nil {
		return ""
	}
	lower := strings.ToLower(strings.TrimSpace(err.Error()))
	switch classifyLocalRuntimeErrorText(lower) {
	case localruntime.CategoryMissingModelPath:
		return string(localruntime.CategoryMissingModelPath)
	case localruntime.CategoryMissingLibPath:
		return string(localruntime.CategoryMissingLibPath)
	case localruntime.CategoryModelPathInvalid:
		return string(localruntime.CategoryModelPathInvalid)
	case localruntime.CategoryLibPathInvalid:
		return string(localruntime.CategoryLibPathInvalid)
	case localruntime.CategoryLibLoadFailed:
		return string(localruntime.CategoryLibLoadFailed)
	case localruntime.CategoryModelLoadFailed:
		return string(localruntime.CategoryModelLoadFailed)
	case localruntime.CategoryWarmupFailed:
		return string(localruntime.CategoryWarmupFailed)
	case localruntime.CategoryUnloadFailed:
		return string(localruntime.CategoryUnloadFailed)
	case localruntime.CategoryModelNotSet:
		return string(localruntime.CategoryModelNotSet)
	}
	switch {
	case strings.Contains(lower, "parser"):
		return "parser"
	case strings.Contains(lower, "provider"):
		return "provider"
	case strings.Contains(lower, "tool"):
		return "tool"
	case strings.Contains(lower, "approval"), strings.Contains(lower, "denied"), strings.Contains(lower, "blocked"):
		return "policy"
	default:
		return "runtime"
	}
}

func shouldPreflightLocalRuntime(cfg config.Config, workflowMode model.Mode) bool {
	if cfg.Provider.Type != config.ProviderYZMA && cfg.Provider.Type != config.ProviderOllama && cfg.Provider.Type != config.ProviderLocal {
		return false
	}
	switch workflowMode {
	case model.ModeAsk, model.ModeGen:
		return true
	default:
		return false
	}
}

func benchmarkLocalRuntimePreflight(ctx context.Context, cfg config.Config, workflow string) (localruntime.BenchmarkPreflightResult, error) {
	store, err := localruntime.NewFileStateStore("")
	if err != nil {
		return localruntime.BenchmarkPreflightResult{}, err
	}
	svc := localruntime.NewService(obs.New(cfg.Debug.Enabled, nil), store)
	return svc.BenchmarkPreflight(ctx, cfg, workflow)
}

func unavailableRuns(workflowMode model.Mode, profiles []Profile, expanded []expandedCase, runs int, preflight localruntime.BenchmarkPreflightResult) []RunRecord {
	out := make([]RunRecord, 0, len(profiles)*len(expanded)*runs)
	for _, profile := range profiles {
		for _, benchCase := range expanded {
			for i := 1; i <= runs; i++ {
				out = append(out, RunRecord{
					Workflow:            string(workflowMode),
					CaseID:              benchCase.ID,
					Mode:                string(benchCase.Mode),
					Profile:             string(profile),
					RunIndex:            i,
					Prompt:              benchCase.PromptRaw,
					Success:             false,
					TotalMS:             1,
					RouteMS:             1,
					NormalizeMS:         0,
					PlannerMS:           0,
					ToolExecMS:          0,
					RenderMS:            0,
					LLMCalls:            0,
					LLMCallMS:           []int64{},
					LLMPromptTokens:     []int{},
					LLMCompletionTokens: []int{},
					ToolCalls:           0,
					Grounded:            false,
					Fallback:            false,
					RepairCount:         0,
					AskPath:             unavailableAskPath(workflowMode),
					AskMode:             string(benchCase.Mode),
					ErrorType:           string(preflight.Category),
					Error:               strings.TrimSpace(preflight.Message),
				})
			}
		}
	}
	return out
}

func splitUnavailableCases(workflowMode model.Mode, expanded []expandedCase) (blocked []expandedCase, runnable []expandedCase) {
	blocked = make([]expandedCase, 0, len(expanded))
	runnable = make([]expandedCase, 0, len(expanded))
	for _, benchCase := range expanded {
		if requiresLLMForCase(workflowMode, benchCase) {
			blocked = append(blocked, benchCase)
			continue
		}
		runnable = append(runnable, benchCase)
	}
	return blocked, runnable
}

func requiresLLMForCase(workflowMode model.Mode, benchCase expandedCase) bool {
	switch workflowMode {
	case model.ModeHelp:
		return false
	case model.ModeGen:
		return !isGenBoundaryPrompt(benchCase.PromptExec)
	default:
		return true
	}
}

func isGenBoundaryPrompt(prompt string) bool {
	lower := strings.ToLower(strings.TrimSpace(prompt))
	if lower == "" {
		return false
	}
	if containsAny(lower, "generate", "write", "create", "build", "compose", "give me a command", "make a script", "command to", "script to") {
		return false
	}
	if strings.HasPrefix(lower, "help ") || strings.HasPrefix(lower, "explain ") || strings.HasPrefix(lower, "show me help") {
		return true
	}
	if strings.HasPrefix(lower, "what does ") && strings.Contains(lower, " do") {
		return true
	}
	if strings.Contains(lower, " --help") || strings.Contains(lower, " -h") {
		return true
	}
	if (!strings.Contains(lower, "?") && !strings.HasPrefix(lower, "what") && !strings.HasPrefix(lower, "which") && !strings.HasPrefix(lower, "who")) == false {
		if containsAny(lower, "what is my", "which directory", "where am i", "who am i", "operating system", "kernel", "hostname", "current folder", "current directory") {
			return true
		}
	}
	return false
}

func containsAny(value string, parts ...string) bool {
	for _, part := range parts {
		part = strings.ToLower(strings.TrimSpace(part))
		if part == "" {
			continue
		}
		if strings.Contains(value, part) {
			return true
		}
	}
	return false
}

func classifyLocalRuntimeErrorText(lower string) localruntime.ErrorCategory {
	switch {
	case strings.Contains(lower, string(localruntime.CategoryMissingModelPath)),
		strings.Contains(lower, "missing model_path"),
		strings.Contains(lower, "provider.model_path"):
		return localruntime.CategoryMissingModelPath
	case strings.Contains(lower, string(localruntime.CategoryMissingLibPath)),
		strings.Contains(lower, "missing lib_path"),
		strings.Contains(lower, "yzma_lib"):
		return localruntime.CategoryMissingLibPath
	case strings.Contains(lower, string(localruntime.CategoryModelPathInvalid)),
		strings.Contains(lower, "model_path"),
		strings.Contains(lower, "no such file"):
		return localruntime.CategoryModelPathInvalid
	case strings.Contains(lower, string(localruntime.CategoryLibPathInvalid)),
		strings.Contains(lower, "lib_path"),
		strings.Contains(lower, "library"):
		return localruntime.CategoryLibPathInvalid
	case strings.Contains(lower, string(localruntime.CategoryLibLoadFailed)),
		strings.Contains(lower, "initialize yzma libraries failed"):
		return localruntime.CategoryLibLoadFailed
	case strings.Contains(lower, string(localruntime.CategoryModelLoadFailed)),
		strings.Contains(lower, "load yzma model"),
		strings.Contains(lower, "create yzma context"):
		return localruntime.CategoryModelLoadFailed
	case strings.Contains(lower, string(localruntime.CategoryWarmupFailed)),
		strings.Contains(lower, "warm-up failed"),
		strings.Contains(lower, "warmup failed"):
		return localruntime.CategoryWarmupFailed
	case strings.Contains(lower, string(localruntime.CategoryUnloadFailed)):
		return localruntime.CategoryUnloadFailed
	case strings.Contains(lower, string(localruntime.CategoryModelNotSet)),
		strings.Contains(lower, "provider.model is empty"):
		return localruntime.CategoryModelNotSet
	default:
		return localruntime.CategoryNone
	}
}

func summarizeRuns(runs []RunRecord) []SummaryRecord {
	type groupKey struct {
		workflow string
		mode     string
		profile  string
	}
	groups := map[groupKey][]RunRecord{}
	for _, run := range runs {
		key := groupKey{
			workflow: strings.TrimSpace(run.Workflow),
			mode:     strings.TrimSpace(run.Mode),
			profile:  strings.TrimSpace(run.Profile),
		}
		groups[key] = append(groups[key], run)
	}

	out := make([]SummaryRecord, 0, len(groups))
	for key, entries := range groups {
		if len(entries) == 0 {
			continue
		}
		latencies := make([]int64, 0, len(entries))
		success := 0
		grounded := 0
		fallback := 0
		llmCalls := 0
		toolCalls := 0
		for _, run := range entries {
			latencies = append(latencies, run.TotalMS)
			if run.Success {
				success++
			}
			if run.Grounded {
				grounded++
			}
			if run.Fallback {
				fallback++
			}
			llmCalls += run.LLMCalls
			toolCalls += run.ToolCalls
		}
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })

		count := len(entries)
		out = append(out, SummaryRecord{
			Workflow:      key.workflow,
			Mode:          key.mode,
			Profile:       key.profile,
			Count:         count,
			SuccessRate:   ratio(success, count),
			MeanMS:        int64(math.Round(avgInt64(latencies))),
			MedianMS:      percentile(latencies, 0.50),
			P95MS:         percentile(latencies, 0.95),
			MinMS:         latencies[0],
			MaxMS:         latencies[len(latencies)-1],
			AvgLLMCalls:   ratio(llmCalls, count),
			AvgToolCalls:  ratio(toolCalls, count),
			GroundingRate: ratio(grounded, count),
			FallbackRate:  ratio(fallback, count),
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Workflow != out[j].Workflow {
			return out[i].Workflow < out[j].Workflow
		}
		if out[i].Profile != out[j].Profile {
			return out[i].Profile < out[j].Profile
		}
		return out[i].Mode < out[j].Mode
	})
	return out
}

func defaultDatasetPath(mode model.Mode) string {
	return fmt.Sprintf("benchmarks/%s.json", strings.ToLower(string(mode)))
}

func toMillis(duration time.Duration) int64 {
	if duration <= 0 {
		return 0
	}
	if duration < time.Millisecond {
		return 1
	}
	return duration.Round(time.Millisecond).Milliseconds()
}

func durationsToMillis(values []time.Duration) []int64 {
	if len(values) == 0 {
		return []int64{}
	}
	out := make([]int64, 0, len(values))
	for _, value := range values {
		out = append(out, toMillis(value))
	}
	return out
}

func intsOrEmpty(values []int) []int {
	if len(values) == 0 {
		return []int{}
	}
	return append([]int(nil), values...)
}

func avgInt64(values []int64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := int64(0)
	for _, value := range values {
		sum += value
	}
	return float64(sum) / float64(len(values))
}

func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	idx := int(math.Ceil(float64(len(sorted))*p)) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func ratio(top int, bottom int) float64 {
	if bottom <= 0 {
		return 0
	}
	return float64(top) / float64(bottom)
}

func RenderHumanSummary(report Report) string {
	lines := make([]string, 0, len(report.Summaries)+3)
	lines = append(lines, "TOPS Benchmark Summary")
	lines = append(lines, "workflow mode profile count success mean(ms) p95(ms) avg_llm avg_tools grounding fallback")
	for _, row := range report.Summaries {
		lines = append(lines, fmt.Sprintf(
			"%s %s %s %d %.2f %.0f %d %.2f %.2f %.2f %.2f",
			row.Workflow,
			row.Mode,
			row.Profile,
			row.Count,
			row.SuccessRate,
			float64(row.MeanMS),
			row.P95MS,
			row.AvgLLMCalls,
			row.AvgToolCalls,
			row.GroundingRate,
			row.FallbackRate,
		))
	}
	askHeaderWritten := false
	for _, run := range report.Runs {
		if strings.TrimSpace(run.Workflow) != string(model.ModeAsk) {
			continue
		}
		if !askHeaderWritten {
			lines = append(lines, "")
			lines = append(lines, "Ask Run Details")
			lines = append(lines, "case ask_mode profile path ask_strategy llm_calls llm_call_ms total_ms route_ms normalize_ms planner_ms tool_ms render_ms repair_count planner_repairs adaptive_replans consecutive_llm_violation evidence_bytes_raw evidence_bytes_used evidence_truncated evidence_rows_used final_prompt_tokens tool_schema_tokens context_tokens last_state_transition escalation_reason")
			askHeaderWritten = true
		}
		lines = append(lines, fmt.Sprintf(
			"%s %s %s %s %s %d %v %d %d %d %d %d %d %d %d %d %t %d %d %t %d %d %d %d %s %s",
			run.CaseID,
			emptyDash(run.AskMode),
			run.Profile,
			emptyDash(run.AskPath),
			emptyDash(run.AskStrategy),
			run.LLMCalls,
			run.LLMCallMS,
			run.TotalMS,
			run.RouteMS,
			run.NormalizeMS,
			run.PlannerMS,
			run.ToolExecMS,
			run.RenderMS,
			run.RepairCount,
			run.PlannerRepairs,
			run.AdaptiveReplans,
			run.ConsecutiveLLMViolation,
			run.EvidenceBytesRaw,
			run.EvidenceBytesUsed,
			run.EvidenceTruncated,
			run.EvidenceRowsUsed,
			run.FinalPromptTokens,
			run.ToolSchemaTokens,
			run.ContextTokens,
			emptyDash(run.LastStateTransition),
			emptyDash(run.AskEscalationReason),
		))
	}
	return strings.Join(lines, "\n")
}

func emptyDash(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return value
}

func unavailableAskPath(workflowMode model.Mode) string {
	if workflowMode == model.ModeAsk {
		return "grounded"
	}
	return ""
}
