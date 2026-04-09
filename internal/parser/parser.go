package parser

import (
	"encoding/json"
	"fmt"
	"strings"

	"tops/internal/model"
)

type Parser struct{}

func New() Parser { return Parser{} }

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
		Command         string                 `json:"command"`
		Explanation     string                 `json:"explanation"`
		Intent          model.GenerationIntent `json:"intent_struct"`
		Assumptions     []string               `json:"assumptions"`
		Ambiguities     []string               `json:"ambiguities"`
		ConfidenceNotes []string               `json:"confidence_notes"`
	}
	if err := decodeStructured(raw, &decoded, genListFields...); err != nil {
		return model.GenResult{}, err
	}
	if strings.TrimSpace(decoded.Command) == "" {
		return model.GenResult{}, fmt.Errorf("model response missing required field: command")
	}
	if strings.TrimSpace(decoded.Explanation) == "" {
		return model.GenResult{}, fmt.Errorf("model response missing required field: explanation")
	}
	return model.GenResult{
		Command:         decoded.Command,
		Explanation:     decoded.Explanation,
		Intent:          decoded.Intent,
		Assumptions:     decoded.Assumptions,
		Ambiguities:     decoded.Ambiguities,
		ConfidenceNotes: decoded.ConfidenceNotes,
	}, nil
}

func (Parser) ParseAsk(raw string) (model.AskResult, error) {
	var decoded struct {
		Answer        string   `json:"answer"`
		Observations  []string `json:"observations"`
		Inferences    []string `json:"inferences"`
		Uncertainties []string `json:"uncertainties"`
		Assumptions   []string `json:"assumptions"`
		Notes         []string `json:"notes"`
	}
	if err := decodeStructured(raw, &decoded, askListFields...); err != nil {
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
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "```") {
		trimmed = strings.TrimPrefix(trimmed, "```json")
		trimmed = strings.TrimPrefix(trimmed, "```")
		trimmed = strings.TrimSuffix(trimmed, "```")
		trimmed = strings.TrimSpace(trimmed)
	}
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start == -1 || end == -1 || end <= start {
		return "", fmt.Errorf("model response did not contain valid JSON object")
	}
	return trimmed[start : end+1], nil
}
