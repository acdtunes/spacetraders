package queries

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// GetWaypointQuery fetches the detail (type, traits, coordinates) of a single
// waypoint. It auto-fetches from the API when the waypoint is not cached, so
// the captain can inspect a waypoint - such as the JUMP_GATE - without
// physically visiting it.
type GetWaypointQuery struct {
	WaypointSymbol string // Required: waypoint symbol (e.g. X1-PZ28-I55)
	PlayerID       *int   // Optional: query by player ID
	AgentSymbol    string // Optional: query by agent symbol
}

// GetWaypointResponse holds the requested waypoint.
type GetWaypointResponse struct {
	Waypoint *shared.Waypoint
}

// GetWaypointHandler handles the GetWaypoint query
type GetWaypointHandler struct {
	waypointProvider system.IWaypointProvider
	playerResolver   *common.PlayerResolver
}

// NewGetWaypointHandler creates a new GetWaypointHandler
func NewGetWaypointHandler(waypointProvider system.IWaypointProvider, playerRepo player.PlayerRepository) *GetWaypointHandler {
	return &GetWaypointHandler{
		waypointProvider: waypointProvider,
		playerResolver:   common.NewPlayerResolver(playerRepo),
	}
}

// Handle executes the GetWaypoint query
func (h *GetWaypointHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	query, ok := request.(*GetWaypointQuery)
	if !ok {
		return nil, fmt.Errorf("invalid request type: expected *GetWaypointQuery")
	}

	if query.WaypointSymbol == "" {
		return nil, fmt.Errorf("waypoint_symbol is required")
	}

	playerID, err := h.playerResolver.ResolvePlayerID(ctx, query.PlayerID, query.AgentSymbol)
	if err != nil {
		return nil, err
	}

	systemSymbol := shared.ExtractSystemSymbol(query.WaypointSymbol)

	waypoint, err := h.waypointProvider.GetWaypoint(ctx, query.WaypointSymbol, systemSymbol, playerID.Value())
	if err != nil {
		return nil, fmt.Errorf("failed to get waypoint %s: %w", query.WaypointSymbol, err)
	}

	return &GetWaypointResponse{
		Waypoint: waypoint,
	}, nil
}
