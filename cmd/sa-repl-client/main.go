package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/chzyer/readline"
	"github.com/tinyrange/staragent/internal/protocol"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: %s cmd [args]", os.Args[0])
		os.Exit(1)
	}

	cmd := exec.Command(os.Args[1], os.Args[2:]...)

	outPipe, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	inPipe, err := cmd.StdinPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	defer cmd.Process.Kill()

	client, err := protocol.StartClient(inPipe, outPipe)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	rl, err := readline.New(">>> ")

	for {
		func() {
			line, err := rl.Readline()
			if err != nil {
				fmt.Fprintf(os.Stderr, "error reading line: %v\n", err)
				os.Exit(1)
			}

			data, err := client.Exchange(context.Background(), line)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error exchanging: %v\n", err)
				os.Exit(1)
			}
			defer data.Close()

			if _, err := io.Copy(rl, data); err != nil {
				fmt.Fprintf(os.Stderr, "error writing output: %v\n", err)
				os.Exit(1)
			}
		}()
	}
}
