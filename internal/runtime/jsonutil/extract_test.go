package jsonutil

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCandidates_WithWrapperProse(t *testing.T) {
	raw := "prefix text\n{\"k\":\"v\"}\ntrailing text"
	candidates := Candidates(raw)
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	if candidates[0] != "{\"k\":\"v\"}" {
		t.Fatalf("unexpected candidate: %q", candidates[0])
	}
}

func TestCandidates_BracesInsideQuotedStrings(t *testing.T) {
	raw := `noise {"text":"hello {world}","ok":true} trailing`
	candidates := Candidates(raw)
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(candidates[0]), &decoded); err != nil {
		t.Fatalf("candidate should remain valid JSON: %v", err)
	}
}

func TestFirstValidObject_MalformedThenValid(t *testing.T) {
	raw := `prefix {bad json} {"valid":"yes","num":1}`
	blob, err := FirstValidObject(raw)
	if err != nil {
		t.Fatalf("expected valid object, got err: %v", err)
	}
	if !strings.Contains(blob, `"valid":"yes"`) {
		t.Fatalf("expected second valid object, got %q", blob)
	}
}

func TestFirstValidObject_WithTrailingToolTags(t *testing.T) {
	raw := `{"mode":"ok"}<tool_call>{"name":"x"}</tool_call>`
	blob, err := FirstValidObject(raw)
	if err != nil {
		t.Fatalf("expected valid object, got err: %v", err)
	}
	if blob != `{"mode":"ok"}` {
		t.Fatalf("unexpected blob: %q", blob)
	}
}

func TestFirstValidObject_FencedJSON(t *testing.T) {
	raw := "```json\n{\"name\":\"value\"}\n```"
	blob, err := FirstValidObject(raw)
	if err != nil {
		t.Fatalf("expected valid object, got err: %v", err)
	}
	if blob != `{"name":"value"}` {
		t.Fatalf("unexpected blob: %q", blob)
	}
}

func TestFirstValidObject_NoObject(t *testing.T) {
	_, err := FirstValidObject("not json here")
	if err == nil {
		t.Fatal("expected error when no object exists")
	}
}
