package parkedsensing

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
)

// --- fakes -------------------------------------------------------------------
//
// Adversarial by construction, in the same spirit as the buy queue's: every
// fail-closed fake returns its error ALONGSIDE the value that would cause the
// MOST work if the engine read it anyway — a populated neighbour list, a
// non-empty uncharted set, an in-scope verdict. An engine that leaks an error
// therefore does not merely misreport, it commands a hull, and the test catches
// it.
//
// Every fake counts its calls. The gating tests assert those counters are ZERO,
// which is the only way to prove a disabled tick is genuinely free rather than
// merely quiet.

type seedCall struct{ verb, ship, arg string }

type fakeSeedCommander struct {
	calls []seedCall
	// jumpFrom records the hull position each gate-hop step was handed, which
	// is what an implementation uses to tell "move to the gate" from "jump off
	// it".
	jumpFrom []string

	isMarket  map[string]bool
	jumpErr   error
	navErr    error
	chartErr  error
	refresErr error
	marketErr error
	syncErr   error
}

func (f *fakeSeedCommander) JumpTo(_ context.Context, _ int, ship, fromWaypoint, system string) error {
	f.calls = append(f.calls, seedCall{"jump", ship, system})
	f.jumpFrom = append(f.jumpFrom, fromWaypoint)
	return f.jumpErr
}

func (f *fakeSeedCommander) NavigateTo(_ context.Context, _ int, ship, waypoint string) error {
	f.calls = append(f.calls, seedCall{"navigate", ship, waypoint})
	return f.navErr
}

func (f *fakeSeedCommander) Chart(_ context.Context, _ int, ship string) error {
	f.calls = append(f.calls, seedCall{"chart", ship, ""})
	return f.chartErr
}

func (f *fakeSeedCommander) RefreshWaypoint(_ context.Context, _ int, _, waypoint string) (bool, error) {
	f.calls = append(f.calls, seedCall{"refresh", "", waypoint})
	if f.refresErr != nil {
		// Adversarial: "it is a market" is the branch that costs a scan.
		return true, f.refresErr
	}
	return f.isMarket[waypoint], nil
}

func (f *fakeSeedCommander) ReadMarketAt(_ context.Context, _ int, waypoint string) error {
	f.calls = append(f.calls, seedCall{"market", "", waypoint})
	return f.marketErr
}

func (f *fakeSeedCommander) SyncWaypoints(_ context.Context, _ int, system string) error {
	f.calls = append(f.calls, seedCall{"sync", "", system})
	return f.syncErr
}

func (f *fakeSeedCommander) verbs() []string {
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, c.verb)
	}
	return out
}

func (f *fakeSeedCommander) countOf(verb string) int {
	n := 0
	for _, c := range f.calls {
		if c.verb == verb {
			n++
		}
	}
	return n
}

type fakeGates struct {
	adjacency map[string][]string
	err       error
	calls     int
}

func (f *fakeGates) Neighbours(_ context.Context, system string) ([]string, error) {
	f.calls++
	if f.err != nil {
		// Adversarial: a populated frontier alongside the error, so an engine
		// that ignores it walks straight into unfunded expansion.
		return []string{"X1-GHOST"}, f.err
	}
	return f.adjacency[system], nil
}

// Mapped reports every system as ALREADY MAPPED, which keeps every test in this file ordering
// exactly as it did before the gate-priority tier existed. The tier only reorders when some target
// is unmapped, so a uniform "mapped" is the neutral answer; the tests that exercise the tier use
// gateMap in expansion_gate_priority_test.go, which can express both.
func (f *fakeGates) Mapped(_ context.Context, _ string) (bool, error) { return true, nil }

type fakeUncharted struct {
	bySystem map[string][]string
	err      error
	calls    int
}

func (f *fakeUncharted) UnchartedWaypoints(_ context.Context, system string) ([]string, error) {
	f.calls++
	if f.err != nil {
		return []string{system + "-GHOST"}, f.err // adversarial: work to do
	}
	return f.bySystem[system], nil
}

type setSeedCall struct{ system, ship, state string }

type fakeExpandLedger struct {
	systems []ExpandSystem
	slots   []QueuedSlot

	systemsErr, slotsErr           error
	setSeedErr, deleteErr          error
	stampErr                       error
	setSeedErrOn                   map[string]error
	upsertSlotErr, upsertSystemErr error
	transitionErr                  error

	setSeeds       []setSeedCall
	stamped        []string
	deleted        []string
	upsertedSlots  []SlotRecord
	upsertedSystem []SystemRecord
	transitions    []transitionCall

	systemsCalls, slotsCalls int
}

func (f *fakeExpandLedger) Systems(_ context.Context, _ int) ([]ExpandSystem, error) {
	f.systemsCalls++
	if f.systemsErr != nil {
		return f.systems, f.systemsErr // adversarial: rows alongside the error
	}
	return f.systems, nil
}

func (f *fakeExpandLedger) SlotsByState(_ context.Context, _ int, states ...string) ([]QueuedSlot, error) {
	f.slotsCalls++
	if f.slotsErr != nil {
		return nil, f.slotsErr
	}
	want := make(map[string]bool, len(states))
	for _, s := range states {
		want[s] = true
	}
	var out []QueuedSlot
	for _, s := range f.slots {
		if want[s.State] {
			out = append(out, s)
		}
	}
	return out, nil
}

func (f *fakeExpandLedger) UpsertSystem(_ context.Context, _ int, record SystemRecord) error {
	f.upsertedSystem = append(f.upsertedSystem, record)
	if f.upsertSystemErr != nil {
		return f.upsertSystemErr
	}
	f.systems = append(f.systems, ExpandSystem{System: record.System, Verdict: record.Verdict})
	return nil
}

// The two upsert variants model the ledger's COLUMN OWNERSHIP (sp-wgjb7), not
// just its signatures. A declaration cannot overwrite a placement's state or its
// hull, and a stand-down cannot erase the screen's measurements — a fake that
// blanket-wrote the row would hide exactly the double-purchase bug that ownership
// exists to prevent, the same reason UpsertSystem preserves the seed columns.
func (f *fakeExpandLedger) UpsertSlotMetadata(_ context.Context, _ int, slot SlotRecord) error {
	f.upsertedSlots = append(f.upsertedSlots, slot)
	if f.upsertSlotErr != nil {
		return f.upsertSlotErr
	}
	if i := f.slotIndex(slot.Waypoint, slot.Kind); i >= 0 {
		f.slots[i].System = slot.System
		f.slots[i].DepthCredits = slot.DepthCredits
		return nil
	}
	f.slots = append(f.slots, QueuedSlot{
		Waypoint: slot.Waypoint, System: slot.System, Kind: slot.Kind,
		State: slot.State, AssignedShip: slot.AssignedShip, DepthCredits: slot.DepthCredits,
	})
	return nil
}

func (f *fakeExpandLedger) UpsertSpareSlot(_ context.Context, _ int, slot SlotRecord) error {
	f.upsertedSlots = append(f.upsertedSlots, slot)
	if f.upsertSlotErr != nil {
		return f.upsertSlotErr
	}
	if i := f.slotIndex(slot.Waypoint, slot.Kind); i >= 0 {
		f.slots[i].System = slot.System
		f.slots[i].State = slot.State
		f.slots[i].AssignedShip = slot.AssignedShip
		return nil
	}
	f.slots = append(f.slots, QueuedSlot{
		Waypoint: slot.Waypoint, System: slot.System, Kind: slot.Kind,
		State: slot.State, AssignedShip: slot.AssignedShip, DepthCredits: slot.DepthCredits,
	})
	return nil
}

// slotIndex finds an existing placement by (waypoint, KIND) — the real ledger is
// keyed on the pair (sp-dpfp8), so a second write of the SAME KIND at a waypoint
// is a CONFLICT while a write of a different kind is a NEW ROW.
//
// This used to match on the waypoint alone, which made the fake structurally
// unable to hold the co-located pair the whole widening exists to allow: a seed
// staged at a yard that is also a parked market would have been folded into the
// market's row here and every assertion about the two of them would have been
// meaningless. A fake that cannot represent the bug cannot witness the fix.
func (f *fakeExpandLedger) slotIndex(waypoint, kind string) int {
	for i := range f.slots {
		if f.slots[i].Waypoint == waypoint && f.slots[i].Kind == kind {
			return i
		}
	}
	return -1
}

// slotAt reads back one placement, so a test can assert that a write aimed at one
// kind left the other kind's row at the same waypoint alone.
func (f *fakeExpandLedger) slotAt(waypoint, kind string) (QueuedSlot, bool) {
	if i := f.slotIndex(waypoint, kind); i >= 0 {
		return f.slots[i], true
	}
	return QueuedSlot{}, false
}

// SetSeed keys its injected failures on system+hull rather than system alone, so
// a test can break the STAMP of a retarget without also breaking the CLEAR and
// the RESTORE that bracket it — all three touch a seed row, and two of them
// touch the same one.
func (f *fakeExpandLedger) SetSeed(_ context.Context, _ int, system, ship, state string) error {
	f.setSeeds = append(f.setSeeds, setSeedCall{system, ship, state})
	if err := f.setSeedErrOn[system+"/"+ship]; err != nil {
		return err
	}
	if f.setSeedErr != nil {
		return f.setSeedErr
	}
	for i := range f.systems {
		if f.systems[i].System == system {
			f.systems[i].SeedShip, f.systems[i].SeedState = ship, state
			return nil
		}
	}
	f.systems = append(f.systems, ExpandSystem{System: system, SeedShip: ship, SeedState: state})
	return nil
}

func (f *fakeExpandLedger) StampCatalogSynced(_ context.Context, _ int, system string) error {
	f.stamped = append(f.stamped, system)
	if f.stampErr != nil {
		return f.stampErr
	}
	for i := range f.systems {
		if f.systems[i].System == system {
			f.systems[i].CatalogKnown = true
		}
	}
	return nil
}

// DeleteSlot removes the row of THIS KIND, mirroring the real ledger's key
// (sp-dpfp8). Matching on the waypoint alone would delete a co-located MARKET row
// too — the exact money bug the kind argument exists to prevent — and a fake that
// did that would let the bug pass its own test.
func (f *fakeExpandLedger) DeleteSlot(_ context.Context, _ int, waypoint, kind string) error {
	f.deleted = append(f.deleted, waypoint)
	if f.deleteErr != nil {
		return f.deleteErr
	}
	for i := range f.slots {
		if f.slots[i].Waypoint == waypoint && f.slots[i].Kind == kind {
			f.slots = append(f.slots[:i], f.slots[i+1:]...)
			return nil
		}
	}
	return nil
}

func (f *fakeExpandLedger) TransitionSlot(_ context.Context, _ int, waypoint, kind, from, to string, set SlotFields) error {
	f.transitions = append(f.transitions, transitionCall{waypoint, from, to, set.AssignedShip, set.PurchaseYard})
	if f.transitionErr != nil {
		return f.transitionErr
	}
	for i := range f.slots {
		if f.slots[i].Waypoint != waypoint || f.slots[i].Kind != kind {
			continue
		}
		if f.slots[i].State != from {
			return ErrSlotClaimed
		}
		f.slots[i].State = to
		if set.AssignedShip != nil {
			f.slots[i].AssignedShip = *set.AssignedShip
		}
		return nil
	}
	return ErrSlotClaimed
}

type fakeExpandShips struct {
	positions map[string]ShipPos
	// docked maps a waypoint to the probe of ours standing at it, mirroring the
	// ships-table read buyerAt falls back to. Seed staging consults the same
	// question, so it is no longer a stub.
	docked    map[string]string
	dockedErr error
	err       error
	calls     int
}

func (f *fakeExpandShips) DockedProbeAt(_ context.Context, _ int, waypoint string) (string, bool, error) {
	if f.dockedErr != nil {
		// Adversarial: a usable hull alongside the error, so a caller that
		// ignores the error stages a purchase it cannot prove is fundable.
		return "PROBE-GHOST", true, f.dockedErr
	}
	s, ok := f.docked[waypoint]
	return s, ok, nil
}

func (f *fakeExpandShips) ShipAt(_ context.Context, _ int, ship string) (ShipPos, error) {
	f.calls++
	if f.err != nil {
		// Adversarial: a locatable, docked hull alongside the error.
		return ShipPos{Waypoint: "X1-GHOST-A1", NavStatus: navigation.NavStatusDocked, Found: true}, f.err
	}
	return f.positions[ship], nil
}

type fakeExpandYards struct {
	bySystem map[string][]string
	err      error
	calls    int
}

func (f *fakeExpandYards) ListProbeYards(_ context.Context, system string) ([]string, error) {
	f.calls++
	if f.err != nil {
		return []string{system + "-YARD"}, f.err // adversarial: a spendable yard
	}
	return f.bySystem[system], nil
}

type fakeExpandMarkets struct {
	goods map[string][]string
	rows  map[string][]scouting.MarketDepthRow
	calls int
}

func (f *fakeExpandMarkets) GoodsAt(_ context.Context, _ int, waypoint string) ([]string, bool, error) {
	f.calls++
	goods, ok := f.goods[waypoint]
	return goods, ok, nil
}

func (f *fakeExpandMarkets) DepthRowsAt(_ context.Context, _ int, waypoint string) ([]scouting.MarketDepthRow, error) {
	f.calls++
	return f.rows[waypoint], nil
}

// screenStub records the systems it was asked about and replays a canned verdict.
type screenStub struct {
	verdicts map[string]string
	err      error
	asked    []string
}

func (s *screenStub) screener() SystemScreener {
	return func(_ context.Context, system string) (ScreenResult, error) {
		s.asked = append(s.asked, system)
		if s.err != nil {
			// Adversarial: an in-scope verdict alongside the error.
			return ScreenResult{Verdict: VerdictInScope}, s.err
		}
		verdict := s.verdicts[system]
		if verdict == "" {
			verdict = VerdictPending
		}
		return ScreenResult{Verdict: verdict}, nil
	}
}

// --- harness -----------------------------------------------------------------

type expandHarness struct {
	gates     *fakeGates
	ledger    *fakeExpandLedger
	seed      *fakeSeedCommander
	ships     *fakeExpandShips
	yards     *fakeExpandYards
	markets   *fakeExpandMarkets
	screen    *screenStub
	whitelist map[string]bool
	// unswept names the systems whose waypoint catalog has NEVER been swept.
	// Everything else defaults to swept — see run().
	unswept map[string]bool
}

func newExpandHarness() *expandHarness {
	return &expandHarness{
		gates:     &fakeGates{adjacency: map[string][]string{}},
		ledger:    &fakeExpandLedger{},
		seed:      &fakeSeedCommander{isMarket: map[string]bool{}},
		ships:     &fakeExpandShips{positions: map[string]ShipPos{}},
		yards:     &fakeExpandYards{bySystem: map[string][]string{}},
		markets:   &fakeExpandMarkets{goods: map[string][]string{}, rows: map[string][]scouting.MarketDepthRow{}},
		screen:    &screenStub{verdicts: map[string]string{}},
		whitelist: map[string]bool{"FUEL": true},
		unswept:   map[string]bool{},
	}
}

func (h *expandHarness) ports() ExpandPorts {
	return ExpandPorts{
		Gates:       h.gates,
		Ledger:      h.ledger,
		Screen:      h.screen.screener(),
		SeedShip:    h.seed,
		Ships:       h.ships,
		Yards:       h.yards,
		MarketGoods: h.markets,
		Uncharted:   &fakeUncharted{bySystem: map[string][]string{}},
	}
}

// run executes one tick with expansion enabled and a budget comfortably above
// the floor, so only the behaviour under test can hold it.
//
// Every system's waypoint catalog is marked SWEPT unless the test names it in
// `unswept`. That inversion is deliberate: an unswept system is a seed target
// whatever its uncharted count says, so leaving the flag at Go's zero value
// would quietly turn every fixture in the file into one and let unrelated tests
// pass for the wrong reason. Tests about the unswept case opt in by name.
func (h *expandHarness) run(t *testing.T, uncharted *fakeUncharted) (ExpandReport, error) {
	t.Helper()
	for i := range h.ledger.systems {
		h.ledger.systems[i].CatalogKnown = !h.unswept[h.ledger.systems[i].System]
	}
	p := h.ports()
	if uncharted != nil {
		p.Uncharted = uncharted
	}
	return AdvanceExpansion(context.Background(), p, 1, ExpandKnobs{
		Enabled: true, MinBudgetRate: 0.05, Whitelist: h.whitelist,
	}, 1.0)
}

// assertIdle proves the tick touched nothing at all — the only way to show a
// gate is free rather than merely quiet.
func (h *expandHarness) assertIdle(t *testing.T) {
	t.Helper()
	if h.gates.calls != 0 || h.ledger.systemsCalls != 0 || h.ledger.slotsCalls != 0 ||
		h.ships.calls != 0 || h.yards.calls != 0 || h.markets.calls != 0 ||
		len(h.seed.calls) != 0 || len(h.screen.asked) != 0 {
		t.Fatalf("expected a zero-call tick, got gates=%d systems=%d slots=%d ships=%d yards=%d markets=%d seed=%v screens=%v",
			h.gates.calls, h.ledger.systemsCalls, h.ledger.slotsCalls, h.ships.calls,
			h.yards.calls, h.markets.calls, h.seed.verbs(), h.screen.asked)
	}
	if len(h.ledger.setSeeds) != 0 || len(h.ledger.deleted) != 0 ||
		len(h.ledger.upsertedSlots) != 0 || len(h.ledger.upsertedSystem) != 0 ||
		len(h.ledger.transitions) != 0 {
		t.Fatalf("expected a zero-write tick, got seeds=%v deleted=%v slots=%v systems=%v transitions=%v",
			h.ledger.setSeeds, h.ledger.deleted, h.ledger.upsertedSlots,
			h.ledger.upsertedSystem, h.ledger.transitions)
	}
}

// --- gating ------------------------------------------------------------------

func TestAdvanceExpansion_DisabledTickIsAZeroCallNoOp(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{{System: "X1-A", Verdict: VerdictInScope, UnchartedCount: 4}}

	rep, err := AdvanceExpansion(context.Background(), h.ports(), 1, ExpandKnobs{
		Enabled: false, MinBudgetRate: 0.05, Whitelist: h.whitelist,
	}, 1.0)

	if err != nil {
		t.Fatalf("a disabled tick must not error: %v", err)
	}
	if rep.Skipped != "disabled" {
		t.Fatalf("Skipped = %q, want \"disabled\"", rep.Skipped)
	}
	h.assertIdle(t)
}

func TestAdvanceExpansion_BudgetStarvedTickIsAZeroCallNoOp(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{{System: "X1-A", Verdict: VerdictInScope, UnchartedCount: 4}}

	// The brake has pushed the SENSING residual below the expansion floor.
	rep, err := AdvanceExpansion(context.Background(), h.ports(), 1, ExpandKnobs{
		Enabled: true, MinBudgetRate: 0.05, Whitelist: h.whitelist,
	}, 0.04)

	if err != nil {
		t.Fatalf("a budget-starved tick must not error: %v", err)
	}
	if rep.Skipped != "budget" {
		t.Fatalf("Skipped = %q, want \"budget\"", rep.Skipped)
	}
	h.assertIdle(t)
}

func TestAdvanceExpansion_BudgetExactlyAtTheFloorRuns(t *testing.T) {
	h := newExpandHarness()

	rep, err := AdvanceExpansion(context.Background(), h.ports(), 1, ExpandKnobs{
		Enabled: true, MinBudgetRate: 0.05, Whitelist: h.whitelist,
	}, 0.05)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Skipped != "" {
		t.Fatalf("a tick exactly at the floor must run, got Skipped=%q", rep.Skipped)
	}
}

func TestAdvanceExpansion_EmptyWhitelistRefusesTheTick(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{{System: "X1-A", Verdict: VerdictInScope, UnchartedCount: 4}}

	_, err := AdvanceExpansion(context.Background(), h.ports(), 1, ExpandKnobs{
		Enabled: true, MinBudgetRate: 0.05, Whitelist: nil,
	}, 1.0)

	if !errors.Is(err, ErrEmptyWhitelist) {
		t.Fatalf("error = %v, want ErrEmptyWhitelist", err)
	}
	h.assertIdle(t)
}

// --- frontier discovery ------------------------------------------------------

func TestAdvanceExpansion_NeverEvaluatedNeighbourIsMarkedPendingWithNoShipWork(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{{System: "X1-A", Verdict: VerdictInScope}}
	h.gates.adjacency = map[string][]string{"X1-A": {"X1-B", "X1-C"}}

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rep.Discovered != 2 {
		t.Fatalf("Discovered = %d, want 2", rep.Discovered)
	}
	if len(h.ledger.upsertedSystem) != 2 {
		t.Fatalf("wrote %d system rows, want 2: %v", len(h.ledger.upsertedSystem), h.ledger.upsertedSystem)
	}
	for _, record := range h.ledger.upsertedSystem {
		if record.Verdict != VerdictPending {
			t.Fatalf("neighbour %s recorded as %q, want PENDING — the engine must never judge a system it has not screened",
				record.System, record.Verdict)
		}
	}
	if len(h.seed.calls) != 0 {
		t.Fatalf("marking a neighbour PENDING must cost no ship work, got %v", h.seed.verbs())
	}
	if rep.Actions != 0 {
		t.Fatalf("Actions = %d, want 0 — a free ledger write must not consume the per-tick budget", rep.Actions)
	}
}

func TestAdvanceExpansion_AlreadyEvaluatedNeighbourIsNotRewritten(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-A", Verdict: VerdictInScope},
		{System: "X1-B", Verdict: VerdictNoWhitelist},
	}
	h.gates.adjacency = map[string][]string{"X1-A": {"X1-B"}, "X1-B": {"X1-A"}}

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Discovered != 0 || len(h.ledger.upsertedSystem) != 0 {
		t.Fatalf("a system already carrying a verdict must never be rewritten to PENDING, got %v", h.ledger.upsertedSystem)
	}
}

// A PENDING system is the ONLY origin in this fixture, which is the whole point
// of it: under a judged-only origin filter there is nothing here to propagate
// from and the tick discovers nothing. Judging needs screening, screening needs
// charting, and charting is flight-bound — so waiting for a verdict advanced the
// frontier one fully-charted ring at a time. A charted gate is the evidence that
// matters, and it arrives ~50 waypoints sooner.
func TestAdvanceExpansion_PendingSystemWithAChartedGatePropagatesItsNeighbours(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{{System: "X1-PEND", Verdict: VerdictPending}}
	h.gates.adjacency = map[string][]string{"X1-PEND": {"X1-NEW1", "X1-NEW2"}}

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rep.Discovered != 2 {
		t.Fatalf("Discovered = %d, want 2 — a PENDING system whose gate is charted must propagate its neighbours", rep.Discovered)
	}
	got := map[string]string{}
	for _, record := range h.ledger.upsertedSystem {
		got[record.System] = record.Verdict
	}
	for _, want := range []string{"X1-NEW1", "X1-NEW2"} {
		if got[want] != VerdictPending {
			t.Fatalf("neighbour %s landed as %q, want a new PENDING ledger row; wrote %v", want, got[want], h.ledger.upsertedSystem)
		}
	}
	if len(h.seed.calls) != 0 {
		t.Fatalf("propagating from a PENDING system must cost no ship work, got %v", h.seed.verbs())
	}
	if rep.Actions != 0 {
		t.Fatalf("Actions = %d, want 0 — a free ledger write must not consume the per-tick budget", rep.Actions)
	}
}

// The gate store is the gate. Propagation is loosened from JUDGED to
// GATE-ADJACENCY-KNOWN, not to everything: a system whose jump gate we have
// never charted has no stored edges, and must still contribute nothing.
func TestAdvanceExpansion_PendingSystemWithNoChartedGatePropagatesNothing(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{{System: "X1-PEND", Verdict: VerdictPending}}
	h.gates.adjacency = map[string][]string{} // gate not charted: the store knows nothing

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Discovered != 0 || len(h.ledger.upsertedSystem) != 0 {
		t.Fatalf("a system with no measured gate adjacency must propagate nothing, got Discovered=%d %v",
			rep.Discovered, h.ledger.upsertedSystem)
	}
}

// The ordering invariant that keeps the widened frontier from thrashing seed
// targeting: a freshly-discovered system carries an HONEST count of zero — we
// have never looked — and must sort BEHIND every system with measured uncharted
// waypoints, however many of them arrive at once.
//
// The fixture is built so that a symbol-first sort fails it: the unscreened
// arrivals sort FIRST alphabetically and the deepest measured system sorts LAST,
// so only a count-first ordering puts the measured work at the head.
func TestSeedlessTargets_FreshlyDiscoveredSystemsSortBehindMeasuredWork(t *testing.T) {
	targets := seedlessTargets([]ExpandSystem{
		{System: "X1-AAA1", Verdict: VerdictPending, UnchartedCount: 0, CatalogKnown: false},
		{System: "X1-AAA2", Verdict: VerdictPending, UnchartedCount: 0, CatalogKnown: false},
		{System: "X1-MID", Verdict: VerdictInScope, UnchartedCount: 5, CatalogKnown: true},
		{System: "X1-ZZZ", Verdict: VerdictInScope, UnchartedCount: 30, CatalogKnown: true},
	})

	var order []string
	for _, target := range targets {
		order = append(order, target.System)
	}
	want := []string{"X1-ZZZ", "X1-MID", "X1-AAA1", "X1-AAA2"}
	if len(order) != len(want) {
		t.Fatalf("targets = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("targets = %v, want %v — measured uncharted counts must outrank an unscreened system's honest zero", order, want)
		}
	}
}

func TestAdvanceExpansion_UnreadableGateGraphFailsTheTickWithoutCommandingAHull(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{{System: "X1-A", Verdict: VerdictInScope}}
	h.gates.err = errors.New("gate store down")

	_, err := h.run(t, nil)
	if err == nil {
		t.Fatal("an unreadable gate graph must fail the tick")
	}
	if len(h.ledger.upsertedSystem) != 0 {
		t.Fatalf("no frontier may be recorded from an unreadable graph, got %v", h.ledger.upsertedSystem)
	}
	if len(h.seed.calls) != 0 {
		t.Fatalf("no hull may be commanded from an unreadable graph, got %v", h.seed.verbs())
	}
}

// --- seed requests -----------------------------------------------------------

func TestAdvanceExpansion_UnseededSystemEnqueuesOneSpareAtTheYard(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-A", Verdict: VerdictInScope},
		{System: "X1-B", Verdict: VerdictPending, UnchartedCount: 3},
	}
	h.gates.adjacency = map[string][]string{"X1-A": {"X1-B"}, "X1-B": {"X1-A"}}
	h.yards.bySystem = map[string][]string{"X1-A": {"X1-A-YARD"}}
	h.ledger.slots = []QueuedSlot{staffedYardRow("X1-A", "X1-A-YARD")}

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
	if got.Waypoint != "X1-A-YARD" || got.System != "X1-A" {
		t.Fatalf("seed request placed at %s in %s, want X1-A-YARD in X1-A — the buy queue can only fund a yard we already stand at",
			got.Waypoint, got.System)
	}
	if got.Kind != SlotKindSpare || got.State != SlotStateWanted {
		t.Fatalf("seed request recorded as %s/%s, want SPARE/WANTED", got.Kind, got.State)
	}
	if rep.Actions != 1 {
		t.Fatalf("Actions = %d, want 1 — an enqueue spends the per-tick budget", rep.Actions)
	}
}

// A yard that already holds a placement of ANOTHER kind is a perfectly good place
// to stage a seed, and the seed goes there rather than walking on to the next
// yard (sp-dpfp8).
//
// This test used to assert the opposite — that the request skipped to X1-A-YARD2
// — on the grounds that writing over the PARKED slot at X1-A-YARD would drop
// PROBE-1 out of the probe-cap count. That reasoning was sound while a waypoint
// held ONE row: the SPARE want and the YARD placement were the same row, so one
// had to destroy the other. They are now separate rows, so the money guard is
// kept by CONSTRUCTION rather than by avoidance, which is what the second half of
// this test pins: PROBE-1's placement must come through the write untouched.
func TestAdvanceExpansion_SeedRequestStagesAtAYardThatAlreadyHoldsAPlacement(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-A", Verdict: VerdictInScope},
		{System: "X1-B", Verdict: VerdictPending, UnchartedCount: 3},
	}
	h.gates.adjacency = map[string][]string{"X1-A": {"X1-B"}}
	h.yards.bySystem = map[string][]string{"X1-A": {"X1-A-YARD", "X1-A-YARD2"}}
	// The screen already parked a probe on the cheapest yard.
	h.ledger.slots = []QueuedSlot{{
		Waypoint: "X1-A-YARD", System: "X1-A", Kind: SlotKindYard,
		State: SlotStateParked, AssignedShip: "PROBE-1",
	}}

	if _, err := h.run(t, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(h.ledger.upsertedSlots) != 1 {
		t.Fatalf("wrote %d slots, want 1: %v", len(h.ledger.upsertedSlots), h.ledger.upsertedSlots)
	}
	got := h.ledger.upsertedSlots[0]
	if got.Waypoint != "X1-A-YARD" {
		t.Fatalf("seed request landed on %s, want X1-A-YARD — a yard already carrying a placement of another kind is still available to stage a seed",
			got.Waypoint)
	}
	if got.Kind != SlotKindSpare || got.State != SlotStateWanted {
		t.Fatalf("seed request recorded as %s/%s, want SPARE/WANTED", got.Kind, got.State)
	}

	// THE MONEY GUARD, now structural: the incumbent row is a different row, so
	// the write cannot have reached it.
	incumbent, found := h.ledger.slotAt("X1-A-YARD", SlotKindYard)
	if !found {
		t.Fatalf("PROBE-1's YARD placement disappeared; it would drop out of the probe cap and authorise re-buying a hull we own")
	}
	if incumbent.AssignedShip != "PROBE-1" || incumbent.State != SlotStateParked {
		t.Fatalf("PROBE-1's placement was disturbed: %+v", incumbent)
	}
}

// THE PRODUCTION FREEZE, reproduced. The fleet's only probe-selling yards were
// both parked MARKET placements, so under the old key every tick found no free
// yard, wrote no SPARE want, and expansion sat at two charting seeds with no way
// to ever order a third — zero rows in (SPARE, WANTED), permanently.
//
// This test asserted that skip as correct behaviour. It is the bug.
func TestAdvanceExpansion_SeedRequestStagesAtTheOnlyYardEvenWhenItIsAParkedMarket(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-A", Verdict: VerdictInScope},
		{System: "X1-B", Verdict: VerdictPending, UnchartedCount: 3},
	}
	h.gates.adjacency = map[string][]string{"X1-A": {"X1-B"}}
	h.yards.bySystem = map[string][]string{"X1-A": {"X1-A-YARD"}}
	h.ledger.slots = []QueuedSlot{{
		Waypoint: "X1-A-YARD", System: "X1-A", Kind: SlotKindMarket,
		State: SlotStateParked, AssignedShip: "PROBE-1",
	}}

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsRequested != 1 {
		t.Fatalf("SeedsRequested = %d, want 1 — the only yard being a parked market is exactly the live fleet's shape, and it froze expansion outright",
			rep.SeedsRequested)
	}
	if len(h.ledger.upsertedSlots) != 1 {
		t.Fatalf("wrote %d slots, want 1: %v", len(h.ledger.upsertedSlots), h.ledger.upsertedSlots)
	}
	if got := h.ledger.upsertedSlots[0]; got.Waypoint != "X1-A-YARD" || got.Kind != SlotKindSpare {
		t.Fatalf("seed request recorded as %s/%s, want a SPARE at X1-A-YARD", got.Waypoint, got.Kind)
	}

	market, found := h.ledger.slotAt("X1-A-YARD", SlotKindMarket)
	if !found || market.AssignedShip != "PROBE-1" || market.State != SlotStateParked {
		t.Fatalf("the parked market at the same waypoint must be untouched and still scanning, got %+v (found=%v)", market, found)
	}
}

func TestAdvanceExpansion_OutstandingSpareSuppressesAFurtherRequest(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-A", Verdict: VerdictInScope},
		{System: "X1-B", Verdict: VerdictPending, UnchartedCount: 3},
	}
	h.gates.adjacency = map[string][]string{"X1-A": {"X1-B"}}
	h.yards.bySystem = map[string][]string{"X1-A": {"X1-A-YARD", "X1-A-YARD2"}}
	// A seed is already on order for the single frontier system.
	h.ledger.slots = []QueuedSlot{{
		Waypoint: "X1-A-YARD", System: "X1-A", Kind: SlotKindSpare, State: SlotStateQueued,
	}}

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsRequested != 0 || len(h.ledger.upsertedSlots) != 0 {
		t.Fatalf("an in-flight SPARE already covers the one frontier system; requested %d more: %v",
			rep.SeedsRequested, h.ledger.upsertedSlots)
	}
}

func TestAdvanceExpansion_AnUnstageableTargetIsSkippedWithoutBlockingAReachableOne(t *testing.T) {
	// X1-FAR borders nothing of ours, so no seed can be staged for it — and it
	// is the DEEPER unknown, so it is considered first. It must pass by without
	// taking anything from X1-NEAR, which we can reach.
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-A", Verdict: VerdictInScope},
		{System: "X1-FAR", Verdict: VerdictPending, UnchartedCount: 9},
		{System: "X1-NEAR", Verdict: VerdictPending, UnchartedCount: 2},
	}
	h.gates.adjacency = map[string][]string{"X1-A": {"X1-NEAR"}}
	h.yards.bySystem = map[string][]string{"X1-A": {"X1-A-YARD"}}
	h.ledger.slots = []QueuedSlot{staffedYardRow("X1-A", "X1-A-YARD")}

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsRequested != 1 {
		t.Fatalf("SeedsRequested = %d, want 1 — the reachable target must still get its seed", rep.SeedsRequested)
	}
	if len(h.ledger.upsertedSlots) != 1 || h.ledger.upsertedSlots[0].Waypoint != "X1-A-YARD" {
		t.Fatalf("slots = %v, want one staged at X1-A-YARD", h.ledger.upsertedSlots)
	}
}

func TestAdvanceExpansion_ASpareOnlySuppressesDemandItCouldActuallyServe(t *testing.T) {
	// Two frontier systems in different neighbourhoods, one spare on order. It
	// borders X1-N1 only, so it answers X1-N1's demand and leaves X1-N2's
	// standing. A blanket count of SPARE rows would read one spare as covering
	// both and stall the second frontier indefinitely.
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-A", Verdict: VerdictInScope},
		{System: "X1-B", Verdict: VerdictInScope},
		{System: "X1-N1", Verdict: VerdictPending, UnchartedCount: 5},
		{System: "X1-N2", Verdict: VerdictPending, UnchartedCount: 4},
	}
	h.gates.adjacency = map[string][]string{"X1-A": {"X1-N1"}, "X1-B": {"X1-N2"}}
	h.yards.bySystem = map[string][]string{"X1-A": {"X1-A-YARD"}, "X1-B": {"X1-B-YARD"}}
	h.ledger.slots = []QueuedSlot{
		{Waypoint: "X1-A-YARD", System: "X1-A", Kind: SlotKindSpare, State: SlotStateQueued},
		staffedYardRow("X1-A", "X1-A-YARD"),
		staffedYardRow("X1-B", "X1-B-YARD"),
	}

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsRequested != 1 {
		t.Fatalf("SeedsRequested = %d, want 1 — X1-N1 is covered, X1-N2 is not", rep.SeedsRequested)
	}
	if len(h.ledger.upsertedSlots) != 1 || h.ledger.upsertedSlots[0].Waypoint != "X1-B-YARD" {
		t.Fatalf("slots = %v, want the seed staged for X1-N2 at X1-B-YARD", h.ledger.upsertedSlots)
	}
}

func TestAdvanceExpansion_AStaleUnreachableSpareNeverStallsExpansion(t *testing.T) {
	// The residue cases the ledger accumulates: a placement reverted to WANTED
	// with no hull by a spare re-task, and a claim left QUEUED by a purchase the
	// treasury refused. Neither borders the frontier, so neither is a seed for
	// it — and nothing will ever make one, since the buy queue's re-task only
	// looks within a single system. Counted bluntly, either would stall
	// expansion permanently.
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-A", Verdict: VerdictInScope},
		{System: "X1-OLD", Verdict: VerdictInScope},
		{System: "X1-B", Verdict: VerdictPending, UnchartedCount: 3},
	}
	h.gates.adjacency = map[string][]string{"X1-A": {"X1-B"}, "X1-OLD": {}}
	h.yards.bySystem = map[string][]string{"X1-A": {"X1-A-YARD"}}
	h.ledger.slots = []QueuedSlot{
		{Waypoint: "X1-OLD-Y1", System: "X1-OLD", Kind: SlotKindSpare, State: SlotStateWanted},
		{Waypoint: "X1-OLD-Y2", System: "X1-OLD", Kind: SlotKindSpare, State: SlotStateQueued},
		staffedYardRow("X1-A", "X1-A-YARD"),
	}

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsRequested != 1 {
		t.Fatalf("SeedsRequested = %d, want 1 — stale rows in X1-OLD cannot serve X1-B", rep.SeedsRequested)
	}
}

func TestAdvanceExpansion_YardsAreResolvedOncePerOriginSystem(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-A", Verdict: VerdictInScope},
		{System: "X1-B", Verdict: VerdictPending, UnchartedCount: 5},
		{System: "X1-C", Verdict: VerdictPending, UnchartedCount: 4},
		{System: "X1-D", Verdict: VerdictPending, UnchartedCount: 3},
	}
	h.gates.adjacency = map[string][]string{"X1-A": {"X1-B", "X1-C", "X1-D"}}
	h.yards.bySystem = map[string][]string{"X1-A": {"X1-A-YARD", "X1-A-YARD2", "X1-A-YARD3"}}

	if _, err := h.run(t, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.yards.calls != 1 {
		t.Fatalf("listed yards %d times, want 1 — three targets bordering one system must not re-read it three times",
			h.yards.calls)
	}
}

func TestAdvanceExpansion_SystemWithAnActiveSeedNeedsNoFurtherRequest(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-A", Verdict: VerdictInScope},
		{System: "X1-B", Verdict: VerdictPending, UnchartedCount: 3,
			SeedShip: "PROBE-9", SeedState: SeedStateCharting},
	}
	h.gates.adjacency = map[string][]string{"X1-A": {"X1-B"}}
	h.yards.bySystem = map[string][]string{"X1-A": {"X1-A-YARD"}}
	h.ships.positions = map[string]ShipPos{
		"PROBE-9": {Waypoint: "X1-B-A1", NavStatus: navigation.NavStatusDocked, Found: true},
	}

	rep, err := h.run(t, &fakeUncharted{bySystem: map[string][]string{"X1-B": {"X1-B-A1"}}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsRequested != 0 {
		t.Fatalf("SeedsRequested = %d, want 0 — X1-B already has a hull on the errand", rep.SeedsRequested)
	}
}

// --- claiming a parked spare -------------------------------------------------

func TestAdvanceExpansion_ParkedSpareIsClaimedAsASeedWithoutBuying(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-A", Verdict: VerdictInScope},
		{System: "X1-B", Verdict: VerdictPending, UnchartedCount: 3},
	}
	h.gates.adjacency = map[string][]string{"X1-A": {"X1-B"}}
	h.yards.bySystem = map[string][]string{"X1-A": {"X1-A-YARD"}}
	h.ledger.slots = []QueuedSlot{{
		Waypoint: "X1-A-YARD", System: "X1-A", Kind: SlotKindSpare,
		State: SlotStateParked, AssignedShip: "PROBE-7",
	}}

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rep.SeedsClaimed != 1 {
		t.Fatalf("SeedsClaimed = %d, want 1", rep.SeedsClaimed)
	}
	if len(h.ledger.setSeeds) != 1 || h.ledger.setSeeds[0] != (setSeedCall{"X1-B", "PROBE-7", SeedStateDispatched}) {
		t.Fatalf("seed writes = %v, want one DISPATCHED mission for PROBE-7 on X1-B", h.ledger.setSeeds)
	}
	if len(h.ledger.deleted) != 1 || h.ledger.deleted[0] != "X1-A-YARD" {
		t.Fatalf("deleted = %v, want the SPARE row released to the mission", h.ledger.deleted)
	}
	if len(h.ledger.upsertedSlots) != 0 {
		t.Fatalf("claiming a spare must never order another probe, got %v", h.ledger.upsertedSlots)
	}
}

func TestAdvanceExpansion_SeedMissionIsStampedBeforeTheSpareRowIsReleased(t *testing.T) {
	// The two writes name one hull. If the row were deleted first, a failure
	// between them would leave the hull named by NOTHING — the probe cap would
	// read the fleet as smaller than it is and authorise buying a replacement
	// for a probe we already own. Stamping first can only over-count.
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-A", Verdict: VerdictInScope},
		{System: "X1-B", Verdict: VerdictPending, UnchartedCount: 3},
	}
	h.gates.adjacency = map[string][]string{"X1-A": {"X1-B"}}
	h.ledger.slots = []QueuedSlot{{
		Waypoint: "X1-A-YARD", System: "X1-A", Kind: SlotKindSpare,
		State: SlotStateParked, AssignedShip: "PROBE-7",
	}}
	h.ledger.deleteErr = errors.New("ledger refusing writes")

	_, err := h.run(t, nil)
	if err == nil {
		t.Fatal("a failed release must surface loudly, not be swallowed")
	}
	if len(h.ledger.setSeeds) != 1 {
		t.Fatalf("the mission must already be stamped when the release fails, got %v", h.ledger.setSeeds)
	}
}

func TestAdvanceExpansion_SpareIsOnlyClaimedFromASystemAdjacentToTheTarget(t *testing.T) {
	// A seed reaches its target with ONE gate hop, so a spare parked somewhere
	// that does not border the frontier system cannot serve it.
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-A", Verdict: VerdictInScope},
		{System: "X1-FAR", Verdict: VerdictInScope},
		{System: "X1-B", Verdict: VerdictPending, UnchartedCount: 3},
	}
	h.gates.adjacency = map[string][]string{"X1-A": {"X1-B"}, "X1-FAR": {}}
	h.yards.bySystem = map[string][]string{"X1-A": {"X1-A-YARD"}}
	h.ledger.slots = []QueuedSlot{
		{Waypoint: "X1-FAR-YARD", System: "X1-FAR", Kind: SlotKindSpare,
			State: SlotStateParked, AssignedShip: "PROBE-FAR"},
		staffedYardRow("X1-A", "X1-A-YARD"),
	}

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsClaimed != 0 {
		t.Fatalf("SeedsClaimed = %d, want 0 — X1-FAR does not border X1-B", rep.SeedsClaimed)
	}
	if len(h.ledger.deleted) != 0 {
		t.Fatalf("no spare row may be released, got %v", h.ledger.deleted)
	}
	// And it must not suppress the demand it cannot serve either: a spare that
	// is unreachable for this target is not a seed for it, and nothing will ever
	// turn it into one, so leaving X1-B unserved would stall expansion forever.
	if rep.SeedsRequested != 1 {
		t.Fatalf("SeedsRequested = %d, want 1 — an unusable spare must not stand in for a real one", rep.SeedsRequested)
	}
}

// --- one hull, one errand, across ticks --------------------------------------
//
// Both tests below drive CONSECUTIVE ticks against the same ledger, because a
// single-tick test cannot reach this class of bug at all: claimSpares consumes
// the spare from the book, so one tick can only ever claim it once. The
// duplication happens on the LEDGER ROUND TRIP, when the next tick rebuilds the
// book from rows that still name a hull already out on an errand.

// seedSystemsOf lists the systems whose row names this hull, which is the shape
// the bug takes: not a bad write, but the same good write repeated onto system
// after system.
func seedSystemsOf(systems []ExpandSystem, hull string) []string {
	var out []string
	for _, s := range systems {
		if s.SeedShip == hull {
			out = append(out, s.System)
		}
	}
	return out
}

func TestAdvanceExpansion_AReParkedHullOnAnErrandIsNotClaimedAgainNextTick(t *testing.T) {
	// THE FOUR-SYSTEM SEED, from live production. One probe was stamped onto four
	// systems roughly thirty seconds apart — one per tick — while a second, idle
	// probe sat parked and unclaimed, because those systems now looked covered.
	//
	// The claim itself is correct: it stamps the errand and deletes the placement
	// row. What brings the row back is PROBE ADOPTION, which indexes hulls by
	// placement row alone and never reads the seed columns. A hull that has not
	// physically departed yet — the mission so far is only a ledger stamp — still
	// looks like an unrecorded probe standing at a waypoint, so adoption writes it
	// a fresh SPARE/PARKED row. The next tick reads that row back and claims the
	// same hull for the next uncovered system, and so on until it finally leaves.
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-HOME", Verdict: VerdictInScope},
		{System: "X1-T1", Verdict: VerdictPending, UnchartedCount: 9},
		{System: "X1-T2", Verdict: VerdictPending, UnchartedCount: 5},
	}
	h.gates.adjacency = map[string][]string{"X1-HOME": {"X1-T1", "X1-T2"}}
	h.ledger.slots = []QueuedSlot{{
		Waypoint: "X1-HOME-I53", System: "X1-HOME", Kind: SlotKindSpare,
		State: SlotStateParked, AssignedShip: "PROBE-18",
	}}
	// The hull is deliberately absent from the ships table, so nothing here turns
	// on where it is. hullsOnErrand reads the SYSTEM rows, never the ship rows —
	// a claim must be refused because the ledger says the hull is busy, not
	// because the fleet happens to report it in flight.

	// Tick one: the deepest-dark target takes the only spare there is.
	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("tick 1: unexpected error: %v", err)
	}
	if rep.SeedsClaimed != 1 || len(seedSystemsOf(h.ledger.systems, "PROBE-18")) != 1 {
		t.Fatalf("tick 1: SeedsClaimed=%d, PROBE-18 on %v, want one claim on X1-T1",
			rep.SeedsClaimed, seedSystemsOf(h.ledger.systems, "PROBE-18"))
	}
	if len(h.ledger.deleted) != 1 || h.ledger.deleted[0] != "X1-HOME-I53" {
		t.Fatalf("tick 1: deleted = %v, want the spare row released to the mission", h.ledger.deleted)
	}

	// BETWEEN TICKS: adoption re-parks the hull it can no longer account for, and
	// a genuinely idle probe is adopted alongside it. This is the ledger the next
	// tick actually reads — the resurrected row FIRST, so a book that still
	// trusts it picks PROBE-18 over the hull that is really free.
	h.ledger.slots = []QueuedSlot{
		{Waypoint: "X1-HOME-I53", System: "X1-HOME", Kind: SlotKindSpare,
			State: SlotStateParked, AssignedShip: "PROBE-18"},
		{Waypoint: "X1-HOME-J55", System: "X1-HOME", Kind: SlotKindSpare,
			State: SlotStateParked, AssignedShip: "PROBE-19"},
	}
	h.ledger.deleted = nil

	rep, err = h.run(t, nil)
	if err != nil {
		t.Fatalf("tick 2: unexpected error: %v", err)
	}

	if got := seedSystemsOf(h.ledger.systems, "PROBE-18"); len(got) != 1 {
		t.Fatalf("PROBE-18 is on the errand for %v — a probe can only fly one mission, "+
			"and every extra system it names is a system nothing is actually charting", got)
	}
	// The second half of the damage, and the reason this is a stall and not just
	// bad bookkeeping: the phantom errands mark their systems covered, so the
	// hull that could have served them is never asked.
	if got := seedSystemsOf(h.ledger.systems, "PROBE-19"); len(got) != 1 || got[0] != "X1-T2" {
		t.Fatalf("PROBE-19 is on the errand for %v, want exactly X1-T2 — the idle hull must take the uncovered target", got)
	}
	if rep.SeedsClaimed != 1 {
		t.Fatalf("tick 2: SeedsClaimed = %d, want 1 — one target left, one free hull", rep.SeedsClaimed)
	}
	if len(h.ledger.deleted) != 1 || h.ledger.deleted[0] != "X1-HOME-J55" {
		t.Fatalf("tick 2: deleted = %v, want only the FREE hull's row released", h.ledger.deleted)
	}
}

func TestAdvanceExpansion_AClaimStrandedByAFailedReleaseIsNotRepeatedNextTick(t *testing.T) {
	// The claim's write order leaves a deliberate window: the errand is stamped
	// first and the row released second, so a failure between them leaves one hull
	// named by both. That direction is chosen on purpose — it over-counts, and an
	// over-count only ever buys FEWER probes — but it is only tolerable because it
	// is TRANSIENT, and it is transient only if the next tick declines to claim
	// the hull a second time. Nothing else unwinds it.
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-HOME", Verdict: VerdictInScope},
		{System: "X1-T1", Verdict: VerdictPending, UnchartedCount: 9},
		{System: "X1-T2", Verdict: VerdictPending, UnchartedCount: 5},
	}
	h.gates.adjacency = map[string][]string{"X1-HOME": {"X1-T1", "X1-T2"}}
	h.ledger.slots = []QueuedSlot{{
		Waypoint: "X1-HOME-I53", System: "X1-HOME", Kind: SlotKindSpare,
		State: SlotStateParked, AssignedShip: "PROBE-18",
	}}
	h.ledger.deleteErr = errors.New("ledger refusing writes")

	if _, err := h.run(t, nil); err == nil {
		t.Fatal("tick 1: a failed release must surface loudly, not be swallowed")
	}
	if got := seedSystemsOf(h.ledger.systems, "PROBE-18"); len(got) != 1 {
		t.Fatalf("tick 1: PROBE-18 on %v, want the errand already stamped when the release failed", got)
	}

	// The row survived the failure, exactly as the write order intends.
	h.ledger.deleteErr = nil
	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("tick 2: unexpected error: %v", err)
	}

	if got := seedSystemsOf(h.ledger.systems, "PROBE-18"); len(got) != 1 {
		t.Fatalf("PROBE-18 is on the errand for %v — the stranded row must not be re-claimed, "+
			"or the safe half of the write order becomes a permanent duplicate-mission loop", got)
	}
	if rep.SeedsClaimed != 0 {
		t.Fatalf("tick 2: SeedsClaimed = %d, want 0 — the only hull in the book is already flying", rep.SeedsClaimed)
	}
}

// --- seed lifecycle ----------------------------------------------------------

func TestAdvanceExpansion_DispatchedSeedJumpsTowardItsTarget(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-B", Verdict: VerdictPending, UnchartedCount: 3,
			SeedShip: "PROBE-7", SeedState: SeedStateDispatched},
	}
	h.ships.positions = map[string]ShipPos{
		"PROBE-7": {Waypoint: "X1-A-YARD", NavStatus: navigation.NavStatusDocked, Found: true},
	}

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := h.seed.calls; len(got) != 1 || got[0] != (seedCall{"jump", "PROBE-7", "X1-B"}) {
		t.Fatalf("seed commands = %v, want exactly one jump to X1-B", got)
	}
	if rep.Jumped != 1 || rep.Actions != 1 {
		t.Fatalf("Jumped=%d Actions=%d, want 1/1", rep.Jumped, rep.Actions)
	}
	if len(h.ledger.setSeeds) != 0 {
		t.Fatalf("the state advances only on the SHIP ROW showing arrival, not on the command returning: %v", h.ledger.setSeeds)
	}
}

// TestAdvanceExpansion_DispatchedSeedHandsTheHopItsOwnPosition pins the argument
// the gate hop turns on.
//
// A gate crossing is two moves — onto the gate, then off it — and only the hull's
// own waypoint separates them. The implementation compares it against the gate
// symbol, so handing down the wrong waypoint (or, worse, leaving it to be guessed
// from distance, which orbitals make ambiguous) puts the jump back on the branch
// that flies the hull there and waits. Nothing else in this file would catch it:
// every other assertion here is satisfied by a jump command being issued at all.
func TestAdvanceExpansion_DispatchedSeedHandsTheHopItsOwnPosition(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-B", Verdict: VerdictPending, UnchartedCount: 3,
			SeedShip: "PROBE-7", SeedState: SeedStateDispatched},
	}
	h.ships.positions = map[string]ShipPos{
		"PROBE-7": {Waypoint: "X1-A-YARD", NavStatus: navigation.NavStatusDocked, Found: true},
	}

	if _, err := h.run(t, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := h.seed.jumpFrom; len(got) != 1 || got[0] != "X1-A-YARD" {
		t.Fatalf("the gate hop was handed %v, want exactly the hull's own waypoint [X1-A-YARD] — "+
			"without it the step cannot tell 'move onto the gate' from 'jump off it'", got)
	}
}

func TestAdvanceExpansion_DispatchedSeedSweepsTheCatalogThenFlipsToCharting(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-B", Verdict: VerdictPending, UnchartedCount: 3,
			SeedShip: "PROBE-7", SeedState: SeedStateDispatched},
	}
	h.ships.positions = map[string]ShipPos{
		"PROBE-7": {Waypoint: "X1-B-A1", NavStatus: navigation.NavStatusInOrbit, Found: true},
	}

	rep, err := h.run(t, &fakeUncharted{bySystem: map[string][]string{"X1-B": {"X1-B-A1"}}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := h.seed.verbs(); len(got) != 1 || got[0] != "sync" {
		t.Fatalf("seed commands = %v, want the waypoint catalog swept on arrival and nothing else", got)
	}
	if len(h.ledger.stamped) != 1 || h.ledger.stamped[0] != "X1-B" {
		t.Fatalf("stamped = %v, want the swept catalog recorded", h.ledger.stamped)
	}
	if len(h.ledger.setSeeds) != 1 || h.ledger.setSeeds[0] != (setSeedCall{"X1-B", "PROBE-7", SeedStateCharting}) {
		t.Fatalf("seed writes = %v, want the mission advanced to CHARTING", h.ledger.setSeeds)
	}
	if rep.Actions != 1 {
		t.Fatalf("Actions = %d, want 1 — the sweep and the flip are ONE step, however many pages the sweep walks",
			rep.Actions)
	}
}

func TestAdvanceExpansion_ArrivalSweepsBeforeTheTourCanReadAnEmptyUnchartedSet(t *testing.T) {
	// The bounce-off this ordering exists to prevent. A system nobody has
	// visited has NO waypoint rows, so its uncharted set is empty — not because
	// it is charted but because we have never asked. A seed that started
	// charting before the sweep would read that empty set, conclude the tour was
	// over, and stand itself down having charted precisely nothing.
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-B", Verdict: VerdictPending,
			SeedShip: "PROBE-7", SeedState: SeedStateDispatched},
	}
	h.ships.positions = map[string]ShipPos{
		"PROBE-7": {Waypoint: "X1-B-A1", NavStatus: navigation.NavStatusDocked, Found: true},
	}

	// The catalog is empty — exactly what an unswept system looks like.
	rep, err := h.run(t, &fakeUncharted{bySystem: map[string][]string{}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(h.screen.asked) != 0 {
		t.Fatalf("the tour must not be finished on arrival, got screens=%v", h.screen.asked)
	}
	if rep.Parked != 0 || rep.Retargeted != 0 {
		t.Fatalf("Parked=%d Retargeted=%d, want 0/0 — nothing was charted yet", rep.Parked, rep.Retargeted)
	}
	if got := h.seed.verbs(); len(got) != 1 || got[0] != "sync" {
		t.Fatalf("seed commands = %v, want the sweep", got)
	}
	if len(h.ledger.setSeeds) != 1 || h.ledger.setSeeds[0].state != SeedStateCharting {
		t.Fatalf("seed writes = %v, want the tour STARTED, not ended", h.ledger.setSeeds)
	}
}

func TestAdvanceExpansion_AFailedSweepLeavesTheSeedDispatchedAndUnstamped(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-B", Verdict: VerdictPending,
			SeedShip: "PROBE-7", SeedState: SeedStateDispatched},
	}
	h.ships.positions = map[string]ShipPos{
		"PROBE-7": {Waypoint: "X1-B-A1", NavStatus: navigation.NavStatusDocked, Found: true},
	}
	h.seed.syncErr = errors.New("waypoint list unavailable")

	if _, err := h.run(t, nil); err != nil {
		t.Fatalf("one failed sweep must not fail the tick: %v", err)
	}
	if len(h.ledger.stamped) != 0 {
		t.Fatalf("a catalog we do not have must never be stamped as synced, got %v", h.ledger.stamped)
	}
	if len(h.ledger.setSeeds) != 0 {
		t.Fatalf("the seed must stay DISPATCHED for the next tick to retry, got %v", h.ledger.setSeeds)
	}
}

func TestAdvanceExpansion_ANeverSweptSystemIsASeedTargetDespiteZeroUncharted(t *testing.T) {
	// An unswept system honestly reports ZERO uncharted waypoints, because we
	// have never looked. A count-only rule would leave the entire population of
	// genuinely unexplored systems permanently invisible to expansion — which is
	// the population it exists to reach.
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-A", Verdict: VerdictInScope},
		{System: "X1-B", Verdict: VerdictPending, UnchartedCount: 0},
	}
	h.unswept = map[string]bool{"X1-B": true}
	h.gates.adjacency = map[string][]string{"X1-A": {"X1-B"}}
	h.yards.bySystem = map[string][]string{"X1-A": {"X1-A-YARD"}}
	h.ledger.slots = []QueuedSlot{staffedYardRow("X1-A", "X1-A-YARD")}

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsRequested != 1 {
		t.Fatalf("SeedsRequested = %d, want 1 — a system we have never looked at needs a seed", rep.SeedsRequested)
	}
}

func TestAdvanceExpansion_ASweptSystemWithNothingLeftIsNotASeedTarget(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-A", Verdict: VerdictInScope},
		{System: "X1-B", Verdict: VerdictNoWhitelist, UnchartedCount: 0},
	}
	h.gates.adjacency = map[string][]string{"X1-A": {"X1-B"}, "X1-B": {}}
	h.yards.bySystem = map[string][]string{"X1-A": {"X1-A-YARD"}}

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsRequested != 0 {
		t.Fatalf("SeedsRequested = %d, want 0 — X1-B is swept and charted through", rep.SeedsRequested)
	}
}

func TestAdvanceExpansion_MerelyUnsweptRanksBelowMeasuredDarkness(t *testing.T) {
	// Deepest-dark-first ordering runs on the HONEST count. An unswept system
	// might hold thirty uncharted waypoints or none, and ranking that guess
	// above a measured thirty would be inventing evidence — so it sorts last and
	// the tick's single seed goes to the system we can actually size.
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-A", Verdict: VerdictInScope},
		{System: "X1-DARK", Verdict: VerdictPending, UnchartedCount: 7},
		{System: "X1-UNKNOWN", Verdict: VerdictPending, UnchartedCount: 0},
	}
	h.unswept = map[string]bool{"X1-UNKNOWN": true}
	h.gates.adjacency = map[string][]string{"X1-A": {"X1-DARK", "X1-UNKNOWN"}}
	h.yards.bySystem = map[string][]string{"X1-A": {"X1-A-YARD"}}
	h.ledger.slots = []QueuedSlot{{
		Waypoint: "X1-A-Y2", System: "X1-A", Kind: SlotKindSpare,
		State: SlotStateParked, AssignedShip: "PROBE-SPARE",
	}}

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsClaimed != 1 {
		t.Fatalf("SeedsClaimed = %d, want 1", rep.SeedsClaimed)
	}
	if len(h.ledger.setSeeds) != 1 || h.ledger.setSeeds[0].system != "X1-DARK" {
		t.Fatalf("seed writes = %v, want the measured-dark system served first", h.ledger.setSeeds)
	}
}

func TestAdvanceExpansion_InTransitSeedIsLeftAlone(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-B", Verdict: VerdictPending, UnchartedCount: 3,
			SeedShip: "PROBE-7", SeedState: SeedStateDispatched},
	}
	h.ships.positions = map[string]ShipPos{
		"PROBE-7": {Waypoint: "X1-A-YARD", NavStatus: navigation.NavStatusInTransit, Found: true},
	}

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(h.seed.calls) != 0 {
		t.Fatalf("a hull already flying must not be commanded again, got %v", h.seed.verbs())
	}
	if rep.Actions != 0 {
		t.Fatalf("Actions = %d, want 0", rep.Actions)
	}
}

func TestAdvanceExpansion_UnlocatableSeedIsNeverCommanded(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-B", Verdict: VerdictPending, UnchartedCount: 3,
			SeedShip: "PROBE-GHOST", SeedState: SeedStateDispatched},
	}
	// positions is empty: the ships table does not know this hull.

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("an absent ship row is an answer, not a tick failure: %v", err)
	}
	if len(h.seed.calls) != 0 {
		t.Fatalf("a hull we cannot locate must never be commanded, got %v", h.seed.verbs())
	}
	if rep.Actions != 0 {
		t.Fatalf("Actions = %d, want 0", rep.Actions)
	}
}

func TestAdvanceExpansion_ChartingSeedNavigatesInTheOrderTheCatalogGives(t *testing.T) {
	// The catalog returns uncharted waypoints in VISIT order, which is what lets
	// an adapter shorten the tour by ordering them by proximity. The engine must
	// take the head of that list rather than re-sorting it and throwing the
	// ordering away.
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-B", Verdict: VerdictPending, UnchartedCount: 2,
			SeedShip: "PROBE-7", SeedState: SeedStateCharting},
	}
	h.ships.positions = map[string]ShipPos{
		"PROBE-7": {Waypoint: "X1-B-GATE", NavStatus: navigation.NavStatusInOrbit, Found: true},
	}

	rep, err := h.run(t, &fakeUncharted{bySystem: map[string][]string{"X1-B": {"X1-B-C3", "X1-B-A1"}}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := h.seed.calls; len(got) != 1 || got[0] != (seedCall{"navigate", "PROBE-7", "X1-B-C3"}) {
		t.Fatalf("seed commands = %v, want one navigate to the head of the catalog's order", got)
	}
	if rep.Navigated != 1 || rep.Actions != 1 {
		t.Fatalf("Navigated=%d Actions=%d, want 1/1", rep.Navigated, rep.Actions)
	}
}

func TestAdvanceExpansion_ChartingSeedChartsWhereItStandsAndRecordsARevealedMarket(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-B", Verdict: VerdictPending, UnchartedCount: 2,
			SeedShip: "PROBE-7", SeedState: SeedStateCharting},
	}
	h.ships.positions = map[string]ShipPos{
		"PROBE-7": {Waypoint: "X1-B-A1", NavStatus: navigation.NavStatusDocked, Found: true},
	}
	h.seed.isMarket = map[string]bool{"X1-B-A1": true}
	h.markets.goods = map[string][]string{"X1-B-A1": {"FUEL", "PLATINUM"}}
	h.markets.rows = map[string][]scouting.MarketDepthRow{
		"X1-B-A1": {{Good: "FUEL", TradeVolume: 10, MidPrice: 30}, {Good: "PLATINUM", TradeVolume: 5, MidPrice: 900}},
	}

	rep, err := h.run(t, &fakeUncharted{bySystem: map[string][]string{"X1-B": {"X1-B-A1", "X1-B-B2"}}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := h.seed.verbs(); len(got) != 3 ||
		got[0] != "chart" || got[1] != "refresh" || got[2] != "market" {
		t.Fatalf("seed commands = %v, want chart → refresh → market", got)
	}
	if rep.Charted != 1 || rep.MarketsFound != 1 {
		t.Fatalf("Charted=%d MarketsFound=%d, want 1/1", rep.Charted, rep.MarketsFound)
	}
	if rep.Actions != 1 {
		t.Fatalf("Actions = %d, want 1 — the chart-and-read bundle is ONE step", rep.Actions)
	}

	if len(h.ledger.upsertedSlots) != 1 {
		t.Fatalf("wrote %d slots, want 1: %v", len(h.ledger.upsertedSlots), h.ledger.upsertedSlots)
	}
	slot := h.ledger.upsertedSlots[0]
	if slot.Waypoint != "X1-B-A1" || slot.Kind != SlotKindMarket || slot.State != SlotStateWanted {
		t.Fatalf("slot = %+v, want a WANTED MARKET placement at X1-B-A1", slot)
	}
	if len(slot.WhitelistGoods) != 1 || slot.WhitelistGoods[0] != "FUEL" {
		t.Fatalf("WhitelistGoods = %v, want only the whitelisted good", slot.WhitelistGoods)
	}
	if slot.DepthCredits != 300 {
		t.Fatalf("DepthCredits = %d, want 300 (10 × 30 over the whitelist only)", slot.DepthCredits)
	}
}

func TestAdvanceExpansion_RevealedMarketOutsideTheWhitelistWritesNoSlot(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-B", Verdict: VerdictPending, UnchartedCount: 2,
			SeedShip: "PROBE-7", SeedState: SeedStateCharting},
	}
	h.ships.positions = map[string]ShipPos{
		"PROBE-7": {Waypoint: "X1-B-A1", NavStatus: navigation.NavStatusDocked, Found: true},
	}
	h.seed.isMarket = map[string]bool{"X1-B-A1": true}
	h.markets.goods = map[string][]string{"X1-B-A1": {"PLATINUM"}}

	rep, err := h.run(t, &fakeUncharted{bySystem: map[string][]string{"X1-B": {"X1-B-A1"}}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Charted != 1 {
		t.Fatalf("Charted = %d, want 1 — the waypoint is still charted", rep.Charted)
	}
	if len(h.ledger.upsertedSlots) != 0 {
		t.Fatalf("a market dealing in nothing we want must earn no placement, got %v", h.ledger.upsertedSlots)
	}
}

func TestAdvanceExpansion_NonMarketWaypointIsChartedWithoutAScan(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-B", Verdict: VerdictPending, UnchartedCount: 2,
			SeedShip: "PROBE-7", SeedState: SeedStateCharting},
	}
	h.ships.positions = map[string]ShipPos{
		"PROBE-7": {Waypoint: "X1-B-A1", NavStatus: navigation.NavStatusDocked, Found: true},
	}

	if _, err := h.run(t, &fakeUncharted{bySystem: map[string][]string{"X1-B": {"X1-B-A1"}}}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.seed.countOf("market") != 0 {
		t.Fatalf("a waypoint with no marketplace must cost no market scan, got %v", h.seed.verbs())
	}
}

func TestAdvanceExpansion_FailedChartLeavesTheTourExactlyWhereItWas(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-B", Verdict: VerdictPending, UnchartedCount: 2,
			SeedShip: "PROBE-7", SeedState: SeedStateCharting},
	}
	h.ships.positions = map[string]ShipPos{
		"PROBE-7": {Waypoint: "X1-B-A1", NavStatus: navigation.NavStatusDocked, Found: true},
	}
	h.seed.chartErr = errors.New("ship not in orbit")

	rep, err := h.run(t, &fakeUncharted{bySystem: map[string][]string{"X1-B": {"X1-B-A1"}}})
	if err != nil {
		t.Fatalf("one failed chart must not fail the tick: %v", err)
	}
	if h.seed.countOf("refresh") != 0 {
		t.Fatalf("nothing was charted, so nothing may be refreshed: %v", h.seed.verbs())
	}
	if rep.Charted != 0 {
		t.Fatalf("Charted = %d, want 0", rep.Charted)
	}
	if len(h.ledger.setSeeds) != 0 {
		t.Fatalf("the mission must stay put for the next tick to retry, got %v", h.ledger.setSeeds)
	}
}

func TestAdvanceExpansion_SeedTakesOneStepPerTick(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-B", Verdict: VerdictPending, UnchartedCount: 3,
			SeedShip: "PROBE-7", SeedState: SeedStateCharting},
	}
	h.ships.positions = map[string]ShipPos{
		"PROBE-7": {Waypoint: "X1-B-A1", NavStatus: navigation.NavStatusDocked, Found: true},
	}

	rep, err := h.run(t, &fakeUncharted{bySystem: map[string][]string{
		"X1-B": {"X1-B-A1", "X1-B-B2", "X1-B-C3"},
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Charted != 1 || rep.Navigated != 0 {
		t.Fatalf("Charted=%d Navigated=%d — a seed charts ONE waypoint per tick and never chains onward",
			rep.Charted, rep.Navigated)
	}
	if rep.Actions != 1 {
		t.Fatalf("Actions = %d, want 1", rep.Actions)
	}
}

func TestAdvanceExpansion_ActionsAreCappedPerTick(t *testing.T) {
	h := newExpandHarness()
	uncharted := map[string][]string{}
	for _, sys := range []string{"X1-B1", "X1-B2", "X1-B3", "X1-B4", "X1-B5", "X1-B6", "X1-B7", "X1-B8"} {
		h.ledger.systems = append(h.ledger.systems, ExpandSystem{
			System: sys, Verdict: VerdictPending, UnchartedCount: 2,
			SeedShip: "PROBE-" + sys, SeedState: SeedStateCharting,
		})
		h.ships.positions["PROBE-"+sys] = ShipPos{
			Waypoint: sys + "-A1", NavStatus: navigation.NavStatusDocked, Found: true,
		}
		uncharted[sys] = []string{sys + "-A1"}
	}

	rep, err := h.run(t, &fakeUncharted{bySystem: uncharted})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Actions != MaxExpansionActions {
		t.Fatalf("Actions = %d, want the cap %d — eight ready seeds must not fire eight command bursts",
			rep.Actions, MaxExpansionActions)
	}
	if h.seed.countOf("chart") != MaxExpansionActions {
		t.Fatalf("charted %d times, want %d", h.seed.countOf("chart"), MaxExpansionActions)
	}
}

// --- terminal states ---------------------------------------------------------

func TestAdvanceExpansion_TourEndClaimsAWantedSlotWithoutBuying(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-B", Verdict: VerdictPending, UnchartedCount: 0,
			SeedShip: "PROBE-7", SeedState: SeedStateCharting},
	}
	h.ships.positions = map[string]ShipPos{
		"PROBE-7": {Waypoint: "X1-B-A1", NavStatus: navigation.NavStatusDocked, Found: true},
	}
	h.screen.verdicts = map[string]string{"X1-B": VerdictInScope}
	h.ledger.slots = []QueuedSlot{{
		Waypoint: "X1-B-A1", System: "X1-B", Kind: SlotKindMarket, State: SlotStateWanted,
	}}

	rep, err := h.run(t, &fakeUncharted{bySystem: map[string][]string{}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(h.screen.asked) != 1 || h.screen.asked[0] != "X1-B" {
		t.Fatalf("the finished system must be re-screened, got %v", h.screen.asked)
	}
	if len(h.ledger.transitions) != 1 {
		t.Fatalf("transitions = %v, want one WANTED→IN_TRANSIT claim", h.ledger.transitions)
	}
	got := h.ledger.transitions[0]
	if got.waypoint != "X1-B-A1" || got.from != SlotStateWanted || got.to != SlotStateInTransit {
		t.Fatalf("transition = %+v, want X1-B-A1 WANTED→IN_TRANSIT", got)
	}
	if got.assignedShip == nil || *got.assignedShip != "PROBE-7" {
		t.Fatalf("the claim must assign the seed hull, got %v", got.assignedShip)
	}
	if len(h.ledger.upsertedSlots) != 0 {
		t.Fatalf("a finished seed filling a placement must never order another probe, got %v", h.ledger.upsertedSlots)
	}
	if rep.Parked != 1 {
		t.Fatalf("Parked = %d, want 1", rep.Parked)
	}
	if len(h.ledger.setSeeds) != 1 || h.ledger.setSeeds[0] != (setSeedCall{"X1-B", "", ""}) {
		t.Fatalf("seed writes = %v, want the mission cleared — the hull now belongs to the placement", h.ledger.setSeeds)
	}
}

func TestAdvanceExpansion_ALedgerRefusingWritesStopsTheTickRatherThanParkingASpare(t *testing.T) {
	// A refused claim is NOT a lost race. Reading it as one would drop the seed
	// through to standing itself down — writing a SPARE row to the very ledger
	// that just refused, and leaving the placement it was meant to fill empty.
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-B", Verdict: VerdictPending,
			SeedShip: "PROBE-7", SeedState: SeedStateCharting},
	}
	h.ships.positions = map[string]ShipPos{
		"PROBE-7": {Waypoint: "X1-B-A1", NavStatus: navigation.NavStatusDocked, Found: true},
	}
	h.screen.verdicts = map[string]string{"X1-B": VerdictInScope}
	h.ledger.slots = []QueuedSlot{{
		Waypoint: "X1-B-A1", System: "X1-B", Kind: SlotKindMarket, State: SlotStateWanted,
	}}
	h.ledger.transitionErr = errors.New("ledger refusing writes")

	_, err := h.run(t, &fakeUncharted{bySystem: map[string][]string{}})
	if err == nil {
		t.Fatal("a ledger outage must stop the tick, not look like contention")
	}
	if len(h.ledger.upsertedSlots) != 0 {
		t.Fatalf("no spare may be parked after a failed claim, got %v", h.ledger.upsertedSlots)
	}
}

func TestAdvanceExpansion_TourEndTakesTheShipyardPlacementBeforeAnyMarket(t *testing.T) {
	// A probe standing at a system's shipyard is what turns a charted system into
	// a STAGING ORIGIN — stagingYardFor will only stage a seed for a neighbour at
	// a yard a hull of ours is standing at, and buyerAt will only buy through one.
	// So the yard placement outranks every market in the system, including the one
	// under the hull's own feet.
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-B", Verdict: VerdictPending,
			SeedShip: "PROBE-7", SeedState: SeedStateCharting},
	}
	// The hull stands on a MARKET placement, so the old "under its own feet" rule
	// and plain ledger order BOTH point away from the yard. Without yard-first
	// this fixture fills X1-B-C3.
	h.ships.positions = map[string]ShipPos{
		"PROBE-7": {Waypoint: "X1-B-C3", NavStatus: navigation.NavStatusDocked, Found: true},
	}
	h.screen.verdicts = map[string]string{"X1-B": VerdictInScope}
	h.yards.bySystem = map[string][]string{"X1-B": {"X1-B-Y9"}}
	h.ledger.slots = []QueuedSlot{
		{Waypoint: "X1-B-A1", System: "X1-B", Kind: SlotKindMarket, State: SlotStateWanted},
		{Waypoint: "X1-B-C3", System: "X1-B", Kind: SlotKindMarket, State: SlotStateWanted},
		// LAST in ledger order and not under the hull's feet, so only a yard-first
		// rule can reach it. Slotted MARKET deliberately: every probe-selling yard
		// we screen is also a whitelisted market, so the screen emits a MARKET row
		// and YARD-kind rows do not occur in practice. Matching on kind here would
		// order an empty set.
		{Waypoint: "X1-B-Y9", System: "X1-B", Kind: SlotKindMarket, State: SlotStateWanted},
	}

	if _, err := h.run(t, &fakeUncharted{bySystem: map[string][]string{}}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(h.ledger.transitions) != 1 || h.ledger.transitions[0].waypoint != "X1-B-Y9" {
		t.Fatalf("transitions = %v, want the shipyard placement X1-B-Y9 claimed first", h.ledger.transitions)
	}
}

func TestAdvanceExpansion_ShipyardPlacementUnderTheHullsFeetBeatsAnotherYard(t *testing.T) {
	// Yard-first is the OUTER key, not the only one: among the system's yards the
	// hull still prefers the one it is already standing on, which costs no flight.
	// Ledger order points the other way, so only the two-key ordering passes.
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-B", Verdict: VerdictPending,
			SeedShip: "PROBE-7", SeedState: SeedStateCharting},
	}
	h.ships.positions = map[string]ShipPos{
		"PROBE-7": {Waypoint: "X1-B-Y2", NavStatus: navigation.NavStatusDocked, Found: true},
	}
	h.screen.verdicts = map[string]string{"X1-B": VerdictInScope}
	h.yards.bySystem = map[string][]string{"X1-B": {"X1-B-Y1", "X1-B-Y2"}}
	h.ledger.slots = []QueuedSlot{
		{Waypoint: "X1-B-Y1", System: "X1-B", Kind: SlotKindMarket, State: SlotStateWanted},
		{Waypoint: "X1-B-Y2", System: "X1-B", Kind: SlotKindMarket, State: SlotStateWanted},
		{Waypoint: "X1-B-A1", System: "X1-B", Kind: SlotKindMarket, State: SlotStateWanted},
	}

	if _, err := h.run(t, &fakeUncharted{bySystem: map[string][]string{}}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(h.ledger.transitions) != 1 || h.ledger.transitions[0].waypoint != "X1-B-Y2" {
		t.Fatalf("transitions = %v, want the yard under the hull's own feet (X1-B-Y2)", h.ledger.transitions)
	}
}

func TestAdvanceExpansion_AnUnreadableYardCatalogStopsTheTourRatherThanFillingBlind(t *testing.T) {
	// The choice is one-way: the seed is CONSUMED by the placement it takes, and
	// no later tick revisits it. So a catalog that cannot say which waypoint is
	// the yard must stop the tick rather than fall back to an unordered fill that
	// leaves the system permanently unable to seed its neighbours. The tick is
	// idempotent and re-derived, so failing loudly costs one cycle.
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-B", Verdict: VerdictPending,
			SeedShip: "PROBE-7", SeedState: SeedStateCharting},
	}
	h.ships.positions = map[string]ShipPos{
		"PROBE-7": {Waypoint: "X1-B-C3", NavStatus: navigation.NavStatusDocked, Found: true},
	}
	h.screen.verdicts = map[string]string{"X1-B": VerdictInScope}
	h.yards.err = errors.New("waypoint catalog unavailable")
	h.ledger.slots = []QueuedSlot{
		{Waypoint: "X1-B-C3", System: "X1-B", Kind: SlotKindMarket, State: SlotStateWanted},
		{Waypoint: "X1-B-Y9", System: "X1-B", Kind: SlotKindMarket, State: SlotStateWanted},
	}

	_, err := h.run(t, &fakeUncharted{bySystem: map[string][]string{}})
	if err == nil {
		t.Fatal("an unreadable yard catalog must stop the tick, not fill a placement blind")
	}
	if len(h.ledger.transitions) != 0 {
		t.Fatalf("transitions = %v, want none — an unordered fill here is not recoverable", h.ledger.transitions)
	}
}

func TestAdvanceExpansion_TourEndPrefersThePlacementUnderTheSeedsOwnFeet(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-B", Verdict: VerdictPending,
			SeedShip: "PROBE-7", SeedState: SeedStateCharting},
	}
	h.ships.positions = map[string]ShipPos{
		"PROBE-7": {Waypoint: "X1-B-C3", NavStatus: navigation.NavStatusDocked, Found: true},
	}
	h.screen.verdicts = map[string]string{"X1-B": VerdictInScope}
	h.ledger.slots = []QueuedSlot{
		{Waypoint: "X1-B-A1", System: "X1-B", Kind: SlotKindMarket, State: SlotStateWanted},
		{Waypoint: "X1-B-C3", System: "X1-B", Kind: SlotKindMarket, State: SlotStateWanted},
	}

	if _, err := h.run(t, &fakeUncharted{bySystem: map[string][]string{}}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(h.ledger.transitions) != 1 || h.ledger.transitions[0].waypoint != "X1-B-C3" {
		t.Fatalf("transitions = %v, want the placement the hull is already standing on", h.ledger.transitions)
	}
}

func TestAdvanceExpansion_RejectedSystemRetargetsTheSeedOnwardWithoutBuying(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-B", Verdict: VerdictPending,
			SeedShip: "PROBE-7", SeedState: SeedStateCharting},
		{System: "X1-C", Verdict: VerdictPending, UnchartedCount: 4},
	}
	h.gates.adjacency = map[string][]string{"X1-B": {"X1-C"}, "X1-C": {"X1-B"}}
	h.ships.positions = map[string]ShipPos{
		"PROBE-7": {Waypoint: "X1-B-A1", NavStatus: navigation.NavStatusDocked, Found: true},
	}
	h.screen.verdicts = map[string]string{"X1-B": VerdictNoWhitelist}

	rep, err := h.run(t, &fakeUncharted{bySystem: map[string][]string{}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rep.Retargeted != 1 {
		t.Fatalf("Retargeted = %d, want 1", rep.Retargeted)
	}
	want := []setSeedCall{{"X1-B", "", ""}, {"X1-C", "PROBE-7", SeedStateDispatched}}
	if len(h.ledger.setSeeds) != 2 || h.ledger.setSeeds[0] != want[0] || h.ledger.setSeeds[1] != want[1] {
		t.Fatalf("seed writes = %v, want %v — the target IS the row, so retargeting is two writes",
			h.ledger.setSeeds, want)
	}
	if len(h.ledger.upsertedSlots) != 0 {
		t.Fatalf("retargeting an existing hull must never order a probe, got %v", h.ledger.upsertedSlots)
	}
	if rep.SeedsRequested != 0 {
		t.Fatalf("SeedsRequested = %d, want 0 — the retargeted hull already covers X1-C", rep.SeedsRequested)
	}
}

// retargetHarness sets up a finished seed on a rejected system with one
// reachable frontier system to move on to.
func retargetHarness(t *testing.T) *expandHarness {
	t.Helper()
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-B", Verdict: VerdictPending,
			SeedShip: "PROBE-7", SeedState: SeedStateCharting},
		{System: "X1-C", Verdict: VerdictPending, UnchartedCount: 4},
	}
	h.gates.adjacency = map[string][]string{"X1-B": {"X1-C"}, "X1-C": {"X1-B"}}
	h.ships.positions = map[string]ShipPos{
		"PROBE-7": {Waypoint: "X1-B-A1", NavStatus: navigation.NavStatusDocked, Found: true},
	}
	h.screen.verdicts = map[string]string{"X1-B": VerdictNoWhitelist}
	return h
}

func TestAdvanceExpansion_AFailedRetargetStampRestoresTheOldErrand(t *testing.T) {
	// Between the clear and the stamp the hull is named by NOTHING — a mid-tour
	// seed has no placement row either, so losing the errand orphans a probe we
	// paid for: invisible to the probe cap, and re-bought. The restore closes
	// that window.
	h := retargetHarness(t)
	h.ledger.setSeedErrOn = map[string]error{"X1-C/PROBE-7": errors.New("ledger refusing writes")}

	_, err := h.run(t, &fakeUncharted{bySystem: map[string][]string{}})
	if err == nil {
		t.Fatal("a failed retarget must surface")
	}

	want := []setSeedCall{
		{"X1-B", "", ""},                         // clear the old errand
		{"X1-C", "PROBE-7", SeedStateDispatched}, // stamp the new one — fails
		{"X1-B", "PROBE-7", SeedStateCharting},   // restore
	}
	if len(h.ledger.setSeeds) != 3 {
		t.Fatalf("seed writes = %v, want %v", h.ledger.setSeeds, want)
	}
	for i, got := range h.ledger.setSeeds {
		if got != want[i] {
			t.Fatalf("seed write %d = %v, want %v", i, got, want[i])
		}
	}
}

func TestAdvanceExpansion_AFailedRestoreNamesTheOrphanedHullInTheError(t *testing.T) {
	// Both writes gone: the probe can only be recovered by hand, so the error
	// must carry enough to do it with.
	h := retargetHarness(t)
	h.ledger.setSeedErrOn = map[string]error{
		"X1-C/PROBE-7": errors.New("ledger refusing writes"),
		"X1-B/PROBE-7": errors.New("still refusing"),
	}

	_, err := h.run(t, &fakeUncharted{bySystem: map[string][]string{}})
	if err == nil {
		t.Fatal("a failed restore must surface")
	}
	for _, want := range []string{"PROBE-7", "X1-B", "X1-C", "unattributable"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not name %q — an orphaned probe is recovered by hand", err, want)
		}
	}
}

func TestAdvanceExpansion_LooseEndParksTheSeedAsASpare(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-B", Verdict: VerdictPending,
			SeedShip: "PROBE-7", SeedState: SeedStateCharting},
	}
	h.gates.adjacency = map[string][]string{"X1-B": {}}
	h.ships.positions = map[string]ShipPos{
		"PROBE-7": {Waypoint: "X1-B-A1", NavStatus: navigation.NavStatusDocked, Found: true},
	}
	h.screen.verdicts = map[string]string{"X1-B": VerdictNoWhitelist}

	rep, err := h.run(t, &fakeUncharted{bySystem: map[string][]string{}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(h.ledger.upsertedSlots) != 1 {
		t.Fatalf("wrote %d slots, want one SPARE stand-down: %v", len(h.ledger.upsertedSlots), h.ledger.upsertedSlots)
	}
	slot := h.ledger.upsertedSlots[0]
	if slot.Waypoint != "X1-B-A1" || slot.Kind != SlotKindSpare || slot.State != SlotStateParked {
		t.Fatalf("slot = %+v, want a PARKED SPARE where the hull stands", slot)
	}
	if slot.AssignedShip != "PROBE-7" {
		t.Fatalf("AssignedShip = %q — a spare row with no hull drops the probe out of the cap count", slot.AssignedShip)
	}
	if len(h.ledger.setSeeds) != 1 || h.ledger.setSeeds[0] != (setSeedCall{"X1-B", "", ""}) {
		t.Fatalf("seed writes = %v, want the mission cleared", h.ledger.setSeeds)
	}
	if rep.Parked != 1 {
		t.Fatalf("Parked = %d, want 1", rep.Parked)
	}
}

func TestAdvanceExpansion_LooseEndClaimsAWantedSlotUnderfootInsteadOfParking(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-B", Verdict: VerdictPending,
			SeedShip: "PROBE-7", SeedState: SeedStateCharting},
	}
	h.gates.adjacency = map[string][]string{"X1-B": {}}
	h.ships.positions = map[string]ShipPos{
		"PROBE-7": {Waypoint: "X1-B-A1", NavStatus: navigation.NavStatusDocked, Found: true},
	}
	// Screened out as a whole, but the waypoint underfoot still carries a want.
	h.screen.verdicts = map[string]string{"X1-B": VerdictNoWhitelist}
	h.ledger.slots = []QueuedSlot{{
		Waypoint: "X1-B-A1", System: "X1-B", Kind: SlotKindMarket, State: SlotStateWanted,
	}}

	if _, err := h.run(t, &fakeUncharted{bySystem: map[string][]string{}}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(h.ledger.upsertedSlots) != 0 {
		t.Fatalf("a SPARE row would overwrite the placement standing there, got %v", h.ledger.upsertedSlots)
	}
	if len(h.ledger.transitions) != 1 || h.ledger.transitions[0].waypoint != "X1-B-A1" {
		t.Fatalf("transitions = %v, want the underfoot placement claimed", h.ledger.transitions)
	}
}

// A seed finishing where ANOTHER hull's placement already stands now parks as a
// spare beside it instead of standing down with no row at all (sp-dpfp8).
//
// The old behaviour was the money-UNSAFE one and the original comment said so:
// the seed was stood down DONE with no placement written, so PROBE-7 — a probe we
// paid for, sitting right there — stopped being counted by CountOwnedProbes and
// the cap authorised buying a replacement for it. That was accepted only because
// the alternative under a waypoint-keyed table was overwriting PROBE-OTHER's row,
// which is worse. With the kinds separated there is no such trade: both hulls keep
// a row, and both stay counted.
func TestAdvanceExpansion_SpareParkStandsDownBesideAnotherHullsPlacement(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-B", Verdict: VerdictPending,
			SeedShip: "PROBE-7", SeedState: SeedStateCharting},
	}
	h.gates.adjacency = map[string][]string{"X1-B": {}}
	h.ships.positions = map[string]ShipPos{
		"PROBE-7": {Waypoint: "X1-B-A1", NavStatus: navigation.NavStatusDocked, Found: true},
	}
	h.screen.verdicts = map[string]string{"X1-B": VerdictNoWhitelist}
	h.ledger.slots = []QueuedSlot{{
		Waypoint: "X1-B-A1", System: "X1-B", Kind: SlotKindMarket,
		State: SlotStateParked, AssignedShip: "PROBE-OTHER",
	}}

	if _, err := h.run(t, &fakeUncharted{bySystem: map[string][]string{}}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(h.ledger.upsertedSlots) != 1 {
		t.Fatalf("wrote %d slots, want 1 — PROBE-7 must keep a row or it leaves the probe cap and is re-bought: %v",
			len(h.ledger.upsertedSlots), h.ledger.upsertedSlots)
	}
	parked := h.ledger.upsertedSlots[0]
	if parked.Waypoint != "X1-B-A1" || parked.Kind != SlotKindSpare || parked.AssignedShip != "PROBE-7" {
		t.Fatalf("seed parked as %+v, want a SPARE at X1-B-A1 naming PROBE-7", parked)
	}

	// PROBE-OTHER's placement is a different row and must be untouched.
	incumbent, found := h.ledger.slotAt("X1-B-A1", SlotKindMarket)
	if !found || incumbent.AssignedShip != "PROBE-OTHER" || incumbent.State != SlotStateParked {
		t.Fatalf("PROBE-OTHER's placement must survive intact, got %+v (found=%v)", incumbent, found)
	}

	// The errand is cleared rather than marked DONE: the hull is a spare again,
	// named by its own placement row.
	if len(h.ledger.setSeeds) != 1 || h.ledger.setSeeds[0].ship != "" {
		t.Fatalf("seed writes = %v, want the errand cleared now that a placement row names the hull",
			h.ledger.setSeeds)
	}
}

func TestAdvanceExpansion_FailedScreenLeavesTheSeedForTheNextTick(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-B", Verdict: VerdictPending,
			SeedShip: "PROBE-7", SeedState: SeedStateCharting},
	}
	h.ships.positions = map[string]ShipPos{
		"PROBE-7": {Waypoint: "X1-B-A1", NavStatus: navigation.NavStatusDocked, Found: true},
	}
	h.screen.err = errors.New("market read failed")

	if _, err := h.run(t, &fakeUncharted{bySystem: map[string][]string{}}); err != nil {
		t.Fatalf("one failed screen must not fail the tick: %v", err)
	}
	if len(h.ledger.setSeeds) != 0 || len(h.ledger.upsertedSlots) != 0 || len(h.ledger.transitions) != 0 {
		t.Fatalf("no terminal decision may be taken on an unscreened system: seeds=%v slots=%v transitions=%v",
			h.ledger.setSeeds, h.ledger.upsertedSlots, h.ledger.transitions)
	}
}

func TestAdvanceExpansion_DoneSeedIsNeverActedOnAgain(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-B", Verdict: VerdictNoWhitelist,
			SeedShip: "PROBE-7", SeedState: SeedStateDone},
	}
	h.ships.positions = map[string]ShipPos{
		"PROBE-7": {Waypoint: "X1-B-A1", NavStatus: navigation.NavStatusDocked, Found: true},
	}

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(h.seed.calls) != 0 || rep.Actions != 0 {
		t.Fatalf("DONE is terminal, got commands=%v actions=%d", h.seed.verbs(), rep.Actions)
	}
	if len(h.screen.asked) != 0 {
		t.Fatalf("a stood-down seed must not re-screen its system, got %v", h.screen.asked)
	}
}
