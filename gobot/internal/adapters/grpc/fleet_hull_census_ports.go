package grpc

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Fleet-wide hull census helpers, shared by every coordinator that counts or locates the player's
// hulls: the growth coordinator's trade-pool count, the contract scaler's contract-pool count, and
// the yard readers' trading-grounds walk.

// distinctShipSystems returns the distinct systems the player's hulls are located in — the trading
// grounds the yard readers scan for shipyards.
func distinctShipSystems(ships []*navigation.Ship) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(ships))
	for _, sh := range ships {
		loc := sh.CurrentLocation()
		if loc == nil {
			continue
		}
		system := shared.ExtractSystemSymbol(loc.Symbol)
		if _, ok := seen[system]; ok {
			continue
		}
		seen[system] = struct{}{}
		out = append(out, system)
	}
	return out
}

// presentHullWaypoints is the set of waypoints a hull of ours occupies — the yards that can answer a
// live shipyard read with a price at all.
//
// INBOUND HULLS COUNT. A flying hull's location row is its DESTINATION, so an arrival not yet synced
// is indistinguishable from one in flight; admitting both costs a wasted read where excluding them
// would silently drop an ask out of the premium denominator. Deliberately CLOCKLESS — refining this
// with an arrival time would put a clock behind a money guard's input.
func presentHullWaypoints(ships []*navigation.Ship) map[string]bool {
	out := make(map[string]bool, len(ships))
	for _, sh := range ships {
		if sh == nil {
			continue
		}
		if loc := sh.CurrentLocation(); loc != nil {
			out[loc.Symbol] = true
		}
	}
	return out
}

func countShips(ctx context.Context, shipRepo navigation.ShipRepository, playerID int, pred func(*navigation.Ship) bool) (int, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return 0, err
	}
	ships, err := shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, s := range ships {
		if pred(s) {
			n++
		}
	}
	return n, nil
}
