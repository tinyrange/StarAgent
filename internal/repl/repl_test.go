package repl

import (
	"context"
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
