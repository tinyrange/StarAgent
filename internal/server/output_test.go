package server

import (
	"bytes"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRetainedOutputAndFullReplay(t *testing.T) {
	session, err := sessionTestServer(t).newSession(nil)
	if err != nil {
		t.Fatal(err)
	}
	var recording bytes.Buffer
	recorder := recordedTurn{session: session, output: &recording}
	text, err := recorder.OnToolCall(t.Context(), "Print a large result.", "", `print("雪" * 6000, end="")`)
	if err != nil || !strings.Contains(text, `output.read("out-1")`) || len(text) > outputLimit+256 {
		t.Fatalf("preview: %d %v", len(text), err)
	}
	var messages []sessionMessage
	if err := yaml.Unmarshal(recording.Bytes(), &messages); err != nil {
		t.Fatal(err)
	}
	if messages[1].FullText != strings.Repeat("雪", 6000) {
		t.Fatal("lost full output")
	}
	text, err = session.OnToolCall(t.Context(), "", "", `saved = ""
offset = 0
for _ in range(10):
    part = output.read("out-1", offset=offset)
    saved += part.text
    offset = part.next_offset
    if not part.truncated: break
print(saved == "雪" * 6000)`)
	if err != nil || text != "True\n" {
		t.Fatalf("retrieval: %q %v", text, err)
	}
	messages = append(messages, sessionMessage{Kind: "end_turn"})
	log := sessionLog{sessionConfig: sessionConfig{Version: 1}, Messages: messages}
	if err := log.replay(t.Context(), sessionTestServer(t)); err != nil {
		t.Fatal(err)
	}
	// A changed middle, invisible in both previews, must still fail replay.
	messages[1].FullText = messages[1].FullText[:9000] + "changed" + messages[1].FullText[9000:]
	if err := log.replay(t.Context(), sessionTestServer(t)); err == nil {
		t.Fatal("ignored a hidden output difference")
	}
}
