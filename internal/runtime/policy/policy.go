package policy

import (
	"regexp"
	"strings"
)

type Engine struct{}

func NewEngine() Engine { return Engine{} }

func (Engine) Classify(command string) []string {
	labels := map[string]struct{}{}
	lower := strings.ToLower(strings.TrimSpace(command))
	if lower == "" {
		labels["high-risk"] = struct{}{}
		return toSlice(labels)
	}

	if containsAny(lower, "sudo ") {
		labels["privileged"] = struct{}{}
	}
	if containsAny(lower, "curl ", "wget ", "http://", "https://") {
		labels["networked"] = struct{}{}
	}
	if destructivePattern.MatchString(lower) {
		labels["destructive"] = struct{}{}
		labels["high-risk"] = struct{}{}
	}
	if irreversiblePattern.MatchString(lower) {
		labels["irreversible"] = struct{}{}
		labels["high-risk"] = struct{}{}
	}
	if safeWritePattern.MatchString(lower) {
		labels["safe-write"] = struct{}{}
	}
	if len(labels) == 0 {
		labels["read-only"] = struct{}{}
	}
	return toSlice(labels)
}

var destructivePattern = regexp.MustCompile(`\brm\b|\bdd\b|\bmkfs\b|\bshutdown\b|\breboot\b|apt(-get)?\s+remove|yum\s+remove|dnf\s+remove|\bchmod\s+-r|\bchown\s+-r`)
var irreversiblePattern = regexp.MustCompile(`\brm\s+-[rf]+\b|\bdd\b|\bmkfs\b`)
var safeWritePattern = regexp.MustCompile(`\bmkdir\b|\btouch\b|\bcp\b|\bmv\b|\becho\b`)

func containsAny(value string, patterns ...string) bool {
	for _, p := range patterns {
		if strings.Contains(value, p) {
			return true
		}
	}
	return false
}

func toSlice(set map[string]struct{}) []string {
	order := []string{"read-only", "safe-write", "privileged", "networked", "destructive", "irreversible", "high-risk"}
	out := make([]string, 0, len(set))
	for _, label := range order {
		if _, ok := set[label]; ok {
			out = append(out, label)
		}
	}
	return out
}
