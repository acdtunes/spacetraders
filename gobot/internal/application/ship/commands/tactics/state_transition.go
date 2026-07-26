package tactics

import (
	"context"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

type stateTransition struct {
	ensure        func(*navigation.Ship) (bool, error)
	callAPI       func(context.Context, *navigation.Ship, shared.PlayerID) error
	doneStatus    string
	alreadyStatus string
}

func runStateTransition(ctx context.Context, shipRepo navigation.ShipRepository, cmd types.ShipCommand, transition stateTransition) (string, error) {
	ship, err := types.LoadShip(ctx, shipRepo, cmd)
	if err != nil {
		return "", err
	}

	stateChanged, err := transition.ensure(ship)
	if err != nil {
		return "", err
	}

	if !stateChanged {
		return transition.alreadyStatus, nil
	}

	if err := transition.callAPI(ctx, ship, cmd.GetPlayerID()); err != nil {
		// 4214 "ship is in transit": the server holds a transit the local
		// snapshot never recorded, so this dock/orbit was aimed at a phantom
		// position. The server nav is authoritative - adopt it so callers can
		// wait the transit out instead of crash-looping at the wrong waypoint.
		if types.IsShipInTransitAPIError(err) {
			return "", adoptServerTransit(ctx, shipRepo, cmd, ship, err)
		}
		return "", err
	}

	return transition.doneStatus, nil
}

// adoptServerTransit reconciles the local snapshot from the authoritative
// server nav after an in-transit (4214) rejection and returns a typed
// ErrShipInTransit carrying the real destination + arrival. When the
// authoritative read is unavailable nothing is adopted and the original
// rejection propagates unchanged - never fabricate nav state from a guess.
// Note ensure() has already flipped the local entity toward the target state;
// the overwrite below also undoes that speculative flip.
func adoptServerTransit(ctx context.Context, shipRepo navigation.ShipRepository, cmd types.ShipCommand, ship *navigation.Ship, cause error) error {
	fresh, syncErr := shipRepo.SyncShipFromAPI(ctx, ship.ShipSymbol(), cmd.GetPlayerID())
	if syncErr != nil || fresh == nil {
		return cause
	}
	*ship = *fresh

	transitDest := ""
	if loc := ship.CurrentLocation(); loc != nil {
		transitDest = loc.Symbol
	}
	var arrival time.Time
	if at := ship.ArrivalTime(); at != nil {
		arrival = *at
	}
	return &types.ErrShipInTransit{
		ShipSymbol:  ship.ShipSymbol(),
		Destination: transitDest,
		Arrival:     arrival,
		Cause:       cause,
	}
}
