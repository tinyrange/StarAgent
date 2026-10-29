package server

import (
	"context"
	"fmt"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
)

const serviceDoc = `service: Access this session's named services.
service.list() -> list[str]: Sorted available names; does not start services.
service.get(name: str) -> service: Get a handle without starting it; unknown names raise.
service.get(name).exec(code: str) -> str: Start if needed, execute in its persistent REPL, and return output.
service.get(name).help() -> str: Start if needed and return its startup documentation.`

type documentedModule struct {
	*starlarkstruct.Module
	doc string
}

func (m *documentedModule) Doc() string { return m.doc }

func (s *serverService) listServices(_ *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackArgs(fn.Name(), args, kwargs); err != nil {
		return nil, err
	}
	names := make(starlark.StringDict)
	for name := range s.session.serviceConfigs {
		names[name] = starlark.None
	}
	for name := range s.session.services {
		names[name] = starlark.None
	}
	delete(names, "") // The built-in REPL is not an external service.
	values := make([]starlark.Value, 0, len(names))
	for _, name := range names.Keys() {
		values = append(values, starlark.String(name))
	}
	return starlark.NewList(values), nil
}

func (s *serverService) getService(_ *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var name string
	if err := starlark.UnpackArgs(fn.Name(), args, kwargs, "name", &name); err != nil {
		return nil, err
	}
	_, configured := s.session.serviceConfigs[name]
	_, running := s.session.services[name]
	if name == "" || (!configured && !running) {
		return nil, fmt.Errorf("unknown service %q", name)
	}
	return &starlarkstruct.Module{Name: name, Members: starlark.StringDict{
		"help": starlark.NewBuiltin("service.help", func(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			if err := starlark.UnpackArgs(fn.Name(), args, kwargs); err != nil {
				return nil, err
			}
			ctx := thread.Local("context").(context.Context)
			service, err := s.session.getService(ctx, name)
			if err != nil {
				return nil, err
			}
			doc, err := service.Documentation(ctx)
			if err != nil {
				return nil, err
			}
			return starlark.String(doc), nil
		}),
		"exec": starlark.NewBuiltin("service.exec", func(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			var code string
			if err := starlark.UnpackArgs(fn.Name(), args, kwargs, "code", &code); err != nil {
				return nil, err
			}
			output, err := s.session.OnToolCall(thread.Local("context").(context.Context), "", name, code)
			if err != nil {
				return nil, err
			}
			return starlark.String(output), nil
		}),
	}}, nil
}
