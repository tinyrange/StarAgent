package server

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
)

type ProviderConfig struct {
	Model           string   `yaml:"model"`
	ReasoningEffort string   `yaml:"reasoning"`
	MaxCostUSD      *float64 `yaml:"max_cost_usd,omitempty"`
}

type TurnInput struct {
	System     string
	Developers []string
	Message    string
}

type TurnCallbacks interface {
	OnMessage(kind, text string) error
	OnUsage(usage TokenUsage) error
	OnToolCall(ctx context.Context, description, service, line string) (string, error)
}

// TokenUsage records one model response. Reasoning tokens are part of output_tokens,
// and cached tokens are part of input_tokens; they must not be added twice.
type TokenUsage struct {
	InputTokens        int `json:"input_tokens" yaml:"input_tokens"`
	OutputTokens       int `json:"output_tokens" yaml:"output_tokens"`
	TotalTokens        int `json:"total_tokens" yaml:"total_tokens"`
	InputTokensDetails *struct {
		CachedTokens     *int `json:"cached_tokens" yaml:"cached_tokens,omitempty"`
		CacheWriteTokens *int `json:"cache_write_tokens" yaml:"cache_write_tokens,omitempty"`
	} `json:"input_tokens_details" yaml:"input_tokens_details,omitempty"`
	OutputTokensDetails *struct {
		ReasoningTokens *int `json:"reasoning_tokens" yaml:"reasoning_tokens,omitempty"`
	} `json:"output_tokens_details" yaml:"output_tokens_details,omitempty"`
}

type ProviderSession interface {
	io.Closer
	Cost() SessionCost
	SetCostLimitUSD(limit float64) error
	RunTurn(ctx context.Context, input TurnInput, callbacks TurnCallbacks) error
}

type Provider interface {
	MakeSession(config ProviderConfig) (ProviderSession, error)
}

type testProvider struct {
}

// MakeSession implements [Provider].
func (t *testProvider) MakeSession(config ProviderConfig) (ProviderSession, error) {
	cost, err := newCostTracker(config)
	return &testProviderSession{costTracker: cost}, err
}

type testProviderSession struct {
	costTracker
}

// runScripts implements [serverTestProviderInterface].
func (t *testProviderSession) runScripts(ctx context.Context, scripts []serverTestProviderScript, callbacks TurnCallbacks) error {
	for _, script := range scripts {
		output, err := callbacks.OnToolCall(ctx, "", script.Service, script.Script)
		// The REPL reports evaluation and parse errors as text in protocol v1.
		if err == nil && (strings.Contains("\n"+output, "\nerror: ") || strings.HasPrefix(output, "<repl>:")) {
			err = fmt.Errorf("%s", strings.TrimSpace(output))
		}
		if script.ShouldFail {
			if err == nil {
				return fmt.Errorf("expected script on service %q to fail", script.Service)
			}
		} else if err != nil {
			return fmt.Errorf("failed to execute: %w", err)
		}
		if _, err := io.WriteString(os.Stdout, output); err != nil {
			return err
		}
	}

	return nil
}

// Close implements [ProviderSession].
func (t *testProviderSession) Close() error {
	return nil
}

// RunTurn implements [ProviderSession].
func (t *testProviderSession) RunTurn(ctx context.Context, input TurnInput, callbacks TurnCallbacks) error {
	return fmt.Errorf("RunTurn not implemented on testProviderSession")
}

var (
	_ Provider                    = &testProvider{}
	_ ProviderSession             = &testProviderSession{}
	_ serverTestProviderInterface = &testProviderSession{}
)
