package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/chzyer/readline"
	"github.com/tinyrange/staragent/internal/protocol"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: %s cmd [args]", os.Args[0])
	}

	cmd := exec.Command(os.Args[1], os.Args[2:]...)
	cmd.Stderr = os.Stderr

	outPipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	defer outPipe.Close()
	inPipe, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	defer inPipe.Close()

	if err := cmd.Start(); err != nil {
		return err
	}

	defer func() {
		inPipe.Close()
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()

	client, err := protocol.StartClient(inPipe, outPipe)
	if err != nil {
		return err
	}

	rl, err := readline.New(">>> ")
	if err != nil {
		return err
	}
	defer rl.Close()

	for {
		line, err := rl.Readline()
		if errors.Is(err, io.EOF) || (errors.Is(err, readline.ErrInterrupt) && line == "") {
			if err := inPipe.Close(); err != nil {
				return err
			}
			return cmd.Wait()
		}
		if errors.Is(err, readline.ErrInterrupt) {
			continue
		}
		if err != nil {
			return fmt.Errorf("reading line: %w", err)
		}

		data, err := client.Exchange(context.Background(), line)
		if err != nil {
			return fmt.Errorf("exchanging: %w", err)
		}
		_, copyErr := io.Copy(rl, data)
		closeErr := data.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return fmt.Errorf("reading response: %w", err)
		}
	}
}
