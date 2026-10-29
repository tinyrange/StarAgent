package service

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
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
	"read_file":   "workspace.read_file(path, start_line=1, end_line=None) -> {ok, path, text, start_line, end_line, truncated}: Inclusive 1-based lines; omitted end_line reads up to 200 lines. end_line is the last returned line (start_line-1 if empty); truncated means more lines follow. Original line endings are preserved.",
	"exists":      "workspace.exists(path) -> {ok, path, exists}: exists=False if missing; other errors raise.",
	"list_dir":    "workspace.list_dir(path=\".\") -> {ok, paths}: Sorted workspace-relative paths of immediate entries, including denied files. Defaults to the workspace root.",
	"glob":        "workspace.glob(pattern) -> {ok, paths}: Sorted workspace-relative matches; ** recurses. Includes denied files; does not follow directory symlinks.",
	"search":      "workspace.search(pattern, query, limit=100) -> {ok, matches, truncated}: Go regex over glob-matched files, including denied files. matches contains {path, line, text} per matching line, sorted by path and 1-based line number. text excludes line endings; truncated means more matches exist. limit must be positive.",
	"mkdir":       "workspace.mkdir(path) -> {ok, path}: Create directory and parents; existing directories are OK.",
	"write_file":  "workspace.write_file(path, contents) -> {ok, path}: Create or overwrite a file; parent must exist.",
	"edit_file":   "workspace.edit_file(path, old, new) -> {ok, path}: Replace exactly one occurrence; empty, missing, or ambiguous old text raises without changing the file.",
	"delete_file": "workspace.delete_file(path) -> {ok, path}: Remove a file or empty directory; missing paths raise.",
}

func (w *workspace) Doc() string {
	var doc strings.Builder
	fmt.Fprintf(&doc, "workspace: existing workspace object\nConfig: root=%q, read=%t, write=%t\n", w.root.Name(), w.read, w.write)
	doc.WriteString("Paths are relative to root. All operations return result objects with ok=True; failures raise. Outside paths raise errors. Denied files block writes, not reads.\n")
	for _, name := range w.AttrNames() {
		doc.WriteString(workspaceMethodDocs[name])
		doc.WriteString("\n")
	}
	return doc.String()
}

func (w *workspace) AttrNames() []string {
	var names []string
	if w.read {
		names = append(names, "exists", "glob", "list_dir", "read_file", "search")
	}
	if w.write {
		names = append(names, "delete_file", "edit_file", "mkdir", "write_file")
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
		case "search":
			builtin = starlark.NewBuiltin("workspace.search", w.search)
		}
	}
	if w.write {
		switch name {
		case "mkdir":
			builtin = starlark.NewBuiltin("workspace.mkdir", w.mkdir)
		case "write_file":
			builtin = starlark.NewBuiltin("workspace.write_file", w.writeFile)
		case "edit_file":
			builtin = starlark.NewBuiltin("workspace.edit_file", w.editFile)
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
	start := 1
	var endValue starlark.Value = starlark.None
	if err := starlark.UnpackArgs("workspace.read_file", args, kwargs, "path", &path, "start_line?", &start, "end_line?", &endValue); err != nil {
		return nil, err
	}
	end := start + 199
	if endValue != starlark.None {
		if err := starlark.AsInt(endValue, &end); err != nil {
			return nil, err
		}
	}
	if start < 1 || end < start {
		return nil, fmt.Errorf("read_file: require 1 <= start_line <= end_line")
	}
	contents, err := w.root.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var text strings.Builder
	number, last, truncated := 0, start-1, false
	for line := range strings.Lines(string(contents)) {
		number++
		if number < start {
			continue
		}
		if number > end {
			truncated = true
			break
		}
		text.WriteString(line)
		last = number
	}
	return workspaceResult(starlark.StringDict{
		"path": starlark.String(path), "text": starlark.String(text.String()),
		"start_line": starlark.MakeInt(start), "end_line": starlark.MakeInt(last), "truncated": starlark.Bool(truncated),
	}, nil)
}

func (w *workspace) mkdir(_ *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var path string
	if err := starlark.UnpackArgs(fn.Name(), args, kwargs, "path", &path); err != nil {
		return nil, err
	}
	if err := w.checkWrite(path); err != nil {
		return nil, err
	}
	return workspaceResult(starlark.StringDict{"path": starlark.String(path)}, w.root.MkdirAll(path, 0755))
}

func (w *workspace) writeFile(_ *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var path, contents string
	if err := starlark.UnpackArgs(fn.Name(), args, kwargs, "path", &path, "contents", &contents); err != nil {
		return nil, err
	}
	if err := w.checkWrite(path); err != nil {
		return nil, err
	}
	return workspaceResult(starlark.StringDict{"path": starlark.String(path)}, w.root.WriteFile(path, []byte(contents), 0644))
}

func (w *workspace) editFile(_ *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var path, old, replacement string
	if err := starlark.UnpackArgs(fn.Name(), args, kwargs, "path", &path, "old", &old, "new", &replacement); err != nil {
		return nil, err
	}
	if old == "" {
		return nil, fmt.Errorf("edit_file: old text must not be empty")
	}
	if err := w.checkWrite(path); err != nil {
		return nil, err
	}
	contents, err := w.root.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := string(contents)
	index := strings.Index(text, old)
	if index < 0 || strings.Contains(text[index+1:], old) {
		return nil, fmt.Errorf("edit_file: old text must match exactly once")
	}
	updated := text[:index] + replacement + text[index+len(old):]
	return workspaceResult(starlark.StringDict{"path": starlark.String(path)}, w.root.WriteFile(path, []byte(updated), 0644))
}

func (w *workspace) deleteFile(_ *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var path string
	if err := starlark.UnpackArgs(fn.Name(), args, kwargs, "path", &path); err != nil {
		return nil, err
	}
	if err := w.checkWrite(path); err != nil {
		return nil, err
	}
	return workspaceResult(starlark.StringDict{"path": starlark.String(path)}, w.root.Remove(path))
}

func (w *workspace) exists(_ *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var path string
	if err := starlark.UnpackArgs(fn.Name(), args, kwargs, "path", &path); err != nil {
		return nil, err
	}
	_, err := w.root.Stat(path)
	exists := err == nil
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	return workspaceResult(starlark.StringDict{"path": starlark.String(path), "exists": starlark.Bool(exists)}, err)
}

func (w *workspace) listDir(_ *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	dir := "."
	if err := starlark.UnpackArgs(fn.Name(), args, kwargs, "path?", &dir); err != nil {
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
	return workspaceResult(starlark.StringDict{"paths": starlark.NewList(values)}, nil)
}

func (w *workspace) glob(_ *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var pattern string
	if err := starlark.UnpackArgs(fn.Name(), args, kwargs, "pattern", &pattern); err != nil {
		return nil, err
	}
	matches, err := w.globPaths(pattern)
	if err != nil {
		return nil, err
	}
	values := make([]starlark.Value, 0, len(matches))
	for _, name := range matches {
		values = append(values, starlark.String(name))
	}
	return workspaceResult(starlark.StringDict{"paths": starlark.NewList(values)}, nil)
}

func (w *workspace) globPaths(pattern string) ([]string, error) {
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
	return matches, nil
}

func (w *workspace) search(_ *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var pattern, query string
	limit := 100
	if err := starlark.UnpackArgs(fn.Name(), args, kwargs, "pattern", &pattern, "query", &query, "limit?", &limit); err != nil {
		return nil, err
	}
	if limit < 1 {
		return nil, fmt.Errorf("search: limit must be positive")
	}
	expression, err := regexp.Compile(query)
	if err != nil {
		return nil, err
	}
	paths, err := w.globPaths(pattern)
	if err != nil {
		return nil, err
	}
	var matches []starlark.Value
	truncated := false
search:
	for _, name := range paths {
		info, err := w.root.Stat(name)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		contents, err := w.root.ReadFile(name)
		if err != nil {
			return nil, err
		}
		number := 0
		for line := range strings.Lines(string(contents)) {
			number++
			line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
			if expression.MatchString(line) {
				if len(matches) == limit {
					truncated = true
					break search
				}
				matches = append(matches, starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{
					"path": starlark.String(name), "line": starlark.MakeInt(number), "text": starlark.String(line),
				}))
			}
		}
	}
	return workspaceResult(starlark.StringDict{"matches": starlark.NewList(matches), "truncated": starlark.Bool(truncated)}, nil)
}

func workspaceResult(fields starlark.StringDict, err error) (starlark.Value, error) {
	if err != nil {
		return nil, err
	}
	fields["ok"] = starlark.True
	return starlarkstruct.FromStringDict(starlarkstruct.Default, fields), nil
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
