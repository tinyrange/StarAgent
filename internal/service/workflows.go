package service

import (
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/tinyrange/staragent/internal/repl"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
	"go.starlark.net/syntax"
)

type workflows struct {
	*workspace
	env, base, loaded, pending starlark.StringDict
}

func (*workflows) String() string { return "workflows" }
func (*workflows) Type() string   { return "workflows" }

// Each file is a struct. Placeholders allow functions in different files to
// refer to one another without imposing a file-loading order.
type workflow struct {
	*starlarkstruct.Struct
	name, file string
}

func (w *workflow) Doc() string {
	var doc strings.Builder
	fmt.Fprintf(&doc, "%s: workflow from .staragent/%s\n", w.name, w.file)
	for _, name := range w.AttrNames() {
		value, _ := w.Attr(name)
		if fn, ok := value.(*starlark.Function); ok {
			fmt.Fprintf(&doc, "%s.%s\n", w.name, repl.FunctionDoc(fn))
		} else {
			fmt.Fprintf(&doc, "%s.%s: %s\n", w.name, name, value.Type())
		}
	}
	return strings.TrimSpace(doc.String())
}

func loadWorkflows(root *os.Root, env starlark.StringDict) error {
	if env.Has("workflows") {
		return fmt.Errorf("global workflows already exists")
	}
	if err := root.MkdirAll(".staragent", 0755); err != nil {
		return err
	}
	directory, err := root.OpenRoot(".staragent")
	if err != nil {
		return err
	}
	w := &workflows{workspace: &workspace{root: directory, read: true, write: true}, env: env}
	env["workflows"] = w
	w.base = maps.Clone(env)
	sources, err := w.sources()
	if err == nil {
		w.pending, err = w.build(sources)
	}
	if err != nil {
		directory.Close()
		return err
	}
	w.install()
	return nil
}

func (w *workflows) Doc() string {
	var doc strings.Builder
	doc.WriteString("workflows: reusable Starlark files in .staragent; enabled unless AGENTS.yml sets workflows: false.\n")
	doc.WriteString("Save functions with workflows.write_file(\"host.star\", r'''def world(name):\n    return \"Hello, \" + name\n'''); call host.world(\"World\") on the next exec. Direct def/lambda in service calls is disabled.\n")
	doc.WriteString("Each .star file loads as a struct named after its filename. Names must be identifiers and cannot conflict with existing globals or other files. Files may contain functions, literal constants, and docstrings; defaults must be literals. Put actions inside functions. No load or privileged access; use configured service APIs and pass session values as arguments.\n")
	doc.WriteString("Writes/edits/deletes validate all workflow files before changing disk; failed validation preserves the working version. Successful changes take effect on the next call. Existing files load at startup; use workflows methods for live updates. help(host) lists members; help(host.world) shows its signature and docstring.\n")
	for _, name := range w.AttrNames() {
		doc.WriteString(strings.ReplaceAll(workspaceMethodDocs[name], "workspace", "workflows"))
		doc.WriteByte('\n')
	}
	doc.WriteString("Paths are relative to .staragent. write_file/edit_file/delete_file operate on .star files.\n")
	fmt.Fprintf(&doc, "Loaded workflows: %s\n", strings.Join(w.loaded.Keys(), ", "))
	return strings.TrimSpace(doc.String())
}

func (w *workflows) Attr(name string) (starlark.Value, error) {
	if !slices.Contains(w.AttrNames(), name) {
		return nil, nil
	}
	fn := starlark.NewBuiltin("workflows."+name, func(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		switch name {
		case "write_file", "edit_file", "delete_file":
			return w.change(name, args, kwargs)
		default:
			method, err := w.workspace.Attr(name)
			if err != nil {
				return nil, err
			}
			return starlark.Call(thread, method, args, kwargs)
		}
	})
	return &documentedBuiltin{Builtin: fn, doc: strings.ReplaceAll(workspaceMethodDocs[name], "workspace", "workflows")}, nil
}

func (w *workflows) sources() (map[string]string, error) {
	sources := map[string]string{}
	err := fs.WalkDir(w.root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || path.Ext(name) != ".star" {
			return nil
		}
		contents, err := w.root.ReadFile(name)
		if err == nil {
			sources[name] = string(contents)
		}
		return err
	})
	return sources, err
}

func (w *workflows) build(sources map[string]string) (starlark.StringDict, error) {
	modules := starlark.StringDict{}
	env := maps.Clone(w.base)
	for _, file := range slices.Sorted(maps.Keys(sources)) {
		name := strings.TrimSuffix(path.Base(file), ".star")
		expr, err := starlarkOptions.ParseExpr(file, name, 0)
		id, ok := expr.(*syntax.Ident)
		if err != nil || !ok || id.Name != name {
			return nil, fmt.Errorf("%s: filename must be a Starlark identifier", file)
		}
		if env.Has(name) || starlark.Universe.Has(name) || (w.env.Has(name) && w.env[name] != w.loaded[name]) {
			return nil, fmt.Errorf("%s: global %q already exists", file, name)
		}
		if previous, ok := w.loaded[name].(*workflow); ok && previous.file != file {
			return nil, fmt.Errorf("%s: global %q belongs to %s", file, name, previous.file)
		}
		module := &workflow{name: name, file: file}
		modules[name], env[name] = module, module
	}
	for _, name := range modules.Keys() {
		module := modules[name].(*workflow)
		file, err := starlarkOptions.Parse(".staragent/"+module.file, sources[module.file], 0)
		if err != nil {
			return nil, err
		}
		if err := validateWorkflow(file); err != nil {
			return nil, err
		}
		program, err := starlark.FileProgram(file, env.Has)
		if err != nil {
			return nil, err
		}
		globals, err := program.Init(&starlark.Thread{Name: module.file}, env)
		if err != nil {
			return nil, err
		}
		globals.Freeze()
		module.Struct = starlarkstruct.FromStringDict(starlark.String(name), globals)
	}
	return modules, nil
}

// Only inert expressions are evaluated while saving. Function bodies are
// compiled but never called, including when validating a replacement file.
func literal(expr syntax.Expr) bool {
	valid := true
	syntax.Walk(expr, func(node syntax.Node) bool {
		switch node := node.(type) {
		case nil, *syntax.Literal, *syntax.ListExpr, *syntax.TupleExpr, *syntax.DictExpr, *syntax.DictEntry, *syntax.ParenExpr, *syntax.UnaryExpr:
		case *syntax.Ident:
			valid = valid && (node.Name == "True" || node.Name == "False" || node.Name == "None")
		default:
			valid = false
		}
		return valid
	})
	return valid
}

func validateWorkflow(file *syntax.File) error {
	var err error
	syntax.Walk(file, func(node syntax.Node) bool {
		if _, ok := node.(*syntax.LoadStmt); ok {
			err = fmt.Errorf("%s: load is not available in workflows", syntax.Start(node))
		}
		return err == nil
	})
	if err != nil {
		return err
	}
	for _, stmt := range file.Stmts {
		valid := false
		switch stmt := stmt.(type) {
		case *syntax.DefStmt:
			valid = true
			for _, param := range stmt.Params {
				if defaultValue, ok := param.(*syntax.BinaryExpr); ok {
					valid = valid && literal(defaultValue.Y)
				}
			}
		case *syntax.AssignStmt:
			_, simple := stmt.LHS.(*syntax.Ident)
			valid = simple && stmt.Op == syntax.EQ && literal(stmt.RHS)
		case *syntax.ExprStmt:
			value, ok := stmt.X.(*syntax.Literal)
			valid = ok && value.Token == syntax.STRING
		}
		if !valid {
			return fmt.Errorf("%s: workflows allow functions, literal constants/defaults, and docstrings; put actions inside functions", syntax.Start(stmt))
		}
	}
	return nil
}

func (w *workflows) change(method string, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var file, contents, old, replacement string
	params := []any{"path", &file}
	if method == "write_file" {
		params = append(params, "contents", &contents)
	}
	if method == "edit_file" {
		params = append(params, "old", &old, "new", &replacement)
	}
	if err := starlark.UnpackArgs("workflows."+method, args, kwargs, params...); err != nil {
		return nil, err
	}
	file = path.Clean(file)
	if !fs.ValidPath(file) || path.Ext(file) != ".star" {
		return nil, fmt.Errorf("workflows: expected a relative .star file path")
	}
	sources, err := w.sources()
	if err != nil {
		return nil, err
	}
	if method == "edit_file" {
		original, exists := sources[file]
		if !exists {
			return nil, fmt.Errorf("%s: %w", file, os.ErrNotExist)
		}
		index := strings.Index(original, old)
		if old == "" || index < 0 || strings.Contains(original[index+1:], old) {
			return nil, fmt.Errorf("edit_file: old text must match exactly once")
		}
		contents = original[:index] + replacement + original[index+len(old):]
	}
	if method == "delete_file" {
		delete(sources, file)
	} else {
		sources[file] = contents
	}
	next, err := w.build(sources)
	if err != nil {
		return nil, err
	}
	if method == "delete_file" {
		err = w.root.Remove(file)
	} else {
		// Rename a complete file, preserving the previous contents on write failure
		// and replacing destination symlinks rather than following them.
		temporary := path.Join(path.Dir(file), ".workflow-"+uuid.NewString())
		var f *os.File
		f, err = w.root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err == nil {
			defer w.root.Remove(temporary)
			_, err = f.WriteString(contents)
			closeErr := f.Close()
			if err == nil {
				err = closeErr
			}
			if err == nil {
				err = w.root.Rename(temporary, file)
			}
		}
	}
	if err != nil {
		return nil, err
	}
	w.pending = next
	return workspaceResult(starlark.StringDict{"path": starlark.String(file)}, nil)
}

func (w *workflows) install() {
	if w.pending == nil {
		return
	}
	for name, value := range w.loaded {
		if w.env[name] == value {
			delete(w.env, name)
		}
	}
	maps.Copy(w.env, w.pending)
	w.loaded, w.pending = w.pending, nil
}

func (w *workflows) prepare(file *syntax.File) error {
	w.install()
	var err error
	syntax.Walk(file, func(node syntax.Node) bool {
		switch node.(type) {
		case *syntax.DefStmt, *syntax.LambdaExpr:
			err = fmt.Errorf("%s: save reusable functions with workflows.write_file(\"name.star\", source), then call name.function(...) on the next exec", syntax.Start(node))
		}
		return err == nil
	})
	return err
}
