// run_trade_route_coordinator_absorption.go — sp-78ai L4: read-only cross-engine
// absorption-ledger consult for scanLanes (design §2, trade-analyst Q1: circuits
// are READ-ONLY — they consult the ledger other engines write, and write nothing
// back). Mirrors idle-arb's L2 consult shape (readAbsorption/reserved) but with
// depth-aware exclusion instead of L2's binary block: a circuit only wants ONE
// lane-sized tranche per visit (not a full-hold worst case), so PLANNED occupancy
// only excludes a lane when it leaves too little unreserved depth to fill that
// tranche at the quoted price — an active recovering shadow, in contrast, blocks
// outright exactly as L2 treats it (Outstanding has already floored it: any
// positive residual is still-live occupied depth).
package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// absorptionConsult is one scan pass's batched ledger read plus its fail-closed
// state — the same per-pass-read/thread-to-every-candidate shape idle-arb's L2
// consult uses (design §2), sized here to one call per scanLanes invocation.
type absorptionConsult struct {
	active     bool // ledger wired AND the trade-route consult not killed
	unreadable bool // the read failed → fail closed
	pools      map[absorption.LaneKey]absorption.KeyOccupancy
}

// readAbsorption performs the once-per-scan consult read. Inert (never excludes)
// when the ledger is unwired or the kill-switch is set; fail-closed (excludes every
// shadow-consult-eligible lane) when the read errors.
func (h *RunTradeRouteCoordinatorHandler) readAbsorption(ctx context.Context, playerID int) absorptionConsult {
	if h.absorptionLedger == nil || h.absorptionConsultDisabled {
		return absorptionConsult{}
	}
	pools, err := h.absorptionLedger.Outstanding(ctx, playerID)
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
			"Trade-route absorption consult: ledger read failed, excluding shadow-consult-eligible lanes this scan (fail-closed): %v", err), nil)
		return absorptionConsult{active: true, unreadable: true}
	}
	return absorptionConsult{active: true, pools: pools}
}

// absorptionVerdict names why the consult would exclude (or clear) one lane's sell side.
type absorptionVerdict string

const (
	absorptionVerdictClear         absorptionVerdict = "clear"
	absorptionVerdictShadow        absorptionVerdict = "shadow"
	absorptionVerdictReservedDepth absorptionVerdict = "reserved-depth"
	absorptionVerdictUnreadable    absorptionVerdict = "unreadable"
)

// evaluate reports the consult's verdict for lane's SELL side (design §2: circuits
// only consult the sell side — the sink they'd dump into). circuitTranche is the
// quantity THIS lane visit would try to move: the smaller of the lane's own
// volume-capped depth and the ship's hold, mirroring trading.HoldFitWeight's own
// "effective" quantity — a circuit fills at most one hold per visit, not the whole
// VolumeCap tours size their tranches to fleet-wide.
//
//   - unreadable: the ledger could not be read this scan → fail closed, every
//     eligible lane excluded (RULINGS #4).
//   - shadow: an EXECUTED recovery residual is still above its floor (Outstanding
//     has already dropped anything recovered past it) — blocks outright, exactly
//     as L2 treats a recovering shadow: the sink is actively healing, no visit
//     should step into it regardless of how little of the tranche is claimed.
//   - reserved-depth: no live shadow, but another engine's PLANNED (in-flight,
//     undecayed) units have already claimed enough of the lane's absorbable depth
//     that what remains can't fill a circuit tranche AT the quoted price — the
//     circuit would only get the leftover at that price before the next chunk
//     prices worse, which the cached quote does not reflect.
//   - clear: neither condition holds; the lane ranks on its own economics.
func (c absorptionConsult) evaluate(lane trading.ArbitrageLane, shipCapacity int) absorptionVerdict {
	if !c.active {
		return absorptionVerdictClear
	}
	if c.unreadable {
		return absorptionVerdictUnreadable
	}
	occ := c.pools[absorption.LaneKey{Waypoint: lane.DestWaypoint, Good: lane.Good, Side: absorption.SideSell}]
	if occ.RecoveringResidual > 0 {
		return absorptionVerdictShadow
	}
	circuitTranche := lane.VolumeCap
	if shipCapacity > 0 && shipCapacity < circuitTranche {
		circuitTranche = shipCapacity
	}
	remaining := float64(lane.VolumeCap) - float64(occ.PlannedUnits)
	if remaining < float64(circuitTranche) {
		return absorptionVerdictReservedDepth
	}
	return absorptionVerdictClear
}

// filterShadowedLanes drops every lane the absorption consult excludes and logs
// one verdict line per excluded lane (mirroring L2's per-candidate verdict
// logging), so `container logs` shows exactly which lanes the consult vetoed and
// why. READ-ONLY (trade-analyst Q1): this never writes to the ledger — it only
// nets each lane's SELL side against the outstanding pools another engine wrote.
func (h *RunTradeRouteCoordinatorHandler) filterShadowedLanes(
	ctx context.Context,
	lanes []trading.ArbitrageLane,
	consult absorptionConsult,
	shipCapacity int,
) []trading.ArbitrageLane {
	if !consult.active {
		return lanes
	}
	logger := common.LoggerFromContext(ctx)
	kept := make([]trading.ArbitrageLane, 0, len(lanes))
	for _, lane := range lanes {
		verdict := consult.evaluate(lane, shipCapacity)
		if verdict == absorptionVerdictClear {
			kept = append(kept, lane)
			continue
		}
		logger.Log("INFO", fmt.Sprintf(
			"Trade-route absorption consult: excluded lane %s %s->%s (verdict %s)",
			lane.Good, lane.SourceWaypoint, lane.DestWaypoint, verdict),
			map[string]interface{}{
				"action":  "absorption_lane_excluded",
				"good":    lane.Good,
				"source":  lane.SourceWaypoint,
				"dest":    lane.DestWaypoint,
				"verdict": string(verdict),
			})
	}
	return kept
}
