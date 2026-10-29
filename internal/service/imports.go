package service

import (
	"embed"
	"fmt"
	"os"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
)

//go:embed stdlib/*.star
var standardLibrary embed.FS

type importConfig struct {
	Path  string          `yaml:"path"`
	Tools map[string]bool `yaml:"tools"`
}

type importedModule struct {
	*starlarkstruct.Module
	path string
}

func (m *importedModule) Doc() string {
	var doc strings.Builder
	fmt.Fprintf(&doc, "%s (import %q)\n", m.Name, m.path)
	for _, name := range m.AttrNames() {
		function := m.Members[name].(*starlark.Function)
		fmt.Fprintf(&doc, "%s.%s\n", m.Name, strings.TrimSpace(function.Doc()))
	}
	return doc.String()
}

func loadImport(root *os.Root, name string, config importConfig) (starlark.Value, error) {
	if !strings.HasPrefix(config.Path, "//stdlib/") {
		return nil, fmt.Errorf("unsupported import path %q", config.Path)
	}
	source, err := standardLibrary.ReadFile(strings.TrimPrefix(config.Path, "//"))
	if err != nil {
		return nil, err
	}
	globals, err := starlark.ExecFileOptions(&starlarkOptions, &starlark.Thread{Name: config.Path}, config.Path, source, starlark.StringDict{
		"privileged": &privileged{cwd: root.Name()},
		"struct":     starlark.NewBuiltin("struct", starlarkstruct.Make),
	})
	if err != nil {
		return nil, err
	}
	members := make(starlark.StringDict)
	for tool, enabled := range config.Tools {
		if !enabled {
			continue
		}
		function, ok := globals[tool].(*starlark.Function)
		if !ok {
			return nil, fmt.Errorf("unknown tool %q", tool)
		}
		members[tool] = function
	}
	return &importedModule{Module: &starlarkstruct.Module{Name: name, Members: members}, path: config.Path}, nil
}
