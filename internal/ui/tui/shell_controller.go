package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"

	"github.com/creack/pty"
	"tops/internal/ui/termutil/ansi"
)

type ShellEvent struct {
	Data string
	Err  error
}

type ShellController interface {
	Start(ctx context.Context, shell string, width int, height int) error
	Write(data []byte) error
	Resize(width int, height int) error
	Events() <-chan ShellEvent
	Close() error
}

type PTYShellController struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	ptmx   *os.File
	events chan ShellEvent
	closed bool
}

func NewPTYShellController() *PTYShellController {
	return &PTYShellController{
		events: make(chan ShellEvent, 128),
	}
}

func ResolveShellCommand(configShell string) ([]string, error) {
	candidates := []string{
		strings.TrimSpace(configShell),
		strings.TrimSpace(os.Getenv("SHELL")),
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		parts := strings.Fields(candidate)
		if len(parts) == 0 {
			continue
		}
		return append([]string{}, parts...), nil
	}
	switch runtime.GOOS {
	case "darwin":
		return []string{"/bin/zsh"}, nil
	default:
		return []string{"/bin/sh"}, nil
	}
}

func (c *PTYShellController) Start(ctx context.Context, shell string, width int, height int) error {
	parts, err := ResolveShellCommand(shell)
	if err != nil {
		return err
	}
	if len(parts) == 0 {
		return fmt.Errorf("shell command cannot be empty")
	}
	args := append([]string{}, parts[1:]...)
	if len(args) == 0 {
		args = append(args, "-i")
	}
	cmd := exec.CommandContext(ctx, parts[0], args...)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("start shell pty: %w", err)
	}
	if width > 0 && height > 0 {
		_ = pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(width), Rows: uint16(height)})
	}

	c.mu.Lock()
	c.cmd = cmd
	c.ptmx = ptmx
	c.closed = false
	c.mu.Unlock()

	go c.readLoop()
	return nil
}

func (c *PTYShellController) readLoop() {
	buf := make([]byte, 4096)
	for {
		c.mu.Lock()
		if c.closed || c.ptmx == nil {
			c.mu.Unlock()
			return
		}
		ptmx := c.ptmx
		c.mu.Unlock()

		n, err := ptmx.Read(buf)
		if n > 0 {
			data := ansi.Strip(string(buf[:n]))
			if strings.TrimSpace(data) != "" || strings.Contains(data, "\n") {
				c.events <- ShellEvent{Data: data}
			}
		}
		if err != nil {
			if err != io.EOF {
				c.events <- ShellEvent{Err: err}
			}
			return
		}
	}
}

func (c *PTYShellController) Write(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.ptmx == nil {
		return fmt.Errorf("shell is not running")
	}
	_, err := c.ptmx.Write(data)
	return err
}

func (c *PTYShellController) Resize(width int, height int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.ptmx == nil || width <= 0 || height <= 0 {
		return nil
	}
	return pty.Setsize(c.ptmx, &pty.Winsize{Cols: uint16(width), Rows: uint16(height)})
}

func (c *PTYShellController) Events() <-chan ShellEvent {
	return c.events
}

func (c *PTYShellController) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	var err error
	if c.ptmx != nil {
		err = c.ptmx.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_, _ = c.cmd.Process.Wait()
	}
	return err
}
