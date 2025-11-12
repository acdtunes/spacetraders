package common

import (
	"context"
	"fmt"
	"reflect"
)

// Request represents a command or query
type Request interface{}

// Response represents the result of handling a request
type Response interface{}

// RequestHandler handles a specific request type
type RequestHandler interface {
	Handle(ctx context.Context, request Request) (Response, error)
}

// Mediator dispatches requests to their handlers
type Mediator interface {
	Send(ctx context.Context, request Request) (Response, error)
	Register(requestType reflect.Type, handler RequestHandler) error
}

// mediator is the concrete implementation
type mediator struct {
	handlers map[reflect.Type]RequestHandler
}

// NewMediator creates a new mediator instance
func NewMediator() Mediator {
	return &mediator{
		handlers: make(map[reflect.Type]RequestHandler),
	}
}

// Register registers a handler for a specific request type
func (m *mediator) Register(requestType reflect.Type, handler RequestHandler) error {
	if requestType == nil {
		return fmt.Errorf("request type cannot be nil")
	}

	if handler == nil {
		return fmt.Errorf("handler cannot be nil")
	}

	if _, exists := m.handlers[requestType]; exists {
		return fmt.Errorf("handler already registered for type %s", requestType)
	}

	m.handlers[requestType] = handler
	return nil
}

// Send dispatches a request to its registered handler
func (m *mediator) Send(ctx context.Context, request Request) (Response, error) {
	if request == nil {
		return nil, fmt.Errorf("request cannot be nil")
	}

	requestType := reflect.TypeOf(request)
	handler, ok := m.handlers[requestType]

	if !ok {
		return nil, fmt.Errorf("no handler registered for type %s", requestType)
	}

	// Direct dispatch - no behaviors/middleware for POC
	return handler.Handle(ctx, request)
}

// Helper function to register handlers with type inference
func RegisterHandler[T Request](m Mediator, handler RequestHandler) error {
	var zero T
	requestType := reflect.TypeOf(zero)
	return m.Register(requestType, handler)
}
