package mediator

import (
	"context"
)

// Request represents a command or query
type Request interface{}

// Response represents the result of handling a request
type Response interface{}

// RequestHandler handles a specific request type
type RequestHandler interface {
	Handle(ctx context.Context, request Request) (Response, error)
}

// HandlerFunc is a function that handles a request
type HandlerFunc func(ctx context.Context, request Request) (Response, error)

// Middleware is a function that wraps handler execution with cross-cutting concerns
// Examples: authentication, logging, telemetry, circuit breakers, retries
type Middleware func(ctx context.Context, request Request, next HandlerFunc) (Response, error)
