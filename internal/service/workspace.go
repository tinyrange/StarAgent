package service

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
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
	"list_dir":    "workspace.list_dir(path: str) -> list[str]: Sorted workspace-relative paths of immediate entries, including denied files.",
	"glob":        "workspace.glob(pattern: str) -> list[str]: Sorted workspace-relative matches; ** recurses. Includes denied files; does not follow directory symlinks.",
	"mkdir":       "workspace.mkdir(path: str) -> None: Create directory and parents; existing directories are OK.",
	"write_file":  "workspace.write_file(path: str, contents: str) -> None: Create or overwrite a file; parent must exist.",
	"delete_file": "workspace.delete_file(path: str) -> None: Remove a file or empty directory; missing paths raise.",
}

func (w *workspace) Doc() string {
	var doc strings.Builder
	doc.WriteString(fmt.Sprintf("workspace(root=%q, read=%t, write=%t, denied_files=%q)\n", w.root.Name(), w.read, w.write, w.deniedFiles))
	doc.WriteString("Paths are relative to root. Outside paths raise errors. Denied files block writes, not reads.\n")
	for _, name := range w.AttrNames() {
		doc.WriteString(workspaceMethodDocs[name])
		doc.WriteString("\n")
	}
	return doc.String()
}

func (w *workspace) AttrNames() []string {
	var names []string
	if w.read {
		names = append(names, "exists", "glob", "list_dir", "read_file")
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
		case "list_dir":
			builtin = starlark.NewBuiltin("workspace.list_dir", w.listDir)
		case "glob":
			builtin = starlark.NewBuiltin("workspace.glob", w.glob)
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

func (w *workspace) checkWrite(path string) error {
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
	if err := w.checkWrite(path); err != nil {
		return nil, err
	}
	return starlark.None, w.root.MkdirAll(path, 0755)
}

func (w *workspace) writeFile(_ *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var path, contents string
	if err := starlark.UnpackArgs(fn.Name(), args, kwargs, "path", &path, "contents", &contents); err != nil {
		return nil, err
	}
	if err := w.checkWrite(path); err != nil {
		return nil, err
	}
	return starlark.None, w.root.WriteFile(path, []byte(contents), 0644)
}

func (w *workspace) deleteFile(_ *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var path string
	if err := starlark.UnpackArgs(fn.Name(), args, kwargs, "path", &path); err != nil {
		return nil, err
	}
	if err := w.checkWrite(path); err != nil {
		return nil, err
	}
	return starlark.None, w.root.Remove(path)
}

func (w *workspace) exists(_ *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var path string
	if err := starlark.UnpackArgs(fn.Name(), args, kwargs, "path", &path); err != nil {
		return nil, err
	}
	_, err := w.root.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return starlark.False, nil
	}
	return starlark.Bool(err == nil), err
}

func (w *workspace) listDir(_ *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var dir string
	if err := starlark.UnpackArgs(fn.Name(), args, kwargs, "path", &dir); err != nil {
		return nil, err
	}
	entries, err := fs.ReadDir(w.root.FS(), dir)
	if err != nil {
		return nil, err
	}
	values := make([]starlark.Value, 0, len(entries))
	for _, entry := range entries {
		values = append(values, starlark.String(path.Join(dir, entry.Name())))
	}
	return starlark.NewList(values), nil
}

func (w *workspace) glob(_ *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var pattern string
	if err := starlark.UnpackArgs(fn.Name(), args, kwargs, "pattern", &pattern); err != nil {
		return nil, err
	}
	pattern = strings.TrimPrefix(pattern, "./")
	if !fs.ValidPath(pattern) {
		return nil, fmt.Errorf("glob: pattern must be workspace-relative without '..'")
	}
	if _, err := path.Match(pattern, ""); err != nil {
		return nil, err
	}
	parts := strings.Split(pattern, "/")
	var matches []string
	err := fs.WalkDir(w.root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err == nil && name != "." && globMatch(parts, strings.Split(name, "/")) {
			matches = append(matches, name)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	slices.Sort(matches)
	values := make([]starlark.Value, 0, len(matches))
	for _, name := range matches {
		values = append(values, starlark.String(name))
	}
	return starlark.NewList(values), nil
}

func globMatch(pattern, name []string) bool {
	if len(pattern) == 0 {
		return len(name) == 0
	}
	if pattern[0] == "**" {
		return globMatch(pattern[1:], name) || (len(name) > 0 && globMatch(pattern, name[1:]))
	}
	if len(name) == 0 {
		return false
	}
	matched, _ := path.Match(pattern[0], name[0]) // Syntax is validated before walking.
	return matched && globMatch(pattern[1:], name[1:])
}

var _ starlark.HasAttrs = (*workspace)(nil)
