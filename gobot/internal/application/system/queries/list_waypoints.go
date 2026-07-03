package queries

import (
	"context"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// ListWaypointsQuery lists the waypoints of a system from the daemon's waypoint
// graph. Unlike the market cache (which only holds physically-visited
// MARKETPLACE waypoints), this surfaces every waypoint - including the
// JUMP_GATE - and auto-syncs the system from the API when the cache is empty
// or stale.
type ListWaypointsQuery struct {
	SystemSymbol string // Required: system to enumerate (e.g. X1-PZ28)
	Trait        string // Optional: keep only waypoints with this trait (e.g. SHIPYARD)
	Type         string // Optional: keep only waypoints of this type (e.g. JUMP_GATE)
	PlayerID     *int   // Optional: query by player ID
	AgentSymbol  string // Optional: query by agent symbol
}

// ListWaypointsResponse holds the matching waypoints sorted by symbol.
type ListWaypointsResponse struct {
	Waypoints []*shared.Waypoint
}

// ListWaypointsHandler handles the ListWaypoints query
type ListWaypointsHandler struct {
	graphProvider  system.ISystemGraphProvider
	playerResolver *common.PlayerResolver
}

// NewListWaypointsHandler creates a new ListWaypointsHandler
func NewListWaypointsHandler(graphProvider system.ISystemGraphProvider, playerRepo player.PlayerRepository) *ListWaypointsHandler {
	return &ListWaypointsHandler{
		graphProvider:  graphProvider,
		playerResolver: common.NewPlayerResolver(playerRepo),
	}
}

// Handle executes the ListWaypoints query
func (h *ListWaypointsHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	query, ok := request.(*ListWaypointsQuery)
	if !ok {
		return nil, fmt.Errorf("invalid request type: expected *ListWaypointsQuery")
	}

	if query.SystemSymbol == "" {
		return nil, fmt.Errorf("system_symbol is required")
	}

	playerID, err := h.playerResolver.ResolvePlayerID(ctx, query.PlayerID, query.AgentSymbol)
	if err != nil {
		return nil, err
	}

	// GetGraph is cache-first and builds from the API when the system is not
	// yet cached, so this satisfies the "sync when empty/stale" requirement.
	graphResult, err := h.graphProvider.GetGraph(ctx, query.SystemSymbol, false, playerID.Value())
	if err != nil {
		return nil, fmt.Errorf("failed to get system graph for %s: %w", query.SystemSymbol, err)
	}

	waypoints := make([]*shared.Waypoint, 0, len(graphResult.Graph.Waypoints))
	for _, waypoint := range graphResult.Graph.Waypoints {
		if query.Trait != "" && !waypoint.HasTrait(query.Trait) {
			continue
		}
		if query.Type != "" && waypoint.Type != query.Type {
			continue
		}
		waypoints = append(waypoints, waypoint)
	}

	// Deterministic ordering: map iteration order is random.
	sort.Slice(waypoints, func(i, j int) bool {
		return waypoints[i].Symbol < waypoints[j].Symbol
	})

	return &ListWaypointsResponse{
		Waypoints: waypoints,
	}, nil
}
