package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// repositionPreviousShip homes a dedicated member, re-balances anything else. Both
// dispatch on a background context: the coordinator's ctx may cancel mid-move.
func (h *RunFleetCoordinatorHandler) repositionPreviousShip(ctx context.Context, cmd *RunFleetCoordinatorCommand, previousShipSymbol, selectedShip string) {
	if previousShipSymbol == "" || previousShipSymbol == selectedShip {
		return
	}
	logger := common.LoggerFromContext(ctx)

	// LIVE dedicated-fleet membership, not the frozen launch list: a hull
	// `fleet add`ed after launch homes between legs like any dedicated
	// member, and this same list is the standby-station occupancy peer set.
	dedicatedMembers := resolveDedicatedMembersForHoming(ctx, logger, h.shipRepo, cmd.PlayerID, dedicatedFleetContract, cmd.DedicatedShips)
	if !isDedicatedShip(previousShipSymbol, dedicatedMembers) {
		logger.Log("INFO", fmt.Sprintf("Selected ship changed from %s to %s - balancing previous ship position", previousShipSymbol, selectedShip), nil)

		opCtx := shared.OperationContextFromContext(ctx)
		go func(shipSymbol string, playerID shared.PlayerID, coordinatorID string) {
			balanceCmd := &BalanceShipPositionCommand{
				ShipSymbol:    shipSymbol,
				PlayerID:      playerID,
				CoordinatorID: coordinatorID,
			}
			// The goroutine deliberately starts from a background context so the flight outlives the
			// tick that scheduled it. Cancellation is what must not cross that boundary; the operation
			// the work belongs to still must, or the fuel it burns is spend nobody can attribute.
			balanceCtx := shared.WithOperationContext(common.WithLogger(context.Background(), common.LoggerFromContext(ctx)), opCtx)

			_, err := h.fleetPoolManager.GetMediator().Send(balanceCtx, balanceCmd)
			if err != nil {
				logger.Log("WARNING", fmt.Sprintf("Failed to balance ship %s position: %v", shipSymbol, err), nil)
			}
		}(previousShipSymbol, cmd.PlayerID, cmd.ContainerID)
		return
	}

	logger.Log("INFO", fmt.Sprintf("Selected ship changed from %s to %s - homing dedicated ship %s to standby station", previousShipSymbol, selectedShip, previousShipSymbol), nil)

	// LIVE standby-station set, not the frozen launch snapshot: a hub
	// `fleet hub add`ed after launch draws idle hulls toward it and a
	// removed one re-homes its hulls to the remaining set, all with no
	// restart. Falls back to cmd.StandbyStations on a read failure or
	// when no provider is wired.
	liveStandby := appContract.ResolveStandbyStations(ctx, logger, h.standbyProvider, cmd.ContainerID, cmd.PlayerID.Value(), cmd.StandbyStations)

	// Resolve the FIXED placement slots when the fleet-hub set is empty, so between-legs homing
	// sends each idle hull to its permanent slot instead of piling. Nil-safe: liveStandby unchanged.
	// The homing zips this hull to its slot by symbol against the dedicated roster, not by demand.
	liveStandby = appContract.ResolveStandbyForHoming(ctx, logger, h.standbyPlacementProvider, cmd.PlayerID.Value(), liveStandby)

	opCtx := shared.OperationContextFromContext(ctx)
	go func(shipSymbol string, playerID shared.PlayerID, standbyStations []string, fleetShips []string) {
		homeCmd := &HomeShipCommand{
			ShipSymbol:      shipSymbol,
			PlayerID:        playerID,
			StandbyStations: standbyStations,
			FleetShips:      fleetShips,
		}
		// The goroutine deliberately starts from a background context so the flight outlives the
		// tick that scheduled it. Cancellation is what must not cross that boundary; the operation
		// the work belongs to still must, or the fuel it burns is spend nobody can attribute.
		homeCtx := shared.WithOperationContext(common.WithLogger(context.Background(), common.LoggerFromContext(ctx)), opCtx)

		_, err := h.fleetPoolManager.GetMediator().Send(homeCtx, homeCmd)
		if err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to home dedicated ship %s: %v", shipSymbol, err), nil)
		}
	}(previousShipSymbol, cmd.PlayerID, liveStandby, dedicatedMembers)
}

// homeCompletedHullToStandby dispatches a just-completed contract-work hull to a
// demand-ranked standby sink THE MOMENT its worker finishes, so the hull
// does not loiter at the delivery waypoint until the next between-legs selection
// change or the ~90s idle-arb sweep.
func (h *RunFleetCoordinatorHandler) homeCompletedHullToStandby(ctx context.Context, cmd *RunFleetCoordinatorCommand, shipSymbol string) {
	logger := common.LoggerFromContext(ctx)

	ship, err := h.shipRepo.FindBySymbol(ctx, shipSymbol, cmd.PlayerID)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("immediate homing: failed to load completed hull %s (skipping fast re-home): %v", shipSymbol, err), nil)
		return
	}
	// The command frigate homes on its own last-resort rules, never parked as a
	// standby drifter (RULINGS #7) — mirror the candidate-side IsCommandHull gate.
	if domainContract.IsCommandHull(ship) {
		return
	}

	// The SAME live-set resolution the between-legs hook uses: live `fleet hub` pins, or the ≤6
	// fixed placement slots auto-driving the set when no hub is pinned.
	// Nil-safe → launch snapshot.
	liveStandby := appContract.ResolveStandbyStations(ctx, logger, h.standbyProvider, cmd.ContainerID, cmd.PlayerID.Value(), cmd.StandbyStations)
	liveStandby = appContract.ResolveStandbyForHoming(ctx, logger, h.standbyPlacementProvider, cmd.PlayerID.Value(), liveStandby)

	dedicatedMembers := resolveDedicatedMembersForHoming(ctx, logger, h.shipRepo, cmd.PlayerID, dedicatedFleetContract, cmd.DedicatedShips)

	// No-thrash: a hull already at ITS OWN assigned fixed slot skips the redundant dispatch. The test
	// is "at MY slot", never "at ANY sink" — two hulls that both delivered to one sink would
	// otherwise both skip and pile there. A hull that owns no slot is left for the scaler to re-role.
	slot, owns := domainContract.AssignedSlot(shipSymbol, dedicatedMembers, liveStandby)
	if !owns || ship.CurrentLocation().Symbol == slot {
		return
	}

	// Fire-and-forget on a background context carrying the container logger — the
	// SAME async dispatch the between-legs hook uses (HomeShipCommand blocks for the
	// whole flight, so a synchronous send would stall the coordinator loop).
	opCtx := shared.OperationContextFromContext(ctx)
	go func(shipSymbol string, playerID shared.PlayerID, standbyStations []string, fleetShips []string) {
		homeCmd := &HomeShipCommand{
			ShipSymbol:      shipSymbol,
			PlayerID:        playerID,
			StandbyStations: standbyStations,
			FleetShips:      fleetShips,
		}
		// The goroutine deliberately starts from a background context so the flight outlives the
		// tick that scheduled it. Cancellation is what must not cross that boundary; the operation
		// the work belongs to still must, or the fuel it burns is spend nobody can attribute.
		homeCtx := shared.WithOperationContext(common.WithLogger(context.Background(), logger), opCtx)
		if _, err := h.fleetPoolManager.GetMediator().Send(homeCtx, homeCmd); err != nil {
			logger.Log("WARNING", fmt.Sprintf("immediate homing: failed to home completed hull %s: %v", shipSymbol, err), nil)
		}
	}(shipSymbol, cmd.PlayerID, liveStandby, dedicatedMembers)
}
