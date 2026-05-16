package render

import (
	"encoding/json"
	"fmt"
	"strings"

	"tops/internal/model"
)

type Renderer struct{}

func New() Renderer { return Renderer{} }

func (Renderer) RenderHelp(result model.HelpResult, format string) (string, error) {
	if format == "json" {
		return marshalJSON(result)
	}
	var b strings.Builder
	writeSection(&b, "Result", result.Target)
	writeSection(&b, "Explanation", result.Summary)
	writeSection(&b, "Usage", result.Syntax)
	writeSection(&b, "Options", joinList(result.ImportantFlags))
	writeSection(&b, "Examples", joinList(result.Examples))
	writeSection(&b, "Notes", joinList(result.Notes))
	writeSection(&b, "Assumptions", joinList(result.Assumptions))
	writeSection(&b, "Risks", joinList(result.Caveats))
	if len(result.Provenance) > 0 {
		items := make([]string, 0, len(result.Provenance))
		for _, src := range result.Provenance {
			if src.Detail != "" {
				items = append(items, fmt.Sprintf("%s (%s)", src.Source, src.Detail))
			} else {
				items = append(items, src.Source)
			}
		}
		writeSection(&b, "Sources", joinList(items))
	}
	return strings.TrimSpace(b.String()), nil
}

func (Renderer) RenderGen(result model.GenResult, format string) (string, error) {
	if format == "json" {
		return marshalJSON(result)
	}
	artifact := strings.TrimSpace(result.Command)
	if artifact == "" {
		return "", nil
	}
	// Keep gen output artifact-first and concise by default.
	if !strings.HasPrefix(artifact, "# workflow blocked") {
		lines := []string{artifact}
		assumptions := nonEmpty(result.Assumptions...)
		if len(assumptions) > 0 {
			lines = append(lines, "", "Assumes: "+assumptions[0])
		}
		ambiguities := nonEmpty(result.Ambiguities...)
		if len(ambiguities) > 0 {
			lines = append(lines, "Note: "+ambiguities[0])
		}
		if hasRiskLabel(result.RiskLabels, "high-risk") {
			lines = append(lines, "Risk: high-risk")
		}
		return strings.Join(lines, "\n"), nil
	}

	// Keep richer details for blocked/error artifact responses.
	notes := make([]string, 0, 5)
	if trimmed := strings.TrimSpace(result.Explanation); trimmed != "" {
		notes = append(notes, "Note: "+trimmed)
	}
	if len(result.Assumptions) > 0 {
		notes = append(notes, "Assumptions: "+strings.Join(nonEmpty(result.Assumptions...), "; "))
	}
	if len(result.Ambiguities) > 0 {
		notes = append(notes, "Ambiguities: "+strings.Join(nonEmpty(result.Ambiguities...), "; "))
	}
	if len(result.RiskLabels) > 0 {
		notes = append(notes, "Risks: "+strings.Join(nonEmpty(result.RiskLabels...), ", "))
	}
	if len(result.ConfidenceNotes) > 0 {
		notes = append(notes, "Confidence: "+strings.Join(nonEmpty(result.ConfidenceNotes...), "; "))
	}
	if len(notes) == 0 {
		return artifact, nil
	}
	return artifact + "\n\n" + strings.Join(notes, "\n"), nil
}

func (Renderer) RenderAsk(result model.AskResult, format string) (string, error) {
	if format == "json" {
		return marshalJSON(result)
	}
	var b strings.Builder
	writeSection(&b, "Result", result.Answer)
	writeSection(&b, "Explanation", joinList(result.Observations))
	writeSection(&b, "Assumptions", joinList(result.Assumptions))
	writeSection(&b, "Risks", joinList(result.Uncertainties))
	writeSection(&b, "Notes", joinList(append(result.Inferences, result.Notes...)))
	return strings.TrimSpace(b.String()), nil
}

func writeSection(b *strings.Builder, title string, body string) {
	if strings.TrimSpace(body) == "" {
		return
	}
	fmt.Fprintf(b, "%s\n%s\n\n", title, body)
}

func marshalJSON(v any) (string, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func joinList(items []string) string {
	cleaned := nonEmpty(items...)
	if len(cleaned) == 0 {
		return ""
	}
	for i := range cleaned {
		cleaned[i] = "- " + cleaned[i]
	}
	return strings.Join(cleaned, "\n")
}

func nonEmpty(items ...string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func hasRiskLabel(labels []string, target string) bool {
	target = strings.TrimSpace(strings.ToLower(target))
	if target == "" {
		return false
	}
	for _, label := range labels {
		if strings.TrimSpace(strings.ToLower(label)) == target {
			return true
		}
	}
	return false
}
