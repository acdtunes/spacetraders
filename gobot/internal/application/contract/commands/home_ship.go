package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractTypes "github.com/andrescamacho/spacetraders-go/internal/application/contract/types"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// Type aliases for convenience
type HomeShipCommand = contractTypes.HomeShipCommand
type HomeShipResponse = contractTypes.HomeShipResponse

// HomeShipHandler dispatches an idle dedicated contract hull to its FIXED placement slot: each
// delivery hull permanently OWNS one waypoint — the symbol-zip of the delivery roster (FleetShips)
// onto the placement slots (StandbyStations), computed by the pure domainContract.AssignedSlot. The
// assignment depends ONLY on the roster and the slot set — NO demand ranking, NO occupancy, NO
// live or peer position — so N hulls land on N DISTINCT slots, identically across restarts, and a
// second pass moves no hull. A runtime demand/occupancy distributor cannot promise that: concurrent
// homing races pile idle hulls onto the top-demand hub. A hull beyond the slot count owns no slot
// and is left where it is for the scaler to re-role into a warehouse.
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

	// Homing is opt-in: an empty --standby-stations list disables relocation entirely. The
	// claim-filter still keeps the ship reserved either way.
	if len(cmd.StandbyStations) == 0 {
		return &HomeShipResponse{Navigated: false}, nil
	}

	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("failed to load ship %s: %w", cmd.ShipSymbol, err)
	}

	// Homing applies to idle standby hulls only: a hull that is claimed/assigned or mid-flight is
	// never relocated, no matter what the dispatcher believed when it fired this command.
	if !ship.IsIdle() || ship.IsInTransit() {
		logger.Log("INFO", "Skipping homing for busy hull", map[string]interface{}{
			"action":      "home_ship",
			"ship_symbol": cmd.ShipSymbol,
			"idle":        ship.IsIdle(),
			"in_transit":  ship.IsInTransit(),
		})
		return &HomeShipResponse{Navigated: false}, nil
	}

	// FIXED PLACEMENT: this hull's permanent slot is the symbol-zip of the delivery roster onto the ≤6
	// placement slots — no demand, no occupancy, no peer position, so every independent homing call for
	// the same roster lands each hull in its OWN distinct slot. A surplus hull (beyond the knee) owns no
	// slot; it is left where it is for the scaler to re-role into a warehouse rather than piled onto an
	// occupied slot.
	slot, owns := domainContract.AssignedSlot(cmd.ShipSymbol, cmd.FleetShips, cmd.StandbyStations)
	if !owns {
		logger.Log("INFO", "No fixed slot for hull (surplus over the delivery knee) — left for re-role", map[string]interface{}{
			"action":      "home_ship",
			"ship_symbol": cmd.ShipSymbol,
		})
		return &HomeShipResponse{Navigated: false}, nil
	}

	systemSymbol := ship.CurrentLocation().SystemSymbol
	graphResult, err := h.graphProvider.GetGraph(ctx, systemSymbol, false, cmd.PlayerID.Value())
	if err != nil {
		return nil, fmt.Errorf("failed to load system graph: %w", err)
	}
	target, inGraph := graphResult.Graph.Waypoints[slot]
	if !inGraph {
		// The hull's assigned slot is not in its current system graph — an operator misconfiguration or a
		// foreign hull the scaler's cross-gate step should have repositioned first. Surface it, never a
		// silent no-op.
		return nil, fmt.Errorf("assigned standby slot %s for ship %s not found in system %s graph", slot, cmd.ShipSymbol, systemSymbol)
	}

	// A hull already at ITS assigned slot stays — "already home ONLY if at MY slot", not "at ANY
	// standby station", which would let a hull sit on a peer's slot forever. A second homing pass
	// therefore moves no hull (no thrash).
	if ship.CurrentLocation().Symbol == slot {
		return &HomeShipResponse{TargetStation: slot, Distance: 0, Navigated: false}, nil
	}

	distance := ship.CurrentLocation().DistanceTo(target)
	logger.Log("INFO", "Homing hull to its fixed standby slot", map[string]interface{}{
		"action":      "home_ship",
		"ship_symbol": cmd.ShipSymbol,
		"slot":        slot,
		"distance":    distance,
	})

	navigateCmd := &shipNav.NavigateRouteCommand{
		ShipSymbol:  cmd.ShipSymbol,
		Destination: slot,
		PlayerID:    cmd.PlayerID,
	}
	if _, err := h.mediator.Send(ctx, navigateCmd); err != nil {
		return nil, fmt.Errorf("failed to navigate to standby slot: %w", err)
	}

	return &HomeShipResponse{TargetStation: slot, Distance: distance, Navigated: true}, nil
}
