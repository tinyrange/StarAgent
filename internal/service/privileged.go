package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
)

type privileged struct {
	cwd string
}

func (*privileged) String() string        { return "privileged" }
func (*privileged) Type() string          { return "privileged" }
func (*privileged) Freeze()               {}
func (*privileged) Truth() starlark.Bool  { return starlark.True }
func (*privileged) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable: privileged") }
func (*privileged) AttrNames() []string   { return []string{"shell"} }

func (p *privileged) Attr(name string) (starlark.Value, error) {
	if name == "shell" {
		return &documentedBuiltin{
			Builtin: starlark.NewBuiltin("privileged.shell", p.shell),
			doc:     "privileged.shell(*args: str, env: dict[str, str] = {}, wd: str = '.', timeout=None) -> shell_command: Execute argv without shell expansion. env overrides inherited variables; relative wd is based on the workspace. timeout is positive seconds, counted when run()/output() starts; None disables it. Timeouts raise. output() returns stdout; run() returns a result.",
		}, nil
	}
	return nil, nil
}

func (p *privileged) shell(_ *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("%s: expected one or more positional strings", fn.Name())
	}
	var env *starlark.Dict
	var timeout starlark.Value = starlark.None
	wd := "."
	if err := starlark.UnpackArgs(fn.Name(), nil, kwargs, "env?", &env, "wd?", &wd, "timeout?", &timeout); err != nil {
		return nil, err
	}
	argv := make([]string, len(args))
	for i, arg := range args {
		value, ok := starlark.AsString(arg)
		if !ok {
			return nil, fmt.Errorf("%s: argument %d must be a string, got %s", fn.Name(), i+1, arg.Type())
		}
		argv[i] = value
	}
	if !filepath.IsAbs(wd) {
		wd = filepath.Join(p.cwd, wd)
	}
	command := &shellCommand{argv: argv, cwd: wd}
	if timeout != starlark.None {
		seconds, ok := starlark.AsFloat(timeout)
		if !ok || !(seconds >= 1e-9 && seconds < float64(1<<63)/float64(time.Second)) {
			return nil, fmt.Errorf("%s: timeout must be positive, finite seconds", fn.Name())
		}
		command.timeout = time.Duration(seconds * float64(time.Second))
	}
	if env != nil {
		for _, item := range env.Items() {
			key, keyOK := starlark.AsString(item[0])
			value, valueOK := starlark.AsString(item[1])
			if !keyOK || !valueOK {
				return nil, fmt.Errorf("%s: env keys and values must be strings", fn.Name())
			}
			command.env = append(command.env, key+"="+value)
		}
	}
	return command, nil
}

type shellCommand struct {
	argv    []string
	cwd     string
	env     []string
	timeout time.Duration
}

func (*shellCommand) String() string        { return "shell_command" }
func (*shellCommand) Type() string          { return "shell_command" }
func (*shellCommand) Freeze()               {}
func (*shellCommand) Truth() starlark.Bool  { return starlark.True }
func (*shellCommand) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable: shell_command") }
func (*shellCommand) AttrNames() []string   { return []string{"output", "run"} }

func (c *shellCommand) Attr(name string) (starlark.Value, error) {
	if name == "output" {
		return starlark.NewBuiltin("shell_command.output", c.output), nil
	}
	if name == "run" {
		return &documentedBuiltin{
			Builtin: starlark.NewBuiltin("shell_command.run", c.run),
			doc:     "run() -> struct(ok: bool, exit_code: int, stdout: str, stderr: str): Execute with the configured argv, env, wd, and timeout. Nonzero exits are results; launch errors and timeouts raise.",
		}, nil
	}
	return nil, nil
}

func (c *shellCommand) command() (*exec.Cmd, context.Context, context.CancelFunc) {
	ctx := context.Background()
	cancel := func() {}
	if c.timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
	}
	cmd := exec.CommandContext(ctx, c.argv[0], c.argv[1:]...)
	if c.timeout > 0 {
		cmd.WaitDelay = time.Second // Bound waits for inherited output pipes too.
	}
	cmd.Dir = c.cwd
	if len(c.env) > 0 {
		cmd.Env = append(cmd.Environ(), c.env...)
	}
	return cmd, ctx, cancel
}

func (c *shellCommand) output(_ *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackArgs(fn.Name(), args, kwargs); err != nil {
		return nil, err
	}
	cmd, ctx, cancel := c.command()
	defer cancel()
	cmd.Stderr = os.Stderr
	output, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("command timed out after %s: %w", c.timeout, ctx.Err())
	}
	if err != nil {
		return nil, err
	}
	return starlark.String(output), nil
}

func (c *shellCommand) run(_ *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackArgs(fn.Name(), args, kwargs); err != nil {
		return nil, err
	}
	cmd, ctx, cancel := c.command()
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("command timed out after %s: %w", c.timeout, ctx.Err())
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return nil, fmt.Errorf("shell: %w: %s", err, stderr.String())
	}
	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{
		"ok": starlark.Bool(err == nil), "exit_code": starlark.MakeInt(cmd.ProcessState.ExitCode()),
		"stdout": starlark.String(stdout.String()), "stderr": starlark.String(stderr.String()),
	}), nil
}

var (
	_ starlark.HasAttrs = (*privileged)(nil)
	_ starlark.HasAttrs = (*shellCommand)(nil)
)
