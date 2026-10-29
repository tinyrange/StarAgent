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
	if function, ok := object.(*starlark.Function); ok {
		return starlark.String(FunctionDoc(function)), nil
	}
	if documented, ok := object.(interface{ Doc() string }); ok {
		return starlark.String(strings.TrimSpace(documented.Doc())), nil
	}
	return nil, fmt.Errorf("help: no documentation for %s", object.Type())
}

// FunctionDoc includes the callable signature even when no docstring was supplied.
func FunctionDoc(fn *starlark.Function) string {
	regular := fn.NumParams()
	if fn.HasVarargs() {
		regular--
	}
	if fn.HasKwargs() {
		regular--
	}
	positional := regular - fn.NumKwonlyParams()
	var params []string
	for i := 0; i <= regular; i++ {
		if i == positional {
			if fn.HasVarargs() {
				name, _ := fn.Param(regular)
				params = append(params, "*"+name)
			} else if fn.NumKwonlyParams() > 0 {
				params = append(params, "*")
			}
		}
		if i == regular {
			break
		}
		name, _ := fn.Param(i)
		if value := fn.ParamDefault(i); value != nil {
			name += "=" + value.String()
		}
		params = append(params, name)
	}
	if fn.HasKwargs() {
		name, _ := fn.Param(fn.NumParams() - 1)
		params = append(params, "**"+name)
	}
	return strings.TrimSpace(fn.Name() + "(" + strings.Join(params, ", ") + ")\n" + strings.TrimSpace(fn.Doc()))
}
