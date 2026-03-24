package tui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"tops/internal/app"
	"tops/internal/chatstore"
	"tops/internal/config"
	"tops/internal/model"
	"tops/internal/obs"
	"tops/internal/ollama"
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
	OllamaManager     ollama.Manager
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
	ollamaManager     ollama.Manager
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
		ollamaManager:     opts.OllamaManager,
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
		sessionID, err := s.store.CreateSession(ctx, s.now())
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

	logger := (*obs.Logger)(nil)
	if runtimePtr != nil {
		logger = runtimePtr.Logger
	}
	if s.ollamaManager == nil {
		s.ollamaManager = ollama.NewManager(ollama.Options{Logger: logger})
	}
	m := newSessionModel(ctx, s, runtimePtr, s.ollamaManager)
	if s.setupOnly {
		m.enterSetupMode("Setup wizard requested.")
	} else if s.startupConfigErr != nil {
		m.enterSetupMode(fmt.Sprintf("Configuration issue detected: %s", s.startupConfigErr.Error()))
	} else if s.forceWizardReason != "" {
		m.enterSetupMode(s.forceWizardReason)
	}

	program := tea.NewProgram(
		m,
		tea.WithContext(ctx),
		tea.WithOutput(out),
		tea.WithoutSignalHandler(),
	)
	if isTTYReader(in) {
		program = tea.NewProgram(
			m,
			tea.WithContext(ctx),
			tea.WithInput(in),
			tea.WithOutput(out),
			tea.WithoutSignalHandler(),
		)
	} else {
		program = tea.NewProgram(
			m,
			tea.WithContext(ctx),
			tea.WithInput(nil),
			tea.WithOutput(out),
			tea.WithoutSignalHandler(),
		)
		go func() {
			time.Sleep(20 * time.Millisecond)
			replayScriptedInput(ctx, in, program)
		}()
	}
	if _, err := program.Run(); err != nil {
		return fmt.Errorf("run bubble tea tui: %w", err)
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

func isTTYReader(in io.Reader) bool {
	file, ok := in.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(file.Fd()))
}

func replayScriptedInput(ctx context.Context, in io.Reader, program *tea.Program) {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 1024), 1024*1024)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		line := scanner.Text()
		for _, r := range line {
			program.Send(keyMsgForRune(r))
		}
		program.Send(tea.KeyMsg{Type: tea.KeyEnter})
	}
}

func keyMsgForRune(r rune) tea.KeyMsg {
	switch r {
	case ' ':
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	case '\t':
		return tea.KeyMsg{Type: tea.KeyTab, Runes: []rune{'\t'}}
	case 0x03:
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case 0x1b:
		return tea.KeyMsg{Type: tea.KeyEsc}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
	}
}
