package contract

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// --- fakes ---------------------------------------------------------------

// idleArbFakeShipRepo backs FindIdleShipsByFleet (FindAllByPlayer) over a
// mutable ship set, so tests can claim/release hulls between dispatch passes
// exactly like the coordinator and the container runners do.
type idleArbFakeShipRepo struct {
	navigation.ShipRepository
	ships []*navigation.Ship
}

func (f *idleArbFakeShipRepo) FindAllByPlayer(context.Context, shared.PlayerID) ([]*navigation.Ship, error) {
	return f.ships, nil
}

// fakeGraphProvider serves one system's waypoint coordinates.
type fakeGraphProvider struct {
	waypoints map[string]*shared.Waypoint
}

func (f *fakeGraphProvider) GetGraph(context.Context, string, bool, int) (*system.GraphLoadResult, error) {
	return &system.GraphLoadResult{
		Graph: &system.NavigationGraph{Waypoints: f.waypoints},
	}, nil
}

// idleArbFakeMarketRepo serves per-waypoint market data for the lane picker.
type idleArbFakeMarketRepo struct {
	fakeMarketRepo // in-system/cross-system finders unused here
	markets        map[string]*market.Market
}

func (f *idleArbFakeMarketRepo) GetMarketData(_ context.Context, waypointSymbol string, _ int) (*market.Market, error) {
	return f.markets[waypointSymbol], nil
}

func (f *idleArbFakeMarketRepo) FindAllMarketsInSystem(context.Context, string, int) ([]string, error) {
	symbols := make([]string, 0, len(f.markets))
	for wp := range f.markets {
		symbols = append(symbols, wp)
	}
	return symbols, nil
}

// fakeIdleArbLauncher records launched specs and, like the real launcher,
// CLAIMS the hull before returning — so the dispatcher's recount sees the
// truth the real ClaimShip would produce.
type fakeIdleArbLauncher struct {
	repo     *idleArbFakeShipRepo
	clock    shared.Clock
	launches []IdleArbSpec
	failNext bool // simulate losing the claim race
}

func (f *fakeIdleArbLauncher) LaunchIdleArb(_ context.Context, spec IdleArbSpec) (string, error) {
	if f.failNext {
		f.failNext = false
		return "", fmt.Errorf("idle-arb claim of %s refused: ship already assigned", spec.ShipSymbol)
	}
	for _, s := range f.repo.ships {
		if s.ShipSymbol() == spec.ShipSymbol {
			if err := s.AssignToContainer("idle-arb-"+spec.ShipSymbol, f.clock); err != nil {
				return "", err
			}
		}
	}
	f.launches = append(f.launches, spec)
	return "idle-arb-" + spec.ShipSymbol, nil
}

// --- fixture builders ------------------------------------------------------

func idleArbWaypoint(t *testing.T, symbol string, x, y float64) *shared.Waypoint {
	t.Helper()
	wp, err := shared.NewWaypoint(symbol, x, y)
	if err != nil {
		t.Fatalf("waypoint %s: %v", symbol, err)
	}
	return wp
}

func idleArbHull(t *testing.T, symbol string, at *shared.Waypoint, fleet string) *navigation.Ship {
	t.Helper()
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("fuel: %v", err)
	}
	cargo, err := shared.NewCargo(40, 0, nil)
	if err != nil {
		t.Fatalf("cargo: %v", err)
	}
	ship, err := navigation.NewShip(
		symbol, shared.MustNewPlayerID(1), at, fuel,
		100, 40, cargo, 30, "FRAME_FRIGATE", "HAULER", nil,
		navigation.NavStatusDocked,
	)
	if err != nil {
		t.Fatalf("ship %s: %v", symbol, err)
	}
	ship.SetDedicatedFleet(fleet)
	return ship
}

func tradeGood(t *testing.T, symbol string, bid, ask int) market.TradeGood {
	t.Helper()
	g, err := market.NewTradeGood(symbol, nil, nil, bid, ask, 100, market.TradeType("EXCHANGE"))
	if err != nil {
		t.Fatalf("trade good %s: %v", symbol, err)
	}
	return *g
}

func marketAt(t *testing.T, waypoint string, goods ...market.TradeGood) *market.Market {
	t.Helper()
	m, err := market.NewMarket(waypoint, goods, time.Now())
	if err != nil {
		t.Fatalf("market %s: %v", waypoint, err)
	}
	return m
}

// --- harness ---------------------------------------------------------------

const testFleet = "contract"

// hub layout: hull(s) at HUB (0,0); NEAR market at (0,100) inside the 250
// radius buying MACHINERY at 150 vs the hub's 100 ask; FAR market at (0,400)
// outside the radius with an even juicier bid that must be IGNORED.
func idleArbHarness(t *testing.T, hulls int, cfg IdleArbConfig) (*IdleArbDispatcher, *idleArbFakeShipRepo, *fakeIdleArbLauncher) {
	t.Helper()

	hub := idleArbWaypoint(t, "X1-HUB-E42", 0, 0)
	near := idleArbWaypoint(t, "X1-HUB-D40", 0, 100)
	far := idleArbWaypoint(t, "X1-HUB-Z99", 0, 400)

	repo := &idleArbFakeShipRepo{}
	for i := 0; i < hulls; i++ {
		repo.ships = append(repo.ships, idleArbHull(t, fmt.Sprintf("TORWIND-%d", i+1), hub, testFleet))
	}

	graph := &fakeGraphProvider{waypoints: map[string]*shared.Waypoint{
		hub.Symbol: hub, near.Symbol: near, far.Symbol: far,
	}}

	markets := &idleArbFakeMarketRepo{markets: map[string]*market.Market{
		hub.Symbol:  marketAt(t, hub.Symbol, tradeGood(t, "MACHINERY", 90, 100)),
		near.Symbol: marketAt(t, near.Symbol, tradeGood(t, "MACHINERY", 150, 160)),
		far.Symbol:  marketAt(t, far.Symbol, tradeGood(t, "MACHINERY", 1000, 1100)),
	}}

	clock := shared.NewRealClock()
	launcher := &fakeIdleArbLauncher{repo: repo, clock: clock}
	dispatcher := NewIdleArbDispatcher(repo, markets, graph, launcher, clock, shared.MustNewPlayerID(1), testFleet, cfg)
	return dispatcher, repo, launcher
}

// --- tests -----------------------------------------------------------------

func TestIdleArb_ReserveHullsNeverDispatched(t *testing.T) {
	dispatcher, repo, launcher := idleArbHarness(t, 3, IdleArbConfig{ReserveHulls: 1})

	launched := dispatcher.DispatchOnce(context.Background())

	if launched != 2 || len(launcher.launches) != 2 {
		t.Fatalf("3 idle − reserve 1 must launch exactly 2, got %d", launched)
	}
	idle, _, err := FindIdleShipsByFleet(context.Background(), shared.MustNewPlayerID(1), repo, testFleet)
	if err != nil {
		t.Fatalf("recount: %v", err)
	}
	if len(idle) != 1 {
		t.Fatalf("exactly the reserve hull must remain idle, got %d", len(idle))
	}
}

func TestIdleArb_AtReserve_NothingDispatched(t *testing.T) {
	dispatcher, _, launcher := idleArbHarness(t, 1, IdleArbConfig{ReserveHulls: 1})

	if launched := dispatcher.DispatchOnce(context.Background()); launched != 0 || len(launcher.launches) != 0 {
		t.Fatalf("the last reserve hull must NEVER be leased to arb, got %d launches", launched)
	}
}

func TestIdleArb_LaneIsHubLocal_AndSpecInheritsGuards(t *testing.T) {
	dispatcher, _, launcher := idleArbHarness(t, 2, IdleArbConfig{
		ReserveHulls: 1, HubRadius: 250, MaxSpendPerLeg: 77_000, MinMarginPerUnit: 5,
	})

	if launched := dispatcher.DispatchOnce(context.Background()); launched != 1 {
		t.Fatalf("expected exactly 1 launch, got %d", launched)
	}
	spec := launcher.launches[0]

	if spec.BuyAt != "X1-HUB-E42" {
		t.Errorf("BuyAt must be the hull's CURRENT waypoint (location guard makes hub-local physical), got %s", spec.BuyAt)
	}
	if spec.SellAt != "X1-HUB-D40" {
		t.Errorf("SellAt must be the in-radius market, not the juicier out-of-radius one, got %s", spec.SellAt)
	}
	if spec.Good != "MACHINERY" {
		t.Errorf("expected MACHINERY lane, got %s", spec.Good)
	}
	if spec.Operation != testFleet {
		t.Errorf("claim identity must be the dispatcher's fleet (l7h2), got %q", spec.Operation)
	}
	if spec.MaxSpend != 77_000 || spec.MinMargin != 5 {
		t.Errorf("guard knobs must pass through untouched, got spend %d margin %d", spec.MaxSpend, spec.MinMargin)
	}
}

func TestIdleArb_NoProfitableLane_NoLaunch_TerminatesCleanly(t *testing.T) {
	dispatcher, repo, launcher := idleArbHarness(t, 3, IdleArbConfig{ReserveHulls: 1, MinMarginPerUnit: 999})

	if launched := dispatcher.DispatchOnce(context.Background()); launched != 0 || len(launcher.launches) != 0 {
		t.Fatalf("margin floor 999 leaves no lane; expected 0 launches, got %d", launched)
	}
	idle, _, _ := FindIdleShipsByFleet(context.Background(), shared.MustNewPlayerID(1), repo, testFleet)
	if len(idle) != 3 {
		t.Fatalf("skipped hulls must stay idle (padding the reserve), got %d", len(idle))
	}
}

func TestIdleArb_LostClaimRace_SkipsHullAndContinues(t *testing.T) {
	dispatcher, repo, launcher := idleArbHarness(t, 3, IdleArbConfig{ReserveHulls: 1})
	launcher.failNext = true // TORWIND-1's launch loses its claim race

	launched := dispatcher.DispatchOnce(context.Background())

	// The race-losing hull is skipped (it stays idle here, padding the
	// reserve) and the dispatcher continues with the remaining surplus: the
	// other two hulls launch, and the reserve still holds afterwards.
	if launched != 2 || len(launcher.launches) != 2 {
		t.Fatalf("a lost claim race must skip that hull and continue with the rest, got %d launches", launched)
	}
	for _, spec := range launcher.launches {
		if spec.ShipSymbol == "TORWIND-1" {
			t.Fatalf("the hull that lost its claim race must not appear among the launches")
		}
	}
	idle, _, _ := FindIdleShipsByFleet(context.Background(), shared.MustNewPlayerID(1), repo, testFleet)
	if len(idle) != 1 {
		t.Fatalf("reserve must hold after the pass, got %d idle", len(idle))
	}
}

// TestIdleArb_TwentyCycles_ZeroMissedClaims is the sp-1z2h acceptance
// simulation: 20 contract cycles interleaved with dispatch ticks, arb legs
// completing between cycles. The invariant under test: whenever the (serial)
// coordinator wants a hull for a contract claim, at least one idle dedicated
// hull exists INSTANTLY — arb never delays or starves a claim — while the
// harvest still launches real legs.
func TestIdleArb_TwentyCycles_ZeroMissedClaims(t *testing.T) {
	dispatcher, repo, launcher := idleArbHarness(t, 6, IdleArbConfig{ReserveHulls: 1})
	ctx := context.Background()
	clock := shared.NewRealClock()
	pid := shared.MustNewPlayerID(1)

	releaseByPrefix := func(prefix string) {
		for _, s := range repo.ships {
			if s.IsAssigned() && len(s.ContainerID()) >= len(prefix) && s.ContainerID()[:len(prefix)] == prefix {
				s.ForceRelease("test_cycle_complete", clock)
			}
		}
	}

	for cycle := 1; cycle <= 20; cycle++ {
		// Idle gap: the dispatcher harvests before the next claim arrives.
		dispatcher.DispatchOnce(ctx)

		// CONTRACT CLAIM: the coordinator needs a hull RIGHT NOW.
		idle, _, err := FindIdleShipsByFleet(ctx, pid, repo, testFleet)
		if err != nil {
			t.Fatalf("cycle %d: discovery failed: %v", cycle, err)
		}
		if len(idle) == 0 {
			t.Fatalf("cycle %d: MISSED CLAIM - no idle hull available for a contract while arb legs run", cycle)
		}
		worker := idle[0]
		if err := worker.AssignToContainer(fmt.Sprintf("contract-work-%d", cycle), clock); err != nil {
			t.Fatalf("cycle %d: contract claim failed: %v", cycle, err)
		}

		// Mid-contract dispatch tick (the worker is flying; more idle-gap
		// harvesting may happen around it).
		dispatcher.DispatchOnce(ctx)

		// Hub-local arb legs (5-8 min) end well inside a contract cycle:
		// release them, then the contract completes and its hull frees too.
		releaseByPrefix("idle-arb-")
		releaseByPrefix("contract-work-")
	}

	if len(launcher.launches) == 0 {
		t.Fatalf("the harvest never launched a single leg across 20 cycles - idle time not harvested")
	}
}

func TestIdleArbConfig_WithDefaults_FillsZeroes(t *testing.T) {
	cfg := IdleArbConfig{}.WithDefaults()
	if cfg.ReserveHulls != DefaultIdleArbReserveHulls ||
		cfg.HubRadius != DefaultIdleArbHubRadius ||
		cfg.MaxSpendPerLeg != DefaultIdleArbMaxSpend ||
		cfg.MinMarginPerUnit != DefaultIdleArbMinMargin ||
		cfg.Interval != DefaultIdleArbInterval {
		t.Fatalf("zero config must take documented defaults, got %+v", cfg)
	}
	custom := IdleArbConfig{ReserveHulls: 2, HubRadius: 50, MaxSpendPerLeg: 5, MinMarginPerUnit: 9, Interval: DefaultIdleArbInterval}.WithDefaults()
	if custom.ReserveHulls != 2 || custom.HubRadius != 50 || custom.MaxSpendPerLeg != 5 || custom.MinMarginPerUnit != 9 {
		t.Fatalf("non-zero config must be preserved, got %+v", custom)
	}
}
