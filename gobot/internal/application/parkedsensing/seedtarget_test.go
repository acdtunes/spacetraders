package parkedsensing

import (
	"errors"
	"testing"
	"time"
)

// seedtarget_test.go pins where a CHARTING SEED is bought once its target is known.
//
// THE DEFECT. Every seed-relative walk went through gateReach.hops, which reports
// "not reachable" for origin==target. Right for a foothold's carrier pool, wrong for
// a seed: it made the target's own system structurally ineligible to stage the seed
// that charts it, so a frontier system holding a probe-selling counter of its own was
// sent hulls bought at least one gate behind it and ferried in — and the probe that
// then parked there could not be claimed to chart the system it was standing in.
//
// MEASURED LIVE (2026-09-01, 369 systems, 1,254 hulls): charting fell 189 -> 92
// charts/hour with 95 of 120 seed hulls DISPATCHED and 25 CHARTING. X1-BU71 holds two
// known yards, one selling SHIP_PROBE, and still took 9 ferried hulls.
//
// WHAT IS NOT THE DEFECT, and is not touched here: five of six frontier targets have
// no known probe yard at all, because a yard is only discoverable by charting the
// system. Those seeds still stage behind the frontier and still fly, and every
// fixture below that names a yard-less target asserts exactly that.
//
// THE PRICE IS STILL THE BUY QUEUE'S. Staging chooses where the hull is WANTED;
// procurement then ranks every counter in reach on landed cost — ask plus hops x
// (gate fee + procurement_jump_penalty_credits) — against that placement, and the
// walk-away ceiling still refuses a counter asking too much. Anchoring the placement
// on the target is what puts the counter -> target walk into that ranking, in the
// vocabulary it already speaks.

// frontierWithItsOwnCounter is X1-BU71's shape, reduced:
//
//	X1-HOME (evidenced probe counter, a probe of ours on it)
//	   |
//	X1-BU71 (uncharted target; evidenced probe counter, a probe of ours on it)
//
// Both counters can transact, so the choice between them is a choice about WHERE THE
// SEED IS GOING and nothing else. X1-HOME is the answer the old walk gave.
func frontierWithItsOwnCounter() (*expandHarness, *fakeListingMemo) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-HOME", Verdict: VerdictInScope},
		{System: "X1-BU71", Verdict: VerdictInScope, UnchartedCount: 7},
	}
	h.gates.adjacency = map[string][]string{
		"X1-HOME": {"X1-BU71"},
		"X1-BU71": {"X1-HOME"},
	}
	h.yards.bySystem = map[string][]string{
		"X1-HOME": {"X1-HOME-YARD"},
		"X1-BU71": {"X1-BU71-YARD"},
	}
	h.ledger.slots = []QueuedSlot{
		staffedYardRow("X1-HOME", "X1-HOME-YARD"),
		staffedYardRow("X1-BU71", "X1-BU71-YARD"),
	}
	memo := &fakeListingMemo{
		sells: map[string]bool{"X1-HOME-YARD": true, "X1-BU71-YARD": true},
		scannedAt: map[string]time.Time{
			"X1-HOME-YARD": time.Now(), "X1-BU71-YARD": time.Now(),
		},
	}
	return h, memo
}

// stagedYard names the single SPARE want this tick wrote, failing when it wrote a
// number other than one.
func stagedYard(t *testing.T, h *expandHarness) SlotRecord {
	t.Helper()
	if len(h.ledger.upsertedSlots) != 1 {
		t.Fatalf("wrote %d placements, want exactly 1: %v", len(h.ledger.upsertedSlots), h.ledger.upsertedSlots)
	}
	return h.ledger.upsertedSlots[0]
}

// THE HEADLINE. The target sells probes and holds a hull of ours to buy through, so
// the seed is wanted THERE and starts charting where it was bought.
func TestAdvanceExpansion_StagesTheSeedAtTheTargetsOwnProbeCounter(t *testing.T) {
	h, memo := frontierWithItsOwnCounter()

	rep, err := h.runWithMemo(t, memo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsRequested != 1 {
		t.Fatalf("SeedsRequested = %d, want 1 (report %+v)", rep.SeedsRequested, rep)
	}

	got := stagedYard(t, h)
	if got.Waypoint != "X1-BU71-YARD" || got.System != "X1-BU71" {
		t.Fatalf("seed staged at %s in %s, want X1-BU71-YARD in X1-BU71 — the target sells probes and "+
			"holds a hull to buy through, so a want written at X1-HOME buys a hull that spends its "+
			"errand flying to work it could have started on",
			got.Waypoint, got.System)
	}
	if got.Kind != SlotKindSpare || got.State != SlotStateWanted {
		t.Fatalf("staged a %s/%s row, want %s/%s — the buy queue funds this under the same floor and "+
			"probe cap as every other placement", got.Kind, got.State, SlotKindSpare, SlotStateWanted)
	}
}

// The irreducible case, and it must read exactly as it did: a target whose yards are
// unknown because nobody has charted it still buys behind the frontier and flies.
func TestAdvanceExpansion_StagesBehindTheFrontierWhenTheTargetHasNoYard(t *testing.T) {
	h, memo := frontierWithItsOwnCounter()
	h.yards.bySystem = map[string][]string{"X1-HOME": {"X1-HOME-YARD"}}
	h.ledger.slots = []QueuedSlot{staffedYardRow("X1-HOME", "X1-HOME-YARD")}

	if _, err := h.runWithMemo(t, memo); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := stagedYard(t, h); got.Waypoint != "X1-HOME-YARD" || got.System != "X1-HOME" {
		t.Fatalf("seed staged at %s in %s, want X1-HOME-YARD in X1-HOME — a target with no known counter "+
			"has to be bought for from behind the frontier, and that is not the defect",
			got.Waypoint, got.System)
	}
}

// FAIL CLOSED ON PRESENCE. A counter in the target that no hull of ours is standing
// at cannot sell us anything, so it must not attract the want away from one that can.
func TestAdvanceExpansion_StagesBehindTheFrontierWhenTheTargetsCounterIsUnstaffed(t *testing.T) {
	h, memo := frontierWithItsOwnCounter()
	h.ledger.slots = []QueuedSlot{staffedYardRow("X1-HOME", "X1-HOME-YARD")}

	if _, err := h.runWithMemo(t, memo); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := stagedYard(t, h); got.Waypoint != "X1-HOME-YARD" {
		t.Fatalf("seed staged at %s, want X1-HOME-YARD — nothing of ours stands at X1-BU71-YARD, so a "+
			"want written there is refused on this tick and every tick after", got.Waypoint)
	}
}

// FAIL CLOSED ON STOCK. A counter we have PRICED and found probe-less is admitted by
// neither staging pass, target or not: staging there writes a want the buy queue
// scans, learns nothing from, and refuses for the memo's whole TTL.
func TestAdvanceExpansion_StagesBehindTheFrontierWhenTheTargetsCounterSellsNoProbe(t *testing.T) {
	h, memo := frontierWithItsOwnCounter()
	memo.sells["X1-BU71-YARD"] = false

	if _, err := h.runWithMemo(t, memo); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := stagedYard(t, h); got.Waypoint != "X1-HOME-YARD" {
		t.Fatalf("seed staged at %s, want X1-HOME-YARD — X1-BU71-YARD has been read and sells no probe",
			got.Waypoint)
	}
}

// FAIL CLOSED ON THE READ ITSELF. With the yard catalog refusing, the tick stages
// NOTHING rather than staging on evidence it never legitimately read. The tick is
// idempotent and re-derived, so failing loudly costs one cycle.
func TestAdvanceExpansion_StagesNothingWhenTheYardCatalogCannotBeRead(t *testing.T) {
	h, memo := frontierWithItsOwnCounter()
	h.yards.err = errors.New("waypoint catalog unreadable")

	if _, err := h.runWithMemo(t, memo); err == nil {
		t.Fatal("an unreadable yard catalog must fail the tick, not stage a seed on a guess")
	}
	if len(h.ledger.upsertedSlots) != 0 {
		t.Fatalf("staged %v on an unreadable catalog", h.ledger.upsertedSlots)
	}
}

// A seed already on order IN the target is supply for it. Read as unreachable, the
// tick orders a SECOND probe for a target already served, every tick until the yards
// run out.
func TestAdvanceExpansion_ASeedAlreadyOnOrderInTheTargetSuppressesASecondRequest(t *testing.T) {
	h, memo := frontierWithItsOwnCounter()
	h.ledger.slots = append(h.ledger.slots, QueuedSlot{
		Waypoint: "X1-BU71-Y2", System: "X1-BU71", Kind: SlotKindSpare,
		State: SlotStateBought, AssignedShip: "PROBE-ONORDER",
	})

	rep, err := h.runWithMemo(t, memo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsRequested != 0 {
		t.Fatalf("SeedsRequested = %d, want 0 — PROBE-ONORDER is already bought for X1-BU71 (report %+v)",
			rep.SeedsRequested, rep)
	}
	if len(h.ledger.upsertedSlots) != 0 {
		t.Fatalf("ordered a second seed for a target already served: %v", h.ledger.upsertedSlots)
	}
}

// A spare parked IN the dark system is the nearest hull to it there is. Read as
// unreachable it sits idle while a replacement is bought behind the frontier.
func TestAdvanceExpansion_ClaimsASpareParkedInTheTargetToChartIt(t *testing.T) {
	h, memo := frontierWithItsOwnCounter()
	h.ledger.slots = append(h.ledger.slots, QueuedSlot{
		Waypoint: "X1-BU71-Y2", System: "X1-BU71", Kind: SlotKindSpare,
		State: SlotStateParked, AssignedShip: "PROBE-ONSITE",
	})

	rep, err := h.runWithMemo(t, memo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsClaimed != 1 {
		t.Fatalf("SeedsClaimed = %d, want 1 — PROBE-ONSITE is standing in the system that needs charting "+
			"(report %+v)", rep.SeedsClaimed, rep)
	}
	if len(h.ledger.setSeeds) != 1 {
		t.Fatalf("stamped %v, want one errand for X1-BU71", h.ledger.setSeeds)
	}
	stamped := h.ledger.setSeeds[0]
	if stamped.system != "X1-BU71" || stamped.ship != "PROBE-ONSITE" || stamped.state != SeedStateDispatched {
		t.Fatalf("stamped %+v, want PROBE-ONSITE dispatched to chart X1-BU71", stamped)
	}
	if rep.SeedsRequested != 0 {
		t.Fatalf("SeedsRequested = %d, want 0 — the claim covers X1-BU71, so nothing more is bought for it",
			rep.SeedsRequested)
	}
}

// darkCounterFleet is the cold shape of the same argument: NOTHING of ours stands at
// any counter, so the borrow pass is the only way to open one.
//
//	X1-HOME (evidenced counter, empty) ── X1-DARK (uncharted target; evidenced counter, empty)
func darkCounterFleet() (*expandHarness, *fakeListingMemo) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-HOME", Verdict: VerdictInScope},
		{System: "X1-DARK", Verdict: VerdictInScope, UnchartedCount: 5},
	}
	h.gates.adjacency = map[string][]string{
		"X1-HOME": {"X1-DARK"},
		"X1-DARK": {"X1-HOME"},
	}
	h.yards.bySystem = map[string][]string{
		"X1-HOME": {"X1-HOME-YARD"},
		"X1-DARK": {"X1-DARK-YARD"},
	}
	h.ledger.slots = nil
	memo := &fakeListingMemo{
		sells: map[string]bool{"X1-HOME-YARD": true, "X1-DARK-YARD": true},
		scannedAt: map[string]time.Time{
			"X1-HOME-YARD": time.Now(), "X1-DARK-YARD": time.Now(),
		},
	}
	return h, memo
}

// THE LENT PATH, SAME ARGUMENT. The counter this pass opens is the counter a seed
// then gets bought at, so it is staffed where the seed is going. The home hull is
// offered FIRST — LendableHulls' cheapest-sacrifice order — and must lose to it.
func TestAdvanceExpansion_LendsToTheCounterInTheTargetRatherThanOneBehindIt(t *testing.T) {
	h, memo := darkCounterFleet()
	h.ships.lendable = []LendableHull{
		{ShipSymbol: "HULL-HOME", Waypoint: "X1-HOME-A1", System: "X1-HOME"},
		{ShipSymbol: "HULL-DARK", Waypoint: "X1-DARK-A1", System: "X1-DARK"},
	}

	rep, err := h.runWithMemo(t, memo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.CountersStaffed != 1 {
		t.Fatalf("CountersStaffed = %d, want 1 (report %+v)", rep.CountersStaffed, rep)
	}
	navs := h.seed.calls
	if len(navs) != 1 || navs[0].ship != "HULL-DARK" || navs[0].arg != "X1-DARK-YARD" {
		t.Fatalf("commands issued = %v, want one navigate of HULL-DARK to X1-DARK-YARD — staffing "+
			"X1-HOME-YARD instead buys the seed a gate short of the system it is meant to chart", navs)
	}
}

// The self case on its own: the ONLY borrowable hull stands in the target, and the
// target is the only system needing one. Read through the flight walk, no target is
// reachable from where it stands and the fleet lends nothing at all.
func TestAdvanceExpansion_LendsToTheTargetsOwnCounterWhenNothingElseServes(t *testing.T) {
	h, memo := darkCounterFleet()
	h.ships.lendable = []LendableHull{
		{ShipSymbol: "HULL-DARK", Waypoint: "X1-DARK-A1", System: "X1-DARK"},
	}

	rep, err := h.runWithMemo(t, memo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsUnstaged != 1 {
		t.Fatalf("SeedsUnstaged = %d, want 1 — no counter anywhere is staffed yet (report %+v)",
			rep.SeedsUnstaged, rep)
	}
	if rep.CountersStaffed != 1 {
		t.Fatalf("CountersStaffed = %d, want 1 — a hull standing in the target can staff the target's "+
			"own counter, and calling that 'no target served' leaves the system unbuyable forever "+
			"(report %+v)", rep.CountersStaffed, rep)
	}
	navs := h.seed.calls
	if len(navs) != 1 || navs[0].ship != "HULL-DARK" || navs[0].arg != "X1-DARK-YARD" {
		t.Fatalf("commands issued = %v, want one navigate of HULL-DARK to X1-DARK-YARD", navs)
	}
}

// --- the coverage path is not the seed path ----------------------------------

// A MARKET placement is COVERAGE: the hull is wanted at that waypoint and nowhere
// else, so "landed at the placement" is the whole objective and there is no target
// behind it to re-anchor on. The seed-aware ordering lives in stagingYardFor, whose
// only caller is requestSeeds, and the buy queue is unchanged — this is the pin that
// a later re-anchoring of the RANKING would break.
//
// The fixture discriminates: with the ranking anchored on the placement's own system
// the local 30,000 beats 26,000 + 7,900 landed, and with it anchored on X1-CHEAP
// instead the 26,000 counter wins outright.
func TestDrain_ACoverageBuyIsRankedAgainstItsOwnPlacement(t *testing.T) {
	ports, pur, _, _ := procurementPorts(t, 30_000, 26_000, 400_000)

	rep := drainProcurement(t, ports)

	if yard := boughtAt(t, pur, rep); yard != "X1-TGT-Y1" {
		t.Fatalf("bought at %s, want X1-TGT-Y1 — a coverage placement is ranked on what a probe costs "+
			"landed AT IT, and 26,000 a gate away lands at 33,900 (report %+v)", yard, rep)
	}
	if rep.Ferried != 0 {
		t.Fatalf("Ferried = %d, want 0 — the purchase was made in the placement's own system", rep.Ferried)
	}
}
