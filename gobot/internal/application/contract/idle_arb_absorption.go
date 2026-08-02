package contract

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// The engine tag attributes a row's origin for telemetry and dead-container
// reclaim; the TTL knobs bound a PLANNED hold whose container dies without
// releasing (dead-container reclaim is the primary cleanup, these are the
// backstop).
const (
	absorptionEngineIdleArb = "idle-arb"
	// defaultAbsorptionPlannedTTLSlack pads a leg's projected round-trip so a healthy
	// in-flight hold never expires early; minAbsorptionPlannedTTL floors it for very
	// short legs. Both are backstops to the dead-container reclaim (RULINGS #5: the
	// slack is a wired config, these are its defaults).
	defaultAbsorptionPlannedTTLSlack = 15 * time.Minute
	minAbsorptionPlannedTTL          = 30 * time.Minute
)

// absorptionConsult is one pass's batched read of the ledger plus its fail-closed
// state. Built once per DispatchOnce (one outstanding query per pass) and
// threaded to every candidate — the within-pass collision is the lane mutex's job,
// the cross-pass/cross-engine collision is this.
type absorptionConsult struct {
	active     bool // ledger wired AND consult not killed
	unreadable bool // the read failed → fail closed
	pools      map[absorption.LaneKey]absorption.KeyOccupancy
}

// reserved reports whether a (good, sink) sell is blocked by the ledger, DEPTH-AWARE:
//   - unreadable ledger → blocks EVERY candidate (fail-closed: never dispatch blind
//     into depth another engine may have reserved or just crushed, RULINGS #4);
//   - a recovering EXECUTED shadow (RecoveringResidual > 0, Outstanding already drops
//     sub-floor shadows) → blocks OUTRIGHT: the sink is actively healing and no leg
//     should step into it regardless of nominal headroom;
//   - in-flight PLANNED units → block ONLY when the remaining unreserved depth can't
//     fit THIS leg's tranche at the quoted price. The tranche is the smaller of the
//     sink's absorptive depth (sinkDepthCap = the sink good's trade volume) and the
//     leg's own lot (legUnits — a leg dumps at most one hold).
//
// An unknown/absent depth (sinkDepthCap <= 0) falls back to the conservative binary
// block on any PLANNED occupancy — never relax depth we can't measure.
func (c absorptionConsult) reserved(good, sink string, sinkDepthCap, legUnits int) bool {
	if !c.active {
		return false
	}
	if c.unreadable {
		return true
	}
	occ := c.pools[absorption.LaneKey{Waypoint: sink, Good: good, Side: absorption.SideSell}]
	if occ.RecoveringResidual > 0 {
		return true
	}
	if occ.PlannedUnits <= 0 {
		return false
	}
	if sinkDepthCap <= 0 {
		return true // unknown depth + real planned occupancy → conservative binary block
	}
	tranche := sinkDepthCap
	if legUnits > 0 && legUnits < tranche {
		tranche = legUnits
	}
	remaining := sinkDepthCap - occ.PlannedUnits
	return remaining < tranche
}

// readAbsorption performs the once-per-pass consult read. Inert (never blocks) when
// the ledger is unwired or the consult is killed; fail-closed (blocks all) when the
// read errors.
func (d *IdleArbDispatcher) readAbsorption(ctx context.Context) absorptionConsult {
	if d.ledger == nil {
		return absorptionConsult{}
	}
	pools, err := d.ledger.Outstanding(ctx, d.playerID.Value())
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
			"Idle-arb absorption consult: ledger read failed, declining all candidates this pass (fail-closed): %v", err), nil)
		return absorptionConsult{active: true, unreadable: true}
	}
	return absorptionConsult{active: true, pools: pools}
}

// readReserveFloorGate performs the once-per-pass live-treasury read for the SHARED
// common.ReserveFloorGate (sp-zq635 §4a): built once per DispatchOnce, it reports when the
// pass's cumulative committed leg-spend plus one more leg would drop treasury below the flat
// ImmutableReserveFloor. The dispatcher's per-leg cap and the arb run's own 50k floor bound
// EACH leg; the gate bounds their SUM — the concurrent breach no per-leg floor can see.
// Inert (never holds) when no reader is wired — the optional-port contract the existing
// dispatcher tests rely on. Fail-closed (holds every leg) when the read errors: a guard
// whose job is keeping treasury above the reserve must never dispatch spend blind. The gate
// MECHANISM is the one shared with the long-haul money envelope (sp-mepj §3); only the Floor
// differs (idle-arb: ImmutableReserveFloor; long-haul: the 200k ContractScalerCushion fence).
func (d *IdleArbDispatcher) readReserveFloorGate(ctx context.Context) common.ReserveFloorGate {
	if d.treasury == nil {
		return common.ReserveFloorGate{}
	}
	balance, err := d.treasury.LiveTreasury(ctx)
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
			"Idle-arb working-capital reserve gate: live treasury read failed, holding all legs this pass (fail-closed): %v", err), nil)
		return common.ReserveFloorGate{Active: true, Unreadable: true}
	}
	return common.ReserveFloorGate{Active: true, Treasury: balance, Floor: common.ImmutableReserveFloor}
}

// logReserveFloorHold emits the reserve-gate hold in MESSAGE TEXT (the CLI renderer drops the
// metadata map), carrying the numbers that refused the leg. Skipped for the fail-closed hold,
// which readReserveFloorGate already logged with its read error.
func logReserveFloorHold(ctx context.Context, g common.ReserveFloorGate, legSpend int, committed int64) {
	if g.Unreadable {
		return
	}
	common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
		"Idle-arb held: the next %d-credit leg would breach the working-capital reserve — treasury %d, committed this pass %d, reserve %d (holding the rest of the pass)",
		legSpend, g.Treasury, committed, g.Floor), nil)
}

// recordAbsorption publishes a just-launched leg's sell-side occupancy to the ledger
// so tours and other dispatchers consult it. Called at the SAME seam the
// lane mutex is marked (noteLaunch) — the leg has committed, so this is a fail-open
// RECORD, not a gate: a write failure loses cross-engine visibility (the armed mutex
// and the arb run's sell floor still guard the leg) but never strands the launched
// leg. units is the full hull hold (worst case); the arb container's convert-at-sale
// corrects it to realized units.
func (d *IdleArbDispatcher) recordAbsorption(ctx context.Context, hull *navigation.Ship, lane *IdleArbLane, containerID string) {
	if d.ledger == nil {
		return
	}
	legSeconds := shared.FlightModeCruise.TravelTime(lane.Distance, hull.EngineSpeed())
	ttl := 2*time.Duration(legSeconds)*time.Second + d.plannedTTLSlack
	if ttl < minAbsorptionPlannedTTL {
		ttl = minAbsorptionPlannedTTL
	}
	entry := absorption.ReserveEntry{
		Waypoint:    lane.SellAt,
		Good:        lane.Good,
		Side:        absorption.SideSell,
		Units:       hull.AvailableCargoSpace(),
		QuotedPrice: lane.DestBid,
		TTL:         ttl,
	}
	if _, err := d.ledger.RecordPlanned(ctx, d.playerID.Value(), containerID, absorptionEngineIdleArb, entry); err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
			"Idle-arb absorption record: could not record leg %s on %s/%s (leg flies; mutex + sell floor still guard it): %v",
			containerID, lane.SellAt, lane.Good, err), nil)
	}
}
