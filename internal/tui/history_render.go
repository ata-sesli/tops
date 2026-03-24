package tui

import (
	"fmt"
	"strings"
	"time"

	"tops/internal/chatstore"
)

func renderHistory(history []HistoryEntry) string {
	if len(history) == 0 {
		return "No history yet."
	}
	var b strings.Builder
	for i, entry := range history {
		status := "ok"
		if !entry.Success {
			status = "error"
		}
		fmt.Fprintf(&b, "%d. [%s] %s (%s)", i+1, entry.Timestamp.Format(time.RFC3339), entry.RawInput, status)
		if entry.Output != "" {
			firstLine := entry.Output
			if idx := strings.Index(firstLine, "\n"); idx >= 0 {
				firstLine = firstLine[:idx]
			}
			fmt.Fprintf(&b, " -> %s", firstLine)
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func renderPersistedMessages(messages []chatstore.PersistedMessage) string {
	if len(messages) == 0 {
		return "No persisted messages."
	}
	var b strings.Builder
	for i, message := range messages {
		status := "ok"
		if !message.Success {
			status = "error"
		}
		modeLabel := message.Mode
		if modeLabel == "" {
			modeLabel = "-"
		}
		fmt.Fprintf(&b, "%d. [msg=%d session=%d] %s kind=%s mode=%s (%s)",
			i+1,
			message.ID,
			message.SessionID,
			message.Timestamp.Format(time.RFC3339),
			message.Kind,
			modeLabel,
			status,
		)
		if message.RawInput != "" {
			fmt.Fprintf(&b, " input=%q", message.RawInput)
		}
		if message.Output != "" {
			firstLine := message.Output
			if idx := strings.Index(firstLine, "\n"); idx >= 0 {
				firstLine = firstLine[:idx]
			}
			fmt.Fprintf(&b, " -> %s", firstLine)
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func renderPersistedSessionMessages(sessionID int64, messages []chatstore.PersistedMessage) string {
	if len(messages) == 0 {
		return fmt.Sprintf("No persisted messages for session %d.", sessionID)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Session %d messages:\n", sessionID)
	for i, message := range messages {
		status := "ok"
		if !message.Success {
			status = "error"
		}
		modeLabel := message.Mode
		if modeLabel == "" {
			modeLabel = "-"
		}
		fmt.Fprintf(&b, "%d. [msg=%d] %s kind=%s mode=%s (%s)",
			i+1,
			message.ID,
			message.Timestamp.Format(time.RFC3339),
			message.Kind,
			modeLabel,
			status,
		)
		if message.RawInput != "" {
			fmt.Fprintf(&b, " input=%q", message.RawInput)
		}
		if message.Output != "" {
			firstLine := message.Output
			if idx := strings.Index(firstLine, "\n"); idx >= 0 {
				firstLine = firstLine[:idx]
			}
			fmt.Fprintf(&b, " -> %s", firstLine)
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func renderPersistedSessions(sessions []chatstore.PersistedSession) string {
	if len(sessions) == 0 {
		return "No persisted sessions."
	}
	var b strings.Builder
	for i, session := range sessions {
		end := "open"
		if session.EndedAt != nil {
			end = session.EndedAt.Format(time.RFC3339)
		}
		fmt.Fprintf(&b, "%d. session=%d started=%s ended=%s\n",
			i+1,
			session.ID,
			session.StartedAt.Format(time.RFC3339),
			end,
		)
	}
	return strings.TrimSpace(b.String())
}
