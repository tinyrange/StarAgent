package service

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tinyrange/staragent/internal/repl"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

func workflowService(t *testing.T, config string, files map[string]string) (repl.Options, *os.Root) {
	t.Helper()
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	for name, text := range files {
		if err := root.MkdirAll(filepath.Dir(name), 0755); err != nil {
			t.Fatal(err)
		}
		if err := root.WriteFile(name, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	env, err := loadAgentsYaml(root, []byte("version: 1\n"+config))
	if err != nil {
		t.Fatal(err)
	}
	opts := repl.Options{SyntaxOptions: &starlarkOptions, Thread: &starlark.Thread{Name: "test"}, Environment: env}
	if w, ok := env["workflows"].(*workflows); ok {
		t.Cleanup(func() { w.root.Close() })
		opts.Prepare = w.prepare
	}
	return opts, root
}

func evaluateWorkflow(t *testing.T, opts repl.Options, code string) string {
	t.Helper()
	var output bytes.Buffer
	if err := repl.Evaluate(opts, code, &output); err != nil {
		t.Fatal(err)
	}
	return output.String()
}

func TestWorkflowLifecycle(t *testing.T) {
	config := "workspace: {read: true, write: true}\nenvironment:\n  greeting: |\n    def greeting(name):\n      return 'Hello, ' + name\n"
	opts, root := workflowService(t, config, map[string]string{
		".staragent/host.star": "def world(name='World'):\n    \"\"\"Greet someone.\"\"\"\n    return greeting(name)\n",
	})
	for _, test := range []struct{ code, want string }{
		{`print(host.world())`, "Hello, World\n"},
		{`print(help(host.world))`, "world(name=\"World\")\nGreet someone.\n"},
		{`print(type(host))`, "struct\n"},
	} {
		if got := evaluateWorkflow(t, opts, test.code); got != test.want {
			t.Fatalf("%s: %q", test.code, got)
		}
	}
	if got := evaluateWorkflow(t, opts, `print(help(host))`); !strings.Contains(got, "host.world(name=") {
		t.Fatal(got)
	}
	source := "PREFIX = 'Saved'\ndef run(name, *args, suffix='!', **kwargs):\n    \"\"\"Call another workflow.\"\"\"\n    return PREFIX + ': ' + host.world(name) + suffix\n"
	if got := evaluateWorkflow(t, opts, `workflows.write_file("caller.star", `+syntax.Quote(source, false)+`); print("saved")`); got != "saved\n" {
		t.Fatal(got)
	}
	if opts.Environment.Has("caller") {
		t.Fatal("published before the next call")
	}
	if got := evaluateWorkflow(t, opts, `print(caller.run("Ada")); print(help(caller.run))`); got != "Saved: Hello, Ada!\nrun(name, *args, suffix=\"!\", **kwargs)\nCall another workflow.\n" {
		t.Fatal(got)
	}
	if got := evaluateWorkflow(t, opts, `workflows.edit_file("host.star", "return greeting(name)", "return greeting(name).upper()")`); strings.Contains(got, "error:") {
		t.Fatal(got)
	}
	if got := evaluateWorkflow(t, opts, `print(caller.run("Ada"))`); got != "Saved: HELLO, ADA!\n" {
		t.Fatal(got)
	}
	// Reload the saved files as a fresh service, after configured exports.
	reloaded, err := loadAgentsYaml(root, []byte("version: 1\n"+config))
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded["workflows"].(*workflows).root.Close()
	restarted := repl.Options{SyntaxOptions: &starlarkOptions, Thread: &starlark.Thread{}, Environment: reloaded}
	if got := evaluateWorkflow(t, restarted, `print(caller.run("Ada"))`); got != "Saved: HELLO, ADA!\n" {
		t.Fatal(got)
	}
	if got := evaluateWorkflow(t, opts, `workflows.write_file("greeting.star", "def run():\n    return 1\n")`); !strings.Contains(got, "already exists") {
		t.Fatal(got)
	}
	// A deletion that would break another workflow is rejected before disk changes.
	if got := evaluateWorkflow(t, opts, `workflows.delete_file("host.star")`); !strings.Contains(got, "undefined: host") {
		t.Fatal(got)
	}
	if _, err := root.Stat(".staragent/host.star"); err != nil {
		t.Fatal(err)
	}
	if got := evaluateWorkflow(t, opts, `workflows.delete_file("caller.star"); workflows.delete_file("host.star")`); strings.Contains(got, "error:") {
		t.Fatal(got)
	}
	evaluateWorkflow(t, opts, `print("removed")`)
	if opts.Environment.Has("host") || opts.Environment.Has("caller") {
		t.Fatal("deleted namespaces remain")
	}
}

func TestWorkflowInvalidUpdatesPreserveFiles(t *testing.T) {
	opts, root := workflowService(t, "workspace: {read: true, write: true}\n", map[string]string{
		".staragent/host.star": "def world():\n    return 'original'\n",
	})
	for _, source := range []string{
		"def world(:\n", "def world():\n    return missing\n",
		"VALUE = -'invalid'\n",
		"def world(value=-'invalid'):\n    return value\n",
		"workspace.write_file('side-effect', 'bad')\n",
		"def world(value=workspace.write_file('side-effect', 'bad')):\n    return value\n",
		"load('//stdlib/golang.star', 'build_check')\n",
		"def world():\n    return privileged.shell('true')\n",
	} {
		got := evaluateWorkflow(t, opts, `workflows.write_file("host.star", `+syntax.Quote(source, false)+`)`)
		if !strings.Contains(got, "error:") {
			t.Fatalf("accepted %q: %s", source, got)
		}
		if got := evaluateWorkflow(t, opts, `print(host.world()); print(workflows.read_file("host.star").text)`); !strings.HasPrefix(got, "original\ndef world():") {
			t.Fatal(got)
		}
	}
	if _, err := root.Stat("side-effect"); !os.IsNotExist(err) {
		t.Fatalf("validation executed actions: %v", err)
	}
	// Actions in function bodies are available only when explicitly invoked.
	code := "def write():\n    return workspace.write_file('side-effect', 'called')\n"
	evaluateWorkflow(t, opts, `workflows.write_file("action.star", `+syntax.Quote(code, false)+`)`)
	if _, err := root.Stat("side-effect"); !os.IsNotExist(err) {
		t.Fatal("save invoked function")
	}
	if got := evaluateWorkflow(t, opts, `print(action.write().ok)`); got != "True\n" {
		t.Fatal(got)
	}
}

func TestWorkflowNamesAndConfinement(t *testing.T) {
	opts, root := workflowService(t, "", nil)
	evaluateWorkflow(t, opts, `occupied = 42`)
	for _, name := range []string{"json.star", "workflows.star", "occupied.star", "len.star", "bad-name.star", "../escape.star"} {
		got := evaluateWorkflow(t, opts, `workflows.write_file(`+syntax.Quote(name, false)+`, "def run():\n    return 1\n")`)
		if !strings.Contains(got, "error:") {
			t.Fatalf("accepted %s: %s", name, got)
		}
	}
	if got := evaluateWorkflow(t, opts, `print(occupied)`); got != "42\n" {
		t.Fatal(got)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "external.star"), []byte("def run():\n    return 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := root.Symlink(outside, ".staragent/outside"); err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{`workflows.read_file("outside/external.star")`, `workflows.write_file("outside/external.star", "def run():\n    return 2\n")`} {
		if got := evaluateWorkflow(t, opts, code); !strings.Contains(got, "error:") {
			t.Fatal(got)
		}
	}
	contents, err := os.ReadFile(filepath.Join(outside, "external.star"))
	if err != nil || !strings.Contains(string(contents), "return 1") {
		t.Fatalf("outside file changed: %s %v", contents, err)
	}
	// Failed disk writes must not publish the validated namespace.
	if got := evaluateWorkflow(t, opts, `workflows.write_file("missing/unsaved.star", "def run():\n    return 1\n")`); !strings.Contains(got, "error:") {
		t.Fatal(got)
	}
	evaluateWorkflow(t, opts, `print("next")`)
	if opts.Environment.Has("unsaved") {
		t.Fatal("published a failed write")
	}
}

func TestWorkflowDefinitionsAndOptOut(t *testing.T) {
	for _, config := range []string{"", "workflows: false\n"} {
		opts, root := workflowService(t, config, nil)
		for _, code := range []string{"def inline():\n    return 1", "callback = lambda: 1", "if True:\n    def inline():\n        return 1"} {
			got := evaluateWorkflow(t, opts, code)
			if config == "" && !strings.Contains(got, "workflows.write_file") {
				t.Fatal(got)
			}
			if config != "" && got != "" {
				t.Fatal(got)
			}
		}
		if config != "" {
			if opts.Environment.Has("workflows") {
				t.Fatal("workflows enabled")
			}
			if _, err := root.Stat(".staragent"); !os.IsNotExist(err) {
				t.Fatalf("created disabled workflow directory: %v", err)
			}
		} else if got := evaluateWorkflow(t, opts, "for x in range(2):\n    print(x)"); got != "0\n1\n" {
			t.Fatal(got)
		}
	}
}

func TestWorkflowStartupValidation(t *testing.T) {
	for _, source := range []string{"def world(:", "load('//stdlib/golang.star', 'build_check')", "def world():\n    return privileged"} {
		root, err := os.OpenRoot(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		root.MkdirAll(".staragent", 0755)
		root.WriteFile(".staragent/host.star", []byte(source), 0644)
		_, err = loadAgentsYaml(root, []byte("version: 1\n"))
		root.Close()
		if err == nil {
			t.Fatalf("startup accepted %s", source)
		}
	}
}
