package tui

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

func copyTextToClipboard(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("nothing to copy")
	}
	var attempts [][]string
	switch runtime.GOOS {
	case "darwin":
		attempts = [][]string{{"pbcopy"}}
	default:
		attempts = [][]string{
			{"wl-copy"},
			{"xclip", "-selection", "clipboard"},
			{"xsel", "--clipboard", "--input"},
		}
	}
	var lastErr error
	for _, argv := range attempts {
		if len(argv) == 0 {
			continue
		}
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	if lastErr != nil {
		return fmt.Errorf("clipboard unavailable; install pbcopy/wl-copy/xclip/xsel or use Ctrl+E export (%w)", lastErr)
	}
	return fmt.Errorf("clipboard unavailable")
}
