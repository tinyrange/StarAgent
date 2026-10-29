package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tinyrange/staragent/internal/service"
	"gopkg.in/yaml.v3"
)

func TestWorkspaceServiceProcess(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "staragent-service" {
		return
	}
	if err := service.RunWorkspace(os.Args[len(os.Args)-1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func sessionTestServer(t *testing.T) *Server {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		providers: make(map[string]Provider), sessions: make(map[string]*Session),
		serviceFactories: map[string]ServiceFactory{
			"service": &workspaceServiceFactory{command: []string{executable, "-test.run=^TestWorkspaceServiceProcess$", "--", "staragent-service"}},
		},
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func TestSessionInputRecordReplay(t *testing.T) {
	var input sessionInput
	if err := readSessionFile("../../tests/inputs/hello.yml", &input); err != nil {
		t.Fatal(err)
	}
	// Nest execution through the server REPL to catch duplicate recording/replay.
	script := `print(service.get("main").exec('print(workspace.read_file("README.md").text)\nworkspace.write_file("README.md", "Updated")'))`
	description := "Read README.md through the main service and replace its contents."
	arguments, _ := json.Marshal(map[string]string{"description": description, "service": "", "script": script})
	reasoning := map[string]any{"type": "reasoning", "id": "rs_1", "encrypted_content": "opaque", "summary": []any{map[string]any{"type": "summary_text", "text": "Read the file first."}}}
	commentary := map[string]any{"type": "message", "role": "assistant", "phase": "commentary", "content": []any{map[string]any{"type": "output_text", "text": "Reading README."}}}
	requests := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("incorrect HTTP method or authorization")
		}
		var body struct {
			Model     string            `json:"model"`
			Input     []map[string]any  `json:"input"`
			Reasoning map[string]string `json:"reasoning"`
			Store     bool              `json:"store"`
			Parallel  bool              `json:"parallel_tool_calls"`
			Tools     []json.RawMessage `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body.Model != "gpt-6.1-sol" || body.Reasoning["effort"] != "high" || body.Store || body.Parallel || len(body.Tools) != 1 {
			t.Error("incorrect model/tool settings")
		}
		var tool struct {
			Name       string `json:"name"`
			Parameters struct {
				Required []string `json:"required"`
			} `json:"parameters"`
		}
		if len(body.Tools) != 1 {
			return
		}
		if err := json.Unmarshal(body.Tools[0], &tool); err != nil {
			t.Error(err)
			return
		}
		if tool.Name != "exec" || strings.Join(tool.Parameters.Required, ",") != "description,service,script" {
			t.Errorf("unexpected exec schema: %#v", tool)
		}
		var output []any
		switch requests {
		case 1:
			if len(body.Input) != 4 || body.Input[0]["role"] != "system" || body.Input[1]["role"] != "developer" || body.Input[2]["role"] != "developer" || body.Input[3]["content"] != input.Prompt {
				t.Errorf("incorrect initial messages: %#v", body.Input)
				return
			}
			if body.Input[1]["content"] != "Service \"\":\n"+serviceDoc || !strings.HasPrefix(body.Input[2]["content"].(string), "Service \"main\":\n") || !strings.Contains(body.Input[2]["content"].(string), "workspace.read_file") {
				t.Error("service documentation is not in separate developer messages")
			}
			output = []any{reasoning, commentary, map[string]any{"type": "function_call", "call_id": "call_1", "name": "exec", "arguments": string(arguments)}}
		case 2:
			if len(body.Input) != 8 {
				t.Errorf("history length = %d", len(body.Input))
				return
			}
			if body.Input[4]["encrypted_content"] != "opaque" || body.Input[5]["phase"] != "commentary" {
				t.Error("reasoning or phase was lost")
			}
			result := body.Input[7]
			if result["type"] != "function_call_output" || result["call_id"] != "call_1" || result["output"] != "Hello, world\n\n" {
				t.Errorf("incorrect tool result: %#v", result)
			}
			output = []any{map[string]any{"type": "message", "phase": "final_answer", "content": []any{map[string]any{"type": "output_text", "text": "Hello, world"}}}}
		default:
			t.Error("unexpected API call")
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "completed", "output": output})
	}))
	defer api.Close()
	server := sessionTestServer(t)
	server.providers["openai"] = &openAIProvider{key: "test-key", endpoint: api.URL, client: api.Client()}
	var recorded bytes.Buffer
	if err := input.run(t.Context(), server, &recorded); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("API requests = %d", requests)
	}
	var log sessionLog
	if err := yaml.Unmarshal(recorded.Bytes(), &log); err != nil {
		t.Fatalf("invalid log: %v\n%s", err, &recorded)
	}
	var kinds []string
	for _, message := range log.Messages {
		if message.Timestamp.IsZero() {
			t.Fatal("recorded message has no timestamp")
		}
		kinds = append(kinds, message.Kind)
	}
	if got := strings.Join(kinds, ","); got != "system,developer,developer,user,reasoning,commentary,tool_call,tool_result,end_turn" {
		t.Fatalf("unexpected messages: %s", got)
	}
	if log.Messages[6].Description != description {
		t.Fatal("exec description was not recorded on the call")
	}
	var originalDir string
	for _, session := range server.sessions {
		originalDir = session.services["main"].(*workspaceService).dir
		data, err := os.ReadFile(filepath.Join(originalDir, "README.md"))
		if err != nil || string(data) != "Updated" {
			t.Fatalf("workspace was not updated: %q, %v", data, err)
		}
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(originalDir); !os.IsNotExist(err) {
		t.Fatalf("workspace was not removed: %v", err)
	}
	// No providers are registered: replay cannot contact the model.
	if err := log.replay(t.Context(), sessionTestServer(t)); err != nil {
		t.Fatal(err)
	}
	log.Messages[7].Text = "wrong\n"
	if err := log.replay(t.Context(), sessionTestServer(t)); err == nil || !strings.Contains(err.Error(), "result mismatch") {
		t.Fatalf("mismatch not detected: %v", err)
	}
}

func TestSessionLogsAreRetained(t *testing.T) {
	t.Chdir(t.TempDir())
	var names []string
	for range 2 {
		file, err := createSessionLog()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString("retained"); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if filepath.Dir(file.Name()) != filepath.Join("tests", "sessions") {
			t.Fatalf("unexpected log directory: %s", file.Name())
		}
		stamp := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(file.Name()), "session-"), ".yml")
		if _, err := time.Parse("2006-01-02T15-04-05.000000000Z", stamp); err != nil {
			t.Fatal(err)
		}
		names = append(names, file.Name())
	}
	if names[0] == names[1] {
		t.Fatal("session log was reused")
	}
	for _, name := range names {
		data, err := os.ReadFile(name)
		if err != nil || string(data) != "retained" {
			t.Fatalf("log not retained: %q, %v", data, err)
		}
	}
}

func TestReplayValidationAndErrors(t *testing.T) {
	var demo sessionLog
	if err := readSessionFile("../../tests/inputs/demo.yml", &demo); err != nil {
		t.Fatal(err)
	}
	if err := demo.replay(t.Context(), sessionTestServer(t)); err != nil {
		t.Fatal(err)
	}
	for _, messages := range [][]sessionMessage{
		nil,
		{{Kind: "tool_call", Script: `fail("must not execute")`}, {Kind: "end_turn"}},
		{{Kind: "tool_call"}, {Kind: "tool_result", Service: "different"}, {Kind: "end_turn"}},
		{{Kind: "tool_result"}, {Kind: "end_turn"}},
		{{Kind: "unknown"}, {Kind: "end_turn"}},
		{{Kind: "user"}},
	} {
		server := sessionTestServer(t)
		log := sessionLog{sessionConfig: demo.sessionConfig, Messages: messages}
		if err := log.replay(t.Context(), server); err == nil {
			t.Errorf("accepted malformed messages: %#v", messages)
		}
		if len(server.sessions) != 0 {
			t.Error("malformed log started a session")
		}
	}
	log := sessionLog{sessionConfig: demo.sessionConfig, Messages: []sessionMessage{
		{Kind: "tool_call", Service: "main", Script: `fail("expected")`},
		{Kind: "tool_result", Service: "main", Text: "error: fail: expected\n"},
		{Kind: "tool_call", Service: "missing", Script: "pass"},
		{Kind: "tool_result", Service: "missing", Error: "failed to find service [missing] on session"},
		{Kind: "end_turn"},
	}}
	if err := log.replay(t.Context(), sessionTestServer(t)); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceSeedContainment(t *testing.T) {
	factory := sessionTestServer(t).serviceFactories["service"]
	for _, name := range []string{"../escape", "/tmp/staragent-escape"} {
		_, err := factory.Create(t.Context(), map[string]any{"temp_workspace": true, "files": map[string]string{name: "bad"}})
		if err == nil {
			t.Errorf("accepted seed path %q", name)
		}
	}
	svc, err := factory.Create(t.Context(), map[string]any{"temp_workspace": true, "files": map[string]string{"nested/file": "hello"}})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	data, err := os.ReadFile(filepath.Join(svc.(*workspaceService).dir, "nested/file"))
	if err != nil || string(data) != "hello" {
		t.Fatalf("nested file: %q, %v", data, err)
	}
}

type brokenLog struct{}

func (brokenLog) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestLogFailurePreventsExecution(t *testing.T) {
	r := recordedTurn{output: brokenLog{}}
	// A nil session would panic if execution happened before recording the call.
	if _, err := r.OnToolCall(context.Background(), "Do nothing.", "", "pass"); err == nil {
		t.Fatal("ignored log failure")
	}
}

func TestReplayDocumentation(t *testing.T) {
	var log sessionLog
	if err := readSessionFile("../../tests/inputs/demo.yml", &log); err != nil {
		t.Fatal(err)
	}
	server := sessionTestServer(t)
	session, err := server.newSession(nil)
	if err != nil {
		t.Fatal(err)
	}
	session.serviceConfigs = log.Services
	var recorded bytes.Buffer
	r := recordedTurn{session: session, output: &recorded}
	text, err := r.OnToolCall(t.Context(), "Print the main service documentation.", "", `print(service.get("main").help())`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, `workspace(root="."`) {
		t.Fatalf("unstable workspace documentation: %s", text)
	}
	if err := r.OnMessage("end_turn", "done"); err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(recorded.Bytes(), &log.Messages); err != nil {
		t.Fatal(err)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if err := log.replay(t.Context(), sessionTestServer(t)); err != nil {
		t.Fatal(err)
	}
}
