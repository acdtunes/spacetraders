package common

// This file provides backward compatibility by re-exporting types from the new packages.
// This allows existing code to continue working while we gradually migrate imports.
//
// DEPRECATED: Import directly from the specific packages instead:
//   - github.com/andrescamacho/spacetraders-go/internal/application/mediator
//   - github.com/andrescamacho/spacetraders-go/internal/application/auth
//   - github.com/andrescamacho/spacetraders-go/internal/application/logging

import (
	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/application/mediator"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/dtos"
)

// Mediator types - re-exported for backward compatibility
type (
	Request        = mediator.Request
	Response       = mediator.Response
	RequestHandler = mediator.RequestHandler
	HandlerFunc    = mediator.HandlerFunc
	Middleware     = mediator.Middleware
	Mediator       = mediator.Mediator
)

// Logging types - re-exported for backward compatibility
type ContainerLogger = logging.ContainerLogger

// Ship DTO types - re-exported for backward compatibility
type (
	RouteSegmentDTO = dtos.RouteSegmentDTO
	ShipRouteDTO    = dtos.ShipRouteDTO
)

// Mediator functions - re-exported for backward compatibility
var (
	NewMediator = mediator.NewMediator
)

// RegisterHandler is a generic function and must be called directly from mediator package
// Example: mediator.RegisterHandler[MyCommand](m, handler)
// For backward compatibility in non-generic contexts, use Mediator.Register directly

// Auth functions - re-exported for backward compatibility
var (
	WithPlayerToken        = auth.WithPlayerToken
	PlayerTokenFromContext = auth.PlayerTokenFromContext
	PlayerTokenMiddleware  = auth.PlayerTokenMiddleware
)

// Logging functions - re-exported for backward compatibility
var (
	WithLogger        = logging.WithLogger
	LoggerFromContext = logging.LoggerFromContext
)

// Ship DTO functions - re-exported for backward compatibility
var (
	RouteSegmentToDTO = dtos.RouteSegmentToDTO
)
