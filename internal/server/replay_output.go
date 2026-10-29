package server

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

var (
	// Match only managed temporary roots, preserving the path inside the workspace.
	replayWorkspace      = regexp.MustCompile(`(?:/private)?(?:/var/folders/[^/\s"\\]+/[^/\s"\\]+/T|/tmp|/var/tmp|` + regexp.QuoteMeta(filepath.Clean(os.TempDir())) + `)/staragent-[0-9]+\b`)
	replayGoTiming       = regexp.MustCompile(`(?m)^((?:ok|FAIL)[ \t]+\S+[ \t]+)[0-9]+(?:\.[0-9]+)?s([ \t]*(?:\[[^\r\n]*\])?)$`)
	replayGoTestTiming   = regexp.MustCompile(`(?m)^([ \t]*--- (?:PASS|FAIL|SKIP): [^\r\n]+ \()[0-9]+(?:\.[0-9]+)?s(\))$`)
	replayQuoted         = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)
	replayUnitTestTiming = regexp.MustCompile(`(?m)^(Ran [0-9]+ tests? in )[0-9]+(?:\.[0-9]+)?s$`)
)

// Normalize known process-output volatility only for comparison. Recordings keep
// the exact text the agent received. All other differences still fail replay.
func normalizeReplayOutput(text string, ignore ...*regexp.Regexp) string {
	normalize := func(value string) string {
		value = replayWorkspace.ReplaceAllString(value, "<workspace>")
		value = replayGoTiming.ReplaceAllString(value, "${1}<elapsed>${2}")
		value = replayGoTestTiming.ReplaceAllString(value, "${1}<elapsed>${2}")
		value = replayUnitTestTiming.ReplaceAllString(value, "${1}<elapsed>s")
		for _, pattern := range ignore {
			value = pattern.ReplaceAllString(value, "<ignored>")
		}
		return value
	}
	// Command results render stdout/stderr as quoted Starlark strings. Decode
	// those strings to recognize complete timing lines, rather than arbitrary numbers.
	text = replayQuoted.ReplaceAllStringFunc(text, func(quoted string) string {
		value, err := strconv.Unquote(quoted)
		if err != nil {
			return quoted
		}
		if changed := normalize(value); changed != value {
			return strconv.Quote(changed)
		}
		return quoted
	})
	return normalize(text)
}
