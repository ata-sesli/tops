package render

import (
	"strings"
	"testing"

	"tops/internal/model"
)

func TestRenderGenText(t *testing.T) {
	renderer := New()
	out, err := renderer.RenderGen(model.GenResult{
		Command:     "find . -name '*.log'",
		Explanation: "Lists log files",
		Assumptions: []string{"cwd is project root"},
		RiskLabels:  []string{"read-only"},
	}, "text")
	if err != nil {
		t.Fatalf("render error: %v", err)
	}
	if !strings.Contains(out, "find . -name '*.log'") {
		t.Fatalf("expected command artifact in output, got %s", out)
	}
	if !strings.Contains(out, "Assumes: cwd is project root") {
		t.Fatalf("expected compact assumption line, got %s", out)
	}
}

func TestRenderHelpJSON(t *testing.T) {
	renderer := New()
	out, err := renderer.RenderHelp(model.HelpResult{Target: "grep", Summary: "search text"}, "json")
	if err != nil {
		t.Fatalf("render error: %v", err)
	}
	if !strings.Contains(out, "\"target\": \"grep\"") {
		t.Fatalf("unexpected json output: %s", out)
	}
}
