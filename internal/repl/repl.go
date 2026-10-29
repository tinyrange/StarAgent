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
	Documentation string
	Prepare       func(*syntax.File) error

	Input  io.Reader
	Output io.Writer
}

// FileOptions is the Starlark dialect used by every agent REPL.
func FileOptions() syntax.FileOptions {
	return syntax.FileOptions{Set: true, While: true, TopLevelControl: true, GlobalReassign: true, Recursion: true}
}

func Start(opts Options) error {
	if opts.Input == nil {
		opts.Input = os.Stdin
	}
	if opts.Output == nil {
		opts.Output = os.Stdout
	}

	server, err := protocol.StartServer(opts.Output, opts.Input, bytes.NewBufferString(opts.Documentation))
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
		evalErr := Evaluate(opts, input, output)
		if err := errors.Join(evalErr, output.Close()); err != nil {
			return err
		}
	}
}

// Evaluate writes parse/evaluation errors to output so the next call can recover.
// Returned errors are output failures, not errors in the agent's code.
func Evaluate(opts Options, input string, output io.Writer) error {
	parsed, err := opts.SyntaxOptions.Parse("<repl>", input, syntax.RetainComments)
	if err != nil {
		_, writeErr := fmt.Fprintf(output, "%v\n", err)
		return writeErr
	}
	if opts.Prepare != nil {
		if err := opts.Prepare(parsed); err != nil {
			_, writeErr := fmt.Fprintf(output, "error: %v\n", err)
			return writeErr
		}
	}

	var printErr error
	write := func(msg string) {
		if printErr == nil {
			_, printErr = io.WriteString(output, msg)
		}
	}
	opts.Thread.SetLocal("repl.print", write)
	opts.Thread.Print = func(_ *starlark.Thread, msg string) { write(msg + "\n") }
	if opts.Environment == nil {
		opts.Environment = make(starlark.StringDict)
	}
	if _, exists := opts.Environment["print"]; !exists {
		opts.Environment["print"] = Print
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
