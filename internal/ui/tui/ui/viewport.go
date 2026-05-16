package ui

import "strings"

// ScrollViewport is a lightweight scrollable viewport for panes.
type ScrollViewport struct {
	Width   int
	Height  int
	YOffset int

	content string
	lines   []string
}

func NewScrollViewport(width, height int) ScrollViewport {
	m := ScrollViewport{Width: width, Height: height}
	m.SetContent("")
	return m
}

func (m *ScrollViewport) SetContent(content string) {
	m.content = content
	m.lines = splitLines(content)
	m.clampOffset()
}

func (m *ScrollViewport) View() string {
	m.clampOffset()
	if m.Height <= 0 || len(m.lines) == 0 {
		return ""
	}
	start := m.YOffset
	if start < 0 {
		start = 0
	}
	end := start + m.Height
	if end > len(m.lines) {
		end = len(m.lines)
	}
	if start >= end {
		return ""
	}
	return strings.Join(m.lines[start:end], "\n")
}

func (m *ScrollViewport) LineUp(n int) {
	if n <= 0 {
		n = 1
	}
	m.YOffset -= n
	m.clampOffset()
}

func (m *ScrollViewport) LineDown(n int) {
	if n <= 0 {
		n = 1
	}
	m.YOffset += n
	m.clampOffset()
}

func (m *ScrollViewport) HalfViewUp() {
	step := m.Height / 2
	if step <= 0 {
		step = 1
	}
	m.LineUp(step)
}

func (m *ScrollViewport) HalfViewDown() {
	step := m.Height / 2
	if step <= 0 {
		step = 1
	}
	m.LineDown(step)
}

func (m *ScrollViewport) GotoTop() {
	m.YOffset = 0
	m.clampOffset()
}

func (m *ScrollViewport) GotoBottom() {
	m.YOffset = max(0, len(m.lines)-max(1, m.Height))
	m.clampOffset()
}

func splitLines(content string) []string {
	if content == "" {
		return []string{""}
	}
	return strings.Split(content, "\n")
}

func (m *ScrollViewport) clampOffset() {
	if m.YOffset < 0 {
		m.YOffset = 0
	}
	maxOffset := max(0, len(m.lines)-max(1, m.Height))
	if m.YOffset > maxOffset {
		m.YOffset = maxOffset
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
