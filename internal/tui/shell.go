package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/creack/pty"

	"tops/internal/obs"
)

type ShellEvent struct {
	Output string
	Err    error
}

type ShellController interface {
	Start(ctx context.Context) error
	Stop() error
	Write(data []byte) error
	Resize(cols int, rows int) error
	Events() <-chan ShellEvent
}

type PTYShellController struct {
	shell  string
	logger *obs.Logger

	mu      sync.Mutex
	cmd     *exec.Cmd
	ptyFile *os.File
	events  chan ShellEvent
	closed  bool
	started bool
	once    sync.Once
}

func NewPTYShellController(shell string, logger *obs.Logger) (ShellController, error) {
	shell = strings.TrimSpace(shell)
	if shell == "" {
		shell = "sh"
	}
	return &PTYShellController{
		shell:  shell,
		logger: logger,
		events: make(chan ShellEvent, 128),
	}, nil
}

func (c *PTYShellController) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return nil
	}
	cmd := exec.CommandContext(ctx, c.shell, "-i")
	f, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("start PTY shell %q: %w", c.shell, err)
	}
	c.cmd = cmd
	c.ptyFile = f
	c.started = true

	go c.readLoop()
	go c.waitLoop()
	return nil
}

func (c *PTYShellController) readLoop() {
	buf := make([]byte, 8192)
	for {
		n, err := c.ptyFile.Read(buf)
		if n > 0 {
			c.emit(ShellEvent{Output: string(buf[:n])})
		}
		if err != nil {
			if err != io.EOF {
				c.emit(ShellEvent{Err: err})
			}
			return
		}
	}
}

func (c *PTYShellController) waitLoop() {
	err := c.cmd.Wait()
	if err != nil {
		c.emit(ShellEvent{Err: err})
	}
	c.closeEvents()
}

func (c *PTYShellController) emit(ev ShellEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	select {
	case c.events <- ev:
	default:
		if c.logger != nil && c.logger.Enabled() {
			c.logger.Printf("shell event dropped due to full channel")
		}
	}
}

func (c *PTYShellController) Events() <-chan ShellEvent {
	return c.events
}

func (c *PTYShellController) Write(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.started || c.ptyFile == nil {
		return fmt.Errorf("shell is not started")
	}
	if len(data) == 0 {
		return nil
	}
	_, err := c.ptyFile.Write(data)
	if err != nil {
		return fmt.Errorf("write to shell PTY: %w", err)
	}
	return nil
}

func (c *PTYShellController) Resize(cols int, rows int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.started || c.ptyFile == nil {
		return nil
	}
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	if err := pty.Setsize(c.ptyFile, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}); err != nil {
		return fmt.Errorf("resize shell PTY: %w", err)
	}
	return nil
}

func (c *PTYShellController) Stop() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	ptyFile := c.ptyFile
	cmd := c.cmd
	c.mu.Unlock()

	if ptyFile != nil {
		_ = ptyFile.Close()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	c.closeEvents()
	return nil
}

func (c *PTYShellController) closeEvents() {
	c.once.Do(func() {
		close(c.events)
	})
}
