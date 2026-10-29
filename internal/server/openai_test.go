package server

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIFailuresAreRecorded(t *testing.T) {
	for _, tt := range []struct {
		name, response, want string
		status               int
	}{
		{"http", `{"error":{"message":"rate limited"}}`, "HTTP 429", 429},
		{"invalid_json", `{`, "unexpected end", 200},
		{"incomplete", `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}`, "incomplete", 200},
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
