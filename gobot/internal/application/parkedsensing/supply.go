package parkedsensing

import (
	"context"
	"fmt"
	"time"
)

// takeSupplyFor consumes a spare that could serve target — one parked WITHIN
// GATE REACH of it — and reports whether it found one. A match means a seed for
// that target already exists somewhere in the pipeline, so no second one should
// be ordered.
//
// THE REACH TEST HERE AND THE ONE IN stagingYardFor MUST MOVE TOGETHER. Widen
// staging alone and a want written two hops out is not recognised as supply by this
// test, so the next tick falls through to staging, finds the first yard taken and
// stages at the NEXT one: a second probe for a target already served, every tick
// until the yards run out. They share gateReach so there is no second reach to drift.
//
// The reach test is the whole point. A blanket count of every SPARE row suppresses
// demand it cannot possibly serve: a spare parked five systems from the frontier is
// not a seed for that frontier and nothing will turn it into one (the buy queue's
// spare re-task only scans within a single system). Counted bluntly, one idle hull
// stalls expansion permanently — and so does any stale row, a placement reverted to
// WANTED by a re-task or left QUEUED by a purchase that could not be afforded.
//
// Reading a QUEUED spare as supply is NOT reading it as a purchase in flight. The
// probe cap asks "does a hull exist?", where QUEUED must answer no because it may
// equally be a claim the treasury refused; this asks "is a request already
// outstanding?", and a QUEUED row is exactly that. The buy queue re-drains QUEUED
// placements every tick, so a second row only duplicates an intent already in hand.
//
// BUT A WANT NOTHING CAN FUND IS NOT AN OUTSTANDING REQUEST. A SPARE want written at
// a yard we do not hold is refused by the buy queue on every tick forever and is
// retired by nothing, so counting it as supply would block the correct request for
// the very target it was meant to serve, indefinitely and precisely for the targets
// that most need one. Such a row is skipped here and left in the pool.
//
// THE HULL TEST COMES FIRST, AND IT IS A MONEY GUARD. A SPARE row that already NAMES
// a hull is a seed genuinely on order — bought, flying, or parked — and its waypoint
// is frequently not staffed by anything else, so a fundability test applied to it
// would stop counting a hull already on its way and order a SECOND one for a target
// already served. Naming a hull ends the question before fundability is ever asked
// (RULINGS #4: the doubt resolves toward NOT spending).
func (t *expandTick) takeSupplyFor(ctx context.Context, target string) (bool, error) {
	for i, spare := range t.book.spares {
		within, err := t.reach.canReach(ctx, spare.System, target)
		if err != nil {
			return false, err
		}
		if !within {
			continue
		}
		if spare.AssignedShip == "" {
			fundable, err := t.hullessSpareIsFundable(ctx, spare.Waypoint)
			if err != nil {
				return false, err
			}
			if !fundable {
				continue
			}
		}
		t.book.spares = append(t.book.spares[:i], t.book.spares[i+1:]...)
		return true, nil
	}
	return false, nil
}

// hullessSpareIsFundable reports whether a SPARE want with no hull behind it could
// still have one bought for it — which is what makes it count as supply.
//
// THE THIRD CALLER OF ONE RULE. Fundable is staffedAt AND not known to sell no
// probe, and this is the third consumer of that rule — the definition moves
// underneath this call site whenever staging changes it. Left on the staffed half
// alone it reads a want at a staffed, probe-less yard as a seed already on order,
// skips the target, and so the row that blocks the target is the row that can never
// be filled: a suppression loop. That state is reached legitimately, not by mistake
// — staging deliberately still stages at never-priced yards (that is how the fleet
// learns), the queue scans one, and the memo records a probe-less answer.
func (t *expandTick) hullessSpareIsFundable(ctx context.Context, waypoint string) (bool, error) {
	fundable, err := t.staffedAt(ctx, waypoint)
	if err != nil || !fundable {
		return false, err
	}
	// DELEGATED, never re-derived: the same readProbeStock the buy queue's
	// skipKnownProbeless and staging's own pass read, so there is no fourth notion of
	// buyable and no second copy of the TTL. A NEVER-PRICED yard stays supply — the
	// queue will scan it and may well buy there, and discounting it orders a
	// duplicate on a guess.
	stock, _, err := readProbeStock(ctx, t.p.ListingMemo, t.playerID, waypoint, time.Now())
	if err != nil {
		return false, err
	}
	return stock != probeStockNone, nil
}

// staffedAt reports whether a hull of ours is STANDING at waypoint, answering exactly
// the question buyerAt answers in the buy queue, the same two ways in the same order.
//
// ONE PREDICATE, TWO CALLERS. Seed staging must not write a want the buy queue will
// refuse, and takeSupplyFor must not treat such a want as a seed on order. Both
// reduce to "could the queue buy here?", so there is no second definition to drift.
//
// The ledger half is free — the tick has already read every slot row — and the ships
// half is consulted only when it misses, because a hull can genuinely be standing at
// a counter before this engine has written a row for it. That is the same fallback
// buyerAt makes, and skipping it would decline yards the queue would have funded.
//
// IT STOPS AT DOCKED, deliberately. PurchaseShipCommand will dock a hull it finds in
// orbit, so the purchase itself would tolerate one — but buyerAt is what SELECTS the
// buyer, and it reads DOCKED only. Staging on an orbiting hull would write a want
// the queue still refuses, the exact failure this predicate prevents one layer down.
//
// A READ FAILURE PROPAGATES rather than being read as "not here". Fail-closed
// per-yard would silently stop staging seeds for as long as the ships table was
// unhappy; the tick is idempotent and re-derived from scratch, so failing it loudly
// costs one cycle and nothing else. Memoised for the TICK: several targets share a
// bordering system, and the answer cannot change while the tick runs.
func (t *expandTick) staffedAt(ctx context.Context, waypoint string) (bool, error) {
	if known, cached := t.staffed[waypoint]; cached {
		return known, nil
	}
	staffed := t.book.staffedYard(waypoint)
	if !staffed {
		_, found, err := t.p.Ships.DockedProbeAt(ctx, t.playerID, waypoint)
		if err != nil {
			return false, fmt.Errorf("failed to look for a hull standing at %q: %w", waypoint, err)
		}
		staffed = found
	}
	t.staffed[waypoint] = staffed
	return staffed, nil
}

// claimSpares turns parked spare hulls into charting errands.
//
// A spare is only usable for a target it can actually REACH: a seed walks to its
// target one gate hop per tick, so the spare must be sitting within MaxWalkRings of
// it. A spare parked further out is left where it is for the placement machinery to
// re-task — an errand it could never complete would hold the hull out of the probe
// cap while charting nothing.
//
// THE WRITE ORDER IS A MONEY GUARD. One hull is named by two rows for an instant,
// and choosing which instant decides which way a crash miscounts. The errand is
// stamped FIRST, so a failure between the writes leaves the hull named by both the
// spare row and the mission — an over-count, which only ever buys FEWER probes.
// Releasing the row first would leave the hull named by NEITHER, and the probe cap
// would authorise buying a replacement for a hull we already own (RULINGS #4).
func (t *expandTick) claimSpares(ctx context.Context) error {
	for _, target := range t.targets {
		if t.covered[target.System] {
			continue
		}
		spare, found, err := t.takeReachableSpare(ctx, target.System)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		if err := t.p.Ledger.SetSeed(ctx, t.playerID, target.System, spare.AssignedShip, SeedStateDispatched); err != nil {
			return fmt.Errorf("failed to send spare %s to chart %q: %w", spare.AssignedShip, target.System, err)
		}
		// The hull now belongs to the errand rather than to the ledger, so its
		// placement row goes away. It is invisible to the probe-cap count until the
		// seed parks again — an UNDER-count, and the one place this engine accepts
		// one. It is bounded (a seed exists only while an errand runs, and errands are
		// capped per tick) and self-healing (every terminal branch of a tour ends in a
		// placement row naming the hull again), and the alternative — a stale spare
		// row left behind — has the buy queue re-task a hull that has already left.
		//
		// RELEASED BY KIND, not by waypoint. The spare was very likely staged AT A YARD
		// that is also a parked market — that co-location is the entire point of the
		// wider key — and a waypoint-wide delete would take the MARKET row with it,
		// dropping the probe scanning there out of the cap while it is still on
		// station. This engine's under-count is deliberate and bounded; that one is
		// neither.
		if err := t.p.Ledger.DeleteSlot(ctx, t.playerID, spare.Waypoint, spare.Kind); err != nil {
			return fmt.Errorf(
				"spare %s sent to chart %q but its placement %s was not released (hull now double-counted, probe cap reads high): %w",
				spare.AssignedShip, target.System, spare.Waypoint, err)
		}
		t.book.dropSpare(spare.Waypoint)
		t.covered[target.System] = true
		t.rep.SeedsClaimed++
	}
	return nil
}

// takeReachableSpare removes and returns the parked spare NEAREST to target, among
// those within gate reach of it. Nearest rather than first: both the flying time and
// the risk of the walk being interrupted scale with the hop count. The ledger's own
// order breaks a tie, so the choice stays reproducible tick to tick.
func (t *expandTick) takeReachableSpare(ctx context.Context, target string) (QueuedSlot, bool, error) {
	best, nearest := -1, t.reach.beyondReach()
	for i, spare := range t.book.parkedSpares {
		hops, within, err := t.reach.hops(ctx, spare.System, target)
		if err != nil {
			return QueuedSlot{}, false, err
		}
		if !within || hops >= nearest {
			continue
		}
		best, nearest = i, hops
	}
	if best < 0 {
		return QueuedSlot{}, false, nil
	}
	spare := t.book.parkedSpares[best]
	t.book.parkedSpares = append(t.book.parkedSpares[:best], t.book.parkedSpares[best+1:]...)
	return spare, true, nil
}

// requestSeeds enqueues SPARE placements for the targets no hull covers yet.
//
// This engine never buys. It records a want at a yard the buy queue can realistically
// fund — one in a system we already hold, since the queue only buys where a hull of
// ours is standing at the counter — and the queue applies the treasury floor and the
// probe cap to it exactly as to every other placement. That is the whole point of
// asking rather than spending: expansion gets no second, unguarded path to the
// treasury.
func (t *expandTick) requestSeeds(ctx context.Context) error {
	for _, target := range t.targets {
		if t.rep.Actions >= MaxExpansionActions {
			return nil
		}
		if t.covered[target.System] {
			continue
		}
		supplied, err := t.takeSupplyFor(ctx, target.System)
		if err != nil {
			return err
		}
		if supplied {
			// A seed for this target is already somewhere in the pipeline — what keeps
			// a frontier several ticks away from being re-ordered on every one.
			continue
		}

		yard, system, err := t.stagingYardFor(ctx, target.System)
		if err != nil {
			return err
		}
		if yard == "" {
			// Nowhere to stage a purchase this tick: no system of ours within gate
			// reach holds a probe-selling yard that is both STAFFED by one of our
			// hulls and free of a SPARE placement. Expected while the map is thin, and
			// it costs nothing — the target waits and takes nothing from the targets
			// we CAN reach on its way past.
			continue
		}
		if err := t.p.Ledger.UpsertSlotMetadata(ctx, t.playerID, SlotRecord{
			Waypoint: yard,
			System:   system,
			Kind:     SlotKindSpare,
			State:    SlotStateWanted,
		}); err != nil {
			return fmt.Errorf("failed to request a charting seed for %q: %w", target.System, err)
		}
		t.book.addSpare(system, yard, SlotStateWanted)
		t.rep.SeedsRequested++
		t.rep.Actions++
	}
	return nil
}
