package functions

import (
	"errors"
	"testing"
)

func TestRegistryHasTenEssentialFunctions(t *testing.T) {
	reg := NewDefaultRegistry()
	list := reg.List()
	if len(list) != 10 {
		t.Fatalf("expected 10 functions, got %d", len(list))
	}
}

func TestListDirectoryDefaults(t *testing.T) {
	reg := NewDefaultRegistry()
	def, ok := reg.Get("list_directory")
	if !ok {
		t.Fatal("list_directory not found")
	}
	cmd, args, _, _, err := def.Resolve(map[string]any{})
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if cmd != "ls" {
		t.Fatalf("expected ls command, got %s", cmd)
	}
	if len(args) != 1 || args[0] != "." {
		t.Fatalf("unexpected args: %#v", args)
	}
}

func TestListProcessesTopNAndSort(t *testing.T) {
	reg := NewDefaultRegistry()
	def, ok := reg.Get("list_processes")
	if !ok {
		t.Fatal("list_processes not found")
	}
	cmd, args, _, limit, err := def.Resolve(map[string]any{
		"sort_by": "cpu",
		"top_n":   30,
	})
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if cmd != "ps" {
		t.Fatalf("expected ps command, got %s", cmd)
	}
	if len(args) != 3 || args[2] != "--sort=-%cpu" {
		t.Fatalf("unexpected args: %#v", args)
	}
	if limit != 31 {
		t.Fatalf("expected output limit 31, got %d", limit)
	}
}

func TestLinuxListeningPortsFallbackToNetstat(t *testing.T) {
	reg := NewRegistry(Options{
		GOOS: "linux",
		LookPath: func(file string) (string, error) {
			return "", errors.New("not found")
		},
	})
	def, ok := reg.Get("list_listening_ports")
	if !ok {
		t.Fatal("list_listening_ports not found")
	}
	cmd, args, _, _, err := def.Resolve(nil)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if cmd != "netstat" {
		t.Fatalf("expected netstat fallback, got %s", cmd)
	}
	if len(args) != 1 || args[0] != "-lnt" {
		t.Fatalf("unexpected args: %#v", args)
	}
}

func TestRejectUnknownArgument(t *testing.T) {
	reg := NewDefaultRegistry()
	def, ok := reg.Get("get_os_info")
	if !ok {
		t.Fatal("get_os_info not found")
	}
	_, _, _, _, err := def.Resolve(map[string]any{"extra": true})
	if err == nil {
		t.Fatal("expected unknown argument error")
	}
}
