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
// THE TWO-ROW ORDERING IS A MONEY GUARD, not a style choice. One hull is named by
// two rows for an instant, and which instant is chosen decides which way a crash
// miscounts. Claiming the target FIRST (as here) leaves both rows naming the hull on
// a failure between the writes, so CountOwnedProbes counts it twice, the cap reads
// the fleet as LARGER than it is, and FEWER probes are bought — wrong, recoverable,
// safe. Releasing the spare first would leave the hull named by NO row, the cap
// reading the fleet as smaller and authorising a replacement for a probe we already
// own: the direction RULINGS #4 forbids. A failed release is surfaced loudly.
func (t *drainTick) reuseSpareHull(ctx context.Context, target QueuedSlot, inSystem []QueuedSlot) (bool, error) {
	for _, spare := range inSystem {
		if spare.Kind != SlotKindSpare || spare.State != SlotStateParked || spare.AssignedShip == "" {
			continue
		}
		if spare.Waypoint == target.Waypoint {
			continue // the target IS the spare's own slot; nothing to move
		}

		hull := spare.AssignedShip
		// The hull is already ours, so the target goes straight to IN_TRANSIT: there
		// is nothing to buy and no BOUGHT state to pass through. This writes
		// IN_TRANSIT for a hull that has NOT been told to move and usually is not
		// even at the target waypoint; the placement machine's dispatchClaim branch
		// notices and flies it. That branch is load-bearing here, not an edge case —
		// without it the hull stands still forever while the slot reads in-flight.
		err := t.p.Ledger.TransitionSlot(ctx, t.playerID, SlotTransition{
			Waypoint: target.Waypoint, Kind: target.Kind, From: target.State, To: SlotStateInTransit,
		}, SlotFields{AssignedShip: &hull})
		switch {
		case errors.Is(err, ErrSlotClaimed):
			// Lost the race for this placement, and the spare is still untouched —
			// which is exactly why the target is claimed first.
			return false, nil
		case err != nil:
			return false, fmt.Errorf("failed to re-task spare hull %s to %s: %w", hull, target.Waypoint, err)
		}

		// The spare's own slot reverts to a want with no hull behind it: the row must
		// stop counting a hull that now belongs to the target.
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
	// ferried reports that this counter is in a DIFFERENT system from the placement,
	// so the hull must be flown across a gate to reach it. Derived from the two
	// symbols rather than from which resolver produced the candidate, so it stays
	// honest on every path — including the recorded-yard preference, which re-offers
	// a remote yard an earlier tick chose without going through the gate search.
	ferried bool
	// ferryHops is how many gate crossings the bought hull must make to reach the
	// placement, and what the buy floor prices the delivery from. Set ONLY by the
	// path that actually walked the gate graph, so it is zero on every other path,
	// the recorded-yard preference included. Nothing reads it directly:
	// domainSensing.FerryHops turns it into a chargeable count and prices an unknown
	// cross-system hop as one rather than none, so a zero is never "free to deliver".
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
// best first: the yard recorded on the slot if a previous tick chose one, then each
// probe-selling yard in the placement's OWN system, cheapest first. A purchase needs
// a hull ALREADY STANDING at the yard — the purchase machinery navigates and docks
// the buyer itself, so a buyer that cannot reach the counter is not a buyer.
//
// The recorded yard is a PREFERENCE, not a commitment — the rest of the list is
// still tried. Treating it as binding lets a claimed placement stall permanently:
// the hull that made its yard executable can be flown off by any other coordinator,
// and the placement would then be skipped every tick while a good yard sat unused.
//
// Presence at a yard is matched WAYPOINT-wise and never kind-wise. A probe-selling
// yard that is also a whitelisted market is slotted MARKET by the screen, so the
// probe standing there sits under a MARKET-kind row; filtering for kind == YARD
// would miss it and buy a second hull for a waypoint that already has one.
//
// LOCAL FIRST, AND ONLY THEN ACROSS A GATE. If the placement's own system can fund
// it, that is the answer and nothing else is read — no topology, no remote yard
// list, no cross-system purchase — which keeps the ordinary fill cheap. Only a
// placement its own system cannot fund falls through to ferryBroker.candidates.
func (t *drainTick) resolvePurchaseCandidates(ctx context.Context, slot QueuedSlot, inSystem []QueuedSlot, now time.Time) ([]purchaseCandidate, error) {
	local, err := t.candidatesInSystem(ctx, slot, inSystem, now)
	if err != nil || len(local) > 0 {
		return local, err
	}
	return t.ferry.candidates(ctx, t.p, t.playerID, slot)
}

// candidatesInSystem lists the executable counters inside the placement's OWN
// system — the only path taken whenever it can answer.
func (t *drainTick) candidatesInSystem(ctx context.Context, slot QueuedSlot, inSystem []QueuedSlot, now time.Time) ([]purchaseCandidate, error) {
	listed, err := t.p.Yards.ListProbeYards(ctx, slot.System)
	if err != nil {
		return nil, fmt.Errorf("failed to list probe yards in %q: %w", slot.System, err)
	}

	yards := make([]string, 0, len(listed)+1)
	if recordedLocalYard(slot) != "" {
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

// recordedLocalYard returns slot.PurchaseYard when it names a yard inside
// slot.System, and "" for a remote one that ferry.candidates must re-verify.
func recordedLocalYard(slot QueuedSlot) string {
	if slot.PurchaseYard == "" {
		return ""
	}
	if shared.ExtractSystemSymbol(slot.PurchaseYard) != slot.System {
		return ""
	}
	return slot.PurchaseYard
}

// skipKnownProbeless reports whether a yard may be passed over WITHOUT a live
// quote, because a stored listing read already says it sells no probe.
//
// ONLY "read recently, sells no probe" skips. A yard NEVER READ is asked — treating
// an absent reading as a negative would freeze the fleet's knowledge and permanently
// write off every yard nothing had happened to read yet — and a STALE reading counts
// as never-read, so a restocked yard is re-checked every probeListingMemoTTL. A yard
// the memo likes is asked too: it only removes candidates, never waves one through,
// so NOTHING here can approve a purchase.
//
// FAILS OPEN, which inverts this queue's usual direction and is deliberate. The memo
// is an API-budget optimisation, not a money guard: an open failure costs the single
// call the drain would make anyway, whereas failing closed would let one unhealthy
// local read starve probe buying across the whole fleet. RULINGS #4 is untouched
// either way — every money guard sits downstream and judges the purchase unchanged.
//
// The skip is RECORDED, through the same per-tick memo that aggregates refusals, so
// a yard that stops being queried does not also stop being reported.
func (t *drainTick) skipKnownProbeless(ctx context.Context, yard string, now time.Time) bool {
	stock, scannedAt, err := readProbeStock(ctx, t.p.ListingMemo, t.playerID, yard, now)
	if err != nil || stock != probeStockNone {
		// FAILS OPEN on a read error: the memo is an API-budget optimisation, not a
		// money guard, and every money guard sits downstream.
		return false
	}
	t.memo.record(BuyStepMemo, yard, "", fmt.Sprintf(
		"stored listings show no probe (read %s ago; re-checked after %s)",
		now.Sub(scannedAt).Truncate(time.Second), probeListingMemoTTL))
	return true
}

// buyerAt finds a hull of ours standing at one waypoint: first a parked sensing
// probe the ledger already accounts for, then any probe of ours the ships table
// shows docked there, and last ANY hull of ours docked there that this engine could
// claim.
//
// THE THIRD READ IS THE COLD-START ESCAPE (counterstaff.go). SpaceTraders sells a
// hull wherever a hull of ours is docked and does not care which, so a command
// frigate or a hauler standing at the counter can sign for a probe the fleet has no
// probe to buy. It is asked LAST, so a probe is always preferred and the borrowed
// hull is engaged only when nothing else can transact here.
//
// NOTHING IS OWED TO THE HULL EITHER WAY. A buyer is used for the length of one
// purchase and named by no row afterwards — the same relationship the DockedProbeAt
// fallback has always had with a probe no placement accounts for. The hull that
// FILLS the placement is the probe that gets bought.
//
// A ROLE IS NEVER INFERRED FROM THIS ANSWER. It reports who can transact, not what
// the fleet owns: the probe cap counts ledger rows, and no row is written here.
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
	if found {
		return ship, true, nil
	}
	ship, found, err = p.Ships.DockedBuyerAt(ctx, playerID, waypoint)
	if err != nil {
		return "", false, fmt.Errorf("failed to look for a hull docked at %q: %w", waypoint, err)
	}
	return ship, found, nil
}

// claimForPurchase moves a placement from WANTED to QUEUED before any money moves,
// so a second writer cannot buy a second probe for the same placement. It reports
// whether this tick owns the placement. A placement already in QUEUED was claimed by
// an earlier tick whose purchase failed; re-claiming it would be a wasted write.
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
// recorded FIRST so the probe cap counts something we have paid for even if the tag
// write then fails; the reverse order would leave a paid-for hull uncounted and
// authorise buying it again. The tag is therefore best-effort here, and the
// placement machine re-asserts it (idempotently) on the next edge. A failed RECORD
// after a successful purchase is the one unrecoverable shape — money spent, hull
// unaccounted — so it surfaces as an error and stops the drain rather than spending
// further against a ledger that is not accepting writes.
//
// The yard is recorded alongside the hull because a retry may have fallen back to a
// different one than the claim chose, and leaving the original would have the row
// assert a provenance the purchase did not have.
func (t *drainTick) recordPurchase(ctx context.Context, slot QueuedSlot, yard string, probe BoughtProbe) error {
	if err := t.p.Ledger.TransitionSlot(ctx, t.playerID, SlotTransition{
		Waypoint: slot.Waypoint, Kind: slot.Kind, From: SlotStateQueued, To: SlotStateBought,
	}, SlotFields{AssignedShip: &probe.ShipSymbol, PurchaseYard: &yard}); err != nil {
		return fmt.Errorf(
			"bought probe %s for slot %s but could not record it (hull unaccounted, drain halted): %w",
			probe.ShipSymbol, slot.Waypoint, err)
	}
	if err := t.p.Fleet.AssignFleet(ctx, t.playerID, probe.ShipSymbol, SensingParkedFleetTag); err != nil {
		// Best-effort by design (see above), but NAMED: an untagged hull looks like an
		// idle undedicated probe to every other coordinator's ownership sweep and can
		// be poached. The placement machine re-asserts the tag on the next edge, so
		// the exposure is one tick — but a silent one leaves no trace of the escape.
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
