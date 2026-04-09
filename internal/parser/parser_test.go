package parser

import (
	"strings"
	"testing"
)

func TestParseGenFencedJSON(t *testing.T) {
	raw := "```json\n{\"command\":\"find . -name '*.log'\",\"explanation\":\"Find log files\",\"intent_struct\":{\"intent\":\"find_files\",\"constraints\":{},\"action\":\"list\"},\"assumptions\":[\"cwd\"],\"ambiguities\":[],\"confidence_notes\":[]}\n```"
	result, err := New().ParseGen(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if result.Command == "" || result.Explanation == "" {
		t.Fatalf("unexpected parsed result: %+v", result)
	}
}

func TestParseAskMissingAnswer(t *testing.T) {
	raw := `{ "observations": ["x"], "inferences":[], "uncertainties":[], "assumptions":[], "notes":[] }`
	_, err := New().ParseAsk(raw)
	if err == nil {
		t.Fatal("expected error for missing answer")
	}
}

func TestParseHelpRejectUnknownFields(t *testing.T) {
	raw := `{ "summary": "ok", "syntax": "cmd", "important_flags": [], "examples": [], "caveats": [], "assumptions": [], "notes": [], "extra": "bad" }`
	_, err := New().ParseHelp(raw, "ls")
	if err == nil {
		t.Fatal("expected unknown field parse error")
	}
}

func TestParseAskNormalizesStringListFields(t *testing.T) {
	raw := `{ "answer": "macOS", "observations": "uname output: Darwin", "inferences":"This is macOS", "uncertainties": null, "assumptions":"uname succeeded", "notes":"Darwin is the kernel name" }`
	result, err := New().ParseAsk(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(result.Observations) != 1 || result.Observations[0] != "uname output: Darwin" {
		t.Fatalf("unexpected observations: %+v", result.Observations)
	}
	if len(result.Inferences) != 1 || result.Inferences[0] != "This is macOS" {
		t.Fatalf("unexpected inferences: %+v", result.Inferences)
	}
	if len(result.Uncertainties) != 0 {
		t.Fatalf("expected empty uncertainties, got %+v", result.Uncertainties)
	}
	if len(result.Assumptions) != 1 || result.Assumptions[0] != "uname succeeded" {
		t.Fatalf("unexpected assumptions: %+v", result.Assumptions)
	}
	if len(result.Notes) != 1 || result.Notes[0] != "Darwin is the kernel name" {
		t.Fatalf("unexpected notes: %+v", result.Notes)
	}
}

func TestParseGenNormalizesStringListFields(t *testing.T) {
	raw := `{ "command":"pwd", "explanation":"Show cwd", "intent_struct":{"intent":"show cwd","constraints":{},"action":"inspect"}, "assumptions":"cwd exists", "ambiguities": null, "confidence_notes":"Grounded by local state" }`
	result, err := New().ParseGen(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(result.Assumptions) != 1 || result.Assumptions[0] != "cwd exists" {
		t.Fatalf("unexpected assumptions: %+v", result.Assumptions)
	}
	if len(result.Ambiguities) != 0 {
		t.Fatalf("expected empty ambiguities, got %+v", result.Ambiguities)
	}
	if len(result.ConfidenceNotes) != 1 || result.ConfidenceNotes[0] != "Grounded by local state" {
		t.Fatalf("unexpected confidence notes: %+v", result.ConfidenceNotes)
	}
}

func TestParseHelpNormalizesNullListFields(t *testing.T) {
	raw := `{ "summary": "ok", "syntax": "ls", "important_flags": null, "examples": "ls -la", "caveats": null, "assumptions": "cwd is readable", "notes": "basic listing" }`
	result, err := New().ParseHelp(raw, "ls")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(result.ImportantFlags) != 0 {
		t.Fatalf("expected empty important flags, got %+v", result.ImportantFlags)
	}
	if len(result.Examples) != 1 || result.Examples[0] != "ls -la" {
		t.Fatalf("unexpected examples: %+v", result.Examples)
	}
	if len(result.Caveats) != 0 {
		t.Fatalf("expected empty caveats, got %+v", result.Caveats)
	}
	if len(result.Assumptions) != 1 || result.Assumptions[0] != "cwd is readable" {
		t.Fatalf("unexpected assumptions: %+v", result.Assumptions)
	}
	if len(result.Notes) != 1 || result.Notes[0] != "basic listing" {
		t.Fatalf("unexpected notes: %+v", result.Notes)
	}
}

func TestParseAskRejectsNonListObjectField(t *testing.T) {
	raw := `{ "answer": "macOS", "observations": {"bad":"value"}, "inferences":[], "uncertainties":[], "assumptions":[], "notes":[] }`
	_, err := New().ParseAsk(raw)
	if err == nil {
		t.Fatal("expected type error")
	}
	if !strings.Contains(err.Error(), `field "observations" must be an array of strings, a string, or null`) {
		t.Fatalf("unexpected error: %v", err)
	}
}
