package help

import (
	"testing"

	"tops/internal/model"
)

func patternsOf(invocations []Invocation) []string {
	out := make([]string, 0, len(invocations))
	for _, inv := range invocations {
		out = append(out, inv.Pattern)
	}
	return out
}

func TestResolverOrderForModernCLI(t *testing.T) {
	target := Target{RootCommand: "kubectl", Subcommands: []string{}}
	invocations := NewResolver().Resolve(target, model.PlatformContext{OSFamily: "macos"})
	patterns := patternsOf(invocations)
	expectedPrefix := []string{"--help", "-h", "help", "man"}
	if len(patterns) < len(expectedPrefix) {
		t.Fatalf("expected at least %d patterns, got %d", len(expectedPrefix), len(patterns))
	}
	for i, expected := range expectedPrefix {
		if patterns[i] != expected {
			t.Fatalf("unexpected pattern order at %d: got=%q want=%q", i, patterns[i], expected)
		}
	}
}

func TestResolverOrderForBSDTools(t *testing.T) {
	target := Target{RootCommand: "du", Subcommands: []string{}}
	invocations := NewResolver().Resolve(target, model.PlatformContext{OSFamily: "macos"})
	patterns := patternsOf(invocations)
	expectedPrefix := []string{"man", "-h", "--help", "help"}
	if len(patterns) < len(expectedPrefix) {
		t.Fatalf("expected at least %d patterns, got %d", len(expectedPrefix), len(patterns))
	}
	for i, expected := range expectedPrefix {
		if patterns[i] != expected {
			t.Fatalf("unexpected pattern order at %d: got=%q want=%q", i, patterns[i], expected)
		}
	}
}

func TestResolverBuildsSubcommandPatterns(t *testing.T) {
	target := Target{RootCommand: "docker", Subcommands: []string{"compose"}}
	invocations := NewResolver().Resolve(target, model.PlatformContext{OSFamily: "macos"})

	hasHelpSubcommandPattern := false
	hasDoubleDashHelpPattern := false
	for _, inv := range invocations {
		if inv.Pattern == "help" && len(inv.Args) == 2 && inv.Args[0] == "help" && inv.Args[1] == "compose" {
			hasHelpSubcommandPattern = true
		}
		if inv.Pattern == "--help" && len(inv.Args) == 2 && inv.Args[0] == "compose" && inv.Args[1] == "--help" {
			hasDoubleDashHelpPattern = true
		}
	}
	if !hasHelpSubcommandPattern {
		t.Fatalf("expected docker compose to include `docker help compose` pattern")
	}
	if !hasDoubleDashHelpPattern {
		t.Fatalf("expected docker compose to include `docker compose --help` pattern")
	}
}
