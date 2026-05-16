package semantic

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"tops/internal/intel/fastlane"
	"tops/internal/model"
	"tops/internal/ops/benchmetrics"
	"tops/internal/runtime/jsonutil"
	"tops/internal/runtime/llm"
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

func (n Normalizer) Normalize(ctx context.Context, req model.CoreRequest) (model.SemanticIntent, error) {
	return n.NormalizeWithOptions(ctx, req, NormalizeOptions{})
}

func (n Normalizer) NormalizeWithOptions(ctx context.Context, req model.CoreRequest, opts NormalizeOptions) (model.SemanticIntent, error) {
	systemPrompt, userPrompt := n.prompts.BuildSemanticIntentPrompt(req)
	resp, err := n.provider.Complete(progress.WithStreamEmission(ctx, false), llm.CompletionRequest{
		SystemPrompt:    systemPrompt,
		UserPrompt:      userPrompt,
		Temperature:     -1,
		MaxTokens:       256,
		Think:           opts.Think,
		SamplingProfile: llm.SamplingProfilePlanner,
	})
	if err != nil {
		return model.SemanticIntent{}, err
	}
	intent, parseErr := ParseSemanticIntent(resp.Content)
	if parseErr == nil {
		intent.RequestedFields = enrichRequestedFields(intent, req.Input)
		return intent, nil
	}
	if shouldSkipSemanticRepair(req) {
		if fallback, ok := fallbackSemanticIntent(req); ok {
			return fallback, nil
		}
		return model.SemanticIntent{}, parseErr
	}
	if n.opt.RepairMaxRetries < 1 {
		if fallback, ok := fallbackSemanticIntent(req); ok {
			return fallback, nil
		}
		return model.SemanticIntent{}, parseErr
	}
	benchmetrics.IncrementRepair(ctx)
	repairResp, repairErr := n.provider.Complete(progress.WithStreamEmission(ctx, false), llm.CompletionRequest{
		SystemPrompt:    "Return exactly one SemanticIntent JSON object. No markdown. No explanation.",
		UserPrompt:      fmt.Sprintf("Previous SemanticIntent was invalid: %s\nRewrite strictly as valid SemanticIntent JSON only. Invalid output:\n%s", parseErr.Error(), resp.Content),
		Temperature:     -1,
		MaxTokens:       260,
		Think:           opts.Think,
		SamplingProfile: llm.SamplingProfilePlanner,
	})
	if repairErr != nil {
		return model.SemanticIntent{}, parseErr
	}
	intent, parseErr = ParseSemanticIntent(repairResp.Content)
	if parseErr != nil {
		if fallback, ok := fallbackSemanticIntent(req); ok {
			return fallback, nil
		}
		return model.SemanticIntent{}, parseErr
	}
	intent.RequestedFields = enrichRequestedFields(intent, req.Input)
	return intent, nil
}

func ParseSemanticIntent(raw string) (model.SemanticIntent, error) {
	blob, err := extractJSON(raw)
	if err != nil {
		return model.SemanticIntent{}, err
	}
	intent, strictErr := parseSemanticIntentBlob(blob, true)
	if strictErr == nil {
		return intent, nil
	}
	intent, tolerantErr := parseSemanticIntentBlob(blob, false)
	if tolerantErr == nil {
		return intent, nil
	}
	return model.SemanticIntent{}, strictErr
}

func enrichRequestedFields(intent model.SemanticIntent, userInput string) []string {
	fields := normalizeRequestedFields(intent.RequestedFields)
	if len(fields) == 0 {
		fields = normalizeRequestedFields(intent.Projection)
	}

	lower := strings.ToLower(strings.TrimSpace(userInput))
	if lower != "" {
		if strings.Contains(lower, "operating system") || strings.Contains(lower, " os ") || strings.HasPrefix(lower, "os ") || strings.HasSuffix(lower, " os") {
			fields = append(fields, "os")
		}
		if strings.Contains(lower, "kernel") {
			fields = append(fields, "kernel")
		}
		if strings.Contains(lower, "version") || strings.Contains(lower, "release") {
			fields = append(fields, "version")
		}
	}
	if len(fields) == 0 && intent.Entity == "os" {
		fields = append(fields, "os")
	}
	return normalizeRequestedFields(fields)
}

func normalizeRequestedFields(fields []string) []string {
	if len(fields) == 0 {
		return []string{}
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(fields))
	for _, raw := range fields {
		norm := normalizeRequestedField(raw)
		if norm == "" {
			continue
		}
		if _, ok := seen[norm]; ok {
			continue
		}
		seen[norm] = struct{}{}
		out = append(out, norm)
	}
	sort.Strings(out)
	return out
}

func normalizeRequestedField(field string) string {
	f := strings.ToLower(strings.TrimSpace(field))
	if f == "" {
		return ""
	}
	replacer := strings.NewReplacer("-", "_", " ", "_")
	f = replacer.Replace(f)
	for strings.Contains(f, "__") {
		f = strings.ReplaceAll(f, "__", "_")
	}
	f = strings.Trim(f, "_")
	switch f {
	case "operating_system":
		return "os"
	case "kernel_name":
		return "kernel"
	case "kernel_release", "os_version", "kernel_version", "release":
		return "version"
	default:
		return f
	}
}

func isAllowedEnum(value string, allowed ...string) bool {
	value = strings.TrimSpace(value)
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}

func normalizeEnumOrDefault(value string, def string, allowed ...string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return def
	}
	for _, item := range allowed {
		if value == item {
			return value
		}
	}
	return def
}

func extractJSON(raw string) (string, error) {
	blob, err := jsonutil.FirstValidObject(raw)
	if err != nil {
		return "", fmt.Errorf("semantic intent response did not contain valid JSON object")
	}
	return blob, nil
}

func parseSemanticIntentBlob(blob string, strictUnknownKeys bool) (model.SemanticIntent, error) {
	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(blob), &rawFields); err != nil {
		return model.SemanticIntent{}, fmt.Errorf("invalid semantic intent JSON: %w", err)
	}
	fields := canonicalizeSemanticFields(rawFields)
	allowed := map[string]struct{}{
		"version": {}, "operation": {}, "entity": {}, "scope": {}, "target_path": {}, "recursion": {}, "visibility": {},
		"filters": {}, "requested_fields": {}, "projection": {}, "sort": {}, "limit": {}, "requires_grounding": {}, "execution_strategy": {}, "ambiguity_notes": {},
	}
	for key := range fields {
		if _, ok := allowed[key]; ok {
			continue
		}
		if strictUnknownKeys {
			return model.SemanticIntent{}, fmt.Errorf("invalid semantic intent JSON: unknown key %q", key)
		}
		delete(fields, key)
	}

	intent := model.DefaultSemanticIntent()
	if v, ok := fields["version"]; ok {
		if err := json.Unmarshal(v, &intent.Version); err != nil {
			return model.SemanticIntent{}, fmt.Errorf("invalid semantic intent JSON: version must be string")
		}
	}
	if v, ok := fields["operation"]; ok {
		if err := json.Unmarshal(v, &intent.Operation); err != nil {
			return model.SemanticIntent{}, fmt.Errorf("invalid semantic intent JSON: operation must be string")
		}
	}
	if v, ok := fields["entity"]; ok {
		if err := json.Unmarshal(v, &intent.Entity); err != nil {
			return model.SemanticIntent{}, fmt.Errorf("invalid semantic intent JSON: entity must be string")
		}
	}
	if v, ok := fields["scope"]; ok {
		if err := json.Unmarshal(v, &intent.Scope); err != nil {
			return model.SemanticIntent{}, fmt.Errorf("invalid semantic intent JSON: scope must be string")
		}
	}
	if v, ok := fields["target_path"]; ok {
		if err := json.Unmarshal(v, &intent.TargetPath); err != nil {
			return model.SemanticIntent{}, fmt.Errorf("invalid semantic intent JSON: target_path must be string")
		}
	}
	if v, ok := fields["recursion"]; ok {
		if err := json.Unmarshal(v, &intent.Recursion); err != nil {
			return model.SemanticIntent{}, fmt.Errorf("invalid semantic intent JSON: recursion must be string")
		}
	}
	if v, ok := fields["visibility"]; ok {
		if err := json.Unmarshal(v, &intent.Visibility); err != nil {
			return model.SemanticIntent{}, fmt.Errorf("invalid semantic intent JSON: visibility must be string")
		}
	}
	if v, ok := fields["filters"]; ok {
		if err := json.Unmarshal(v, &intent.Filters); err != nil {
			return model.SemanticIntent{}, fmt.Errorf("invalid semantic intent JSON: filters must be object")
		}
	}
	if v, ok := fields["requested_fields"]; ok {
		if err := json.Unmarshal(v, &intent.RequestedFields); err != nil {
			return model.SemanticIntent{}, fmt.Errorf("invalid semantic intent JSON: requested_fields must be array")
		}
	}
	if v, ok := fields["projection"]; ok {
		if err := json.Unmarshal(v, &intent.Projection); err != nil {
			return model.SemanticIntent{}, fmt.Errorf("invalid semantic intent JSON: projection must be array")
		}
	}
	if v, ok := fields["sort"]; ok {
		if err := json.Unmarshal(v, &intent.Sort); err != nil {
			return model.SemanticIntent{}, fmt.Errorf("invalid semantic intent JSON: sort must be string")
		}
	}
	if v, ok := fields["limit"]; ok {
		if err := json.Unmarshal(v, &intent.Limit); err != nil {
			return model.SemanticIntent{}, fmt.Errorf("invalid semantic intent JSON: limit must be integer")
		}
	}
	if v, ok := fields["requires_grounding"]; ok {
		if err := json.Unmarshal(v, &intent.RequiresGrounding); err != nil {
			return model.SemanticIntent{}, fmt.Errorf("invalid semantic intent JSON: requires_grounding must be boolean")
		}
	}
	if v, ok := fields["execution_strategy"]; ok {
		if err := json.Unmarshal(v, &intent.ExecutionStrategy); err != nil {
			return model.SemanticIntent{}, fmt.Errorf("invalid semantic intent JSON: execution_strategy must be string")
		}
	}
	if v, ok := fields["ambiguity_notes"]; ok {
		notes, err := parseStringListLenient(v)
		if err != nil {
			return model.SemanticIntent{}, fmt.Errorf("invalid semantic intent JSON: ambiguity_notes must be array")
		}
		intent.AmbiguityNotes = notes
	}

	intent.Version = normalizeEnumOrDefault(intent.Version, "v1", "v1")
	if !isAllowedEnum(intent.Operation, "count", "list", "inspect", "summarize", "explain", "generate") {
		return model.SemanticIntent{}, fmt.Errorf("invalid semantic intent JSON: unsupported operation %q", intent.Operation)
	}
	if !isAllowedEnum(intent.Entity, "directory", "file", "process", "port", "os", "path", "disk", "custom") {
		return model.SemanticIntent{}, fmt.Errorf("invalid semantic intent JSON: unsupported entity %q", intent.Entity)
	}
	if !isAllowedEnum(intent.Scope, "current_directory", "explicit_path", "system") {
		return model.SemanticIntent{}, fmt.Errorf("invalid semantic intent JSON: unsupported scope %q", intent.Scope)
	}
	if !isAllowedEnum(intent.Recursion, "none", "recursive") {
		return model.SemanticIntent{}, fmt.Errorf("invalid semantic intent JSON: unsupported recursion %q", intent.Recursion)
	}
	if !isAllowedEnum(intent.Visibility, "visible_only", "include_hidden") {
		return model.SemanticIntent{}, fmt.Errorf("invalid semantic intent JSON: unsupported visibility %q", intent.Visibility)
	}
	if !isAllowedEnum(intent.ExecutionStrategy, "direct", "enumerate_then_aggregate") {
		return model.SemanticIntent{}, fmt.Errorf("invalid semantic intent JSON: unsupported execution_strategy %q", intent.ExecutionStrategy)
	}
	if intent.Filters == nil {
		intent.Filters = map[string]any{}
	}
	intent.RequestedFields = normalizeRequestedFields(intent.RequestedFields)
	if len(intent.RequestedFields) == 0 {
		intent.RequestedFields = normalizeRequestedFields(intent.Projection)
	}
	if intent.Projection == nil {
		intent.Projection = []string{}
	}
	if intent.AmbiguityNotes == nil {
		intent.AmbiguityNotes = []string{}
	}
	if strings.TrimSpace(intent.TargetPath) == "" {
		intent.TargetPath = "."
	}
	if intent.Limit < 0 {
		intent.Limit = 0
	}
	if intent.Operation == "count" {
		intent.ExecutionStrategy = "enumerate_then_aggregate"
	}
	return intent, nil
}

func parseStringListLenient(raw json.RawMessage) ([]string, error) {
	items := []string{}
	if len(raw) == 0 {
		return items, nil
	}
	if err := json.Unmarshal(raw, &items); err == nil {
		return items, nil
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		single = strings.TrimSpace(single)
		if single == "" {
			return []string{}, nil
		}
		return []string{single}, nil
	}
	var mixed []any
	if err := json.Unmarshal(raw, &mixed); err == nil {
		out := make([]string, 0, len(mixed))
		for _, item := range mixed {
			text, ok := item.(string)
			if !ok {
				continue
			}
			text = strings.TrimSpace(text)
			if text == "" {
				continue
			}
			out = append(out, text)
		}
		return out, nil
	}
	return nil, fmt.Errorf("invalid string list")
}

func canonicalizeSemanticFields(raw map[string]json.RawMessage) map[string]json.RawMessage {
	aliases := map[string]string{
		"path": "target_path",
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

func fallbackSemanticIntent(req model.CoreRequest) (model.SemanticIntent, bool) {
	fastIntent := fastlane.ExtractIntent(req.Input)
	intent := model.DefaultSemanticIntent()
	intent.TargetPath = "."
	intent.RequiresGrounding = true
	intent.Recursion = fastIntent.Recursion
	intent.Visibility = fastIntent.Visibility
	switch fastIntent.Type {
	case fastlane.IntentOSInfo:
		intent.Operation = "summarize"
		intent.Entity = "os"
		intent.Scope = "system"
		intent.RequestedFields = normalizeRequestedFields(fastIntent.RequestedFields)
		if len(intent.RequestedFields) == 0 {
			intent.RequestedFields = []string{"os"}
		}
		return intent, true
	case fastlane.IntentCurrentDirectory:
		intent.Operation = "inspect"
		intent.Entity = "path"
		intent.Scope = "current_directory"
		intent.RequestedFields = []string{"path"}
		return intent, true
	case fastlane.IntentCurrentUser:
		intent.Operation = "inspect"
		intent.Entity = "custom"
		intent.Scope = "system"
		intent.RequestedFields = []string{"current_user"}
		return intent, true
	case fastlane.IntentHostname:
		intent.Operation = "inspect"
		intent.Entity = "os"
		intent.Scope = "system"
		intent.RequestedFields = []string{"hostname"}
		return intent, true
	case fastlane.IntentFileCount:
		intent.Operation = "count"
		intent.Entity = "file"
		intent.Scope = "current_directory"
		intent.ExecutionStrategy = "enumerate_then_aggregate"
		return intent, true
	case fastlane.IntentDirectoryCount:
		intent.Operation = "count"
		intent.Entity = "directory"
		intent.Scope = "current_directory"
		intent.ExecutionStrategy = "enumerate_then_aggregate"
		return intent, true
	case fastlane.IntentDiskUsage:
		intent.Operation = "inspect"
		intent.Entity = "disk"
		intent.Scope = "system"
		intent.RequestedFields = []string{"disk_usage"}
		return intent, true
	case fastlane.IntentToolVersion:
		intent.Operation = "inspect"
		intent.Entity = "custom"
		intent.Scope = "system"
		intent.RequestedFields = []string{"version"}
		return intent, true
	default:
		return model.SemanticIntent{}, false
	}
}

func shouldSkipSemanticRepair(req model.CoreRequest) bool {
	intent := fastlane.ExtractIntent(req.Input)
	return intent.Type != fastlane.IntentUnknown
}
