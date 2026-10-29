package server

import (
	"context"
	"fmt"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

func (s *serverService) stringBuiltin(service string, write bool) *starlark.Builtin {
	method := "get_string"
	if write {
		method = "set_string"
	}
	return starlark.NewBuiltin("service."+method, func(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var name, value string
		params := []any{"name", &name}
		if write {
			params = append(params, "value", &value)
		}
		if err := starlark.UnpackArgs(fn.Name(), args, kwargs, params...); err != nil {
			return nil, err
		}
		expr, err := syntax.ParseExpr("<name>", name, 0)
		ident, ok := expr.(*syntax.Ident)
		if err != nil || !ok || ident.Name != name || name == "None" || name == "True" || name == "False" {
			return nil, fmt.Errorf("%s: name must be a variable name", fn.Name())
		}
		code := "str(" + name + ")"
		if write {
			code = name + " = " + syntax.Quote(value, false)
		}
		output, err := s.session.execute(thread.Local("context").(context.Context), service, code)
		if err == nil {
			if write && strings.TrimSpace(output) == "" {
				return starlark.None, nil
			}
			if !write {
				expr, parseErr := syntax.ParseExpr("<string>", output, 0)
				if literal, ok := expr.(*syntax.Literal); parseErr == nil && ok && literal.Token == syntax.STRING {
					return starlark.String(literal.Value.(string)), nil
				}
			}
		} else {
			output = err.Error() + "\n" + output
		}
		id := s.session.retainOutput(output)
		message := "expected a quoted string response"
		if write {
			message = "write not confirmed; inspect the remote variable before retrying"
		}
		return nil, fmt.Errorf("%s: %s; inspect output.read(%q)", fn.Name(), message, id)
	})
}
