package services

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/tactics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-rqwm: the factory MAKE-stage sold its fabricated OUTPUT at the BUY market
// instead of the sink the chain-margin guard priced. The guard cleared MEDICINE
// against sink A1@5,248; execution accumulated the output at the factory D39 and
// dumped it there via the make-room path (laddering D39's own ~1,560 bid, vs a
// ~3,100 harvest cost) — net −258k. These tests pin the fix: the output is flown
// to the guard's resale sink and sold THERE, a sink below basis PARKS (holds
// onboard) rather than dumping, and the make-room path never dumps the output at
// the factory/buy market.

const (
	sinkTestFactoryWP = "X1-DR-FACTORY" // where the output is harvested (the buy market, D39 analogue)
	sinkTestSinkWP    = "X1-DR-A1"      // the guard's resale sink (A1 analogue)
	sinkTestOutput    = "MEDICINE"
	sinkTestSystem    = "X1-DR"
)

// sinkTestMarketRepo serves a resale sink for sinkTestOutput at a DIFFERENT
// waypoint than the factory, so a sell there is provably not a sell at the buy
// market. sinkWP == "" models a good with no priceable resale sink.
type sinkTestMarketRepo struct {
	market.MarketRepository
	sinkWP  string
	sinkBid int
	sinkVol int
}

func (r *sinkTestMarketRepo) FindBestMarketBuying(ctx context.Context, good, systemSymbol string, playerID int) (*market.BestMarketBuyingResult, error) {
	if r.sinkWP == "" || good != sinkTestOutput {
		return nil, nil
	}
	return &market.BestMarketBuyingResult{
		WaypointSymbol: r.sinkWP,
		TradeSymbol:    good,
		PurchasePrice:  r.sinkBid,
		Supply:         "HIGH",
	}, nil
}

func (r *sinkTestMarketRepo) GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error) {
	if r.sinkWP == "" || waypointSymbol != r.sinkWP {
		return nil, nil
	}
	supply := "HIGH"
	activity := "STRONG"
	good, err := market.NewTradeGood(sinkTestOutput, &supply, &activity, r.sinkBid, r.sinkBid, r.sinkVol, market.TradeTypeImport)
	if err != nil {
		return nil, err
	}
	return market.NewMarket(waypointSymbol, []market.TradeGood{*good}, time.Now())
}

// locationNow reports where the fake ship currently is (mutated by the mediator's
// modeled navigation). Used to prove the sell leg was flown to the sink.
func (r *dockRaceShipRepo) locationNow() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.location
}

// navAttempts reports how many NavigateRouteCommands the fake handled — 0 proves a
// park never left for the sink.
func (m *dockRaceMediator) navAttempts() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.navCalls
}

func mustCargoItem(good string, units int) *shared.CargoItem {
	item, err := shared.NewCargoItem(good, good, "", units)
	if err != nil {
		panic(err)
	}
	return item
}

func makeCargo(good string, units int) []*shared.CargoItem {
	return []*shared.CargoItem{mustCargoItem(good, units)}
}

// newSinkExecutor wires a ProductionExecutor over the dock-race ship/mediator fakes
// but with a market repo whose resale sink is a DISTINCT waypoint from the factory.
// The ship starts DOCKED at the factory (having just harvested the output).
func newSinkExecutor(t *testing.T, mr *sinkTestMarketRepo) (*ProductionExecutor, *dockRaceShipRepo, *dockRaceMediator) {
	t.Helper()
	repo := &dockRaceShipRepo{
		location:      sinkTestFactoryWP,
		navStatus:     navigation.NavStatusDocked,
		cargoCapacity: 40,
	}
	mediator := &dockRaceMediator{
		repo:        repo,
		dockHandler: tactics.NewDockShipHandler(repo),
	}
	marketLocator := NewMarketLocator(mr, nil, nil, nil)
	executor := NewProductionExecutorWithConfig(
		mediator,
		repo,
		mr,
		marketLocator,
		&dockRaceClock{},
		[]time.Duration{time.Millisecond},
		nil,
	)
	return executor, repo, mediator
}

// Acceptance core (sp-rqwm): a harvested output whose sink bid clears the basis is
// flown to the guard's resale sink and sold THERE — never at the factory/buy market.
func TestSellFabricatedOutputAtSink_SellsAtGuardSink_NotBuyMarket(t *testing.T) {
	mr := &sinkTestMarketRepo{sinkWP: sinkTestSinkWP, sinkBid: 5248, sinkVol: 40}
	executor, repo, mediator := newSinkExecutor(t, mr)
	repo.fillCargo(makeCargo(sinkTestOutput, 40))

	basis := 3000 // factory ask; sink bid 5248 >= 3000 => sell
	revenue, err := executor.SellFabricatedOutputAtSink(
		context.Background(), dockRaceShip, sinkTestOutput, basis, sinkTestSystem, shared.MustNewPlayerID(1), nil,
	)
	if err != nil {
		t.Fatalf("selling a profitable output at its sink must succeed, got %v", err)
	}
	if revenue <= 0 {
		t.Fatalf("expected positive revenue from the sink sale, got %d", revenue)
	}
	if mediator.sellAttempts() != 1 {
		t.Fatalf("expected exactly 1 sell (at the sink), got %d", mediator.sellAttempts())
	}
	if got := repo.locationNow(); got != sinkTestSinkWP {
		t.Fatalf("the ship must have flown to the resale sink %s before selling — ended at %s (a sell here at the factory %s is the bug)", sinkTestSinkWP, got, sinkTestFactoryWP)
	}
	if left := onboardUnits(repo.buildShip(), sinkTestOutput); left != 0 {
		t.Fatalf("the output must be sold at the sink, %d units still onboard", left)
	}
}

// The exact incident economics (basis 3,100 vs sink bid 1,560): a sink below the
// bid>=basis loss floor must PARK — hold the output onboard, sell nothing, and
// never leave for the sink — not realize the loss.
func TestSellFabricatedOutputAtSink_BidBelowBasis_ParksAndHolds(t *testing.T) {
	mr := &sinkTestMarketRepo{sinkWP: sinkTestSinkWP, sinkBid: 1560, sinkVol: 40}
	executor, repo, mediator := newSinkExecutor(t, mr)
	repo.fillCargo(makeCargo(sinkTestOutput, 40))

	basis := 3100 // sink bid 1560 < 3100 => the −258k scenario => must park
	revenue, err := executor.SellFabricatedOutputAtSink(
		context.Background(), dockRaceShip, sinkTestOutput, basis, sinkTestSystem, shared.MustNewPlayerID(1), nil,
	)
	if err != nil {
		t.Fatalf("a below-basis sink must park gracefully, not error: %v", err)
	}
	if revenue != 0 {
		t.Fatalf("a sink below basis must realize ZERO revenue (park), got %d", revenue)
	}
	if mediator.sellAttempts() != 0 {
		t.Fatalf("must NOT sell the output below basis, got %d sell(s)", mediator.sellAttempts())
	}
	if mediator.navAttempts() != 0 {
		t.Fatalf("must NOT fly the sell leg when parking below basis, got %d navigation(s)", mediator.navAttempts())
	}
	if held := onboardUnits(repo.buildShip(), sinkTestOutput); held != 40 {
		t.Fatalf("the output must be HELD onboard when parked (never dumped), got %d units", held)
	}
}

// No priceable resale sink at all: hold the output onboard and retry later — never
// fall back to dumping it at the current/buy market.
func TestSellFabricatedOutputAtSink_NoSink_ParksAndHolds(t *testing.T) {
	mr := &sinkTestMarketRepo{sinkWP: ""} // FindBestMarketBuying => nil => FindImportMarket errors
	executor, repo, mediator := newSinkExecutor(t, mr)
	repo.fillCargo(makeCargo(sinkTestOutput, 40))

	revenue, err := executor.SellFabricatedOutputAtSink(
		context.Background(), dockRaceShip, sinkTestOutput, 3000, sinkTestSystem, shared.MustNewPlayerID(1), nil,
	)
	if err != nil {
		t.Fatalf("a missing sink must park gracefully, not error: %v", err)
	}
	if revenue != 0 || mediator.sellAttempts() != 0 || mediator.navAttempts() != 0 {
		t.Fatalf("no sink => hold onboard: expected 0 revenue/0 sells/0 navs, got %d/%d/%d", revenue, mediator.sellAttempts(), mediator.navAttempts())
	}
	if held := onboardUnits(repo.buildShip(), sinkTestOutput); held != 40 {
		t.Fatalf("the output must be HELD onboard when no sink exists, got %d units", held)
	}
}

// Nothing onboard (e.g. an inputs-only confirm or a skipped harvest): a no-op, not
// a spurious navigation or sell.
func TestSellFabricatedOutputAtSink_NothingOnboard_NoOp(t *testing.T) {
	mr := &sinkTestMarketRepo{sinkWP: sinkTestSinkWP, sinkBid: 5248, sinkVol: 40}
	executor, _, mediator := newSinkExecutor(t, mr)

	revenue, err := executor.SellFabricatedOutputAtSink(
		context.Background(), dockRaceShip, sinkTestOutput, 3000, sinkTestSystem, shared.MustNewPlayerID(1), nil,
	)
	if err != nil {
		t.Fatalf("an empty hold must be a clean no-op, got %v", err)
	}
	if revenue != 0 || mediator.sellAttempts() != 0 || mediator.navAttempts() != 0 {
		t.Fatalf("empty hold => no-op: expected 0/0/0, got %d/%d/%d", revenue, mediator.sellAttempts(), mediator.navAttempts())
	}
}

// Fix (c): the make-room path must never dump the fabricated OUTPUT at the current
// (factory/buy) market — it frees space by selling only the unprotected residue,
// leaving the output onboard for the sink-sell leg to drain.
func TestFreeCargoSpace_ProtectsOutput_NeverDumpsItAtBuyMarket(t *testing.T) {
	executor, repo, mediator := newDockRaceExecutor(t, nil)
	// Full hold (cap 40): 20 protected output + 20 freeable residue.
	repo.fillCargo([]*shared.CargoItem{
		mustCargoItem(sinkTestOutput, 20),
		mustCargoItem("RESIDUE_FEED", 20),
	})

	reloaded, err := executor.freeCargoSpace(context.Background(), repo.buildShip(), shared.MustNewPlayerID(1), sinkTestOutput)
	if err != nil {
		t.Fatalf("freeCargoSpace must free the residue and succeed, got %v", err)
	}
	if mediator.sellAttempts() != 1 {
		t.Fatalf("expected exactly 1 sell (the residue only, not the output), got %d", mediator.sellAttempts())
	}
	if held := onboardUnits(reloaded, sinkTestOutput); held != 20 {
		t.Fatalf("the fabricated output must be RETAINED (never dumped at the buy market), got %d units", held)
	}
	if residue := onboardUnits(reloaded, "RESIDUE_FEED"); residue != 0 {
		t.Fatalf("the unprotected residue should have been sold to free space, %d units remain", residue)
	}
}
