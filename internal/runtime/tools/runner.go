package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"tops/internal/obs"
	"tops/internal/runtime/commandcatalog"
)

type ToolSpec struct {
	Name    string
	Args    []string
	Timeout time.Duration
}

type ToolResult struct {
	Stdout          string
	Stderr          string
	ExitCode        int
	Duration        time.Duration
	CWD             string
	StdoutTruncated bool
	StderrTruncated bool
}

type ToolRunner interface {
	Run(ctx context.Context, spec ToolSpec) (ToolResult, error)
}

type Runner struct {
	catalog        commandcatalog.Catalog
	maxOutputBytes int
	logger         *obs.Logger
}

const defaultMaxOutputBytes = 256 * 1024

func NewRunner(logger *obs.Logger) *Runner {
	return &Runner{catalog: commandcatalog.Default(), maxOutputBytes: defaultMaxOutputBytes, logger: logger}
}

func (r *Runner) Run(ctx context.Context, spec ToolSpec) (ToolResult, error) {
	commandName := strings.TrimSpace(spec.Name)
	if commandName == "" {
		return ToolResult{}, errors.New("tool name is required")
	}
	cwd, err := os.Getwd()
	if err != nil || strings.TrimSpace(cwd) == "" {
		cwd = "."
	}
	normalizedArgs, entry, err := r.catalog.ValidateAndNormalize(commandName, spec.Args, cwd)
	if err != nil {
		return ToolResult{}, fmt.Errorf("tool validation failed for %q: %w", commandName, err)
	}
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = entry.DefaultTimeout
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stdoutBuf := &limitedBuffer{limit: r.maxOutputBytes}
	stderrBuf := &limitedBuffer{limit: r.maxOutputBytes}
	start := time.Now()
	cmd := exec.CommandContext(ctx, entry.Name, normalizedArgs...)
	cmd.Stdout = stdoutBuf
	cmd.Stderr = stderrBuf

	runErr := cmd.Run()
	duration := time.Since(start)

	result := ToolResult{
		Stdout:          strings.TrimSpace(stdoutBuf.String()),
		Stderr:          strings.TrimSpace(stderrBuf.String()),
		Duration:        duration,
		ExitCode:        0,
		CWD:             cwd,
		StdoutTruncated: stdoutBuf.truncated,
		StderrTruncated: stderrBuf.truncated,
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		} else if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			result.ExitCode = 124
			return result, fmt.Errorf("tool %q timed out after %s", entry.Name, timeout)
		} else {
			return result, fmt.Errorf("tool %q failed: %w", entry.Name, runErr)
		}
	}
	if r.logger != nil && r.logger.Enabled() {
		r.logger.Printf("tool=%s args=%q exit=%d os=%s duration=%s", entry.Name, strings.Join(normalizedArgs, " "), result.ExitCode, runtime.GOOS, duration)
	}
	return result, nil
}

type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if l.limit <= 0 {
		return l.buf.Write(p)
	}
	if l.buf.Len() >= l.limit {
		l.truncated = true
		return len(p), nil
	}
	remaining := l.limit - l.buf.Len()
	if len(p) > remaining {
		_, _ = l.buf.Write(p[:remaining])
		l.truncated = true
		return len(p), nil
	}
	return l.buf.Write(p)
}

func (l *limitedBuffer) String() string {
	return l.buf.String()
}
