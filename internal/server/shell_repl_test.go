package server

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/tinyrange/staragent/internal/repl"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// Run the real REPL in a child test process without building another binary.
func TestShellREPLProcess(t *testing.T) {
	mode := os.Args[len(os.Args)-1]
	switch mode {
	case "staragent-invalid-handshake":
		fmt.Print("invalid handshake")
		io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	case "staragent-repl":
		err := repl.Start(repl.Options{
			SyntaxOptions: &syntax.FileOptions{GlobalReassign: true},
			Thread:        &starlark.Thread{Name: "test service"},
			Environment:   make(starlark.StringDict),
			Documentation: "Test service documentation",
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
}

func helperConfig(t *testing.T, mode string) map[string]any {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{
		"kind": "shell-repl",
		"args": []any{executable, "-test.run=^TestShellREPLProcess$", "--", mode},
	}
}

func TestSessionShellREPL(t *testing.T) {
	server := &Server{
		providers:        map[string]Provider{"test": &testProvider{}},
		sessions:         make(map[string]*Session),
		serviceFactories: make(map[string]ServiceFactory),
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := server.RegisterServiceFactory("shell-repl", &shellREPLFactory{}); err != nil {
		t.Fatal(err)
	}
	session, err := server.CreateSession("test", ProviderConfig{})
	if err != nil {
		t.Fatal(err)
	}
	session.serviceConfigs = map[string]map[string]any{"hello": helperConfig(t, "staragent-repl")}
	ctx := context.Background()
	for _, tt := range []struct{ service, script, want string }{
		{"", `print("built in")`, "built in\n"},
		{"hello", `test = "World"`, ""},
		{"hello", `print("Hello, " + test)`, "Hello, World\n"},
		{"hello", `test = "again"`, ""},
		{"hello", `print(test)`, "again\n"},
	} {
		got, err := session.OnToolCall(ctx, tt.service, tt.script)
		if err != nil || got != tt.want {
			t.Fatalf("%s: got %q, %v; want %q", tt.script, got, err, tt.want)
		}
		if tt.service == "" && len(session.services) != 1 {
			t.Fatal("shell service started before its first call")
		}
	}
	service := session.services["hello"].(*shellREPLService)
	if doc, err := service.Documentation(ctx); err != nil || doc != "Test service documentation" {
		t.Fatalf("documentation: %q, %v", doc, err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if service.cmd.ProcessState == nil || !service.cmd.ProcessState.Success() {
		t.Fatal("child was not reaped after a clean exit")
	}
}

func TestShellREPLInvalidConfig(t *testing.T) {
	for _, args := range []any{nil, "echo", []any{}, []any{""}, []any{"echo", 42}} {
		if _, err := (&shellREPLFactory{}).Create(context.Background(), map[string]any{"args": args}); err == nil {
			t.Errorf("accepted invalid args: %#v", args)
		}
	}
}

func TestShellREPLFailedInit(t *testing.T) {
	for _, mode := range []string{"missing-command", "staragent-invalid-handshake"} {
		t.Run(mode, func(t *testing.T) {
			config := helperConfig(t, mode)
			if mode == "missing-command" {
				config["args"] = []string{t.TempDir() + "/missing-command"}
			}
			service, err := (&shellREPLFactory{}).Create(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			session := &Session{services: make(map[string]Service)}
			if err := session.RegisterService(context.Background(), "broken", service); err == nil {
				t.Fatal("initialization unexpectedly succeeded")
			}
			if len(session.services) != 0 {
				t.Fatal("failed service was retained in the session")
			}
			child := service.(*shellREPLService)
			if child.cmd.Process != nil && child.cmd.ProcessState == nil {
				t.Fatal("failed child was not reaped")
			}
		})
	}
}

func TestHelloFile(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "run", "./cmd/sa-server", "-run-test", "tests/hello.yml")
	cmd.Dir = "../.."
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("hello.yml: %v\n%s", err, output)
	}
	if got, want := strings.ReplaceAll(string(output), "\r\n", "\n"), "Hello, World\nHello, World\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
