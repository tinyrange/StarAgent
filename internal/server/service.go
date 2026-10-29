package server

import (
	"context"
	"io"
	"strings"

	"github.com/tinyrange/staragent/internal/repl"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
)

type ServiceFactory interface {
	Create(ctx context.Context, config map[string]any) (Service, error)
}

type Service interface {
	io.Closer
	Init(ctx context.Context) error
	ExecuteLine(ctx context.Context, line string) (string, error)
	Documentation(ctx context.Context) (string, error)
}

type serverService struct {
	session *Session

	declared starlark.StringDict
}

// Close implements [Service].
func (s *serverService) Close() error {
	return nil
}

// Documentation implements [Service].
func (s *serverService) Documentation(ctx context.Context) (string, error) {
	return serviceDoc + "\n\n" + repl.PrintDoc, nil
}

// ExecuteLine implements [Service].
func (s *serverService) ExecuteLine(ctx context.Context, line string) (string, error) {
	opts := repl.FileOptions()
	thread := &starlark.Thread{Name: "server repl"}
	thread.SetLocal("context", ctx)
	var output strings.Builder
	err := repl.Evaluate(repl.Options{SyntaxOptions: &opts, Thread: thread, Environment: s.declared}, line, &output)
	return output.String(), err
}

func (s *serverService) Init(ctx context.Context) error {
	s.declared = starlark.StringDict{
		"help": starlark.NewBuiltin("help", repl.Help),
		"json": repl.JSON,
		"service": &documentedModule{
			Module: &starlarkstruct.Module{Name: "service", Members: starlark.StringDict{
				"list": starlark.NewBuiltin("service.list", s.listServices),
				"get":  starlark.NewBuiltin("service.get", s.getService),
			}},
			doc: serviceDoc,
		},
	}

	return nil
}

var (
	_ Service = &serverService{}
)
