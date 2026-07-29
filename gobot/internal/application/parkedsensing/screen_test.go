package parkedsensing

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
)

const testPlayerID = 7

// --- fakes -------------------------------------------------------------------

type fakeCatalog struct {
	markets   []string
	uncharted int
	yards     []string
	err       error
	// catalogUnknown inverts the default: the harness starts every system with
	// its waypoint list KNOWN, because that is the situation nearly every test
	// here is about (what does this system DEAL IN). The unswept case is its own
	// small set of tests, which opt in.
	catalogUnknown bool
	// catalogKnownErr breaks the catalog-known read ALONE. It is separate from
	// err because err breaks ListMarketWaypoints too, and that read comes first
	// — so a test using the shared field would never reach this branch and would
	// silently duplicate the market-listing failure test instead.
	catalogKnownErr error
	// heavyYards are the system's HEAVY-selling shipyards (sp-fwk8z T3). Separate from
	// yards so a test can model a system whose heavy yard is NOT a probe yard.
	heavyYards []string
}

func (f *fakeCatalog) ListMarketWaypoints(_ context.Context, _ string) ([]string, error) {
	return f.markets, f.err
}
func (f *fakeCatalog) ListUnchartedCount(_ context.Context, _ string) (int, error) {
	return f.uncharted, f.err
}
func (f *fakeCatalog) ListHeavyYards(_ context.Context, _ string) ([]string, error) {
	return f.heavyYards, f.err
}
func (f *fakeCatalog) ListProbeYards(_ context.Context, _ string) ([]string, error) {
	return f.yards, f.err
}
func (f *fakeCatalog) CatalogKnown(_ context.Context, _ string) (bool, error) {
	if err := f.catalogKnownErr; err != nil {
		// Adversarial: "we know this system" alongside the error, so a screen
		// that leaks it writes the durable rejection this signal exists to stop.
		return true, err
	}
	if f.err != nil {
		return true, f.err
	}
	return !f.catalogUnknown, nil
}

type fakeMarketGoods struct {
	goods map[string][]string                  // waypoint → known goods (absent = unknown)
	depth map[string][]scouting.MarketDepthRow // waypoint → priced rows
	err   error
}

func (f *fakeMarketGoods) GoodsAt(_ context.Context, _ int, waypoint string) ([]string, bool, error) {
	if f.err != nil {
		return nil, false, f.err
	}
	goods, known := f.goods[waypoint]
	return goods, known, nil
}

func (f *fakeMarketGoods) DepthRowsAt(_ context.Context, _ int, waypoint string) ([]scouting.MarketDepthRow, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.depth[waypoint], nil
}

type fakeRemote struct {
	goods map[string][]string
	err   error
	calls []string // waypoints fetched, in order
}

func (f *fakeRemote) FetchGoods(_ context.Context, _ int, _ string, waypoint string) ([]string, error) {
	f.calls = append(f.calls, waypoint)
	if f.err != nil {
		return nil, f.err
	}
	return f.goods[waypoint], nil
}

type fakeLedger struct {
	existing []ExistingSlot
	slots    []SlotRecord
	systems  []SystemRecord
	err      error
}

func (f *fakeLedger) ExistingSlots(_ context.Context, _ int, _ string) ([]ExistingSlot, error) {
	return f.existing, f.err
}

// commit mirrors what a real ledger does between screens: the placements written
// this tick are the placements the next tick reads back.
func (f *fakeLedger) commit() {
	for _, slot := range f.slots {
		f.existing = append(f.existing, ExistingSlot{
			Waypoint:       slot.Waypoint,
			WhitelistGoods: slot.WhitelistGoods,
			DepthCredits:   slot.DepthCredits,
		})
	}
	f.slots = nil
}
func (f *fakeLedger) UpsertSlotMetadata(_ context.Context, _ int, slot SlotRecord) error {
	if f.err != nil {
		return f.err
	}
	f.slots = append(f.slots, slot)
	return nil
}
func (f *fakeLedger) UpsertSystem(_ context.Context, _ int, record SystemRecord) error {
	if f.err != nil {
		return f.err
	}
	f.systems = append(f.systems, record)
	return nil
}

type harness struct {
	catalog *fakeCatalog
	goods   *fakeMarketGoods
	remote  *fakeRemote
	ledger  *fakeLedger
}

func newHarness() *harness {
	return &harness{
		catalog: &fakeCatalog{},
		goods: &fakeMarketGoods{
			goods: map[string][]string{},
			depth: map[string][]scouting.MarketDepthRow{},
		},
		remote: &fakeRemote{goods: map[string][]string{}},
		ledger: &fakeLedger{},
	}
}

func (h *harness) ports() ScreenPorts {
	return ScreenPorts{
		Waypoints:    h.catalog,
		MarketGoods:  h.goods,
		RemoteMarket: h.remote,
		Ledger:       h.ledger,
	}
}

func (h *harness) screen(t *testing.T, whitelist map[string]bool) ScreenResult {
	t.Helper()
	result, err := ScreenSystem(context.Background(), h.ports(), testPlayerID, "X1-AA11", whitelist)
	if err != nil {
		t.Fatalf("ScreenSystem returned error: %v", err)
	}
	return result
}

func whitelistOf(goods ...string) map[string]bool {
	set := make(map[string]bool, len(goods))
	for _, good := range goods {
		set[good] = true
	}
	return set
}

// --- verdict rules -----------------------------------------------------------

// (a) A fully charted system with one market whose goods we want is in scope,
// and that market earns a MARKET slot carrying only the MATCHED goods.
func TestScreenSystemInScopeWithWhitelistedMarket(t *testing.T) {
	h := newHarness()
	h.catalog.markets = []string{"X1-AA11-A1"}
	h.goods.goods["X1-AA11-A1"] = []string{"IRON_ORE", "FUEL"}

	result := h.screen(t, whitelistOf("IRON_ORE"))

	if result.Verdict != VerdictInScope {
		t.Fatalf("Verdict = %q, want %q", result.Verdict, VerdictInScope)
	}
	want := []PlannedSlot{{
		Waypoint:       "X1-AA11-A1",
		Kind:           SlotKindMarket,
		WhitelistGoods: []string{"IRON_ORE"},
	}}
	if !reflect.DeepEqual(result.Slots, want) {
		t.Fatalf("Slots =\n %+v\nwant %+v", result.Slots, want)
	}
}

// (b) Charted through, nothing we want: a final rejection.
func TestScreenSystemNoWhitelistWhenFullyChartedAndNoMatch(t *testing.T) {
	h := newHarness()
	h.catalog.markets = []string{"X1-AA11-A1"}
	h.catalog.uncharted = 0
	h.goods.goods["X1-AA11-A1"] = []string{"FUEL"}

	result := h.screen(t, whitelistOf("IRON_ORE"))

	if result.Verdict != VerdictNoWhitelist {
		t.Fatalf("Verdict = %q, want %q", result.Verdict, VerdictNoWhitelist)
	}
	if len(result.Slots) != 0 {
		t.Fatalf("expected no slots in a rejected system, got %+v", result.Slots)
	}
}

// (c) Uncharted waypoints remain and nothing whitelisted has surfaced yet: not
// a rejection, just not decided — charting is expansion's job.
func TestScreenSystemPendingWhileUnchartedAndNoMatchYet(t *testing.T) {
	h := newHarness()
	h.catalog.markets = []string{"X1-AA11-A1"}
	h.catalog.uncharted = 3
	h.goods.goods["X1-AA11-A1"] = []string{"FUEL"}

	result := h.screen(t, whitelistOf("IRON_ORE"))

	if result.Verdict != VerdictPending {
		t.Fatalf("Verdict = %q, want %q", result.Verdict, VerdictPending)
	}
	if result.UnchartedCount != 3 {
		t.Fatalf("UnchartedCount = %d, want 3", result.UnchartedCount)
	}
	if len(result.Slots) != 0 {
		t.Fatalf("expected no slots while undecided, got %+v", result.Slots)
	}
}

// One revealed whitelisted market decides the system even with charting still
// running — otherwise the first market's probe would wait on the last waypoint.
func TestScreenSystemInScopeWhileUnchartedRemain(t *testing.T) {
	h := newHarness()
	h.catalog.markets = []string{"X1-AA11-A1"}
	h.catalog.uncharted = 2
	h.goods.goods["X1-AA11-A1"] = []string{"IRON_ORE"}

	result := h.screen(t, whitelistOf("IRON_ORE"))

	if result.Verdict != VerdictInScope {
		t.Fatalf("Verdict = %q, want %q", result.Verdict, VerdictInScope)
	}
	if result.UnchartedCount != 2 {
		t.Fatalf("UnchartedCount = %d, want 2", result.UnchartedCount)
	}
	if len(result.Slots) != 1 || result.Slots[0].Waypoint != "X1-AA11-A1" {
		t.Fatalf("expected the revealed market to be slotted, got %+v", result.Slots)
	}
}

// A system with no markets at all and nothing left to chart deals in nothing.
func TestScreenSystemNoWhitelistWhenNoMarketsAtAll(t *testing.T) {
	h := newHarness()

	result := h.screen(t, whitelistOf("IRON_ORE"))

	if result.Verdict != VerdictNoWhitelist {
		t.Fatalf("Verdict = %q, want %q", result.Verdict, VerdictNoWhitelist)
	}
}

// --- remote gap fill ---------------------------------------------------------

// (d) A charted market the database has never seen is resolved by exactly one
// remote fetch, and what it reveals is written to the ledger as a slot.
//
// Note what does NOT happen here: market_data is not backfilled. Prices need a
// ship present, so GoodsAt keeps reporting a gap at this waypoint until a probe
// actually parks there. The SLOT ROW is the cache that closes the loop — see
// TestScreenSystemDoesNotRefetchAnAlreadySlottedWaypoint.
func TestScreenSystemFetchesUnknownMarketOnceAndPersists(t *testing.T) {
	h := newHarness()
	h.catalog.markets = []string{"X1-AA11-A1"}
	h.remote.goods["X1-AA11-A1"] = []string{"IRON_ORE", "FUEL"}

	result := h.screen(t, whitelistOf("IRON_ORE"))

	if !reflect.DeepEqual(h.remote.calls, []string{"X1-AA11-A1"}) {
		t.Fatalf("remote fetches = %v, want exactly one for X1-AA11-A1", h.remote.calls)
	}
	if result.Verdict != VerdictInScope {
		t.Fatalf("Verdict = %q, want %q", result.Verdict, VerdictInScope)
	}
	want := []SlotRecord{{
		Waypoint:       "X1-AA11-A1",
		System:         "X1-AA11",
		Kind:           SlotKindMarket,
		State:          SlotStateWanted,
		WhitelistGoods: []string{"IRON_ORE"},
	}}
	if !reflect.DeepEqual(h.ledger.slots, want) {
		t.Fatalf("persisted slots =\n %+v\nwant %+v", h.ledger.slots, want)
	}
}

// The database is consulted first: a market we already know costs no API call.
func TestScreenSystemDoesNotFetchKnownMarket(t *testing.T) {
	h := newHarness()
	h.catalog.markets = []string{"X1-AA11-A1"}
	h.goods.goods["X1-AA11-A1"] = []string{"IRON_ORE"}

	h.screen(t, whitelistOf("IRON_ORE"))

	if len(h.remote.calls) != 0 {
		t.Fatalf("expected no remote fetch for a known market, got %v", h.remote.calls)
	}
}

// Re-screening a system must not re-buy knowledge it already owns. A waypoint
// discovered remotely stays absent from market_data until a probe parks there,
// so the slot row is the only cache — and it must SUPPLY the goods, not merely
// suppress the call: dropping them would take the waypoint out of the hit set
// and delete its own slot from the plan.
func TestScreenSystemDoesNotRefetchAnAlreadySlottedWaypoint(t *testing.T) {
	h := newHarness()
	h.catalog.markets = []string{"X1-AA11-A1"}
	h.remote.goods["X1-AA11-A1"] = []string{"IRON_ORE"}

	h.screen(t, whitelistOf("IRON_ORE")) // first screen: one fetch, slot written
	h.ledger.commit()
	h.remote.calls = nil

	result := h.screen(t, whitelistOf("IRON_ORE"))

	if len(h.remote.calls) != 0 {
		t.Fatalf("second screen re-fetched an already-slotted waypoint: %v", h.remote.calls)
	}
	if result.Verdict != VerdictInScope {
		t.Fatalf("Verdict = %q, want %q — the slot's recorded goods must still count", result.Verdict, VerdictInScope)
	}
	if len(result.Slots) != 1 || result.Slots[0].Waypoint != "X1-AA11-A1" {
		t.Fatalf("expected the slotted waypoint to stay in the plan, got %+v", result.Slots)
	}
	if len(h.ledger.slots) != 0 {
		t.Fatalf("expected no rewrite of the existing slot, got %+v", h.ledger.slots)
	}
}

// A slot's recorded goods are a whitelist PROJECTION, so an EMPTY one is a real
// answer — "nothing we want is here" — and not a gap. That is what a YARD slot
// records, and what a market that matched nothing records. It must suppress the
// API call exactly like a populated projection: under an era-invariant
// whitelist a refetch could only confirm the same emptiness, at the cost of a
// call. The waypoint stays out of the hit set.
func TestScreenSystemTreatsEmptyProjectionAsAuthoritative(t *testing.T) {
	for _, projection := range [][]string{nil, {}} {
		h := newHarness()
		h.catalog.markets = []string{"X1-AA11-Y1", "X1-AA11-A1"}
		h.goods.goods["X1-AA11-A1"] = []string{"IRON_ORE"} // keeps the system in scope
		h.ledger.existing = []ExistingSlot{{Waypoint: "X1-AA11-Y1", WhitelistGoods: projection}}
		h.remote.goods["X1-AA11-Y1"] = []string{"IRON_ORE"} // must never be consulted

		result := h.screen(t, whitelistOf("IRON_ORE"))

		if len(h.remote.calls) != 0 {
			t.Fatalf("empty projection was treated as a gap and refetched: %v", h.remote.calls)
		}
		for _, slot := range result.Slots {
			if slot.Waypoint == "X1-AA11-Y1" && slot.Kind == SlotKindMarket {
				t.Fatalf("empty-projection waypoint became a market hit: %+v", result.Slots)
			}
		}
	}
}

// The recorded depth survives a re-screen: it is the only measurement that slot
// has, and re-deriving it from an empty market_data would silently zero it.
func TestScreenSystemKeepsRecordedDepthForSlottedWaypoint(t *testing.T) {
	h := newHarness()
	h.catalog.markets = []string{"X1-AA11-A1"}
	h.ledger.existing = []ExistingSlot{{
		Waypoint:       "X1-AA11-A1",
		WhitelistGoods: []string{"IRON_ORE"},
		DepthCredits:   4200,
	}}

	result := h.screen(t, whitelistOf("IRON_ORE"))

	if result.Slots[0].DepthCredits != 4200 {
		t.Fatalf("DepthCredits = %d, want the recorded 4200", result.Slots[0].DepthCredits)
	}
}

// Uncharted waypoints cost no API calls: the fetch budget is one call per
// CHARTED market with a cache gap and nothing else. Charting them is expansion's
// job, and a market GET on an uncharted waypoint would tell us nothing anyway.
func TestScreenSystemFetchBudgetIsChartedGapsOnly(t *testing.T) {
	h := newHarness()
	h.catalog.markets = []string{"X1-AA11-A1", "X1-AA11-A2"}
	h.catalog.uncharted = 5
	h.goods.goods["X1-AA11-A2"] = []string{"FUEL"} // known: no gap, no call

	h.screen(t, whitelistOf("IRON_ORE"))

	if !reflect.DeepEqual(h.remote.calls, []string{"X1-AA11-A1"}) {
		t.Fatalf("remote fetches = %v, want exactly one (the single charted gap)", h.remote.calls)
	}
}

// A market known to trade nothing is still KNOWN: an empty goods list is an
// answer, not a gap, and re-fetching it every tick would burn the API budget.
func TestScreenSystemDoesNotRefetchKnownEmptyMarket(t *testing.T) {
	h := newHarness()
	h.catalog.markets = []string{"X1-AA11-A1"}
	h.goods.goods["X1-AA11-A1"] = []string{}

	result := h.screen(t, whitelistOf("IRON_ORE"))

	if len(h.remote.calls) != 0 {
		t.Fatalf("expected no remote fetch for a known-empty market, got %v", h.remote.calls)
	}
	if result.Verdict != VerdictNoWhitelist {
		t.Fatalf("Verdict = %q, want %q", result.Verdict, VerdictNoWhitelist)
	}
}

// A failed fetch leaves the market unresolved. It must NOT harden into a
// rejection: NO_WHITELIST is durable, and recording one because an API call
// failed would permanently write off a system nobody ever read.
func TestScreenSystemStaysPendingWhenRemoteFetchFails(t *testing.T) {
	h := newHarness()
	h.catalog.markets = []string{"X1-AA11-A1"}
	h.remote.err = errors.New("boom")

	result := h.screen(t, whitelistOf("IRON_ORE"))

	if result.Verdict != VerdictPending {
		t.Fatalf("Verdict = %q, want %q", result.Verdict, VerdictPending)
	}
	if len(result.Slots) != 0 {
		t.Fatalf("expected no slots from an unresolved market, got %+v", result.Slots)
	}
}

// --- yards -------------------------------------------------------------------

// (e) An in-scope system gets a YARD slot for EVERY shipyard it offers, in the
// catalogue's cheapest-first order — not just the first one.
//
// Buying any hull requires a ship already standing at that shipyard, so a yard
// we never place at is a counter we can never buy from. Slotting only yards[0]
// left every other shipyard in the system permanently unbuyable.
func TestScreenSystemPlansAYardSlotForEveryYard(t *testing.T) {
	h := newHarness()
	h.catalog.markets = []string{"X1-AA11-A1"}
	h.catalog.yards = []string{"X1-AA11-Y1", "X1-AA11-Y2"}
	h.goods.goods["X1-AA11-A1"] = []string{"IRON_ORE"}

	result := h.screen(t, whitelistOf("IRON_ORE"))

	want := []PlannedSlot{
		{Waypoint: "X1-AA11-A1", Kind: SlotKindMarket, WhitelistGoods: []string{"IRON_ORE"}},
		{Waypoint: "X1-AA11-Y1", Kind: SlotKindYard},
		{Waypoint: "X1-AA11-Y2", Kind: SlotKindYard},
	}
	if !reflect.DeepEqual(result.Slots, want) {
		t.Fatalf("Slots =\n %+v\nwant %+v", result.Slots, want)
	}
}

// A system with exactly ONE shipyard is planned exactly as it always was: one
// MARKET slot, one YARD slot. The every-yard change must be a pure extension —
// if it moved the single-yard case at all it would be re-planning the 500-odd
// one-yard systems the fleet already holds.
func TestScreenSystemSingleYardSystemIsUnchanged(t *testing.T) {
	h := newHarness()
	h.catalog.markets = []string{"X1-AA11-A1"}
	h.catalog.yards = []string{"X1-AA11-Y1"}
	h.goods.goods["X1-AA11-A1"] = []string{"IRON_ORE"}

	result := h.screen(t, whitelistOf("IRON_ORE"))

	want := []PlannedSlot{
		{Waypoint: "X1-AA11-A1", Kind: SlotKindMarket, WhitelistGoods: []string{"IRON_ORE"}},
		{Waypoint: "X1-AA11-Y1", Kind: SlotKindYard},
	}
	if !reflect.DeepEqual(result.Slots, want) {
		t.Fatalf("Slots =\n %+v\nwant %+v", result.Slots, want)
	}
}

// A waypoint listed TWICE by the yard catalogue still claims exactly one slot.
//
// No adapter returns duplicates today (the probe read is primary-keyed per
// waypoint, the heavy read dedupes explicitly, the trait fallback lists distinct
// waypoints), but nothing in the PORT CONTRACT promises it, and the cost of the
// invariant failing is a duplicate placement for one waypoint — which is a
// duplicate probe purchase. The guard is cheap; this pins that it is real.
func TestScreenSystemSlotsADuplicatedYardOnlyOnce(t *testing.T) {
	h := newHarness()
	h.catalog.markets = []string{"X1-AA11-A1"}
	h.catalog.yards = []string{"X1-AA11-Y1", "X1-AA11-Y1"}
	h.goods.goods["X1-AA11-A1"] = []string{"IRON_ORE"}

	result := h.screen(t, whitelistOf("IRON_ORE"))

	want := []PlannedSlot{
		{Waypoint: "X1-AA11-A1", Kind: SlotKindMarket, WhitelistGoods: []string{"IRON_ORE"}},
		{Waypoint: "X1-AA11-Y1", Kind: SlotKindYard},
	}
	if !reflect.DeepEqual(result.Slots, want) {
		t.Fatalf("a duplicated yard must claim one slot, got\n %+v\nwant %+v", result.Slots, want)
	}
}

// THE CASE THE OWNER'S RULE TURNS ON: three shipyards in one system, one of
// which is ALSO a whitelisted market.
//
// All three waypoints must end up covered — two fresh YARD slots plus the MARKET
// slot that already covers the third — and no waypoint may claim two slots. The
// collision half is load-bearing: the ledger is waypoint-keyed, so a second slot
// on the co-located yard would overwrite the MARKET row (losing the goods list)
// rather than add a placement, and would authorise buying a second probe for a
// waypoint already covered.
func TestScreenSystemCoversEveryYardWithoutDoubleSlottingTheMarketYard(t *testing.T) {
	h := newHarness()
	// Y2 is the yard that is also a whitelisted market.
	h.catalog.markets = []string{"X1-AA11-Y2"}
	h.catalog.yards = []string{"X1-AA11-Y1", "X1-AA11-Y2", "X1-AA11-Y3"}
	h.goods.goods["X1-AA11-Y2"] = []string{"IRON_ORE"}

	result := h.screen(t, whitelistOf("IRON_ORE"))

	byWaypoint := map[string][]string{}
	for _, slot := range result.Slots {
		byWaypoint[slot.Waypoint] = append(byWaypoint[slot.Waypoint], slot.Kind)
	}
	for _, yard := range []string{"X1-AA11-Y1", "X1-AA11-Y2", "X1-AA11-Y3"} {
		kinds, covered := byWaypoint[yard]
		if !covered {
			t.Fatalf("shipyard %q got no placement at all — it can never be bought at: %+v", yard, result.Slots)
		}
		if len(kinds) != 1 {
			t.Fatalf("shipyard %q claimed %d slots (%v), want exactly 1 — a waypoint-keyed ledger would overwrite, not add", yard, len(kinds), kinds)
		}
	}
	// The co-located yard keeps the MARKET kind, which is the one carrying goods.
	if got := byWaypoint["X1-AA11-Y2"][0]; got != SlotKindMarket {
		t.Fatalf("the yard that is also a market must stay kind %q, got %q", SlotKindMarket, got)
	}
	// The two shipyard-only waypoints are the new YARD rows.
	for _, yard := range []string{"X1-AA11-Y1", "X1-AA11-Y3"} {
		if got := byWaypoint[yard][0]; got != SlotKindYard {
			t.Fatalf("shipyard %q should be kind %q, got %q", yard, SlotKindYard, got)
		}
	}
	if len(result.Slots) != 3 {
		t.Fatalf("expected exactly 3 placements for 3 waypoints, got %d: %+v", len(result.Slots), result.Slots)
	}
}

func TestScreenSystemPlansNoYardSlotWithoutAYard(t *testing.T) {
	h := newHarness()
	h.catalog.markets = []string{"X1-AA11-A1"}
	h.goods.goods["X1-AA11-A1"] = []string{"IRON_ORE"}

	result := h.screen(t, whitelistOf("IRON_ORE"))

	for _, slot := range result.Slots {
		if slot.Kind == SlotKindYard {
			t.Fatalf("planned a YARD slot with no yard in the system: %+v", result.Slots)
		}
	}
}

// CONTRACT: a probe-selling yard that is also a whitelisted market gets exactly
// ONE slot, and its kind is MARKET — the kind that carries the goods list.
//
// The consequence, which consumers MUST honour: a watched yard can hold a slot
// of either kind, so "is a probe present at this yard?" is answered by matching
// waypoint + PARKED state, NEVER by filtering on slot_kind == YARD. A kind-wise
// lookup would miss the probe standing on this very waypoint and buy a second
// one for it.
func TestScreenSystemSlotsAYardThatIsAlsoAMarketAsMarketOnly(t *testing.T) {
	h := newHarness()
	h.catalog.markets = []string{"X1-AA11-Y1"}
	h.catalog.yards = []string{"X1-AA11-Y1"}
	h.goods.goods["X1-AA11-Y1"] = []string{"IRON_ORE"}

	result := h.screen(t, whitelistOf("IRON_ORE"))

	want := []PlannedSlot{{
		Waypoint:       "X1-AA11-Y1",
		Kind:           SlotKindMarket,
		WhitelistGoods: []string{"IRON_ORE"},
	}}
	if !reflect.DeepEqual(result.Slots, want) {
		t.Fatalf("Slots =\n %+v\nwant exactly one MARKET slot at the yard waypoint %+v", result.Slots, want)
	}
	// And it is the yard waypoint that got covered, so a waypoint-wise presence
	// check finds it.
	if h.ledger.slots[0].Waypoint != "X1-AA11-Y1" || h.ledger.slots[0].Kind != SlotKindMarket {
		t.Fatalf("persisted slot = %+v, want a MARKET slot at the yard waypoint", h.ledger.slots[0])
	}
}

// --- depth -------------------------------------------------------------------

// Depth is Σ trade_volume × mid-price over the WHITELISTED goods only, and it
// orders slots without deciding them.
func TestScreenSystemComputesDepthOverWhitelistedGoodsOnly(t *testing.T) {
	h := newHarness()
	h.catalog.markets = []string{"X1-AA11-A1"}
	h.goods.goods["X1-AA11-A1"] = []string{"IRON_ORE", "FUEL"}
	h.goods.depth["X1-AA11-A1"] = []scouting.MarketDepthRow{
		{Good: "IRON_ORE", TradeVolume: 10, MidPrice: 100}, // 1000
		{Good: "FUEL", TradeVolume: 500, MidPrice: 3},      // excluded
	}

	result := h.screen(t, whitelistOf("IRON_ORE"))

	if result.Slots[0].DepthCredits != 1000 {
		t.Fatalf("DepthCredits = %d, want 1000", result.Slots[0].DepthCredits)
	}
}

// A remote-fetched market has goods but no prices — depth 0 is a blind prior,
// not a claim the market is worthless.
func TestScreenSystemGivesUnpricedMarketZeroDepth(t *testing.T) {
	h := newHarness()
	h.catalog.markets = []string{"X1-AA11-A1"}
	h.remote.goods["X1-AA11-A1"] = []string{"IRON_ORE"}

	result := h.screen(t, whitelistOf("IRON_ORE"))

	if result.Slots[0].DepthCredits != 0 {
		t.Fatalf("DepthCredits = %d, want 0 for an unpriced market", result.Slots[0].DepthCredits)
	}
}

// Garbled rows contribute nothing rather than negative or nonsense depth.
func TestScreenSystemIgnoresNonPositiveDepthRows(t *testing.T) {
	h := newHarness()
	h.catalog.markets = []string{"X1-AA11-A1"}
	h.goods.goods["X1-AA11-A1"] = []string{"IRON_ORE"}
	h.goods.depth["X1-AA11-A1"] = []scouting.MarketDepthRow{
		{Good: "IRON_ORE", TradeVolume: -5, MidPrice: 100},
		{Good: "IRON_ORE", TradeVolume: 10, MidPrice: 0},
		{Good: "IRON_ORE", TradeVolume: 2, MidPrice: 50}, // 100
	}

	result := h.screen(t, whitelistOf("IRON_ORE"))

	if result.Slots[0].DepthCredits != 100 {
		t.Fatalf("DepthCredits = %d, want 100", result.Slots[0].DepthCredits)
	}
}

// --- ledger writes -----------------------------------------------------------

// The verdict is recorded with the system's depth and uncharted count, so the
// screen's decision survives a restart.
func TestScreenSystemRecordsVerdict(t *testing.T) {
	h := newHarness()
	h.catalog.markets = []string{"X1-AA11-A1"}
	h.catalog.uncharted = 4
	h.goods.goods["X1-AA11-A1"] = []string{"IRON_ORE"}
	h.goods.depth["X1-AA11-A1"] = []scouting.MarketDepthRow{
		{Good: "IRON_ORE", TradeVolume: 10, MidPrice: 100},
	}

	h.screen(t, whitelistOf("IRON_ORE"))

	want := []SystemRecord{{
		System:         "X1-AA11",
		Verdict:        VerdictInScope,
		UnchartedCount: 4,
		DepthCredits:   1000,
		CatalogKnown:   true,
	}}
	if !reflect.DeepEqual(h.ledger.systems, want) {
		t.Fatalf("recorded systems =\n %+v\nwant %+v", h.ledger.systems, want)
	}
}

// A waypoint that already carries a slot is never re-upserted: the ledger row
// holds the live state and the assigned hull, and rewriting it as WANTED would
// drop the hull out of the probe-cap count and authorise buying it twice.
func TestScreenSystemDoesNotRewriteExistingSlots(t *testing.T) {
	h := newHarness()
	h.catalog.markets = []string{"X1-AA11-A1", "X1-AA11-A2"}
	h.goods.goods["X1-AA11-A1"] = []string{"IRON_ORE"}
	h.goods.goods["X1-AA11-A2"] = []string{"IRON_ORE"}
	h.ledger.existing = []ExistingSlot{{Waypoint: "X1-AA11-A1", WhitelistGoods: []string{"IRON_ORE"}}}

	result := h.screen(t, whitelistOf("IRON_ORE"))

	if len(h.ledger.slots) != 1 || h.ledger.slots[0].Waypoint != "X1-AA11-A2" {
		t.Fatalf("expected only the new waypoint to be written, got %+v", h.ledger.slots)
	}
	// The plan still describes the whole desired placement set.
	if len(result.Slots) != 2 {
		t.Fatalf("expected both placements in the plan, got %+v", result.Slots)
	}
}

// --- ordering & errors -------------------------------------------------------

// Slots come back in a stable order regardless of catalogue order, so repeated
// screens produce the same plan.
func TestScreenSystemOrdersMarketSlotsByWaypoint(t *testing.T) {
	h := newHarness()
	h.catalog.markets = []string{"X1-AA11-C1", "X1-AA11-A1", "X1-AA11-B1"}
	for _, waypoint := range h.catalog.markets {
		h.goods.goods[waypoint] = []string{"IRON_ORE"}
	}

	result := h.screen(t, whitelistOf("IRON_ORE"))

	got := make([]string, 0, len(result.Slots))
	for _, slot := range result.Slots {
		got = append(got, slot.Waypoint)
	}
	want := []string{"X1-AA11-A1", "X1-AA11-B1", "X1-AA11-C1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("slot order = %v, want %v", got, want)
	}
}

// Matched goods are ordered too — they are persisted as the slot's goods list.
func TestScreenSystemOrdersMatchedGoods(t *testing.T) {
	h := newHarness()
	h.catalog.markets = []string{"X1-AA11-A1"}
	h.goods.goods["X1-AA11-A1"] = []string{"IRON_ORE", "FUEL", "COPPER_ORE"}

	result := h.screen(t, whitelistOf("IRON_ORE", "COPPER_ORE"))

	want := []string{"COPPER_ORE", "IRON_ORE"}
	if !reflect.DeepEqual(result.Slots[0].WhitelistGoods, want) {
		t.Fatalf("WhitelistGoods = %v, want %v", result.Slots[0].WhitelistGoods, want)
	}
}

// An empty whitelist is a caller bug, not a finding about the system. Screening
// against it would match nothing, stamp NO_WHITELIST — a DURABLE verdict, since
// only PENDING systems are re-screened — and permanently write off systems on
// the strength of a config that was briefly empty. Refuse the tick and write
// NOTHING; the caller logs and skips.
func TestScreenSystemEmptyWhitelistIsRefusedAndWritesNothing(t *testing.T) {
	for _, whitelist := range []map[string]bool{nil, {}} {
		h := newHarness()
		h.catalog.markets = []string{"X1-AA11-A1"}
		h.goods.goods["X1-AA11-A1"] = []string{"IRON_ORE"}

		_, err := ScreenSystem(context.Background(), h.ports(), testPlayerID, "X1-AA11", whitelist)

		if !errors.Is(err, ErrEmptyWhitelist) {
			t.Fatalf("err = %v, want ErrEmptyWhitelist", err)
		}
		if len(h.ledger.systems) != 0 {
			t.Fatalf("an empty whitelist must record NO verdict, got %+v", h.ledger.systems)
		}
		if len(h.ledger.slots) != 0 {
			t.Fatalf("an empty whitelist must record NO slots, got %+v", h.ledger.slots)
		}
	}
}

// PENDING is a real verdict and must be durably recorded — it is what marks the
// system for re-screening later.
func TestScreenSystemRecordsPendingVerdict(t *testing.T) {
	h := newHarness()
	h.catalog.markets = []string{"X1-AA11-A1"}
	h.remote.err = errors.New("boom")

	h.screen(t, whitelistOf("IRON_ORE"))

	want := []SystemRecord{{System: "X1-AA11", Verdict: VerdictPending, CatalogKnown: true}}
	if !reflect.DeepEqual(h.ledger.systems, want) {
		t.Fatalf("recorded systems =\n %+v\nwant %+v", h.ledger.systems, want)
	}
}

// A system whose waypoint list has never been swept reads EXACTLY like one that
// is charted through and deals in nothing: no markets, nothing uncharted,
// nothing unresolved. Rejecting it would write off an unexplored system on
// evidence that is simply absent — durably, since only PENDING systems are
// re-screened, and contagiously, since NO_WHITELIST systems are propagation
// origins for the expansion frontier.
func TestScreenSystemNeverRejectsASystemItHasNeverSwept(t *testing.T) {
	h := newHarness()
	h.catalog.catalogUnknown = true
	// No markets, nothing uncharted — the empty readings of a system nobody has
	// visited.

	result := h.screen(t, whitelistOf("IRON_ORE"))

	if result.Verdict != VerdictPending {
		t.Fatalf("Verdict = %q, want PENDING — absence of evidence is not evidence of absence", result.Verdict)
	}
	want := []SystemRecord{{System: "X1-AA11", Verdict: VerdictPending, CatalogKnown: false}}
	if !reflect.DeepEqual(h.ledger.systems, want) {
		t.Fatalf("recorded systems =\n %+v\nwant %+v", h.ledger.systems, want)
	}
}

// The catalog signal gates only the REJECTION. A market we can already see is a
// reason to place a probe whether or not the rest of the system has been swept —
// exactly as an uncharted count does not hold IN_SCOPE back.
func TestScreenSystemStillGoesInScopeWithAnUnsweptCatalog(t *testing.T) {
	h := newHarness()
	h.catalog.catalogUnknown = true
	h.catalog.markets = []string{"X1-AA11-A1"}
	h.goods.goods["X1-AA11-A1"] = []string{"IRON_ORE"}

	result := h.screen(t, whitelistOf("IRON_ORE"))

	if result.Verdict != VerdictInScope {
		t.Fatalf("Verdict = %q, want IN_SCOPE", result.Verdict)
	}
}

// An unreadable catalog signal fails the screen rather than guessing. Guessing
// "known" is the direction that writes off a system on no evidence at all.
//
// Only the catalog read is broken here — every other read succeeds — so the
// failure can ONLY come from this branch. Reusing the fake's shared error field
// would break ListMarketWaypoints first and make this a duplicate of
// TestScreenSystemPropagatesCatalogError.
func TestScreenSystemFailsWhenTheCatalogSignalIsUnreadable(t *testing.T) {
	h := newHarness()
	h.catalog.markets = []string{"X1-AA11-A1"}
	h.goods.goods["X1-AA11-A1"] = []string{"COPPER"} // resolves, matches nothing
	h.catalog.catalogKnownErr = errors.New("catalog signal unreadable")

	_, err := ScreenSystem(context.Background(), h.ports(), testPlayerID, "X1-AA11", whitelistOf("IRON_ORE"))
	if err == nil {
		t.Fatal("an unreadable catalog signal must fail the screen")
	}
	if len(h.ledger.systems) != 0 {
		t.Fatalf("no verdict may be recorded, got %+v", h.ledger.systems)
	}
}

// A legitimate rejection — a real whitelist, charted through, nothing matched —
// IS recorded, so the system stops being re-screened.
func TestScreenSystemRecordsNoWhitelistVerdict(t *testing.T) {
	h := newHarness()
	h.catalog.markets = []string{"X1-AA11-A1"}
	h.goods.goods["X1-AA11-A1"] = []string{"FUEL"}

	h.screen(t, whitelistOf("IRON_ORE"))

	want := []SystemRecord{{System: "X1-AA11", Verdict: VerdictNoWhitelist, CatalogKnown: true}}
	if !reflect.DeepEqual(h.ledger.systems, want) {
		t.Fatalf("recorded systems =\n %+v\nwant %+v", h.ledger.systems, want)
	}
}

func TestScreenSystemPropagatesCatalogError(t *testing.T) {
	h := newHarness()
	h.catalog.err = errors.New("catalogue down")

	if _, err := ScreenSystem(context.Background(), h.ports(), testPlayerID, "X1-AA11", whitelistOf("IRON_ORE")); err == nil {
		t.Fatal("expected the catalogue error to propagate")
	}
}

func TestScreenSystemPropagatesLedgerError(t *testing.T) {
	h := newHarness()
	h.catalog.markets = []string{"X1-AA11-A1"}
	h.goods.goods["X1-AA11-A1"] = []string{"IRON_ORE"}
	h.ledger.err = errors.New("ledger down")

	if _, err := ScreenSystem(context.Background(), h.ports(), testPlayerID, "X1-AA11", whitelistOf("IRON_ORE")); err == nil {
		t.Fatal("expected the ledger error to propagate")
	}
}

// --- quartermaster coverage at heavy-selling yards (sp-fwk8z T3) --------------

// A system whose shipyard sells HEAVIES but no probes still earns a YARD slot: a parked
// quartermaster there is what makes a future heavy purchase instant instead of requiring a hull to
// fly in first.
func TestPlanSlots_HeavyOnlyYardStillEarnsASlot(t *testing.T) {
	cat := &fakeCatalog{heavyYards: []string{"X1-AA-H1"}}
	slots, err := planSlots(context.Background(), ScreenPorts{Waypoints: cat}, "X1-AA", nil)
	if err != nil {
		t.Fatalf("planSlots error: %v", err)
	}
	if len(slots) != 1 {
		t.Fatalf("a heavy-only yard must earn one slot, got %d (%+v)", len(slots), slots)
	}
	if slots[0].Waypoint != "X1-AA-H1" || slots[0].Kind != SlotKindYard {
		t.Fatalf("expected a YARD slot at the heavy yard, got %+v", slots[0])
	}
}

// THE COLLISION CONTRACT. A yard that is ALSO a whitelisted market gets exactly ONE slot, and it is
// the MARKET one — the ledger is waypoint-keyed, so a second slot would overwrite rather than add,
// and a YARD slot would have to drop the goods list that is the reason the waypoint is watched.
// Consumers asking "is there a probe at this yard?" match on waypoint + PARKED, never on kind.
func TestPlanSlots_HeavyYardCoLocatedWithAMarketClaimsNoSecondRow(t *testing.T) {
	cat := &fakeCatalog{heavyYards: []string{"X1-AA-M1"}}
	hits := []screenedMarket{{waypoint: "X1-AA-M1", goods: []string{"FUEL"}, depth: 1000}}

	slots, err := planSlots(context.Background(), ScreenPorts{Waypoints: cat}, "X1-AA", hits)
	if err != nil {
		t.Fatalf("planSlots error: %v", err)
	}
	if len(slots) != 1 {
		t.Fatalf("a heavy yard co-located with a market must not claim a second row, got %d slots (%+v)", len(slots), slots)
	}
	if slots[0].Kind != SlotKindMarket {
		t.Fatalf("MARKET must win the collision, got kind %q", slots[0].Kind)
	}
	if len(slots[0].WhitelistGoods) != 1 {
		t.Fatalf("the winning MARKET slot must keep its goods list, got %+v", slots[0])
	}
}

// PRECEDENCE IS AN ORDERING, NOT AN EXCLUSION — and this test used to assert the
// exclusion, which was the defect.
//
// Probes are what sensing actually buys, so a probe yard is still the FIRST
// placement the system offers and the probe-priced cheapest-first order is
// untouched. What is no longer true is that offering a probe yard hides the heavy
// list entirely: the heavy read used to happen only when the system sold no probe
// at all, so a system selling both never consulted it. Measured live, X1-QR78-AE4F
// sells probes AND heavy freighters, so X1-QR78 never looked at its heavy list and
// its second heavy yard X1-QR78-FE8C — which sells no probe, so nothing else would
// ever place there — was invisible as a heavy yard while the fleet hunted one.
func TestPlanSlots_HeavyYardIsConsideredEvenWhenTheSystemSellsProbes(t *testing.T) {
	cat := &fakeCatalog{yards: []string{"X1-AA-P1"}, heavyYards: []string{"X1-AA-H1"}}
	slots, err := planSlots(context.Background(), ScreenPorts{Waypoints: cat}, "X1-AA", nil)
	if err != nil {
		t.Fatalf("planSlots error: %v", err)
	}
	want := []PlannedSlot{
		{Waypoint: "X1-AA-P1", Kind: SlotKindYard},
		{Waypoint: "X1-AA-H1", Kind: SlotKindYard},
	}
	if !reflect.DeepEqual(slots, want) {
		t.Fatalf("Slots =\n %+v\nwant the probe yard FIRST and the heavy yard still covered %+v", slots, want)
	}
}

// A waypoint that sells BOTH classes appears in both catalogue lists and still
// claims exactly ONE slot, in its probe-priced position. The ledger keys a slot on
// (waypoint, kind), so a second YARD row here would not add a placement — but it
// would be a second WANTED placement for one counter, which is a second probe
// bought for a waypoint already covered.
func TestPlanSlots_AYardSellingBothClassesClaimsOneSlot(t *testing.T) {
	cat := &fakeCatalog{
		yards:      []string{"X1-AA-P1", "X1-AA-BOTH"},
		heavyYards: []string{"X1-AA-BOTH", "X1-AA-H1"},
	}
	slots, err := planSlots(context.Background(), ScreenPorts{Waypoints: cat}, "X1-AA", nil)
	if err != nil {
		t.Fatalf("planSlots error: %v", err)
	}
	want := []PlannedSlot{
		{Waypoint: "X1-AA-P1", Kind: SlotKindYard},
		{Waypoint: "X1-AA-BOTH", Kind: SlotKindYard},
		{Waypoint: "X1-AA-H1", Kind: SlotKindYard},
	}
	if !reflect.DeepEqual(slots, want) {
		t.Fatalf("Slots =\n %+v\nwant one slot per waypoint, dual-class yard in its probe position %+v", slots, want)
	}
}
