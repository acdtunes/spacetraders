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
	"github.com/andrescamacho/spacetraders-go/internal/application/player"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/dtos"
)

// Mediator types
type (
	Request        = mediator.Request
	Response       = mediator.Response
	RequestHandler = mediator.RequestHandler
	HandlerFunc    = mediator.HandlerFunc
	Middleware     = mediator.Middleware
	Mediator       = mediator.Mediator
)

// Logging types
type ContainerLogger = logging.ContainerLogger

// Ship DTO types
type (
	RouteSegmentDTO = dtos.RouteSegmentDTO
	ShipRouteDTO    = dtos.ShipRouteDTO
)

// Mediator functions
var (
	NewMediator = mediator.NewMediator
)

// RegisterHandler is a generic function and must be called directly from mediator package
// Example: mediator.RegisterHandler[MyCommand](m, handler)
// For backward compatibility in non-generic contexts, use Mediator.Register directly

// Auth functions
var (
	WithPlayerToken        = auth.WithPlayerToken
	PlayerTokenFromContext = auth.PlayerTokenFromContext
	PlayerTokenMiddleware  = auth.PlayerTokenMiddleware
)

// Logging functions
var (
	WithLogger        = logging.WithLogger
	LoggerFromContext = logging.LoggerFromContext
)

// Ship DTO functions
var (
	RouteSegmentToDTO = dtos.RouteSegmentToDTO
)

// Player resolution types
// DEPRECATED: Import directly from github.com/andrescamacho/spacetraders-go/internal/application/player
type PlayerResolver = player.PlayerResolver

// Player resolution functions
var NewPlayerResolver = player.NewPlayerResolver
