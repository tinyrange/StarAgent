package service

import (
	"fmt"
	"os"
	"os/exec"

	"go.starlark.net/starlark"
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
		return starlark.NewBuiltin("privileged.shell", p.shell), nil
	}
	return nil, nil
}

func (p *privileged) shell(_ *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var command string
	if err := starlark.UnpackArgs(fn.Name(), args, kwargs, "command", &command); err != nil {
		return nil, err
	}
	return &shellCommand{command: command, cwd: p.cwd}, nil
}

type shellCommand struct {
	command, cwd string
}

func (*shellCommand) String() string        { return "shell_command" }
func (*shellCommand) Type() string          { return "shell_command" }
func (*shellCommand) Freeze()               {}
func (*shellCommand) Truth() starlark.Bool  { return starlark.True }
func (*shellCommand) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable: shell_command") }
func (*shellCommand) AttrNames() []string   { return []string{"output"} }

func (c *shellCommand) Attr(name string) (starlark.Value, error) {
	if name == "output" {
		return starlark.NewBuiltin("shell_command.output", c.output), nil
	}
	return nil, nil
}

func (c *shellCommand) output(_ *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackArgs(fn.Name(), args, kwargs); err != nil {
		return nil, err
	}
	cmd := exec.Command("sh", "-c", c.command)
	cmd.Dir = c.cwd
	cmd.Stderr = os.Stderr
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return starlark.String(output), nil
}

var (
	_ starlark.HasAttrs = (*privileged)(nil)
	_ starlark.HasAttrs = (*shellCommand)(nil)
)
