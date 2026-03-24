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
	writeSection(&b, "Notes", joinList(append(nonEmpty(result.Syntax), result.Notes...)))
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
	var b strings.Builder
	writeSection(&b, "Result", result.Command)
	writeSection(&b, "Explanation", result.Explanation)
	writeSection(&b, "Assumptions", joinList(result.Assumptions))
	writeSection(&b, "Risks", joinList(result.RiskLabels))
	writeSection(&b, "Notes", joinList(append(result.Ambiguities, result.ConfidenceNotes...)))
	return strings.TrimSpace(b.String()), nil
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
