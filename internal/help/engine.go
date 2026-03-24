package help

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"tops/internal/llm"
	"tops/internal/model"
	"tops/internal/parser"
	"tops/internal/progress"
	"tops/internal/prompt"
	"tops/internal/tools"
)

type Engine struct {
	provider llm.LLMProvider
	prompts  prompt.Builder
	parser   parser.Parser
	runner   tools.ToolRunner
	timeout  time.Duration
}

func NewEngine(provider llm.LLMProvider, prompts prompt.Builder, responseParser parser.Parser, runner tools.ToolRunner, timeout time.Duration) Engine {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return Engine{provider: provider, prompts: prompts, parser: responseParser, runner: runner, timeout: timeout}
}

var commandTokenPattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

func (e Engine) Run(ctx context.Context, req model.CoreRequest) (model.HelpResult, error) {
	target := strings.TrimSpace(req.Input)
	if target == "" {
		return model.HelpResult{}, fmt.Errorf("help input is required")
	}
	parts := strings.Fields(target)
	command := parts[0]

	evidence := make([]model.ToolEvidence, 0, 3)
	provenance := make([]model.Provenance, 0, 3)
	progress.UpdatePhase(ctx, "tools")

	if commandTokenPattern.MatchString(command) {
		shell := req.Shell
		if shell == "" {
			shell = "sh"
		}
		builtinCmd := fmt.Sprintf("help %s", command)
		item := e.runEvidence(ctx, shell, []string{"-lc", builtinCmd}, e.timeout)
		item.Command = shell + " -lc \"" + builtinCmd + "\""
		evidence = append(evidence, item)
		if item.Succeeded && item.Stdout != "" {
			provenance = append(provenance, model.Provenance{Source: "shell builtin help", Detail: shell})
		}
	}

	helpItem := e.runEvidence(ctx, command, []string{"--help"}, e.timeout)
	evidence = append(evidence, helpItem)
	if helpItem.Succeeded && helpItem.Stdout != "" {
		provenance = append(provenance, model.Provenance{Source: "command --help", Detail: command})
	}

	manItem := e.runEvidence(ctx, "man", []string{command}, e.timeout)
	evidence = append(evidence, manItem)
	if manItem.Succeeded && manItem.Stdout != "" {
		provenance = append(provenance, model.Provenance{Source: "man", Detail: command})
	}

	systemPrompt, userPrompt := e.prompts.BuildHelpPrompt(req, evidence)
	progress.UpdatePhase(ctx, "provider")
	completion, err := e.provider.Complete(ctx, llm.CompletionRequest{
		SystemPrompt: systemPrompt,
		UserPrompt:   userPrompt,
		Temperature:  0.1,
		MaxTokens:    900,
	})
	if err != nil {
		return model.HelpResult{}, err
	}
	parsed, err := e.parser.ParseHelp(completion.Content, target)
	if err != nil {
		return model.HelpResult{}, err
	}
	parsed.Provenance = provenance
	return parsed, nil
}

func (e Engine) runEvidence(ctx context.Context, name string, args []string, timeout time.Duration) model.ToolEvidence {
	spec := tools.ToolSpec{Name: name, Args: args, Timeout: timeout}
	result, err := e.runner.Run(ctx, spec)
	item := model.ToolEvidence{Command: strings.TrimSpace(name + " " + strings.Join(args, " "))}
	if err != nil {
		item.Stderr = err.Error()
		item.Succeeded = false
		item.ExitCode = result.ExitCode
		item.Stdout = result.Stdout
		item.Duration = result.Duration
		return item
	}
	item.Stdout = result.Stdout
	item.Stderr = result.Stderr
	item.ExitCode = result.ExitCode
	item.Duration = result.Duration
	item.Succeeded = result.ExitCode == 0
	return item
}
