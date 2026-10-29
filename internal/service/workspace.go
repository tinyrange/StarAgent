package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

var workspaceMethodDocs = map[string]string{
	"read_file":   "workspace.read_file(path: str) -> str: Read file contents.",
	"exists":      "workspace.exists(path: str) -> bool: False if missing; other errors raise.",
	"mkdir":       "workspace.mkdir(path: str) -> None: Create directory and parents; existing directories are OK.",
	"write_file":  "workspace.write_file(path: str, contents: str) -> None: Create or overwrite a file; parent must exist.",
	"delete_file": "workspace.delete_file(path: str) -> None: Remove a file or empty directory; missing paths raise.",
}

func (w *workspace) Doc() string {
	var doc strings.Builder
	doc.WriteString(fmt.Sprintf("workspace(root=%q, read=%t, write=%t, denied_files=%q)\n", w.root.Name(), w.read, w.write, w.deniedFiles))
	doc.WriteString("Paths are relative to root. Outside paths and denied files raise errors.\n")
	for _, name := range w.AttrNames() {
		doc.WriteString(workspaceMethodDocs[name])
		doc.WriteString("\n")
	}
	return doc.String()
}

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
	var builtin *starlark.Builtin
	if w.read {
		switch name {
		case "read_file":
			builtin = starlark.NewBuiltin("workspace.read_file", w.readFile)
		case "exists":
			builtin = starlark.NewBuiltin("workspace.exists", w.exists)
		}
	}
	if w.write {
		switch name {
		case "mkdir":
			builtin = starlark.NewBuiltin("workspace.mkdir", w.mkdir)
		case "write_file":
			builtin = starlark.NewBuiltin("workspace.write_file", w.writeFile)
		case "delete_file":
			builtin = starlark.NewBuiltin("workspace.delete_file", w.deleteFile)
		}
	}
	if builtin == nil {
		return nil, nil
	}
	return &documentedBuiltin{Builtin: builtin, doc: workspaceMethodDocs[name]}, nil
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
