package help

import (
	"fmt"
	"regexp"
	"strings"
)

type Target struct {
	RootCommand string   `json:"root_command"`
	Subcommands []string `json:"subcommands"`
}

func (t Target) Display() string {
	parts := make([]string, 0, 1+len(t.Subcommands))
	if strings.TrimSpace(t.RootCommand) != "" {
		parts = append(parts, t.RootCommand)
	}
	parts = append(parts, t.Subcommands...)
	return strings.Join(parts, " ")
}

var helpTokenPattern = regexp.MustCompile(`^[A-Za-z0-9._:@/+\-]+$`)

const disallowedHelpTokenChars = "|&;<>$`\\"

func ParseTarget(raw string) (Target, error) {
	normalized := sanitizeTargetPhrase(raw)
	if normalized == "" {
		return Target{}, fmt.Errorf("help target is empty")
	}
	tokens := strings.Fields(normalized)
	if len(tokens) == 0 {
		return Target{}, fmt.Errorf("help target is empty")
	}
	for _, token := range tokens {
		if strings.TrimSpace(token) == "" {
			return Target{}, fmt.Errorf("help target token cannot be empty")
		}
		if strings.ContainsAny(token, disallowedHelpTokenChars) {
			return Target{}, fmt.Errorf("invalid help target token %q", token)
		}
		if strings.HasPrefix(token, "-") {
			return Target{}, fmt.Errorf("help target token %q must be a command or subcommand, not an option", token)
		}
		if !helpTokenPattern.MatchString(token) {
			return Target{}, fmt.Errorf("invalid help target token %q", token)
		}
	}
	root := strings.ToLower(tokens[0])
	subs := make([]string, 0, len(tokens)-1)
	for _, token := range tokens[1:] {
		subs = append(subs, strings.ToLower(token))
	}
	return Target{RootCommand: root, Subcommands: subs}, nil
}

func sanitizeTargetPhrase(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	trimmed = strings.TrimSpace(strings.TrimRight(trimmed, "?.!,;:"))
	for {
		if len(trimmed) < 2 {
			break
		}
		first := trimmed[0]
		last := trimmed[len(trimmed)-1]
		if (first == '`' && last == '`') || (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			trimmed = strings.TrimSpace(trimmed[1 : len(trimmed)-1])
			continue
		}
		break
	}
	return strings.TrimSpace(trimmed)
}
