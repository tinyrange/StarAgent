package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/tinyrange/staragent/internal/protocol"
)

type shellREPLFactory struct{}

func (f *shellREPLFactory) Create(ctx context.Context, config map[string]any) (Service, error) {
	var args []string
	switch values := config["args"].(type) {
	case []string:
		args = values
	case []any:
		for i, value := range values {
			arg, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("args[%d] must be a string", i)
			}
			args = append(args, arg)
		}
	default:
		return nil, fmt.Errorf("args must be a nonempty list of strings")
	}
	if len(args) == 0 || args[0] == "" {
		return nil, fmt.Errorf("args must include a nonempty command")
	}
	return &shellREPLService{args: args}, nil
}

type shellREPLService struct {
	args   []string
	cwd    string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	client *protocol.Client
}

func (s *shellREPLService) Init(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.cmd = exec.Command(s.args[0], s.args[1:]...)
	s.cmd.Dir = s.cwd
	s.cmd.Stderr = os.Stderr
	var err error
	s.stdout, err = s.cmd.StdoutPipe()
	if err != nil {
		return err
	}
	s.stdin, err = s.cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := s.cmd.Start(); err != nil {
		return err
	}
	s.client, err = protocol.StartClient(s.stdin, s.stdout)
	if err != nil {
		// A malformed handshake need not mean the child has exited.
		s.cmd.Process.Kill()
		return err
	}
	return nil
}

func (s *shellREPLService) ExecuteLine(ctx context.Context, line string) (string, error) {
	response, err := s.client.Exchange(ctx, line)
	if err != nil {
		return "", err
	}
	output, readErr := io.ReadAll(response)
	return string(output), errors.Join(readErr, response.Close())
}

func (s *shellREPLService) Documentation(ctx context.Context) (string, error) {
	return s.client.Documentation(), nil
}

func (s *shellREPLService) Close() error {
	if s.stdin != nil {
		s.stdin.Close()
		s.stdin = nil
	}
	if s.stdout != nil {
		defer s.stdout.Close()
		s.stdout = nil
	}
	if s.cmd == nil || s.cmd.Process == nil || s.cmd.ProcessState != nil {
		return nil
	}
	// EOF normally stops the REPL. Bound shutdown if the child ignores it.
	done := make(chan error, 1)
	go func() { done <- s.cmd.Wait() }()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		s.cmd.Process.Kill()
		return errors.Join(fmt.Errorf("shell-repl did not exit after stdin closed"), <-done)
	}
}

var (
	_ ServiceFactory = &shellREPLFactory{}
	_ Service        = &shellREPLService{}
)
