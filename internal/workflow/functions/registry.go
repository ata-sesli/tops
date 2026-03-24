package functions

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

type FunctionDefinition struct {
	Name        string
	Description string
	ArgSchema   map[string]string
	Resolve     func(args map[string]any) (command string, argv []string, expectedEvidence string, outputLineLimit int, err error)
}

type FunctionRegistry interface {
	Get(name string) (FunctionDefinition, bool)
	List() []FunctionDefinition
}

type Options struct {
	GOOS     string
	LookPath func(file string) (string, error)
}

type registry struct {
	defs map[string]FunctionDefinition
	list []FunctionDefinition
	goos string
	look func(file string) (string, error)
}

func NewDefaultRegistry() FunctionRegistry {
	return NewRegistry(Options{})
}

func NewRegistry(opts Options) FunctionRegistry {
	goos := strings.TrimSpace(opts.GOOS)
	if goos == "" {
		goos = runtime.GOOS
	}
	look := opts.LookPath
	if look == nil {
		look = exec.LookPath
	}
	r := &registry{
		defs: make(map[string]FunctionDefinition, 10),
		goos: strings.ToLower(goos),
		look: look,
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
	r.add(FunctionDefinition{
		Name:        "get_os_info",
		Description: "Read local operating system name and kernel release.",
		ArgSchema:   map[string]string{},
		Resolve: func(args map[string]any) (string, []string, string, int, error) {
			if err := rejectUnknownArgs(args); err != nil {
				return "", nil, "", 0, err
			}
			return "uname", []string{"-srm"}, "OS name and kernel release", 0, nil
		},
	})
	r.add(FunctionDefinition{
		Name:        "get_working_directory",
		Description: "Read current working directory.",
		ArgSchema:   map[string]string{},
		Resolve: func(args map[string]any) (string, []string, string, int, error) {
			if err := rejectUnknownArgs(args); err != nil {
				return "", nil, "", 0, err
			}
			return "pwd", []string{}, "Current working directory", 0, nil
		},
	})
	r.add(FunctionDefinition{
		Name:        "list_directory",
		Description: "List directory entries at a path.",
		ArgSchema: map[string]string{
			"path": "string (default \".\")",
			"all":  "bool (default false)",
			"long": "bool (default false)",
		},
		Resolve: func(args map[string]any) (string, []string, string, int, error) {
			if err := rejectUnknownArgs(args, "path", "all", "long"); err != nil {
				return "", nil, "", 0, err
			}
			path, err := getStringArg(args, "path", ".")
			if err != nil {
				return "", nil, "", 0, err
			}
			all, err := getBoolArg(args, "all", false)
			if err != nil {
				return "", nil, "", 0, err
			}
			long, err := getBoolArg(args, "long", false)
			if err != nil {
				return "", nil, "", 0, err
			}
			var argv []string
			switch {
			case long && all:
				argv = []string{"-la", path}
			case long:
				argv = []string{"-l", path}
			case all:
				argv = []string{"-a", path}
			default:
				argv = []string{path}
			}
			return "ls", argv, fmt.Sprintf("Directory listing for %s", path), 0, nil
		},
	})
	r.add(FunctionDefinition{
		Name:        "stat_path",
		Description: "Read file/directory metadata for a path.",
		ArgSchema: map[string]string{
			"path": "string (required)",
		},
		Resolve: func(args map[string]any) (string, []string, string, int, error) {
			if err := rejectUnknownArgs(args, "path"); err != nil {
				return "", nil, "", 0, err
			}
			path, err := getRequiredStringArg(args, "path")
			if err != nil {
				return "", nil, "", 0, err
			}
			return "stat", []string{path}, fmt.Sprintf("Filesystem metadata for %s", path), 0, nil
		},
	})
	r.add(FunctionDefinition{
		Name:        "detect_file_type",
		Description: "Detect file type for a path.",
		ArgSchema: map[string]string{
			"path": "string (required)",
		},
		Resolve: func(args map[string]any) (string, []string, string, int, error) {
			if err := rejectUnknownArgs(args, "path"); err != nil {
				return "", nil, "", 0, err
			}
			path, err := getRequiredStringArg(args, "path")
			if err != nil {
				return "", nil, "", 0, err
			}
			return "file", []string{path}, fmt.Sprintf("File type information for %s", path), 0, nil
		},
	})
	r.add(FunctionDefinition{
		Name:        "resolve_symlink",
		Description: "Resolve symlink target for a path.",
		ArgSchema: map[string]string{
			"path": "string (required)",
		},
		Resolve: func(args map[string]any) (string, []string, string, int, error) {
			if err := rejectUnknownArgs(args, "path"); err != nil {
				return "", nil, "", 0, err
			}
			path, err := getRequiredStringArg(args, "path")
			if err != nil {
				return "", nil, "", 0, err
			}
			return "readlink", []string{path}, fmt.Sprintf("Symlink target for %s", path), 0, nil
		},
	})
	r.add(FunctionDefinition{
		Name:        "get_disk_free",
		Description: "Read free disk space.",
		ArgSchema: map[string]string{
			"human": "bool (default true)",
		},
		Resolve: func(args map[string]any) (string, []string, string, int, error) {
			if err := rejectUnknownArgs(args, "human"); err != nil {
				return "", nil, "", 0, err
			}
			human, err := getBoolArg(args, "human", true)
			if err != nil {
				return "", nil, "", 0, err
			}
			if human {
				return "df", []string{"-h"}, "Disk free and usage summary", 0, nil
			}
			return "df", []string{}, "Disk free and usage summary", 0, nil
		},
	})
	r.add(FunctionDefinition{
		Name:        "get_directory_usage",
		Description: "Read aggregate directory size usage.",
		ArgSchema: map[string]string{
			"path":  "string (default \".\")",
			"human": "bool (default true)",
		},
		Resolve: func(args map[string]any) (string, []string, string, int, error) {
			if err := rejectUnknownArgs(args, "path", "human"); err != nil {
				return "", nil, "", 0, err
			}
			path, err := getStringArg(args, "path", ".")
			if err != nil {
				return "", nil, "", 0, err
			}
			human, err := getBoolArg(args, "human", true)
			if err != nil {
				return "", nil, "", 0, err
			}
			if human {
				return "du", []string{"-sh", path}, fmt.Sprintf("Total directory size for %s", path), 0, nil
			}
			return "du", []string{"-s", path}, fmt.Sprintf("Total directory size for %s", path), 0, nil
		},
	})
	r.add(FunctionDefinition{
		Name:        "list_processes",
		Description: "List processes sorted by memory or cpu usage.",
		ArgSchema: map[string]string{
			"sort_by": "string mem|cpu (default mem)",
			"top_n":   "int (default 20)",
		},
		Resolve: func(args map[string]any) (string, []string, string, int, error) {
			if err := rejectUnknownArgs(args, "sort_by", "top_n"); err != nil {
				return "", nil, "", 0, err
			}
			sortBy, err := getStringArg(args, "sort_by", "mem")
			if err != nil {
				return "", nil, "", 0, err
			}
			sortBy = strings.ToLower(sortBy)
			switch sortBy {
			case "mem", "memory":
				sortBy = "mem"
			case "cpu":
			default:
				return "", nil, "", 0, fmt.Errorf("invalid function argument: sort_by must be mem or cpu")
			}
			topN, err := getIntArg(args, "top_n", 20)
			if err != nil {
				return "", nil, "", 0, err
			}
			if topN < 1 || topN > 500 {
				return "", nil, "", 0, fmt.Errorf("invalid function argument: top_n must be between 1 and 500")
			}
			sortArg := "--sort=-%mem"
			if sortBy == "cpu" {
				sortArg = "--sort=-%cpu"
			}
			return "ps", []string{"-Ao", "pid,comm,%mem,%cpu", sortArg}, "Process list ordered by resource usage", topN + 1, nil
		},
	})
	r.add(FunctionDefinition{
		Name:        "list_listening_ports",
		Description: "List listening TCP ports and owning processes.",
		ArgSchema:   map[string]string{},
		Resolve: func(args map[string]any) (string, []string, string, int, error) {
			if err := rejectUnknownArgs(args); err != nil {
				return "", nil, "", 0, err
			}
			switch r.goos {
			case "darwin":
				return "lsof", []string{"-nP", "-iTCP", "-sTCP:LISTEN"}, "Listening TCP ports and owning process info", 0, nil
			case "linux":
				if _, err := r.look("ss"); err == nil {
					return "ss", []string{"-lntp"}, "Listening TCP ports and owning process info", 0, nil
				}
				return "netstat", []string{"-lnt"}, "Listening TCP ports", 0, nil
			default:
				return "netstat", []string{"-lnt"}, "Listening TCP ports", 0, nil
			}
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
		return "", errors.New("invalid function argument: path cannot contain newline")
	}
	return out, nil
}

func getStringArg(args map[string]any, key string, def string) (string, error) {
	if args == nil {
		return def, nil
	}
	val, ok := args[key]
	if !ok {
		return def, nil
	}
	str, ok := val.(string)
	if !ok {
		return "", fmt.Errorf("invalid function argument: %s must be a string", key)
	}
	out := strings.TrimSpace(str)
	if out == "" {
		return def, nil
	}
	if strings.ContainsAny(out, "\n\r") {
		return "", fmt.Errorf("invalid function argument: %s cannot contain newline", key)
	}
	return out, nil
}

func getBoolArg(args map[string]any, key string, def bool) (bool, error) {
	if args == nil {
		return def, nil
	}
	val, ok := args[key]
	if !ok {
		return def, nil
	}
	typed, ok := val.(bool)
	if !ok {
		return false, fmt.Errorf("invalid function argument: %s must be a boolean", key)
	}
	return typed, nil
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
