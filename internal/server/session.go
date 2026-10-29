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
	outputs        []string
	lastOutput     string
}

// OnMessage implements [TurnCallbacks].
func (s *Session) OnMessage(kind, text string) error {
	return nil
}

func (s *Session) OnUsage(usage TokenUsage) error { return nil }

// OnToolCall implements [TurnCallbacks].
func (s *Session) OnToolCall(ctx context.Context, _ string, serviceName string, line string) (string, error) {
	text, err := s.execute(ctx, serviceName, line)
	s.lastOutput = text
	return s.presentOutput(text), err
}

func (s *Session) execute(ctx context.Context, serviceName, line string) (string, error) {
	if _, running := s.services[serviceName]; !running {
		if _, configured := s.serviceConfigs[serviceName]; !configured {
			return fmt.Sprintf("error: unknown service %q\n", serviceName), nil
		}
	}
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
	if s.provider != nil {
		err = errors.Join(err, s.provider.Close())
	}
	return err
}

func (s *Session) Id() string                { return s.sessionId }
func (s *Session) Provider() ProviderSession { return s.provider }

func (s *Session) Cost() SessionCost {
	if s.provider == nil {
		return SessionCost{LimitUSD: DefaultMaxCostUSD}
	}
	return s.provider.Cost()
}
func (s *Session) SetCostLimitUSD(limit float64) error {
	if s.provider == nil {
		return fmt.Errorf("session has no provider")
	}
	return s.provider.SetCostLimitUSD(limit)
}

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
