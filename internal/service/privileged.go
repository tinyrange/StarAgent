package service

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

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
			doc:     "privileged.shell(*args: str, env: dict[str, str] = {}, wd: str = '.') -> shell_command: Execute argv without shell expansion. env overrides inherited variables; relative wd is based on the workspace. output() returns stdout and raises on failure; run() returns a result.",
		}, nil
	}
	return nil, nil
}

func (p *privileged) shell(_ *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("%s: expected one or more positional strings", fn.Name())
	}
	var env *starlark.Dict
	wd := "."
	if err := starlark.UnpackArgs(fn.Name(), nil, kwargs, "env?", &env, "wd?", &wd); err != nil {
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
	argv []string
	cwd  string
	env  []string
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
			doc:     "run() -> struct(ok: bool, exit_code: int, stdout: str, stderr: str): Execute with the configured argv, env, and wd. Nonzero exits are results; launch errors raise.",
		}, nil
	}
	return nil, nil
}

func (c *shellCommand) command() *exec.Cmd {
	cmd := exec.Command(c.argv[0], c.argv[1:]...)
	cmd.Dir = c.cwd
	if len(c.env) > 0 {
		cmd.Env = append(cmd.Environ(), c.env...)
	}
	return cmd
}

func (c *shellCommand) output(_ *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackArgs(fn.Name(), args, kwargs); err != nil {
		return nil, err
	}
	cmd := c.command()
	cmd.Stderr = os.Stderr
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return starlark.String(output), nil
}

func (c *shellCommand) run(_ *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackArgs(fn.Name(), args, kwargs); err != nil {
		return nil, err
	}
	cmd := c.command()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
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
