package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type sessionConfig struct {
	Version  int `yaml:"version"`
	Provider struct {
		Name           string `yaml:"name"`
		ProviderConfig `yaml:",inline"`
	} `yaml:"provider"`
	Services map[string]map[string]any `yaml:"services"`
}

type sessionInput struct {
	sessionConfig `yaml:",inline"`
	System        string `yaml:"system,omitempty"`
	Developer     string `yaml:"developer,omitempty"`
	Prompt        string `yaml:"prompt"`
}

type sessionLog struct {
	sessionConfig `yaml:",inline"`
	Messages      []sessionMessage `yaml:"messages"`
}

type sessionMessage struct {
	Timestamp   time.Time `yaml:"timestamp,omitempty"`
	Kind        string    `yaml:"kind"`
	Description string    `yaml:"description,omitempty"`
	Service     string    `yaml:"service,omitempty"`
	Script      string    `yaml:"script,omitempty"`
	Text        string    `yaml:"text,omitempty"`
	Error       string    `yaml:"error,omitempty"`
}

func createSessionLog() (*os.File, error) {
	dir := filepath.Join("tests", "sessions")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	name := "session-" + time.Now().UTC().Format("2006-01-02T15-04-05.000000000Z") + ".yml"
	return os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
}

func readSessionFile(name string, value any) error {
	data, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("expected a single YAML document")
	}
	return nil
}

func (c *sessionConfig) validate(server *Server) error {
	if c.Version != 1 {
		return fmt.Errorf("unexpected session version: %d", c.Version)
	}
	for name, config := range c.Services {
		if name == "" {
			return fmt.Errorf("service name is reserved for the inbuilt service")
		}
		kind, _ := config["kind"].(string)
		if _, ok := server.serviceFactories[kind]; !ok {
			return fmt.Errorf("service %q: unknown kind %q", name, kind)
		}
	}
	return nil
}

const defaultSystem = `You are an agent working through persistent Starlark REPLs, one per service.
Use exec(description, service, script) to run Starlark. Every call must include a description field containing one sentence explaining what the code does. The empty service name selects the server REPL.
Inspect results and use the documented APIs to accomplish the user's task. Print values you need to see.
Keep changes minimal and give a concise final answer.`

func (input *sessionInput) run(ctx context.Context, server *Server, output io.Writer) error {
	if err := input.validate(server); err != nil {
		return err
	}
	if strings.TrimSpace(input.Prompt) == "" {
		return fmt.Errorf("prompt is required")
	}
	session, err := server.CreateSession(input.Provider.Name, input.Provider.ProviderConfig)
	if err != nil {
		return err
	}
	session.serviceConfigs = input.Services
	if err := yaml.NewEncoder(output).Encode(input.sessionConfig); err != nil {
		return err
	}
	if _, err := io.WriteString(output, "messages:\n"); err != nil {
		return err
	}
	callbacks := &recordedTurn{session: session, output: output}
	runErr := func() error {
		system := input.System
		if system == "" {
			system = defaultSystem
		}
		if err := callbacks.OnMessage("system", system); err != nil {
			return err
		}
		var docs []string
		if input.Developer != "" {
			docs = append(docs, input.Developer)
		}
		for _, name := range append([]string{""}, slices.Sorted(maps.Keys(input.Services))...) {
			service, err := session.getService(ctx, name)
			if err != nil {
				return err
			}
			doc, err := service.Documentation(ctx)
			if err != nil {
				return err
			}
			docs = append(docs, fmt.Sprintf("Service %q:\n%s", name, doc))
		}
		for _, doc := range docs {
			if err := callbacks.OnMessage("developer", doc); err != nil {
				return err
			}
		}
		if err := callbacks.OnMessage("user", input.Prompt); err != nil {
			return err
		}
		return session.Provider().RunTurn(ctx, TurnInput{System: system, Developers: docs, Message: input.Prompt}, callbacks)
	}()
	if runErr != nil {
		return errors.Join(runErr, callbacks.OnMessage("error", runErr.Error()))
	}
	return nil
}

// Record only the outer model call: service.get(...).exec(...) may make nested
// calls, which replay must not execute a second time.
type recordedTurn struct {
	session *Session
	output  io.Writer
}

func (r *recordedTurn) write(message sessionMessage) error {
	message.Timestamp = time.Now().UTC()
	// Each fresh encoder emits one sequence entry, appended to messages.
	return yaml.NewEncoder(r.output).Encode([]sessionMessage{message})
}

func (r *recordedTurn) OnMessage(kind, text string) error {
	return r.write(sessionMessage{Kind: kind, Text: text})
}

func (r *recordedTurn) OnToolCall(ctx context.Context, description, service, script string) (string, error) {
	if err := r.write(sessionMessage{Kind: "tool_call", Description: description, Service: service, Script: script}); err != nil {
		return "", err
	}
	text, callErr := r.session.OnToolCall(ctx, description, service, script)
	result := sessionMessage{Kind: "tool_result", Service: service, Text: text}
	if callErr != nil {
		result.Error = callErr.Error()
	}
	return text, errors.Join(callErr, r.write(result))
}

func (log *sessionLog) replay(ctx context.Context, server *Server) error {
	if err := log.validate(server); err != nil {
		return err
	}
	// Validate the entire sequence before executing any recorded code.
	for i := 0; i < len(log.Messages); i++ {
		message := log.Messages[i]
		switch message.Kind {
		case "tool_call":
			if i+1 == len(log.Messages) || log.Messages[i+1].Kind != "tool_result" || log.Messages[i+1].Service != message.Service {
				return fmt.Errorf("message %d: tool_call requires a matching tool_result", i+1)
			}
			i++
		case "system", "developer", "user", "commentary", "reasoning", "end_turn", "error":
		default:
			return fmt.Errorf("message %d: unexpected kind %q", i+1, message.Kind)
		}
	}
	if len(log.Messages) == 0 {
		return fmt.Errorf("session log has no messages")
	}
	last := log.Messages[len(log.Messages)-1].Kind
	if last != "end_turn" && last != "error" {
		return fmt.Errorf("session log is incomplete")
	}
	session, err := server.newSession(nil) // Offline: never construct a provider.
	if err != nil {
		return err
	}
	session.serviceConfigs = log.Services
	for i, message := range log.Messages {
		if message.Kind != "tool_call" {
			continue
		}
		text, callErr := session.OnToolCall(ctx, message.Description, message.Service, message.Script)
		gotError := ""
		if callErr != nil {
			gotError = callErr.Error()
		}
		want := log.Messages[i+1]
		if text != want.Text || gotError != want.Error {
			return fmt.Errorf("message %d, service %q: result mismatch\nwant text: %q\n got text: %q\nwant error: %q\n got error: %q", i+1, message.Service, want.Text, text, want.Error, gotError)
		}
	}
	return nil
}
