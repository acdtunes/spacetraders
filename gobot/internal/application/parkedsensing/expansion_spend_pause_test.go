package parkedsensing

import (
	"context"
	"testing"

	expansionCmd "github.com/andrescamacho/spacetraders-go/internal/application/expansion/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// expansion_spend_pause_test.go pins the line ExpandKnobs.SpendEnabled draws through this engine:
// the free half runs, the half that asks another engine to buy does not.
//
// THE PRODUCTION FAILURE IT EXISTS FOR. The switch used to return the tick before its first port
// call, so an operator stopping PROBE PURCHASES also stopped markFrontier — a ledger write off
// adjacency already measured, costing neither a credit nor an API call. Measured live over the three
// hours it was off: new systems priced per hour ran 1, 1, 5 against 20 in the first hour back on,
// while 308 idle probes we already owned stood unused. They were never blocked from moving — the
// dispatch that flies them has never read this knob — they had run out of DESTINATIONS, because the
// pass that names new ones had been switched off alongside the pass that spends.
//
// SO EVERY FIXTURE HERE IS RUN TWICE, ARMED AND THEN PAUSED, on the same data. A guard test that
// only asserts the paused run did nothing proves nothing: the fixture could be inert. The armed run
// is what proves the path was live and the pause is what closed it.

// spendPauseFixture is hot on every gate-based path at once — frontier marking, a charting seed in
// flight, a claimable parked spare, and a fundable seed request — so one pair of runs covers the
// whole split rather than one pass at a time.
//
// X1-FRESH is deliberately ABSENT from the ledger while X1-HOME's gate names it: that is the free
// discovery the pause must preserve. The two dark systems are deliberately TWO, because claimSpares
// runs first and marks its target covered — with only one, requestSeeds would find nothing to do and
// the armed run would fail to prove the spending path was reachable at all.
func spendPauseFixture() *expandHarness {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-HOME", Verdict: VerdictInScope},
		{System: "X1-DARK1", Verdict: VerdictPending, UnchartedCount: 5},
		{System: "X1-DARK2", Verdict: VerdictPending, UnchartedCount: 4},
		// An errand already in flight: the hull is ours, bought, and stamped onto this
		// system by an earlier tick.
		{System: "X1-TOUR", Verdict: VerdictPending, UnchartedCount: 3,
			SeedShip: "PROBE-TOUR", SeedState: SeedStateDispatched},
	}
	h.gates.adjacency = map[string][]string{
		"X1-HOME":  {"X1-DARK1", "X1-DARK2", "X1-FRESH"},
		"X1-DARK1": {"X1-HOME"},
		"X1-DARK2": {"X1-HOME"},
		"X1-TOUR":  {"X1-HOME"},
	}
	h.yards.bySystem = map[string][]string{"X1-HOME": {"X1-HOME-YARD"}}
	h.ledger.slots = []QueuedSlot{
		// A hull standing at the counter, which is the only thing that makes a seed
		// request fundable — the buy queue buys nowhere else.
		{Waypoint: "X1-HOME-YARD", System: "X1-HOME", Kind: SlotKindMarket,
			State: SlotStateParked, AssignedShip: "PROBE-HOME"},
		// A spare parked and idle, claimable onto an errand.
		{Waypoint: "X1-HOME-SP1", System: "X1-HOME", Kind: SlotKindSpare,
			State: SlotStateParked, AssignedShip: "PROBE-SPARE"},
	}
	// Standing in X1-HOME rather than at its target, so the errand's next step is a
	// gate hop: one unambiguous ship command, with no catalog fixture behind it.
	h.ships.positions = map[string]ShipPos{
		"PROBE-TOUR": {Waypoint: "X1-HOME-A1", NavStatus: navigation.NavStatusDocked, Found: true},
	}
	return h
}

// runSpend drives one tick over the fixture with spending armed or paused.
func runSpend(t *testing.T, h *expandHarness, spend bool) (ExpandReport, error) {
	t.Helper()
	for i := range h.ledger.systems {
		h.ledger.systems[i].CatalogKnown = !h.unswept[h.ledger.systems[i].System]
	}
	return AdvanceExpansion(context.Background(), h.ports(), 1, ExpandKnobs{
		SpendEnabled: spend, MinBudgetRate: 0.05, Whitelist: h.whitelist,
	}, 1.0)
}

// --- the regression -----------------------------------------------------------

// THE BEAD'S DEFECT, and the assertion that fails on the code as shipped. A gate neighbour we have
// measured and never evaluated is recorded PENDING with spending paused.
//
// THIS ROW IS THE WHOLE CHAIN'S FIRST LINK, which is why the assertion is placed on it rather than
// further downstream. A PENDING row is what the screening sweep consumes to write MARKET placements,
// and a MARKET placement is what the idle-orphan dispatch flies an already-bought probe to. Neither
// of those two reads this knob — so with the first link restored the rest of the chain runs on hulls
// we already own, for nothing, exactly as the operator asked.
func TestAdvanceExpansion_SpendPaused_StillMarksTheFrontier(t *testing.T) {
	h := spendPauseFixture()

	rep, err := runSpend(t, h, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rep.Discovered != 1 {
		t.Fatalf("Discovered = %d, want 1 — X1-FRESH is a measured gate neighbour with no ledger row, and recording it costs a row write and nothing else", rep.Discovered)
	}
	var pending []string
	for _, record := range h.ledger.upsertedSystem {
		if record.Verdict != VerdictPending {
			t.Fatalf("a paused tick wrote verdict %q for %s — it has judged nothing and must claim to have judged nothing", record.Verdict, record.System)
		}
		pending = append(pending, record.System)
	}
	if len(pending) != 1 || pending[0] != "X1-FRESH" {
		t.Fatalf("recorded frontier %v, want exactly [X1-FRESH]", pending)
	}
}

// --- the money guard ----------------------------------------------------------

// NOTHING BELOW THE PAUSE IS REACHABLE, proven against a fixture the armed run shows is live on
// every one of those paths.
//
// The three assertions are the three ways this engine can reach the treasury, all of them through
// another engine: a SPARE want the buy queue funds by buying a probe, a placement row deleted out
// from under the probe cap (which is headroom, and headroom authorises a purchase), and a hull
// commanded anywhere at all. Under RULINGS #4 the doubt resolves toward not spending, so all three
// sit on the far side of the pause and this is what holds them there.
func TestAdvanceExpansion_SpendPaused_RaisesNoPurchaseIntent(t *testing.T) {
	armed := spendPauseFixture()
	armedRep, err := runSpend(t, armed, true)
	if err != nil {
		t.Fatalf("armed run: unexpected error: %v", err)
	}

	// THE FIXTURE IS LIVE. Without this the pause assertions below could pass on a
	// fixture that never had a spending path to close.
	if armedRep.SeedsRequested != 1 {
		t.Fatalf("armed SeedsRequested = %d, want 1 — the fixture must actually reach the buy queue, or the paused run proves nothing", armedRep.SeedsRequested)
	}
	if armedRep.SeedsClaimed != 1 {
		t.Fatalf("armed SeedsClaimed = %d, want 1 — the fixture must actually claim the parked spare", armedRep.SeedsClaimed)
	}
	if len(armed.seed.calls) == 0 {
		t.Fatal("armed run commanded no hull — the fixture must actually move the errand in flight")
	}

	paused := spendPauseFixture()
	pausedRep, err := runSpend(t, paused, false)
	if err != nil {
		t.Fatalf("paused run: unexpected error: %v", err)
	}

	if !pausedRep.SpendingPaused {
		t.Fatal("SpendingPaused = false on a paused tick")
	}
	// A SPARE want is a purchase order in everything but name: the buy queue drains it
	// by buying a probe.
	if len(paused.ledger.upsertedSlots) != 0 {
		t.Fatalf("a paused tick wrote %d placement row(s): %v — a SPARE want is what the buy queue funds by BUYING A PROBE",
			len(paused.ledger.upsertedSlots), paused.ledger.upsertedSlots)
	}
	if pausedRep.SeedsRequested != 0 || pausedRep.SeedsClaimed != 0 {
		t.Fatalf("paused SeedsRequested=%d SeedsClaimed=%d, want 0/0", pausedRep.SeedsRequested, pausedRep.SeedsClaimed)
	}
	// Deleting a placement row is a deliberate UNDER-count of the probe fleet
	// (claimSpares says so), and an under-count is cap headroom the buy queue spends.
	if len(paused.ledger.deleted) != 0 || len(paused.ledger.setSeeds) != 0 {
		t.Fatalf("a paused tick stamped errands %v and released placements %v — releasing a row drops its hull out of the probe cap, which is headroom the buy queue spends",
			paused.ledger.setSeeds, paused.ledger.deleted)
	}
	if len(paused.seed.calls) != 0 {
		t.Fatalf("a paused tick commanded hulls: %v — flying is how an errand sustains that under-count", paused.seed.verbs())
	}
	if len(paused.ledger.transitions) != 0 {
		t.Fatalf("a paused tick advanced %d placement(s): %v", len(paused.ledger.transitions), paused.ledger.transitions)
	}
}

// THE LATCH, and the reason the pause RETRACTS off-gate demand rather than merely stopping to raise
// it. ExplorerOffGateBridge keeps the last signal it was handed and answers the autosizer from it on
// every read until something overwrites it — so a pause that skipped the emit would leave a
// `Demanded: true` raised seconds before the operator flipped the switch standing indefinitely, and
// the autosizer would buy a 769k explorer against it.
//
// Not emitting is not the same as not demanding, and the difference is a purchase. The sealed pocket
// is the shape that raises the demand in the first place, so the armed run here is what loads the
// latch the paused run must clear.
func TestAdvanceExpansion_SpendPaused_RetractsStandingExplorerDemand(t *testing.T) {
	og := sealedPocket()
	og.explorers.symbol, og.explorers.found = "EXPLORER-1", true

	sink := og.sink
	ports := og.ports()
	ports.OffGate = OffGatePorts{Select: og.selector, Demand: sink, Explorer: og.explorers, Warp: og.warp}
	for i := range og.ledger.systems {
		og.ledger.systems[i].CatalogKnown = !og.unswept[og.ledger.systems[i].System]
	}

	armed, err := AdvanceExpansion(context.Background(), ports, 1, ExpandKnobs{
		SpendEnabled: true, MinBudgetRate: 0.05, Whitelist: og.whitelist,
	}, 1.0)
	if err != nil {
		t.Fatalf("armed run: unexpected error: %v", err)
	}
	if !armed.OffGateDemanded {
		t.Fatal("armed run raised no off-gate demand — the fixture must load the latch, or the retraction below proves nothing")
	}
	if latest, ok := sink.last(); !ok || !latest.Demanded {
		t.Fatalf("armed run left the bridge holding %+v, want a standing demand", latest)
	}

	// The operator flips the switch. The very next tick must clear what is latched.
	paused, err := AdvanceExpansion(context.Background(), ports, 1, ExpandKnobs{
		SpendEnabled: false, MinBudgetRate: 0.05, Whitelist: og.whitelist,
	}, 1.0)
	if err != nil {
		t.Fatalf("paused run: unexpected error: %v", err)
	}
	if paused.OffGateDemanded {
		t.Fatal("a paused tick reported raising off-gate demand")
	}
	latest, ok := sink.last()
	if !ok {
		t.Fatal("a paused tick wrote nothing to the demand bridge — the bridge LATCHES, so silence leaves the previous demand standing and the autosizer buys against it")
	}
	if latest.Demanded || latest.ExplorerCount != 0 {
		t.Fatalf("the bridge still holds %+v after the pause, want a zero signal — the autosizer reads this and buys a 769k explorer", latest)
	}
	if len(og.warp.calls) != 1 {
		t.Fatalf("warp dispatches = %d, want exactly 1 (the armed run's) — a paused tick must not fly the explorer", len(og.warp.calls))
	}
}

// A wiring gap must not be the thing that leaves a demand standing. The retraction needs somewhere
// to write and nothing else, so it is guarded on the sink alone — requiring the selector, finder and
// dispatcher too would reintroduce the latch through the nil-check.
func TestAdvanceExpansion_SpendPaused_RetractsThroughAPartiallyWiredSlice(t *testing.T) {
	h := spendPauseFixture()
	sink := &fakeDemandSink{}
	ports := h.ports()
	ports.OffGate = OffGatePorts{Demand: sink} // Select/Explorer/Warp absent: wired() is false
	for i := range h.ledger.systems {
		h.ledger.systems[i].CatalogKnown = !h.unswept[h.ledger.systems[i].System]
	}

	if _, err := AdvanceExpansion(context.Background(), ports, 1, ExpandKnobs{
		SpendEnabled: false, MinBudgetRate: 0.05, Whitelist: h.whitelist,
	}, 1.0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	latest, ok := sink.last()
	if !ok {
		t.Fatal("no retraction written through a partially wired slice — the sink is all a retraction needs, and gating it on the other three ports is how a latched demand survives a pause")
	}
	if latest != (expansionCmd.OffGateDemandSignal{}) {
		t.Fatalf("retraction wrote %+v, want the zero signal", latest)
	}
}

// --- the free half stays free -------------------------------------------------

// The deliberate gate read runs while paused, and it must: it is what keeps the ADJACENCY STORE
// growing, and markFrontier can only name neighbours the store already holds. Pause the read and the
// discovery half drains within a few ticks and stalls exactly as the whole engine used to — the same
// defect, arrived at one pass later.
//
// It spends no credits (its own contract: "writes nothing, moves nothing and spends nothing"). It
// does spend API budget, which is why the budget floor stays OUTSIDE the pause and is asserted here
// too: an operator's economic choice must not be able to reach past the fleet's own rate-limit
// protection.
func TestAdvanceExpansion_SpendPaused_StillReadsUnmappedGates(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-HOME", Verdict: VerdictInScope},
		{System: "X1-UNREAD", Verdict: VerdictInScope},
	}
	gates := newGateStore()
	gates.stored["X1-HOME"] = []string{"X1-UNREAD"} // read
	gates.live["X1-UNREAD"] = []string{"X1-BEYOND"} // unread, and it names new territory
	for i := range h.ledger.systems {
		h.ledger.systems[i].CatalogKnown = true
	}
	p := h.ports()
	p.Gates = gates
	p.GateRead = gates

	rep, err := AdvanceExpansion(context.Background(), p, 1, ExpandKnobs{
		SpendEnabled: false, MinBudgetRate: 0.05, Whitelist: h.whitelist,
	}, 1.0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.GatesRead != 1 || gates.readCount("X1-UNREAD") != 1 {
		t.Fatalf("GatesRead = %d, reads of X1-UNREAD = %d, want 1/1 — without this pass markFrontier can only re-walk neighbours we already hold",
			rep.GatesRead, gates.readCount("X1-UNREAD"))
	}

	// And the budget floor still outranks the pause in the other direction.
	starved, err := AdvanceExpansion(context.Background(), p, 1, ExpandKnobs{
		SpendEnabled: false, MinBudgetRate: 0.05, Whitelist: h.whitelist,
	}, 0.04)
	if err != nil {
		t.Fatalf("budget-starved run: unexpected error: %v", err)
	}
	if starved.Skipped != "budget" {
		t.Fatalf("Skipped = %q, want \"budget\" — the API-pressure gate is the outermost one, and a spend pause must not reach past it", starved.Skipped)
	}
}

// ARMED IS BYTE-IDENTICAL. The pause is a new branch, not a new behaviour on the old path: with
// spending on, the same fixture produces exactly the report it always did.
func TestAdvanceExpansion_ArmedIsUnchangedByThePause(t *testing.T) {
	h := spendPauseFixture()

	rep, err := runSpend(t, h, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SpendingPaused {
		t.Fatal("SpendingPaused = true on an armed tick")
	}
	if rep.Skipped != "" {
		t.Fatalf("Skipped = %q, want empty", rep.Skipped)
	}
	if rep.Discovered != 1 || rep.SeedsClaimed != 1 || rep.SeedsRequested != 1 || rep.Jumped != 1 {
		t.Fatalf("armed report = discovered %d, claimed %d, requested %d, jumped %d; want 1/1/1/1",
			rep.Discovered, rep.SeedsClaimed, rep.SeedsRequested, rep.Jumped)
	}
}
