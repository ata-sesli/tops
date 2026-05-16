package jsonutil

import (
	"encoding/json"
	"fmt"
	"strings"
)

func FirstValidObject(raw string) (string, error) {
	for _, candidate := range Candidates(raw) {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(candidate), &fields); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("model response did not contain valid JSON object")
}

func Candidates(raw string) []string {
	trimmed := stripFencedWrapper(raw)
	out := make([]string, 0, 4)
	inString := false
	escaped := false
	for start := 0; start < len(trimmed); start++ {
		ch := trimmed[start]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}
		if ch == '"' {
			inString = true
			continue
		}
		if ch != '{' {
			continue
		}
		end, ok := findObjectEnd(trimmed, start)
		if !ok {
			continue
		}
		out = append(out, trimmed[start:end+1])
	}
	return out
}

func stripFencedWrapper(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}
	trimmed = strings.TrimPrefix(trimmed, "```json")
	trimmed = strings.TrimPrefix(trimmed, "```JSON")
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSpace(trimmed)
	if end := strings.LastIndex(trimmed, "```"); end >= 0 {
		trimmed = strings.TrimSpace(trimmed[:end])
	}
	return trimmed
}

func findObjectEnd(raw string, start int) (int, bool) {
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(raw); i++ {
		ch := raw[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i, true
			}
			if depth < 0 {
				return -1, false
			}
		}
	}
	return -1, false
}
