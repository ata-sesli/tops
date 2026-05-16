package fastlane

import (
	"regexp"
	"strings"

	"tops/internal/model"
	"tops/internal/runtime/platformresolver"
)

type IntentType string

const (
	IntentUnknown          IntentType = "unknown"
	IntentOSInfo           IntentType = "os_info"
	IntentCurrentDirectory IntentType = "current_directory"
	IntentCurrentUser      IntentType = "current_user"
	IntentHostname         IntentType = "hostname"
	IntentDirectoryCount   IntentType = "directory_count"
	IntentFileCount        IntentType = "file_count"
	IntentToolVersion      IntentType = "tool_version"
	IntentDiskUsage        IntentType = "disk_usage"
)

type Intent struct {
	Type            IntentType
	Scope           string
	Visibility      string
	Recursion       string
	RequestedFields []string
	ToolName        string
}

type TemplateCommand struct {
	TemplateID       string
	CommandName      string
	Args             []string
	OutputLineLimit  int
	ExpectedEvidence string
}

var templateResolver = platformresolver.New()

func ExtractIntent(query string) Intent {
	normalized := normalize(query)
	intent := Intent{
		Type:            IntentUnknown,
		Scope:           "current_directory",
		Visibility:      "visible_only",
		Recursion:       "none",
		RequestedFields: []string{},
	}
	if normalized == "" {
		return intent
	}

	if hasAny(normalized, "including hidden", "include hidden", "hidden directories", "hidden files", "dot directories", "dotfiles", "dot files") {
		intent.Visibility = "include_hidden"
	}
	if hasAny(normalized, "recursive", "recursively", "all subdirectories", "all subfolders") {
		intent.Recursion = "recursive"
	}

	if tool := extractVersionTool(normalized); tool != "" {
		intent.Type = IntentToolVersion
		intent.ToolName = tool
		return intent
	}
	if hasAny(normalized, "disk usage", "disk space", "free space", "filesystem usage") {
		intent.Type = IntentDiskUsage
		return intent
	}
	if hasAny(normalized, "hostname", "host name", "machine name") {
		intent.Type = IntentHostname
		return intent
	}
	if hasAny(normalized, "who am i", "current user", "username", "user name", "whoami") {
		intent.Type = IntentCurrentUser
		return intent
	}
	if isCountQuery(normalized) && hasAny(normalized, "files", "file") {
		intent.Type = IntentFileCount
		return intent
	}
	if isCountQuery(normalized) && hasAny(normalized, "directories", "directory", "folders", "folder") {
		intent.Type = IntentDirectoryCount
		return intent
	}
	if hasAny(normalized, "current directory", "current folder", "which folder", "where am i", "pwd") {
		intent.Type = IntentCurrentDirectory
		return intent
	}

	if hasAny(normalized, "operating system", "kernel", " os ", " os", "os ") {
		intent.Type = IntentOSInfo
		fields := make([]string, 0, 3)
		if hasAny(normalized, "operating system", " os ", " os", "os ") {
			fields = append(fields, "os")
		}
		if hasAny(normalized, "kernel") {
			fields = append(fields, "kernel")
		}
		if hasAny(normalized, "version", "release") {
			fields = append(fields, "version")
		}
		if len(fields) == 0 {
			fields = append(fields, "os")
		}
		intent.RequestedFields = dedupe(fields)
		return intent
	}

	return intent
}

func BuildTemplateCommand(intent Intent, platform model.PlatformContext) (TemplateCommand, bool) {
	var key platformresolver.TemplateKey
	switch intent.Type {
	case IntentOSInfo:
		if includesRequestedField(intent.RequestedFields, "kernel") || includesRequestedField(intent.RequestedFields, "version") {
			key = platformresolver.TemplateKernelInfo
		} else {
			key = platformresolver.TemplateOSInfo
		}
	case IntentCurrentDirectory:
		key = platformresolver.TemplatePWD
	case IntentCurrentUser:
		key = platformresolver.TemplateCurrentUser
	case IntentHostname:
		key = platformresolver.TemplateHostname
	case IntentDirectoryCount:
		key = platformresolver.TemplateDirCountCurrentDirectory
	case IntentFileCount:
		key = platformresolver.TemplateFileCountCurrentDirectory
	case IntentToolVersion:
		if !isSupportedVersionTool(intent.ToolName) {
			return TemplateCommand{}, false
		}
		key = platformresolver.TemplateToolVersion
	case IntentDiskUsage:
		key = platformresolver.TemplateDiskUsage
	default:
		return TemplateCommand{}, false
	}

	resolved, err := templateResolver.ResolveTemplate(key, platform, platformresolver.ResolveParams{
		Visibility: intent.Visibility,
		Recursion:  intent.Recursion,
		ToolName:   intent.ToolName,
	})
	if err != nil {
		return TemplateCommand{}, false
	}
	return TemplateCommand{
		TemplateID:       resolved.TemplateID,
		CommandName:      resolved.CommandName,
		Args:             resolved.Args,
		OutputLineLimit:  resolved.OutputLineLimit,
		ExpectedEvidence: resolved.ExpectedEvidence,
	}, true
}

func IsTrivialAskTemplate(templateID string) bool {
	switch strings.TrimSpace(templateID) {
	case "os_info", "kernel_info", "current_directory", "current_user", "hostname", "tool_version", "directory_count", "file_count", "disk_usage":
		return true
	default:
		return false
	}
}

func BuildGenExplanation(intent Intent) string {
	switch intent.Type {
	case IntentOSInfo:
		return "Inspect operating system, kernel, and version."
	case IntentCurrentDirectory:
		return "Print the current working directory."
	case IntentCurrentUser:
		return "Print the current user."
	case IntentHostname:
		return "Print the host name."
	case IntentDirectoryCount:
		return "Count directories using bounded readonly find."
	case IntentFileCount:
		return "Count files using bounded readonly find."
	case IntentToolVersion:
		return "Inspect tool version output."
	case IntentDiskUsage:
		return "Inspect disk usage summary."
	default:
		return "Inspect local system information."
	}
}

func includesRequestedField(fields []string, target string) bool {
	target = strings.ToLower(strings.TrimSpace(target))
	for _, field := range fields {
		if strings.ToLower(strings.TrimSpace(field)) == target {
			return true
		}
	}
	return false
}

func normalize(input string) string {
	input = strings.TrimSpace(strings.ToLower(input))
	if input == "" {
		return ""
	}
	re := regexp.MustCompile(`[^a-z0-9_\-\s]+`)
	cleaned := re.ReplaceAllString(input, " ")
	return strings.Join(strings.Fields(cleaned), " ")
}

func hasAny(text string, needles ...string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	padded := " " + strings.ToLower(strings.TrimSpace(text)) + " "
	for _, needle := range needles {
		n := strings.ToLower(strings.TrimSpace(needle))
		if n == "" {
			continue
		}
		if strings.Contains(padded, " "+n+" ") {
			return true
		}
	}
	return false
}

func isCountQuery(normalized string) bool {
	return hasAny(normalized, "how many", "count")
}

func extractVersionTool(normalized string) string {
	if hasAny(normalized, "operating system", "kernel version", "os version", "system version") {
		return ""
	}
	phrases := []struct {
		regex *regexp.Regexp
		idx   int
	}{
		{regex: regexp.MustCompile(`\bversion of ([a-z0-9._+-]+)\b`), idx: 1},
		{regex: regexp.MustCompile(`\b([a-z0-9._+-]+) version\b`), idx: 1},
	}
	for _, item := range phrases {
		match := item.regex.FindStringSubmatch(normalized)
		if len(match) <= item.idx {
			continue
		}
		tool := strings.TrimSpace(match[item.idx])
		if tool == "" {
			continue
		}
		if tool == "kernel" || tool == "system" || tool == "os" {
			continue
		}
		return tool
	}
	return ""
}

func isSupportedVersionTool(tool string) bool {
	tool = strings.TrimSpace(strings.ToLower(tool))
	if tool == "" {
		return false
	}
	switch tool {
	case "git", "node", "npm", "python", "python3", "go", "tps":
		return true
	default:
		return false
	}
}

func dedupe(items []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		normalized := strings.TrimSpace(strings.ToLower(item))
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}
