package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	"tops/internal/obs"
)

type ToolSpec struct {
	Name    string
	Args    []string
	Timeout time.Duration
}

type ToolResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Duration time.Duration
}

type ToolRunner interface {
	Run(ctx context.Context, spec ToolSpec) (ToolResult, error)
}

type Runner struct {
	allowlist map[string]struct{}
	logger    *obs.Logger
}

var argSafetyPattern = regexp.MustCompile(`^[a-zA-Z0-9._/:=+@%,-]+$`)

func NewRunner(logger *obs.Logger) *Runner {
	allowed := []string{
		"pwd", "ls", "stat", "file", "readlink", "ps", "lsof", "ss", "netstat", "du", "df", "find", "man", "head", "col", "bash", "zsh", "sh", "uname",
	}
	allow := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		allow[name] = struct{}{}
	}
	return &Runner{allowlist: allow, logger: logger}
}

func (r *Runner) Run(ctx context.Context, spec ToolSpec) (ToolResult, error) {
	if strings.TrimSpace(spec.Name) == "" {
		return ToolResult{}, errors.New("tool name is required")
	}
	if _, ok := r.allowlist[spec.Name]; !ok {
		return ToolResult{}, fmt.Errorf("tool %q is not allowlisted", spec.Name)
	}
	for _, arg := range spec.Args {
		if strings.ContainsAny(arg, "\n\r") {
			return ToolResult{}, fmt.Errorf("invalid newline in argument %q", arg)
		}
		if arg == "" {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		if !argSafetyPattern.MatchString(arg) {
			return ToolResult{}, fmt.Errorf("argument %q contains unsupported characters", arg)
		}
	}
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	cmd := exec.CommandContext(ctx, spec.Name, spec.Args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	duration := time.Since(start)

	result := ToolResult{
		Stdout:   strings.TrimSpace(stdout.String()),
		Stderr:   strings.TrimSpace(stderr.String()),
		Duration: duration,
		ExitCode: 0,
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		} else if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			result.ExitCode = 124
			return result, fmt.Errorf("tool %q timed out after %s", spec.Name, timeout)
		} else {
			return result, fmt.Errorf("tool %q failed: %w", spec.Name, err)
		}
	}
	if r.logger != nil && r.logger.Enabled() {
		r.logger.Printf("tool=%s args=%q exit=%d os=%s duration=%s", spec.Name, strings.Join(spec.Args, " "), result.ExitCode, runtime.GOOS, duration)
	}
	return result, nil
}
