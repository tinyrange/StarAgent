package service

import (
	"flag"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"go.starlark.net/starlark"
	"gopkg.in/yaml.v3"

	"github.com/tinyrange/staragent/internal/repl"
)

type agentsFile struct {
	Version     int                     `yaml:"version"`
	Environemnt map[string]string       `yaml:"environment"`
	Imports     map[string]importConfig `yaml:"imports"`
	Workflows   *bool                   `yaml:"workflows"`
	Workspace   struct {
		Read        bool     `yaml:"read"`
		Write       bool     `yaml:"write"`
		DeniedFiles []string `yaml:"denied_files"`
	} `yaml:"workspace"`
}

const agentsFileVersion = 1

var starlarkOptions = repl.FileOptions()

func loadAgentsYaml(root *os.Root, contents []byte) (starlark.StringDict, error) {
	var agents agentsFile
	if err := yaml.Unmarshal(contents, &agents); err != nil {
		return nil, fmt.Errorf("failed to parse file: %w", err)
	}

	if agents.Version != agentsFileVersion {
		return nil, fmt.Errorf("unexpected version: expected %d got %d", agentsFileVersion, agents.Version)
	}

	ret := starlark.StringDict{"help": &documentedBuiltin{
		Builtin: starlark.NewBuiltin("help", repl.Help),
		doc:     "help(object) -> str: Return documentation without executing the object.",
	}}
	ret["json"] = repl.JSON
	ret["print"] = repl.Print
	if agents.Workspace.Read || agents.Workspace.Write {
		ret["workspace"] = &workspace{
			root: root, read: agents.Workspace.Read, write: agents.Workspace.Write,
			deniedFiles: agents.Workspace.DeniedFiles,
		}
	}

	for _, name := range slices.Sorted(maps.Keys(agents.Imports)) {
		module, err := loadImport(root, name, agents.Imports[name])
		if err != nil {
			return nil, fmt.Errorf("import %s: %w", name, err)
		}
		ret[name] = module
	}

	for name, script := range agents.Environemnt {
		thread := &starlark.Thread{Name: "AGENTS.yml"}

		// Functions retain these private bindings; the REPL only gets exports.
		env := maps.Clone(ret)
		env["privileged"] = &privileged{cwd: root.Name()}
		result, err := starlark.ExecFileOptions(&starlarkOptions, thread, fmt.Sprintf("AGENTS:%s", name), script, env)
		if err != nil {
			return nil, fmt.Errorf("failed to execute environment script: %w", err)
		}

		if _, ok := result[name]; !ok {
			return nil, fmt.Errorf("failed to find %s in environment script", name)
		}

		ret[name] = result[name]
	}
	if agents.Workflows == nil || *agents.Workflows {
		if err := loadWorkflows(root, ret); err != nil {
			return nil, fmt.Errorf("workflows: %w", err)
		}
	}

	return ret, nil
}

type documentedBuiltin struct {
	*starlark.Builtin
	doc string
}

func (b *documentedBuiltin) Doc() string { return b.doc }

func Run() error {
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)

	cwd := fs.String("cwd", ".", "the directory to load AGENTS.yml from")

	if err := fs.Parse(os.Args[1:]); err != nil {
		return fmt.Errorf("failed to parse args: %w", err)
	}

	return RunWorkspace(*cwd)
}

// RunWorkspace serves a workspace over the standard input/output protocol.
func RunWorkspace(cwd string) error {
	root, err := os.OpenRoot(cwd)
	if err != nil {
		return fmt.Errorf("failed to open workspace: %w", err)
	}
	defer root.Close()

	agents, err := root.ReadFile("AGENTS.yml")
	if err != nil {
		return fmt.Errorf("failed to read AGENTS.yml: %w", err)
	}

	env, err := loadAgentsYaml(root, agents)
	if err != nil {
		return fmt.Errorf("failed to load AGENTS.yml: %w", err)
	}

	var documentation []string
	for _, name := range env.Keys() {
		doc := env[name].Type()
		if documented, ok := env[name].(interface{ Doc() string }); ok {
			if text := strings.TrimSpace(documented.Doc()); text != "" {
				doc = text
			}
		}
		documentation = append(documentation, name+":\n"+doc)
	}

	opts := repl.Options{
		SyntaxOptions: &starlarkOptions,
		Thread:        &starlark.Thread{Name: "service repl"},
		Environment:   env,
		Documentation: strings.Join(documentation, "\n\n"),
	}
	if workflows, ok := env["workflows"].(*workflows); ok {
		defer workflows.root.Close()
		opts.Prepare = workflows.prepare
	}
	return repl.Start(opts)
}
