package functions

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"tops/internal/runtime/commandcatalog"
)

type FunctionArgument struct {
	Type        string
	Description string
	Required    bool
	Enum        []string
	ItemsType   string
}

type FunctionDefinition struct {
	Name           string
	Description    string
	SelfSufficient bool
	ArgSchema      map[string]string
	Arguments      map[string]FunctionArgument
	Resolve        func(args map[string]any) (command string, argv []string, expectedEvidence string, outputLineLimit int, err error)
}

type FunctionRegistry interface {
	Get(name string) (FunctionDefinition, bool)
	List() []FunctionDefinition
}

type Options struct{}

type registry struct {
	defs    map[string]FunctionDefinition
	list    []FunctionDefinition
	catalog commandcatalog.Catalog
}

func NewDefaultRegistry() FunctionRegistry {
	return NewRegistry(Options{})
}

func NewRegistry(_ Options) FunctionRegistry {
	r := &registry{
		defs:    make(map[string]FunctionDefinition, 1),
		catalog: commandcatalog.Default(),
	}
	r.registerDefinitions()
	return r
}

func (r *registry) Get(name string) (FunctionDefinition, bool) {
	def, ok := r.defs[strings.TrimSpace(strings.ToLower(name))]
	return def, ok
}

func (r *registry) List() []FunctionDefinition {
	out := make([]FunctionDefinition, len(r.list))
	copy(out, r.list)
	return out
}

func (r *registry) add(def FunctionDefinition) {
	key := strings.TrimSpace(strings.ToLower(def.Name))
	r.defs[key] = def
}

func (r *registry) registerDefinitions() {
	enumValues := r.catalog.Names()
	argSchema := map[string]string{
		"command_name":      "string (required, allowlisted command name)",
		"args":              "array of strings (default [])",
		"output_line_limit": "int (optional, defaults by command)",
	}
	r.add(FunctionDefinition{
		Name:        "run_readonly_command",
		Description: "Execute a validated readonly command from the static catalog.",
		ArgSchema:   argSchema,
		Arguments: map[string]FunctionArgument{
			"command_name": {
				Type:        "string",
				Description: argSchema["command_name"],
				Required:    true,
				Enum:        enumValues,
			},
			"args": {
				Type:        "array",
				ItemsType:   "string",
				Description: argSchema["args"],
			},
			"output_line_limit": {
				Type:        "integer",
				Description: argSchema["output_line_limit"],
			},
		},
		Resolve: func(args map[string]any) (string, []string, string, int, error) {
			if err := rejectUnknownArgs(args, "command_name", "args", "output_line_limit"); err != nil {
				return "", nil, "", 0, err
			}
			commandName, err := getRequiredStringArg(args, "command_name")
			if err != nil {
				return "", nil, "", 0, err
			}
			argv, err := getStringSliceArg(args, "args", []string{})
			if err != nil {
				return "", nil, "", 0, err
			}
			// Some models occasionally place inline args inside command_name
			// (for example "uname -a"). Normalize that form only when args is empty.
			commandName, argv, err = normalizeInlineCommandName(commandName, argv)
			if err != nil {
				return "", nil, "", 0, err
			}
			if entry, ok := r.catalog.Get(commandName); ok && entry.RequireTrailingFlag && len(argv) == 0 {
				if fallback, ok := preferredTrailingFlag(entry); ok {
					argv = append(argv, fallback)
				}
			}
			normalized, entry, validateErr := r.catalog.ValidateAndNormalize(commandName, argv, ".")
			if validateErr != nil {
				return "", nil, "", 0, fmt.Errorf("invalid function arguments for %q: %w", commandName, validateErr)
			}
			outputLineLimit, err := getIntArg(args, "output_line_limit", entry.DefaultOutputLineLimit)
			if err != nil {
				return "", nil, "", 0, err
			}
			if outputLineLimit <= 0 {
				outputLineLimit = entry.DefaultOutputLineLimit
			}
			if outputLineLimit > 1500 {
				outputLineLimit = 1500
			}
			return entry.Name, normalized, fmt.Sprintf("Validated output from %s", entry.Name), outputLineLimit, nil
		},
	})

	r.list = make([]FunctionDefinition, 0, len(r.defs))
	keys := make([]string, 0, len(r.defs))
	for key := range r.defs {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		r.list = append(r.list, r.defs[key])
	}
}

func preferredTrailingFlag(entry commandcatalog.Entry) (string, bool) {
	preferredOrder := []string{"--version", "-V", "-v", "--help", "-h", "version"}
	for _, candidate := range preferredOrder {
		if _, ok := entry.AllowedFlags[candidate]; ok {
			return candidate, true
		}
	}
	for _, prefix := range entry.AllowedFlagPrefixes {
		trimmed := strings.TrimSpace(prefix)
		if trimmed != "" {
			return trimmed, true
		}
	}
	return "", false
}

func rejectUnknownArgs(args map[string]any, allowed ...string) error {
	if len(args) == 0 {
		return nil
	}
	allowedSet := map[string]struct{}{}
	for _, name := range allowed {
		allowedSet[name] = struct{}{}
	}
	for key := range args {
		if _, ok := allowedSet[key]; !ok {
			return fmt.Errorf("invalid function argument: unknown key %q", key)
		}
	}
	return nil
}

func getRequiredStringArg(args map[string]any, key string) (string, error) {
	val, ok := args[key]
	if !ok {
		return "", fmt.Errorf("invalid function argument: %s is required", key)
	}
	str, ok := val.(string)
	if !ok {
		return "", fmt.Errorf("invalid function argument: %s must be a string", key)
	}
	out := strings.TrimSpace(str)
	if out == "" {
		return "", fmt.Errorf("invalid function argument: %s cannot be empty", key)
	}
	if strings.ContainsAny(out, "\n\r") {
		return "", fmt.Errorf("invalid function argument: %s cannot contain newline", key)
	}
	return out, nil
}

func getStringSliceArg(args map[string]any, key string, def []string) ([]string, error) {
	if args == nil {
		return append([]string(nil), def...), nil
	}
	val, ok := args[key]
	if !ok || val == nil {
		return append([]string(nil), def...), nil
	}
	switch typed := val.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			trimmed := strings.TrimSpace(item)
			if strings.ContainsAny(trimmed, "\n\r") {
				return nil, fmt.Errorf("invalid function argument: %s items cannot contain newline", key)
			}
			out = append(out, trimmed)
		}
		return out, nil
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			str, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("invalid function argument: %s must be an array of strings", key)
			}
			trimmed := strings.TrimSpace(str)
			if strings.ContainsAny(trimmed, "\n\r") {
				return nil, fmt.Errorf("invalid function argument: %s items cannot contain newline", key)
			}
			out = append(out, trimmed)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("invalid function argument: %s must be an array of strings", key)
	}
}

func getIntArg(args map[string]any, key string, def int) (int, error) {
	if args == nil {
		return def, nil
	}
	val, ok := args[key]
	if !ok {
		return def, nil
	}
	switch typed := val.(type) {
	case float64:
		if typed != math.Trunc(typed) {
			return 0, fmt.Errorf("invalid function argument: %s must be an integer", key)
		}
		return int(typed), nil
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, fmt.Errorf("invalid function argument: %s must be an integer", key)
		}
		return int(parsed), nil
	case int:
		return typed, nil
	case int64:
		return int(typed), nil
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, fmt.Errorf("invalid function argument: %s must be an integer", key)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("invalid function argument: %s must be an integer", key)
	}
}

func normalizeInlineCommandName(commandName string, args []string) (string, []string, error) {
	fields := strings.Fields(strings.TrimSpace(commandName))
	if len(fields) == 0 {
		return "", nil, fmt.Errorf("invalid function argument: command_name cannot be empty")
	}
	if len(fields) == 1 {
		return fields[0], args, nil
	}
	if len(args) > 0 {
		return "", nil, fmt.Errorf("invalid function argument: command_name must not include inline args when args is provided")
	}
	return fields[0], fields[1:], nil
}
