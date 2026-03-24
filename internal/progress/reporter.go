package progress

import (
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

type Reporter interface {
	Start(phase string)
	Update(phase string)
	Finish(err error)
}

type StreamEmitter interface {
	ThinkingChunk(chunk string)
	ResponseChunk(chunk string)
}

type WorkflowEmitter interface {
	ActionStarted(stepID string, commandLine string, actionClass string)
	PermissionRequested(stepID string, commandLine string, actionClass string)
	PermissionDecision(stepID string, commandLine string, actionClass string, approved bool, source string)
	ActionCompleted(stepID string, commandLine string, actionClass string, exitCode int, errText string)
}

type noopReporter struct{}

func (noopReporter) Start(string)  {}
func (noopReporter) Update(string) {}
func (noopReporter) Finish(error)  {}
func NewNoop() Reporter            { return noopReporter{} }
func writerIsTTY(out io.Writer) bool {
	f, ok := out.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

type contextKey struct{}

var reporterKey contextKey

func WithReporter(ctx context.Context, reporter Reporter) context.Context {
	if reporter == nil {
		reporter = noopReporter{}
	}
	return context.WithValue(ctx, reporterKey, reporter)
}

func FromContext(ctx context.Context) Reporter {
	if ctx == nil {
		return noopReporter{}
	}
	if reporter, ok := ctx.Value(reporterKey).(Reporter); ok && reporter != nil {
		return reporter
	}
	return noopReporter{}
}

func UpdatePhase(ctx context.Context, phase string) {
	FromContext(ctx).Update(phase)
}

func EmitThinkingChunk(ctx context.Context, chunk string) {
	if emitter, ok := FromContext(ctx).(StreamEmitter); ok && emitter != nil {
		emitter.ThinkingChunk(chunk)
	}
}

func EmitResponseChunk(ctx context.Context, chunk string) {
	if emitter, ok := FromContext(ctx).(StreamEmitter); ok && emitter != nil {
		emitter.ResponseChunk(chunk)
	}
}

func EmitActionStarted(ctx context.Context, stepID string, commandLine string, actionClass string) {
	if emitter, ok := FromContext(ctx).(WorkflowEmitter); ok && emitter != nil {
		emitter.ActionStarted(stepID, commandLine, actionClass)
	}
}

func EmitPermissionRequested(ctx context.Context, stepID string, commandLine string, actionClass string) {
	if emitter, ok := FromContext(ctx).(WorkflowEmitter); ok && emitter != nil {
		emitter.PermissionRequested(stepID, commandLine, actionClass)
	}
}

func EmitPermissionDecision(ctx context.Context, stepID string, commandLine string, actionClass string, approved bool, source string) {
	if emitter, ok := FromContext(ctx).(WorkflowEmitter); ok && emitter != nil {
		emitter.PermissionDecision(stepID, commandLine, actionClass, approved, source)
	}
}

func EmitActionCompleted(ctx context.Context, stepID string, commandLine string, actionClass string, exitCode int, errText string) {
	if emitter, ok := FromContext(ctx).(WorkflowEmitter); ok && emitter != nil {
		emitter.ActionCompleted(stepID, commandLine, actionClass, exitCode, errText)
	}
}

type CallbackReporter struct {
	mu        sync.Mutex
	started   bool
	startedAt time.Time
	phase     string
	cb        func(phase string, elapsed time.Duration, done bool, err error)
}

func NewCallback(cb func(phase string, elapsed time.Duration, done bool, err error)) *CallbackReporter {
	return &CallbackReporter{cb: cb}
}

func (r *CallbackReporter) Start(phase string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.started {
		r.started = true
		r.startedAt = time.Now()
	}
	r.phase = strings.TrimSpace(phase)
	if r.cb != nil {
		r.cb(r.phase, time.Since(r.startedAt), false, nil)
	}
}

func (r *CallbackReporter) Update(phase string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.started {
		r.started = true
		r.startedAt = time.Now()
	}
	r.phase = strings.TrimSpace(phase)
	if r.cb != nil {
		r.cb(r.phase, time.Since(r.startedAt), false, nil)
	}
}

func (r *CallbackReporter) Finish(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.started {
		return
	}
	if r.cb != nil {
		r.cb(r.phase, time.Since(r.startedAt), true, err)
	}
	r.started = false
}
