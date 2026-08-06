package services

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// A good with no recipe entry, or an entry with no inputs, is RAW: it terminates recursion and
// must be bought rather than fabricated. This is the SECOND half of the rule; the first half is
// the curated goods.IsMineableRawMaterial list, covered against the real recipe map in
// gate_topology_raw_termination_test.go.
//
// This fixture cannot exercise that half — it omits IRON_ORE, so IRON_ORE is raw here by absence
// rather than by curation, and the case below stays green under either rule. That is precisely
// why the curated half needs the real map to test it.
//
// NOTE: this comment previously claimed the rule "replaces the deleted fabricate depth cap —
// recursion is bounded by the DAG". The recipe map is CYCLIC and the cap is NOT deleted. See
// IsRaw's doc comment.
func TestGateTopology_IsRaw(t *testing.T) {
	chain := map[string][]string{
		"FAB_MATS":           {"IRON", "QUARTZ_SAND"},
		"IRON":               {"IRON_ORE"},
		"ADVANCED_CIRCUITRY": {"COPPER", "SILICON"},
		"EMPTY_RECIPE":       {},
	}
	topo := NewGateTopology(nil, chain)

	cases := []struct {
		good string
		want bool
	}{
		{"FAB_MATS", false},
		{"IRON", false},
		{"IRON_ORE", true},     // absent from the map entirely
		{"EMPTY_RECIPE", true}, // present but with no inputs
	}
	for _, tc := range cases {
		if got := topo.IsRaw(tc.good); got != tc.want {
			t.Fatalf("IsRaw(%q) = %v, want %v", tc.good, got, tc.want)
		}
	}
}

func TestGateTopology_InputsReturnsNilForRawGoods(t *testing.T) {
	topo := NewGateTopology(nil, map[string][]string{"FAB_MATS": {"IRON", "QUARTZ_SAND"}})

	if got := topo.Inputs("FAB_MATS"); len(got) != 2 || got[0] != "IRON" || got[1] != "QUARTZ_SAND" {
		t.Fatalf("Inputs(FAB_MATS) = %v, want [IRON QUARTZ_SAND]", got)
	}
	if got := topo.Inputs("IRON_ORE"); got != nil {
		t.Fatalf("Inputs(IRON_ORE) = %v, want nil for a raw good", got)
	}

	// A present-but-empty recipe entry is the case where the raw guard is load-bearing.
	// The IRON_ORE case above cannot prove it: an ABSENT key already yields nil from a
	// bare map lookup, so deleting the guard leaves that assertion green (verified by
	// mutation). Only a stored empty slice distinguishes them — a bare lookup returns it
	// non-nil, which would break the invariant the recursion relies on: IsRaw(g) is true
	// exactly when Inputs(g) is nil.
	emptyRecipe := NewGateTopology(nil, map[string][]string{"EMPTY_RECIPE": {}})
	if got := emptyRecipe.Inputs("EMPTY_RECIPE"); got != nil {
		t.Fatalf("Inputs(EMPTY_RECIPE) = %v, want nil for a recipe entry with no inputs", got)
	}
}

// Inputs must hand back a copy, never the recipe map's own backing array. supplyChainMap is
// shared with SupplyChainResolver, so a caller that sorts, reverses, or index-assigns the
// result would corrupt recipe data process-wide — and the DAG walk in later phases is exactly
// that kind of caller. Returning the live slice is unsafe by construction: append() only
// happens to be harmless while every entry is a cap==len composite literal, which is a
// property of the construction site, not a guarantee this type can make.
func TestGateTopology_InputsReturnsDefensiveCopy(t *testing.T) {
	chain := map[string][]string{"FAB_MATS": {"IRON", "QUARTZ_SAND"}}
	topo := NewGateTopology(nil, chain)

	first := topo.Inputs("FAB_MATS")
	first[0] = "CORRUPTED_BY_CALLER"

	if got := topo.Inputs("FAB_MATS"); got[0] != "IRON" {
		t.Fatalf("Inputs(FAB_MATS)[0] = %q after a caller mutated an earlier result, want %q", got[0], "IRON")
	}
	if chain["FAB_MATS"][0] != "IRON" {
		t.Fatalf("caller mutation reached the shared recipe map: chain[FAB_MATS] = %v, want [IRON QUARTZ_SAND]", chain["FAB_MATS"])
	}
}

// fakeMarkets is a controllable marketResolver. It can express BOTH not-found conventions,
// because the interface permits both and the two are not interchangeable in these tests:
//
//   - exportErr/importErr: good -> error. This is the PRODUCTION convention. Every return in
//     *MarketLocator.FindExportMarket yields (nil, error) when nothing matches — including the
//     isShipType detour into findShipyardSellingShip, whose five returns are (nil, error) at
//     "no yard reader wired", "failed to find shipyards", "no shipyards in system" and "no
//     shipyard selling X", with the only success return guarded non-nil. FindImportMarket is
//     the same shape. So the real locator can NEVER return (nil, nil): a good that nothing
//     exports arrives here as an error.
//   - exports/imports: good -> result, where a missing entry yields (nil, nil). No production
//     implementation produces this today. It models the convention used by sibling locators in
//     the same file (FindExportMarketWithGoodSupply returns nil, nil explicitly) and which any
//     future marketResolver implementation is free to adopt.
//
// The blanket err field short-circuits both lookups and stands for an infrastructure outage
// rather than an absent market.
type fakeMarkets struct {
	exports   map[string]*MarketLocatorResult
	imports   map[string]*MarketLocatorResult
	exportErr map[string]error
	importErr map[string]error
	err       error
}

func (f *fakeMarkets) FindExportMarket(_ context.Context, good, _ string, _ int) (*MarketLocatorResult, error) {
	if f.err != nil {
		return nil, f.err
	}
	if err := f.exportErr[good]; err != nil {
		return nil, err
	}
	return f.exports[good], nil
}

func (f *fakeMarkets) FindImportMarket(_ context.Context, good, _ string, _ int) (*MarketLocatorResult, error) {
	if f.err != nil {
		return nil, f.err
	}
	if err := f.importErr[good]; err != nil {
		return nil, err
	}
	return f.imports[good], nil
}

// The terminal factory is whatever EXPORTS the good this era. The test asserts the
// waypoint came from market data, never from a constant.
func TestGateTopology_TerminalFactoryResolvesTheExporter(t *testing.T) {
	markets := &fakeMarkets{exports: map[string]*MarketLocatorResult{
		"FAB_MATS": {WaypointSymbol: "ERA7-WP-ALPHA", Supply: "HIGH", TradeVolume: 20},
	}}
	topo := NewGateTopology(markets, map[string][]string{"FAB_MATS": {"IRON"}})

	got, err := topo.TerminalFactory(context.Background(), "FAB_MATS", "ERA7-SYS", 1)
	if err != nil {
		t.Fatalf("TerminalFactory returned error: %v", err)
	}
	if got == nil || got.WaypointSymbol != "ERA7-WP-ALPHA" {
		t.Fatalf("TerminalFactory = %+v, want the exporting waypoint ERA7-WP-ALPHA", got)
	}
}

// THE PRODUCTION REFUSAL PATH. "Nothing in this system exports FAB_MATS this era" is the
// business outcome this whole seam exists to handle, and the real locator reports it as an
// ERROR (see fakeMarkets: no path in FindExportMarket returns (nil, nil)). So this — not the
// nil-result test below — is what a genuinely absent factory looks like at the seam.
//
// The refusal is the point: gate construction must stop rather than proceed against some
// other waypoint, because substituting one is how feedstock ends up where it cannot be used.
func TestGateTopology_TerminalFactoryRefusesWhenLocatorReportsNoExporter(t *testing.T) {
	noExporter := errors.New("no export or exchange market found selling FAB_MATS in ERA7-SYS")
	topo := NewGateTopology(
		&fakeMarkets{exportErr: map[string]error{"FAB_MATS": noExporter}},
		map[string][]string{"FAB_MATS": {"IRON"}},
	)

	got, err := topo.TerminalFactory(context.Background(), "FAB_MATS", "ERA7-SYS", 1)
	if err == nil {
		t.Fatalf("TerminalFactory = %+v, nil error; want a refusal when the locator reports no exporter", got)
	}
	if got != nil {
		t.Fatalf("TerminalFactory returned %+v alongside an error; want nil", got)
	}
	if !errors.Is(err, noExporter) {
		t.Fatalf("TerminalFactory error = %v, want it to wrap the locator's %v", err, noExporter)
	}
}

// This pins the DEFENSIVE branch, not the production path: no current implementation of
// marketResolver returns (nil, nil), so this scenario is unreachable through *MarketLocator
// today. It is kept deliberately. The interface permits the convention, sibling locators in
// market_locator.go already use it, and a future implementer adopting it must still meet a
// REFUSAL here rather than have a nil result handed back to a caller that would dereference
// it. Deleting this test is what would make the result == nil guard look dead.
func TestGateTopology_TerminalFactoryRefusesWhenNoExporter(t *testing.T) {
	topo := NewGateTopology(&fakeMarkets{exports: map[string]*MarketLocatorResult{}},
		map[string][]string{"FAB_MATS": {"IRON"}})

	got, err := topo.TerminalFactory(context.Background(), "FAB_MATS", "ERA7-SYS", 1)
	if err == nil {
		t.Fatalf("TerminalFactory = %+v, nil error; want a refusal when nothing exports the good", got)
	}
	if got != nil {
		t.Fatalf("TerminalFactory returned %+v alongside an error; want nil", got)
	}
}

// An infrastructure failure (repo down) propagates rather than being swallowed into a
// "no exporter" verdict — an outage must not be mistaken for a factual absence of a factory.
// The contract is error AND nil result: a caller that checks only one of the two must not be
// handed a half-valid pair.
func TestGateTopology_TerminalFactoryPropagatesLocatorError(t *testing.T) {
	boom := errors.New("market repo down")
	topo := NewGateTopology(&fakeMarkets{err: boom}, map[string][]string{"FAB_MATS": {"IRON"}})

	got, err := topo.TerminalFactory(context.Background(), "FAB_MATS", "ERA7-SYS", 1)
	if !errors.Is(err, boom) {
		t.Fatalf("TerminalFactory error = %v, want it to wrap %v", err, boom)
	}
	if got != nil {
		t.Fatalf("TerminalFactory returned %+v alongside an error; want nil", got)
	}
}

// A feed target must IMPORT the good. This is the sp-b27a2 guard: that bug dispatched
// IRON_ORE to a waypoint which only imported other goods, stranding haulers at full
// cargo with feedstock they could neither deliver nor dump.
//
// The fixture is arranged so that resolving by the WRONG role cannot pass by accident. The
// only export on offer is a good FeedTarget was not asked about, so a FindExportMarket call
// here finds nothing and the seam refuses — the test fails loudly instead of quietly
// returning a plausible waypoint.
func TestGateTopology_FeedTargetResolvesTheImporter(t *testing.T) {
	markets := &fakeMarkets{
		imports: map[string]*MarketLocatorResult{
			"IRON_ORE": {WaypointSymbol: "ERA7-WP-SMELTER", Supply: "LIMITED"},
		},
		// The circuitry exporter does NOT import IRON_ORE — resolving by export role
		// would have picked it and stranded the cargo.
		exports: map[string]*MarketLocatorResult{
			"ADVANCED_CIRCUITRY": {WaypointSymbol: "ERA7-WP-CIRCUITS"},
		},
	}
	topo := NewGateTopology(markets, map[string][]string{"IRON": {"IRON_ORE"}})

	got, err := topo.FeedTarget(context.Background(), "IRON_ORE", "ERA7-SYS", 1)
	if err != nil {
		t.Fatalf("FeedTarget returned error: %v", err)
	}
	if got == nil || got.WaypointSymbol != "ERA7-WP-SMELTER" {
		t.Fatalf("FeedTarget = %+v, want the IMPORTING waypoint ERA7-WP-SMELTER", got)
	}
}

// THE PRODUCTION REFUSAL PATH, and the exact sp-b27a2 condition. "Nothing in this system
// imports IRON_ORE" is reported by the real locator as an ERROR: *MarketLocator.FindImportMarket
// returns (nil, fmt.Errorf("no market found importing %s", good)) when the repo yields no buying
// market, and every other return on that method is likewise (nil, error) or a non-nil success.
// So this — not the nil-result test below — is what an absent importer looks like at the seam
// today, and it is the branch that had to fail closed for the incident not to happen.
//
// FAIL CLOSED: no importer means no dispatch. Returning any waypoint here would recreate the
// stranding, because the haulers arrive holding cargo the destination will not buy.
func TestGateTopology_FeedTargetRefusesWhenLocatorReportsNoImporter(t *testing.T) {
	noImporter := errors.New("no market found importing IRON_ORE")
	topo := NewGateTopology(
		&fakeMarkets{importErr: map[string]error{"IRON_ORE": noImporter}},
		map[string][]string{"IRON": {"IRON_ORE"}},
	)

	got, err := topo.FeedTarget(context.Background(), "IRON_ORE", "ERA7-SYS", 1)
	if err == nil {
		t.Fatalf("FeedTarget = %+v, nil error; want a refusal when the locator reports no importer", got)
	}
	if got != nil {
		t.Fatalf("FeedTarget returned %+v alongside an error; want nil so no dispatch can occur", got)
	}
	if !errors.Is(err, noImporter) {
		t.Fatalf("FeedTarget error = %v, want it to wrap the locator's %v", err, noImporter)
	}
}

// This pins the DEFENSIVE branch, not the production path: no current implementation of
// marketResolver returns (nil, nil), so this scenario is unreachable through *MarketLocator
// today. It is kept deliberately, on the same reasoning as its TerminalFactory twin — the
// interface permits the convention and sibling locators in market_locator.go already use it
// (FindExportMarketWithGoodSupply returns nil, nil explicitly), so a future implementer
// adopting it must still meet a REFUSAL here rather than hand a nil result to a caller that
// would dereference it. Deleting this test is what would make the result == nil guard look dead.
//
// FAIL CLOSED: no importer means no dispatch, whichever way the absence is signalled.
func TestGateTopology_FeedTargetRefusesWhenNothingImportsTheGood(t *testing.T) {
	markets := &fakeMarkets{
		imports: map[string]*MarketLocatorResult{},
		exports: map[string]*MarketLocatorResult{
			"ADVANCED_CIRCUITRY": {WaypointSymbol: "ERA7-WP-CIRCUITS"},
		},
	}
	topo := NewGateTopology(markets, map[string][]string{"IRON": {"IRON_ORE"}})

	got, err := topo.FeedTarget(context.Background(), "IRON_ORE", "ERA7-SYS", 1)
	if err == nil {
		t.Fatalf("FeedTarget = %+v, nil error; want a refusal when nothing imports the good", got)
	}
	if got != nil {
		t.Fatalf("FeedTarget returned %+v alongside an error; want nil so no dispatch can occur", got)
	}
}

// An infrastructure failure propagates rather than being reduced to a "no importer" verdict.
// FeedTarget refuses either way — which is correct today — but the wrapped cause must survive
// so an operator reading the log can tell an outage from a system that genuinely has no
// consumer for the good.
func TestGateTopology_FeedTargetPropagatesLocatorError(t *testing.T) {
	boom := errors.New("market repo down")
	topo := NewGateTopology(&fakeMarkets{err: boom}, map[string][]string{"IRON": {"IRON_ORE"}})

	got, err := topo.FeedTarget(context.Background(), "IRON_ORE", "ERA7-SYS", 1)
	if !errors.Is(err, boom) {
		t.Fatalf("FeedTarget error = %v, want it to wrap %v", err, boom)
	}
	if got != nil {
		t.Fatalf("FeedTarget returned %+v alongside an error; want nil", got)
	}
}

// tradeGoodListing builds one listing row. Prices and volume are irrelevant to
// ValidateFeedDestination — it reads the trade TYPE only — so they are fixed constants and
// carry no meaning a reader should try to interpret.
func tradeGoodListing(t *testing.T, good string, tradeType market.TradeType) market.TradeGood {
	t.Helper()
	supply, activity := "MODERATE", "STRONG"
	listed, err := market.NewTradeGood(good, &supply, &activity, 10, 10, 20, tradeType)
	if err != nil {
		t.Fatalf("building a %s listing for %s: %v", tradeType, good, err)
	}
	return *listed
}

// feedListing builds a destination's market listing from the two roles that decide whether a
// hull can put cargo down there: an IMPORT is a consumer that buys the good, an EXPORT is the
// market's own product that it will not buy back.
func feedListing(t *testing.T, waypoint string, imports, exports []string) *market.Market {
	t.Helper()
	rows := make([]market.TradeGood, 0, len(imports)+len(exports))
	for _, good := range imports {
		rows = append(rows, tradeGoodListing(t, good, market.TradeTypeImport))
	}
	for _, good := range exports {
		rows = append(rows, tradeGoodListing(t, good, market.TradeTypeExport))
	}
	listing, err := market.NewMarket(waypoint, rows, time.Now())
	if err != nil {
		t.Fatalf("building the market listing for %s: %v", waypoint, err)
	}
	return listing
}

// A destination that will take every input off the hull is accepted. The EXCHANGE row is not
// decoration: a trader is a legitimate place to put cargo down even though it is not a factory,
// and accepting it keeps this guard in exact agreement with the marketBuys predicate that
// deliverInputs applies on arrival. Divergence between the two would be its own stranding —
// approving a navigate the delivery step then refuses.
//
// The empty-inputs row pins the ordering of the guards: with nothing being carried there is no
// cargo to strand, so an unreadable listing is not a reason to park the fabrication.
func TestGateTopology_ValidateFeedDestinationAcceptsADestinationThatTakesEveryInput(t *testing.T) {
	topo := NewGateTopology(nil, map[string][]string{"FAB_MATS": {"IRON", "QUARTZ_SAND"}})

	cases := []struct {
		name        string
		destination *market.Market
		inputs      []string
	}{
		{
			name:        "factory imports both inputs and exports its own product",
			destination: feedListing(t, "ERA7-WP-FABMILL", []string{"IRON", "QUARTZ_SAND"}, []string{"FAB_MATS"}),
			inputs:      []string{"IRON", "QUARTZ_SAND"},
		},
		{
			name: "an exchange trades the input rather than consuming it",
			destination: mustMarket(t, "ERA7-WP-EXCHANGE", []market.TradeGood{
				tradeGoodListing(t, "IRON", market.TradeTypeExchange),
			}),
			inputs: []string{"IRON"},
		},
		{
			name:        "nothing is being carried, so an unreadable listing strands nothing",
			destination: nil,
			inputs:      nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := topo.ValidateFeedDestination(tc.destination, "ERA7-WP-FABMILL", tc.inputs); err != nil {
				t.Fatalf("ValidateFeedDestination rejected a destination that takes every input: %v", err)
			}
		})
	}
}

func mustMarket(t *testing.T, waypoint string, rows []market.TradeGood) *market.Market {
	t.Helper()
	listing, err := market.NewMarket(waypoint, rows, time.Now())
	if err != nil {
		t.Fatalf("building the market listing for %s: %v", waypoint, err)
	}
	return listing
}

// THE sp-b27a2 CONDITION. IRON_ORE belongs to the FAB_MATS chain; the fabricate path carried it
// to the ADVANCED_CIRCUITRY exporter, which does not list it at all, and the hauler then sat at
// 80/80 unable to deliver OR dump. The refusal must NAME the offending good: this fires inside a
// coordinator whose only output is a log line, and "cannot feed that factory" without the good is
// not diagnosable.
func TestGateTopology_ValidateFeedDestinationRejectsAWrongChainFactory(t *testing.T) {
	topo := NewGateTopology(nil, map[string][]string{"IRON": {"IRON_ORE"}})
	circuitry := feedListing(t, "ERA7-WP-CIRCUITS",
		[]string{"ELECTRONICS", "MICROPROCESSORS"}, // its own chain's inputs — no FAB_MATS good among them
		[]string{"ADVANCED_CIRCUITRY"})

	err := topo.ValidateFeedDestination(circuitry, "ERA7-WP-CIRCUITS", []string{"IRON_ORE"})
	if err == nil {
		t.Fatal("ValidateFeedDestination accepted a factory that does not import IRON_ORE — this is the sp-b27a2 stranding")
	}
	if !strings.Contains(err.Error(), "IRON_ORE") {
		t.Fatalf("error %q does not name the offending good; the log must be diagnosable", err)
	}
}

// A destination that EXPORTS the input is a rejection too, and it is a different listing branch
// from the absent-good case above: the good is present, so a naive "is it listed?" check would
// wave it through. An export listing is the market's own product — selling into it ladders its own
// bid down and is exactly the dump-at-the-factory that deliverInputs already refuses on arrival.
func TestGateTopology_ValidateFeedDestinationRejectsAnInputTheDestinationOnlyExports(t *testing.T) {
	topo := NewGateTopology(nil, map[string][]string{"IRON": {"IRON_ORE"}})
	smelter := feedListing(t, "ERA7-WP-SMELTER", nil, []string{"IRON_ORE", "IRON"})

	err := topo.ValidateFeedDestination(smelter, "ERA7-WP-SMELTER", []string{"IRON_ORE"})
	if err == nil {
		t.Fatal("ValidateFeedDestination accepted a destination that only EXPORTS the input; it will not buy its own product back")
	}
	if !strings.Contains(err.Error(), "IRON_ORE") {
		t.Fatalf("error %q does not name the offending good; the log must be diagnosable", err)
	}
}

// FAIL CLOSED on an unreadable listing. This USED to be the opposite of what marketBuys answered
// for the same nil (it returned true, on the reasoning that a SELL which has already arrived
// spends nothing, so a data gap should not stall it, whereas this guards a NAVIGATE that has not
// happened and strands a loaded hull if it guesses wrong). sp-kdsrh closed that divergence: the
// arrival side fails closed as well, since an unreadable listing is exactly when an EXPORT cannot
// be told from an IMPORT, and offering a factory its own product is what the filter is for. Both
// sides refuse now. Refusing here still costs one skipped pass and the run retries.
func TestGateTopology_ValidateFeedDestinationRefusesAnUnreadableListing(t *testing.T) {
	topo := NewGateTopology(nil, map[string][]string{"FAB_MATS": {"IRON", "QUARTZ_SAND"}})

	err := topo.ValidateFeedDestination(nil, "ERA7-WP-FABMILL", []string{"IRON", "QUARTZ_SAND"})
	if err == nil {
		t.Fatal("ValidateFeedDestination accepted an unreadable market listing; want a refusal, since a blind navigate is what strands a loaded hull")
	}
	if !strings.Contains(err.Error(), "ERA7-WP-FABMILL") {
		t.Fatalf("error %q does not name the destination; the log must be diagnosable", err)
	}
}

// Waypoint symbols look like X1-AB12-C3. They are regenerated every era, so any literal
// in this layer is a bug that survives exactly until the next era rolls. This guard
// fails the build rather than letting the constraint decay into a comment.
//
// Scope is deliberately gate_topology.go only: the rest of the package predates this
// rule and is not in scope for phase 1.
func TestGateTopology_SourceContainsNoWaypointLiterals(t *testing.T) {
	src, err := os.ReadFile("gate_topology.go")
	if err != nil {
		t.Fatalf("reading gate_topology.go: %v", err)
	}

	waypointLiteral := regexp.MustCompile(`"[A-Z]\d+-[A-Z0-9]+-[A-Z0-9]+"`)
	if found := waypointLiteral.FindAllString(string(src), -1); len(found) > 0 {
		t.Fatalf("gate_topology.go contains hardcoded waypoint symbols %v — "+
			"resolve locations by market role instead", found)
	}

	// The guard must be able to fail. If the pattern cannot match a known-bad string,
	// a green result would be meaningless.
	//
	// Calibrating on ONE example would under-prove that. A regression that TIGHTENS the
	// pattern — say to [A-Z]\d+-[A-Z]{2}\d{2}-[A-Z]\d{2} — still matches X1-KP23-F46, so a
	// single-string check stays green while the guard silently goes blind to every other
	// shape. These four are the real shapes this repo actually uses: sector-numbered,
	// short system, wordy suffix, and an all-letter middle.
	for _, known := range []string{
		`x := "X1-KP23-F46"`,
		`"X1-UM5-J59"`,
		`"X1-DR-GATE"`,
		`"X1-BETA-MARKETPLACE"`,
	} {
		if !waypointLiteral.MatchString(known) {
			t.Fatalf("waypoint-literal pattern failed calibration on %s", known)
		}
	}
	if strings.Contains(string(src), "X1-") {
		t.Fatal("gate_topology.go references an X1- prefixed symbol")
	}
}
