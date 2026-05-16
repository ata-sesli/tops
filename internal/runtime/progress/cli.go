package progress

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

var spinnerFrames = []string{"|", "/", "-", `\`}

type TraceMode string

const (
	TraceModeDebug   TraceMode = "debug"
	TraceModeRelease TraceMode = "release"
)

type CLIReporter struct {
	out     io.Writer
	enabled bool
	mode    TraceMode

	mu        sync.Mutex
	started   bool
	startedAt time.Time
	phase     string
	frameIdx  int
	done      chan struct{}
	streaming bool
	streamTag string
}

func NewCLI(out io.Writer) *CLIReporter {
	return NewCLIWithMode(out, "")
}

func NewCLIWithMode(out io.Writer, mode string) *CLIReporter {
	return &CLIReporter{
		out:     out,
		enabled: writerIsTTY(out),
		mode:    normalizeTraceMode(mode),
	}
}

func (r *CLIReporter) Start(phase string) {
	r.mu.Lock()
	if !r.enabled || r.out == nil {
		r.mu.Unlock()
		return
	}
	if r.started {
		r.phase = normalizedPhase(phase)
		r.mu.Unlock()
		return
	}
	r.started = true
	r.startedAt = time.Now()
	r.phase = normalizedPhase(phase)
	r.frameIdx = 0
	r.done = make(chan struct{})
	done := r.done
	r.mu.Unlock()

	go r.loop(done)
}

func (r *CLIReporter) Update(phase string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.enabled || r.out == nil {
		return
	}
	if !r.started {
		r.started = true
		r.startedAt = time.Now()
		r.done = make(chan struct{})
		go r.loop(r.done)
	}
	r.phase = normalizedPhase(phase)
}

func (r *CLIReporter) Finish(err error) {
	r.mu.Lock()
	if !r.enabled || r.out == nil || !r.started {
		r.mu.Unlock()
		return
	}
	done := r.done
	r.started = false
	startedAt := r.startedAt
	phase := r.phase
	hadStream := r.streaming || r.streamTag != ""
	r.streaming = false
	r.streamTag = ""
	r.mu.Unlock()

	close(done)
	if hadStream {
		_, _ = fmt.Fprintln(r.out)
	}
	elapsed := time.Since(startedAt)
	status := "done"
	if err != nil {
		status = "failed"
	}
	_, _ = fmt.Fprintf(r.out, "\r[%s] %s in %s%s", trimPhaseForDisplay(phase), status, formatElapsed(elapsed), clearToEOL())
	_, _ = fmt.Fprintln(r.out)
}

func (r *CLIReporter) loop(done <-chan struct{}) {
	ticker := time.NewTicker(120 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			r.mu.Lock()
			if !r.started || r.out == nil {
				r.mu.Unlock()
				return
			}
			if r.streaming {
				r.mu.Unlock()
				continue
			}
			elapsed := time.Since(r.startedAt)
			phase := trimPhaseForDisplay(r.phase)
			frame := spinnerFrames[r.frameIdx%len(spinnerFrames)]
			r.frameIdx++
			_, _ = fmt.Fprintf(r.out, "\r%s %s (%s)%s", frame, phase, formatElapsed(elapsed), clearToEOL())
			r.mu.Unlock()
		}
	}
}

func (r *CLIReporter) ThinkingChunk(chunk string) {
	if r.mode == TraceModeRelease {
		return
	}
	r.writeStreamChunk("thinking", chunk)
}

func (r *CLIReporter) ResponseChunk(chunk string) {
	if r.mode == TraceModeRelease {
		return
	}
	r.writeStreamChunk("answering", chunk)
}

func (r *CLIReporter) writeStreamChunk(tag string, chunk string) {
	if chunk == "" {
		return
	}
	cleaned := strings.ReplaceAll(chunk, "\r", "")
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.enabled || r.out == nil || !r.started {
		return
	}
	if r.streamTag != tag {
		if r.streamTag != "" {
			_, _ = fmt.Fprintln(r.out)
		}
		phase := trimPhaseForDisplay(r.phase)
		_, _ = fmt.Fprintf(r.out, "\r[%s] %s: ", phase, tag)
		r.streamTag = tag
	}
	r.streaming = true
	_, _ = fmt.Fprint(r.out, cleaned)
}

func (r *CLIReporter) ActionStarted(stepID string, commandLine string, actionClass string) {
	r.writeWorkflowLine(fmt.Sprintf("action[%s] (%s): %s", strings.TrimSpace(stepID), strings.TrimSpace(actionClass), strings.TrimSpace(commandLine)))
}

func (r *CLIReporter) PermissionRequested(stepID string, commandLine string, actionClass string) {
	r.writeWorkflowLine(fmt.Sprintf("permission requested[%s] (%s): %s", strings.TrimSpace(stepID), strings.TrimSpace(actionClass), strings.TrimSpace(commandLine)))
}

func (r *CLIReporter) PermissionDecision(stepID string, commandLine string, actionClass string, approved bool, source string) {
	decision := "denied"
	if approved {
		decision = "approved"
	}
	r.writeWorkflowLine(fmt.Sprintf("permission %s[%s] (%s, source=%s): %s", decision, strings.TrimSpace(stepID), strings.TrimSpace(actionClass), strings.TrimSpace(source), strings.TrimSpace(commandLine)))
}

func (r *CLIReporter) ActionCompleted(stepID string, commandLine string, actionClass string, exitCode int, duration time.Duration, errText string) {
	dur := formatElapsed(duration)
	if strings.TrimSpace(errText) != "" {
		r.writeWorkflowLine(fmt.Sprintf("action completed[%s] (%s) exit=%d in %s error=%s", strings.TrimSpace(stepID), strings.TrimSpace(actionClass), exitCode, dur, strings.TrimSpace(errText)))
		return
	}
	r.writeWorkflowLine(fmt.Sprintf("action completed[%s] (%s) exit=%d in %s", strings.TrimSpace(stepID), strings.TrimSpace(actionClass), exitCode, dur))
}

func (r *CLIReporter) StatusLine(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	r.writeWorkflowLine("status: " + line)
}

func (r *CLIReporter) writeWorkflowLine(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.enabled || r.out == nil || !r.started {
		return
	}
	if r.streamTag != "" {
		_, _ = fmt.Fprintln(r.out)
		r.streamTag = ""
	}
	r.streaming = false
	prefix := "[workflow] "
	if r.mode == TraceModeRelease {
		prefix = ""
	}
	_, _ = fmt.Fprintf(r.out, "\r%s%s%s", prefix, line, clearToEOL())
	_, _ = fmt.Fprintln(r.out)
}

func normalizeTraceMode(mode string) TraceMode {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case string(TraceModeRelease):
		return TraceModeRelease
	default:
		return TraceModeDebug
	}
}

func normalizedPhase(phase string) string {
	phase = strings.TrimSpace(strings.ToLower(phase))
	if phase == "" {
		return "working"
	}
	return phase
}

func trimPhaseForDisplay(phase string) string {
	phase = normalizedPhase(phase)
	switch phase {
	case "planning":
		return "planning"
	case "provider":
		return "provider"
	case "tools":
		return "tools"
	case "rendering":
		return "rendering"
	default:
		return phase
	}
}

func formatElapsed(d time.Duration) string {
	total := int(d.Round(time.Second).Seconds())
	if total < 0 {
		total = 0
	}
	min := total / 60
	sec := total % 60
	return fmt.Sprintf("%02d:%02d", min, sec)
}

func clearToEOL() string {
	return "\x1b[K"
}
