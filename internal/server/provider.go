package server

import (
	"context"
	"fmt"
	"io"
	"os"
)

type ProviderConfig struct {
	Model           string
	ReasoningEffort string
}

type TurnInput struct {
	Message string
}

type TurnCallbacks interface {
	OnCommentaryFragment(fragment string) error
	OnToolCall(ctx context.Context, service string, line string) (string, error)
}

type ProviderSession interface {
	io.Closer
	RunTurn(ctx context.Context, input TurnInput, callbacks TurnCallbacks) error
}

type Provider interface {
	MakeSession(config ProviderConfig) (ProviderSession, error)
}

type testProvider struct {
}

// MakeSession implements [Provider].
func (t *testProvider) MakeSession(config ProviderConfig) (ProviderSession, error) {
	return &testProviderSession{}, nil
}

type testProviderSession struct {
}

// runScripts implements [serverTestProviderInterface].
func (t *testProviderSession) runScripts(ctx context.Context, scripts []serverTestProviderScript, callbacks TurnCallbacks) error {
	for _, script := range scripts {
		output, err := callbacks.OnToolCall(ctx, script.Service, script.Script)
		if err != nil {
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
