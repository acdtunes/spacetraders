package grpc

import (
	"context"
	"fmt"
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
