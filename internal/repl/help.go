package repl

import (
	"fmt"
	"strings"

	"go.starlark.net/starlark"
)

func Help(_ *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var object starlark.Value
	if err := starlark.UnpackArgs(fn.Name(), args, kwargs, "object", &object); err != nil {
		return nil, err
	}
	if documented, ok := object.(interface{ Doc() string }); ok {
		return starlark.String(strings.TrimSpace(documented.Doc())), nil
	}
	return nil, fmt.Errorf("help: no documentation for %s", object.Type())
}
