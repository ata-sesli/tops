package workflow

import "testing"

func TestClassifyActionClass(t *testing.T) {
	if got := ClassifyActionClass([]string{"read-only"}); got != ActionClassReadOnly {
		t.Fatalf("expected read_only, got %s", got)
	}
	if got := ClassifyActionClass([]string{"safe-write"}); got != ActionClassWrite {
		t.Fatalf("expected write, got %s", got)
	}
}

func TestDefaultExecutionPolicy(t *testing.T) {
	p := DefaultExecutionPolicy()
	if p.ReadOnly != ActionPermissionAllow {
		t.Fatalf("expected read_only allow, got %s", p.ReadOnly)
	}
	if p.Write != ActionPermissionRequest {
		t.Fatalf("expected write request, got %s", p.Write)
	}
}
