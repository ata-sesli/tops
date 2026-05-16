package modelprofile

import (
	"path/filepath"
	"strings"
	"testing"

	"tops/internal/config"
	"tops/internal/model"
)

func boolRef(v bool) *bool {
	return &v
}

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
		AskResponse: AskResponseConfig{
			Observations: boolRef(false),
			Notes:        boolRef(false),
		},
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
	effective := got.EffectiveAskResponseProfile()
	if effective.Observations {
		t.Fatal("expected observations disabled")
	}
	if effective.Notes {
		t.Fatal("expected notes disabled")
	}
	if !effective.Inferences || !effective.Uncertainties || !effective.Assumptions {
		t.Fatalf("expected unspecified ask fields to default on, got %+v", effective)
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

func TestModelProfileDefaultAskResponseProfile(t *testing.T) {
	profile := ModelProfile{
		Provider: config.ProviderOllama,
		Model:    "llama3.1",
	}
	if got := profile.EffectiveAskResponseProfile(); got != model.DefaultAskResponseProfile() {
		t.Fatalf("expected default ask response profile, got %+v", got)
	}
}

func TestUpsertRejectsInvalidSamplerFields(t *testing.T) {
	profiles := Empty()
	err := profiles.Upsert(ModelProfile{
		Provider:      config.ProviderYZMA,
		Model:         "gemma4:e4b",
		Temperature:   -0.1,
		TopK:          20,
		TopP:          0.9,
		MinP:          0.1,
		RepeatPenalty: 1.1,
	})
	if err == nil || !strings.Contains(err.Error(), "temperature") {
		t.Fatalf("expected temperature validation error, got %v", err)
	}

	err = profiles.Upsert(ModelProfile{
		Provider:      config.ProviderYZMA,
		Model:         "gemma4:e4b",
		Temperature:   0.7,
		TopK:          20,
		TopP:          1.2,
		MinP:          0.1,
		RepeatPenalty: 1.1,
	})
	if err == nil || !strings.Contains(err.Error(), "top_p") {
		t.Fatalf("expected top_p validation error, got %v", err)
	}
}
