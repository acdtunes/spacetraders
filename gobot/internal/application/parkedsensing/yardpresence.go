package parkedsensing

import (
	"context"
	"errors"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/yardscan"
)

// yardpresence.go is the PAID half of shipyard discovery, and yardcatalog.go
// beside it is the free one.
//
// THE SPLIT IS THE API'S, NOT OURS. `GET /shipyard` answers with `shipTypes` —
// what a counter sells — to anyone, and with `ships`, the listings carrying
// purchasePrice, only when a hull of ours is standing at the waypoint. The free
// pass harvests the first for nothing. The second cannot be harvested at all
// without spending something, and what it costs is a HULL: a probe flown to the
// yard, after which the ordinary scan rotation reads the price on its next turn
// and keeps reading it thereafter.
//
// WHY A SEPARATE PASS AND NOT A BRANCH OF THE BUY QUEUE. The obvious home looked
// like DrainBuyQueue, which already fills placements from hulls we own via
// reuseSpareHull and footholdBroker.fill, and whose WANTED candidate list already
// contains almost every yard this pass wants (measured live: 1,554 of the 1,591
// unpriced yards carry a WANTED market slot, because a yard that is also a
// whitelisted market is slotted MARKET by the screen). It is the wrong home, and
// the numbers say why rather than the taste. The drain runs at most
// maxDrainAttempts = 6 placements per tick, ordered by how little coverage a
// system already has, against 8,930 outstanding WANTED market slots. A
// price-critical yard would sit behind thousands of ordinary placements that
// outrank it on a criterion that knows nothing about shipyards, and would be
// reached approximately never. Hanging presence off that queue would have shipped
// a feature that reads correct and never fires.
//
// So the demand comes from the one component that can rank these yards — the
// shipyard-read budget, whose top weight tier IS this set — and the movement
// reuses the drain's own surplusPool rather than inventing a second idea of which
// hull may be taken. This pass is the join, and it is deliberately thin.
//
// RULINGS #2 — it holds NO cross-tick state. The request list is recomputed from
// the budget's live facts every tick and the surplus pool is rebuilt from the
// ledger every tick, so a restart resumes with nothing lost and a yard priced by
// another path simply stops appearing. RULINGS #4 — it BUYS NOTHING. Every move
// it makes is a hull the fleet already owns changing which slot claims it; there
// is no purchaser on its ports and no code path here reaches one.

// MaxYardPresenceDispatches bounds how many hulls ONE tick may send to unpriced
// yards.
//
// A plain const in the manner of MaxYardCatalogReads and maxFootholdRetasks, and
// a BACKSTOP rather than the safety property: correctness comes from the budget's
// allowance (which refuses long before this binds) and from surplusPool
// re-deciding redundancy after every take. Two, matching maxFootholdRetasks,
// because the failure this bounds is the same one — a single tick reading one
// redundancy picture and emptying a system on the strength of it.
const MaxYardPresenceDispatches = 2

// yardPresenceRequestLimit is how many ranked requests the pass asks for.
//
// LARGER THAN THE DISPATCH BOUND, and that gap is the point. Most requests fail
// to find a releasable hull in their own system — measured live, only about 104
// of 1,554 candidate yards have one — so asking for exactly two would usually
// yield zero dispatches while two perfectly fillable yards sat just below the
// cut. Asking for a wider slice and stopping at the first two that can actually
// be filled is what turns the ranking into throughput. It is capped rather than
// unbounded because the list is sorted, and everything past the head is a yard
// the next tick will offer again anyway.
const yardPresenceRequestLimit = 64

// YardPresenceDemand reports which yards need a hull standing on them, best
// first.
//
// A PULL, deliberately, and the direction matters. The shipyard-read budget owns
// this judgement — the set is its top weight tier, recomputed from live facts —
// and asking it per tick means a yard that has just been priced leaves the set on
// its own. A push in the other direction would be a latching bridge: this pass
// would hold the last list it was handed and keep sending hulls at counters that
// no longer need one.
// The READ half is YardDemandReader, embedded rather than respelled, because the
// buy queue's ordering consults the same set (yardqueue.go) and one spelling is
// what stops the mover and the queue drifting into two ideas of which yard is
// dark. What this adds on top is the meter, which only a mover may consume.
type YardPresenceDemand interface {
	YardDemandReader
	// AdmitPresence consumes one reposition from the metered allowance, reporting
	// whether there was one to consume. It is asked ONCE PER DISPATCH and only
	// after a hull has actually been found, so a tick that can move nothing spends
	// no allowance and the meter measures repositions rather than intentions.
	AdmitPresence() bool
}

// YardPresencePorts is everything DispatchYardPresence needs from the outside
// world.
//
// It is cut narrow on purpose, in the manner of the other engine bundles: no
// purchaser, no treasury, no mover. This pass writes two ledger rows and nothing
// else — the flying is the placement machine's job, exactly as it is for a
// foothold — so a port that could spend a credit or steer a hull directly would
// be a port it has no business holding.
type YardPresencePorts struct {
	Demand      YardPresenceDemand
	Ledger      BuyLedger
	Ships       ParkedShipReader
	MannedHulls MannedHullReader
}

// wired reports whether every port is present. The pass is INERT rather than
// fatal when one is missing: it is an extension to sensing, not a precondition
// for it, and a daemon wired without it must tick along unchanged.
func (p YardPresencePorts) wired() bool {
	return p.Demand != nil && p.Ledger != nil && p.Ships != nil && p.MannedHulls != nil
}

// YardPresenceReport is one pass's accounting, for the heartbeat.
type YardPresenceReport struct {
	// Requested is how many yards the budget said need presence — the backlog an
	// operator watches fall.
	Requested int
	// Dispatched counts hulls actually re-tasked to a yard this tick.
	Dispatched int
	// NoHull counts requests that found no releasable hull in their own system.
	// A high number beside a high Requested is the honest signal that the fleet is
	// fully committed, not that the pass is broken.
	NoHull int
	// Metered counts requests that found a hull and were refused by the
	// allowance. It separates "nothing to send" from "not allowed to send yet",
	// which are the two readings an operator would otherwise have to guess between.
	Metered int
}

// DispatchYardPresence sends spare hulls to the yards the fleet cannot price,
// bounded by MaxYardPresenceDispatches and by the budget's own allowance.
//
// WHAT COUNTS AS SPARE IS NOT DECIDED HERE. It is surplusPool's definition, the
// same one the foothold path has used in production: a PARKED MARKET hull that no
// scout post mans and whose every whitelisted good survives its departure. That
// reuse is deliberate rather than convenient — a second, looser idea of "spare"
// living in a discovery pass is exactly how a fleet ends up with its markets
// quietly stripped, and the one place that judgement belongs is the place that
// already got it reviewed.
//
// IN-SYSTEM ONLY. The pool is asked for a hull in the REQUEST'S OWN system and
// nowhere else, unlike the foothold path which walks gates to reach a target. A
// foothold converts a whole system from unbuyable to self-funding and can justify
// a multi-tick crossing; a price read cannot. An in-system hop is minutes of
// flight and a handful of API calls, and it is the tier the fleet can afford to
// run continuously.
//
// A REQUEST WITH NO WANTED SLOT AT ITS WAYPOINT IS SKIPPED, and the pass NEVER
// creates one. Placements are the screen's to write; a discovery pass that could
// invent them would be able to station hulls at waypoints the screen has judged
// not worth watching. In practice this costs almost nothing — a yard that is also
// a whitelisted market already carries a slot, which is 1,554 of the 1,591
// unpriced yards — and what it buys is that this pass can only ever ACCELERATE a
// placement the fleet had already decided it wanted.
func DispatchYardPresence(ctx context.Context, p YardPresencePorts, playerID int) (YardPresenceReport, error) {
	var rep YardPresenceReport
	if !p.wired() {
		return rep, nil
	}

	requests := p.Demand.PresenceRequests(ctx, playerID, yardPresenceRequestLimit)
	rep.Requested = len(requests)
	if len(requests) == 0 {
		return rep, nil
	}

	// The two guard reads, taken ONCE for the tick and FAIL-CLOSED. An unreadable
	// post list read permissively is an empty one — "no hull is manned" — which
	// would hand the scouting fleet's hulls to this pass. Both are returned as
	// errors rather than logged-and-continued because unlike the foothold path
	// (an extension inside a drain that has other work to do) this pass has no
	// other work: a tick that cannot prove a hull releasable has nothing left to
	// do but stop.
	parked, err := p.Ledger.SlotsByState(ctx, playerID, SlotStateParked)
	if err != nil {
		return rep, fmt.Errorf("parked sensing placements unreadable, sending no hull to any yard: %w", err)
	}
	manned, err := p.MannedHulls.MannedHulls(ctx, playerID)
	if err != nil {
		return rep, fmt.Errorf("manned scout posts unreadable, sending no hull to any yard: %w", err)
	}
	pool := newSurplusPool(parked, manned)

	for _, request := range requests {
		if rep.Dispatched >= MaxYardPresenceDispatches {
			break
		}
		sent, err := dispatchOnePresence(ctx, p, playerID, request, pool, &rep)
		if err != nil {
			return rep, err
		}
		if sent {
			rep.Dispatched++
		}
	}
	return rep, nil
}

// dispatchOnePresence re-tasks one releasable hull to one yard, reporting
// whether it moved.
//
// THE ORDER OF THE CHECKS IS THE COST ORDER, cheapest first, and the allowance
// deliberately comes LAST. Asking the meter before a hull has been found would
// spend a token on a yard that turns out to have nobody to send, and since most
// requests are in that state the allowance would be consumed almost entirely by
// requests that move nothing — the meter would throttle the pass without ever
// pacing an actual reposition.
func dispatchOnePresence(
	ctx context.Context,
	p YardPresencePorts,
	playerID int,
	request yardscan.PresenceRequest,
	pool *surplusPool,
	rep *YardPresenceReport,
) (bool, error) {
	target, found, err := presenceTarget(ctx, p, playerID, request)
	if err != nil || !found {
		return false, err
	}

	// The destination's own goods are handed to the pool because this move stays
	// inside one system: the hull is not subtracting coverage, it is relocating
	// it, and a good the target watches is watched again the moment the hull
	// arrives. See coveredAfterMove.
	source, ok := pool.take(request.System, target.WhitelistGoods)
	if !ok {
		rep.NoHull++
		return false, nil
	}
	if source.Waypoint == target.Waypoint {
		// The hull is already standing on the yard. Nothing to move, and moving it
		// to itself would release its own slot and strand it.
		return false, nil
	}

	// The ledger says PARKED; the ships table is asked whether the hull is
	// actually standing still. The two can disagree — a row is written from the
	// last confirmed reading, and another engine may have moved the hull since —
	// and re-tasking one that is mid-flight would have two machines steering it.
	// Unreadable or absent is treated as NOT takeable, exactly as the foothold
	// path treats it. The row is already out of the pool, so a hull in this state
	// is skipped for the tick rather than retried against the next request.
	pos, err := p.Ships.ShipAt(ctx, playerID, source.AssignedShip)
	if err != nil || !pos.Found || pos.NavStatus == navigation.NavStatusInTransit {
		return false, nil
	}

	if !p.Demand.AdmitPresence() {
		rep.Metered++
		return false, nil
	}

	return retaskToYard(ctx, p, playerID, source, target)
}

// presenceTarget finds the WANTED placement standing at a requested yard.
//
// It reads the yard's OWN SYSTEM's slots rather than filtering a global list,
// which is the same read the drain makes per placement and is what keeps this
// pass's cost proportional to the yards it is working on rather than to the
// 8,930-row placement table.
//
// Matching is on WAYPOINT and never on kind. A yard that is also a whitelisted
// market is slotted MARKET by the screen — see SlotKindYard's own note — so a
// kind-filtered lookup would miss the very placements this pass exists to fill.
func presenceTarget(ctx context.Context, p YardPresencePorts, playerID int, request yardscan.PresenceRequest) (QueuedSlot, bool, error) {
	slots, err := p.Ledger.SlotsBySystem(ctx, playerID, request.System)
	if err != nil {
		return QueuedSlot{}, false, fmt.Errorf("sensing slots in %q unreadable: %w", request.System, err)
	}
	for _, slot := range slots {
		if slot.Waypoint != request.Waypoint {
			continue
		}
		// WANTED ONLY. A QUEUED placement was claimed for purchase by the drain and
		// may have money moving against it; anything from BOUGHT on already names a
		// hull. Re-tasking either would race a machine that is mid-decision, and a
		// yard whose placement is already being filled needs nothing from this pass
		// — it is about to get a hull anyway.
		if slot.State != SlotStateWanted {
			return QueuedSlot{}, false, nil
		}
		return slot, true, nil
	}
	return QueuedSlot{}, false, nil
}

// retaskToYard performs the two ledger writes that move a hull from its market
// to the yard.
//
// THE WRITE ORDER IS A MONEY GUARD, and it is the same one reuseSpareHull,
// claimSpares and footholdFromSurplus are built on. One hull is named by two rows
// for an instant, and which instant is chosen decides which way a crash
// miscounts:
//
//   - Claim the target FIRST, as here: a failure between the writes leaves both
//     rows naming the hull, so CountOwnedProbes counts it twice, the cap reads the
//     fleet as LARGER than it is, and FEWER probes are bought. Recoverable, and
//     safe.
//   - Release the source first: a failure leaves the hull named by NO row, the cap
//     reads the fleet as smaller than it is, and a replacement is bought for a
//     probe we already own — the exact direction RULINGS #4 forbids.
//
// The target goes straight to IN_TRANSIT with no BOUGHT state, because there is
// nothing to buy. That writes IN_TRANSIT for a hull that has not been told to move
// and is not standing at the target; the placement machine's dispatchClaim branch
// is what notices and flies it, exactly as it does for a re-tasked spare.
func retaskToYard(ctx context.Context, p YardPresencePorts, playerID int, source, target QueuedSlot) (bool, error) {
	hull := source.AssignedShip

	err := p.Ledger.TransitionSlot(ctx, playerID, target.Waypoint, target.Kind, target.State, SlotStateInTransit,
		SlotFields{AssignedShip: &hull})
	switch {
	case errors.Is(err, ErrSlotClaimed):
		// Another writer took the placement. The source is untouched — which is
		// precisely why the target is claimed first — so the hull is still parked
		// and still counted, and nothing needs undoing.
		return false, nil
	case err != nil:
		return false, fmt.Errorf("failed to re-task hull %s to unpriced yard %s: %w", hull, target.Waypoint, err)
	}

	// The market the hull leaves goes back to being a want with no hull behind it.
	// WANTED rather than deleted: it is still a placement the fleet intends to
	// fill, and leaving it on the books is what gets it re-covered once a hull can
	// be spared for it.
	cleared := ""
	if err := p.Ledger.TransitionSlot(ctx, playerID, source.Waypoint, source.Kind, SlotStateParked, SlotStateWanted,
		SlotFields{AssignedShip: &cleared}); err != nil {
		return true, fmt.Errorf(
			"hull %s re-tasked to unpriced yard %s but its market %s was not released (hull now double-counted, cap reads high): %w",
			hull, target.Waypoint, source.Waypoint, err)
	}

	// Named rather than silent. This is the one pass that takes a hull off a
	// working market for a reason no other engine can see, so an operator
	// reconstructing why a market went quiet must be able to find it.
	logging.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf(
		"Sent %s from market %s to unpriced shipyard %s so its prices can be read", hull, source.Waypoint, target.Waypoint),
		map[string]interface{}{
			"action":        "parked_sensing_yard_presence_dispatch",
			"ship_symbol":   hull,
			"from_waypoint": source.Waypoint,
			"waypoint":      target.Waypoint,
			"system_symbol": target.System,
		})
	return true, nil
}
