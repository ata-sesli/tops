package bench

import (
	"strings"
	"testing"
)

func TestDefaultAskDatasetIsCoreOnlyAndKeepsTenCases(t *testing.T) {
	cases, err := LoadDataset("../../../benchmarks/ask.json")
	if err != nil {
		t.Fatalf("load ask dataset: %v", err)
	}
	if len(cases) != 10 {
		t.Fatalf("expected ask dataset to keep 10 cases, got %d", len(cases))
	}
	for _, tc := range cases {
		prompt := strings.ToLower(tc.Prompt)
		if strings.Contains(prompt, "python") || strings.Contains(prompt, "git") {
			t.Fatalf("ask benchmark prompt must stay core-only, got %q", tc.Prompt)
		}
		if tc.ID == "ask_open_ports" {
			return
		}
	}
	t.Fatal("expected core capabilities dataset to retain ask_open_ports")
}
