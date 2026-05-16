package ui

import (
	"strings"

	tea "github.com/phoenix-tui/phoenix/tea"
	"tops/internal/ui/tui/render"
)

// InputField is a lightweight text input used by TOPS manager/chat.
type InputField struct {
	Prompt      string
	Placeholder string
	CharLimit   int
	Width       int

	focused bool
	value   string
}

func NewInputField() InputField {
	return InputField{
		Width:   80,
		focused: true,
	}
}

func (m *InputField) Focus() {
	m.focused = true
}

func (m *InputField) Blur() {
	m.focused = false
}

func (m *InputField) SetValue(value string) {
	m.value = enforceLimit(value, m.CharLimit)
}

func (m InputField) Value() string {
	return m.value
}

func isCtrlRune(msg tea.KeyMsg, r rune) bool {
	if msg.Type != tea.KeyRune || !msg.Ctrl {
		return false
	}
	return msg.Rune == r || msg.Rune == unicodeUpper(r)
}

func unicodeUpper(r rune) rune {
	if r >= 'a' && r <= 'z' {
		return r - ('a' - 'A')
	}
	return r
}

func (m InputField) Update(msg tea.KeyMsg) (InputField, tea.Cmd) {
	if !m.focused {
		return m, nil
	}
	switch {
	case msg.Type == tea.KeyBackspace || msg.Type == tea.KeyDelete || isCtrlRune(msg, 'h'):
		r := []rune(m.value)
		if len(r) > 0 {
			m.value = string(r[:len(r)-1])
		}
	case msg.Type == tea.KeySpace:
		m.value = enforceLimit(m.value+" ", m.CharLimit)
	case msg.Type == tea.KeyTab:
		m.value = enforceLimit(m.value+"\t", m.CharLimit)
	case msg.Type == tea.KeyRune && !msg.Ctrl && !msg.Alt && msg.Rune != 0:
		m.value = enforceLimit(m.value+string(msg.Rune), m.CharLimit)
	}
	return m, nil
}

func (m InputField) View() string {
	body := m.value
	switch {
	case m.focused && body == "":
		if strings.TrimSpace(m.Placeholder) != "" {
			body = "|" + m.Placeholder
		} else {
			body = "|"
		}
	case m.focused:
		body = body + "|"
	case body == "":
		body = m.Placeholder
	}
	if m.Width > 0 {
		lineWidth := m.Width - render.Width(m.Prompt)
		if lineWidth < 0 {
			lineWidth = 0
		}
		body = clampToWidth(body, lineWidth)
	}
	return m.Prompt + body
}

func enforceLimit(value string, limit int) string {
	if limit <= 0 {
		return value
	}
	r := []rune(value)
	if len(r) <= limit {
		return value
	}
	return string(r[:limit])
}

func clampToWidth(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if render.Width(value) <= width {
		return value
	}
	var out strings.Builder
	for _, r := range value {
		next := out.String() + string(r)
		if render.Width(next) > width {
			break
		}
		out.WriteRune(r)
	}
	return out.String()
}
