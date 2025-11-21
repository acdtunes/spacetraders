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
	RegisterMiddleware(middleware Middleware)
}

// mediator is the concrete implementation
type mediator struct {
	handlers    map[reflect.Type]RequestHandler
	middlewares []Middleware
}

// NewMediator creates a new mediator instance
func NewMediator() Mediator {
	return &mediator{
		handlers:    make(map[reflect.Type]RequestHandler),
		middlewares: make([]Middleware, 0),
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

// RegisterMiddleware registers middleware to be executed for all requests
// Middleware is executed in the order it is registered
func (m *mediator) RegisterMiddleware(middleware Middleware) {
	m.middlewares = append(m.middlewares, middleware)
}

// Send dispatches a request to its registered handler
// Executes all registered middleware in order before reaching the handler
func (m *mediator) Send(ctx context.Context, request Request) (Response, error) {
	if request == nil {
		return nil, fmt.Errorf("request cannot be nil")
	}

	requestType := reflect.TypeOf(request)
	handler, ok := m.handlers[requestType]

	if !ok {
		return nil, fmt.Errorf("no handler registered for type %s", requestType)
	}

	// Build middleware chain from right to left
	// The innermost function is the handler execution
	next := handler.Handle

	// Wrap handler with middleware (in reverse order so first registered executes first)
	for i := len(m.middlewares) - 1; i >= 0; i-- {
		middleware := m.middlewares[i]
		currentNext := next
		next = func(ctx context.Context, req Request) (Response, error) {
			return middleware(ctx, req, currentNext)
		}
	}

	// Execute the middleware chain (which will eventually call the handler)
	return next(ctx, request)
}

func RegisterHandler[T Request](m Mediator, handler RequestHandler) error {
	var zero T
	requestType := reflect.TypeOf(zero)
	return m.Register(requestType, handler)
}
