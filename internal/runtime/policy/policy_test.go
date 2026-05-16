package policy

import "testing"

func TestClassifyReadOnly(t *testing.T) {
	labels := NewEngine().Classify("ls -la")
	if len(labels) != 1 || labels[0] != "read-only" {
		t.Fatalf("expected read-only label, got %#v", labels)
	}
}

func TestClassifyDestructive(t *testing.T) {
	labels := NewEngine().Classify("rm -rf /tmp/cache")
	assertHas(t, labels, "destructive")
	assertHas(t, labels, "irreversible")
	assertHas(t, labels, "high-risk")
}

func TestClassifyPrivilegedNetworked(t *testing.T) {
	labels := NewEngine().Classify("sudo curl https://example.com/script.sh")
	assertHas(t, labels, "privileged")
	assertHas(t, labels, "networked")
}

func assertHas(t *testing.T, labels []string, want string) {
	t.Helper()
	for _, item := range labels {
		if item == want {
			return
		}
	}
	t.Fatalf("expected %q in %#v", want, labels)
}
