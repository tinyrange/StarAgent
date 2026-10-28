package service

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
	"gopkg.in/yaml.v3"

	"github.com/tinyrange/staragent/internal/repl"
)

type agentsFile struct {
	Version     int               `yaml:"version"`
	Environemnt map[string]string `yaml:"environment"`
}

const agentsFileVersion = 1

var starlarkOptions = syntax.FileOptions{
	Set:             true,
	While:           true,
	TopLevelControl: true,
	GlobalReassign:  true,
	Recursion:       true,
}

func loadAgentsYaml(wd string, contents []byte) (starlark.StringDict, error) {
	var agents agentsFile
	if err := yaml.Unmarshal(contents, &agents); err != nil {
		return nil, fmt.Errorf("failed to parse file: %w", err)
	}

	if agents.Version != agentsFileVersion {
		return nil, fmt.Errorf("unexpected version: expected %d got %d", agentsFileVersion, agents.Version)
	}

	ret := make(starlark.StringDict)

	for name, script := range agents.Environemnt {
		thread := &starlark.Thread{Name: "AGENTS.yml"}

		result, err := starlark.ExecFileOptions(&starlarkOptions, thread, fmt.Sprintf("AGENTS:%s", name), script, ret)
		if err != nil {
			return nil, fmt.Errorf("failed to execute environment script: %w", err)
		}

		if _, ok := result[name]; !ok {
			return nil, fmt.Errorf("failed to find %s in environment script", name)
		}

		ret[name] = result[name]
	}

	return ret, nil
}

func Run() error {
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)

	cwd := fs.String("cwd", ".", "the directory to load AGENTS.yml from")

	if err := fs.Parse(os.Args[1:]); err != nil {
		return fmt.Errorf("failed to parse args: %w", err)
	}

	wd, err := filepath.Abs(*cwd)
	if err != nil {
		return fmt.Errorf("failed to resolve path: %w", err)
	}

	agents, err := os.ReadFile(filepath.Join(wd, "AGENTS.yml"))
	if err != nil {
		return fmt.Errorf("failed to read AGENTS.yml: %w", err)
	}

	env, err := loadAgentsYaml(wd, agents)
	if err != nil {
		return fmt.Errorf("failed to load AGENTS.yml: %w", err)
	}

	return repl.Start(repl.Options{
		SyntaxOptions: &starlarkOptions,
		Thread:        &starlark.Thread{Name: "service repl"},
		Environment:   env,
	})
}
