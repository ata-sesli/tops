package help

import "tops/internal/model"

type Invocation struct {
	CommandName string
	Args        []string
	Pattern     string
}

type Resolver struct{}

func NewResolver() Resolver { return Resolver{} }

func (Resolver) Resolve(target Target, platform model.PlatformContext) []Invocation {
	family := model.NormalizePlatformContext(platform).OSFamily
	base := append([]string(nil), target.Subcommands...)
	useManFirst := shouldPreferManFirst(target.RootCommand, family)

	candidates := []Invocation{}
	if useManFirst {
		candidates = append(candidates,
			Invocation{CommandName: "man", Args: []string{target.RootCommand}, Pattern: "man"},
			Invocation{CommandName: target.RootCommand, Args: append(append([]string(nil), base...), "-h"), Pattern: "-h"},
			Invocation{CommandName: target.RootCommand, Args: append(append([]string(nil), base...), "--help"), Pattern: "--help"},
		)
		if helpArgs, ok := buildHelpPatternArgs(base); ok {
			candidates = append(candidates, Invocation{CommandName: target.RootCommand, Args: helpArgs, Pattern: "help"})
		}
	} else {
		candidates = append(candidates,
			Invocation{CommandName: target.RootCommand, Args: append(append([]string(nil), base...), "--help"), Pattern: "--help"},
			Invocation{CommandName: target.RootCommand, Args: append(append([]string(nil), base...), "-h"), Pattern: "-h"},
		)
		if helpArgs, ok := buildHelpPatternArgs(base); ok {
			candidates = append(candidates, Invocation{CommandName: target.RootCommand, Args: helpArgs, Pattern: "help"})
		}
		candidates = append(candidates, Invocation{CommandName: "man", Args: []string{target.RootCommand}, Pattern: "man"})
	}

	seen := map[string]struct{}{}
	resolved := make([]Invocation, 0, len(candidates))
	for _, candidate := range candidates {
		key := candidate.CommandName + "\x00"
		for _, arg := range candidate.Args {
			key += arg + "\x00"
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		resolved = append(resolved, candidate)
	}
	return resolved
}

func shouldPreferManFirst(rootCommand string, family string) bool {
	if family != "macos" && family != "linux" {
		return false
	}
	switch rootCommand {
	case "du", "ls", "find", "ps", "df", "stat", "file", "readlink", "uname", "hostname", "whoami", "sw_vers", "cat", "col":
		return true
	default:
		return false
	}
}

func buildHelpPatternArgs(base []string) ([]string, bool) {
	if len(base) == 0 {
		return []string{"help"}, true
	}
	out := make([]string, 0, len(base)+1)
	out = append(out, "help")
	out = append(out, base...)
	return out, true
}
