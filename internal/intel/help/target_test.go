package help

import "testing"

func TestParseTargetAcceptsCommandTargets(t *testing.T) {
	cases := []struct {
		input string
		root  string
		subs  int
	}{
		{input: "du", root: "du", subs: 0},
		{input: "python3", root: "python3", subs: 0},
		{input: "uv", root: "uv", subs: 0},
		{input: "kubectl", root: "kubectl", subs: 0},
		{input: "docker compose", root: "docker", subs: 1},
		{input: "git status", root: "git", subs: 1},
	}
	for _, tc := range cases {
		target, err := ParseTarget(tc.input)
		if err != nil {
			t.Fatalf("expected %q to parse: %v", tc.input, err)
		}
		if target.RootCommand != tc.root {
			t.Fatalf("unexpected root for %q: got=%q want=%q", tc.input, target.RootCommand, tc.root)
		}
		if len(target.Subcommands) != tc.subs {
			t.Fatalf("unexpected subcommand count for %q: got=%d want=%d", tc.input, len(target.Subcommands), tc.subs)
		}
	}
}

func TestParseTargetRejectsUnsafeTargets(t *testing.T) {
	cases := []string{
		"--help",
		"du | grep size",
		"$(whoami)",
	}
	for _, input := range cases {
		if _, err := ParseTarget(input); err == nil {
			t.Fatalf("expected %q to be rejected", input)
		}
	}
}
