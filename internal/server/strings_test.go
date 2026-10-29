package server

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"go.starlark.net/syntax"
	"gopkg.in/yaml.v3"
)

func TestServiceStringsAndReplay(t *testing.T) {
	session, err := sessionTestServer(t).newSession(nil)
	if err != nil {
		t.Fatal(err)
	}
	session.serviceConfigs = map[string]map[string]any{}
	for _, name := range []string{"source", "destination"} {
		session.serviceConfigs[name] = map[string]any{"kind": "service", "temp_workspace": true, "files": map[string]string{"AGENTS.yml": "version: 1\nworkflows: false\nworkspace: {read: true, write: true}\n"}}
	}
	payload := "'''\"\"\"\\\n雪\x02\x03\x00\n'); fail('injection') #"
	var recording bytes.Buffer
	recorder := recordedTurn{session: session, output: &recording}
	text, err := recorder.OnToolCall(t.Context(), "Produce contents in the source REPL.", "source", `count = [0]
def produce():
    count[0] += 1
    print("producer diagnostic")
    return `+syntax.Quote(payload, false)+`
contents = produce()
number = 18446744073709551615
empty = ""
none = None`)
	if err != nil || text != "producer diagnostic\n" {
		t.Fatalf("%q %v", text, err)
	}
	text, err = recorder.OnToolCall(t.Context(), "Copy strings between REPLs.", "", `source = service.get("source")
destination = service.get("destination")
contents = source.get_string("contents")
destination.set_string("contents", contents)
if destination.get_string("contents") != contents: fail("contents changed")
destination.set_string(name="empty", value=source.get_string("empty"))
if destination.get_string("empty") != "": fail("empty string changed")
if source.get_string("number") != "18446744073709551615": fail("number changed")
if source.get_string("none") != "None": fail("None changed")
print(source.get_string("count"))`)
	if err != nil || text != "[1]\n" {
		t.Fatalf("%q %v", text, err)
	}
	text, err = recorder.OnToolCall(t.Context(), "Read a missing source variable.", "", `source.get_string("missing")`)
	if err != nil || !strings.Contains(text, `output.read("out-1")`) {
		t.Fatalf("%q %v", text, err)
	}
	text, err = recorder.OnToolCall(t.Context(), "Verify the stored contents remain available.", "", `print(destination.get_string("contents") == contents)`)
	if err != nil || text != "True\n" {
		t.Fatalf("%q %v", text, err)
	}
	var messages []sessionMessage
	if err := yaml.Unmarshal(recording.Bytes(), &messages); err != nil {
		t.Fatal(err)
	}
	if messages[5].Outputs["out-1"] == "" {
		t.Fatal("missing diagnostic recording")
	}
	messages = append(messages, sessionMessage{Kind: "end_turn"})
	log := sessionLog{sessionConfig: sessionConfig{Version: 1, Services: session.serviceConfigs}, Messages: messages}
	if err := log.replay(t.Context(), sessionTestServer(t)); err != nil {
		t.Fatal(err)
	}
}

type stringTestService struct {
	response string
	err      error
	requests []string
}

func (*stringTestService) Init(context.Context) error                    { return nil }
func (*stringTestService) Close() error                                  { return nil }
func (*stringTestService) Documentation(context.Context) (string, error) { return "", nil }
func (s *stringTestService) ExecuteLine(_ context.Context, code string) (string, error) {
	s.requests = append(s.requests, code)
	return s.response, s.err
}

func TestServiceStringFailures(t *testing.T) {
	session, err := sessionTestServer(t).newSession(nil)
	if err != nil {
		t.Fatal(err)
	}
	remote := &stringTestService{}
	if err := session.RegisterService(t.Context(), "remote", remote); err != nil {
		t.Fatal(err)
	}
	for _, script := range []string{
		`service.get("remote").get_string("produce()")`,
		`service.get("remote").set_string("x; fail('injected')", "text")`,
		`service.get("remote").set_string("x", 42)`,
	} {
		text, err := session.OnToolCall(t.Context(), "", "", script)
		if err != nil || !strings.HasPrefix(text, "error:") || len(remote.requests) != 0 {
			t.Fatalf("%q %v", text, err)
		}
	}
	for _, response := range []string{`42`, `b"bytes"`, `"first"; fail("injected")`, `str("not a literal")`, "diagnostic\n\"value\""} {
		remote.response = response
		text, err := session.OnToolCall(t.Context(), "", "", `service.get("remote").get_string("contents")`)
		if err != nil || !strings.Contains(text, "expected a quoted string") || session.outputs[len(session.outputs)-1] != response {
			t.Fatalf("%q %v", text, err)
		}
	}
	remote.response = ""
	remote.err = errors.New("connection lost after write")
	before := len(remote.requests)
	text, err := session.OnToolCall(t.Context(), "", "", `service.get("remote").set_string("contents", "value")`)
	if err != nil || !strings.Contains(text, "write not confirmed") || len(remote.requests) != before+1 {
		t.Fatalf("%q %v", text, err)
	}
}
