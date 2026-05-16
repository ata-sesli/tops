package help

import (
	"regexp"
	"strings"
)

type Intent struct {
	Target string
}

var helpIntentPatterns = []struct {
	pattern *regexp.Regexp
}{
	{pattern: regexp.MustCompile(`(?i)^\s*help\s+(.+?)\s*$`)},
	{pattern: regexp.MustCompile(`(?i)^\s*help\s+for\s+(.+?)\s*$`)},
	{pattern: regexp.MustCompile(`(?i)^\s*explain\s+(.+?)\s*$`)},
	{pattern: regexp.MustCompile(`(?i)^\s*show\s+me\s+help\s+for\s+(.+?)\s*$`)},
	{pattern: regexp.MustCompile(`(?i)^\s*how\s+do\s+i\s+use\s+(.+?)\s*$`)},
	{pattern: regexp.MustCompile(`(?i)^\s*what\s+does\s+(.+?)\s+do\??\s*$`)},
}

func DetectIntent(input string) (Intent, bool) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return Intent{}, false
	}
	for _, candidate := range helpIntentPatterns {
		matches := candidate.pattern.FindStringSubmatch(trimmed)
		if len(matches) != 2 {
			continue
		}
		target := sanitizeTargetPhrase(matches[1])
		if target == "" {
			return Intent{}, false
		}
		return Intent{Target: target}, true
	}
	return Intent{}, false
}
