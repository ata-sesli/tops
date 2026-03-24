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
	for _, section := range []string{"Result", "Explanation", "Assumptions", "Risks"} {
		if !strings.Contains(out, section) {
			t.Fatalf("missing section %s in %s", section, out)
		}
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
