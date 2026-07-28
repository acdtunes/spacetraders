package parkedsensing

import (
	"testing"
)

// staging_test.go pins the invariant requestSeeds' own doc comment states and
// the code did not enforce: a seed is staged at a yard THE BUY QUEUE CAN
// ACTUALLY FUND — "one in a system we already hold, since the queue only buys
// where a hull of ours is standing at the counter".
//
// stagingYardFor accepted any probe-selling yard in any bordering system that
// carried a screening verdict. A verdict means "screened and worth trading
// with"; it does NOT mean we have a hull there. So seeds were staged at yards in
// systems we had never visited, the buy queue correctly refused them forever,
// and the target never got eyes.
//
// THE FIXTURE TRAP THIS FILE AVOIDS. If both bordering origins held a hull, the
// filtered and unfiltered origin sets would stage the same yard and the test
// would prove nothing. So in every fixture below exactly ONE bordering system is
// staffed, and the other is screened IN_SCOPE with a probe-selling yard and no
// hull at all — the empty one sorts FIRST alphabetically, so an unfiltered
// stagingYardFor picks it.

// staffedYardRow is a hull of ours STANDING at a yard: the ledger row that makes
// that yard a place the buy queue could actually buy through.
//
// It exists because several expansion tests predate the occupancy requirement
// and their fixtures gave the staging origin a yard but no hull — a fleet state
// that cannot occur, and the reason the bug went unnoticed. Each of those
// fixtures now says out loud that we are actually there. The row is MARKET-kind
// because that is the live shape (a probe-selling yard worth buying at is
// normally one we already watch) and because a SPARE row on the same waypoint
// would trip the separate free-yard guard.
func staffedYardRow(system, waypoint string) QueuedSlot {
	return QueuedSlot{
		Waypoint: waypoint, System: system, Kind: SlotKindMarket,
		State: SlotStateParked, AssignedShip: "PROBE-AT-" + waypoint,
	}
}

// TestAdvanceExpansion_StagesTheSeedAtAnOccupiedYardNotMerelyAScreenedOne is the
// bug: X1-AA is screened, has a yard, and has nothing of ours standing on it.
// X1-BB is screened, has a yard, and has a PARKED placement holding a hull.
// Only X1-BB's yard can fund a purchase.
func TestAdvanceExpansion_StagesTheSeedAtAnOccupiedYardNotMerelyAScreenedOne(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-AA", Verdict: VerdictInScope},
		{System: "X1-BB", Verdict: VerdictInScope},
		{System: "X1-TARGET", Verdict: VerdictPending, UnchartedCount: 3},
	}
	h.gates.adjacency = map[string][]string{
		"X1-AA":     {"X1-TARGET"},
		"X1-BB":     {"X1-TARGET"},
		"X1-TARGET": {"X1-AA", "X1-BB"},
	}
	h.yards.bySystem = map[string][]string{
		"X1-AA": {"X1-AA-YARD"}, // screened, but we have never been there
		"X1-BB": {"X1-BB-YARD"}, // screened AND occupied
	}
	// The only hull we actually have: parked at X1-BB's yard, so it can act as
	// the purchaser exactly as buyerAt requires.
	h.ledger.slots = []QueuedSlot{{
		Waypoint: "X1-BB-YARD", System: "X1-BB", Kind: SlotKindMarket,
		State: SlotStateParked, AssignedShip: "PROBE-BB",
	}}

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rep.SeedsRequested != 1 {
		t.Fatalf("SeedsRequested = %d, want 1", rep.SeedsRequested)
	}
	if len(h.ledger.upsertedSlots) != 1 {
		t.Fatalf("wrote %d slots, want exactly 1: %v", len(h.ledger.upsertedSlots), h.ledger.upsertedSlots)
	}
	got := h.ledger.upsertedSlots[0]
	if got.Waypoint != "X1-BB-YARD" || got.System != "X1-BB" {
		t.Fatalf("seed staged at %s in %s, want X1-BB-YARD in X1-BB — X1-AA is screened but unoccupied, so the buy queue can never fund a purchase there",
			got.Waypoint, got.System)
	}
}

// TestAdvanceExpansion_StagesNoSeedWhenNoBorderingSystemIsOccupied pins the
// other half: with nowhere fundable to stage, NOTHING is written.
//
// Writing an unfundable want is worse than writing none. The row is permanent
// (nothing retires a WANTED SPARE), the buy queue re-reads and re-refuses it
// every tick forever, and — through takeSupplyFor — it then SUPPRESSES the
// correct request that would otherwise be made once a bordering system becomes
// occupied. One bad row poisons the target indefinitely.
func TestAdvanceExpansion_StagesNoSeedWhenNoBorderingSystemIsOccupied(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-AA", Verdict: VerdictInScope},
		{System: "X1-TARGET", Verdict: VerdictPending, UnchartedCount: 3},
	}
	h.gates.adjacency = map[string][]string{
		"X1-AA":     {"X1-TARGET"},
		"X1-TARGET": {"X1-AA"},
	}
	h.yards.bySystem = map[string][]string{"X1-AA": {"X1-AA-YARD"}}
	// No slots at all: we hold nothing anywhere.

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("no fundable staging yard must not fail the tick: %v", err)
	}
	if rep.SeedsRequested != 0 {
		t.Fatalf("SeedsRequested = %d, want 0 — no bordering system holds a hull", rep.SeedsRequested)
	}
	if len(h.ledger.upsertedSlots) != 0 {
		t.Fatalf("wrote a seed want the buy queue can never fund: %v", h.ledger.upsertedSlots)
	}
}

// TestAdvanceExpansion_AParkedRowWithNoHullDoesNotStaffAYard pins the exact
// condition buyerAt applies. A PARKED row whose assigned_ship is empty is a torn
// or released row, not a hull standing at the counter, and it must not make a
// yard look fundable.
func TestAdvanceExpansion_AParkedRowWithNoHullDoesNotStaffAYard(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-AA", Verdict: VerdictInScope},
		{System: "X1-TARGET", Verdict: VerdictPending, UnchartedCount: 3},
	}
	h.gates.adjacency = map[string][]string{
		"X1-AA":     {"X1-TARGET"},
		"X1-TARGET": {"X1-AA"},
	}
	h.yards.bySystem = map[string][]string{"X1-AA": {"X1-AA-YARD"}}
	h.ledger.slots = []QueuedSlot{{
		Waypoint: "X1-AA-YARD", System: "X1-AA", Kind: SlotKindMarket,
		State: SlotStateParked, AssignedShip: "", // no hull behind the row
	}}

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsRequested != 0 || len(h.ledger.upsertedSlots) != 0 {
		t.Fatalf("staged a seed on the strength of a PARKED row naming no hull: %d requested, %v",
			rep.SeedsRequested, h.ledger.upsertedSlots)
	}
}

// TestAdvanceExpansion_AnUnarrivedHullDoesNotStaffAYard pins that only a hull
// actually STANDING at the counter staffs it. A placement still IN_TRANSIT names
// a real hull, but that hull is somewhere between here and there — the buy queue
// could not buy through it, so it cannot make the yard a staging candidate.
func TestAdvanceExpansion_AnUnarrivedHullDoesNotStaffAYard(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-AA", Verdict: VerdictInScope},
		{System: "X1-TARGET", Verdict: VerdictPending, UnchartedCount: 3},
	}
	h.gates.adjacency = map[string][]string{
		"X1-AA":     {"X1-TARGET"},
		"X1-TARGET": {"X1-AA"},
	}
	h.yards.bySystem = map[string][]string{"X1-AA": {"X1-AA-YARD"}}
	h.ledger.slots = []QueuedSlot{{
		Waypoint: "X1-AA-YARD", System: "X1-AA", Kind: SlotKindMarket,
		State: SlotStateInTransit, AssignedShip: "PROBE-FLYING",
	}}

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsRequested != 0 || len(h.ledger.upsertedSlots) != 0 {
		t.Fatalf("staged a seed at a yard whose hull has not arrived: %d requested, %v",
			rep.SeedsRequested, h.ledger.upsertedSlots)
	}
}

// TestAdvanceExpansion_AnUnfundableWantIsNotSupply is the second half of the
// bug, and the half that makes the first half self-perpetuating.
//
// takeSupplyFor suppresses a seed request when a SPARE row already sits in a
// bordering system — "a seed for this target is already somewhere in the
// pipeline". A want staged at an UNSTAFFED yard is not in any pipeline: nothing
// retires it, and the buy queue refuses it on every tick forever. Counted as
// supply it permanently blocks the correct request for the very target it was
// meant to serve.
//
// Here X1-BAD holds exactly such a row and borders the target; X1-BB is staffed
// and also borders it. The seed must still be staged at X1-BB.
func TestAdvanceExpansion_AnUnfundableWantIsNotSupply(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-BAD", Verdict: VerdictInScope},
		{System: "X1-BB", Verdict: VerdictInScope},
		{System: "X1-TARGET", Verdict: VerdictPending, UnchartedCount: 3},
	}
	h.gates.adjacency = map[string][]string{
		"X1-BAD":    {"X1-TARGET"},
		"X1-BB":     {"X1-TARGET"},
		"X1-TARGET": {"X1-BAD", "X1-BB"},
	}
	h.yards.bySystem = map[string][]string{
		"X1-BAD": {"X1-BAD-YARD"},
		"X1-BB":  {"X1-BB-YARD"},
	}
	h.ledger.slots = []QueuedSlot{
		// The residue of the bug: a want at a yard we do not hold, with no hull
		// behind it and no way to ever get one.
		{Waypoint: "X1-BAD-YARD", System: "X1-BAD", Kind: SlotKindSpare, State: SlotStateWanted},
		staffedYardRow("X1-BB", "X1-BB-YARD"),
	}

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsRequested != 1 {
		t.Fatalf("SeedsRequested = %d, want 1 — a want nothing can fund is not a seed on order, and must not suppress the one that is",
			rep.SeedsRequested)
	}
	if got := h.ledger.upsertedSlots[0]; got.Waypoint != "X1-BB-YARD" {
		t.Fatalf("seed staged at %s, want X1-BB-YARD", got.Waypoint)
	}
}

// TestAdvanceExpansion_AStaleWantBecomesSupplyOnceItsYardIsStaffed is why the
// rows the bug already wrote are LEFT IN PLACE rather than pruned.
//
// A stale want is inert, not toxic: takeSupplyFor no longer counts it (see
// above), and the only thing it blocks is its OWN waypoint, through
// book.occupied. And that block is self-healing — the moment a hull stands at
// that yard the row stops being residue and becomes exactly the seed request it
// was always meant to be, fundable by the buy queue on the next drain. It needs
// presence, which is what this fix delivers; it does not need deleting.
//
// Pruning would buy nothing and risk something: DeleteSlot on a SPARE row is the
// sp-wgjb7 money-guard class, and a row that has — or races into having — an
// assigned ship would drop a real hull out of the probe-cap count and authorise
// buying a replacement for a probe standing right there. Leaving a row is not a
// write; deleting one is.
func TestAdvanceExpansion_AStaleWantBecomesSupplyOnceItsYardIsStaffed(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-BAD", Verdict: VerdictInScope},
		{System: "X1-TARGET", Verdict: VerdictPending, UnchartedCount: 3},
	}
	h.gates.adjacency = map[string][]string{
		"X1-BAD":    {"X1-TARGET"},
		"X1-TARGET": {"X1-BAD"},
	}
	h.yards.bySystem = map[string][]string{"X1-BAD": {"X1-BAD-YARD"}}
	h.ledger.slots = []QueuedSlot{
		// The row the bug wrote, untouched...
		{Waypoint: "X1-BAD-YARD", System: "X1-BAD", Kind: SlotKindSpare, State: SlotStateWanted},
		// ...and a hull that has since arrived at that very yard.
		staffedYardRow("X1-BAD", "X1-BAD-YARD"),
	}

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsRequested != 0 {
		t.Fatalf("SeedsRequested = %d, want 0 — the stale want is now fundable, so it IS the outstanding request and must not be duplicated",
			rep.SeedsRequested)
	}
	if len(h.ledger.deleted) != 0 {
		t.Fatalf("a stale want must never be deleted — dropping a SPARE row is the sp-wgjb7 money-guard class: %v", h.ledger.deleted)
	}
}

// TestAdvanceExpansion_AWantWithAHullIsStillSupply is the guard on the other
// side of that change, and it matters more than it looks.
//
// A SPARE row that already NAMES a hull is a seed genuinely in flight — bought,
// or flying, or parked. Its yard is usually no longer "staffed" by anything else,
// so a fundability test applied bluntly would stop counting it and the tick would
// order a SECOND hull for a target already being served. That is a spend, and it
// is the direction RULINGS #4 forbids.
func TestAdvanceExpansion_AWantWithAHullIsStillSupply(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-BAD", Verdict: VerdictInScope},
		{System: "X1-TARGET", Verdict: VerdictPending, UnchartedCount: 3},
	}
	h.gates.adjacency = map[string][]string{
		"X1-BAD":    {"X1-TARGET"},
		"X1-TARGET": {"X1-BAD"},
	}
	h.yards.bySystem = map[string][]string{"X1-BAD": {"X1-BAD-YARD"}}
	h.ledger.slots = []QueuedSlot{
		// Bought and flying: a seed already on its way to this neighbourhood.
		// Its own waypoint is NOT staffed — that is the normal shape for a hull
		// in transit, and it is exactly what a fundability test applied to this
		// row would trip over.
		{Waypoint: "X1-BAD-Y2", System: "X1-BAD", Kind: SlotKindSpare,
			State: SlotStateInTransit, AssignedShip: "PROBE-ONWAY"},
		// A STAFFED yard stands ready in the same system. Without this the test
		// proves nothing: staging would fail for want of a yard and the count
		// would read 0 whether or not the hull test guarded the supply check.
		// With it, dropping that guard orders a second hull — which is the spend
		// this pins.
		staffedYardRow("X1-BAD", "X1-BAD-YARD"),
	}

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsRequested != 0 {
		t.Fatalf("SeedsRequested = %d, want 0 — a hull already flying to this neighbourhood is supply, and re-ordering spends money on a target already served",
			rep.SeedsRequested)
	}
}

// TestAdvanceExpansion_AProbeDockedAtAYardStaffsItWithoutALedgerRow widens the
// staffing test to exactly what the buy queue accepts, and no further.
//
// buyerAt takes EITHER a PARKED placement naming a hull OR any probe of ours the
// ships table shows docked at the waypoint. The second case is real: a hull can
// be standing at a counter before this engine has written a row for it. Staging
// must accept it too, or it declines a yard the queue would have funded.
//
// It must NOT go further than that. The purchase command will dock a hull it
// finds in orbit, but buyerAt — which is what actually selects the buyer — reads
// DOCKED only, so staging on an orbiting hull would recreate the very bug this
// file exists to fix, one layer down.
func TestAdvanceExpansion_AProbeDockedAtAYardStaffsItWithoutALedgerRow(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-BB", Verdict: VerdictInScope},
		{System: "X1-TARGET", Verdict: VerdictPending, UnchartedCount: 3},
	}
	h.gates.adjacency = map[string][]string{
		"X1-BB":     {"X1-TARGET"},
		"X1-TARGET": {"X1-BB"},
	}
	h.yards.bySystem = map[string][]string{"X1-BB": {"X1-BB-YARD"}}
	// No placement row anywhere — the only evidence we are there is the hull
	// itself, docked at the counter.
	h.ships.docked = map[string]string{"X1-BB-YARD": "PROBE-UNRECORDED"}

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsRequested != 1 {
		t.Fatalf("SeedsRequested = %d, want 1 — a docked probe staffs its yard even with no placement row, exactly as buyerAt reads it",
			rep.SeedsRequested)
	}
	if got := h.ledger.upsertedSlots[0]; got.Waypoint != "X1-BB-YARD" {
		t.Fatalf("seed staged at %s, want X1-BB-YARD", got.Waypoint)
	}
}

// TestAdvanceExpansion_StagingIsPerYardNotPerSystem pins the GRANULARITY of the
// staffing test, and with it that the read never filters on slot kind.
//
// A hull somewhere in the system does not make every yard in it fundable —
// buyerAt wants a hull at THAT waypoint — so a system-level test would still
// stage on a bare yard and still never be funded. And the hull that staffs a
// yard is normally recorded under a MARKET row, because a probe-selling yard
// worth buying at is usually one we already watch (states.go); a read filtered
// to YARD-kind rows would miss it and call the fleet's best yards empty.
//
// Here the bare yard sorts FIRST, so both mistakes pick it.
func TestAdvanceExpansion_StagingIsPerYardNotPerSystem(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-BB", Verdict: VerdictInScope},
		{System: "X1-TARGET", Verdict: VerdictPending, UnchartedCount: 3},
	}
	h.gates.adjacency = map[string][]string{
		"X1-BB":     {"X1-TARGET"},
		"X1-TARGET": {"X1-BB"},
	}
	h.yards.bySystem = map[string][]string{"X1-BB": {"X1-BB-BARE", "X1-BB-STAFFED"}}
	h.ledger.slots = []QueuedSlot{{
		Waypoint: "X1-BB-STAFFED", System: "X1-BB", Kind: SlotKindMarket,
		State: SlotStateParked, AssignedShip: "PROBE-BB",
	}}

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsRequested != 1 {
		t.Fatalf("SeedsRequested = %d, want 1 — the staffed yard is fundable", rep.SeedsRequested)
	}
	if got := h.ledger.upsertedSlots[0]; got.Waypoint != "X1-BB-STAFFED" {
		t.Fatalf("seed staged at %s, want X1-BB-STAFFED — the bare yard cannot fund a purchase", got.Waypoint)
	}
}
