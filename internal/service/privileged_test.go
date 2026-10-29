package service

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"go.starlark.net/starlark"
)

func TestCommandChild(t *testing.T) {
	if os.Getenv("STARAGENT_COMMAND_CHILD") != "1" {
		return
	}
	fmt.Fprint(os.Stdout, "before timeout\n")
	fmt.Fprint(os.Stderr, "diagnostic\n")
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

func TestTimeoutRetainsBothStreams(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range []bool{false, true} {
		c := shellCommand{argv: []string{exe, "-test.run=^TestCommandChild$"}, cwd: t.TempDir(), env: []string{"STARAGENT_COMMAND_CHILD=1"}, timeout: 500 * time.Millisecond}
		if output {
			_, err = c.output(nil, starlark.NewBuiltin("output", c.output), nil, nil)
			if err == nil || !strings.Contains(err.Error(), "before timeout") || !strings.Contains(err.Error(), "diagnostic") {
				t.Fatalf("lost output: %v", err)
			}
		} else {
			value, err := c.run(nil, starlark.NewBuiltin("run", c.run), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			attrs := value.(starlark.HasAttrs)
			for name, want := range map[string]starlark.Value{"ok": starlark.False, "timed_out": starlark.True, "exit_code": starlark.MakeInt(-1), "stdout": starlark.String("before timeout\n"), "stderr": starlark.String("diagnostic\n")} {
				got, err := attrs.Attr(name)
				if err != nil || got.String() != want.String() {
					t.Fatalf("%s: %v, %v", name, got, err)
				}
			}
		}
	}
	c := shellCommand{argv: []string{t.TempDir() + "/not-a-command"}}
	if _, err := c.run(nil, starlark.NewBuiltin("run", c.run), nil, nil); err == nil {
		t.Fatal("launch failure became a result")
	}
}
