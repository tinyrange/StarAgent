package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// The server launches itself in service mode, using the same stdio protocol as
// sa-service. No separate executable or source checkout is needed.
type workspaceServiceFactory struct{ command []string }

func (f *workspaceServiceFactory) Create(ctx context.Context, config map[string]any) (_ Service, err error) {
	var options struct {
		TempWorkspace bool              `yaml:"temp_workspace"`
		Files         map[string]string `yaml:"files"`
	}
	data, err := yaml.Marshal(config)
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, &options); err != nil {
		return nil, err
	}
	if !options.TempWorkspace {
		return nil, fmt.Errorf("kind service currently requires temp_workspace: true")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "staragent-*")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.RemoveAll(dir))
		}
	}()
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	for name, contents := range options.Files {
		if !filepath.IsLocal(name) {
			return nil, fmt.Errorf("seed file must be workspace-relative: %q", name)
		}
		if err := root.MkdirAll(filepath.Dir(name), 0700); err != nil {
			return nil, err
		}
		if err := root.WriteFile(name, []byte(contents), 0600); err != nil {
			return nil, err
		}
	}
	// A relative root keeps documentation stable across fresh replay workspaces.
	command := append(append([]string{}, f.command...), ".")
	return &workspaceService{shellREPLService: shellREPLService{args: command, cwd: dir}, dir: dir}, nil
}

type workspaceService struct {
	shellREPLService
	dir string
}

func (s *workspaceService) Close() error {
	return errors.Join(s.shellREPLService.Close(), os.RemoveAll(s.dir))
}
