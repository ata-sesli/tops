package benchmetrics

import (
	"context"
	"strings"
	"sync"
	"time"
)

const (
	StageRoute     = "route"
	StageNormalize = "normalize"
	StagePlanner   = "planner"
	StageRender    = "render"
)

type Snapshot struct {
	Stages                  map[string]time.Duration
	LLMCalls                int
	LLMTime                 time.Duration
	LLMCallDurations        []time.Duration
	LLMPromptTokens         []int
	LLMCompletionTokens     []int
	ToolCalls               int
	ToolExecTime            time.Duration
	Grounded                bool
	Fallback                bool
	RepairCount             int
	OutputKind              string
	AskPath                 string
	AskMode                 string
	AskStrategy             string
	AskEscalationReason     string
	PlannerRepairs          int
	AdaptiveReplans         int
	ConsecutiveLLMViolation bool
	LastStateTransition     string
	EvidenceBytesRaw        int
	EvidenceBytesUsed       int
	EvidenceTruncated       bool
	EvidenceRowsUsed        int
	FinalPromptTokens       int
	ToolSchemaTokens        int
	ContextTokens           int
}

type Collector struct {
	mu sync.Mutex

	stages                  map[string]time.Duration
	llmCalls                int
	llmTime                 time.Duration
	llmCallDurations        []time.Duration
	llmPromptTokens         []int
	llmCompletionTokens     []int
	toolCalls               int
	toolExecTime            time.Duration
	grounded                bool
	fallback                bool
	repairCount             int
	outputKind              string
	askPath                 string
	askMode                 string
	askStrategy             string
	askEscalationReason     string
	plannerRepairs          int
	adaptiveReplans         int
	consecutiveLLMViolation bool
	lastStateTransition     string
	evidenceBytesRaw        int
	evidenceBytesUsed       int
	evidenceTruncated       bool
	evidenceRowsUsed        int
	finalPromptTokens       int
	toolSchemaTokens        int
	contextTokens           int
}

func NewCollector() *Collector {
	return &Collector{
		stages: make(map[string]time.Duration),
	}
}

func (c *Collector) AddStage(name string, duration time.Duration) {
	if c == nil {
		return
	}
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" || duration <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stages[name] += duration
}

func (c *Collector) RecordLLMCall(duration time.Duration) {
	c.RecordLLMCallWithUsage(duration, 0, 0)
}

func (c *Collector) RecordLLMCallWithUsage(duration time.Duration, promptTokens int, completionTokens int) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.llmCalls++
	if duration > 0 {
		c.llmTime += duration
		c.llmCallDurations = append(c.llmCallDurations, duration)
	} else {
		c.llmCallDurations = append(c.llmCallDurations, 0)
	}
	c.llmPromptTokens = append(c.llmPromptTokens, max0(promptTokens))
	c.llmCompletionTokens = append(c.llmCompletionTokens, max0(completionTokens))
}

func (c *Collector) RecordToolCall(duration time.Duration) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.toolCalls++
	c.grounded = true
	if duration > 0 {
		c.toolExecTime += duration
	}
}

func (c *Collector) MarkGrounded() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.grounded = true
}

func (c *Collector) MarkFallback() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fallback = true
}

func (c *Collector) IncrementRepair() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.repairCount++
}

func (c *Collector) SetOutputKind(value string) {
	if c == nil {
		return
	}
	value = strings.TrimSpace(value)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.outputKind = value
}

func (c *Collector) SetAskPath(value string) {
	if c == nil {
		return
	}
	value = strings.TrimSpace(value)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.askPath = value
}

func (c *Collector) SetAskEscalationReason(value string) {
	if c == nil {
		return
	}
	value = strings.TrimSpace(value)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.askEscalationReason = value
}

func (c *Collector) SetAskEscalationReasonIfEmpty(value string) {
	if c == nil {
		return
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if strings.TrimSpace(c.askEscalationReason) != "" {
		return
	}
	c.askEscalationReason = value
}

func (c *Collector) SetAskMode(value string) {
	if c == nil {
		return
	}
	value = strings.TrimSpace(value)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.askMode = value
}

func (c *Collector) SetAskStrategy(value string) {
	if c == nil {
		return
	}
	value = strings.TrimSpace(value)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.askStrategy = value
}

func (c *Collector) IncrementPlannerRepair() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.plannerRepairs++
}

func (c *Collector) SetPlannerRepairs(value int) {
	if c == nil {
		return
	}
	if value < 0 {
		value = 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.plannerRepairs = value
}

func (c *Collector) IncrementAdaptiveReplan() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.adaptiveReplans++
}

func (c *Collector) SetAdaptiveReplans(value int) {
	if c == nil {
		return
	}
	if value < 0 {
		value = 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.adaptiveReplans = value
}

func (c *Collector) MarkConsecutiveLLMViolation() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.consecutiveLLMViolation = true
}

func (c *Collector) SetLastStateTransition(value string) {
	if c == nil {
		return
	}
	value = strings.TrimSpace(value)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastStateTransition = value
}

func (c *Collector) SetEvidenceMetrics(raw int, used int, truncated bool, rows int) {
	if c == nil {
		return
	}
	if raw < 0 {
		raw = 0
	}
	if used < 0 {
		used = 0
	}
	if rows < 0 {
		rows = 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evidenceBytesRaw = raw
	c.evidenceBytesUsed = used
	c.evidenceTruncated = truncated
	c.evidenceRowsUsed = rows
}

func (c *Collector) SetPromptSizeMetrics(finalPromptTokens int, toolSchemaTokens int, contextTokens int) {
	if c == nil {
		return
	}
	if finalPromptTokens < 0 {
		finalPromptTokens = 0
	}
	if toolSchemaTokens < 0 {
		toolSchemaTokens = 0
	}
	if contextTokens < 0 {
		contextTokens = 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if finalPromptTokens > 0 {
		c.finalPromptTokens = finalPromptTokens
	}
	if toolSchemaTokens > 0 {
		c.toolSchemaTokens = toolSchemaTokens
	}
	if contextTokens > 0 {
		c.contextTokens = contextTokens
	}
}

func (c *Collector) Snapshot() Snapshot {
	if c == nil {
		return Snapshot{Stages: map[string]time.Duration{}}
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	stages := make(map[string]time.Duration, len(c.stages))
	for key, value := range c.stages {
		stages[key] = value
	}
	return Snapshot{
		Stages:                  stages,
		LLMCalls:                c.llmCalls,
		LLMTime:                 c.llmTime,
		LLMCallDurations:        append([]time.Duration(nil), c.llmCallDurations...),
		LLMPromptTokens:         append([]int(nil), c.llmPromptTokens...),
		LLMCompletionTokens:     append([]int(nil), c.llmCompletionTokens...),
		ToolCalls:               c.toolCalls,
		ToolExecTime:            c.toolExecTime,
		Grounded:                c.grounded,
		Fallback:                c.fallback,
		RepairCount:             c.repairCount,
		OutputKind:              c.outputKind,
		AskPath:                 c.askPath,
		AskMode:                 c.askMode,
		AskStrategy:             c.askStrategy,
		AskEscalationReason:     c.askEscalationReason,
		PlannerRepairs:          c.plannerRepairs,
		AdaptiveReplans:         c.adaptiveReplans,
		ConsecutiveLLMViolation: c.consecutiveLLMViolation,
		LastStateTransition:     c.lastStateTransition,
		EvidenceBytesRaw:        c.evidenceBytesRaw,
		EvidenceBytesUsed:       c.evidenceBytesUsed,
		EvidenceTruncated:       c.evidenceTruncated,
		EvidenceRowsUsed:        c.evidenceRowsUsed,
		FinalPromptTokens:       c.finalPromptTokens,
		ToolSchemaTokens:        c.toolSchemaTokens,
		ContextTokens:           c.contextTokens,
	}
}

type contextKey struct{}

var collectorKey contextKey

func WithCollector(ctx context.Context, collector *Collector) context.Context {
	if collector == nil {
		return ctx
	}
	return context.WithValue(ctx, collectorKey, collector)
}

func FromContext(ctx context.Context) *Collector {
	if ctx == nil {
		return nil
	}
	collector, _ := ctx.Value(collectorKey).(*Collector)
	return collector
}

func StartStage(ctx context.Context, name string) func() {
	start := time.Now()
	return func() {
		AddStageDuration(ctx, name, time.Since(start))
	}
}

func AddStageDuration(ctx context.Context, name string, duration time.Duration) {
	if collector := FromContext(ctx); collector != nil {
		collector.AddStage(name, duration)
	}
}

func RecordLLMCall(ctx context.Context, duration time.Duration) {
	if collector := FromContext(ctx); collector != nil {
		collector.RecordLLMCall(duration)
	}
}

func RecordLLMCallWithUsage(ctx context.Context, duration time.Duration, promptTokens int, completionTokens int) {
	if collector := FromContext(ctx); collector != nil {
		collector.RecordLLMCallWithUsage(duration, promptTokens, completionTokens)
	}
}

func RecordToolCall(ctx context.Context, duration time.Duration) {
	if collector := FromContext(ctx); collector != nil {
		collector.RecordToolCall(duration)
	}
}

func MarkGrounded(ctx context.Context) {
	if collector := FromContext(ctx); collector != nil {
		collector.MarkGrounded()
	}
}

func MarkFallback(ctx context.Context) {
	if collector := FromContext(ctx); collector != nil {
		collector.MarkFallback()
	}
}

func IncrementRepair(ctx context.Context) {
	if collector := FromContext(ctx); collector != nil {
		collector.IncrementRepair()
	}
}

func SetOutputKind(ctx context.Context, value string) {
	if collector := FromContext(ctx); collector != nil {
		collector.SetOutputKind(value)
	}
}

func SetAskPath(ctx context.Context, value string) {
	if collector := FromContext(ctx); collector != nil {
		collector.SetAskPath(value)
	}
}

func SetAskEscalationReason(ctx context.Context, value string) {
	if collector := FromContext(ctx); collector != nil {
		collector.SetAskEscalationReason(value)
	}
}

func SetAskEscalationReasonIfEmpty(ctx context.Context, value string) {
	if collector := FromContext(ctx); collector != nil {
		collector.SetAskEscalationReasonIfEmpty(value)
	}
}

func SetAskMode(ctx context.Context, value string) {
	if collector := FromContext(ctx); collector != nil {
		collector.SetAskMode(value)
	}
}

func SetAskStrategy(ctx context.Context, value string) {
	if collector := FromContext(ctx); collector != nil {
		collector.SetAskStrategy(value)
	}
}

func IncrementPlannerRepair(ctx context.Context) {
	if collector := FromContext(ctx); collector != nil {
		collector.IncrementPlannerRepair()
	}
}

func SetPlannerRepairs(ctx context.Context, value int) {
	if collector := FromContext(ctx); collector != nil {
		collector.SetPlannerRepairs(value)
	}
}

func IncrementAdaptiveReplan(ctx context.Context) {
	if collector := FromContext(ctx); collector != nil {
		collector.IncrementAdaptiveReplan()
	}
}

func SetAdaptiveReplans(ctx context.Context, value int) {
	if collector := FromContext(ctx); collector != nil {
		collector.SetAdaptiveReplans(value)
	}
}

func MarkConsecutiveLLMViolation(ctx context.Context) {
	if collector := FromContext(ctx); collector != nil {
		collector.MarkConsecutiveLLMViolation()
	}
}

func SetLastStateTransition(ctx context.Context, value string) {
	if collector := FromContext(ctx); collector != nil {
		collector.SetLastStateTransition(value)
	}
}

func SetEvidenceMetrics(ctx context.Context, raw int, used int, truncated bool, rows int) {
	if collector := FromContext(ctx); collector != nil {
		collector.SetEvidenceMetrics(raw, used, truncated, rows)
	}
}

func SetPromptSizeMetrics(ctx context.Context, finalPromptTokens int, toolSchemaTokens int, contextTokens int) {
	if collector := FromContext(ctx); collector != nil {
		collector.SetPromptSizeMetrics(finalPromptTokens, toolSchemaTokens, contextTokens)
	}
}

func max0(value int) int {
	if value < 0 {
		return 0
	}
	return value
}
