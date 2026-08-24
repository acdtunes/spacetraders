package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// coordinatorPass holds the state one reconcile pass accumulates.
type coordinatorPass struct {
	h      *RunFleetCoordinatorHandler
	cmd    *RunFleetCoordinatorCommand
	result *RunFleetCoordinatorResponse
	errMon *health.Monitor
}

// shipPool is one pass's claimable pool. generalEntities backs the command frigate's
// last-resort verdict; dedicatedFleetActive means the pool is sealed.
//
// available may include an unclaimed in-transit dedicated member; dockable excludes it. Every
// consumer but the route-ETA selection - starting with contract negotiation - must read dockable.
type shipPool struct {
	available            []string
	dockable             []string
	generalEntities      []*navigation.Ship
	dedicatedFleetActive bool
}

// negotiationCandidate names the pool member NegotiateContract may claim: it rejects a ship
// mid-flight, so only a dockable member is ever offered - empty means the caller must wait.
func (p shipPool) negotiationCandidate() string {
	if len(p.dockable) > 0 {
		return p.dockable[0]
	}
	return ""
}

// An empty checkpoint keeps the failure off the error monitor and the captain outbox.
func (p *coordinatorPass) recordStepFailure(ctx context.Context, checkpoint, msg string, cause error) {
	common.LoggerFromContext(ctx).Log("ERROR", msg, nil)
	p.result.Errors = append(p.result.Errors, msg)
	if checkpoint != "" {
		if streak, crossed := p.errMon.Note(checkpoint, cause.Error()); crossed {
			p.h.recordErrorLoopEvent(ctx, p.cmd, checkpoint, cause, streak)
		}
	}
}

func (p *coordinatorPass) retryAfterStepFailure(ctx context.Context, checkpoint, msg string, cause error) {
	p.recordStepFailure(ctx, checkpoint, msg, cause)
	p.h.clock.Sleep(stepRetryBackoff)
}

// discoverShipPool resolves this pass's claimable pool. A false second result
// means the failure was already recorded and paced — retry the pass.
func (p *coordinatorPass) discoverShipPool(ctx context.Context) (shipPool, bool) {
	// The command ship is a first-class hauling candidate: it hauls contract
	// legs fine and is often the fastest, largest hull owned, so it competes
	// on distance like any hauler instead of sitting benched until zero
	// haulers remain.
	generalShipEntities, generalShips, err := appContract.FindIdleLightHaulers(ctx, p.cmd.PlayerID, p.h.shipRepo, "", appContract.IncludeCommandShip)
	if err != nil {
		p.retryAfterStepFailure(ctx, "find_idle_haulers", fmt.Sprintf("Failed to find idle haulers: %v", err), err)
		return shipPool{}, false
	}
	p.errMon.Note("find_idle_haulers", "")

	// FindIdleLightHaulers' generic cargo check only screens out probes
	// (CargoCapacity() == 0); a command hull below the cargo baseline would
	// double-trip a load a dedicated hauler moves in one pass, spending its
	// whole speed advantage for a net loss, so it is filtered back out here,
	// before any candidate is ranked or claimed.
	// That comparison needs a hauler dispatchable INSTEAD, so a fleet with none
	// AVAILABLE — none owned (cold start), or all busy — skips it and keeps the frigate.
	haulerAlternative, err := appContract.HaulerAlternativeAvailable(ctx, p.cmd.PlayerID, p.h.shipRepo, dedicatedFleetContract)
	if err != nil {
		p.retryAfterStepFailure(ctx, "", fmt.Sprintf("Failed to check for an available hauler alternative: %v", err), err)
		return shipPool{}, false
	}
	if haulerAlternative {
		generalShips = appContract.FilterCommandCargoBaseline(ctx, generalShipEntities, p.cmd.CommandCargoBaseline)
	}

	// The coordinator's own dedicated fleet is invisible to
	// FindIdleLightHaulers via the claim-filter, so it is looked up
	// separately here — by fleet NAME from the persisted tag, not the
	// remembered --dedicated-ships list, so a `fleet assign`/`unassign` takes
	// effect on the very next pass, no restart needed.
	// RequireCargoCapacity: a 0-cargo hull mispinned into the contract fleet
	// is UNSELECTABLE here — it can never carry a delivery, so claiming it
	// just spawns a worker that dies instantly. AdmitUnclaimedInTransit prices
	// an unclaimed mid-flight hull into SelectClosestShip's route-ETA ranking
	// below; idle-arb's own calls omit both policies.
	dedicatedIdleEntities, dedicatedIdleShips, err := appContract.FindIdleShipsByFleet(ctx, p.cmd.PlayerID, p.h.shipRepo, dedicatedFleetContract, appContract.RequireCargoCapacity, appContract.AdmitUnclaimedInTransit)
	if err != nil {
		p.retryAfterStepFailure(ctx, "", fmt.Sprintf("Failed to find idle dedicated ships: %v", err), err)
		return shipPool{}, false
	}

	// Excludes exactly the members AdmitUnclaimedInTransit widened in for above, from the
	// entities already fetched (no second query).
	dedicatedDockableShips := make([]string, 0, len(dedicatedIdleEntities))
	for _, ship := range dedicatedIdleEntities {
		if ship.NavStatus() != navigation.NavStatusInTransit {
			dedicatedDockableShips = append(dedicatedDockableShips, ship.ShipSymbol())
		}
	}

	// EXCLUSIVE MODE: a dedicated fleet, once tagged (via --dedicated-ships at
	// startup or a live `fleet assign` with no restart), is sealed — the
	// coordinator draws ONLY from its own idle members, even when that set is
	// empty because every member is busy.
	dedicatedFleetActive, err := appContract.FleetHasMembers(ctx, p.cmd.PlayerID, p.h.shipRepo, dedicatedFleetContract)
	if err != nil {
		p.retryAfterStepFailure(ctx, "", fmt.Sprintf("Failed to check dedicated fleet membership: %v", err), err)
		return shipPool{}, false
	}
	// SelectAvailableShips appends onto generalShips in the non-EXCLUSIVE branch, so calling it
	// twice with the same slice risks the second call overwriting the first's backing array. A
	// fresh copy keeps available and dockable independent.
	dockableGeneralShips := append([]string(nil), generalShips...)
	return shipPool{
		available:            appContract.SelectAvailableShips(generalShips, dedicatedIdleShips, dedicatedFleetActive),
		dockable:             appContract.SelectAvailableShips(dockableGeneralShips, dedicatedDockableShips, dedicatedFleetActive),
		generalEntities:      generalShipEntities,
		dedicatedFleetActive: dedicatedFleetActive,
	}, true
}
