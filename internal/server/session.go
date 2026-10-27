package server

import (
	"context"
	"fmt"
	"io"

	"github.com/google/uuid"
)

func makeSessionId() string {
	return uuid.New().String()
}

type Session struct {
	server    *Server
	sessionId string
	provider  ProviderSession
	services  map[string]Service
}

// OnCommentaryFragment implements [TurnCallbacks].
func (s *Session) OnCommentaryFragment(fragment string) error {
	return fmt.Errorf("OnCommentaryFragment not implemented on Session")
}

// OnToolCall implements [TurnCallbacks].
func (s *Session) OnToolCall(ctx context.Context, serviceName string, line string) (string, error) {
	service, ok := s.services[serviceName]
	if !ok {
		return "", fmt.Errorf("failed to find service [%s] on session", serviceName)
	}

	return service.ExecuteLine(ctx, line)
}

// Close implements [io.Closer].
func (s *Session) Close() error {
	return s.provider.Close()
}

func (s *Session) Id() string                { return s.sessionId }
func (s *Session) Provider() ProviderSession { return s.provider }

func (s *Session) RegisterService(ctx context.Context, name string, service Service) error {
	if _, ok := s.services[name]; ok {
		return fmt.Errorf("session has already regisrtered %s", name)
	}

	s.services[name] = service

	if err := s.services[name].Init(ctx); err != nil {
		return fmt.Errorf("failed to init service: %w", err)
	}

	return nil
}

var (
	_ io.Closer     = &Session{}
	_ TurnCallbacks = &Session{}
)
