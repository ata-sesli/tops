package obs

import (
	"io"
	"log"
	"os"
	"strings"
	"sync"
)

type Logger struct {
	enabled bool
	base    *log.Logger
	mu      sync.Mutex
}

func New(enabled bool, out io.Writer) *Logger {
	if out == nil {
		out = os.Stderr
	}
	return &Logger{
		enabled: enabled,
		base:    log.New(out, "[tops debug] ", log.LstdFlags),
	}
}

func (l *Logger) Enabled() bool {
	if l == nil {
		return false
	}
	return l.enabled
}

func (l *Logger) Printf(format string, args ...any) {
	if l == nil || !l.enabled {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.base.Printf(format, args...)
}

func Redact(value string) string {
	redactions := []string{
		"api_key",
		"authorization",
		"bearer ",
		"token",
	}
	lower := strings.ToLower(value)
	for _, marker := range redactions {
		if strings.Contains(lower, marker) {
			return "[redacted]"
		}
	}
	return value
}
