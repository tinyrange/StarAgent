package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// One tool routes every operation through a service's Starlark REPL.
var executionTool = json.RawMessage(`{
  "type": "function", "name": "exec", "strict": true,
  "description": "Execute Starlark in a persistent service REPL. Returns its printed output. Use an empty service name for the server REPL.",
  "parameters": {
    "type": "object", "additionalProperties": false,
    "properties": {
      "description": {"type": "string", "description": "One sentence explaining what this code does, sent before execution."},
      "service": {"type": "string"}, "script": {"type": "string"}
    },
    "required": ["description", "service", "script"]
  }
}`)

type openAIProvider struct {
	key      string
	endpoint string
	client   *http.Client
}

func (p *openAIProvider) MakeSession(config ProviderConfig) (ProviderSession, error) {
	if p.key == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY is required")
	}
	if config.Model == "" {
		return nil, fmt.Errorf("provider.model is required")
	}
	return &openAISession{provider: p, config: config}, nil
}

type openAISession struct {
	provider *openAIProvider
	config   ProviderConfig
	history  []json.RawMessage
}

func (s *openAISession) Close() error { return nil }

func (s *openAISession) appendInput(value map[string]any) {
	data, _ := json.Marshal(value) // Call sites contain only strings and JSON values.
	s.history = append(s.history, data)
}

func (s *openAISession) RunTurn(ctx context.Context, input TurnInput, callbacks TurnCallbacks) error {
	if len(s.history) == 0 {
		if input.System != "" {
			s.appendInput(map[string]any{"role": "system", "content": input.System})
		}
		for _, text := range input.Developers {
			s.appendInput(map[string]any{"role": "developer", "content": text})
		}
	}
	s.appendInput(map[string]any{"role": "user", "content": input.Message})
	for {
		output, err := s.respond(ctx, callbacks)
		if err != nil {
			return err
		}
		// Preserve all output items, including encrypted reasoning and message phase.
		s.history = append(s.history, output...)
		var items []struct {
			Type      string `json:"type"`
			Name      string `json:"name"`
			CallID    string `json:"call_id"`
			Arguments string `json:"arguments"`
			Phase     string `json:"phase"`
			Content   []struct {
				Type    string `json:"type"`
				Text    string `json:"text"`
				Refusal string `json:"refusal"`
			} `json:"content"`
			Summary []struct {
				Text string `json:"text"`
			} `json:"summary"`
		}
		// Decode together so messages preceding tool calls are logged as commentary.
		data, _ := json.Marshal(output)
		if err := json.Unmarshal(data, &items); err != nil {
			return err
		}
		calls := 0
		for _, item := range items {
			if item.Type == "function_call" {
				calls++
			}
		}
		final := false
		for _, item := range items {
			switch item.Type {
			case "reasoning":
				for _, summary := range item.Summary {
					if err := callbacks.OnMessage("reasoning", summary.Text); err != nil {
						return err
					}
				}
			case "message":
				text := ""
				for _, part := range item.Content {
					switch part.Type {
					case "output_text":
						text += part.Text
					case "refusal":
						text += part.Refusal
					default:
						return fmt.Errorf("unsupported OpenAI content type %q", part.Type)
					}
				}
				kind := "commentary"
				if calls == 0 && item.Phase != "commentary" {
					kind = "end_turn"
					final = true
				}
				if err := callbacks.OnMessage(kind, text); err != nil {
					return err
				}
			case "function_call":
				if item.Name != "exec" || item.CallID == "" {
					return fmt.Errorf("invalid OpenAI tool call %q", item.Name)
				}
				var args struct {
					Description string  `json:"description"`
					Service     *string `json:"service"`
					Script      *string `json:"script"`
				}
				decoder := json.NewDecoder(bytes.NewBufferString(item.Arguments))
				decoder.DisallowUnknownFields()
				if err := decoder.Decode(&args); err != nil {
					return fmt.Errorf("invalid exec arguments: %w", err)
				}
				if args.Service == nil || args.Script == nil {
					return fmt.Errorf("exec requires service and script")
				}
				if strings.TrimSpace(args.Description) == "" {
					return fmt.Errorf("exec requires a one-sentence description")
				}
				text, err := callbacks.OnToolCall(ctx, args.Description, *args.Service, *args.Script)
				if err != nil {
					return err
				}
				s.appendInput(map[string]any{"type": "function_call_output", "call_id": item.CallID, "output": text})
			default:
				return fmt.Errorf("unsupported OpenAI output type %q", item.Type)
			}
		}
		if calls == 0 {
			if !final {
				return fmt.Errorf("OpenAI response contained no tool call or final answer")
			}
			return nil
		}
	}
}

func (s *openAISession) respond(ctx context.Context, callbacks TurnCallbacks) ([]json.RawMessage, error) {
	reasoning := map[string]string{"summary": "auto"}
	if s.config.ReasoningEffort != "" {
		reasoning["effort"] = s.config.ReasoningEffort
	}
	body, err := json.Marshal(map[string]any{
		"model": s.config.Model, "input": s.history,
		"tools": []json.RawMessage{executionTool}, "parallel_tool_calls": false,
		"reasoning": reasoning, "store": false,
	})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.provider.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+s.provider.key)
	request.Header.Set("Content-Type", "application/json")
	response, err := s.provider.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OpenAI HTTP %d: %s", response.StatusCode, data)
	}
	var result struct {
		Usage             *TokenUsage       `json:"usage"`
		Status            string            `json:"status"`
		Output            []json.RawMessage `json:"output"`
		Error             json.RawMessage   `json:"error"`
		IncompleteDetails json.RawMessage   `json:"incomplete_details"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	if result.Usage != nil {
		if err := callbacks.OnUsage(*result.Usage); err != nil {
			return nil, err
		}
	}
	if result.Status != "completed" {
		return nil, fmt.Errorf("OpenAI response status %q: error=%s incomplete_details=%s", result.Status, result.Error, result.IncompleteDetails)
	}
	return result.Output, nil
}
