package capability

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"tops/internal/model"
	"tops/internal/runtime/workflow"
)

func compileSystemInfo(ctx CompileContext, args map[string]any) (CommandPlan, error) {
	field := stringArg(args, "field", "os")
	switch field {
	case "os":
		if ctx.Platform.OSFamily == "macos" {
			return singleStep("Inspect operating system", "sw_vers", nil, 80, truncateLinesPostprocess(80)), nil
		}
		if ctx.Platform.OSFamily == "linux" {
			return singleStep("Inspect operating system", "cat", []string{"/etc/os-release"}, 120, truncateLinesPostprocess(120)), nil
		}
		return singleStep("Inspect operating system", "uname", []string{"-srm"}, 40, truncateLinesPostprocess(40)), nil
	case "kernel_version", "architecture":
		return singleStep("Inspect kernel and architecture", "uname", []string{"-srm"}, 40, truncateLinesPostprocess(40)), nil
	case "hostname":
		return singleStep("Inspect hostname", "hostname", nil, 20, truncateLinesPostprocess(20)), nil
	case "current_user":
		return singleStep("Inspect current user", "whoami", nil, 20, truncateLinesPostprocess(20)), nil
	default:
		return CommandPlan{}, fmt.Errorf("unsupported system.info field %q", field)
	}
}

func compileFilesystemLocation(_ CompileContext, args map[string]any) (CommandPlan, error) {
	target := stringArg(args, "target", "current_directory")
	switch target {
	case "current_directory":
		return singleStep("Inspect current directory", "pwd", nil, 20, truncateLinesPostprocess(20)), nil
	case "home_directory":
		return CommandPlan{Reason: "Home directory lookup is not command-backed in core v1", Unavailable: "filesystem.location home_directory is unsupported in core v1"}, nil
	default:
		return CommandPlan{}, fmt.Errorf("unsupported filesystem.location target %q", target)
	}
}

func compileFilesystemCount(_ CompileContext, args map[string]any) (CommandPlan, error) {
	entity := stringArg(args, "entity", "file")
	root, rootErr := rootArg(args)
	if rootErr != nil {
		return CommandPlan{}, rootErr
	}
	recursion := stringArg(args, "recursion", "none")
	maxDepth := intArg(args, "max_depth", 1)
	limit := clampInt(intArg(args, "limit", 500), 1, 500)
	findArgs := []string{root, "-mindepth", "1"}
	if recursion == "none" {
		findArgs = append(findArgs, "-maxdepth", "1")
	} else if recursion == "max_depth" && maxDepth > 0 {
		findArgs = append(findArgs, "-maxdepth", strconv.Itoa(maxDepth))
	}
	switch entity {
	case "file":
		findArgs = append(findArgs, "-type", "f")
	case "directory":
		findArgs = append(findArgs, "-type", "d")
	case "both":
	default:
		return CommandPlan{}, fmt.Errorf("unsupported filesystem.count entity %q", entity)
	}
	visibility := stringArg(args, "visibility", "visible_only")
	return singleStep(
		"Count filesystem entries",
		"find",
		findArgs,
		0,
		countLinesPostprocess(entity, visibility, limit),
	), nil
}

func compileDiskUsage(_ CompileContext, args map[string]any) (CommandPlan, error) {
	target := stringArg(args, "target", "current_directory")
	limit := clampInt(intArg(args, "limit", 120), 1, 200)
	switch target {
	case "current_directory":
		return singleStep("Inspect current directory disk usage", "du", []string{"-sh", "."}, limit, truncateLinesPostprocess(limit)), nil
	case "explicit_path":
		return singleStep("Inspect path disk usage", "du", []string{"-sh", stringArg(args, "path", ".")}, limit, truncateLinesPostprocess(limit)), nil
	case "mounted_volumes":
		return singleStep("Inspect filesystem capacity", "df", []string{"-h"}, limit, parseDiskUsagePostprocess("mounted_volumes", limit)), nil
	case "root_volume":
		return singleStep("Inspect root filesystem capacity", "df", []string{"-h"}, limit, parseDiskUsagePostprocess("root_volume", limit)), nil
	default:
		return CommandPlan{}, fmt.Errorf("unsupported disk.usage target %q", target)
	}
}

func compileOpenPorts(ctx CompileContext, args map[string]any) (CommandPlan, error) {
	limit := clampInt(intArg(args, "limit", 50), 1, 100)
	available := ctx.CommandAvailable
	if available == nil {
		available = func(string) bool { return false }
	}
	osFamily := strings.TrimSpace(ctx.Platform.OSFamily)
	if osFamily == "" || osFamily == "unknown" {
		osFamily = normalizeRuntimeOS()
	}
	candidates := []struct {
		name string
		args []string
	}{
		{name: "ss", args: []string{"-lntp"}},
		{name: "netstat", args: []string{"-lnt"}},
	}
	if osFamily == "macos" {
		candidates = []struct {
			name string
			args []string
		}{
			{name: "netstat", args: []string{"-an", "-p", "tcp"}},
			{name: "lsof", args: []string{"-nP", "-iTCP", "-sTCP:LISTEN"}},
		}
	}
	for _, candidate := range candidates {
		if available(candidate.name) {
			return singleStep("Inspect open ports", candidate.name, candidate.args, limit*2, parsePortsPostprocess(limit)), nil
		}
	}
	return CommandPlan{
		Reason:      "Open ports capability unavailable",
		Unavailable: fmt.Sprintf("network.open_ports requires one of the supported port inspection commands on %s", osFamily),
		Postprocess: func(_ []workflow.StepResult) model.ToolEvidence {
			return unavailableEvidence("network.open_ports", fmt.Sprintf("no supported port inspection command found on %s", osFamily))
		},
	}, nil
}

func singleStep(intent, command string, args []string, limit int, post func([]workflow.StepResult) model.ToolEvidence) CommandPlan {
	return CommandPlan{
		Reason: intent,
		Step: workflow.WorkflowStep{
			ID:               "cap-1",
			Intent:           intent,
			CommandName:      command,
			Args:             append([]string(nil), args...),
			ExpectedEvidence: intent,
			OutputLineLimit:  limit,
		},
		Postprocess: post,
	}
}

func countLinesPostprocess(entity string, visibility string, limit int) func([]workflow.StepResult) model.ToolEvidence {
	return func(results []workflow.StepResult) model.ToolEvidence {
		result := lastStepResult(results)
		count, sample := countMatchingLines(result, visibility, clampInt(limit, 1, 50))
		exact := result.StdoutLineCountExact && !result.StdoutTruncated
		if result.StdoutLineCountTotal == 0 && strings.TrimSpace(result.Stdout) != "" && !result.StdoutTruncated {
			exact = true
		}
		payload := map[string]any{
			"postprocess": "count_lines",
			"entity":      entity,
			"visibility":  visibility,
			"count":       count,
			"count_exact": exact,
			"count_text":  fmt.Sprintf("count=%d", count),
			"sample":      sample,
		}
		return jsonEvidence("filesystem.count", payload, firstExitCode(results))
	}
}

func truncateLinesPostprocess(limit int) func([]workflow.StepResult) model.ToolEvidence {
	return func(results []workflow.StepResult) model.ToolEvidence {
		return truncateLinesEvidence(results, limit)
	}
}

func parsePortsPostprocess(limit int) func([]workflow.StepResult) model.ToolEvidence {
	return func(results []workflow.StepResult) model.ToolEvidence {
		rows := parsePortRows(firstStdout(results), limit)
		payload := map[string]any{
			"postprocess": "parse_ports",
			"count":       len(rows),
			"rows":        rows,
		}
		return jsonEvidence("network.open_ports", payload, firstExitCode(results))
	}
}

func parseDiskUsagePostprocess(target string, limit int) func([]workflow.StepResult) model.ToolEvidence {
	return func(results []workflow.StepResult) model.ToolEvidence {
		rows := parseDiskRows(firstStdout(results), target, limit)
		payload := map[string]any{
			"postprocess": "parse_disk_usage",
			"target":      target,
			"count":       len(rows),
			"rows":        rows,
		}
		return jsonEvidence("disk.usage", payload, firstExitCode(results))
	}
}

func countMatchingLines(result workflow.StepResult, visibility string, sampleLimit int) (int, []string) {
	if visibility == "include_hidden" && result.StdoutLineCountExact && !result.StdoutTruncated {
		return result.StdoutNonemptyCount, sampleMatchingLines(result.Stdout, visibility, sampleLimit)
	}
	count := 0
	sample := []string{}
	lines := strings.Split(result.Stdout, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "... (") {
			continue
		}
		base := filepath.Base(line)
		hidden := strings.HasPrefix(base, ".")
		switch visibility {
		case "visible_only":
			if hidden {
				continue
			}
		case "hidden_only":
			if !hidden {
				continue
			}
		}
		count++
		if sampleLimit <= 0 || len(sample) < sampleLimit {
			sample = append(sample, line)
		}
	}
	return count, sample
}

func sampleMatchingLines(stdout string, visibility string, sampleLimit int) []string {
	_, sample := countMatchingLines(workflow.StepResult{Stdout: stdout}, visibility, sampleLimit)
	return sample
}

func truncateLinesEvidence(results []workflow.StepResult, limit int) model.ToolEvidence {
	lines := strings.Split(firstStdout(results), "\n")
	out := []string{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, line)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	payload := map[string]any{
		"postprocess": "truncate_lines",
		"rows":        out,
	}
	return jsonEvidence("capability", payload, firstExitCode(results))
}

func unavailableEvidence(capabilityID string, reason string) model.ToolEvidence {
	payload := map[string]any{
		"status":        "capability_unavailable",
		"capability_id": capabilityID,
		"reason":        reason,
	}
	blob, _ := json.Marshal(payload)
	return model.ToolEvidence{Command: capabilityID, Stdout: string(blob), ExitCode: 0, Succeeded: true}
}

func jsonEvidence(command string, payload map[string]any, exitCode int) model.ToolEvidence {
	blob, _ := json.Marshal(payload)
	return model.ToolEvidence{
		Command:   command,
		Stdout:    string(blob),
		ExitCode:  exitCode,
		Succeeded: exitCode == 0,
	}
}

func parsePortRows(stdout string, limit int) []map[string]string {
	rows := []map[string]string{}
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(strings.ToLower(line), "proto") || strings.Contains(strings.ToLower(line), "command") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		local := ""
		state := ""
		process := ""
		for _, field := range fields {
			if strings.Contains(field, ":") || strings.Contains(field, ".") {
				if portFromAddress(field) != "" {
					local = field
				}
			}
			upper := strings.Trim(strings.ToUpper(field), "()")
			if upper == "LISTEN" || upper == "LISTENING" {
				state = upper
			}
			if strings.Contains(field, "/") || strings.HasPrefix(field, "pid=") {
				process = field
			}
		}
		port := portFromAddress(local)
		if port == "" {
			continue
		}
		if state == "" {
			continue
		}
		rows = append(rows, map[string]string{
			"local_address": local,
			"port":          port,
			"state":         state,
			"process":       process,
		})
		if limit > 0 && len(rows) >= limit {
			break
		}
	}
	return rows
}

func parseDiskRows(stdout string, target string, limit int) []map[string]string {
	rows := []map[string]string{}
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(strings.ToLower(line), "filesystem") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		mount := fields[len(fields)-1]
		if target == "root_volume" && mount != "/" {
			continue
		}
		row := map[string]string{
			"filesystem": fields[0],
			"size":       fields[1],
			"used":       fields[2],
			"available":  fields[3],
			"capacity":   fields[4],
			"mounted_on": mount,
		}
		rows = append(rows, row)
		if limit > 0 && len(rows) >= limit {
			break
		}
	}
	return rows
}

func portFromAddress(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if idx := strings.LastIndex(addr, ":"); idx != -1 && idx+1 < len(addr) {
		port := strings.Trim(addr[idx+1:], ")*")
		if _, err := strconv.Atoi(port); err == nil {
			return port
		}
	}
	if idx := strings.LastIndex(addr, "."); idx != -1 && idx+1 < len(addr) {
		port := strings.Trim(addr[idx+1:], ")*")
		if _, err := strconv.Atoi(port); err == nil {
			return port
		}
	}
	return ""
}

func rootArg(args map[string]any) (string, error) {
	switch stringArg(args, "scope", "current_directory") {
	case "home":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		if strings.TrimSpace(home) == "" {
			return "", fmt.Errorf("resolve home directory: empty path")
		}
		return home, nil
	case "explicit_path":
		return stringArg(args, "path", "."), nil
	default:
		return ".", nil
	}
}

func lastStepResult(results []workflow.StepResult) workflow.StepResult {
	if len(results) == 0 {
		return workflow.StepResult{}
	}
	return results[len(results)-1]
}

func firstStdout(results []workflow.StepResult) string {
	if len(results) == 0 {
		return ""
	}
	return results[len(results)-1].Stdout
}

func firstExitCode(results []workflow.StepResult) int {
	if len(results) == 0 {
		return 0
	}
	return results[len(results)-1].ExitCode
}

func stringArg(args map[string]any, key string, fallback string) string {
	if val, ok := args[key]; ok {
		if s, ok := val.(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return fallback
}

func intArg(args map[string]any, key string, fallback int) int {
	val, ok := args[key]
	if !ok {
		return fallback
	}
	switch v := val.(type) {
	case int:
		return v
	case float64:
		return int(v)
	case json.Number:
		i, err := v.Int64()
		if err == nil {
			return int(i)
		}
	}
	return fallback
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func normalizeRuntimeOS() string {
	switch runtime.GOOS {
	case "darwin":
		return "macos"
	case "linux":
		return "linux"
	default:
		return runtime.GOOS
	}
}

func renderCapability(cap Capability) string {
	args := make([]string, 0, len(cap.Arguments))
	for name, spec := range cap.Arguments {
		item := name + ":" + spec.Type
		if len(spec.Enum) > 0 {
			item += "(" + strings.Join(spec.Enum, "|") + ")"
		}
		args = append(args, item)
	}
	sort.Strings(args)
	return fmt.Sprintf("- %s: %s args=[%s] examples=%q", cap.ID, cap.Description, strings.Join(args, ", "), strings.Join(cap.Examples, "; "))
}

func RenderCapabilities(caps []Capability) string {
	lines := make([]string, 0, len(caps))
	for _, cap := range caps {
		lines = append(lines, renderCapability(cap))
	}
	return strings.Join(lines, "\n")
}
