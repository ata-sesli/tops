package tui

import (
	"fmt"
	"strconv"
	"strings"

	"tops/internal/model"
)

const defaultPersistedLimit = 20
const defaultSessionReadLimit = 50

type CommandKind string

const (
	KindMode                CommandKind = "mode"
	KindHistory             CommandKind = "history"
	KindHistoryDB           CommandKind = "history_db"
	KindSessions            CommandKind = "sessions"
	KindSessionRead         CommandKind = "session_read"
	KindSessionDelete       CommandKind = "session_delete"
	KindPurge               CommandKind = "purge"
	KindModels              CommandKind = "models"
	KindModelUse            CommandKind = "model_use"
	KindModelConfigShow     CommandKind = "model_config_show"
	KindModelConfigSet      CommandKind = "model_config_set"
	KindModelConfigReset    CommandKind = "model_config_reset"
	KindExecutionPolicyShow CommandKind = "execution_policy_show"
	KindExecutionPolicySet  CommandKind = "execution_policy_set"
	KindSetup               CommandKind = "setup"
	KindClear               CommandKind = "clear"
	KindExit                CommandKind = "exit"
	KindGuidance            CommandKind = "guidance"
	KindInvalid             CommandKind = "invalid"
)

type ParseResult struct {
	Raw         string
	Kind        CommandKind
	Mode        model.Mode
	Payload     string
	Message     string
	Limit       int
	SessionID   int64
	ConfigField string
}

func ParseInput(raw string) ParseResult {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ParseResult{Raw: raw, Kind: KindGuidance, Message: guidanceMessage()}
	}
	if !strings.HasPrefix(trimmed, "/") {
		return ParseResult{Raw: raw, Kind: KindGuidance, Message: guidanceMessage()}
	}

	parts := strings.Fields(trimmed)
	if len(parts) == 0 {
		return ParseResult{Raw: raw, Kind: KindGuidance, Message: guidanceMessage()}
	}
	cmd := strings.ToLower(strings.TrimPrefix(parts[0], "/"))
	payload := strings.TrimSpace(trimmed[len(parts[0]):])

	switch cmd {
	case "help":
		return parseMode(raw, model.ModeHelp, payload, "Usage: /help <command or snippet>")
	case "gen":
		return parseMode(raw, model.ModeGen, payload, "Usage: /gen <natural language request>")
	case "ask":
		return parseMode(raw, model.ModeAsk, payload, "Usage: /ask <question>")
	case "history":
		return parseHistory(raw, payload)
	case "sessions":
		return parseSessions(raw, payload)
	case "session":
		return parseSession(raw, payload)
	case "purge":
		if strings.TrimSpace(payload) != "confirm" {
			return ParseResult{Raw: raw, Kind: KindInvalid, Message: "Usage: /purge confirm"}
		}
		return ParseResult{Raw: raw, Kind: KindPurge}
	case "models":
		if payload != "" {
			return ParseResult{Raw: raw, Kind: KindInvalid, Message: "Usage: /models"}
		}
		return ParseResult{Raw: raw, Kind: KindModels}
	case "model":
		return parseModelCommand(raw, payload)
	case "setup":
		if payload != "" {
			return ParseResult{Raw: raw, Kind: KindInvalid, Message: "Usage: /setup"}
		}
		return ParseResult{Raw: raw, Kind: KindSetup}
	case "execution":
		return parseExecutionCommand(raw, payload)
	case "clear":
		if payload != "" {
			return ParseResult{Raw: raw, Kind: KindInvalid, Message: "Usage: /clear"}
		}
		return ParseResult{Raw: raw, Kind: KindClear}
	case "exit", "quit":
		if payload != "" {
			return ParseResult{Raw: raw, Kind: KindInvalid, Message: "Usage: /exit"}
		}
		return ParseResult{Raw: raw, Kind: KindExit}
	default:
		return ParseResult{Raw: raw, Kind: KindInvalid, Message: fmt.Sprintf("Unknown command %q. %s", parts[0], guidanceMessage())}
	}
}

func parseMode(raw string, mode model.Mode, payload string, usage string) ParseResult {
	if strings.TrimSpace(payload) == "" {
		return ParseResult{Raw: raw, Kind: KindInvalid, Message: usage}
	}
	return ParseResult{Raw: raw, Kind: KindMode, Mode: mode, Payload: payload}
}

func parseHistory(raw string, payload string) ParseResult {
	if payload == "" {
		return ParseResult{Raw: raw, Kind: KindHistory}
	}
	parts := strings.Fields(payload)
	if len(parts) == 0 || strings.ToLower(parts[0]) != "db" {
		return ParseResult{Raw: raw, Kind: KindInvalid, Message: "Usage: /history OR /history db [N]"}
	}
	if len(parts) == 1 {
		return ParseResult{Raw: raw, Kind: KindHistoryDB, Limit: defaultPersistedLimit}
	}
	if len(parts) == 2 {
		limit, err := parseLimit(parts[1])
		if err != nil {
			return ParseResult{Raw: raw, Kind: KindInvalid, Message: err.Error()}
		}
		return ParseResult{Raw: raw, Kind: KindHistoryDB, Limit: limit}
	}
	return ParseResult{Raw: raw, Kind: KindInvalid, Message: "Usage: /history db [N]"}
}

func parseSessions(raw string, payload string) ParseResult {
	if payload == "" {
		return ParseResult{Raw: raw, Kind: KindSessions, Limit: defaultPersistedLimit}
	}
	parts := strings.Fields(payload)
	if len(parts) != 1 {
		return ParseResult{Raw: raw, Kind: KindInvalid, Message: "Usage: /sessions [N]"}
	}
	limit, err := parseLimit(parts[0])
	if err != nil {
		return ParseResult{Raw: raw, Kind: KindInvalid, Message: err.Error()}
	}
	return ParseResult{Raw: raw, Kind: KindSessions, Limit: limit}
}

func parseSession(raw string, payload string) ParseResult {
	parts := strings.Fields(strings.TrimSpace(payload))
	if len(parts) == 0 {
		return ParseResult{Raw: raw, Kind: KindInvalid, Message: "Usage: /session read <id> [N] OR /session delete <id> confirm"}
	}

	switch strings.ToLower(parts[0]) {
	case "read":
		if len(parts) != 2 && len(parts) != 3 {
			return ParseResult{Raw: raw, Kind: KindInvalid, Message: "Usage: /session read <id> [N]"}
		}
		sessionID, err := parseSessionID(parts[1])
		if err != nil {
			return ParseResult{Raw: raw, Kind: KindInvalid, Message: err.Error()}
		}
		limit := defaultSessionReadLimit
		if len(parts) == 3 {
			limit, err = parseLimit(parts[2])
			if err != nil {
				return ParseResult{Raw: raw, Kind: KindInvalid, Message: err.Error()}
			}
		}
		return ParseResult{Raw: raw, Kind: KindSessionRead, SessionID: sessionID, Limit: limit}
	case "delete":
		if len(parts) != 3 || !strings.EqualFold(parts[2], "confirm") {
			return ParseResult{Raw: raw, Kind: KindInvalid, Message: "Usage: /session delete <id> confirm"}
		}
		sessionID, err := parseSessionID(parts[1])
		if err != nil {
			return ParseResult{Raw: raw, Kind: KindInvalid, Message: err.Error()}
		}
		return ParseResult{Raw: raw, Kind: KindSessionDelete, SessionID: sessionID}
	default:
		return ParseResult{Raw: raw, Kind: KindInvalid, Message: "Usage: /session read <id> [N] OR /session delete <id> confirm"}
	}
}

func parseLimit(raw string) (int, error) {
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 {
		return 0, fmt.Errorf("invalid limit %q: must be a positive integer", raw)
	}
	return limit, nil
}

func parseSessionID(raw string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid session id %q: must be a positive integer", raw)
	}
	return id, nil
}

func parseModelCommand(raw string, payload string) ParseResult {
	trimmed := strings.TrimSpace(payload)
	parts := strings.Fields(trimmed)
	if len(parts) == 0 {
		return ParseResult{Raw: raw, Kind: KindInvalid, Message: "Usage: /model use <index|name> OR /model config <show|set|reset>"}
	}

	switch strings.ToLower(parts[0]) {
	case "use":
		if len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
			return ParseResult{Raw: raw, Kind: KindInvalid, Message: "Usage: /model use <index|name>"}
		}
		return ParseResult{Raw: raw, Kind: KindModelUse, Payload: strings.TrimSpace(parts[1])}
	case "config":
		return parseModelConfig(raw, trimmed)
	default:
		return ParseResult{Raw: raw, Kind: KindInvalid, Message: "Usage: /model use <index|name> OR /model config <show|set|reset>"}
	}
}

func parseModelConfig(raw string, payload string) ParseResult {
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(payload), "config"))
	if rest == "" {
		return ParseResult{Raw: raw, Kind: KindInvalid, Message: "Usage: /model config <show|set|reset>"}
	}
	if strings.EqualFold(rest, "show") {
		return ParseResult{Raw: raw, Kind: KindModelConfigShow}
	}
	if strings.EqualFold(rest, "reset") {
		return ParseResult{Raw: raw, Kind: KindModelConfigReset}
	}
	if !strings.HasPrefix(strings.ToLower(rest), "set ") {
		return ParseResult{Raw: raw, Kind: KindInvalid, Message: "Usage: /model config set <context|max_length|system_prompt|think> <value>"}
	}
	setPayload := strings.TrimSpace(rest[len("set "):])
	firstSpace := strings.IndexAny(setPayload, " \t")
	if firstSpace <= 0 {
		return ParseResult{Raw: raw, Kind: KindInvalid, Message: "Usage: /model config set <context|max_length|system_prompt|think> <value>"}
	}
	field := strings.ToLower(strings.TrimSpace(setPayload[:firstSpace]))
	value := strings.TrimSpace(setPayload[firstSpace+1:])
	if value == "" {
		return ParseResult{Raw: raw, Kind: KindInvalid, Message: "Usage: /model config set <context|max_length|system_prompt|think> <value>"}
	}
	switch field {
	case "context", "max_length", "system_prompt", "think":
		return ParseResult{Raw: raw, Kind: KindModelConfigSet, ConfigField: field, Payload: value}
	default:
		return ParseResult{Raw: raw, Kind: KindInvalid, Message: "Usage: /model config set <context|max_length|system_prompt|think> <value>"}
	}
}

func parseExecutionCommand(raw string, payload string) ParseResult {
	parts := strings.Fields(strings.TrimSpace(payload))
	if len(parts) == 0 {
		return ParseResult{Raw: raw, Kind: KindInvalid, Message: "Usage: /execution policy show OR /execution policy set <read-only|write> <allow|request|disallow>"}
	}
	if !strings.EqualFold(parts[0], "policy") {
		return ParseResult{Raw: raw, Kind: KindInvalid, Message: "Usage: /execution policy show OR /execution policy set <read-only|write> <allow|request|disallow>"}
	}
	if len(parts) == 2 && strings.EqualFold(parts[1], "show") {
		return ParseResult{Raw: raw, Kind: KindExecutionPolicyShow}
	}
	if len(parts) == 4 && strings.EqualFold(parts[1], "set") {
		target := strings.ToLower(strings.TrimSpace(parts[2]))
		if target != "read-only" && target != "write" {
			return ParseResult{Raw: raw, Kind: KindInvalid, Message: "Usage: /execution policy set <read-only|write> <allow|request|disallow>"}
		}
		value := strings.ToLower(strings.TrimSpace(parts[3]))
		if value != "allow" && value != "request" && value != "disallow" {
			return ParseResult{Raw: raw, Kind: KindInvalid, Message: "Usage: /execution policy set <read-only|write> <allow|request|disallow>"}
		}
		return ParseResult{Raw: raw, Kind: KindExecutionPolicySet, ConfigField: target, Payload: value}
	}
	return ParseResult{Raw: raw, Kind: KindInvalid, Message: "Usage: /execution policy show OR /execution policy set <read-only|write> <allow|request|disallow>"}
}

func guidanceMessage() string {
	return "Manager commands: /setup, /models, /model use <index|name>, /model config <show|set|reset>, /execution policy <show|set>, /history, /history db [N], /sessions [N], /session read <id> [N], /session delete <id> confirm, /purge confirm, /clear, /exit"
}
