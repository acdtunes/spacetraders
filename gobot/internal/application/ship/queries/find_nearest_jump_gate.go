package queries

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// FindNearestJumpGateQuery represents a query to find the nearest jump gate
type FindNearestJumpGateQuery struct {
	ShipSymbol  string // Required: ship symbol to find jump gate for
	PlayerID    *int   // Optional: query by player ID
	AgentSymbol string // Optional: query by agent symbol
}

// FindNearestJumpGateResponse represents the result of finding the nearest jump gate
type FindNearestJumpGateResponse struct {
	JumpGate      *shared.Waypoint
	Distance      float64
	SystemSymbol  string
}

// FindNearestJumpGateHandler handles the FindNearestJumpGate query
type FindNearestJumpGateHandler struct {
	shipRepo       navigation.ShipRepository
	graphProvider  system.ISystemGraphProvider
	playerResolver *common.PlayerResolver
}

// NewFindNearestJumpGateHandler creates a new FindNearestJumpGateHandler
func NewFindNearestJumpGateHandler(
	shipRepo navigation.ShipRepository,
	graphProvider system.ISystemGraphProvider,
	playerRepo player.PlayerRepository,
) *FindNearestJumpGateHandler {
	return &FindNearestJumpGateHandler{
		shipRepo:       shipRepo,
		graphProvider:  graphProvider,
		playerResolver: common.NewPlayerResolver(playerRepo),
	}
}

// Handle executes the FindNearestJumpGate query
func (h *FindNearestJumpGateHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	query, ok := request.(*FindNearestJumpGateQuery)
	if !ok {
		return nil, fmt.Errorf("invalid request type: expected *FindNearestJumpGateQuery")
	}

	if query.ShipSymbol == "" {
		return nil, fmt.Errorf("ship_symbol is required")
	}

	playerID, err := h.playerResolver.ResolvePlayerID(ctx, query.PlayerID, query.AgentSymbol)
	if err != nil {
		return nil, err
	}

	// Get ship to determine current location and system
	ship, err := h.shipRepo.FindBySymbol(ctx, query.ShipSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get ship: %w", err)
	}

	currentLocation := ship.CurrentLocation()
	systemSymbol := currentLocation.SystemSymbol

	// Get system graph to access all waypoints
	graphResult, err := h.graphProvider.GetGraph(ctx, systemSymbol, false, playerID.Value())
	if err != nil {
		return nil, fmt.Errorf("failed to get system graph for %s: %w", systemSymbol, err)
	}

	// Filter for jump gates
	var jumpGates []*shared.Waypoint
	for _, waypoint := range graphResult.Graph.Waypoints {
		if waypoint.IsJumpGate() {
			jumpGates = append(jumpGates, waypoint)
		}
	}

	if len(jumpGates) == 0 {
		return nil, fmt.Errorf("no jump gates found in system %s", systemSymbol)
	}

	// Find nearest jump gate
	nearest, distance := shared.FindNearestWaypoint(currentLocation, jumpGates)

	return &FindNearestJumpGateResponse{
		JumpGate:     nearest,
		Distance:     distance,
		SystemSymbol: systemSymbol,
	}, nil
}
