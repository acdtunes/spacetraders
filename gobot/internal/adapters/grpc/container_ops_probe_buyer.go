package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ProbeBuyerFleetCoordinator is RETIRED and DELETED (Admiral, 2026-07-28). The probe-sensing
// coordinator (probe_sensing_coordinator, boot-standing) owns probe supply now, and it should have
// been retired when the sensing model landed; it was not, and when bootstrap advanced to EXPANSION
// it spent 245,316 credits on 9 SHIP_PROBE in five minutes, taking the treasury 188,857 -> 90,842.
//
// The buying was never the point of it: the sensing engine's own drain buys what its placements
// need, behind a floor and a cap, and the parked-probe model reuses hulls it already owns before it
// buys another. A second engine buying into the same fleet could only ever double-spend.
//
// The coordinator, its container type, its wiring, its tune knobs and its launch path are all gone.
// The verb is kept — and only the verb — so a residual caller (an old CLI, a script, a captain
// habit) gets an honest answer rather than a missing method, exactly as the frontier-expansion and
// market-freshness retirements do. Its command type is in retiredCommandTypes, so a persisted row
// of the old type is marked terminated at recovery instead of alarming as an unexplained loss.
//
// The hulls it recruited keep their "probe-buyer" dedicated_fleet tag — nothing rewrites a ships
// row on retirement — so sensing adoption carries that tag in its adoptable allowlist
// (legacyProbeBuyerFleetTag) and absorbs them back. Deleting the coordinator does NOT strand them.
func (s *DaemonServer) ProbeBuyerFleetCoordinator(ctx context.Context, playerID int, tickIntervalSecs int) (string, error) {
	return "", fmt.Errorf("the probe-buyer fleet coordinator is retired: the probe-sensing coordinator (boot-standing) owns probe supply — operate it via `tune --operation sensing`")
}

// legacyProbeBuyerFleetTag is the dedicated_fleet tag the deleted coordinator wrote. Duplicated as
// a literal here rather than imported, because the package that defined it is gone — this and
// sensing's adoption allowlist are its last two readers.
const legacyProbeBuyerFleetTag = "probe-buyer"

// releaseRetiredProbeBuyerHulls clears dedicated_fleet="probe-buyer" from every hull still carrying
// it, at daemon boot.
//
// WHY THE DELETION IS NOT COMPLETE WITHOUT THIS. Retiring a coordinator does not rewrite a ships
// row, so its recruits keep pointing at a fleet that no longer exists — TORWIND-2 and TORWIND-E on
// the live fleet. That tag is not inert: sensing's DockedProbeAt admits ONLY "" and
// sensing_parked, so a probe-buyer-tagged hull is invisible to the buy path, and adoption only
// retags the ones it actually absorbs (a hull standing on an already-occupied waypoint is skipped
// and keeps the tag indefinitely). A hull dedicated to a deleted coordinator is driven by nobody
// and reachable by nobody.
//
// Clearing rather than teaching the readers to accept the tag, and the reason is in DockedProbeAt's
// own contract: a hull tagged to a foreign fleet is rejected by the claim path PERMANENTLY, so
// admitting one there would have the buy queue select it, pay for a live price read, fail the
// claim, and select the same hull again every tick forever — the standing API drain that filter
// exists to prevent. Widening the filter would trade a stranded hull for a permanent leak.
//
// Goes through AssignFleet, the single dedicated_fleet write path, rather than a raw UPDATE.
// Idempotent: after the first boot nothing matches, so it costs one read and no writes. Fail-open
// and non-fatal — a daemon that cannot reach the ships table must still start.
func (s *DaemonServer) releaseRetiredProbeBuyerHulls(ctx context.Context, playerID int) {
	if s.shipRepo == nil || playerID <= 0 {
		return
	}
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return
	}
	ships, err := s.shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		fmt.Printf("Warning: could not release hulls of the retired probe-buyer fleet: %v\n", err)
		return
	}
	for _, ship := range ships {
		if ship.DedicatedFleet() != legacyProbeBuyerFleetTag {
			continue
		}
		if aerr := s.shipRepo.AssignFleet(ctx, ship.ShipSymbol(), "", pid); aerr != nil {
			fmt.Printf("Warning: could not release %s from the retired probe-buyer fleet: %v\n", ship.ShipSymbol(), aerr)
			continue
		}
		fmt.Printf("Released %s from the retired probe-buyer fleet (dedicated_fleet cleared; sensing can now drive it)\n", ship.ShipSymbol())
	}
}
