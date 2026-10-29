package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOpenAIFailuresAreRecorded(t *testing.T) {
	for _, tt := range []struct {
		name, response, want string
		status               int
	}{
		{"http", `{"error":{"message":"rate limited"}}`, "HTTP 429", 429},
		{"invalid_json", `{`, "unexpected end", 200},
		{"incomplete", `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":10,"output_tokens":20,"output_tokens_details":{"reasoning_tokens":15},"total_tokens":30}}`, "incomplete", 200},
		{"empty", `{"status":"completed","output":[]}`, "no tool call or final answer", 200},
		{"unknown_tool", `{"status":"completed","output":[{"type":"function_call","name":"shell","call_id":"1","arguments":"{}"}]}`, "invalid OpenAI tool", 200},
		{"missing_args", `{"status":"completed","output":[{"type":"function_call","name":"exec","call_id":"1","arguments":"{}"}]}`, "requires service and script", 200},
		{"invalid_args", `{"status":"completed","output":[{"type":"function_call","name":"exec","call_id":"1","arguments":"{\"service\":4,\"script\":\"pass\"}"}]}`, "invalid exec arguments", 200},
		{"missing_description", `{"status":"completed","output":[{"type":"function_call","name":"exec","call_id":"1","arguments":"{\"service\":\"\",\"script\":\"pass\"}"}]}`, "requires a one-sentence description", 200},
		{"blank_description", `{"status":"completed","output":[{"type":"function_call","name":"exec","call_id":"1","arguments":"{\"description\":\"  \",\"service\":\"\",\"script\":\"pass\"}"}]}`, "requires a one-sentence description", 200},
	} {
		t.Run(tt.name, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.response)
			}))
			defer api.Close()
			server := sessionTestServer(t)
			server.providers["openai"] = &openAIProvider{key: "test-key", endpoint: api.URL, client: api.Client()}
			var input sessionInput
			if err := readSessionFile("../../tests/inputs/hello.yml", &input); err != nil {
				t.Fatal(err)
			}
			input.Services = nil // Exercise API failures without starting child services.
			var log bytes.Buffer
			err := input.run(t.Context(), server, &log)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want %q", err, tt.want)
			}
			if !strings.Contains(log.String(), "kind: error") || !strings.Contains(log.String(), tt.want) {
				t.Fatalf("failure missing from log: %s", &log)
			}
			if strings.Contains(log.String(), "kind: tool_call") {
				t.Fatal("invalid response executed a tool")
			}
			if tt.name == "incomplete" && !strings.Contains(log.String(), "reasoning_tokens: 15") {
				t.Fatal("lost usage from an incomplete response")
			}
		})
	}
}

func TestOpenAIRecoversFromAgentCodeErrors(t *testing.T) {
	for _, tt := range []struct{ name, service, script, want string }{
		{"server parse", "", ")", "<repl>:"},
		{"server eval", "", "print('before'); fail('broken')", "before\nerror: fail: broken"},
		{"workspace parse", "main", ")", "<repl>:"},
		{"workspace eval", "main", "print('before'); fail('broken')", "before\nerror: fail: broken"},
		{"unknown service", "missing", "pass", "error: unknown service"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			requests := 0
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var request struct {
					Input []map[string]any `json:"input"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				var output any
				if requests <= 2 {
					service, script := tt.service, tt.script
					if requests == 2 {
						last := request.Input[len(request.Input)-1]
						text, _ := last["output"].(string)
						if !strings.Contains(text, tt.want) {
							t.Errorf("agent did not receive its error: %q", text)
						}
						service, script = "", "print(42)"
					}
					args, _ := json.Marshal(map[string]string{"description": "Run the next step.", "service": service, "script": script})
					output = map[string]any{"type": "function_call", "name": "exec", "call_id": fmt.Sprint(requests), "arguments": string(args)}
				} else {
					if request.Input[len(request.Input)-1]["output"] != "42\n" {
						t.Error("repair call did not execute")
					}
					output = map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "Recovered."}}}
				}
				json.NewEncoder(w).Encode(map[string]any{"status": "completed", "output": []any{output}})
			}))
			defer api.Close()
			server := sessionTestServer(t)
			server.providers["openai"] = &openAIProvider{key: "test-key", endpoint: api.URL, client: api.Client()}
			var input sessionInput
			if err := readSessionFile("../../tests/inputs/hello.yml", &input); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := input.run(t.Context(), server, &output); err != nil {
				t.Fatal(err)
			}
			if requests != 3 {
				t.Fatalf("got %d API requests", requests)
			}
			var log sessionLog
			if err := yaml.Unmarshal(output.Bytes(), &log); err != nil {
				t.Fatal(err)
			}
			if err := log.replay(t.Context(), sessionTestServer(t)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSessionFileRejectsUnknownFields(t *testing.T) {
	// The input and log formats are deliberately separate.
	var input sessionInput
	if err := readSessionFile("../../tests/inputs/demo.yml", &input); err == nil {
		t.Fatal("accepted a log as input")
	}
	var log sessionLog
	if err := readSessionFile("../../tests/inputs/hello.yml", &log); err == nil {
		t.Fatal("accepted input as a log")
	}
}
