package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractTypes "github.com/andrescamacho/spacetraders-go/internal/application/contract/types"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// Type aliases for convenience
type HomeShipCommand = contractTypes.HomeShipCommand
type HomeShipResponse = contractTypes.HomeShipResponse

// HomeShipHandler dispatches an idle dedicated contract ship to the nearest
// operator-configured standby station (sp-snmb). Unlike
// BalanceShipPositionHandler this is a stateless "closest station, go there"
// operation - no temporary container/assignment ceremony, because a
// dedicated ship is already permanently invisible to other coordinators via
// the DedicatedFleet claim-filter, and the contract coordinator's own
// FindIdleDedicatedShips already excludes in-transit ships from re-claiming
// during the homing trip.
type HomeShipHandler struct {
	mediator      common.Mediator
	shipRepo      navigation.ShipRepository
	graphProvider system.ISystemGraphProvider
}

// NewHomeShipHandler creates a new homing handler.
func NewHomeShipHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	graphProvider system.ISystemGraphProvider,
) *HomeShipHandler {
	return &HomeShipHandler{
		mediator:      mediator,
		shipRepo:      shipRepo,
		graphProvider: graphProvider,
	}
}

// Handle executes the homing dispatch.
func (h *HomeShipHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*HomeShipCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	logger := common.LoggerFromContext(ctx)

	// Homing is opt-in: an empty --standby-stations list disables relocation
	// entirely. The claim-filter still keeps the ship reserved either way.
	if len(cmd.StandbyStations) == 0 {
		return &HomeShipResponse{Navigated: false}, nil
	}

	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("failed to load ship %s: %w", cmd.ShipSymbol, err)
	}

	systemSymbol := ship.CurrentLocation().SystemSymbol
	graphResult, err := h.graphProvider.GetGraph(ctx, systemSymbol, false, cmd.PlayerID.Value())
	if err != nil {
		return nil, fmt.Errorf("failed to load system graph: %w", err)
	}

	var candidates []*shared.Waypoint
	for _, symbol := range cmd.StandbyStations {
		wp, ok := graphResult.Graph.Waypoints[symbol]
		if !ok {
			continue // Skip stations not found in this system's graph.
		}
		candidates = append(candidates, wp)
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("none of the configured standby stations %v found in system %s graph", cmd.StandbyStations, systemSymbol)
	}

	nearest, distance := shared.FindNearestWaypoint(ship.CurrentLocation(), candidates)

	logger.Log("INFO", "Standby station selected for homing", map[string]interface{}{
		"action":      "home_ship",
		"ship_symbol": cmd.ShipSymbol,
		"station":     nearest.Symbol,
		"distance":    distance,
	})

	if ship.IsAtLocation(nearest) {
		return &HomeShipResponse{TargetStation: nearest.Symbol, Distance: distance, Navigated: false}, nil
	}

	navigateCmd := &shipNav.NavigateRouteCommand{
		ShipSymbol:  cmd.ShipSymbol,
		Destination: nearest.Symbol,
		PlayerID:    cmd.PlayerID,
	}
	if _, err := h.mediator.Send(ctx, navigateCmd); err != nil {
		return nil, fmt.Errorf("failed to navigate to standby station: %w", err)
	}

	logger.Log("INFO", "Dedicated ship homing to standby station", map[string]interface{}{
		"action":      "home_ship",
		"ship_symbol": cmd.ShipSymbol,
		"station":     nearest.Symbol,
		"distance":    distance,
	})

	return &HomeShipResponse{TargetStation: nearest.Symbol, Distance: distance, Navigated: true}, nil
}
