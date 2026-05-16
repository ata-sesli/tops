package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"tops/internal/config"
	"tops/internal/runtime/commandcatalog"
	"tops/internal/runtime/policy"
	"tops/internal/runtime/tools"
	"tops/internal/runtime/workflow"
	"tops/internal/storage/commandmemory"
)

func newRunCommand(opts RootOptions, configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Search and execute commands from Command Memory",
		Args:  cobra.ExactArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigOnly(strings.TrimSpace(*configPath))
			if err != nil {
				return configError(err)
			}
			store, err := openCommandMemoryStore()
			if err != nil {
				return fmt.Errorf("command memory unavailable: %w", err)
			}
			defer store.Close()

			cwd, cwdErr := os.Getwd()
			if cwdErr != nil {
				cwd = ""
			}
			projectRoot, _ := commandmemory.DetectProjectContext(cwd)
			picker := runPicker{
				in:          bufio.NewReader(opts.Stdin),
				out:         cmd.OutOrStdout(),
				store:       store,
				cfg:         cfg,
				cwd:         strings.TrimSpace(cwd),
				projectRoot: strings.TrimSpace(projectRoot),
				catalog:     commandcatalog.Default(),
				policy:      policy.NewEngine(),
				runner:      tools.NewRunner(nil),
			}
			return picker.Run(cmd.Context())
		},
	}
	return cmd
}

type runPicker struct {
	in          *bufio.Reader
	out         io.Writer
	store       commandmemory.Store
	cfg         config.Config
	cwd         string
	projectRoot string
	catalog     commandcatalog.Catalog
	policy      policy.Engine
	runner      tools.ToolRunner
}

func (p *runPicker) Run(ctx context.Context) error {
	query := ""
	for {
		items, err := p.store.Search(ctx, commandmemory.SearchOptions{
			Query:       query,
			CWD:         p.cwd,
			ProjectRoot: p.projectRoot,
			Limit:       20,
		})
		if err != nil {
			return fmt.Errorf("search command memory: %w", err)
		}
		if len(items) == 0 && strings.TrimSpace(query) == "" {
			_, _ = fmt.Fprintln(p.out, "No command memory yet. Use `tps gen` to generate commands first.")
			return nil
		}

		renderRunPicker(p.out, query, items)
		_, _ = fmt.Fprint(p.out, "> ")
		line, err := p.in.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("read run picker input: %w", err)
		}
		line = strings.TrimSpace(line)
		if errors.Is(err, io.EOF) && line == "" {
			return nil
		}

		action, parseErr := parseRunAction(line)
		if parseErr != nil {
			_, _ = fmt.Fprintf(p.out, "Invalid selection: %v\n", parseErr)
			if errors.Is(err, io.EOF) {
				return nil
			}
			continue
		}
		switch action.Kind {
		case runActionQuit:
			return nil
		case runActionSearch:
			query = action.Query
		case runActionHide:
			item, ok := itemByIndex(items, action.Index)
			if !ok {
				_, _ = fmt.Fprintf(p.out, "Invalid index: %d\n", action.Index)
				continue
			}
			if hideErr := p.store.Hide(ctx, item.ID); hideErr != nil {
				_, _ = fmt.Fprintf(p.out, "Hide failed: %v\n", hideErr)
				continue
			}
			_, _ = fmt.Fprintf(p.out, "Hidden: %s\n", item.Title)
		case runActionPin:
			item, ok := itemByIndex(items, action.Index)
			if !ok {
				_, _ = fmt.Fprintf(p.out, "Invalid index: %d\n", action.Index)
				continue
			}
			next := !item.Pinned
			if pinErr := p.store.SetPinned(ctx, item.ID, next); pinErr != nil {
				_, _ = fmt.Fprintf(p.out, "Pin update failed: %v\n", pinErr)
				continue
			}
			if next {
				_, _ = fmt.Fprintf(p.out, "Pinned: %s\n", item.Title)
			} else {
				_, _ = fmt.Fprintf(p.out, "Unpinned: %s\n", item.Title)
			}
		case runActionRun:
			item, ok := itemByIndex(items, action.Index)
			if !ok {
				_, _ = fmt.Fprintf(p.out, "Invalid index: %d\n", action.Index)
				continue
			}
			executed, exitCode, success, runErr := p.executeItem(ctx, item)
			if runErr != nil {
				_, _ = fmt.Fprintf(p.out, "Run failed: %v\n", runErr)
			}
			if executed {
				if recordErr := p.store.RecordRun(ctx, item.ID, exitCode, success); recordErr != nil {
					_, _ = fmt.Fprintf(p.out, "Warning: failed to update command memory stats: %v\n", recordErr)
				}
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
		}
	}
}

func (p *runPicker) executeItem(ctx context.Context, item commandmemory.Item) (executed bool, exitCode int, success bool, err error) {
	artifact := strings.TrimSpace(item.ArtifactText())
	if artifact == "" {
		return false, 0, false, fmt.Errorf("selected command memory item has empty artifact")
	}
	_, _ = fmt.Fprintf(p.out, "\nAbout to run:\n%s\n", artifact)

	riskLabels := mergeRiskLabels(item.Risk, p.policy.Classify(artifact))
	actionClass := workflow.ClassifyActionClass(riskLabels)
	execPolicy := workflow.ExecutionPolicy{
		ReadOnly: workflow.ActionPermission(p.cfg.Execution.Permissions.ReadOnly),
		Write:    workflow.ActionPermission(p.cfg.Execution.Permissions.Write),
	}
	permission := execPolicy.PermissionFor(actionClass)
	if permission == workflow.ActionPermissionDisallow {
		return false, 0, false, fmt.Errorf("execution blocked by policy for %s actions", actionClass)
	}

	validated, commandName, normalizedArgs, _ := p.validateForCatalogRun(artifact)
	needsConfirm := shouldConfirmRun(item, artifact, riskLabels, actionClass, permission, validated)
	if needsConfirm {
		approved, confirmErr := promptRunApproval(p.in, p.out, riskLabels)
		if confirmErr != nil {
			return false, 0, false, confirmErr
		}
		if !approved {
			_, _ = fmt.Fprintln(p.out, "Run cancelled.")
			return false, 0, false, nil
		}
	}

	if validated {
		res, runErr := p.runner.Run(ctx, tools.ToolSpec{
			Name:    commandName,
			Args:    normalizedArgs,
			Timeout: runTimeout(item),
		})
		printRunOutput(p.out, res.Stdout, res.Stderr)
		success = runErr == nil && res.ExitCode == 0
		return true, res.ExitCode, success, runErr
	}

	res, runErr := executeShellArtifact(ctx, p.cfg.Shell, artifact, runTimeout(item))
	printRunOutput(p.out, res.Stdout, res.Stderr)
	success = runErr == nil && res.ExitCode == 0
	return true, res.ExitCode, success, runErr
}

func (p *runPicker) validateForCatalogRun(artifact string) (bool, string, []string, error) {
	parsed, ok := parseSimpleCatalogCandidate(artifact)
	if !ok {
		return false, "", nil, fmt.Errorf("artifact is not a simple catalog command")
	}
	if len(parsed.Args) == 0 {
		return false, "", nil, fmt.Errorf("empty command")
	}
	name := parsed.Args[0]
	args := append([]string(nil), parsed.Args[1:]...)
	normalized, _, err := p.catalog.ValidateAndNormalize(name, args, p.cwd)
	if err != nil {
		return false, name, nil, err
	}
	return true, name, normalized, nil
}

type simpleParsedCommand struct {
	Args []string
}

func parseSimpleCatalogCandidate(artifact string) (simpleParsedCommand, bool) {
	trimmed := strings.TrimSpace(artifact)
	if trimmed == "" {
		return simpleParsedCommand{}, false
	}
	if strings.Contains(trimmed, "\n") {
		return simpleParsedCommand{}, false
	}
	if strings.ContainsAny(trimmed, "|&;<>`()$\\") {
		return simpleParsedCommand{}, false
	}
	return simpleParsedCommand{Args: strings.Fields(trimmed)}, true
}

func shouldConfirmRun(item commandmemory.Item, artifact string, riskLabels []string, actionClass workflow.ActionClass, permission workflow.ActionPermission, catalogValidated bool) bool {
	if permission == workflow.ActionPermissionRequest {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(item.OutputKind), "shell_script") {
		return true
	}
	if strings.Contains(strings.TrimSpace(artifact), "\n") {
		return true
	}
	if actionClass != workflow.ActionClassReadOnly {
		return true
	}
	if !catalogValidated {
		return true
	}
	for _, label := range riskLabels {
		switch strings.ToLower(strings.TrimSpace(label)) {
		case "high-risk", "destructive", "irreversible", "privileged", "networked":
			return true
		}
	}
	return false
}

func promptRunApproval(in *bufio.Reader, out io.Writer, riskLabels []string) (bool, error) {
	risk := "unknown"
	if len(riskLabels) > 0 {
		risk = strings.Join(riskLabels, ", ")
	}
	for {
		_, _ = fmt.Fprintf(out, "Risk: %s\nRun? [y/N]: ", risk)
		line, err := in.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return false, err
		}
		answer := strings.ToLower(strings.TrimSpace(line))
		switch answer {
		case "y", "yes":
			return true, nil
		case "", "n", "no":
			return false, nil
		default:
			_, _ = fmt.Fprintln(out, "Please answer y or n (default is N).")
		}
		if errors.Is(err, io.EOF) {
			return false, nil
		}
	}
}

type shellRunResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

func executeShellArtifact(ctx context.Context, shellName string, artifact string, timeout time.Duration) (shellRunResult, error) {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	shellBinary := resolveShellBinary(shellName)
	cmd := exec.CommandContext(runCtx, shellBinary, "-lc", artifact)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	result := shellRunResult{
		Stdout:   text,
		Stderr:   "",
		ExitCode: 0,
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
			return result, nil
		}
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			result.ExitCode = 124
			return result, fmt.Errorf("command timed out after %s", timeout)
		}
		return result, err
	}
	return result, nil
}

func resolveShellBinary(shellName string) string {
	name := strings.TrimSpace(shellName)
	if name == "" {
		name = "sh"
	}
	if path, err := exec.LookPath(name); err == nil && strings.TrimSpace(path) != "" {
		return path
	}
	if path, err := exec.LookPath("sh"); err == nil && strings.TrimSpace(path) != "" {
		return path
	}
	return "/bin/sh"
}

func runTimeout(item commandmemory.Item) time.Duration {
	if strings.EqualFold(strings.TrimSpace(item.OutputKind), "shell_script") {
		return 120 * time.Second
	}
	return 60 * time.Second
}

func printRunOutput(out io.Writer, stdout string, stderr string) {
	stdout = strings.TrimSpace(stdout)
	stderr = strings.TrimSpace(stderr)
	if stdout != "" {
		_, _ = fmt.Fprintln(out, stdout)
	}
	if stderr != "" {
		_, _ = fmt.Fprintf(out, "stderr:\n%s\n", stderr)
	}
}

type runActionKind string

const (
	runActionSearch runActionKind = "search"
	runActionRun    runActionKind = "run"
	runActionHide   runActionKind = "hide"
	runActionPin    runActionKind = "pin"
	runActionQuit   runActionKind = "quit"
)

type runAction struct {
	Kind  runActionKind
	Query string
	Index int
}

func parseRunAction(raw string) (runAction, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return runAction{Kind: runActionSearch, Query: ""}, nil
	}
	lower := strings.ToLower(raw)
	switch lower {
	case "q", "quit", "exit", "esc":
		return runAction{Kind: runActionQuit}, nil
	}
	if strings.HasPrefix(raw, "/") {
		return runAction{Kind: runActionSearch, Query: strings.TrimSpace(strings.TrimPrefix(raw, "/"))}, nil
	}
	if idx, ok := parseIndexedAction(raw, "d"); ok {
		return runAction{Kind: runActionHide, Index: idx}, nil
	}
	if idx, ok := parseIndexedAction(raw, "p"); ok {
		return runAction{Kind: runActionPin, Index: idx}, nil
	}
	if idx, err := strconv.Atoi(raw); err == nil {
		if idx <= 0 {
			return runAction{}, fmt.Errorf("index must be >= 1")
		}
		return runAction{Kind: runActionRun, Index: idx}, nil
	}
	return runAction{Kind: runActionSearch, Query: raw}, nil
}

func parseIndexedAction(raw string, prefix string) (int, bool) {
	raw = strings.TrimSpace(raw)
	lower := strings.ToLower(raw)
	if !strings.HasPrefix(lower, prefix) {
		return 0, false
	}
	value := strings.TrimSpace(raw[len(prefix):])
	value = strings.TrimPrefix(value, " ")
	if value == "" {
		return 0, false
	}
	idx, err := strconv.Atoi(value)
	if err != nil || idx <= 0 {
		return 0, false
	}
	return idx, true
}

func itemByIndex(items []commandmemory.Item, index int) (commandmemory.Item, bool) {
	if index <= 0 || index > len(items) {
		return commandmemory.Item{}, false
	}
	return items[index-1], true
}

func renderRunPicker(out io.Writer, query string, items []commandmemory.Item) {
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintln(out, "Search Command Memory")
	if strings.TrimSpace(query) == "" {
		_, _ = fmt.Fprintln(out, "Query: (all)")
	} else {
		_, _ = fmt.Fprintf(out, "Query: %s\n", strings.TrimSpace(query))
	}
	if len(items) == 0 {
		_, _ = fmt.Fprintln(out, "No matches. Type a new search query or q to quit.")
		_, _ = fmt.Fprintln(out, "Actions: [query] search, /query search, q quit")
		return
	}
	for i, item := range items {
		pin := " "
		if item.Pinned {
			pin = "*"
		}
		preview := runPreview(item.ArtifactText(), 72)
		metadata := fmt.Sprintf("risk=%s uses=%d", valueOrNA(item.Risk), item.UseCount)
		if item.LastUsedAt != nil {
			metadata += " last_used=" + item.LastUsedAt.Local().Format("2006-01-02")
		}
		_, _ = fmt.Fprintf(out, "%2d. [%s] %s\n    %s\n    %s\n", i+1, pin, strings.TrimSpace(item.Title), preview, metadata)
	}
	_, _ = fmt.Fprintln(out, "Actions: [query] search, /query search, <n> run, d<n> hide, p<n> pin/unpin, q quit")
}

func runPreview(raw string, max int) string {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "\n", " ⏎ "))
	if raw == "" {
		return "(empty)"
	}
	if max <= 0 || len(raw) <= max {
		return raw
	}
	return strings.TrimSpace(raw[:max-3]) + "..."
}

func valueOrNA(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "n/a"
	}
	return raw
}

func openCommandMemoryStore() (commandmemory.Store, error) {
	path, err := commandmemory.DefaultPath()
	if err != nil {
		return nil, err
	}
	return commandmemory.OpenSQLite(path, nil)
}

func mergeRiskLabels(storedRisk string, classified []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(classified)+4)
	for _, part := range strings.Split(strings.ToLower(strings.TrimSpace(storedRisk)), ",") {
		label := strings.TrimSpace(part)
		if label == "" {
			continue
		}
		if _, ok := seen[label]; ok {
			continue
		}
		seen[label] = struct{}{}
		out = append(out, label)
	}
	for _, label := range classified {
		norm := strings.ToLower(strings.TrimSpace(label))
		if norm == "" {
			continue
		}
		if _, ok := seen[norm]; ok {
			continue
		}
		seen[norm] = struct{}{}
		out = append(out, norm)
	}
	return out
}
