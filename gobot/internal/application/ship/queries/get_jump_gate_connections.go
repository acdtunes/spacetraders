package queries

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// GetJumpGateConnectionsQuery discovers the systems directly reachable by a
// single jump from a given system's own jump gate. Unlike
// FindNearestJumpGateQuery (ship-relative: "nearest gate to THIS ship"),
// this is purely SystemSymbol-based, matching the multi-system trade-route
// lane scanner's system-scoped needs (sp-wlev).
type GetJumpGateConnectionsQuery struct {
	SystemSymbol string // Required: system to discover jump connections from
	PlayerID     *int   // Optional: query by player ID
	AgentSymbol  string // Optional: query by agent symbol
}

// GetJumpGateConnectionsResponse reports the queried system's own jump gate
// plus the set of system symbols one hop away via that gate's live
// connections. Multiple gates per system are not handled (out of scope for
// sp-wlev): the first gate found in the system graph is used.
type GetJumpGateConnectionsResponse struct {
	JumpGate         *shared.Waypoint
	ConnectedSystems []string
}

// GetJumpGateConnectionsHandler handles the GetJumpGateConnections query
type GetJumpGateConnectionsHandler struct {
	graphProvider  system.ISystemGraphProvider
	apiClient      ports.APIClient
	playerRepo     player.PlayerRepository
	playerResolver *common.PlayerResolver
}

// NewGetJumpGateConnectionsHandler creates a new GetJumpGateConnectionsHandler
func NewGetJumpGateConnectionsHandler(
	graphProvider system.ISystemGraphProvider,
	apiClient ports.APIClient,
	playerRepo player.PlayerRepository,
) *GetJumpGateConnectionsHandler {
	return &GetJumpGateConnectionsHandler{
		graphProvider:  graphProvider,
		apiClient:      apiClient,
		playerRepo:     playerRepo,
		playerResolver: common.NewPlayerResolver(playerRepo),
	}
}

// Handle executes the GetJumpGateConnections query
func (h *GetJumpGateConnectionsHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	query, ok := request.(*GetJumpGateConnectionsQuery)
	if !ok {
		return nil, fmt.Errorf("invalid request type: expected *GetJumpGateConnectionsQuery")
	}

	if query.SystemSymbol == "" {
		return nil, fmt.Errorf("system_symbol is required")
	}

	playerID, err := h.playerResolver.ResolvePlayerID(ctx, query.PlayerID, query.AgentSymbol)
	if err != nil {
		return nil, err
	}

	playerEntity, err := h.playerRepo.FindByID(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get player: %w", err)
	}

	// Get system graph to find the system's own jump gate.
	graphResult, err := h.graphProvider.GetGraph(ctx, query.SystemSymbol, false, playerID.Value())
	if err != nil {
		return nil, fmt.Errorf("failed to get system graph for %s: %w", query.SystemSymbol, err)
	}

	var gate *shared.Waypoint
	for _, waypoint := range graphResult.Graph.Waypoints {
		if waypoint.IsJumpGate() {
			gate = waypoint
			break
		}
	}

	if gate == nil {
		return nil, fmt.Errorf("no jump gate found in system %s", query.SystemSymbol)
	}

	// Resolve the gate's live connections via the API - the system graph
	// only carries waypoints/edges within the system, not cross-system gate
	// links.
	gateData, err := h.apiClient.GetJumpGate(ctx, query.SystemSymbol, gate.Symbol, playerEntity.Token)
	if err != nil {
		return nil, fmt.Errorf("failed to get jump gate connections for %s: %w", gate.Symbol, err)
	}

	seen := make(map[string]bool, len(gateData.Connections))
	connectedSystems := make([]string, 0, len(gateData.Connections))
	for _, waypointSymbol := range gateData.Connections {
		connSystem := shared.ExtractSystemSymbol(waypointSymbol)
		if connSystem == query.SystemSymbol || seen[connSystem] {
			continue
		}
		seen[connSystem] = true
		connectedSystems = append(connectedSystems, connSystem)
	}

	return &GetJumpGateConnectionsResponse{
		JumpGate:         gate,
		ConnectedSystems: connectedSystems,
	}, nil
}
