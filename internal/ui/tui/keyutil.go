package tui

import tea "github.com/phoenix-tui/phoenix/tea"

func isShiftTab(msg tea.KeyMsg) bool {
	return msg.Type == tea.KeyTab && msg.Shift
}

func isCtrlRune(msg tea.KeyMsg, r rune) bool {
	if msg.Type != tea.KeyRune || !msg.Ctrl {
		return false
	}
	if msg.Rune == r {
		return true
	}
	if r >= 'a' && r <= 'z' && msg.Rune == r-('a'-'A') {
		return true
	}
	return false
}

func keyRune(msg tea.KeyMsg) (rune, bool) {
	if msg.Type == tea.KeySpace {
		return ' ', true
	}
	if msg.Type != tea.KeyRune || msg.Ctrl || msg.Alt {
		return 0, false
	}
	if msg.Rune == 0 {
		return 0, false
	}
	return msg.Rune, true
}

func isBackspaceLike(msg tea.KeyMsg) bool {
	return msg.Type == tea.KeyBackspace || msg.Type == tea.KeyDelete || isCtrlRune(msg, 'h')
}

func isEnterLike(msg tea.KeyMsg) bool {
	return msg.Type == tea.KeyEnter || isCtrlRune(msg, 'j')
}

func isQuitKey(msg tea.KeyMsg) bool {
	return msg.Type == tea.KeyCtrlC || isCtrlRune(msg, 'c')
}
