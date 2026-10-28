package repl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/tinyrange/staragent/internal/protocol"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

type Options struct {
	SyntaxOptions *syntax.FileOptions
	Thread        *starlark.Thread
	Environment   starlark.StringDict

	Input  io.Reader
	Output io.Writer
}

func Start(opts Options) error {
	if opts.Input == nil {
		opts.Input = os.Stdin
	}
	if opts.Output == nil {
		opts.Output = os.Stdout
	}

	var documentation = ""

	server, err := protocol.StartServer(opts.Output, opts.Input, bytes.NewBufferString(documentation))
	if err != nil {
		return fmt.Errorf("failed to start server: %w", err)
	}

	for {
		input, output, err := server.Exchange(context.Background())
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("failed to exchange: %w", err)
		}
		evalErr := evaluate(opts, input, output)
		if err := errors.Join(evalErr, output.Close()); err != nil {
			return err
		}
	}
}

func evaluate(opts Options, input string, output io.Writer) error {
	parsed, err := opts.SyntaxOptions.Parse("<repl>", input, syntax.RetainComments)
	if err != nil {
		_, writeErr := fmt.Fprintf(output, "%v\n", err)
		return writeErr
	}

	var printErr error
	opts.Thread.Print = func(thread *starlark.Thread, msg string) {
		if printErr == nil {
			_, printErr = fmt.Fprintln(output, msg)
		}
	}

	// A lone expression should display its value, as in the Starlark REPL.
	if len(parsed.Stmts) == 1 {
		if expr, ok := parsed.Stmts[0].(*syntax.ExprStmt); ok {
			var value starlark.Value
			value, err = starlark.EvalExprOptions(parsed.Options, opts.Thread, expr.X, opts.Environment)
			if err == nil {
				opts.Environment["_"] = value
				if value != starlark.None && printErr == nil {
					_, printErr = fmt.Fprintln(output, value)
				}
			}
		} else {
			err = starlark.ExecREPLChunk(parsed, opts.Thread, opts.Environment)
		}
	} else {
		err = starlark.ExecREPLChunk(parsed, opts.Thread, opts.Environment)
	}
	if printErr != nil {
		return printErr
	}
	if err != nil {
		_, writeErr := fmt.Fprintf(output, "error: %v\n", err)
		return writeErr
	}
	return nil
}
