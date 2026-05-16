package tools

import (
	"context"
	"testing"
	"time"
)

func TestRunnerAllowlistedCommand(t *testing.T) {
	r := NewRunner(nil)
	res, err := r.Run(context.Background(), ToolSpec{Name: "pwd", Timeout: time.Second})
	if err != nil {
		t.Fatalf("expected pwd to run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", res.ExitCode)
	}
}

func TestRunnerRejectsNonAllowlistedCommand(t *testing.T) {
	r := NewRunner(nil)
	_, err := r.Run(context.Background(), ToolSpec{Name: "rm", Args: []string{"-rf", "/"}})
	if err == nil {
		t.Fatal("expected allowlist error")
	}
}

func TestRunnerRejectsUnsafeArgument(t *testing.T) {
	r := NewRunner(nil)
	_, err := r.Run(context.Background(), ToolSpec{Name: "ls", Args: []string{"bad\narg"}})
	if err == nil {
		t.Fatal("expected argument sanitization error")
	}
}

func TestRunnerAllowlistsUname(t *testing.T) {
	r := NewRunner(nil)
	res, err := r.Run(context.Background(), ToolSpec{Name: "uname", Args: []string{"-srm"}, Timeout: time.Second})
	if err != nil {
		t.Fatalf("expected uname to run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", res.ExitCode)
	}
}
