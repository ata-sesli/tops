package tui

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestPTYShellControllerEnterExecutesCommand(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	shell := NewPTYShellController()
	if err := shell.Start(ctx, "/bin/sh", 80, 24); err != nil {
		t.Fatalf("start shell: %v", err)
	}
	defer shell.Close()

	if err := shell.Write([]byte("echo TOPS_PTY_OK\r")); err != nil {
		t.Fatalf("write shell command: %v", err)
	}

	var output strings.Builder
	for {
		select {
		case event := <-shell.Events():
			if event.Err != nil {
				t.Fatalf("shell event error: %v", event.Err)
			}
			output.WriteString(event.Data)
			if strings.Contains(output.String(), "TOPS_PTY_OK") {
				return
			}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for shell command output; saw %q", output.String())
		}
	}
}
