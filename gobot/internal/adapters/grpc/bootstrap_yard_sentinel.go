package grpc

import (
	"context"
	"fmt"

	bootstrapCmd "github.com/andrescamacho/spacetraders-go/internal/application/bootstrap/commands"
	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// bootstrapYardSentinelAcquirer manages the standing yard-sentinel probe's whole lifecycle:
// buy+reserve, the idempotent navigate+dock positioning, and the EXPANSION-hand-off release. Embeds
// bootstrapAcquirer to reuse its cheapest-yard PriceCheck, the money-integrity buy path (buyWith), the
// ship repo, and the waypoint trait lookup — mirroring bootstrapHaulerAcquirer's embedding, and needing
// NO fields of its own for the same reason: every dependency already lives on the embedded struct.
type bootstrapYardSentinelAcquirer struct {
	*bootstrapAcquirer
}

// BuyAndReserve buys ONE shipType at yard (the reused money-integrity batch path) and immediately
// reserves the bought hull for the captain with `reason` — never a DedicatedFleet tag. See
// bootstrapCmd.YardSentinelAcquirer's doc for why the claim/assignment axis is what protects the hull
// from selectHomeTourHulls (no change needed to that function) while keeping it in
// adoptStrandedProbes's "" allowlist case the instant the reservation is released.
func (a *bootstrapYardSentinelAcquirer) BuyAndReserve(ctx context.Context, playerID int, shipType, yard, reason, purchaserSymbol string) (bootstrapCmd.BuyResult, error) {
	bought, err := a.buyWith(ctx, playerID, shipType, yard, purchaserSymbol)
	if err != nil {
		return bootstrapCmd.BuyResult{}, err
	}
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return bought, err
	}
	if rerr := a.shipRepo.ReserveForCaptain(ctx, bought.ShipSymbol, reason, pid); rerr != nil {
		return bought, fmt.Errorf("reserve yard sentinel %s for the captain: %w", bought.ShipSymbol, rerr)
	}
	return bought, nil
}

// EnsureParked flies the sentinel to the home shipyard and docks it — idempotent and best-effort,
// re-derived from a live ship read on every call exactly like ShipyardScanner.EnsureShipyardReadable
// (whose home-yard discovery this reuses, over the SAME waypointTraitLister): already docked at the
// yard ⇒ no-op (docked=true, no port call); mid-flight ⇒ wait (docked=false); standing at the yard
// undocked ⇒ dock; otherwise ⇒ navigate (arrives in ORBIT — presence alone is what a shipyard price
// read needs; docking is this function's OWN next call, once the arrival is observed).
//
// docked=false on every non-terminal branch is a WAIT, never a failure, mirroring
// EnsureShipyardReadable's own dispatched=false contract — a hull already en route from an earlier
// call must not be re-navigated, and a fresh buy the roster has not synced yet simply retries next tick.
func (a *bootstrapYardSentinelAcquirer) EnsureParked(ctx context.Context, playerID int, homeSystem, shipSymbol string) (bool, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return false, nil
	}
	ships, err := a.shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return false, nil
	}
	var sentinel *navigation.Ship
	for _, s := range ships {
		if s != nil && s.ShipSymbol() == shipSymbol {
			sentinel = s
			break
		}
	}
	if sentinel == nil {
		return false, nil // the fresh buy has not synced to the roster yet — retried next tick
	}
	if sentinel.IsInTransit() {
		return false, nil // an earlier navigate is still under way
	}

	yardWps, err := a.waypointRepo.ListBySystemWithTrait(ctx, homeSystem, shipyardTrait)
	if err != nil {
		return false, nil
	}
	dest := ""
	isYard := map[string]struct{}{}
	for _, wp := range yardWps {
		if wp == nil {
			continue
		}
		isYard[wp.Symbol] = struct{}{}
		if dest == "" {
			dest = wp.Symbol
		}
	}
	if dest == "" {
		return false, nil // no known home-system shipyard yet — retry once waypoint data arrives
	}

	atYard := false
	if loc := sentinel.CurrentLocation(); loc != nil {
		_, atYard = isYard[loc.Symbol]
	}
	if !atYard {
		if _, nerr := a.med.Send(ctx, &navCmd.NavigateRouteCommand{ShipSymbol: shipSymbol, Destination: dest, PlayerID: pid}); nerr != nil {
			return false, fmt.Errorf("navigate yard sentinel %s to home shipyard %s: %w", shipSymbol, dest, nerr)
		}
		return false, nil // now in transit; a later tick docks it once arrival is observed
	}
	if sentinel.IsDocked() {
		return true, nil
	}
	if _, derr := a.med.Send(ctx, &shipTypes.DockShipCommand{ShipSymbol: shipSymbol, PlayerID: pid}); derr != nil {
		return false, fmt.Errorf("dock yard sentinel %s at the home shipyard %s: %w", shipSymbol, dest, derr)
	}
	return true, nil
}

// Release clears the sentinel's captain reservation (the EXPANSION hand-off), returning it to plain
// idle with DedicatedFleet still "" — see bootstrapCmd.YardSentinelAcquirer's doc for why that alone
// makes the already boot-standing probe-sensing coordinator's adoptStrandedProbes pick it up.
func (a *bootstrapYardSentinelAcquirer) Release(ctx context.Context, playerID int, shipSymbol, reason string) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}
	return a.shipRepo.ReleaseCaptainReservation(ctx, shipSymbol, reason, pid)
}
