package parkedsensing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// reuseSpareHull re-tasks a spare probe already parked in the target's system,
// filling the placement with no purchase at all. It reports whether one was
// found and moved.
//
// THE TWO-ROW ORDERING IS A MONEY GUARD, not a style choice. One hull is named
// by two rows for an instant, and which instant is chosen decides which way a
// crash miscounts:
//
//   - Claim the target FIRST (as here): a failure between the two writes leaves
//     both rows naming the hull, so CountOwnedProbes counts it twice. The cap
//     then reads the fleet as LARGER than it is and buys FEWER probes. Wrong,
//     recoverable, and safe.
//   - Release the spare first: a failure between the writes leaves the hull
//     named by NO row. The cap reads the fleet as smaller than it is and
//     authorises buying a replacement for a probe we already own. Wrong, and it
//     spends money — the exact direction RULINGS #4 forbids.
//
// So the transient over-count is chosen deliberately over the transient
// under-count, and a failed release is surfaced loudly rather than swallowed.
func (t *drainTick) reuseSpareHull(ctx context.Context, target QueuedSlot, inSystem []QueuedSlot) (bool, error) {
	for _, spare := range inSystem {
		if spare.Kind != SlotKindSpare || spare.State != SlotStateParked || spare.AssignedShip == "" {
			continue
		}
		if spare.Waypoint == target.Waypoint {
			continue // the target IS the spare's own slot; nothing to move
		}

		hull := spare.AssignedShip
		// The hull is already ours, so the target goes straight to IN_TRANSIT:
		// there is nothing to buy, and no BOUGHT state to pass through.
		//
		// Note that this writes IN_TRANSIT for a hull that has NOT been told to
		// move, and usually is not even at the target waypoint. The placement
		// machine's dispatchClaim branch is what notices that and flies it. That
		// branch is load-bearing for this path, not an edge case: without it the
		// hull stands where it is forever while the slot reads as in-flight.
		err := t.p.Ledger.TransitionSlot(ctx, t.playerID, SlotTransition{
			Waypoint: target.Waypoint, Kind: target.Kind, From: target.State, To: SlotStateInTransit,
		}, SlotFields{AssignedShip: &hull})
		switch {
		case errors.Is(err, ErrSlotClaimed):
			// Lost the race for this placement. Not ours any more — and the
			// spare is still untouched, which is exactly why the target is
			// claimed first.
			return false, nil
		case err != nil:
			return false, fmt.Errorf("failed to re-task spare hull %s to %s: %w", hull, target.Waypoint, err)
		}

		// The spare's own slot reverts to a want with no hull behind it: the
		// reserve is spent, and the row must stop counting a hull that now
		// belongs to the target.
		cleared := ""
		if err := t.p.Ledger.TransitionSlot(ctx, t.playerID, SlotTransition{
			Waypoint: spare.Waypoint, Kind: spare.Kind, From: SlotStateParked, To: SlotStateWanted,
		}, SlotFields{AssignedShip: &cleared}); err != nil {
			return true, fmt.Errorf(
				"spare hull %s re-tasked to %s but its slot %s was not released (hull now double-counted, cap reads high): %w",
				hull, target.Waypoint, spare.Waypoint, err)
		}
		return true, nil
	}
	return false, nil
}

// purchaseCandidate is one executable place to buy: a yard, and a hull of ours
// standing at it to buy through.
type purchaseCandidate struct {
	yard, buyer string
	// ferried reports that this counter is in a DIFFERENT system from the
	// placement, so the hull will have to be flown across a gate to reach it.
	//
	// Derived from the two symbols rather than from which resolver produced the
	// candidate, so it stays honest on every path — including a retry, where a
	// remote yard recorded by an earlier tick is re-offered through the
	// recorded-yard preference rather than through the cross-system search.
	ferried bool
	// ferryHops is how many gate crossings the bought hull must make to reach the
	// placement, and it is what the buy floor prices the delivery from (sp-e46yc).
	//
	// Set ONLY by the path that actually walked the gate graph, so it is zero on
	// every other path — including the recorded-yard preference, which re-offers a
	// remote yard an earlier tick chose without re-walking it. Reading that zero
	// as "free to deliver" is precisely the defect this field exists to close, so
	// nothing reads it directly: domainSensing.FerryHops turns it into a
	// chargeable count, and prices an unknown cross-system hop as one rather than
	// as none.
	ferryHops int
}

// newPurchaseCandidate pairs a counter with its buyer, deciding from the symbols
// alone whether reaching it means crossing a gate.
func newPurchaseCandidate(system, yard, buyer string) purchaseCandidate {
	return purchaseCandidate{
		yard:    yard,
		buyer:   buyer,
		ferried: shared.ExtractSystemSymbol(yard) != system,
	}
}

// resolvePurchaseCandidates lists every executable place to buy for a placement,
// best first.
//
// A purchase needs a hull ALREADY STANDING at the yard — the purchase machinery
// navigates and docks the buyer itself, so a buyer that cannot reach the counter
// is not a buyer. Candidates, in order:
//
//  1. the yard recorded on the slot, if a previous tick already chose one;
//  2. then each probe-selling yard in the placement's OWN system, cheapest
//     first.
//
// The recorded yard is a PREFERENCE, not a commitment — the rest of the list is
// still tried. Treating it as binding would let a claimed placement stall
// permanently: the hull that made its yard executable can be flown off by any
// other coordinator, and the placement would then be skipped every tick forever
// while a perfectly good yard sat unused next door.
//
// Presence at a yard is matched WAYPOINT-wise and never kind-wise. When a
// probe-selling yard is also a whitelisted market the screen slots it as
// MARKET, so the probe standing on that yard sits under a MARKET-kind row;
// filtering for kind == YARD would miss it and buy a second hull for a waypoint
// that already has one.
//
// LOCAL FIRST, AND ONLY THEN ACROSS A GATE. If the placement's own system can
// fund it, that is the answer and nothing else is read — no topology, no remote
// yard list, no cross-system purchase. That short-circuit is what keeps the
// ordinary fill exactly as cheap and exactly as fast as it was before the ferry
// existed. Only a placement its own system genuinely cannot fund falls through to
// ferryBroker.candidates, which is where the reasoning about crossing a gate lives.
func (t *drainTick) resolvePurchaseCandidates(ctx context.Context, slot QueuedSlot, inSystem []QueuedSlot, now time.Time) ([]purchaseCandidate, error) {
	local, err := t.candidatesInSystem(ctx, slot, inSystem, now)
	if err != nil || len(local) > 0 {
		return local, err
	}
	return t.ferry.candidates(ctx, t.p, t.playerID, slot)
}

// candidatesInSystem lists the executable counters inside the placement's OWN
// system — the whole of the buy path before cross-system buying existed, and
// still the only path taken whenever it can answer.
func (t *drainTick) candidatesInSystem(ctx context.Context, slot QueuedSlot, inSystem []QueuedSlot, now time.Time) ([]purchaseCandidate, error) {
	listed, err := t.p.Yards.ListProbeYards(ctx, slot.System)
	if err != nil {
		return nil, fmt.Errorf("failed to list probe yards in %q: %w", slot.System, err)
	}

	yards := make([]string, 0, len(listed)+1)
	if slot.PurchaseYard != "" {
		yards = append(yards, slot.PurchaseYard)
	}
	for _, y := range listed {
		if y != slot.PurchaseYard {
			yards = append(yards, y)
		}
	}

	candidates := make([]purchaseCandidate, 0, len(yards))
	for _, yard := range yards {
		// Asked BEFORE buyerAt so a dead yard costs neither the ships read nor the
		// live quote behind it. This is a LOCAL read; it never touches the API.
		if t.skipKnownProbeless(ctx, yard, now) {
			continue
		}
		buyer, found, err := buyerAt(ctx, t.p, t.playerID, yard, inSystem)
		if err != nil {
			return nil, err
		}
		if found {
			candidates = append(candidates, newPurchaseCandidate(slot.System, yard, buyer))
		}
	}
	return candidates, nil
}

// skipKnownProbeless reports whether a yard may be passed over WITHOUT a live
// quote, because a stored listing read already says it sells no probe.
//
// THE THREE ANSWERS, and only the middle one skips:
//
//   - never read (known=false) → ASK. This is how a yard enters the memo at all;
//     treating an absent reading as a negative would freeze the fleet's knowledge
//     and permanently write off every yard nothing had happened to read yet.
//   - read recently, sells no probe → SKIP. The standing fact this change exists
//     to stop paying for.
//   - read recently, sells a probe → ASK. The memo removes candidates; it never
//     waves one through, so a yard it likes is quoted and floor-checked exactly as
//     before. NOTHING here can approve a purchase.
//
// A STALE reading is treated as never-read, so a restocked yard is re-checked
// every probeListingMemoTTL rather than written off for the era.
//
// FAILS OPEN, which inverts this queue's usual direction and is deliberate. The
// memo is an API-budget optimisation, not a money guard: the worst an open failure
// costs is the single call the drain already makes today, whereas failing closed
// would let one unhealthy local read starve probe buying across the whole fleet.
// RULINGS #4 is untouched either way — every money guard sits downstream of this
// and judges the purchase unchanged.
//
// The skip is RECORDED, through the same per-tick memo that aggregates refusals,
// so a yard that stops being queried does not also stop being reported.
func (t *drainTick) skipKnownProbeless(ctx context.Context, yard string, now time.Time) bool {
	stock, scannedAt, err := readProbeStock(ctx, t.p.ListingMemo, t.playerID, yard, now)
	if err != nil || stock != probeStockNone {
		// FAILS OPEN on the read error, unchanged: the memo is an API-budget
		// optimisation, not a money guard, and every money guard sits downstream.
		return false
	}
	t.memo.record(BuyStepMemo, yard, "", fmt.Sprintf(
		"stored listings show no probe (read %s ago; re-checked after %s)",
		now.Sub(scannedAt).Truncate(time.Second), probeListingMemoTTL))
	return true
}

// buyerAt finds a hull of ours standing at one waypoint: first a parked sensing
// probe the ledger already accounts for, then any probe of ours the ships table
// shows docked there.
func buyerAt(ctx context.Context, p BuyPorts, playerID int, waypoint string, inSystem []QueuedSlot) (string, bool, error) {
	for _, s := range inSystem {
		if s.Waypoint == waypoint && s.State == SlotStateParked && s.AssignedShip != "" {
			return s.AssignedShip, true, nil
		}
	}
	ship, found, err := p.Ships.DockedProbeAt(ctx, playerID, waypoint)
	if err != nil {
		return "", false, fmt.Errorf("failed to look for a docked probe at %q: %w", waypoint, err)
	}
	return ship, found, nil
}

// claimForPurchase moves a placement from WANTED to QUEUED before any money
// moves, so a second writer cannot buy a second probe for the same placement. It
// reports whether this tick owns the placement.
//
// A placement already in QUEUED was claimed by an earlier tick whose purchase
// failed; re-claiming it would be a wasted write.
func (t *drainTick) claimForPurchase(ctx context.Context, slot QueuedSlot, yard string) (bool, error) {
	if slot.State != SlotStateWanted {
		return true, nil
	}
	err := t.p.Ledger.TransitionSlot(ctx, t.playerID, SlotTransition{
		Waypoint: slot.Waypoint, Kind: slot.Kind, From: SlotStateWanted, To: SlotStateQueued,
	}, SlotFields{PurchaseYard: &yard})
	switch {
	case errors.Is(err, ErrSlotClaimed):
		// Another writer owns this placement now. Routine, and nothing was
		// spent — move on to the next one.
		return false, nil
	case err != nil:
		// Not contention: the ledger itself is refusing writes. A claim we
		// cannot record is a claim that cannot protect a purchase.
		return false, fmt.Errorf("failed to claim sensing slot %s for purchase: %w", slot.Waypoint, err)
	}
	t.rep.Queued++
	return true, nil
}

// recordPurchase writes the bought hull against its placement and tags it.
//
// The two writes are ordered by what a failure between them costs. The hull is
// recorded FIRST so the probe cap counts something we have paid for even if the
// tag write then fails; the reverse order would leave a paid-for hull uncounted
// and authorise buying it again. The tag is therefore best-effort here, and the
// placement machine re-asserts it (idempotently) on the next edge.
//
// A failed RECORD after a successful purchase is the one unrecoverable shape —
// money spent, hull unaccounted — so it surfaces as an error and stops the
// drain rather than spending further against a ledger that is not accepting
// writes.
//
// The yard is recorded alongside the hull because a retry may have fallen back
// to a different one than the claim chose. Leaving the original would leave the
// row asserting a provenance the purchase did not have.
func (t *drainTick) recordPurchase(ctx context.Context, slot QueuedSlot, yard string, probe BoughtProbe) error {
	if err := t.p.Ledger.TransitionSlot(ctx, t.playerID, SlotTransition{
		Waypoint: slot.Waypoint, Kind: slot.Kind, From: SlotStateQueued, To: SlotStateBought,
	}, SlotFields{AssignedShip: &probe.ShipSymbol, PurchaseYard: &yard}); err != nil {
		return fmt.Errorf(
			"bought probe %s for slot %s but could not record it (hull unaccounted, drain halted): %w",
			probe.ShipSymbol, slot.Waypoint, err)
	}
	if err := t.p.Fleet.AssignFleet(ctx, t.playerID, probe.ShipSymbol, SensingParkedFleetTag); err != nil {
		// Best-effort by design (see above), but NAMED: an untagged hull looks
		// like an idle undedicated probe to every other coordinator's ownership
		// sweep and can be poached. The placement machine re-asserts the tag on
		// the next edge, so this is a one-tick exposure — but a silent one would
		// leave a poached hull with no trace of how it got away.
		logging.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
			"Bought probe %s is recorded against slot %s but was not tagged into the sensing fleet (poachable until the placement machine re-tags it): %v",
			probe.ShipSymbol, slot.Waypoint, err), map[string]interface{}{
			"action":      "parked_sensing_purchase_tag_failed",
			"ship_symbol": probe.ShipSymbol,
			"waypoint":    slot.Waypoint,
		})
	}
	return nil
}
