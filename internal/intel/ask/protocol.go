package ask

import (
	"encoding/json"
	"fmt"
	"strings"

	"tops/internal/runtime/workflow/functions"
)

type askLoopCommandKind string

const (
	askLoopCommandCall  askLoopCommandKind = "call"
	askLoopCommandFinal askLoopCommandKind = "final"
)

type askLoopCommand struct {
	Kind         askLoopCommandKind
	FunctionName string
	Args         map[string]any
	FinalRaw     string
}

func parseAskLoopCommand(raw string, registry functions.FunctionRegistry) (askLoopCommand, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return askLoopCommand{}, fmt.Errorf("invalid ask loop output: empty response")
	}

	if strings.HasPrefix(trimmed, "CALL ") {
		if strings.Contains(trimmed, "\n") {
			return askLoopCommand{}, fmt.Errorf("invalid ask loop output: CALL must be a single line")
		}
		payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "CALL"))
		if payload == "" {
			return askLoopCommand{}, fmt.Errorf("invalid ask loop output: CALL is missing function name")
		}
		name, argsRaw, _ := strings.Cut(payload, " ")
		name = strings.TrimSpace(name)
		if name == "" {
			return askLoopCommand{}, fmt.Errorf("invalid ask loop output: CALL is missing function name")
		}
		if registry != nil {
			if _, ok := registry.Get(name); !ok {
				return askLoopCommand{}, fmt.Errorf("invalid ask loop output: unknown function %q", name)
			}
		}

		args := map[string]any{}
		argsRaw = strings.TrimSpace(argsRaw)
		if argsRaw != "" {
			if err := json.Unmarshal([]byte(argsRaw), &args); err != nil {
				return askLoopCommand{}, fmt.Errorf("invalid ask loop output: CALL args must be a JSON object: %w", err)
			}
			if args == nil {
				args = map[string]any{}
			}
		}

		return askLoopCommand{
			Kind:         askLoopCommandCall,
			FunctionName: name,
			Args:         args,
		}, nil
	}

	if strings.HasPrefix(trimmed, "FINAL") {
		firstLine := trimmed
		if idx := strings.Index(firstLine, "\n"); idx >= 0 {
			firstLine = firstLine[:idx]
		}
		if !strings.HasPrefix(strings.TrimSpace(firstLine), "FINAL a:") {
			return askLoopCommand{}, fmt.Errorf("invalid ask loop output: FINAL must start with 'FINAL a:'")
		}
		return askLoopCommand{
			Kind:     askLoopCommandFinal,
			FinalRaw: trimmed,
		}, nil
	}

	return askLoopCommand{}, fmt.Errorf("invalid ask loop output: expected CALL or FINAL")
}

type taggedFinalStreamCollector struct {
	raw           strings.Builder
	emittedAnswer string
}

func (c *taggedFinalStreamCollector) Feed(chunk string, onAnswerDelta func(string)) {
	if chunk == "" {
		return
	}
	c.raw.WriteString(chunk)
	answer, ok := extractTaggedAnswerProgress(c.raw.String())
	if !ok {
		return
	}
	if strings.HasPrefix(answer, c.emittedAnswer) {
		delta := answer[len(c.emittedAnswer):]
		if delta != "" && onAnswerDelta != nil {
			onAnswerDelta(delta)
		}
		c.emittedAnswer = answer
		return
	}
	if answer != c.emittedAnswer {
		c.emittedAnswer = answer
	}
}

func (c *taggedFinalStreamCollector) Raw() string {
	return c.raw.String()
}

func extractTaggedAnswerProgress(raw string) (string, bool) {
	normalized := strings.ReplaceAll(raw, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")

	lines := strings.Split(normalized, "\n")
	firstLineIdx := -1
	firstLine := ""
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		firstLineIdx = i
		firstLine = trimmed
		break
	}
	if firstLineIdx < 0 {
		return "", false
	}
	answerStartIdx := firstLineIdx
	answerValue := ""
	switch {
	case strings.HasPrefix(firstLine, "FINAL a:"):
		answerValue = strings.TrimSpace(strings.TrimPrefix(firstLine, "FINAL a:"))
		answerStartIdx = firstLineIdx + 1
	case strings.EqualFold(firstLine, "FINAL"):
		next := firstLineIdx + 1
		for ; next < len(lines); next++ {
			trimmed := strings.TrimSpace(lines[next])
			if trimmed == "" {
				continue
			}
			if !strings.HasPrefix(trimmed, "a:") {
				return "", false
			}
			answerValue = strings.TrimSpace(strings.TrimPrefix(trimmed, "a:"))
			answerStartIdx = next + 1
			break
		}
		if answerValue == "" {
			return "", false
		}
	default:
		return "", false
	}

	var answer strings.Builder
	answer.WriteString(answerValue)
	for idx := answerStartIdx; idx < len(lines); idx++ {
		line := lines[idx]
		trimmed := strings.TrimSpace(line)
		if key, _, ok := strings.Cut(trimmed, ":"); ok && isTaggedKey(strings.TrimSpace(key)) {
			break
		}
		if answer.Len() > 0 {
			answer.WriteString("\n")
		}
		answer.WriteString(line)
	}
	if strings.TrimSpace(answer.String()) == "" {
		return "", false
	}
	return answer.String(), true
}

func parseTaggedFinalAnswer(raw string) (string, error) {
	normalized := strings.ReplaceAll(raw, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	lines := strings.Split(normalized, "\n")

	firstLineIdx := -1
	firstLine := ""
	for idx, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		firstLineIdx = idx
		firstLine = trimmed
		break
	}
	if firstLineIdx < 0 {
		return "", fmt.Errorf("invalid final answer format: missing FINAL line")
	}
	answerLineStart := firstLineIdx + 1
	answer := ""
	switch {
	case strings.HasPrefix(firstLine, "FINAL a:"):
		answer = strings.TrimSpace(strings.TrimPrefix(firstLine, "FINAL a:"))
	case strings.EqualFold(firstLine, "FINAL"):
		for idx := firstLineIdx + 1; idx < len(lines); idx++ {
			trimmed := strings.TrimSpace(lines[idx])
			if trimmed == "" {
				continue
			}
			if !strings.HasPrefix(trimmed, "a:") {
				return "", fmt.Errorf("invalid final answer format: expected a: line after FINAL")
			}
			answer = strings.TrimSpace(strings.TrimPrefix(trimmed, "a:"))
			answerLineStart = idx + 1
			break
		}
	default:
		return "", fmt.Errorf("invalid final answer format: first non-empty line must be FINAL or FINAL a:")
	}
	if answer == "" {
		return "", fmt.Errorf("invalid final answer format: missing required a: value")
	}

	for _, line := range lines[answerLineStart:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			return "", fmt.Errorf("invalid final answer format: expected tagged field line key:value")
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "a" {
			return "", fmt.Errorf("invalid final answer format: a: field must appear only once")
		}
		if !isTaggedKey(key) {
			return "", fmt.Errorf("invalid final answer format: invalid field key %q", key)
		}
		_ = value
	}
	return answer, nil
}

func isTaggedKey(key string) bool {
	if key == "" {
		return false
	}
	for _, r := range key {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		return false
	}
	return true
}
