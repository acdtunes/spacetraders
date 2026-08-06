package parkedsensing

import (
	"context"
	"errors"
	"testing"
	"time"
)

// counterstaff_test.go pins the cold-start escape: a fleet holding NO spare probe
// can still get its first charting seed bought.
//
// THE DEADLOCK IT WITNESSES, measured in prod era 6 — 3 probes owned, 2 slots
// PARKED, 5,549 slots WANTED, ZERO seeds requested:
//
//   - requestSeeds writes a SPARE want only where staffedAt already answers, and
//     staffedAt could only ever see a PROBE. With no probe standing on a probe
//     counter in reach, no want is written at all.
//   - footholdBroker.fill is only ever offered SPARE/WANTED placements, and
//     requestSeeds is the ONLY writer of those. So the foothold path — the thing
//     that exists to break exactly this deadlock — is handed nothing to break.
//   - surplusPool draws PARKED MARKET probes and coveredAfterMove refuses any
//     candidate whose goods lose their last observer, so a fleet with one probe per
//     system releases nothing either.
//
// Every route assumes spare PROBES already exist. The escape is a NON-PROBE hull
// standing at the counter as a BUYER — SpaceTraders sells a hull wherever one of
// ours is docked and does not care which.

// coldFleet is the fixture the deadlock needs, and it is built so the old rule and
// the new one DISAGREE: there is not one parked probe anywhere, and the only
// shipyard in the world is an evidenced probe counter in the system our command
// frigate is already sitting in.
//
//	X1-HOME (probe counter, no hull of ours on it) ── X1-DARK (uncharted target)
//	   ^ TORWIND-1 parked at X1-HOME-A1, idle and undedicated
func coldFleet() (*expandHarness, *fakeListingMemo) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-HOME", Verdict: VerdictInScope},
		{System: "X1-DARK", Verdict: VerdictPending, UnchartedCount: 6},
	}
	h.gates.adjacency = map[string][]string{
		"X1-HOME": {"X1-DARK"},
		"X1-DARK": {},
	}
	h.yards.bySystem = map[string][]string{"X1-HOME": {"X1-HOME-YARD"}}
	// NO placement rows at all: zero parked probes, which is the whole point.
	h.ledger.slots = nil
	h.ships.lendable = []LendableHull{
		{ShipSymbol: "TORWIND-1", Waypoint: "X1-HOME-A1", System: "X1-HOME"},
	}
	memo := &fakeListingMemo{
		sells:     map[string]bool{"X1-HOME-YARD": true},
		scannedAt: map[string]time.Time{"X1-HOME-YARD": time.Now()},
	}
	return h, memo
}

// arrive models the borrowed hull having landed and docked: the ships table now
// shows a NON-PROBE hull of ours standing at the counter, which is what
// DockedBuyerAt reports and what staffedAt has to be able to see.
func (h *expandHarness) arrive(hull, yard string) {
	if h.ships.lent == nil {
		h.ships.lent = map[string]string{}
	}
	h.ships.lent[yard] = hull
	for i := range h.ships.lendable {
		if h.ships.lendable[i].ShipSymbol == hull {
			h.ships.lendable[i].Waypoint = yard
		}
	}
}

// --- the regression that matters ---------------------------------------------

// THE HEADLINE. A cold fleet reports the deadlock and lends a hull to the counter.
// Before the fix this tick did nothing at all and reported a serene zero.
func TestAdvanceExpansion_ColdFleetLendsAHullToTheProbeCounter(t *testing.T) {
	h, memo := coldFleet()

	rep, err := h.runWithMemo(t, memo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rep.SeedsRequested != 0 {
		t.Fatalf("SeedsRequested = %d, want 0 — nothing of ours is standing at the counter yet, and a want "+
			"written there could not be funded", rep.SeedsRequested)
	}
	if rep.SeedsUnstaged != 1 {
		t.Fatalf("SeedsUnstaged = %d, want 1 — X1-DARK was passed over for want of a staffed counter, and a "+
			"tick that reports zero here is the silent deadlock this counter exists to name", rep.SeedsUnstaged)
	}
	if rep.CountersStaffed != 1 {
		t.Fatalf("CountersStaffed = %d, want 1 — with no probe anywhere the only way to open the counter is "+
			"to lend it a hull that is not a probe", rep.CountersStaffed)
	}

	navs := h.seed.calls
	if len(navs) != 1 || navs[0].verb != "navigate" || navs[0].ship != "TORWIND-1" || navs[0].arg != "X1-HOME-YARD" {
		t.Fatalf("commands issued = %v, want exactly one navigate of TORWIND-1 to X1-HOME-YARD", navs)
	}
}

// THE OTHER HALF OF THE REGRESSION, and the one the acceptance criterion names: once
// the borrowed hull is standing at the counter, the seed request the fleet could
// never raise is raised.
func TestAdvanceExpansion_SeedIsRequestedOnceTheLentHullStandsAtTheCounter(t *testing.T) {
	h, memo := coldFleet()

	if _, err := h.runWithMemo(t, memo); err != nil {
		t.Fatalf("unexpected error on the dispatching tick: %v", err)
	}
	h.arrive("TORWIND-1", "X1-HOME-YARD")

	rep, err := h.runWithMemo(t, memo)
	if err != nil {
		t.Fatalf("unexpected error on the staging tick: %v", err)
	}

	if rep.SeedsRequested != 1 {
		t.Fatalf("SeedsRequested = %d, want 1 — TORWIND-1 is docked at X1-HOME-YARD, so the buy queue can "+
			"transact there and the want is fundable", rep.SeedsRequested)
	}
	if rep.SeedsUnstaged != 0 {
		t.Fatalf("SeedsUnstaged = %d, want 0 — the target was staged, not passed over", rep.SeedsUnstaged)
	}
	if rep.CountersStaffed != 0 {
		t.Fatalf("CountersStaffed = %d, want 0 — the counter is open, so nothing more is borrowed", rep.CountersStaffed)
	}
	if len(h.ledger.upsertedSlots) != 1 {
		t.Fatalf("wrote %d placements, want exactly 1: %v", len(h.ledger.upsertedSlots), h.ledger.upsertedSlots)
	}
	got := h.ledger.upsertedSlots[0]
	if got.Waypoint != "X1-HOME-YARD" || got.Kind != SlotKindSpare || got.State != SlotStateWanted {
		t.Fatalf("staged %s as %s/%s, want X1-HOME-YARD as SPARE/WANTED", got.Waypoint, got.Kind, got.State)
	}
	if got.AssignedShip != "" {
		t.Fatalf("the staged want names hull %q — a SPARE want must name NO hull; naming the borrowed one "+
			"would have the probe cap count a frigate and the placement machine dedicate it into the "+
			"sensing fleet", got.AssignedShip)
	}
}

// --- the borrowed hull is never taken ----------------------------------------

// THE STRANDING GUARD, and it is the reason this pass is not routed through
// footholdFromSurplus. That path claims the placement FOR THE CARRIER, and a
// placement row naming a frigate is what dedicates it into the sensing fleet
// (placement.go dispatchClaim → AssignFleet) and what makes CountOwnedProbes count
// it — CountOwnedProbes selects on state and assigned_ship and never on role. So
// the borrow must write NOTHING.
func TestAdvanceExpansion_LendingAHullWritesNoPlacementNoSeedAndNoTransition(t *testing.T) {
	h, memo := coldFleet()

	rep, err := h.runWithMemo(t, memo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.CountersStaffed != 1 {
		t.Fatalf("CountersStaffed = %d, want 1 — the fixture must actually lend, or this proves nothing", rep.CountersStaffed)
	}

	if len(h.ledger.upsertedSlots) != 0 {
		t.Fatalf("the borrow wrote %v — a placement row naming TORWIND-1 would put a command frigate inside "+
			"the probe cap and hand it to the sensing fleet permanently", h.ledger.upsertedSlots)
	}
	if len(h.ledger.transitions) != 0 {
		t.Fatalf("the borrow transitioned %v — it must claim no placement at all", h.ledger.transitions)
	}
	if len(h.ledger.setSeeds) != 0 {
		t.Fatalf("the borrow stamped errands %v — a frigate must never be given a charting mission", h.ledger.setSeeds)
	}
	if len(h.ledger.deleted) != 0 {
		t.Fatalf("the borrow deleted placements %v — it takes nothing off the books", h.ledger.deleted)
	}
}

// --- the warm fleet is untouched ---------------------------------------------

// A FLEET THAT CAN ALREADY STAGE NEVER BORROWS, and it never even ASKS: the ships
// read is behind the deadlock counter, so the ordinary path costs not one extra
// port call. Proven on the existing evidenced-yard world rather than a new fixture,
// so a regression in the ordinary path shows up here too.
func TestAdvanceExpansion_AStaffedCounterBorrowsNothingAndNeverAsks(t *testing.T) {
	h, memo := twoYardWorld()
	// Adversarial: a perfectly borrowable hull sits in a system holding an
	// UNSTAFFED counter. An implementation that ran the pass unconditionally would
	// fly it.
	h.ledger.systems = append(h.ledger.systems, ExpandSystem{System: "X1-SPARE", Verdict: VerdictInScope})
	h.gates.adjacency["X1-SPARE"] = []string{"X1-TARGET"}
	h.yards.bySystem["X1-SPARE"] = []string{"X1-SPARE-YARD"}
	h.ships.lendable = []LendableHull{
		{ShipSymbol: "TORWIND-1", Waypoint: "X1-SPARE-A1", System: "X1-SPARE"},
	}

	rep, err := h.runWithMemo(t, memo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rep.SeedsRequested != 1 {
		t.Fatalf("SeedsRequested = %d, want 1 — the ordinary staging path must be unchanged", rep.SeedsRequested)
	}
	if rep.SeedsUnstaged != 0 || rep.CountersStaffed != 0 {
		t.Fatalf("SeedsUnstaged=%d CountersStaffed=%d, want 0/0 — a fleet that can stage a seed the ordinary "+
			"way must never take a hull off another coordinator", rep.SeedsUnstaged, rep.CountersStaffed)
	}
	if h.ships.lendableCalls != 0 {
		t.Fatalf("asked for lendable hulls %d times on a tick that staged everything it wanted — the read is "+
			"supposed to be behind the deadlock, so the ordinary path pays nothing for the escape",
			h.ships.lendableCalls)
	}
	if n := h.seed.countOf("navigate"); n != 0 {
		t.Fatalf("issued %d navigate commands, want 0", n)
	}
}

// --- nothing to lend ----------------------------------------------------------

// NO HULL AVAILABLE IS A WAIT, NOT A FAILURE. Every hull is out on its own work, so
// the tick reports the deadlock and completes: no panic, no error, and the target
// simply waits for the next tick.
func TestAdvanceExpansion_NoLendableHullLeavesTheTargetWaiting(t *testing.T) {
	h, memo := coldFleet()
	h.ships.lendable = nil

	rep, err := h.runWithMemo(t, memo)
	if err != nil {
		t.Fatalf("a fleet with nothing to lend must not fail the tick: %v", err)
	}
	if rep.SeedsUnstaged != 1 {
		t.Fatalf("SeedsUnstaged = %d, want 1 — the deadlock is still reported", rep.SeedsUnstaged)
	}
	if rep.CountersStaffed != 0 {
		t.Fatalf("CountersStaffed = %d, want 0", rep.CountersStaffed)
	}
	if n := h.seed.countOf("navigate"); n != 0 {
		t.Fatalf("issued %d navigate commands with no hull to send", n)
	}
}

// A REFUSED HOP COSTS NOTHING AND IS NOT AN ERROR. Nothing was written, so the next
// tick re-derives and retries for free; failing the tick here would take the
// off-gate pass down over a cooldown.
func TestAdvanceExpansion_ARefusedLendIsRetriedRatherThanFailingTheTick(t *testing.T) {
	h, memo := coldFleet()
	h.seed.navErr = errors.New("ship is on cooldown")

	rep, err := h.runWithMemo(t, memo)
	if err != nil {
		t.Fatalf("a refused hop must not fail the tick: %v", err)
	}
	if rep.CountersStaffed != 0 {
		t.Fatalf("CountersStaffed = %d, want 0 — the hull never left", rep.CountersStaffed)
	}
	if rep.Actions != 0 {
		t.Fatalf("Actions = %d, want 0 — a refused command bought nothing and must not spend the tick's budget",
			rep.Actions)
	}
}

// A SHIPS-TABLE READ FAILURE FAILS THE TICK rather than reading as "nothing to
// lend". This pass is the fleet's only way out of the deadlock, and a read fault
// silently reported as an empty fleet is indistinguishable from one.
func TestAdvanceExpansion_ALendableReadFailureStopsTheTick(t *testing.T) {
	h, memo := coldFleet()
	h.ships.lendableErr = errors.New("ships table unavailable")

	_, err := h.runWithMemo(t, memo)
	if err == nil {
		t.Fatal("an unreadable ships table was read as an empty fleet — the deadlock would then look like a " +
			"fleet with nothing to lend, forever, with a healthy heartbeat")
	}
	if n := h.seed.countOf("navigate"); n != 0 {
		t.Fatalf("commanded %d hulls off a failed read (the fake offers a usable one alongside the error)", n)
	}
}

// --- idempotence --------------------------------------------------------------

// TWO CONSECUTIVE TICKS DO NOT DOUBLE-DISPATCH. The hull sent on the first tick is
// IN_TRANSIT on the second and the counter it is bound for is struck out, so a
// SECOND idle hull standing in the same system is not sent after it.
//
// The guard is derived from the ships table rather than remembered, which is why it
// survives a restart: the fixture never tells the engine what the previous tick did.
func TestAdvanceExpansion_ASecondTickDoesNotSendASecondHullToTheSameCounter(t *testing.T) {
	h, memo := coldFleet()
	h.ships.lendable = append(h.ships.lendable,
		LendableHull{ShipSymbol: "TORWIND-7", Waypoint: "X1-HOME-A2", System: "X1-HOME"})

	if _, err := h.runWithMemo(t, memo); err != nil {
		t.Fatalf("unexpected error on the first tick: %v", err)
	}
	if n := h.seed.countOf("navigate"); n != 1 {
		t.Fatalf("first tick issued %d navigates, want exactly 1 — the per-tick bound is one hull", n)
	}

	// TORWIND-1 is now under way to the counter; TORWIND-7 is still standing by.
	h.ships.lendable[0] = LendableHull{
		ShipSymbol: "TORWIND-1", Waypoint: "X1-HOME-YARD", System: "X1-HOME", InTransit: true,
	}

	if _, err := h.runWithMemo(t, memo); err != nil {
		t.Fatalf("unexpected error on the second tick: %v", err)
	}
	if n := h.seed.countOf("navigate"); n != 1 {
		t.Fatalf("issued %d navigates across two ticks, want 1 — a hull is already inbound to X1-HOME-YARD, "+
			"and sending a second one is two coordinators' hulls spent on one counter", n)
	}
}

// ONE HULL PER TICK even when several counters are open and several hulls are free.
// The bound is a pacing rule: a tick reads one picture of the fleet and must not
// empty it on the strength of it.
func TestAdvanceExpansion_LendsAtMostOneHullPerTick(t *testing.T) {
	h, memo := coldFleet()
	h.ledger.systems = append(h.ledger.systems, ExpandSystem{System: "X1-ALT", Verdict: VerdictInScope})
	h.gates.adjacency["X1-ALT"] = []string{"X1-DARK"}
	h.yards.bySystem["X1-ALT"] = []string{"X1-ALT-YARD"}
	memo.sells["X1-ALT-YARD"] = true
	memo.scannedAt["X1-ALT-YARD"] = time.Now()
	h.ships.lendable = append(h.ships.lendable,
		LendableHull{ShipSymbol: "TORWIND-7", Waypoint: "X1-ALT-A1", System: "X1-ALT"})

	rep, err := h.runWithMemo(t, memo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.CountersStaffed != 1 {
		t.Fatalf("CountersStaffed = %d, want 1 — two open counters and two free hulls, and the tick still "+
			"lends exactly one", rep.CountersStaffed)
	}
}

// --- what the borrow refuses to do -------------------------------------------

// IN-SYSTEM ONLY. A borrowed hull is never walked across a gate: a foothold converts
// a system and can justify a multi-tick crossing of a hull this engine OWNS, but a
// hauler taken from the contract fleet cannot be held for one.
//
// Adversarial fixture: the only open counter is one gate hop from the only free
// hull, and it is evidenced and perfectly routable to the target.
func TestAdvanceExpansion_ALentHullIsNeverWalkedAcrossAGate(t *testing.T) {
	h, memo := coldFleet()
	// The hull is parked in a system with no shipyard at all.
	h.ledger.systems = append(h.ledger.systems, ExpandSystem{System: "X1-CAMP", Verdict: VerdictInScope})
	h.gates.adjacency["X1-CAMP"] = []string{"X1-HOME"}
	h.ships.lendable = []LendableHull{
		{ShipSymbol: "TORWIND-1", Waypoint: "X1-CAMP-A1", System: "X1-CAMP"},
	}

	rep, err := h.runWithMemo(t, memo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.CountersStaffed != 0 {
		t.Fatalf("CountersStaffed = %d, want 0 — X1-HOME-YARD is one hop away and the borrow is in-system "+
			"only; crossing a gate with a borrowed hull is a multi-tick hold on another coordinator's work",
			rep.CountersStaffed)
	}
	if n := h.seed.countOf("navigate"); n != 0 {
		t.Fatalf("issued %d navigate commands, want 0", n)
	}
}

// A HULL IS NEVER FLOWN TO THE WAYPOINT IT IS ALREADY ON. Standing there while
// staffedAt still says NO means it is in ORBIT above the counter, not berthed at it,
// and the fix for that is a dock command this pass does not hold — so the hop would
// be re-issued and refused on every tick forever.
func TestAdvanceExpansion_DoesNotFlyALentHullToTheWaypointItIsAlreadyOn(t *testing.T) {
	h, memo := coldFleet()
	h.ships.lendable = []LendableHull{
		{ShipSymbol: "TORWIND-1", Waypoint: "X1-HOME-YARD", System: "X1-HOME"},
	}

	rep, err := h.runWithMemo(t, memo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.CountersStaffed != 0 {
		t.Fatalf("CountersStaffed = %d, want 0 — TORWIND-1 is already at X1-HOME-YARD and no berth command "+
			"exists here, so flying it to itself buys nothing and never stops", rep.CountersStaffed)
	}
	if n := h.seed.countOf("navigate"); n != 0 {
		t.Fatalf("issued %d navigate commands to the hull's own waypoint", n)
	}
}

// A COUNTER KNOWN TO SELL NO PROBE IS NEVER STAFFED. It reads the SAME rule staging
// reads (probeStock.acceptsStaging), so the pass can never open a counter staging
// would then refuse to use — which would be a hull spent for nothing, every tick.
func TestAdvanceExpansion_DoesNotStaffACounterKnownToSellNoProbe(t *testing.T) {
	h, memo := coldFleet()
	memo.sells["X1-HOME-YARD"] = false // priced, recently, and it sells no probe

	rep, err := h.runWithMemo(t, memo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.CountersStaffed != 0 {
		t.Fatalf("CountersStaffed = %d, want 0 — the stored listings say X1-HOME-YARD sells no probe, so a "+
			"hull standing there could buy nothing", rep.CountersStaffed)
	}
}

// A NEVER-PRICED COUNTER IS STILL STAFFED, on the second pass. It is a trait guess,
// but it is also how a cold fleet LEARNS where probes are sold: in a first-mover era
// no yard has ever been priced, so ranking the guess last must not mean dropping it.
func TestAdvanceExpansion_StaffsANeverPricedCounterWhenNoEvidencedOneIsOpen(t *testing.T) {
	h, memo := coldFleet()
	delete(memo.sells, "X1-HOME-YARD")
	delete(memo.scannedAt, "X1-HOME-YARD") // absent from scannedAt == never read

	rep, err := h.runWithMemo(t, memo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.CountersStaffed != 1 {
		t.Fatalf("CountersStaffed = %d, want 1 — nothing has ever been priced in a first-mover era, so a "+
			"never-read counter is the only kind there is and refusing it re-seals the deadlock",
			rep.CountersStaffed)
	}
}

// EVIDENCE FIRST, exactly as staging ranks it: a counter we have PRICED and found
// selling probes beats a never-priced trait guess, even when the guess is offered by
// an earlier hull in the list.
func TestAdvanceExpansion_StaffsAnEvidencedCounterAheadOfATraitGuess(t *testing.T) {
	h, memo := coldFleet()
	delete(memo.sells, "X1-HOME-YARD")
	delete(memo.scannedAt, "X1-HOME-YARD") // X1-HOME's counter is now a guess
	h.ledger.systems = append(h.ledger.systems, ExpandSystem{System: "X1-ALT", Verdict: VerdictInScope})
	h.gates.adjacency["X1-ALT"] = []string{"X1-DARK"}
	h.yards.bySystem["X1-ALT"] = []string{"X1-ALT-YARD"}
	memo.sells["X1-ALT-YARD"] = true
	memo.scannedAt["X1-ALT-YARD"] = time.Now()
	// The guess's hull is FIRST in the list, so an implementation that walked hulls
	// as the outer loop would take it.
	h.ships.lendable = append(h.ships.lendable,
		LendableHull{ShipSymbol: "TORWIND-7", Waypoint: "X1-ALT-A1", System: "X1-ALT"})

	rep, err := h.runWithMemo(t, memo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.CountersStaffed != 1 {
		t.Fatalf("CountersStaffed = %d, want 1", rep.CountersStaffed)
	}
	navs := h.seed.calls
	if len(navs) != 1 || navs[0].arg != "X1-ALT-YARD" {
		t.Fatalf("lent a hull to %v, want X1-ALT-YARD — it carries a stored SHIP_PROBE listing while "+
			"X1-HOME-YARD is only a shipyard-trait guess", navs)
	}
}

// A SYSTEM STAGING WOULD NEVER CHOOSE IS NOT WORTH A HULL. The counter is open and
// evidenced, but no target needing a seed is routable from it — so staffing it could
// never turn into a seed request, and the flight would be pure waste.
func TestAdvanceExpansion_DoesNotStaffACounterNoTargetIsRoutableFrom(t *testing.T) {
	h, memo := coldFleet()
	// Sever X1-HOME from the target: the counter is still there, and still useless.
	h.gates.adjacency["X1-HOME"] = []string{}

	rep, err := h.runWithMemo(t, memo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsUnstaged != 1 {
		t.Fatalf("SeedsUnstaged = %d, want 1 — the target is still unstaged", rep.SeedsUnstaged)
	}
	if rep.CountersStaffed != 0 {
		t.Fatalf("CountersStaffed = %d, want 0 — no target is routable from X1-HOME, so staging would never "+
			"pick its counter however well staffed it was", rep.CountersStaffed)
	}
}

// A COUNTER ALREADY HOLDING AN UNFUNDABLE SPARE WANT IS THE MOST VALUABLE ONE TO
// STAFF, and it is deliberately eligible here where stagingYardFor skips it. That
// row is exactly the placement footholdFromSurplus exists to fill and cannot, cold:
// staffing the counter makes it fundable on the next drain.
func TestAdvanceExpansion_StaffsACounterHoldingAStrandedSpareWant(t *testing.T) {
	h, memo := coldFleet()
	h.ledger.slots = []QueuedSlot{{
		Waypoint: "X1-HOME-YARD", System: "X1-HOME", Kind: SlotKindSpare, State: SlotStateWanted,
	}}

	rep, err := h.runWithMemo(t, memo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.CountersStaffed != 1 {
		t.Fatalf("CountersStaffed = %d, want 1 — a SPARE want standing at an unstaffed counter is the "+
			"foothold's own deadlock, and lending it a buyer is what unfreezes it", rep.CountersStaffed)
	}
}

// --- the spend pause reaches it ----------------------------------------------

// THE PAUSE STOPS THE BORROW, because the borrow COMMANDS A HULL. Everything below
// the spend gate either moves a ship or raises a purchase intent, and this does the
// first in service of the second.
func TestAdvanceExpansion_SpendPauseLendsNoHull(t *testing.T) {
	h, memo := coldFleet()

	p := h.ports()
	p.ListingMemo = memo
	rep, err := advanceExpansionPaused(t, p, h)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !rep.SpendingPaused {
		t.Fatal("SpendingPaused = false, want true")
	}
	if rep.CountersStaffed != 0 {
		t.Fatalf("CountersStaffed = %d, want 0 — a paused tick must command no hull", rep.CountersStaffed)
	}
	if h.ships.lendableCalls != 0 {
		t.Fatalf("a paused tick asked for lendable hulls %d times; it must not reach the pass at all",
			h.ships.lendableCalls)
	}
}

// --- staffedAt sees the borrowed hull ----------------------------------------

// THE PREDICATE IS A SUPERSET, in buyerAt's order. A probe on station still answers
// first and the borrowed-hull read is only consulted when it misses, so no yard that
// staged before the escape existed stops staging now.
func TestStaffedAt_PrefersAProbeAndFallsBackToALentHull(t *testing.T) {
	h, memo := coldFleet()
	h.ships.docked = map[string]string{"X1-HOME-YARD": "PROBE-ON-STATION"}

	rep, err := h.runWithMemo(t, memo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsRequested != 1 {
		t.Fatalf("SeedsRequested = %d, want 1 — a probe standing at the counter is the ordinary path and must "+
			"still stage", rep.SeedsRequested)
	}
	if rep.CountersStaffed != 0 {
		t.Fatalf("CountersStaffed = %d, want 0 — a probe is already there", rep.CountersStaffed)
	}
}

// A FAILED BORROWED-HULL READ PROPAGATES, exactly as the probe read's does. Read
// permissively it would silently stop staging seeds for as long as the ships table
// was unhappy; the tick is idempotent, so failing loudly costs one cycle.
func TestStaffedAt_ALentHullReadFailurePropagates(t *testing.T) {
	h, memo := coldFleet()
	h.ships.lentErr = errors.New("ships table unavailable")

	_, err := h.runWithMemo(t, memo)
	if err == nil {
		t.Fatal("an unreadable ships table was read as an empty counter — the fake offers a usable buyer " +
			"alongside the error, so a caller that leaks it stages a want nothing can fund")
	}
}

// advanceExpansionPaused runs one tick with the operator's spend switch OFF.
func advanceExpansionPaused(t *testing.T, p ExpandPorts, h *expandHarness) (ExpandReport, error) {
	t.Helper()
	for i := range h.ledger.systems {
		h.ledger.systems[i].CatalogKnown = !h.unswept[h.ledger.systems[i].System]
	}
	return AdvanceExpansion(context.Background(), p, 1, ExpandKnobs{
		SpendEnabled: false, MinBudgetRate: 0.05, Whitelist: h.whitelist,
	}, 1.0)
}
