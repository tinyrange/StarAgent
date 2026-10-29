package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"go.starlark.net/starlark"
)

type workspace struct {
	root        *os.Root
	read, write bool
	deniedFiles []string
}

func (*workspace) String() string        { return "workspace" }
func (*workspace) Type() string          { return "workspace" }
func (*workspace) Freeze()               {}
func (*workspace) Truth() starlark.Bool  { return starlark.True }
func (*workspace) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable: workspace") }

func (w *workspace) AttrNames() []string {
	var names []string
	if w.read {
		names = append(names, "exists", "read_file")
	}
	if w.write {
		names = append(names, "delete_file", "mkdir", "write_file")
	}
	return names
}

func (w *workspace) Attr(name string) (starlark.Value, error) {
	if w.read {
		switch name {
		case "read_file":
			return starlark.NewBuiltin("workspace.read_file", w.readFile), nil
		case "exists":
			return starlark.NewBuiltin("workspace.exists", w.exists), nil
		}
	}
	if w.write {
		switch name {
		case "mkdir":
			return starlark.NewBuiltin("workspace.mkdir", w.mkdir), nil
		case "write_file":
			return starlark.NewBuiltin("workspace.write_file", w.writeFile), nil
		case "delete_file":
			return starlark.NewBuiltin("workspace.delete_file", w.deleteFile), nil
		}
	}
	return nil, nil
}

func (w *workspace) checkPath(path string) error {
	for _, denied := range w.deniedFiles {
		if filepath.Clean(path) == filepath.Clean(denied) {
			return fmt.Errorf("%s: %w", path, os.ErrPermission)
		}
		info, err := w.root.Stat(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		protected, err := w.root.Stat(denied)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if info != nil && protected != nil && os.SameFile(info, protected) {
			return fmt.Errorf("%s: %w", path, os.ErrPermission)
		}
	}
	return nil
}

func (w *workspace) readFile(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var path string
	if err := starlark.UnpackArgs("workspace.read_file", args, kwargs, "path", &path); err != nil {
		return nil, err
	}
	if err := w.checkPath(path); err != nil {
		return nil, err
	}
	contents, err := w.root.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return starlark.String(contents), nil
}

func (w *workspace) mkdir(_ *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var path string
	if err := starlark.UnpackArgs(fn.Name(), args, kwargs, "path", &path); err != nil {
		return nil, err
	}
	if err := w.checkPath(path); err != nil {
		return nil, err
	}
	return starlark.None, w.root.MkdirAll(path, 0755)
}

func (w *workspace) writeFile(_ *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var path, contents string
	if err := starlark.UnpackArgs(fn.Name(), args, kwargs, "path", &path, "contents", &contents); err != nil {
		return nil, err
	}
	if err := w.checkPath(path); err != nil {
		return nil, err
	}
	return starlark.None, w.root.WriteFile(path, []byte(contents), 0644)
}

func (w *workspace) deleteFile(_ *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var path string
	if err := starlark.UnpackArgs(fn.Name(), args, kwargs, "path", &path); err != nil {
		return nil, err
	}
	if err := w.checkPath(path); err != nil {
		return nil, err
	}
	return starlark.None, w.root.Remove(path)
}

func (w *workspace) exists(_ *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var path string
	if err := starlark.UnpackArgs(fn.Name(), args, kwargs, "path", &path); err != nil {
		return nil, err
	}
	if err := w.checkPath(path); err != nil {
		return nil, err
	}
	_, err := w.root.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return starlark.False, nil
	}
	return starlark.Bool(err == nil), err
}

var _ starlark.HasAttrs = (*workspace)(nil)
