package tui

import (
	"testing"

	"tops/internal/model"
)

func TestParseInputModeCommand(t *testing.T) {
	parsed := ParseInput("/help grep -r")
	if parsed.Kind != KindMode {
		t.Fatalf("expected mode kind, got %s", parsed.Kind)
	}
	if parsed.Mode != model.ModeHelp || parsed.Payload != "grep -r" {
		t.Fatalf("unexpected parse result: %+v", parsed)
	}
}

func TestParseInputHistoryDBDefaultLimit(t *testing.T) {
	parsed := ParseInput("/history db")
	if parsed.Kind != KindHistoryDB {
		t.Fatalf("expected history_db kind, got %s", parsed.Kind)
	}
	if parsed.Limit != defaultPersistedLimit {
		t.Fatalf("expected default limit %d, got %d", defaultPersistedLimit, parsed.Limit)
	}
}

func TestParseInputSessionReadDefaultLimit(t *testing.T) {
	parsed := ParseInput("/session read 7")
	if parsed.Kind != KindSessionRead {
		t.Fatalf("expected session_read kind, got %s", parsed.Kind)
	}
	if parsed.SessionID != 7 || parsed.Limit != defaultSessionReadLimit {
		t.Fatalf("unexpected parse result: %+v", parsed)
	}
}

func TestParseInputSessionReadCustomLimit(t *testing.T) {
	parsed := ParseInput("/session read 7 12")
	if parsed.Kind != KindSessionRead || parsed.SessionID != 7 || parsed.Limit != 12 {
		t.Fatalf("unexpected parse result: %+v", parsed)
	}
}

func TestParseInputSessionDeleteConfirm(t *testing.T) {
	parsed := ParseInput("/session delete 9 confirm")
	if parsed.Kind != KindSessionDelete || parsed.SessionID != 9 {
		t.Fatalf("unexpected parse result: %+v", parsed)
	}
}

func TestParseInputModelConfigShow(t *testing.T) {
	parsed := ParseInput("/model config show")
	if parsed.Kind != KindModelConfigShow {
		t.Fatalf("expected model_config_show kind, got %s", parsed.Kind)
	}
}

func TestParseInputModelConfigSet(t *testing.T) {
	parsed := ParseInput("/model config set max_length 2048")
	if parsed.Kind != KindModelConfigSet {
		t.Fatalf("expected model_config_set kind, got %s", parsed.Kind)
	}
	if parsed.ConfigField != "max_length" || parsed.Payload != "2048" {
		t.Fatalf("unexpected parse result: %+v", parsed)
	}
}

func TestParseInputModelConfigSetSystemPrompt(t *testing.T) {
	parsed := ParseInput("/model config set system_prompt You are concise")
	if parsed.Kind != KindModelConfigSet {
		t.Fatalf("expected model_config_set kind, got %s", parsed.Kind)
	}
	if parsed.ConfigField != "system_prompt" || parsed.Payload != "You are concise" {
		t.Fatalf("unexpected parse result: %+v", parsed)
	}
}

func TestParseInputModelConfigSetThink(t *testing.T) {
	parsed := ParseInput("/model config set think off")
	if parsed.Kind != KindModelConfigSet {
		t.Fatalf("expected model_config_set kind, got %s", parsed.Kind)
	}
	if parsed.ConfigField != "think" || parsed.Payload != "off" {
		t.Fatalf("unexpected parse result: %+v", parsed)
	}
}

func TestParseInputModelConfigReset(t *testing.T) {
	parsed := ParseInput("/model config reset")
	if parsed.Kind != KindModelConfigReset {
		t.Fatalf("expected model_config_reset kind, got %s", parsed.Kind)
	}
}

func TestParseInputModelResponseShow(t *testing.T) {
	parsed := ParseInput("/model response show")
	if parsed.Kind != KindModelResponseShow {
		t.Fatalf("expected model_response_show kind, got %s", parsed.Kind)
	}
}

func TestParseInputModelResponseSet(t *testing.T) {
	parsed := ParseInput("/model response set notes off")
	if parsed.Kind != KindModelResponseSet {
		t.Fatalf("expected model_response_set kind, got %s", parsed.Kind)
	}
	if parsed.ConfigField != "notes" || parsed.Payload != "off" {
		t.Fatalf("unexpected parse result: %+v", parsed)
	}
}

func TestParseInputPurgeConfirm(t *testing.T) {
	parsed := ParseInput("/purge confirm")
	if parsed.Kind != KindPurge {
		t.Fatalf("expected purge kind, got %s", parsed.Kind)
	}
}

func TestParseInputInvalidLimit(t *testing.T) {
	parsed := ParseInput("/history db nope")
	if parsed.Kind != KindInvalid {
		t.Fatalf("expected invalid kind, got %s", parsed.Kind)
	}
}

func TestParseInputPlainTextGuidance(t *testing.T) {
	parsed := ParseInput("what process uses port 3000")
	if parsed.Kind != KindGuidance {
		t.Fatalf("expected guidance kind, got %s", parsed.Kind)
	}
}

func TestParseInputUnknownCommand(t *testing.T) {
	parsed := ParseInput("/unknown cmd")
	if parsed.Kind != KindInvalid {
		t.Fatalf("expected invalid kind, got %s", parsed.Kind)
	}
}

func TestParseInputSetupCommand(t *testing.T) {
	parsed := ParseInput("/setup")
	if parsed.Kind != KindSetup {
		t.Fatalf("expected setup kind, got %s", parsed.Kind)
	}
}

func TestParseInputModelsCommand(t *testing.T) {
	parsed := ParseInput("/models")
	if parsed.Kind != KindModels {
		t.Fatalf("expected models kind, got %s", parsed.Kind)
	}
}

func TestParseInputModelUseCommand(t *testing.T) {
	parsed := ParseInput("/model use llama3.1")
	if parsed.Kind != KindModelUse {
		t.Fatalf("expected model_use kind, got %s", parsed.Kind)
	}
	if parsed.Payload != "llama3.1" {
		t.Fatalf("unexpected model payload: %+v", parsed)
	}
}

func TestParseInputModelUseInvalid(t *testing.T) {
	parsed := ParseInput("/model nope")
	if parsed.Kind != KindInvalid {
		t.Fatalf("expected invalid kind, got %s", parsed.Kind)
	}
}

func TestParseInputExecutionPolicyShow(t *testing.T) {
	parsed := ParseInput("/execution policy show")
	if parsed.Kind != KindExecutionPolicyShow {
		t.Fatalf("expected execution_policy_show kind, got %s", parsed.Kind)
	}
}

func TestParseInputExecutionPolicySetReadOnly(t *testing.T) {
	parsed := ParseInput("/execution policy set read-only disallow")
	if parsed.Kind != KindExecutionPolicySet {
		t.Fatalf("expected execution_policy_set kind, got %s", parsed.Kind)
	}
	if parsed.ConfigField != "read-only" || parsed.Payload != "disallow" {
		t.Fatalf("unexpected parse result: %+v", parsed)
	}
}

func TestParseInputExecutionPolicySetWrite(t *testing.T) {
	parsed := ParseInput("/execution policy set write request")
	if parsed.Kind != KindExecutionPolicySet {
		t.Fatalf("expected execution_policy_set kind, got %s", parsed.Kind)
	}
	if parsed.ConfigField != "write" || parsed.Payload != "request" {
		t.Fatalf("unexpected parse result: %+v", parsed)
	}
}

func TestParseInputExecutionTraceShow(t *testing.T) {
	parsed := ParseInput("/execution trace show")
	if parsed.Kind != KindExecutionTraceShow {
		t.Fatalf("expected execution_trace_show kind, got %s", parsed.Kind)
	}
}

func TestParseInputExecutionTraceSet(t *testing.T) {
	parsed := ParseInput("/execution trace set debug")
	if parsed.Kind != KindExecutionTraceSet {
		t.Fatalf("expected execution_trace_set kind, got %s", parsed.Kind)
	}
	if parsed.Payload != "debug" {
		t.Fatalf("unexpected parse result: %+v", parsed)
	}
}
