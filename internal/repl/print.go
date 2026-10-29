package repl

import (
	"io"
	"os"
	"strings"

	"go.starlark.net/starlark"
)

const PrintDoc = `print(*values, sep=" ", end="\n") -> None: Write values separated by sep and followed by end. Strings and bytes print literally; other values use their Starlark representation. Use end="" to omit the trailing newline.`

var Print = &printBuiltin{starlark.NewBuiltin("print", printValues)}

type printBuiltin struct{ *starlark.Builtin }

func (*printBuiltin) Doc() string { return PrintDoc }

func printValues(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	sep, end := " ", "\n"
	if err := starlark.UnpackArgs(fn.Name(), nil, kwargs, "sep?", &sep, "end?", &end); err != nil {
		return nil, err
	}
	var text strings.Builder
	for i, value := range args {
		if i > 0 {
			text.WriteString(sep)
		}
		switch value := value.(type) {
		case starlark.String:
			text.WriteString(string(value))
		case starlark.Bytes:
			text.WriteString(string(value))
		default:
			text.WriteString(value.String())
		}
	}
	text.WriteString(end)
	if write, ok := thread.Local("repl.print").(func(string)); ok {
		write(text.String())
		return starlark.None, nil
	}
	_, err := io.WriteString(os.Stderr, text.String())
	return starlark.None, err
}
