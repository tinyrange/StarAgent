package repl

import (
	"bytes"
	"context"
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
		err := func() error {
			input, output, err := server.Exchange(context.Background())
			if err != nil {
				return fmt.Errorf("failed to exchange: %w", err)
			}
			defer output.Close()

			parsed, err := opts.SyntaxOptions.Parse("<repl>", input, syntax.RetainComments)
			if err != nil {
				if _, err := fmt.Fprintf(output, "%v\n", err); err != nil {
					fmt.Fprintf(os.Stderr, "failed to write: %v", err)
				}
			}

			opts.Thread.Print = func(thread *starlark.Thread, msg string) {
				if _, err := fmt.Fprintf(output, "%s\n", msg); err != nil {
					fmt.Fprintf(os.Stderr, "failed to write: %v", err)
				}
			}

			if err := starlark.ExecREPLChunk(parsed, opts.Thread, opts.Environment); err != nil {
				if _, innerErr := fmt.Fprintf(output, "error: %v\n", err); err != nil {
					return fmt.Errorf("failed to write error: %v (inner %w)", err, innerErr)
				}
			}

			return nil
		}()
		if err != nil {
			return err
		}
	}
}
