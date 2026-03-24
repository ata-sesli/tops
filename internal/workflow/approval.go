package workflow

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

var ErrApprovalUnavailable = errors.New("interactive approval is required to execute workflow steps")

type TerminalPrompter struct {
	in  *bufio.Reader
	out io.Writer
	mu  sync.Mutex
}

func NewTerminalPrompter(in io.Reader, out io.Writer) *TerminalPrompter {
	if in == nil || out == nil {
		return nil
	}
	return &TerminalPrompter{
		in:  bufio.NewReader(in),
		out: out,
	}
}

func (p *TerminalPrompter) ApproveStep(ctx context.Context, step WorkflowStep) (bool, error) {
	if p == nil || p.in == nil || p.out == nil {
		return false, ErrApprovalUnavailable
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	commandLine := strings.TrimSpace(step.CommandName + " " + strings.Join(step.Args, " "))
	if commandLine == "" {
		commandLine = step.CommandName
	}
	for {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		actionClass := ClassifyActionClass(step.RiskLabels)
		_, _ = fmt.Fprintf(p.out, "Approve step %q (%s action): %s ? [y/N]: ", step.ID, actionClass, commandLine)
		input, err := p.in.ReadString('\n')
		if err != nil {
			return false, fmt.Errorf("%w: %v", ErrApprovalUnavailable, err)
		}
		answer := strings.ToLower(strings.TrimSpace(input))
		switch answer {
		case "y", "yes":
			return true, nil
		case "":
			return false, nil
		case "n", "no":
			return false, nil
		default:
			_, _ = fmt.Fprintln(p.out, "Please answer y or n (default is N).")
		}
	}
}

type StaticPrompter struct {
	Approve bool
	Err     error
}

func (p StaticPrompter) ApproveStep(context.Context, WorkflowStep) (bool, error) {
	if p.Err != nil {
		return false, p.Err
	}
	return p.Approve, nil
}
