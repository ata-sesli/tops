package genintent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"tops/internal/model"
	"tops/internal/ops/benchmetrics"
	"tops/internal/runtime/llm"
	"tops/internal/runtime/jsonutil"
	"tops/internal/runtime/optimization"
	"tops/internal/runtime/progress"
	"tops/internal/runtime/prompt"
)

type Normalizer struct {
	provider llm.LLMProvider
	prompts  prompt.Builder
	opt      optimization.Config
}

type NormalizeOptions struct {
	Think *bool
}

func NewNormalizer(provider llm.LLMProvider, prompts prompt.Builder, opts ...optimization.Config) Normalizer {
	opt := optimization.Default()
	if len(opts) > 0 {
		opt = opts[0]
	}
	return Normalizer{provider: provider, prompts: prompts, opt: opt}
}

func (n Normalizer) Normalize(ctx context.Context, req model.CoreRequest) (model.GenIntent, error) {
	return n.NormalizeWithOptions(ctx, req, NormalizeOptions{})
}

func (n Normalizer) NormalizeWithOptions(ctx context.Context, req model.CoreRequest, opts NormalizeOptions) (model.GenIntent, error) {
	systemPrompt, userPrompt := n.prompts.BuildGenIntentPrompt(req)
	resp, err := n.provider.Complete(progress.WithStreamEmission(ctx, false), llm.CompletionRequest{
		SystemPrompt:    systemPrompt,
		UserPrompt:      userPrompt,
		Temperature:     -1,
		MaxTokens:       256,
		Think:           opts.Think,
		SamplingProfile: llm.SamplingProfilePlanner,
	})
	if err != nil {
		return model.GenIntent{}, err
	}
	intent, parseErr := ParseGenIntent(resp.Content)
	if parseErr == nil {
		return enforcePolicy(req, intent), nil
	}
	if repairedRaw, repaired, repairErr := repairOptionalMetadataShape(resp.Content); repairErr == nil && repaired {
		benchmetrics.IncrementRepair(ctx)
		intent, parseErr = ParseGenIntent(repairedRaw)
		if parseErr == nil {
			return enforcePolicy(req, intent), nil
		}
	}
	if n.opt.RepairMaxRetries < 1 {
		fallback := model.DefaultGenIntent()
		fallback.Goal = strings.TrimSpace(req.Input)
		if fallback.Goal != "" {
			return enforcePolicy(req, fallback), nil
		}
		return model.GenIntent{}, parseErr
	}
	benchmetrics.IncrementRepair(ctx)
	repairResp, repairErr := n.provider.Complete(progress.WithStreamEmission(ctx, false), llm.CompletionRequest{
		SystemPrompt:    "Return exactly one GenIntent JSON object. No markdown. No explanation.",
		UserPrompt:      fmt.Sprintf("Previous GenIntent was invalid: %s\nRewrite strictly as valid GenIntent JSON only. Invalid output:\n%s", parseErr.Error(), resp.Content),
		Temperature:     -1,
		MaxTokens:       320,
		Think:           opts.Think,
		SamplingProfile: llm.SamplingProfilePlanner,
	})
	if repairErr != nil {
		return model.GenIntent{}, parseErr
	}
	intent, parseErr = ParseGenIntent(repairResp.Content)
	if parseErr != nil {
		fallback := model.DefaultGenIntent()
		fallback.Goal = strings.TrimSpace(req.Input)
		if fallback.Goal == "" {
			return model.GenIntent{}, parseErr
		}
		return enforcePolicy(req, fallback), nil
	}
	return enforcePolicy(req, intent), nil
}

func ParseGenIntent(raw string) (model.GenIntent, error) {
	blob, err := extractJSON(raw)
	if err != nil {
		return model.GenIntent{}, err
	}
	intent, strictErr := parseGenIntentBlob(blob, true)
	if strictErr == nil {
		return intent, nil
	}
	intent, tolerantErr := parseGenIntentBlob(blob, false)
	if tolerantErr == nil {
		return intent, nil
	}
	return model.GenIntent{}, strictErr
}

func parseOptionalMetadataList(raw json.RawMessage, field string) ([]string, error) {
	if isJSONNull(raw) {
		return []string{}, nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return list, nil
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return []string{single}, nil
	}
	return nil, fmt.Errorf("invalid gen intent JSON: %s must be array, string, or null", field)
}

func repairOptionalMetadataShape(raw string) (string, bool, error) {
	blob, err := extractJSON(raw)
	if err != nil {
		return "", false, err
	}

	var fields map[string]any
	if err := json.Unmarshal([]byte(blob), &fields); err != nil {
		return "", false, err
	}

	optionalKeys := []string{"requested_constraints", "safety_notes", "ambiguity_notes"}
	changed := false
	for _, key := range optionalKeys {
		value, exists := fields[key]
		if !exists {
			continue
		}
		coerced, coercedChanged := coerceOptionalMetadataList(value)
		if !coercedChanged {
			continue
		}
		fields[key] = coerced
		changed = true
	}
	if !changed {
		return "", false, nil
	}

	repaired, err := json.Marshal(fields)
	if err != nil {
		return "", false, err
	}
	return string(repaired), true, nil
}

func coerceOptionalMetadataList(value any) ([]string, bool) {
	switch typed := value.(type) {
	case nil:
		return []string{}, true
	case string:
		return []string{typed}, true
	case []any:
		out := make([]string, 0, len(typed))
		changed := false
		for _, item := range typed {
			switch entry := item.(type) {
			case nil:
				changed = true
			case string:
				out = append(out, entry)
			default:
				out = append(out, stringifyMetadataValue(entry))
				changed = true
			}
		}
		return out, changed
	default:
		return []string{stringifyMetadataValue(typed)}, true
	}
}

func stringifyMetadataValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return strings.TrimSpace(fmt.Sprintf("%v", typed))
	case bool:
		return strings.TrimSpace(fmt.Sprintf("%t", typed))
	default:
		blob, err := json.Marshal(typed)
		if err != nil {
			return strings.TrimSpace(fmt.Sprintf("%v", typed))
		}
		return strings.TrimSpace(string(blob))
	}
}

func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

func enforcePolicy(req model.CoreRequest, intent model.GenIntent) model.GenIntent {
	query := strings.TrimSpace(req.Input)
	if strings.TrimSpace(intent.Goal) == "" {
		intent.Goal = query
	}
	intent.TargetShell = selectTargetShell(query, req, intent.TargetShell)
	intent.OutputKind = selectOutputKind(query, intent.OutputKind)
	intent.RequiresGrounding = computeRequiresGrounding(query, intent)
	intent.NeedsCurrentEnvironmentContext = intent.RequiresGrounding
	return intent
}

func selectTargetShell(query string, req model.CoreRequest, suggested string) string {
	suggested = normalizeEnumOrDefault(suggested, "unknown", "unknown", "bash", "zsh", "sh", "powershell")
	lower := strings.ToLower(strings.TrimSpace(query))
	switch {
	case containsAny(lower, "powershell", "pwsh"):
		return "powershell"
	case containsAny(lower, "bash"):
		return "bash"
	case containsAny(lower, "zsh"):
		return "zsh"
	case containsAny(lower, " shell") && containsAny(lower, "sh"):
		return "sh"
	}
	configured := strings.ToLower(strings.TrimSpace(req.Shell))
	switch configured {
	case "bash", "zsh", "sh", "powershell", "pwsh":
		if configured == "pwsh" {
			return "powershell"
		}
		return configured
	}
	platform := model.NormalizePlatformContext(req.PlatformContext)
	if platform.OSFamily == "windows" {
		return "powershell"
	}
	if suggested != "unknown" {
		return suggested
	}
	return "bash"
}

func selectOutputKind(query string, suggested string) string {
	suggested = normalizeEnumOrDefault(suggested, "single_command", "single_command", "multi_command", "shell_script")
	lower := strings.ToLower(strings.TrimSpace(query))
	if containsAny(lower, "script", "bash script", "shell script", "powershell script") {
		return "shell_script"
	}
	if containsAny(lower, "for each", "loop", "iterate", "while ", "if ", "condition") {
		return "shell_script"
	}
	if containsAny(lower, "then", "after that", "step by step", "multiple commands") {
		if suggested == "shell_script" {
			return "shell_script"
		}
		return "multi_command"
	}
	if suggested == "shell_script" && !containsAny(lower, "script") {
		return "multi_command"
	}
	if suggested == "multi_command" {
		return "multi_command"
	}
	return "single_command"
}

func computeRequiresGrounding(query string, intent model.GenIntent) bool {
	if intent.NeedsCurrentEnvironmentContext {
		return true
	}

	signals := strings.ToLower(strings.TrimSpace(strings.Join([]string{
		query,
		intent.Goal,
		strings.Join(intent.RequestedConstraints, " "),
		strings.Join(intent.AmbiguityNotes, " "),
	}, " ")))
	if hasGroundingSignals(signals) {
		return true
	}

	// Keep grounding strict: a model-suggested true without concrete local-state
	// signals should not trigger execution.
	return false
}

func hasGroundingSignals(lower string) bool {
	if lower == "" {
		return false
	}
	return containsAny(lower,
		"in this directory",
		"in my current directory",
		"in the current directory",
		"current directory contents",
		"current folder contents",
		"this folder",
		"this repo",
		"this repository",
		"this project",
		"my files",
		"local files",
		"existing files",
		"on this machine",
		"on my machine",
		"currently installed",
		"installed version",
		"installed tool",
		"installed tools",
		"tool version installed",
		"available on this machine",
		"available locally",
		"local state",
	)
}

func normalizeStringList(items []string) []string {
	if len(items) == 0 {
		return []string{}
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		norm := strings.TrimSpace(item)
		if norm == "" {
			continue
		}
		key := strings.ToLower(norm)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, norm)
	}
	sort.Strings(out)
	return out
}

func containsAny(value string, parts ...string) bool {
	for _, part := range parts {
		part = strings.ToLower(strings.TrimSpace(part))
		if part == "" {
			continue
		}
		if strings.Contains(value, part) {
			return true
		}
	}
	return false
}

func normalizeEnumOrDefault(value string, fallback string, allowed ...string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	return fallback
}

func extractJSON(raw string) (string, error) {
	blob, err := jsonutil.FirstValidObject(raw)
	if err != nil {
		return "", fmt.Errorf("model response did not contain valid JSON object")
	}
	return blob, nil
}

func parseGenIntentBlob(blob string, strictUnknownKeys bool) (model.GenIntent, error) {
	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(blob), &rawFields); err != nil {
		return model.GenIntent{}, fmt.Errorf("invalid gen intent JSON: %w", err)
	}
	fields := canonicalizeGenIntentFields(rawFields)
	allowed := map[string]struct{}{
		"version": {}, "goal": {}, "output_kind": {}, "target_shell": {}, "platform_scope": {}, "requires_grounding": {},
		"requested_constraints": {}, "safety_notes": {}, "ambiguity_notes": {}, "needs_current_environment_context": {},
	}
	for key := range fields {
		if _, ok := allowed[key]; ok {
			continue
		}
		if strictUnknownKeys {
			return model.GenIntent{}, fmt.Errorf("invalid gen intent JSON: unknown key %q", key)
		}
		delete(fields, key)
	}

	intent := model.DefaultGenIntent()
	if v, ok := fields["version"]; ok {
		if err := json.Unmarshal(v, &intent.Version); err != nil {
			return model.GenIntent{}, fmt.Errorf("invalid gen intent JSON: version must be string")
		}
	}
	if v, ok := fields["goal"]; ok {
		if err := json.Unmarshal(v, &intent.Goal); err != nil {
			return model.GenIntent{}, fmt.Errorf("invalid gen intent JSON: goal must be string")
		}
	}
	if v, ok := fields["output_kind"]; ok {
		if err := json.Unmarshal(v, &intent.OutputKind); err != nil {
			return model.GenIntent{}, fmt.Errorf("invalid gen intent JSON: output_kind must be string")
		}
	}
	if v, ok := fields["target_shell"]; ok {
		if err := json.Unmarshal(v, &intent.TargetShell); err != nil {
			return model.GenIntent{}, fmt.Errorf("invalid gen intent JSON: target_shell must be string")
		}
	}
	if v, ok := fields["platform_scope"]; ok {
		if err := json.Unmarshal(v, &intent.PlatformScope); err != nil {
			return model.GenIntent{}, fmt.Errorf("invalid gen intent JSON: platform_scope must be string")
		}
	}
	if v, ok := fields["requires_grounding"]; ok {
		if err := json.Unmarshal(v, &intent.RequiresGrounding); err != nil {
			return model.GenIntent{}, fmt.Errorf("invalid gen intent JSON: requires_grounding must be boolean")
		}
	}
	if v, ok := fields["requested_constraints"]; ok {
		list, err := parseOptionalMetadataList(v, "requested_constraints")
		if err != nil {
			return model.GenIntent{}, err
		}
		intent.RequestedConstraints = list
	}
	if v, ok := fields["safety_notes"]; ok {
		list, err := parseOptionalMetadataList(v, "safety_notes")
		if err != nil {
			return model.GenIntent{}, err
		}
		intent.SafetyNotes = list
	}
	if v, ok := fields["ambiguity_notes"]; ok {
		list, err := parseOptionalMetadataList(v, "ambiguity_notes")
		if err != nil {
			return model.GenIntent{}, err
		}
		intent.AmbiguityNotes = list
	}
	if v, ok := fields["needs_current_environment_context"]; ok {
		if err := json.Unmarshal(v, &intent.NeedsCurrentEnvironmentContext); err != nil {
			return model.GenIntent{}, fmt.Errorf("invalid gen intent JSON: needs_current_environment_context must be boolean")
		}
	}

	intent.Version = normalizeEnumOrDefault(intent.Version, "v1", "v1")
	intent.Goal = strings.TrimSpace(intent.Goal)
	if intent.Goal == "" {
		return model.GenIntent{}, fmt.Errorf("invalid gen intent JSON: goal cannot be empty")
	}
	intent.OutputKind = normalizeEnumOrDefault(intent.OutputKind, "single_command", "single_command", "multi_command", "shell_script")
	intent.TargetShell = normalizeEnumOrDefault(intent.TargetShell, "unknown", "unknown", "bash", "zsh", "sh", "powershell")
	intent.PlatformScope = normalizeEnumOrDefault(intent.PlatformScope, "unspecified", "unspecified", "current_platform", "portable")
	intent.RequestedConstraints = normalizeStringList(intent.RequestedConstraints)
	intent.SafetyNotes = normalizeStringList(intent.SafetyNotes)
	intent.AmbiguityNotes = normalizeStringList(intent.AmbiguityNotes)

	return intent, nil
}

func canonicalizeGenIntentFields(raw map[string]json.RawMessage) map[string]json.RawMessage {
	aliases := map[string]string{
		"needs_current_environment": "needs_current_environment_context",
	}
	out := make(map[string]json.RawMessage, len(raw))
	for key, value := range raw {
		canonical := normalizeIntentKey(key)
		if mapped, ok := aliases[canonical]; ok {
			canonical = mapped
		}
		if canonical == "" {
			continue
		}
		if _, exists := out[canonical]; exists {
			continue
		}
		out[canonical] = value
	}
	return out
}

func normalizeIntentKey(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	var b strings.Builder
	prevUnderscore := false
	prevLetterOrDigit := false
	for _, r := range value {
		switch {
		case r == '_' || r == '-' || unicode.IsSpace(r) || r == '.':
			if b.Len() > 0 && !prevUnderscore {
				b.WriteByte('_')
				prevUnderscore = true
			}
			prevLetterOrDigit = false
		case unicode.IsUpper(r):
			if b.Len() > 0 && prevLetterOrDigit && !prevUnderscore {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
			prevUnderscore = false
			prevLetterOrDigit = true
		default:
			b.WriteRune(unicode.ToLower(r))
			prevUnderscore = false
			prevLetterOrDigit = unicode.IsLetter(r) || unicode.IsDigit(r)
		}
	}
	out := strings.Trim(b.String(), "_")
	for strings.Contains(out, "__") {
		out = strings.ReplaceAll(out, "__", "_")
	}
	return out
}
