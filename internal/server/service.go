package server

import (
	"context"
	"fmt"
	"io"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
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
	return "", nil
}

// ExecuteLine implements [Service].
func (s *serverService) ExecuteLine(ctx context.Context, line string) (string, error) {
	opts := s.getOptions()

	file, err := opts.Parse("<repl>", line, syntax.RetainComments)
	if err != nil {
		return "", fmt.Errorf("failed to parse: %w", err)
	}

	thread := s.getThread()
	var output strings.Builder
	thread.Print = func(_ *starlark.Thread, msg string) {
		fmt.Fprintln(&output, msg)
	}

	if err := starlark.ExecREPLChunk(file, thread, s.declared); err != nil {
		return "", fmt.Errorf("failed to execute: %w", err)
	}

	return output.String(), nil
}

func (s *serverService) getOptions() syntax.FileOptions {
	return syntax.FileOptions{Set: true, While: true}
}

func (s *serverService) getThread() *starlark.Thread {
	return &starlark.Thread{
		Name: "repl",
	}
}

func (s *serverService) Init(ctx context.Context) error {
	s.declared = make(starlark.StringDict)

	return nil
}

var (
	_ Service = &serverService{}
)
