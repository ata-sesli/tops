package bench

import "testing"

func TestAskBenchmarkFailureReason(t *testing.T) {
	tests := []struct {
		name     string
		record   RunRecord
		prompt   string
		wantFail bool
	}{
		{
			name: "fail transition always fails",
			record: RunRecord{
				AskStrategy:         "reactive_tools",
				LastStateTransition: "fail",
				ToolCalls:           1,
			},
			prompt:   "what is my current directory?",
			wantFail: true,
		},
		{
			name: "consecutive llm violation fails",
			record: RunRecord{
				AskStrategy:             "reactive_tools",
				ConsecutiveLLMViolation: true,
				ToolCalls:               1,
			},
			prompt:   "what kernel version am I on?",
			wantFail: true,
		},
		{
			name: "reactive local zero tools fails",
			record: RunRecord{
				AskStrategy: "reactive_tools",
				ToolCalls:   0,
			},
			prompt:   "what kernel version am I on?",
			wantFail: true,
		},
		{
			name: "planned local zero tools fails",
			record: RunRecord{
				AskStrategy: "planned_tools",
				ToolCalls:   0,
			},
			prompt:   "what is my disk usage for this directory?",
			wantFail: true,
		},
		{
			name: "conceptual zero tools passes",
			record: RunRecord{
				AskStrategy: "conceptual",
				ToolCalls:   0,
			},
			prompt:   "explain git commit",
			wantFail: false,
		},
		{
			name: "reactive with tools passes",
			record: RunRecord{
				AskStrategy: "reactive_tools",
				ToolCalls:   1,
			},
			prompt:   "what is my hostname?",
			wantFail: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reason := askBenchmarkFailureReason(tc.record, tc.prompt)
			if tc.wantFail && reason == "" {
				t.Fatal("expected failure reason, got empty")
			}
			if !tc.wantFail && reason != "" {
				t.Fatalf("expected no failure reason, got %q", reason)
			}
		})
	}
}
