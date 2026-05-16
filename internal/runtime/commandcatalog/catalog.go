package commandcatalog

import (
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Entry struct {
	Name                     string
	Description              string
	SupportedOSFamilies      map[string]struct{}
	AllowedFlags             map[string]struct{}
	AllowedFlagPrefixes      []string
	RequireTrailingFlag      bool
	MaxArgs                  int
	DefaultOutputLineLimit   int
	DefaultTimeout           time.Duration
	MatchCountKnownFromLines bool
}

type Catalog struct {
	entries map[string]Entry
	order   []string
}

func Default() Catalog {
	entries := []Entry{
		{
			Name:                   "uname",
			Description:            "Read OS information.",
			SupportedOSFamilies:    osSet("macos", "linux"),
			AllowedFlags:           set("-srm", "-a"),
			MaxArgs:                1,
			DefaultOutputLineLimit: 40,
			DefaultTimeout:         3 * time.Second,
		},
		{
			Name:                   "whoami",
			Description:            "Read current user identity.",
			AllowedFlags:           set(),
			MaxArgs:                0,
			DefaultOutputLineLimit: 20,
			DefaultTimeout:         3 * time.Second,
		},
		{
			Name:                   "hostname",
			Description:            "Read host name.",
			AllowedFlags:           set(),
			MaxArgs:                0,
			DefaultOutputLineLimit: 20,
			DefaultTimeout:         3 * time.Second,
		},
		{
			Name:                   "pwd",
			Description:            "Read current working directory.",
			SupportedOSFamilies:    osSet("macos", "linux"),
			AllowedFlags:           set(),
			MaxArgs:                0,
			DefaultOutputLineLimit: 20,
			DefaultTimeout:         3 * time.Second,
		},
		{
			Name:                     "ls",
			Description:              "List directory entries.",
			AllowedFlags:             set("-a", "-l", "-la", "-al"),
			MaxArgs:                  3,
			DefaultOutputLineLimit:   240,
			DefaultTimeout:           8 * time.Second,
			MatchCountKnownFromLines: true,
		},
		{
			Name:                   "stat",
			Description:            "Read filesystem metadata for a path.",
			AllowedFlags:           set(),
			MaxArgs:                1,
			DefaultOutputLineLimit: 120,
			DefaultTimeout:         6 * time.Second,
		},
		{
			Name:                   "file",
			Description:            "Detect file type information.",
			AllowedFlags:           set(),
			MaxArgs:                1,
			DefaultOutputLineLimit: 80,
			DefaultTimeout:         6 * time.Second,
		},
		{
			Name:                   "readlink",
			Description:            "Resolve symlink targets.",
			AllowedFlags:           set(),
			MaxArgs:                1,
			DefaultOutputLineLimit: 40,
			DefaultTimeout:         6 * time.Second,
		},
		{
			Name:                   "df",
			Description:            "Read filesystem capacity and usage.",
			AllowedFlags:           set("-h"),
			MaxArgs:                1,
			DefaultOutputLineLimit: 120,
			DefaultTimeout:         6 * time.Second,
		},
		{
			Name:                   "du",
			Description:            "Read directory size usage.",
			AllowedFlags:           set("-s", "-sh", "--help", "-h"),
			MaxArgs:                2,
			DefaultOutputLineLimit: 120,
			DefaultTimeout:         10 * time.Second,
		},
		{
			Name:                   "ps",
			Description:            "Inspect running processes.",
			AllowedFlags:           set("-Ao", "--sort=-%mem", "--sort=-%cpu"),
			MaxArgs:                3,
			DefaultOutputLineLimit: 260,
			DefaultTimeout:         6 * time.Second,
		},
		{
			Name:                   "lsof",
			Description:            "Inspect listening TCP ports on macOS.",
			SupportedOSFamilies:    osSet("macos"),
			AllowedFlags:           set("-nP", "-iTCP", "-sTCP:LISTEN"),
			MaxArgs:                3,
			DefaultOutputLineLimit: 260,
			DefaultTimeout:         6 * time.Second,
		},
		{
			Name:                   "ss",
			Description:            "Inspect listening sockets on Linux.",
			SupportedOSFamilies:    osSet("linux"),
			AllowedFlags:           set("-lntp"),
			MaxArgs:                1,
			DefaultOutputLineLimit: 260,
			DefaultTimeout:         6 * time.Second,
		},
		{
			Name:                   "netstat",
			Description:            "Inspect listening sockets.",
			AllowedFlags:           set("-lnt", "-an", "-p"),
			MaxArgs:                3,
			DefaultOutputLineLimit: 260,
			DefaultTimeout:         6 * time.Second,
		},
		{
			Name:                     "find",
			Description:              "Enumerate filesystem entries under a constrained depth.",
			AllowedFlags:             set("-mindepth", "-maxdepth", "-type", "-name"),
			MaxArgs:                  16,
			DefaultOutputLineLimit:   500,
			DefaultTimeout:           12 * time.Second,
			MatchCountKnownFromLines: true,
		},
		{
			Name:                   "man",
			Description:            "Read manual pages.",
			AllowedFlags:           set(),
			MaxArgs:                1,
			DefaultOutputLineLimit: 240,
			DefaultTimeout:         12 * time.Second,
		},
		{
			Name:                   "git",
			Description:            "Read git help or version metadata.",
			AllowedFlags:           set("--version", "--help", "-h"),
			RequireTrailingFlag:    true,
			MaxArgs:                8,
			DefaultOutputLineLimit: 320,
			DefaultTimeout:         8 * time.Second,
		},
		{
			Name:                   "docker",
			Description:            "Read Docker help or version metadata.",
			AllowedFlags:           set("--version", "--help", "-h"),
			RequireTrailingFlag:    true,
			MaxArgs:                12,
			DefaultOutputLineLimit: 320,
			DefaultTimeout:         8 * time.Second,
		},
		{
			Name:                   "kubectl",
			Description:            "Read kubectl help metadata.",
			AllowedFlags:           set("--help", "-h"),
			RequireTrailingFlag:    true,
			MaxArgs:                12,
			DefaultOutputLineLimit: 320,
			DefaultTimeout:         8 * time.Second,
		},
		{
			Name:                   "ffmpeg",
			Description:            "Read ffmpeg help or version metadata.",
			AllowedFlags:           set("--help", "-h", "--version", "-version"),
			RequireTrailingFlag:    true,
			MaxArgs:                8,
			DefaultOutputLineLimit: 320,
			DefaultTimeout:         8 * time.Second,
		},
		{
			Name:                   "tar",
			Description:            "Read tar help or version metadata.",
			AllowedFlags:           set("--help", "--version"),
			RequireTrailingFlag:    true,
			MaxArgs:                8,
			DefaultOutputLineLimit: 280,
			DefaultTimeout:         6 * time.Second,
		},
		{
			Name:                   "cargo",
			Description:            "Read cargo help or version metadata.",
			AllowedFlags:           set("--help", "-h", "--version"),
			RequireTrailingFlag:    true,
			MaxArgs:                10,
			DefaultOutputLineLimit: 320,
			DefaultTimeout:         8 * time.Second,
		},
		{
			Name:                   "node",
			Description:            "Read Node.js version metadata.",
			AllowedFlags:           set("--version"),
			RequireTrailingFlag:    true,
			MaxArgs:                1,
			DefaultOutputLineLimit: 40,
			DefaultTimeout:         4 * time.Second,
		},
		{
			Name:                   "npm",
			Description:            "Read npm version metadata.",
			AllowedFlags:           set("--version"),
			RequireTrailingFlag:    true,
			MaxArgs:                1,
			DefaultOutputLineLimit: 40,
			DefaultTimeout:         4 * time.Second,
		},
		{
			Name:                   "python",
			Description:            "Read Python version metadata.",
			AllowedFlags:           set("--version"),
			RequireTrailingFlag:    true,
			MaxArgs:                1,
			DefaultOutputLineLimit: 40,
			DefaultTimeout:         4 * time.Second,
		},
		{
			Name:                   "python3",
			Description:            "Read Python 3 version metadata.",
			AllowedFlags:           set("--version"),
			RequireTrailingFlag:    true,
			MaxArgs:                1,
			DefaultOutputLineLimit: 40,
			DefaultTimeout:         4 * time.Second,
		},
		{
			Name:                   "uv",
			Description:            "Read uv help or version metadata.",
			AllowedFlags:           set("--help", "-h", "--version", "help"),
			RequireTrailingFlag:    true,
			MaxArgs:                8,
			DefaultOutputLineLimit: 220,
			DefaultTimeout:         5 * time.Second,
		},
		{
			Name:                   "go",
			Description:            "Read Go version metadata.",
			AllowedFlags:           set("version"),
			RequireTrailingFlag:    true,
			MaxArgs:                1,
			DefaultOutputLineLimit: 40,
			DefaultTimeout:         4 * time.Second,
		},
		{
			Name:                   "tps",
			Description:            "Read TPS version metadata.",
			AllowedFlags:           set("--version"),
			RequireTrailingFlag:    true,
			MaxArgs:                1,
			DefaultOutputLineLimit: 40,
			DefaultTimeout:         4 * time.Second,
		},
		{
			Name:                   "head",
			Description:            "Read first lines of a file.",
			AllowedFlags:           set("-n"),
			AllowedFlagPrefixes:    []string{"-n"},
			MaxArgs:                3,
			DefaultOutputLineLimit: 120,
			DefaultTimeout:         6 * time.Second,
		},
		{
			Name:                   "col",
			Description:            "Filter reverse line feeds from text output.",
			SupportedOSFamilies:    osSet("macos", "linux"),
			AllowedFlags:           set("-b"),
			MaxArgs:                1,
			DefaultOutputLineLimit: 120,
			DefaultTimeout:         6 * time.Second,
		},
		{
			Name:                   "sw_vers",
			Description:            "Read macOS version metadata.",
			SupportedOSFamilies:    osSet("macos"),
			AllowedFlags:           set(),
			MaxArgs:                0,
			DefaultOutputLineLimit: 80,
			DefaultTimeout:         3 * time.Second,
		},
		{
			Name:                   "cat",
			Description:            "Read plaintext files such as /etc/os-release.",
			SupportedOSFamilies:    osSet("linux", "macos"),
			AllowedFlags:           set(),
			MaxArgs:                1,
			DefaultOutputLineLimit: 120,
			DefaultTimeout:         3 * time.Second,
		},
		{
			Name:                   "cmd",
			Description:            "Run built-in Windows readonly queries via cmd /c.",
			SupportedOSFamilies:    osSet("windows"),
			AllowedFlags:           set(),
			MaxArgs:                3,
			DefaultOutputLineLimit: 120,
			DefaultTimeout:         5 * time.Second,
		},
	}
	entryMap := make(map[string]Entry, len(entries))
	order := make([]string, 0, len(entries))
	for _, entry := range entries {
		key := strings.TrimSpace(strings.ToLower(entry.Name))
		entryMap[key] = entry
		order = append(order, entry.Name)
	}
	sort.Strings(order)
	return Catalog{entries: entryMap, order: order}
}

func (c Catalog) Names() []string {
	out := make([]string, len(c.order))
	copy(out, c.order)
	return out
}

func (c Catalog) Get(name string) (Entry, bool) {
	entry, ok := c.entries[strings.TrimSpace(strings.ToLower(name))]
	return entry, ok
}

func (c Catalog) ValidateAndNormalize(name string, args []string, cwd string) ([]string, Entry, error) {
	return c.ValidateAndNormalizeForOS(name, args, cwd, runtime.GOOS)
}

func (c Catalog) ValidateAndNormalizeForOS(name string, args []string, cwd string, goos string) ([]string, Entry, error) {
	entry, ok := c.Get(name)
	if !ok {
		return nil, Entry{}, fmt.Errorf("command %q is not allowlisted", name)
	}
	if err := validatePlatformSupport(entry, goos); err != nil {
		return nil, Entry{}, err
	}
	if entry.MaxArgs > 0 && len(args) > entry.MaxArgs {
		return nil, Entry{}, fmt.Errorf("invalid arguments: command %s accepts at most %d args", entry.Name, entry.MaxArgs)
	}
	if err := validateFlags(entry, args); err != nil {
		return nil, Entry{}, err
	}
	normalized := make([]string, len(args))
	copy(normalized, args)
	for i := range normalized {
		normalized[i] = strings.TrimSpace(normalized[i])
		if strings.ContainsAny(normalized[i], "\n\r") {
			return nil, Entry{}, fmt.Errorf("invalid arguments: newline is not allowed in args")
		}
	}
	if err := validateTrailingFlagRequirement(entry, normalized); err != nil {
		return nil, Entry{}, err
	}
	var err error
	switch strings.ToLower(entry.Name) {
	case "find":
		normalized, err = validateFindArgs(normalized, cwd)
		if err != nil {
			return nil, Entry{}, err
		}
	case "ls", "stat", "file", "readlink", "du", "cat":
		normalized, err = normalizePathArguments(entry.Name, normalized, cwd)
		if err != nil {
			return nil, Entry{}, err
		}
	}
	return normalized, entry, nil
}

func set(values ...string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, value := range values {
		out[value] = struct{}{}
	}
	return out
}

func osSet(values ...string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		out[value] = struct{}{}
	}
	return out
}

func validatePlatformSupport(entry Entry, goos string) error {
	if len(entry.SupportedOSFamilies) == 0 {
		return nil
	}
	family := normalizeOSFamily(goos)
	if _, ok := entry.SupportedOSFamilies[family]; ok {
		return nil
	}
	return fmt.Errorf("command %q is not supported on %s", entry.Name, family)
}

func normalizeOSFamily(goos string) string {
	switch strings.ToLower(strings.TrimSpace(goos)) {
	case "darwin":
		return "macos"
	case "linux":
		return "linux"
	case "windows":
		return "windows"
	default:
		return "unknown"
	}
}

func validateFlags(entry Entry, args []string) error {
	for _, arg := range args {
		if arg == "" || !strings.HasPrefix(arg, "-") {
			continue
		}
		if _, ok := entry.AllowedFlags[arg]; ok {
			continue
		}
		allowedByPrefix := false
		for _, prefix := range entry.AllowedFlagPrefixes {
			if strings.HasPrefix(arg, prefix) {
				allowedByPrefix = true
				break
			}
		}
		if allowedByPrefix {
			continue
		}
		return fmt.Errorf("invalid arguments: flag %q is not allowed for %s", arg, entry.Name)
	}
	return nil
}

func validateTrailingFlagRequirement(entry Entry, args []string) error {
	if !entry.RequireTrailingFlag {
		return nil
	}
	if len(args) == 0 {
		return fmt.Errorf("invalid arguments: %s requires a trailing allowed flag", entry.Name)
	}
	last := strings.TrimSpace(args[len(args)-1])
	if last == "" {
		return fmt.Errorf("invalid arguments: %s requires a trailing allowed flag", entry.Name)
	}
	if _, ok := entry.AllowedFlags[last]; ok {
		return nil
	}
	for _, prefix := range entry.AllowedFlagPrefixes {
		if strings.HasPrefix(last, prefix) {
			return nil
		}
	}
	return fmt.Errorf("invalid arguments: %s requires a trailing allowed flag", entry.Name)
}

func normalizePathArguments(command string, args []string, cwd string) ([]string, error) {
	out := make([]string, len(args))
	copy(out, args)
	switch strings.ToLower(strings.TrimSpace(command)) {
	case "stat", "file", "readlink", "cat":
		if len(out) != 1 {
			return nil, fmt.Errorf("invalid arguments: %s requires exactly one path", command)
		}
		p, err := normalizePathArg(out[0], cwd)
		if err != nil {
			return nil, err
		}
		out[0] = p
	case "ls", "du":
		for i, arg := range out {
			if strings.HasPrefix(arg, "-") {
				continue
			}
			p, err := normalizePathArg(arg, cwd)
			if err != nil {
				return nil, err
			}
			out[i] = p
		}
	}
	return out, nil
}

func validateFindArgs(args []string, cwd string) ([]string, error) {
	if len(args) == 0 {
		args = []string{"."}
	}
	out := make([]string, len(args))
	copy(out, args)
	root, err := normalizePathArg(out[0], cwd)
	if err != nil {
		return nil, err
	}
	if root == "/" {
		return nil, fmt.Errorf("invalid arguments: find root path '/' is not allowed")
	}
	out[0] = root
	hasMaxDepth := false
	for i := 1; i < len(out); i++ {
		token := out[i]
		switch token {
		case "-mindepth", "-maxdepth":
			if i+1 >= len(out) {
				return nil, fmt.Errorf("invalid arguments: %s requires a numeric value", token)
			}
			value := out[i+1]
			parsed, parseErr := strconv.Atoi(value)
			if parseErr != nil || parsed < 0 {
				return nil, fmt.Errorf("invalid arguments: %s value must be a non-negative integer", token)
			}
			if token == "-maxdepth" {
				hasMaxDepth = true
			}
			i++
		case "-type":
			if i+1 >= len(out) {
				return nil, fmt.Errorf("invalid arguments: -type requires a value")
			}
			typeValue := strings.TrimSpace(out[i+1])
			if typeValue != "d" && typeValue != "f" && typeValue != "l" {
				return nil, fmt.Errorf("invalid arguments: -type must be one of d,f,l")
			}
			i++
		case "-name":
			if i+1 >= len(out) {
				return nil, fmt.Errorf("invalid arguments: -name requires a value")
			}
			nameValue := strings.TrimSpace(out[i+1])
			if nameValue == "" {
				return nil, fmt.Errorf("invalid arguments: -name value cannot be empty")
			}
			i++
		default:
			if strings.HasPrefix(token, "-") {
				return nil, fmt.Errorf("invalid arguments: flag %q is not allowed for find", token)
			}
			return nil, fmt.Errorf("invalid arguments: unexpected token %q for find", token)
		}
	}
	if !hasMaxDepth {
		return nil, fmt.Errorf("invalid arguments: find requires -maxdepth for bounded execution")
	}
	return out, nil
}

func normalizePathArg(raw string, cwd string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		value = "."
	}
	if strings.Contains(value, "~") {
		return "", fmt.Errorf("invalid arguments: '~' expansion is not allowed")
	}
	if strings.ContainsAny(value, "*?[]") {
		return "", fmt.Errorf("invalid arguments: glob patterns are not allowed in path arguments")
	}
	cleaned := filepath.Clean(value)
	if cleaned == "" {
		cleaned = "."
	}
	abs := cleaned
	if !filepath.IsAbs(abs) {
		base := cwd
		if strings.TrimSpace(base) == "" {
			base = "."
		}
		abs = filepath.Clean(filepath.Join(base, cleaned))
	}
	if isForbiddenPath(abs) {
		return "", fmt.Errorf("invalid arguments: path %q is outside allowed scope", raw)
	}
	return cleaned, nil
}

func isForbiddenPath(path string) bool {
	for _, prefix := range []string{"/proc", "/sys", "/dev"} {
		if path == prefix || strings.HasPrefix(path, prefix+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
