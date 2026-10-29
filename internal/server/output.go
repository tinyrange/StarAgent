package server

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
)

const outputLimit = 8192

const outputDoc = `output: Read full outputs retained on the session host.
Model-facing exec output is previewed above 8192 bytes; full text is retained for this session and recorded in the log. Nested service calls return full output.
output.read(id, offset=0, limit=4096) -> {text, next_offset, truncated, bytes}: Read UTF-8 text using byte offsets; limit is 1..8192. Print .text for readable output; follow next_offset while truncated. Available in the server REPL (service="").`

func textEnd(text string, end int) int {
	if end >= len(text) {
		return len(text)
	}
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return end
}

func (s *Session) presentOutput(text string) string {
	if len(text) <= outputLimit {
		return text
	}
	id := s.retainOutput(text)
	return text[:textEnd(text, outputLimit*3/4)] +
		fmt.Sprintf("\n[Output truncated; full text: output.read(%q) in the server REPL.]\n", id) +
		text[textEnd(text, len(text)-outputLimit/4):]
}

func (s *Session) retainOutput(text string) string {
	s.outputs = append(s.outputs, text)
	return fmt.Sprintf("out-%d", len(s.outputs))
}

func (s *Session) outputModule() starlark.Value {
	return &documentedModule{Module: &starlarkstruct.Module{Name: "output", Members: starlark.StringDict{
		"read": starlark.NewBuiltin("output.read", func(_ *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			var id string
			offset, limit := 0, 4096
			if err := starlark.UnpackArgs(fn.Name(), args, kwargs, "id", &id, "offset?", &offset, "limit?", &limit); err != nil {
				return nil, err
			}
			n, err := strconv.Atoi(strings.TrimPrefix(id, "out-"))
			if err != nil || id != fmt.Sprintf("out-%d", n) || n < 1 || n > len(s.outputs) {
				return nil, fmt.Errorf("unknown output %q", id)
			}
			text := s.outputs[n-1]
			if offset < 0 || offset > len(text) || limit < 1 || limit > outputLimit || (offset < len(text) && !utf8.RuneStart(text[offset])) {
				return nil, fmt.Errorf("invalid byte offset or limit (1..8192)")
			}
			end := textEnd(text, offset+limit)
			if end == offset && end < len(text) {
				return nil, fmt.Errorf("limit is too small for the next UTF-8 character")
			}
			return starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{
				"text": starlark.String(text[offset:end]), "next_offset": starlark.MakeInt(end),
				"truncated": starlark.Bool(end < len(text)), "bytes": starlark.MakeInt(len(text)),
			}), nil
		}),
	}}, doc: outputDoc}
}
