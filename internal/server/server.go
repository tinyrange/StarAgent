package server

import (
	"context"
	"fmt"
	"io"
)

// Data Model
// Server contains many sessions and providers.
// Services are REPLs.
// Sessions track a provider session of messages.
// Server has a simple internal REPL with a basic set of tools including the ability to spawn more REPLs.

type Server struct {
	providers        map[string]Provider
	sessions         map[string]*Session
	serviceFactories map[string]ServiceFactory
}

func (s *Server) Close() error {
	for _, session := range s.sessions {
		if err := session.Close(); err != nil {
			return err
		}
	}

	return nil
}

func (s *Server) RegisterProvider(name string, provider Provider) error {
	if _, ok := s.providers[name]; ok {
		return fmt.Errorf("provider %s already registered", name)
	}

	s.providers[name] = provider

	return nil
}

func (s *Server) CreateSession(providerName string, providerConfig ProviderConfig) (*Session, error) {
	provider, ok := s.providers[providerName]
	if !ok {
		return nil, fmt.Errorf("provider not found: %s", providerName)
	}

	providerSession, err := provider.MakeSession(providerConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to make provider session: %w", err)
	}

	session := &Session{
		server:    s,
		sessionId: makeSessionId(),
		provider:  providerSession,
		services:  make(map[string]Service),
	}

	s.sessions[session.sessionId] = session

	if err := session.RegisterService(context.Background(), "", &serverService{
		session: session,
	}); err != nil {
		return nil, fmt.Errorf("failed to register inbuilt service: %w", err)
	}

	return session, nil
}

var (
	_ io.Closer = &Server{}
)
