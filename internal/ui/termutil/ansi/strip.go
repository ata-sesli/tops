package ansi

import "regexp"

var stripPattern = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\a\x1b]*(?:\a|\x1b\\)|\x1b[@-_]`)

// Strip removes ANSI escape sequences from terminal output.
func Strip(text string) string {
	if text == "" {
		return ""
	}
	return stripPattern.ReplaceAllString(text, "")
}
