package server

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"time"

	"github.com/tinyrange/staragent/internal/service"

	"gopkg.in/yaml.v3"
)

type serverTestProviderScript struct {
	Service    string `yaml:"service"`
	Script     string `yaml:"script"`
	ShouldFail bool   `yaml:"should_fail"`
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
	inputName := fs.String("input", "", "run a session input YAML file")
	replayName := fs.String("replay", "", "replay a session log offline and check tool results")
	var replayIgnore []*regexp.Regexp
	fs.Func("replay-ignore", "explicit regexp to ignore in replay comparisons only (repeatable; raw recordings remain intact)", func(pattern string) error {
		compiled, err := regexp.Compile(pattern)
		if err == nil {
			replayIgnore = append(replayIgnore, compiled)
		}
		return err
	})
	workspace := fs.String("service-workspace", "", "serve a workspace over stdio (internal)")

	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if *workspace != "" {
		return service.RunWorkspace(*workspace)
	}
	modes := 0
	for _, name := range []string{*testName, *inputName, *replayName} {
		if name != "" {
			modes++
		}
	}
	if modes != 1 {
		return fmt.Errorf("specify one of -run-test, -input, or -replay")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	server := &Server{
		providers:        make(map[string]Provider),
		sessions:         make(map[string]*Session),
		serviceFactories: make(map[string]ServiceFactory),
	}
	defer func() { err = errors.Join(err, server.Close()) }()
	if err := server.RegisterServiceFactory("shell-repl", &shellREPLFactory{}); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if err := server.RegisterServiceFactory("service", &workspaceServiceFactory{command: []string{executable, "-service-workspace"}}); err != nil {
		return err
	}
	if *replayName != "" {
		var log sessionLog
		if err := readSessionFile(*replayName, &log); err != nil {
			return err
		}
		return log.replay(ctx, server, replayIgnore...)
	}
	if *inputName != "" {
		var input sessionInput
		if err := readSessionFile(*inputName, &input); err != nil {
			return err
		}
		if err := server.RegisterProvider("openai", &openAIProvider{
			key: os.Getenv("OPENAI_API_KEY"), endpoint: "https://api.openai.com/v1/responses",
			client: &http.Client{Timeout: 5 * time.Minute},
		}); err != nil {
			return err
		}
		var output *os.File
		output, err = createSessionLog()
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, output.Close()) }()
		fmt.Fprintf(os.Stderr, "Recording session to %s\n", output.Name())
		return input.run(ctx, server, output)
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
