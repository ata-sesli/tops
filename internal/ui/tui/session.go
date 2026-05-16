package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	tea "github.com/phoenix-tui/phoenix/tea"
	"golang.org/x/term"

	"tops/internal/app"
	"tops/internal/config"
	"tops/internal/model"
	"tops/internal/storage/chatstore"
)

type Runner interface {
	Run(ctx context.Context, in io.Reader, out io.Writer, rt app.Runtime) error
}

type HistoryEntry struct {
	Timestamp time.Time
	RawInput  string
	Success   bool
	Output    string
}

type ExecuteFunc func(ctx context.Context, rt app.Runtime, mode model.Mode, input string) (string, error)

type RuntimeLoader func(configPath string) (app.Runtime, error)

type SessionOptions struct {
	Store             chatstore.ChatStore
	ConfigPath        string
	RuntimeLoader     RuntimeLoader
	StartupConfigErr  error
	SetupOnly         bool
	ForceWizardReason string
}

type Session struct {
	history []HistoryEntry
	now     func() time.Time
	exec    ExecuteFunc

	store     chatstore.ChatStore
	sessionID int64

	configPath        string
	runtimeLoader     RuntimeLoader
	startupConfigErr  error
	setupOnly         bool
	forceWizardReason string
}

func NewSession(store chatstore.ChatStore) *Session {
	return NewSessionWithOptions(SessionOptions{Store: store})
}

func NewSessionWithOptions(opts SessionOptions) *Session {
	return &Session{
		now:               time.Now,
		exec:              defaultExecute,
		store:             opts.Store,
		configPath:        strings.TrimSpace(opts.ConfigPath),
		runtimeLoader:     opts.RuntimeLoader,
		startupConfigErr:  opts.StartupConfigErr,
		setupOnly:         opts.SetupOnly,
		forceWizardReason: strings.TrimSpace(opts.ForceWizardReason),
	}
}

func defaultExecute(ctx context.Context, rt app.Runtime, mode model.Mode, input string) (string, error) {
	return app.ExecuteMode(ctx, rt, mode, input, app.ExecuteOptions{ForceText: true})
}

func (s *Session) Run(ctx context.Context, in io.Reader, out io.Writer, rt app.Runtime) (runErr error) {
	if in == nil {
		return fmt.Errorf("tui input reader is required")
	}
	if out == nil {
		return fmt.Errorf("tui output writer is required")
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.exec == nil {
		s.exec = defaultExecute
	}

	if s.configPath == "" {
		path, err := config.DefaultPath()
		if err == nil {
			s.configPath = path
		}
	}

	if s.store != nil {
		sessionID, err := s.store.CreateSession(ctx, chatstore.SessionRecord{
			Kind:      chatstore.SessionKindManager,
			Title:     "Manager",
			StartedAt: s.now(),
			UpdatedAt: s.now(),
		})
		if err != nil {
			return fmt.Errorf("initialize chat persistence session: %w", err)
		}
		s.sessionID = sessionID
		defer func() {
			if s.sessionID == 0 {
				return
			}
			if err := s.store.CloseSession(ctx, s.sessionID, s.now()); err != nil && runErr == nil {
				runErr = fmt.Errorf("finalize chat persistence session: %w", err)
			}
		}()
	}

	var runtimePtr *app.Runtime
	if hasRuntime(rt) {
		cloned := rt
		runtimePtr = &cloned
	} else if s.runtimeLoader != nil && s.startupConfigErr == nil && s.configPath != "" {
		loaded, err := s.runtimeLoader(s.configPath)
		if err != nil {
			s.startupConfigErr = err
		} else {
			runtimePtr = &loaded
		}
	}

	m := newSessionModel(ctx, s, runtimePtr)
	if s.setupOnly {
		m.enterSetupMode("Setup wizard requested.")
	} else if s.startupConfigErr != nil {
		m.enterSetupMode(fmt.Sprintf("Configuration issue detected: %s", s.startupConfigErr.Error()))
	} else if s.forceWizardReason != "" {
		m.enterSetupMode(s.forceWizardReason)
	}

	options := []tea.Option[*sessionModel]{
		tea.WithInput[*sessionModel](in),
		tea.WithOutput[*sessionModel](out),
	}
	ttyInput := isTTYReader(in)
	if ttyInput {
		options = append(options, tea.WithAltScreen[*sessionModel]())
	}
	program := tea.New[*sessionModel](&m, options...)
	err := program.Run()
	if err != nil {
		return fmt.Errorf("run phoenix tui: %w", err)
	}
	if m.runtime != nil {
		_ = m.runtime.Close(ctx)
	} else if runtimePtr != nil {
		_ = runtimePtr.Close(ctx)
	}
	return nil
}

func hasRuntime(rt app.Runtime) bool {
	return strings.TrimSpace(string(rt.Config.Provider.Type)) != ""
}

func (s *Session) appendHistory(raw string, success bool, output string) {
	s.history = append(s.history, HistoryEntry{
		Timestamp: s.now(),
		RawInput:  strings.TrimSpace(raw),
		Success:   success,
		Output:    strings.TrimSpace(output),
	})
}

func (s *Session) persist(ctx context.Context, parsed ParseResult, success bool, output string, errorText string, rt *app.Runtime) {
	if s.store == nil || s.sessionID == 0 {
		return
	}
	record := chatstore.MessageRecord{
		SessionID: s.sessionID,
		Timestamp: s.now(),
		Source:    "system",
		RawInput:  strings.TrimSpace(parsed.Raw),
		Kind:      string(parsed.Kind),
		Mode:      string(parsed.Mode),
		Payload:   strings.TrimSpace(parsed.Payload),
		Output:    strings.TrimSpace(output),
		Success:   success,
		ErrorText: strings.TrimSpace(errorText),
	}
	if err := s.store.InsertMessage(ctx, record); err != nil {
		if rt != nil && rt.Logger != nil && rt.Logger.Enabled() {
			rt.Logger.Printf("failed to persist chat message: %s", err)
		}
	}
}

func (s *Session) createChatSession(ctx context.Context, title string) (int64, error) {
	if s.store == nil {
		return 0, fmt.Errorf("chat storage unavailable")
	}
	now := s.now()
	return s.store.CreateSession(ctx, chatstore.SessionRecord{
		Kind:      chatstore.SessionKindChat,
		Title:     strings.TrimSpace(title),
		StartedAt: now,
		UpdatedAt: now,
	})
}

func isTTYReader(in io.Reader) bool {
	type fdReader interface {
		Fd() uintptr
	}
	if file, ok := in.(*os.File); ok {
		return term.IsTerminal(int(file.Fd()))
	}
	if fdr, ok := in.(fdReader); ok {
		return term.IsTerminal(int(fdr.Fd()))
	}
	return false
}
