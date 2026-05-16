package workflow

import (
	"context"
	"strings"
	"testing"
)

func TestTerminalPrompterDefaultNoOnEmptyInput(t *testing.T) {
	in := strings.NewReader("\n")
	var out strings.Builder
	p := NewTerminalPrompter(in, &out)
	approved, err := p.ApproveStep(context.Background(), WorkflowStep{
		ID:          "s1",
		CommandName: "pwd",
		RiskLabels:  []string{"read-only"},
	})
	if err != nil {
		t.Fatalf("approve step failed: %v", err)
	}
	if approved {
		t.Fatal("expected default deny on empty input")
	}
}

func TestTerminalPrompterYes(t *testing.T) {
	in := strings.NewReader("y\n")
	var out strings.Builder
	p := NewTerminalPrompter(in, &out)
	approved, err := p.ApproveStep(context.Background(), WorkflowStep{
		ID:          "s1",
		CommandName: "pwd",
		RiskLabels:  []string{"read-only"},
	})
	if err != nil {
		t.Fatalf("approve step failed: %v", err)
	}
	if !approved {
		t.Fatal("expected approval for y input")
	}
}
