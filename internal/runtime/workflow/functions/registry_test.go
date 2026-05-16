package functions

import (
	"strings"
	"testing"
)

func TestRegistryContainsReadonlyRunner(t *testing.T) {
	reg := NewDefaultRegistry()
	list := reg.List()
	if len(list) != 1 {
		t.Fatalf("expected exactly one function, got %d", len(list))
	}
	if list[0].Name != "run_readonly_command" {
		t.Fatalf("unexpected function name: %q", list[0].Name)
	}
}

func TestRunReadonlyCommandResolvesPwd(t *testing.T) {
	reg := NewDefaultRegistry()
	def, ok := reg.Get("run_readonly_command")
	if !ok {
		t.Fatal("run_readonly_command not found")
	}
	cmd, args, expected, limit, err := def.Resolve(map[string]any{
		"command_name": "pwd",
		"args":         []any{},
	})
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if cmd != "pwd" {
		t.Fatalf("expected pwd command, got %q", cmd)
	}
	if len(args) != 0 {
		t.Fatalf("expected no args, got %#v", args)
	}
	if !strings.Contains(strings.ToLower(expected), "validated output") {
		t.Fatalf("unexpected expected evidence text: %q", expected)
	}
	if limit <= 0 {
		t.Fatalf("expected positive output line limit, got %d", limit)
	}
}

func TestRunReadonlyCommandRejectsUnknownArgument(t *testing.T) {
	reg := NewDefaultRegistry()
	def, ok := reg.Get("run_readonly_command")
	if !ok {
		t.Fatal("run_readonly_command not found")
	}
	_, _, _, _, err := def.Resolve(map[string]any{
		"command_name": "pwd",
		"extra":        true,
	})
	if err == nil {
		t.Fatal("expected unknown argument error")
	}
	if !strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("unexpected error: %v", err)
	}
}
