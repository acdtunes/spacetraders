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
type shipPool struct {
	available            []string
	generalEntities      []*navigation.Ship
	dedicatedFleetActive bool
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
	generalShips = appContract.FilterCommandCargoBaseline(ctx, generalShipEntities, p.cmd.CommandCargoBaseline)

	// The coordinator's own dedicated fleet is invisible to
	// FindIdleLightHaulers via the claim-filter, so it is looked up
	// separately here — by fleet NAME from the persisted tag, not the
	// remembered --dedicated-ships list, so a `fleet assign`/`unassign` takes
	// effect on the very next pass, no restart needed.
	// RequireCargoCapacity: a 0-cargo hull mispinned into the contract fleet
	// is UNSELECTABLE here — it can never carry a delivery, so claiming it
	// just spawns a worker that dies instantly. The idle-arb dispatcher's own
	// FindIdleShipsByFleet calls omit the policy and keep every tagged
	// member, so its reserve accounting is unchanged.
	_, dedicatedIdleShips, err := appContract.FindIdleShipsByFleet(ctx, p.cmd.PlayerID, p.h.shipRepo, dedicatedFleetContract, appContract.RequireCargoCapacity)
	if err != nil {
		p.retryAfterStepFailure(ctx, "", fmt.Sprintf("Failed to find idle dedicated ships: %v", err), err)
		return shipPool{}, false
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
	return shipPool{
		available:            appContract.SelectAvailableShips(generalShips, dedicatedIdleShips, dedicatedFleetActive),
		generalEntities:      generalShipEntities,
		dedicatedFleetActive: dedicatedFleetActive,
	}, true
}
