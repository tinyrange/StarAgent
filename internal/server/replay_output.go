package server

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

var (
	// Match only managed temporary roots, preserving the path inside the workspace.
	replayWorkspace    = regexp.MustCompile(`(?:/private)?(?:/var/folders/[^/\s"\\]+/[^/\s"\\]+/T|/tmp|/var/tmp|` + regexp.QuoteMeta(filepath.Clean(os.TempDir())) + `)/staragent-[0-9]+\b`)
	replayGoTiming     = regexp.MustCompile(`(?m)^((?:ok|FAIL)[ \t]+\S+[ \t]+)[0-9]+(?:\.[0-9]+)?s([ \t]*(?:\[[^\r\n]*\])?)$`)
	replayGoTestTiming = regexp.MustCompile(`(?m)^([ \t]*--- (?:PASS|FAIL|SKIP): [^\r\n]+ \()[0-9]+(?:\.[0-9]+)?s(\))$`)
	replayQuoted       = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)
)

// Normalize known process-output volatility only for comparison. Recordings keep
// the exact text the agent received. All other differences still fail replay.
func normalizeReplayOutput(text string) string {
	normalize := func(value string) string {
		value = replayWorkspace.ReplaceAllString(value, "<workspace>")
		value = replayGoTiming.ReplaceAllString(value, "${1}<elapsed>${2}")
		return replayGoTestTiming.ReplaceAllString(value, "${1}<elapsed>${2}")
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
