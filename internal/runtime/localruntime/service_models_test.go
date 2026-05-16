package localruntime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tops/internal/config"
)

func TestDiscoverModelsAcrossDirsMultiPathWarningsAndSort(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	dirA := filepath.Join(root, "a")
	dirB := filepath.Join(root, "b")
	missing := filepath.Join(root, "missing")
	if err := os.MkdirAll(filepath.Join(dirA, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir dirA: %v", err)
	}
	if err := os.MkdirAll(dirB, 0o755); err != nil {
		t.Fatalf("mkdir dirB: %v", err)
	}
	writeModel := func(path string) {
		t.Helper()
		if err := os.WriteFile(path, []byte("gguf"), 0o644); err != nil {
			t.Fatalf("write model %s: %v", path, err)
		}
	}
	writeModel(filepath.Join(dirA, "nested", "zeta.gguf"))
	writeModel(filepath.Join(dirA, "alpha.gguf"))
	writeModel(filepath.Join(dirB, "beta.gguf"))

	result, err := DiscoverModelsAcrossDirs([]string{dirA, missing, dirB, dirA})
	if err != nil {
		t.Fatalf("discover models: %v", err)
	}
	if len(result.Entries) != 3 {
		t.Fatalf("expected 3 models, got %d (%+v)", len(result.Entries), result.Entries)
	}
	if !strings.EqualFold(result.Entries[0].Name, "alpha") || !strings.EqualFold(result.Entries[1].Name, "beta") || !strings.EqualFold(result.Entries[2].Name, "zeta") {
		t.Fatalf("expected sorted model names alpha,beta,zeta; got %+v", result.Entries)
	}
	if len(result.Warnings) == 0 {
		t.Fatal("expected warning for invalid scan path")
	}
}

func TestListModelsDetailedCreatesDefaultModelsDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := config.Config{
		Provider: config.ProviderConfig{
			Type:  config.ProviderYZMA,
			Model: "gemma4:e4b",
		},
		Shell:      "zsh",
		Output:     config.OutputConfig{Format: "text"},
		Inspection: config.InspectionConfig{TimeoutSeconds: 10},
	}
	if err := cfg.ApplyDefaults(); err != nil {
		t.Fatalf("apply defaults: %v", err)
	}
	svc := NewService(nil, nil)
	result, err := svc.ListModelsDetailed(cfg)
	if err != nil {
		t.Fatalf("list models detailed: %v", err)
	}
	defaultDir := filepath.Join(home, ".tops", "models")
	if _, statErr := os.Stat(defaultDir); statErr != nil {
		t.Fatalf("expected default models dir to be created: %v", statErr)
	}
	if len(result.ModelsDirs) == 0 || !strings.EqualFold(result.ModelsDirs[0], defaultDir) {
		t.Fatalf("expected default models dir as first scan path, got %+v", result.ModelsDirs)
	}
}
