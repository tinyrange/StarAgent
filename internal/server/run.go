package server

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type serverTestProviderScript struct {
	Service string `yaml:"service"`
	Script  string `yaml:"script"`
}

type serverTestProvider struct {
	Name string `yaml:"name"`
	// for test provider
	Scripts []serverTestProviderScript `yaml:"scripts"`
}

type serverTestService = map[string]any

type serverTestFile struct {
	Version  int                          `yaml:"version"`
	Provider serverTestProvider           `yaml:"provider"`
	Services map[string]serverTestService `yaml:"services"`
}

type serverTestProviderInterface interface {
	runScripts(ctx context.Context, scripts []serverTestProviderScript, callbacks TurnCallbacks) error
}

const serverTestFileVersion = 1

func (s *serverTestFile) execute(server *Server) error {
	// validate version
	if s.Version != serverTestFileVersion {
		return fmt.Errorf("unexpected version: expected %d got %d", serverTestFileVersion, s.Version)
	}

	session, err := server.CreateSession(s.Provider.Name, ProviderConfig{})
	if err != nil {
		return fmt.Errorf("failed to make session: %w", err)
	}
	for name, config := range s.Services {
		if name == "" {
			return fmt.Errorf("service name is reserved for the inbuilt service")
		}
		kind, _ := config["kind"].(string)
		if _, ok := server.serviceFactories[kind]; !ok {
			return fmt.Errorf("service %q: unknown kind %q", name, kind)
		}
	}
	session.serviceConfigs = s.Services

	if test, ok := session.Provider().(serverTestProviderInterface); ok {
		return test.runScripts(context.Background(), s.Provider.Scripts, session)
	} else {
		return fmt.Errorf("serverTestProviderInterface not found, cannot execute test")
	}
}

func Run() (err error) {
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)

	testName := fs.String("run-test", "", "run a specified test YAML file")

	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}

	server := &Server{
		providers:        make(map[string]Provider),
		sessions:         make(map[string]*Session),
		serviceFactories: make(map[string]ServiceFactory),
	}
	defer func() { err = errors.Join(err, server.Close()) }()
	if err := server.RegisterServiceFactory("shell-repl", &shellREPLFactory{}); err != nil {
		return err
	}

	if *testName != "" {
		// add test provider
		if err := server.RegisterProvider("test", &testProvider{}); err != nil {
			return fmt.Errorf("failed to register test provider: %w", err)
		}

		content, err := os.ReadFile(*testName)
		if err != nil {
			return fmt.Errorf("failed to read test file: %w", err)
		}

		var parsed serverTestFile

		if err := yaml.Unmarshal(content, &parsed); err != nil {
			return fmt.Errorf("failed to parse yaml: %w", err)
		}

		if err := parsed.execute(server); err != nil {
			return fmt.Errorf("failed to execute: %w", err)
		}

		return nil
	} else {
		return fmt.Errorf("please specify -run-test <test.yml>")
	}
}
