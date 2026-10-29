package repl

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/tinyrange/staragent/internal/protocol"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

func TestREPLSession(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		serverConn.Close()
		clientConn.Close()
	})
	for _, conn := range []net.Conn{serverConn, clientConn} {
		if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan error, 1)
	go func() {
		defer serverConn.Close()
		done <- Start(Options{
			SyntaxOptions: &syntax.FileOptions{GlobalReassign: true},
			Thread:        &starlark.Thread{Name: "test"},
			Environment:   make(starlark.StringDict),
			Input:         serverConn,
			Output:        serverConn,
		})
	}()
	client, err := protocol.StartClient(clientConn, clientConn)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		input string
		want  string
	}{
		{"1 + 2", "3\n"},
		{"_ + 1", "4\n"},
		{"x = [1]", ""},
		{"x.append(2)", ""},
		{"x", "[1, 2]\n"},
		{"print('hello')", "hello\n"},
		{`print()`, "\n"},
		{`print(end="")`, ""},
		{`print("hello", end="")`, "hello"},
		{`print("a", 42, ["b"], b"c", sep="|", end="!")`, "a|42|[\"b\"]|c!"},
		{`print("a", end=""); print("b")`, "ab\n"},
		{`print("a\n", end="")`, "a\n"},
		{`saved_print = print`, ""},
		{"def emit():\n    saved_print('saved', end='!')", ""},
		{`emit()`, "saved!"},
		{`print("untouched default")`, "untouched default\n"},
		{")", "<repl>:1:1: unexpected ')'\n"},
		{"1 / 0", "error: floating-point division by zero\n"},
		{"x.append(3); print(x)", "[1, 2, 3]\n"},
		{"# comment", ""},
		{"", ""},
		{"x", "[1, 2, 3]\n"},
	} {
		response, err := client.Exchange(context.Background(), tt.input)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(response)
		if err != nil {
			t.Fatal(err)
		}
		if err := response.Close(); err != nil {
			t.Fatal(err)
		}
		if string(got) != tt.want {
			t.Fatalf("input %q: got %q, want %q", tt.input, got, tt.want)
		}
	}
	clientConn.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("clean disconnect: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("REPL did not exit after disconnect")
	}
}

func TestREPLTruncatedRequest(t *testing.T) {
	err := Start(Options{Input: strings.NewReader("print(1)"), Output: io.Discard})
	if err == nil {
		t.Fatal("accepted a truncated request")
	}
}

func TestPrintErrors(t *testing.T) {
	opts := Options{SyntaxOptions: &syntax.FileOptions{}, Thread: &starlark.Thread{}, Environment: make(starlark.StringDict)}
	for _, script := range []string{`print("x", end=1)`, `print("x", sep=None)`, `print("x", unknown="")`} {
		var output bytes.Buffer
		if err := Evaluate(opts, script, &output); err != nil || !strings.HasPrefix(output.String(), "error: print:") {
			t.Fatalf("%s: output=%q, error=%v", script, output.String(), err)
		}
	}
	want := errors.New("output unavailable")
	if err := Evaluate(opts, `print("x", end="")`, failedWriter{want}); !errors.Is(err, want) {
		t.Fatalf("output failure: got %v, want %v", err, want)
	}
}

type failedWriter struct{ err error }

func (w failedWriter) Write([]byte) (int, error) { return 0, w.err }
