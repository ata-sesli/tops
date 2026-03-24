package modelprofile

import (
	"path/filepath"
	"testing"

	"tops/internal/config"
)

func TestLoadMissingFileReturnsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-models.json")
	profiles, err := Load(path)
	if err != nil {
		t.Fatalf("load missing file failed: %v", err)
	}
	if profiles.Version != schemaVersion {
		t.Fatalf("expected schema version %d, got %d", schemaVersion, profiles.Version)
	}
	if len(profiles.Entries) != 0 {
		t.Fatalf("expected zero entries, got %d", len(profiles.Entries))
	}
}

func TestUpsertGetDeleteRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	profiles := Empty()
	err := profiles.Upsert(ModelProfile{
		Provider:     config.ProviderOllama,
		Model:        "llama3.1",
		Context:      8192,
		MaxLength:    1200,
		SystemPrompt: "be concise",
		Think:        "off",
	})
	if err != nil {
		t.Fatalf("upsert failed: %v", err)
	}
	if err := SaveAtomic(path, profiles); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	got, ok := loaded.Get(config.ProviderOllama, "llama3.1")
	if !ok {
		t.Fatal("expected profile to exist")
	}
	if got.Context != 8192 || got.MaxLength != 1200 || got.SystemPrompt != "be concise" {
		t.Fatalf("unexpected profile values: %+v", got)
	}
	if got.Think != "off" {
		t.Fatalf("expected think=off, got %q", got.Think)
	}

	if !loaded.Delete(config.ProviderOllama, "llama3.1") {
		t.Fatal("expected delete to return true")
	}
	if _, ok := loaded.Get(config.ProviderOllama, "llama3.1"); ok {
		t.Fatal("expected profile to be deleted")
	}
}

func TestUpsertRejectsInvalidValues(t *testing.T) {
	profiles := Empty()
	err := profiles.Upsert(ModelProfile{
		Provider:  config.ProviderOllama,
		Model:     "llama3.1",
		Context:   -1,
		MaxLength: 0,
	})
	if err == nil {
		t.Fatal("expected validation error for negative context")
	}
}

func TestUpsertRejectsInvalidThink(t *testing.T) {
	profiles := Empty()
	err := profiles.Upsert(ModelProfile{
		Provider: config.ProviderOllama,
		Model:    "llama3.1",
		Think:    "extreme",
	})
	if err == nil {
		t.Fatal("expected validation error for invalid think value")
	}
}
