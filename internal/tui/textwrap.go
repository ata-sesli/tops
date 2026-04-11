package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func wrapTextBlock(input string, width int) string {
	if width <= 0 {
		return input
	}
	lines := strings.Split(input, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, wrapLineHard(line, width)...)
	}
	return strings.Join(out, "\n")
}

func wrapLineHard(line string, width int) []string {
	if width <= 0 {
		return []string{line}
	}
	if line == "" {
		return []string{""}
	}
	if lipgloss.Width(line) <= width {
		return []string{line}
	}
	runes := []rune(line)
	var lines []string
	start := 0
	curWidth := 0
	for i, r := range runes {
		w := lipgloss.Width(string(r))
		if w <= 0 {
			w = 1
		}
		if curWidth+w > width && i > start {
			lines = append(lines, string(runes[start:i]))
			start = i
			curWidth = 0
		}
		curWidth += w
	}
	if start < len(runes) {
		lines = append(lines, string(runes[start:]))
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}
