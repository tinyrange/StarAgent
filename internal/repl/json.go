package repl

import (
	"go.starlark.net/lib/json"
	"go.starlark.net/starlarkstruct"
)

var JSON = &jsonModule{Module: json.Module}

type jsonModule struct{ *starlarkstruct.Module }

func (*jsonModule) Doc() string {
	return `json: Encode and decode JSON values.
json.encode(value) -> str: Encode a Starlark value; object keys must be strings.
json.decode(text[, default]) -> value: Decode JSON; invalid input raises unless a positional default is supplied.
json.encode_indent(value, prefix="", indent="\t") -> str: Encode with indentation.
json.indent(text, prefix="", indent="\t") -> str: Pretty-print valid JSON.`
}
