package parser

import "testing"

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
