package server

import (
	"fmt"
	"regexp"
	"testing"
)

func TestReplayOutputNormalization(t *testing.T) {
	quoted := func(s string) string {
		return fmt.Sprintf("struct(exit_code = 1, ok = False, stderr = %q, stdout = %q)\n", s, "")
	}
	for _, tt := range []struct {
		name, before, after string
		equal               bool
	}{
		{"temporary roots", quoted("at file:///private/var/folders/xx/one/T/staragent-123/check.js:7:8\n"), quoted("at file:///var/folders/yy/two/T/staragent-456/check.js:7:8\n"), true},
		{"linux roots", "/tmp/staragent-123/source.c", "/var/tmp/staragent-456/source.c", true},
		{"Go summary", quoted("ok  \texample.test/page\t0.004s\n"), quoted("ok  \texample.test/page\t1.234s\n"), true},
		{"Go failure", quoted("--- FAIL: TestPage (0.00s)\n    page_test.go:8: wrong\nFAIL\texample.test/page\t0.004s\n"), quoted("--- FAIL: TestPage (2.00s)\n    page_test.go:8: wrong\nFAIL\texample.test/page\t2.004s\n"), true},
		{"unittest timing", quoted("Ran 13 tests in 0.032s\n\nOK\n"), quoted("Ran 13 tests in 0.044s\n\nOK\n"), true},
		{"unittest count", quoted("Ran 13 tests in 0.032s\n\nOK\n"), quoted("Ran 12 tests in 0.032s\n\nOK\n"), false},
		{"unittest outcome", quoted("Ran 13 tests in 0.032s\n\nOK\n"), quoted("Ran 13 tests in 0.032s\n\nFAILED\n"), false},
		{"different file", "/tmp/staragent-123/a.c", "/tmp/staragent-456/b.c", false},
		{"different assertion", quoted("at /tmp/staragent-123/check.js:7:8\n4000 != 0"), quoted("at /tmp/staragent-456/check.js:8:8\n4000 != 1"), false},
		{"test outcome", quoted("ok  \texample.test/page\t0.004s\n"), quoted("FAIL\texample.test/page\t0.008s\n"), false},
		{"test package", quoted("ok  \texample.test/a\t0.004s\n"), quoted("ok  \texample.test/b\t0.004s\n"), false},
		{"application duration", quoted("timeout after 0.004s"), quoted("timeout after 0.008s"), false},
		{"application number", "123", "456", false},
		{"escaped newline", `"a\nb"`, "\"a\nb\"", false},
		{"ordinary path", "/projects/staragent-123/a", "/projects/staragent-456/a", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeReplayOutput(tt.before) == normalizeReplayOutput(tt.after); got != tt.equal {
				t.Fatalf("equal=%v, want %v\nbefore: %s\nafter: %s", got, tt.equal, normalizeReplayOutput(tt.before), normalizeReplayOutput(tt.after))
			}
		})
	}
}

func TestReplayExplicitIgnore(t *testing.T) {
	ignore := regexp.MustCompile(`seconds=[0-9]+\.[0-9]+`)
	a := `struct(stdout = "moves=96 seconds=0.32\n")`
	b := `struct(stdout = "moves=96 seconds=0.45\n")`
	if normalizeReplayOutput(a) == normalizeReplayOutput(b) {
		t.Fatal("ignored application timing by default")
	}
	if normalizeReplayOutput(a, ignore) != normalizeReplayOutput(b, ignore) {
		t.Fatal("explicit ignore failed")
	}
	if normalizeReplayOutput(a, ignore) == normalizeReplayOutput(`struct(stdout = "moves=97 seconds=0.45\n")`, ignore) {
		t.Fatal("ignored the actual result")
	}
}

func TestServerREPLDialectAndRecovery(t *testing.T) {
	session, err := sessionTestServer(t).newSession(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ script, want string }{
		{"values = []", ""},
		{"for n in range(3):\n    values.append(n)", ""},
		{"if True:\n    x = 1", ""},
		{"x = 2", ""},
		{"values", "[0, 1, 2]\n"},
		{`print("a", "b", sep="|", end="")`, "a|b"},
		{`saved_print = print`, ""},
		{`saved_print("next", end="!")`, "next!"},
		{"print('before'); fail('stop')", "before\nerror: fail: stop\n"},
		{")", "<repl>:1:1: unexpected ')'\n"},
		{"print(x, values)", "2 [0, 1, 2]\n"},
	} {
		got, err := session.OnToolCall(t.Context(), "", "", tt.script)
		if err != nil || got != tt.want {
			t.Fatalf("%q: got %q, %v; want %q", tt.script, got, err, tt.want)
		}
	}
}
