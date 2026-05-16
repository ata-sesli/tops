package parser

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"tops/internal/model"
	"tops/internal/ops/benchmetrics"
	"tops/internal/runtime/jsonutil"
)

type Parser struct{}

func New() Parser { return Parser{} }

type RepairCallback func(ctx context.Context, mode string, raw string, parseErr error) (string, error)

var (
	helpListFields = []string{"important_flags", "examples", "caveats", "assumptions", "notes"}
	genListFields  = []string{"assumptions", "ambiguities", "confidence_notes"}
	askListFields  = []string{"observations", "inferences", "uncertainties", "assumptions", "notes"}
)

func (Parser) ParseHelp(raw string, target string) (model.HelpResult, error) {
	var decoded struct {
		Summary        string   `json:"summary"`
		Syntax         string   `json:"syntax"`
		ImportantFlags []string `json:"important_flags"`
		Examples       []string `json:"examples"`
		Caveats        []string `json:"caveats"`
		Assumptions    []string `json:"assumptions"`
		Notes          []string `json:"notes"`
	}
	if err := decodeStructured(raw, &decoded, helpListFields...); err != nil {
		return model.HelpResult{}, err
	}
	if strings.TrimSpace(decoded.Summary) == "" {
		return model.HelpResult{}, fmt.Errorf("model response missing required field: summary")
	}
	return model.HelpResult{
		Target:         target,
		Summary:        decoded.Summary,
		Syntax:         decoded.Syntax,
		ImportantFlags: decoded.ImportantFlags,
		Examples:       decoded.Examples,
		Caveats:        decoded.Caveats,
		Assumptions:    decoded.Assumptions,
		Notes:          decoded.Notes,
	}, nil
}

func (Parser) ParseGen(raw string) (model.GenResult, error) {
	var decoded struct {
		Command         string         `json:"command"`
		Explanation     string         `json:"explanation"`
		Intent          map[string]any `json:"intent_struct"`
		OutputKind      string         `json:"output_kind"`
		TargetShell     string         `json:"target_shell"`
		Assumptions     []string       `json:"assumptions"`
		Ambiguities     []string       `json:"ambiguities"`
		ConfidenceNotes []string       `json:"confidence_notes"`
	}
	if err := decodeStructured(raw, &decoded, genListFields...); err != nil {
		tolerant, tolerantErr := parseGenTolerant(raw)
		if tolerantErr != nil {
			return model.GenResult{}, err
		}
		return tolerant, nil
	}
	if strings.TrimSpace(decoded.Command) == "" {
		return model.GenResult{}, fmt.Errorf("model response missing required field: command")
	}
	intent := parseGenerationIntent(decoded.Intent)
	return model.GenResult{
		Command:         decoded.Command,
		Explanation:     decoded.Explanation,
		Intent:          intent,
		OutputKind:      decoded.OutputKind,
		TargetShell:     decoded.TargetShell,
		Assumptions:     decoded.Assumptions,
		Ambiguities:     decoded.Ambiguities,
		ConfidenceNotes: decoded.ConfidenceNotes,
	}, nil
}

func parseGenTolerant(raw string) (model.GenResult, error) {
	jsonBlob, err := extractJSON(raw)
	if err != nil {
		return model.GenResult{}, err
	}
	jsonBlob, err = normalizeListFields(jsonBlob, genListFields)
	if err != nil {
		return model.GenResult{}, err
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonBlob), &obj); err != nil {
		return model.GenResult{}, fmt.Errorf("invalid model JSON: %w", err)
	}
	command, err := parseRequiredStringField(obj, "command")
	if err != nil {
		return model.GenResult{}, err
	}
	explanation, _ := parseOptionalStringField(obj, "explanation")
	outputKind, _ := parseOptionalStringField(obj, "output_kind")
	targetShell, _ := parseOptionalStringField(obj, "target_shell")
	intentStruct, _ := parseOptionalMapField(obj, "intent_struct")
	assumptions, err := parseStringListField(obj, "assumptions")
	if err != nil {
		return model.GenResult{}, err
	}
	ambiguities, err := parseStringListField(obj, "ambiguities")
	if err != nil {
		return model.GenResult{}, err
	}
	confidenceNotes, err := parseStringListField(obj, "confidence_notes")
	if err != nil {
		return model.GenResult{}, err
	}
	return model.GenResult{
		Command:         command,
		Explanation:     explanation,
		Intent:          parseGenerationIntent(intentStruct),
		OutputKind:      outputKind,
		TargetShell:     targetShell,
		Assumptions:     assumptions,
		Ambiguities:     ambiguities,
		ConfidenceNotes: confidenceNotes,
	}, nil
}

func parseRequiredStringField(obj map[string]json.RawMessage, key string) (string, error) {
	value, ok := obj[key]
	if !ok {
		return "", fmt.Errorf("model response missing required field: %s", key)
	}
	var out string
	if err := json.Unmarshal(value, &out); err != nil {
		return "", fmt.Errorf("invalid model JSON: field %q must be string", key)
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return "", fmt.Errorf("model response missing required field: %s", key)
	}
	return out, nil
}

func parseOptionalStringField(obj map[string]json.RawMessage, key string) (string, error) {
	value, ok := obj[key]
	if !ok || isJSONNull(value) {
		return "", nil
	}
	var out string
	if err := json.Unmarshal(value, &out); err != nil {
		return "", fmt.Errorf("invalid model JSON: field %q must be string", key)
	}
	return strings.TrimSpace(out), nil
}

func parseOptionalMapField(obj map[string]json.RawMessage, key string) (map[string]any, error) {
	value, ok := obj[key]
	if !ok || isJSONNull(value) {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal(value, &out); err != nil {
		return nil, fmt.Errorf("invalid model JSON: field %q must be object", key)
	}
	return out, nil
}

func parseStringListField(obj map[string]json.RawMessage, key string) ([]string, error) {
	value, ok := obj[key]
	if !ok || isJSONNull(value) {
		return []string{}, nil
	}
	var out []string
	if err := json.Unmarshal(value, &out); err != nil {
		return nil, fmt.Errorf("invalid model JSON: field %q must be an array of strings", key)
	}
	return out, nil
}

func parseGenerationIntent(raw map[string]any) model.GenerationIntent {
	out := model.GenerationIntent{
		Intent:      "",
		Constraints: map[string]string{},
		Action:      "",
	}
	if len(raw) == 0 {
		return out
	}

	if v, ok := raw["intent"].(string); ok {
		out.Intent = strings.TrimSpace(v)
	}
	if v, ok := raw["action"].(string); ok {
		out.Action = strings.TrimSpace(v)
	}
	if constraints, ok := raw["constraints"]; ok {
		if asMap, ok := constraints.(map[string]any); ok {
			for key, value := range asMap {
				trimmedKey := strings.TrimSpace(key)
				if trimmedKey == "" {
					continue
				}
				out.Constraints[trimmedKey] = stringifyConstraintValue(value)
			}
		}
	}
	return out
}

func stringifyConstraintValue(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case nil:
		return ""
	case float64, bool:
		return strings.TrimSpace(fmt.Sprintf("%v", typed))
	default:
		blob, err := json.Marshal(typed)
		if err != nil {
			return strings.TrimSpace(fmt.Sprintf("%v", typed))
		}
		return strings.TrimSpace(string(blob))
	}
}

func (Parser) ParseAsk(raw string) (model.AskResult, error) {
	jsonBlob, err := extractJSON(raw)
	if err != nil {
		return model.AskResult{}, err
	}
	jsonBlob, err = normalizeAskAliases(jsonBlob)
	if err != nil {
		return model.AskResult{}, err
	}
	jsonBlob, err = normalizeListFields(jsonBlob, askListFields)
	if err != nil {
		return model.AskResult{}, err
	}

	var decoded struct {
		Answer        string   `json:"answer"`
		Observations  []string `json:"observations"`
		Inferences    []string `json:"inferences"`
		Uncertainties []string `json:"uncertainties"`
		Assumptions   []string `json:"assumptions"`
		Notes         []string `json:"notes"`
	}
	dec := json.NewDecoder(strings.NewReader(jsonBlob))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&decoded); err != nil {
		return model.AskResult{}, fmt.Errorf("invalid model JSON: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return model.AskResult{}, err
	}
	if strings.TrimSpace(decoded.Answer) == "" {
		return model.AskResult{}, fmt.Errorf("model response missing required field: answer")
	}
	return model.AskResult{
		Answer:        decoded.Answer,
		Observations:  decoded.Observations,
		Inferences:    decoded.Inferences,
		Uncertainties: decoded.Uncertainties,
		Assumptions:   decoded.Assumptions,
		Notes:         decoded.Notes,
	}, nil
}

func normalizeAskAliases(jsonBlob string) (string, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonBlob), &obj); err != nil {
		return "", fmt.Errorf("invalid model JSON: %w", err)
	}
	aliasToCanonical := map[string]string{
		"a": "answer",
		"o": "observations",
		"i": "inferences",
		"u": "uncertainties",
		"s": "assumptions",
		"n": "notes",
	}
	for alias, canonical := range aliasToCanonical {
		if _, exists := obj[canonical]; exists {
			continue
		}
		if raw, ok := obj[alias]; ok {
			obj[canonical] = raw
			delete(obj, alias)
		}
	}
	normalized, err := json.Marshal(obj)
	if err != nil {
		return "", fmt.Errorf("normalize model JSON: %w", err)
	}
	return string(normalized), nil
}

func (p Parser) ParseHelpWithRepair(ctx context.Context, raw string, target string, repair RepairCallback) (model.HelpResult, error) {
	parsed, err := p.ParseHelp(raw, target)
	if err == nil || repair == nil {
		return parsed, err
	}
	benchmetrics.IncrementRepair(ctx)
	repaired, repairErr := repair(ctx, "help", raw, err)
	if repairErr != nil || strings.TrimSpace(repaired) == "" {
		return model.HelpResult{}, err
	}
	return p.ParseHelp(repaired, target)
}

func (p Parser) ParseGenWithRepair(ctx context.Context, raw string, repair RepairCallback) (model.GenResult, error) {
	parsed, err := p.ParseGen(raw)
	if err == nil || repair == nil {
		return parsed, err
	}
	benchmetrics.IncrementRepair(ctx)
	repaired, repairErr := repair(ctx, "gen", raw, err)
	if repairErr != nil || strings.TrimSpace(repaired) == "" {
		return model.GenResult{}, err
	}
	return p.ParseGen(repaired)
}

func (p Parser) ParseAskWithRepair(ctx context.Context, raw string, repair RepairCallback) (model.AskResult, error) {
	parsed, err := p.ParseAsk(raw)
	if err == nil || repair == nil {
		return parsed, err
	}
	benchmetrics.IncrementRepair(ctx)
	repaired, repairErr := repair(ctx, "ask", raw, err)
	if repairErr != nil || strings.TrimSpace(repaired) == "" {
		return model.AskResult{}, err
	}
	return p.ParseAsk(repaired)
}

func decodeStructured(raw string, out any, listFields ...string) error {
	jsonBlob, err := extractJSON(raw)
	if err != nil {
		return err
	}
	if len(listFields) > 0 {
		jsonBlob, err = normalizeListFields(jsonBlob, listFields)
		if err != nil {
			return err
		}
	}
	dec := json.NewDecoder(strings.NewReader(jsonBlob))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("invalid model JSON: %w", err)
	}
	return nil
}

func normalizeListFields(jsonBlob string, fields []string) (string, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonBlob), &obj); err != nil {
		return "", fmt.Errorf("invalid model JSON: %w", err)
	}

	for _, field := range fields {
		raw, ok := obj[field]
		if !ok || isJSONNull(raw) {
			obj[field] = json.RawMessage("[]")
			continue
		}

		var list []string
		if err := json.Unmarshal(raw, &list); err == nil {
			continue
		}

		var single string
		if err := json.Unmarshal(raw, &single); err == nil {
			wrapped, err := json.Marshal([]string{single})
			if err != nil {
				return "", fmt.Errorf("normalize field %q: %w", field, err)
			}
			obj[field] = wrapped
			continue
		}

		return "", fmt.Errorf("invalid model JSON: field %q must be an array of strings, a string, or null", field)
	}

	normalized, err := json.Marshal(obj)
	if err != nil {
		return "", fmt.Errorf("normalize model JSON: %w", err)
	}
	return string(normalized), nil
}

func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

func extractJSON(raw string) (string, error) {
	return jsonutil.FirstValidObject(raw)
}
