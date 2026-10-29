package server

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"
)

func makeSessionId() string {
	return uuid.New().String()
}

type Session struct {
	server         *Server
	sessionId      string
	provider       ProviderSession
	services       map[string]Service
	serviceConfigs map[string]map[string]any
}

// OnCommentaryFragment implements [TurnCallbacks].
func (s *Session) OnCommentaryFragment(fragment string) error {
	return fmt.Errorf("OnCommentaryFragment not implemented on Session")
}

// OnToolCall implements [TurnCallbacks].
func (s *Session) OnToolCall(ctx context.Context, serviceName string, line string) (string, error) {
	service, err := s.getService(ctx, serviceName)
	if err != nil {
		return "", err
	}
	return service.ExecuteLine(ctx, line)
}

func (s *Session) getService(ctx context.Context, serviceName string) (Service, error) {
	service, ok := s.services[serviceName]
	if !ok {
		config, ok := s.serviceConfigs[serviceName]
		if !ok {
			return nil, fmt.Errorf("failed to find service [%s] on session", serviceName)
		}
		kind, _ := config["kind"].(string)
		factory, ok := s.server.serviceFactories[kind]
		if !ok {
			return nil, fmt.Errorf("service %q: unknown kind %q", serviceName, kind)
		}
		var err error
		service, err = factory.Create(ctx, config)
		if err != nil {
			return nil, fmt.Errorf("failed to create service %q: %w", serviceName, err)
		}
		if err := s.RegisterService(ctx, serviceName, service); err != nil {
			return nil, err
		}
	}

	return service, nil
}

// Close implements [io.Closer].
func (s *Session) Close() error {
	var err error
	for name, service := range s.services {
		err = errors.Join(err, service.Close())
		delete(s.services, name)
	}
	return errors.Join(err, s.provider.Close())
}

func (s *Session) Id() string                { return s.sessionId }
func (s *Session) Provider() ProviderSession { return s.provider }

func (s *Session) RegisterService(ctx context.Context, name string, service Service) error {
	if _, ok := s.services[name]; ok {
		return fmt.Errorf("session has already regisrtered %s", name)
	}

	if err := service.Init(ctx); err != nil {
		return fmt.Errorf("failed to init service %q: %w", name, errors.Join(err, service.Close()))
	}
	s.services[name] = service

	return nil
}

var (
	_ io.Closer     = &Session{}
	_ TurnCallbacks = &Session{}
)
