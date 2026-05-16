package help

import (
	"fmt"
	"strings"

	"tops/internal/model"
)

type Summarizer struct{}

func NewSummarizer() Summarizer { return Summarizer{} }

func (Summarizer) Summarize(target Target, observation model.CommandObservation) model.HelpResult {
	visible := visibleHelpLines(observation)
	summary := summarizePurpose(target, visible)
	syntax := strings.Join(extractUsageLines(target, visible, 2), " | ")
	flags := extractFlagLines(visible, 8)
	sections := extractSectionNames(visible, 6)

	notes := make([]string, 0, 4)
	if len(sections) > 0 {
		notes = append(notes, "Visible help sections: "+strings.Join(sections, ", "))
	}
	if len(observation.Stdout) == 0 && len(observation.Stderr) > 0 {
		notes = append(notes, "Help text was captured from stderr output.")
	}
	if observation.ExitCode != 0 {
		notes = append(notes, fmt.Sprintf("Command exited with code %d, but help text was still captured.", observation.ExitCode))
	}
	if syntax == "" {
		notes = append(notes, "No explicit usage line was visible in the captured help text.")
	}
	if len(flags) == 0 {
		notes = append(notes, "No option or flag lines were visible in the captured help text.")
	}

	caveats := make([]string, 0, 2)
	if observation.StdoutTruncated || observation.StderrTruncated || !observation.StdoutLineCountExact {
		caveats = append(caveats, "Help output was truncated, so this explanation may miss options or sections.")
	}

	return model.HelpResult{
		Target:         target.Display(),
		Summary:        summary,
		Syntax:         syntax,
		ImportantFlags: flags,
		Examples:       []string{},
		Caveats:        caveats,
		Assumptions:    []string{},
		Notes:          notes,
	}
}

func BuildUnavailableResult(target Target, reason string) model.HelpResult {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "TOPS could not retrieve meaningful built-in help text for this target."
	}
	return model.HelpResult{
		Target:      target.Display(),
		Summary:     reason,
		Caveats:     []string{"No command help text was available, so no grounded explanation could be produced."},
		Assumptions: []string{},
		Notes:       []string{},
		Examples:    []string{},
	}
}

func summarizePurpose(target Target, lines []string) string {
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "usage:") || strings.HasPrefix(lower, "usages:") {
			continue
		}
		if looksLikeSectionHeader(trimmed) {
			continue
		}
		if looksLikeFlagLine(trimmed) {
			continue
		}
		return fmt.Sprintf("The built-in help text describes `%s` as: %s", target.Display(), clampLine(trimmed, 220))
	}
	return fmt.Sprintf("This explanation is based only on the built-in help text for `%s`.", target.Display())
}

func visibleHelpLines(observation model.CommandObservation) []string {
	out := make([]string, 0, len(observation.Stdout)+len(observation.Stderr))
	out = append(out, observation.Stdout...)
	if len(observation.Stderr) > 0 {
		out = append(out, observation.Stderr...)
	}
	return out
}

func extractUsageLines(target Target, lines []string, max int) []string {
	if max <= 0 {
		max = 1
	}
	rootLower := strings.ToLower(target.RootCommand)
	usage := make([]string, 0, max)
	seen := map[string]struct{}{}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		isUsage := strings.HasPrefix(lower, "usage:") || strings.HasPrefix(lower, "usages:")
		if !isUsage && strings.HasPrefix(lower, rootLower+" ") && (strings.Contains(trimmed, "[") || strings.Contains(trimmed, "<")) {
			isUsage = true
		}
		if !isUsage {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		usage = append(usage, clampLine(trimmed, 180))
		if len(usage) >= max {
			break
		}
	}
	return usage
}

func extractFlagLines(lines []string, max int) []string {
	if max <= 0 {
		max = 6
	}
	flags := make([]string, 0, max)
	seen := map[string]struct{}{}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !looksLikeFlagLine(trimmed) {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		flags = append(flags, clampLine(trimmed, 180))
		if len(flags) >= max {
			break
		}
	}
	return flags
}

func extractSectionNames(lines []string, max int) []string {
	if max <= 0 {
		max = 5
	}
	sections := make([]string, 0, max)
	seen := map[string]struct{}{}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !looksLikeSectionHeader(trimmed) {
			continue
		}
		name := strings.TrimSuffix(trimmed, ":")
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		sections = append(sections, name)
		if len(sections) >= max {
			break
		}
	}
	return sections
}

func looksLikeSectionHeader(line string) bool {
	if line == "" || !strings.HasSuffix(line, ":") {
		return false
	}
	if len(line) > 48 {
		return false
	}
	lower := strings.ToLower(line)
	if strings.HasPrefix(lower, "usage:") || strings.HasPrefix(lower, "usages:") {
		return false
	}
	base := strings.TrimSuffix(line, ":")
	for _, ch := range base {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == ' ' || ch == '_' || ch == '-' || ch == '/' {
			continue
		}
		return false
	}
	return strings.TrimSpace(base) != ""
}

func looksLikeFlagLine(line string) bool {
	if line == "" {
		return false
	}
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "--") {
		return true
	}
	if strings.HasPrefix(line, "-") && len(line) >= 2 {
		return true
	}
	return false
}

func clampLine(line string, max int) string {
	line = strings.TrimSpace(line)
	if max <= 0 || len(line) <= max {
		return line
	}
	if max <= 3 {
		return line[:max]
	}
	return line[:max-3] + "..."
}
