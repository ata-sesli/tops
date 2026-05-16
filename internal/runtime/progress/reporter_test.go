package progress

import (
	"bytes"
	"testing"
	"time"
)

func TestCallbackReporterLifecycle(t *testing.T) {
	var calls int
	var sawDone bool
	reporter := NewCallback(func(phase string, elapsed time.Duration, done bool, err error) {
		calls++
		if elapsed < 0 {
			t.Fatalf("elapsed must be non-negative, got %s", elapsed)
		}
		if done {
			sawDone = true
		}
	})

	reporter.Start("planning")
	reporter.Update("provider")
	reporter.Finish(nil)

	if calls < 3 {
		t.Fatalf("expected at least 3 callback invocations, got %d", calls)
	}
	if !sawDone {
		t.Fatal("expected final done callback")
	}
}

func TestCLIReporterNonTTYNoPanic(t *testing.T) {
	var out bytes.Buffer
	reporter := NewCLI(&out)
	reporter.Start("planning")
	reporter.Update("provider")
	reporter.Finish(nil)
}

func TestCLIReporterStreamChunks(t *testing.T) {
	var out bytes.Buffer
	reporter := &CLIReporter{
		out:     &out,
		enabled: true,
	}
	reporter.Start("provider")
	reporter.ThinkingChunk("thinking...")
	reporter.ResponseChunk("answer")
	reporter.Finish(nil)

	got := out.String()
	if got == "" {
		t.Fatal("expected output from stream reporter")
	}
	if !bytes.Contains([]byte(got), []byte("thinking")) {
		t.Fatalf("expected thinking output, got %q", got)
	}
	if !bytes.Contains([]byte(got), []byte("answer")) {
		t.Fatalf("expected answer output, got %q", got)
	}
}

func TestCLIReporterReleaseModeSuppressesResponseChunks(t *testing.T) {
	var out bytes.Buffer
	reporter := &CLIReporter{
		out:     &out,
		enabled: true,
		mode:    TraceModeRelease,
	}
	reporter.Start("provider")
	reporter.ThinkingChunk("thinking...")
	reporter.ResponseChunk(`{"workflow_plan":{}}`)
	reporter.Finish(nil)

	got := out.String()
	if bytes.Contains([]byte(got), []byte("thinking")) {
		t.Fatalf("expected thinking output to be suppressed in release mode, got %q", got)
	}
	if bytes.Contains([]byte(got), []byte("workflow_plan")) {
		t.Fatalf("expected response chunk to be suppressed in release mode, got %q", got)
	}
	if !bytes.Contains([]byte(got), []byte("provider")) {
		t.Fatalf("expected release mode progress output, got %q", got)
	}
}

func TestCLIReporterWorkflowLinesInReleaseMode(t *testing.T) {
	var out bytes.Buffer
	reporter := &CLIReporter{
		out:     &out,
		enabled: true,
		mode:    TraceModeRelease,
	}
	reporter.Start("tools")
	reporter.ActionStarted("s1", "uname -srm", "read_only")
	reporter.PermissionDecision("s1", "uname -srm", "read_only", true, "policy")
	reporter.ActionCompleted("s1", "uname -srm", "read_only", 0, 5*time.Millisecond, "")
	reporter.Finish(nil)

	got := out.String()
	if !bytes.Contains([]byte(got), []byte("action[s1]")) {
		t.Fatalf("expected action line, got %q", got)
	}
	if !bytes.Contains([]byte(got), []byte("permission approved[s1]")) {
		t.Fatalf("expected permission line, got %q", got)
	}
}
