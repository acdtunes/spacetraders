package services

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/tactics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// THE FACTORY ROLE'S TERMINAL. FeedFactory puts inputs INTO a factory at a PINNED destination —
// the waypoint the caller already resolved by market role. It never re-decides where to go, and
// it never spends: the buy is BuyAtTerminalFactory's job, so every money guard stays on one path.
//
// The fixtures are the sp-b27a2 ones (newFeedDestinationRun): a hull LOADED with SILICON_CRYSTALS
// and COPPER, and a factory whose IMPORT listings are the variable under test.

// The same-chain case, which is the overwhelming majority in production: the factory imports
// every input aboard, so the hull flies there and delivers.
func TestFeedFactory_DeliversTheInputsAtAFactoryThatImportsThem(t *testing.T) {
	executor, mediator, ship := newFeedDestinationRun(t, true)

	result, err := executor.FeedFactory(feedDestinationCtx(), ship,
		&MarketLocatorResult{WaypointSymbol: fdFactoryWP}, []string{"SILICON_CRYSTALS", "COPPER"}, 1, nil)
	if err != nil {
		t.Fatalf("feeding a factory that imports every input must not error: %v", err)
	}
	if result == nil || result.Refused {
		t.Fatalf("result = %+v, want an accepted feed", result)
	}
	if !mediator.navigatedToFactory() {
		t.Fatalf("the hull never reached %s", fdFactoryWP)
	}
	if mediator.sellCount() == 0 {
		t.Fatal("no input was delivered at a factory that imports every one of them")
	}
	if result.UnitsDelivered == 0 {
		t.Fatalf("result = %+v; UnitsDelivered is what an operator reads to tell a fed factory from a starved one — 'sold something' is not the same fact as 'sold 20 units'", result)
	}
	// ADDED (not in the brief): the result must name WHERE it fed. An operator reading a fleet of
	// feed legs cannot attribute a starved factory to a leg whose destination is blank, and the
	// zero value is indistinguishable from an unset field.
	if result.WaypointSymbol != fdFactoryWP {
		t.Fatalf("result.WaypointSymbol = %q, want %s — the leg must report which factory it fed", result.WaypointSymbol, fdFactoryWP)
	}
}

// THE sp-b27a2 GUARD, ON THE NEW PATH. The factory exports its output but imports none of the
// inputs aboard, so the hull would arrive with cargo it can neither deliver nor dump. Refuse the
// NAVIGATE — do not fly and hope, and never substitute another waypoint.
func TestFeedFactory_RefusesToFlyToADestinationThatCannotAcceptTheCargo(t *testing.T) {
	executor, mediator, ship := newFeedDestinationRun(t, false)

	result, err := executor.FeedFactory(feedDestinationCtx(), ship,
		&MarketLocatorResult{WaypointSymbol: fdFactoryWP}, []string{"SILICON_CRYSTALS", "COPPER"}, 1, nil)
	if err != nil {
		t.Fatalf("a refused destination parks the leg, it does not error it: %v", err)
	}
	if result == nil || !result.Refused {
		t.Fatalf("result = %+v, want Refused — a silently-skipped feed and a delivered one must not look the same", result)
	}
	if mediator.navigatedToFactory() {
		t.Fatalf("the hull was flown to %s, which imports none of its cargo — that is the sp-b27a2 stranding, reproduced on the factory-fleet path", fdFactoryWP)
	}
	if mediator.sellCount() != 0 {
		t.Fatalf("sold %d time(s) at a destination the leg refused to fly to", mediator.sellCount())
	}
}

// IT SPENDS NOTHING. The whole point of keeping the buy on BuyAtTerminalFactory is that there is
// exactly ONE spend primitive on this path; a purchase issued here would be a second one, outside
// the tranche loop's per-iteration, fail-closed money guards (RULINGS #4).
func TestFeedFactory_SpendsNothing(t *testing.T) {
	executor, mediator, ship := newFeedDestinationRun(t, true)

	result, err := executor.FeedFactory(feedDestinationCtx(), ship,
		&MarketLocatorResult{WaypointSymbol: fdFactoryWP}, []string{"SILICON_CRYSTALS", "COPPER"}, 1, nil)
	if err != nil {
		t.Fatalf("FeedFactory: %v", err)
	}
	// ADDED (not in the brief): a do-nothing FeedFactory spends nothing too, so "spent 0" is
	// vacuously true unless this run actually flew and delivered. Assert the work happened FIRST,
	// then assert it cost nothing — otherwise the zero below is a statement about an empty run.
	if !mediator.navigatedToFactory() || result == nil || result.UnitsDelivered == 0 {
		t.Fatalf("fixture is inert: navigated=%v result=%+v — a leg that did no work spends nothing whatever the implementation does, so the zero-spend claim below would prove nothing", mediator.navigatedToFactory(), result)
	}
	if spent := mediator.creditsSpent(); spent != 0 {
		t.Fatalf("FeedFactory spent %d credits; the buy belongs to BuyAtTerminalFactory so every money guard stays on ONE path", spent)
	}
}

// Refuses a caller bug rather than resolving around it. Picking a destination here would undo the
// role-based topology resolution the caller performed — the same pinning contract
// BuyAtTerminalFactory holds on the buy side.
func TestFeedFactory_RefusesAnUnresolvedOrEmptyRequest(t *testing.T) {
	executor, mediator, ship := newFeedDestinationRun(t, true)
	ctx := feedDestinationCtx()

	if _, err := executor.FeedFactory(ctx, ship, nil, []string{"COPPER"}, 1, nil); err == nil {
		t.Fatal("a nil destination must be refused, not resolved here")
	}
	if _, err := executor.FeedFactory(ctx, ship, &MarketLocatorResult{}, []string{"COPPER"}, 1, nil); err == nil {
		t.Fatal("a destination with no waypoint must be refused")
	}
	if _, err := executor.FeedFactory(ctx, ship, &MarketLocatorResult{WaypointSymbol: fdFactoryWP}, nil, 1, nil); err == nil {
		t.Fatal("a feed naming no inputs must be refused: ValidateFeedDestination accepts an empty input list, so this would fly a hull somewhere for no reason")
	}
	// ADDED (not in the brief): ship was the ONLY unvalidated parameter. A nil hull reaches
	// ShipSymbol() at the navigate, i.e. AFTER the destination guard — so it PANICKED on the
	// accepted path while its siblings returned clean errors, and returned cleanly on the refused
	// one. A caller bug must not be a daemon crash.
	if _, err := executor.FeedFactory(ctx, nil, &MarketLocatorResult{WaypointSymbol: fdFactoryWP}, []string{"COPPER"}, 1, nil); err == nil {
		t.Fatal("a nil ship must be refused with an error, not panic at the navigate")
	}
	if mediator.navigatedToFactory() {
		t.Fatal("a refused request must not move the hull")
	}
}

// ADDED (not in the brief). THE inputs-VS-HOLD BOUNDARY, which the doc previously read as though
// they were the same thing. The guard's subject is the named `inputs`, but deliverInputs offers
// the WHOLE HOLD — so what actually lands is decided good-by-good by the DESTINATION'S OWN
// LISTING, and both of marketBuys' refusal branches must hold:
//
//   - the factory's own EXPORT is refused (selling into its own bid ladders it down; this is
//     exactly why the gate FACTORY leg must flush a gate material at the SITE and cannot expect a
//     terminal factory to take it back), and
//   - a good the market does not list at all is refused.
//
// Without this, "pass the whole hold" and "pass what you bought" are indistinguishable at the call
// site, and a leg could quietly hand a factory cargo it was never meant to get.
func TestFeedFactory_OffersTheWholeHoldButTheDestinationsListingDecidesWhatLands(t *testing.T) {
	executor, mediator, ship := newStrayCargoFeedRun(t)

	// Only the two same-chain inputs are NAMED — naming the strays would make
	// ValidateFeedDestination refuse the whole trip, which is the other half of this contract.
	result, err := executor.FeedFactory(feedDestinationCtx(), ship,
		&MarketLocatorResult{WaypointSymbol: fdFactoryWP}, []string{"SILICON_CRYSTALS", "COPPER"}, 1, nil)
	if err != nil {
		t.Fatalf("stray cargo aboard must not veto a trip whose named inputs are all importable (sp-w2qg5): %v", err)
	}
	// BEHAVIOUR FIRST, so a t.Fatalf on the reported result can never render these unreachable.
	sold := mediator.soldGoods()
	if !mediator.navigatedToFactory() {
		t.Fatalf("the hull never reached %s, so nothing below says anything about what a factory would have taken", fdFactoryWP)
	}
	for _, want := range []string{"SILICON_CRYSTALS", "COPPER"} {
		if !containsGood(sold, want) {
			t.Fatalf("sold %v; the factory imports %s and the leg named it, so it must be delivered", sold, want)
		}
	}
	// fdOutput is the factory's OWN export; fdStrayGood is not listed there at all.
	for _, refused := range []string{fdOutput, fdStrayGood} {
		if containsGood(sold, refused) {
			t.Fatalf("sold %s at %s — it is not an import of that market, so the whole-hold offer must be filtered by the destination's listing, not dumped", refused, fdFactoryWP)
		}
	}
	if result.UnitsDelivered != 20 {
		t.Fatalf("UnitsDelivered = %d, want 20 (10 SILICON_CRYSTALS + 10 COPPER) — cargo the market refused must not be booked as fed", result.UnitsDelivered)
	}
}

// fdStrayGood is a good the fixture's factory does not list in ANY direction — the cargo a re-roled
// or restarted hull turns up carrying.
const fdStrayGood = "FAB_MATS"

// soldGoods is the goods actually sold, where sellCount asks only how many sells happened. A test
// about WHICH cargo a market took cannot be written against a count.
func (m *fdMediator) soldGoods() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.sold...)
}

// newStrayCargoFeedRun mirrors newFeedDestinationRun (same factory, same importing destination)
// with TWO extra goods aboard that the factory will not take: its own export, and a good it does
// not list. It builds its own rather than parameterising the shared helper so the sp-b27a2 fixture
// every existing test depends on is left untouched.
func newStrayCargoFeedRun(t *testing.T) (*ProductionExecutor, *fdMediator, *navigation.Ship) {
	t.Helper()
	repo := &fdMarketRepo{
		importsInputs: true,
		inputs: []feedInputSpec{
			{good: "SILICON_CRYSTALS", waypoint: "X1-FD-SILICON", supply: "MODERATE", tradeVolume: 100, ask: 10},
			{good: "COPPER", waypoint: "X1-FD-COPPER", supply: "MODERATE", tradeVolume: 100, ask: 10},
		},
	}
	shipRepo := &dockRaceShipRepo{
		location: fdOrigin, navStatus: navigation.NavStatusDocked,
		cargoCapacity: 400, cargoUnits: 32,
		cargoInventory: []*shared.CargoItem{
			{Symbol: "SILICON_CRYSTALS", Name: "SILICON_CRYSTALS", Units: 10},
			{Symbol: "COPPER", Name: "COPPER", Units: 10},
			{Symbol: fdOutput, Name: fdOutput, Units: 7},
			{Symbol: fdStrayGood, Name: fdStrayGood, Units: 5},
		},
	}
	mediator := &fdMediator{repo: shipRepo, dockHandler: tactics.NewDockShipHandler(shipRepo)}
	executor := NewProductionExecutorWithConfig(
		mediator, shipRepo, repo, NewMarketLocator(repo, nil, nil, nil), &dockRaceClock{},
		[]time.Duration{time.Millisecond}, nil,
	)
	return executor, mediator, shipRepo.buildShip()
}

// deliverInputs' new units figure must be the units actually SOLD, not the goods count. The
// fixture sells 10 SILICON_CRYSTALS + 10 COPPER, so a units-vs-goods confusion (2 vs 20) is
// visible here and nowhere else.
func TestFeedFactory_ReportsUnitsSoldNotGoodsSold(t *testing.T) {
	executor, _, ship := newFeedDestinationRun(t, true)

	result, err := executor.FeedFactory(feedDestinationCtx(), ship,
		&MarketLocatorResult{WaypointSymbol: fdFactoryWP}, []string{"SILICON_CRYSTALS", "COPPER"}, 1, nil)
	if err != nil {
		t.Fatalf("FeedFactory: %v", err)
	}
	if result.UnitsDelivered != 20 {
		t.Fatalf("UnitsDelivered = %d, want 20 (10 SILICON_CRYSTALS + 10 COPPER) — a goods count would read 2", result.UnitsDelivered)
	}
	// ADDED (not in the brief): NOTHING pinned Revenue, so an implementation that dropped it from
	// the result — deliverInputs returns it, FeedFactory discards it — passed every assertion
	// above. The fixture pays 5/unit, so 20 units is 100 credits.
	if result.Revenue != 100 {
		t.Fatalf("Revenue = %d, want 100 (20 units at the fixture's 5 credits/unit) — the leg's revenue must survive into the result, not be dropped on the floor", result.Revenue)
	}
}

// ADDED (not in the brief). THE FAILURE CLASS THE BRIEF NAMES AS RECURRING: a leg that reports a
// number it did not move. UnitsDelivered must be what the market actually TOOK, not what the hold
// offered — those two are equal in the base fixture (its sell echoes cmd.Units straight back), so
// an implementation summing item.Units instead of response.UnitsSold passes every other test in
// this file. Here the market takes 4 of each 10 offered, and the two answers separate: 8 vs 20.
//
// This is not hypothetical arithmetic. A market whose trade volume is below the offered lot fills
// partially, and a feed leg that books the offered figure reports a fed factory that is still
// starving.
func TestFeedFactory_CountsUnitsTheMarketTook_NotUnitsOffered(t *testing.T) {
	const takenPerSell = 4
	executor, mediator, ship := newPartialFillFeedRun(t, takenPerSell)

	result, err := executor.FeedFactory(feedDestinationCtx(), ship,
		&MarketLocatorResult{WaypointSymbol: fdFactoryWP}, []string{"SILICON_CRYSTALS", "COPPER"}, 1, nil)
	if err != nil {
		t.Fatalf("FeedFactory: %v", err)
	}
	// Non-vacuity: the separation only exists if both lots were actually offered and partially
	// taken. Two sells of 10 offered, 4 taken each.
	if got := mediator.sellCount(); got != 2 {
		t.Fatalf("the fixture made %d sell(s), want 2 — with fewer, the offered figure (20) and the taken figure (8) do not separate and this test cannot see the bug", got)
	}
	// STRENGTHENED (was a sell COUNT alone): the count says two lots were offered but not how big
	// they were, so the 8-vs-20 separation rested on an UNASSERTED Units:10 in the ship fixture plus
	// a mediator that ignores cmd.Units. If deliverInputs ever offered 4 per lot, both figures would
	// be 8, this test would still pass, and it would be proving nothing. Assert the offer itself.
	if offered := mediator.unitsOffered(); offered != 20 {
		t.Fatalf("the hold offered %d units, want 20 — the offered and taken figures must be DIFFERENT numbers (20 vs %d) or this test cannot tell an offered-vs-taken bug from correct behaviour", offered, 2*takenPerSell)
	}
	if result.UnitsDelivered != 2*takenPerSell {
		t.Fatalf("UnitsDelivered = %d, want %d — the market took %d of each 10-unit lot offered; %d is the OFFERED figure and reports a fed factory that is still starving",
			result.UnitsDelivered, 2*takenPerSell, takenPerSell, 20)
	}
	if result.Revenue != 2*takenPerSell*5 {
		t.Fatalf("Revenue = %d, want %d — revenue must follow the units the market took, not the units offered", result.Revenue, 2*takenPerSell*5)
	}
}

// ADDED (not in the brief). The guard's FAIL-CLOSED branch on the new path: an unreadable listing
// is passed to ValidateFeedDestination as nil, which refuses. Flying on a data gap is the same
// stranding as flying to a known wrong-chain factory — the hull is at the far end of a system
// either way — so "we could not read it" must park the leg, never wave it through.
func TestFeedFactory_RefusesADestinationWhoseListingCannotBeRead(t *testing.T) {
	executor, mediator, ship := newFeedDestinationRun(t, true)

	// fdOrigin is not the factory and not an input source, so the fixture's repo has no listing
	// for it at all — the unreadable case, with the SAME hull and the SAME cargo as the accepted
	// run above.
	result, err := executor.FeedFactory(feedDestinationCtx(), ship,
		&MarketLocatorResult{WaypointSymbol: fdOrigin}, []string{"SILICON_CRYSTALS", "COPPER"}, 1, nil)
	if err != nil {
		t.Fatalf("an unreadable destination parks the leg, it does not error it: %v", err)
	}
	if result == nil || !result.Refused {
		t.Fatalf("result = %+v, want Refused — an unreadable listing must fail CLOSED", result)
	}
	if got := mediator.navigationCount(); got != 0 {
		t.Fatalf("the hull was navigated %d time(s) toward a destination whose listing could not be read — that is the sp-b27a2 stranding via a data gap", got)
	}
	if mediator.sellCount() != 0 {
		t.Fatalf("sold %d time(s) at a destination the leg refused to fly to", mediator.sellCount())
	}
}

// navigationCount is the total number of navigates issued, where navigatedToFactory asks only
// about fdFactoryWP. A leg refused for an unreadable listing must not move the hull ANYWHERE, and
// the factory-specific check cannot see a hull sent somewhere else.
//
// Declared here rather than in the sp-b27a2 fixture file for the same reason
// dockRaceShipRepo.shipLocation and .locationNow are declared in their consumers' files: the
// shared fixture stays untouched, and Go only requires a method to share its type's PACKAGE.
func (m *fdMediator) navigationCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.navigatedTo)
}

// fdPartialFillMediator is the sp-b27a2 mediator with exactly ONE change: a sell reports FEWER
// units sold than the command offered. Everything else — navigate recording, docking, purchase
// accounting — delegates to the embedded fixture, so the only variable is the units figure.
//
// The base fixture cannot express this: its sell echoes cmd.Units back as UnitsSold, which makes
// "units offered" and "units taken" the same number and hides the confusion under test.
type fdPartialFillMediator struct {
	*fdMediator
	unitsPerSell int
	// offered is the Units on each SellCargoCommand — what the HOLD put on the table, as opposed to
	// what the market took. Recorded because the offered-vs-taken separation this fixture exists to
	// create is otherwise asserted only through a sell COUNT, which cannot see the lot size.
	offered []int
}

func (m *fdPartialFillMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	sell, ok := request.(*shipCargo.SellCargoCommand)
	if !ok {
		return m.fdMediator.Send(ctx, request)
	}
	m.mu.Lock()
	m.sold = append(m.sold, sell.GoodSymbol)
	m.offered = append(m.offered, sell.Units)
	m.mu.Unlock()
	return &shipCargo.SellCargoResponse{
		TotalRevenue:     m.unitsPerSell * 5,
		UnitsSold:        m.unitsPerSell,
		TransactionCount: 1,
	}, nil
}

// unitsOffered is the total units the hold offered across every sell this run.
func (m *fdPartialFillMediator) unitsOffered() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	total := 0
	for _, units := range m.offered {
		total += units
	}
	return total
}

// newPartialFillFeedRun mirrors newFeedDestinationRun (same factory, same loaded hull, same
// importing destination) with the partial-fill mediator swapped in. It builds its own rather than
// parameterising the shared helper so the sp-b27a2 fixture every existing test depends on is left
// untouched.
func newPartialFillFeedRun(t *testing.T, unitsPerSell int) (*ProductionExecutor, *fdPartialFillMediator, *navigation.Ship) {
	t.Helper()
	repo := &fdMarketRepo{
		importsInputs: true,
		inputs: []feedInputSpec{
			{good: "SILICON_CRYSTALS", waypoint: "X1-FD-SILICON", supply: "MODERATE", tradeVolume: 100, ask: 10},
			{good: "COPPER", waypoint: "X1-FD-COPPER", supply: "MODERATE", tradeVolume: 100, ask: 10},
		},
	}
	shipRepo := &dockRaceShipRepo{
		location: fdOrigin, navStatus: navigation.NavStatusDocked,
		cargoCapacity: 400, cargoUnits: 20,
		cargoInventory: []*shared.CargoItem{
			{Symbol: "SILICON_CRYSTALS", Name: "SILICON_CRYSTALS", Units: 10},
			{Symbol: "COPPER", Name: "COPPER", Units: 10},
		},
	}
	mediator := &fdPartialFillMediator{
		fdMediator:   &fdMediator{repo: shipRepo, dockHandler: tactics.NewDockShipHandler(shipRepo)},
		unitsPerSell: unitsPerSell,
	}
	executor := NewProductionExecutorWithConfig(
		mediator, shipRepo, repo, NewMarketLocator(repo, nil, nil, nil), &dockRaceClock{},
		[]time.Duration{time.Millisecond}, nil,
	)
	return executor, mediator, shipRepo.buildShip()
}
