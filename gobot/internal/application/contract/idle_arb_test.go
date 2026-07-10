package contract

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
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

// fakeContractGoods serves a fixed open-contract-goods set (guard 3), and can
// simulate a contract-read failure to exercise the fail-closed dispatch path.
type fakeContractGoods struct {
	goods map[string]struct{}
	err   error
}

func contractGoodsOf(symbols ...string) fakeContractGoods {
	set := make(map[string]struct{}, len(symbols))
	for _, s := range symbols {
		set[s] = struct{}{}
	}
	return fakeContractGoods{goods: set}
}

func (f fakeContractGoods) OpenContractGoods(context.Context, int) (map[string]struct{}, error) {
	return f.goods, f.err
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

// hub layout: hull(s) at HUB (0,0); NEAR market at (0,50) INSIDE the 80u leash
// buying MACHINERY at 150 vs the hub's 100 ask (margin 50/unit); FAR market at
// (0,400) outside both the leash and the 250 hub-radius with an even juicier bid
// that must be IGNORED. NEAR sits at ~50u — the "legs max ~52u naturally" shape
// the sp-uohe leash formalizes — so the default 80u leash still admits it.
func idleArbHarness(t *testing.T, hulls int, cfg IdleArbConfig) (*IdleArbDispatcher, *idleArbFakeShipRepo, *fakeIdleArbLauncher) {
	t.Helper()
	return idleArbHarnessGoods(t, hulls, cfg, nil)
}

// idleArbHarnessGoods is idleArbHarness with an explicit contract-goods provider
// (guard 3). A nil provider leaves the contract-good exclusion inert.
func idleArbHarnessGoods(t *testing.T, hulls int, cfg IdleArbConfig, contractGoods ContractGoodsProvider) (*IdleArbDispatcher, *idleArbFakeShipRepo, *fakeIdleArbLauncher) {
	t.Helper()

	hub := idleArbWaypoint(t, "X1-HUB-E42", 0, 0)
	near := idleArbWaypoint(t, "X1-HUB-D40", 0, 50)
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
	dispatcher := NewIdleArbDispatcher(repo, markets, graph, launcher, nil, contractGoods, clock, shared.MustNewPlayerID(1), testFleet, cfg)
	return dispatcher, repo, launcher
}

// --- tests -----------------------------------------------------------------

// idleArbTwoSinkHarness builds a dispatcher over a hub with TWO distinct in-leash
// sinks for the same good (sp-lbbm): sinkA (0,40) at a fatter margin (100) than
// sinkB (0,50) (50). Because the lane mutex forbids two hulls dumping ONE sink in
// a window, a reserve/claim-race test that must still launch two legs needs two
// sinks — the highest-margin hull takes sinkA and the next falls back to sinkB.
// Hulls are TORWIND-1..N at the shared hub.
func idleArbTwoSinkHarness(t *testing.T, hulls int, cfg IdleArbConfig) (*IdleArbDispatcher, *idleArbFakeShipRepo, *fakeIdleArbLauncher) {
	t.Helper()
	hub := idleArbWaypoint(t, "X1-HUB-E42", 0, 0)
	sinkA := idleArbWaypoint(t, "X1-HUB-A40", 0, 40)
	sinkB := idleArbWaypoint(t, "X1-HUB-B50", 0, 50)

	repo := &idleArbFakeShipRepo{}
	for i := 0; i < hulls; i++ {
		repo.ships = append(repo.ships, idleArbHull(t, fmt.Sprintf("TORWIND-%d", i+1), hub, testFleet))
	}
	graph := &fakeGraphProvider{waypoints: map[string]*shared.Waypoint{
		hub.Symbol: hub, sinkA.Symbol: sinkA, sinkB.Symbol: sinkB,
	}}
	markets := &idleArbFakeMarketRepo{markets: map[string]*market.Market{
		hub.Symbol:   marketAt(t, hub.Symbol, tradeGood(t, "MACHINERY", 90, 100)),
		sinkA.Symbol: marketAt(t, sinkA.Symbol, tradeGood(t, "MACHINERY", 200, 210)), // margin 100
		sinkB.Symbol: marketAt(t, sinkB.Symbol, tradeGood(t, "MACHINERY", 150, 160)), // margin 50
	}}
	clock := shared.NewRealClock()
	launcher := &fakeIdleArbLauncher{repo: repo, clock: clock}
	d := NewIdleArbDispatcher(repo, markets, graph, launcher, nil, nil, clock, shared.MustNewPlayerID(1), testFleet, cfg)
	return d, repo, launcher
}

func TestIdleArb_ReserveHullsNeverDispatched(t *testing.T) {
	// Two distinct in-leash sinks so the lane mutex (sp-lbbm) does not cap the
	// count: a single shared sink would (correctly) allow only ONE concurrent
	// dump, confounding the reserve-count assertion. Here the two surplus hulls
	// spread across the two sinks, isolating reserve discipline.
	dispatcher, repo, launcher := idleArbTwoSinkHarness(t, 3, IdleArbConfig{ReserveHulls: 1})

	launched := dispatcher.DispatchOnce(context.Background())

	if launched != 2 || len(launcher.launches) != 2 {
		t.Fatalf("3 idle − reserve 1 must launch exactly 2, got %d", launched)
	}
	// The mutex spread the two legs across DIFFERENT sinks — never two into one.
	sinks := map[string]bool{}
	for _, spec := range launcher.launches {
		sinks[spec.SellAt] = true
	}
	if len(sinks) != 2 {
		t.Fatalf("the two legs must hit two DISTINCT sinks (no concurrent same-sink dump), got %v", sinks)
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
	if spec.MaxSpend != 77_000 {
		t.Errorf("max-spend guard knob must pass through untouched, got %d", spec.MaxSpend)
	}
	// Guard 1 (sp-uohe): the spec's MinMargin is the RELATIVE live-verify floor,
	// max(absolute floor 5, ceil(0.80 × quoted margin 50) = 40) = 40 — NOT the
	// flat absolute floor. This is what arms the arb run's live-verify gate.
	if spec.MinMargin != 40 {
		t.Errorf("MinMargin must be the 80%%-of-quote live-verify floor (40), got %d", spec.MinMargin)
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
	// Two sinks (sp-lbbm): the two surviving hulls spread across them rather than
	// collide, so the claim-race-skip-and-continue behavior can still show two
	// launches without the concurrent same-sink dump the mutex now forbids.
	dispatcher, repo, launcher := idleArbTwoSinkHarness(t, 3, IdleArbConfig{ReserveHulls: 1})
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
		cfg.LeashRadius != DefaultIdleArbLeashRadius ||
		cfg.MaxLegDuration != DefaultIdleArbMaxLegDuration ||
		cfg.MaxSpendPerLeg != DefaultIdleArbMaxSpend ||
		cfg.MinMarginPerUnit != DefaultIdleArbMinMargin ||
		cfg.MarginVerifyFraction != DefaultIdleArbMarginVerifyFraction ||
		cfg.Interval != DefaultIdleArbInterval {
		t.Fatalf("zero config must take documented defaults, got %+v", cfg)
	}
	// A nil blacklist defaults to [ELECTRONICS] (the −234k good).
	if len(cfg.Blacklist) != 1 || cfg.Blacklist[0] != "ELECTRONICS" {
		t.Fatalf("nil blacklist must default to [ELECTRONICS], got %v", cfg.Blacklist)
	}
	// An EXPLICIT empty blacklist is preserved (the whitelist-flip that disables
	// it entirely) — it must NOT be re-defaulted to ELECTRONICS.
	flipped := IdleArbConfig{Blacklist: []string{}}.WithDefaults()
	if len(flipped.Blacklist) != 0 {
		t.Fatalf("explicit empty blacklist must be preserved (disabled), got %v", flipped.Blacklist)
	}
	custom := IdleArbConfig{ReserveHulls: 2, HubRadius: 50, LeashRadius: 33, MaxSpendPerLeg: 5, MinMarginPerUnit: 9, MarginVerifyFraction: 0.5, Interval: DefaultIdleArbInterval}.WithDefaults()
	if custom.ReserveHulls != 2 || custom.HubRadius != 50 || custom.LeashRadius != 33 || custom.MaxSpendPerLeg != 5 || custom.MinMarginPerUnit != 9 || custom.MarginVerifyFraction != 0.5 {
		t.Fatalf("non-zero config must be preserved, got %+v", custom)
	}
}

// --- sp-uohe money-guard tests ---------------------------------------------

// Guard 1 (live pre-buy verify): the effective floor is the tighter of the
// absolute floor and ceil(fraction × quoted margin). This is the value handed to
// the arb run's live-verify gate; the run itself aborts pre-buy when the LIVE
// margin misses it (proven end-to-end by
// TestArbCoordinator_MinMarginAbortsBeforeBuy).
func TestIdleArbMinMargin_RelativeFloor(t *testing.T) {
	cfg := IdleArbConfig{MinMarginPerUnit: 5, MarginVerifyFraction: 0.80}
	if got := idleArbMinMargin(cfg, 100); got != 80 {
		t.Errorf("quoted 100 → relative floor ceil(0.8×100)=80, got %d", got)
	}
	if got := idleArbMinMargin(cfg, 51); got != 41 {
		t.Errorf("quoted 51 → ceil(0.8×51)=41 (ceil rounds up), got %d", got)
	}
	if got := idleArbMinMargin(cfg, 3); got != 5 {
		t.Errorf("quoted 3 → absolute floor 5 dominates the tiny relative floor, got %d", got)
	}
}

// Guard 1 end-to-end at the dispatch seam: a launched leg carries the 80%-of-
// quote live-verify floor, not the flat MinMargin=1 that let the −234k
// ELECTRONICS legs through. Remove idleArbMinMargin and this goes red.
func TestIdleArb_MarginVerifyFloorArmsTheGate(t *testing.T) {
	d, _, launcher := idleArbHarness(t, 2, IdleArbConfig{ReserveHulls: 1})
	if launched := d.DispatchOnce(context.Background()); launched != 1 {
		t.Fatalf("expected exactly 1 launch, got %d", launched)
	}
	// NEAR quotes margin 50/unit → floor ceil(0.80 × 50) = 40, not the flat 1.
	if got := launcher.launches[0].MinMargin; got != 40 {
		t.Fatalf("launched leg must carry the 80%%-of-quote floor (40), got %d", got)
	}
}

// Guard 4 (blacklist): a listed good is never dispatched; a config whitelist-flip
// (explicit empty list) re-enables it with no code change.
func TestIdleArb_Blacklist_NeverDispatches_FlipReEnables(t *testing.T) {
	blocked, repo, launcher := idleArbHarness(t, 2, IdleArbConfig{ReserveHulls: 1, Blacklist: []string{"MACHINERY"}})
	if launched := blocked.DispatchOnce(context.Background()); launched != 0 || len(launcher.launches) != 0 {
		t.Fatalf("a blacklisted good must never dispatch, got %d launches", launched)
	}
	if blocked.skipBlacklist == 0 {
		t.Fatalf("a blacklist skip must be counted, got %d", blocked.skipBlacklist)
	}
	if idle, _, _ := FindIdleShipsByFleet(context.Background(), shared.MustNewPlayerID(1), repo, testFleet); len(idle) != 2 {
		t.Fatalf("blacklist-skipped hulls must stay idle, got %d", len(idle))
	}

	// Whitelist flip: an explicit empty blacklist re-enables the same lane.
	open, _, launcher2 := idleArbHarness(t, 2, IdleArbConfig{ReserveHulls: 1, Blacklist: []string{}})
	if launched := open.DispatchOnce(context.Background()); launched != 1 || launcher2.launches[0].Good != "MACHINERY" {
		t.Fatalf("clearing the blacklist must re-enable dispatch, got %d launches", launched)
	}
}

// Guard 3 (contract-good exclusion): a good under an open contract is never
// dispatched (no competing with our own sourcing), and a contract-read failure
// fails CLOSED — the whole pass is skipped rather than dispatched blind.
func TestIdleArb_ContractGood_NeverDispatches_FailClosed(t *testing.T) {
	under, _, launcher := idleArbHarnessGoods(t, 2, IdleArbConfig{ReserveHulls: 1}, contractGoodsOf("MACHINERY"))
	if launched := under.DispatchOnce(context.Background()); launched != 0 || len(launcher.launches) != 0 {
		t.Fatalf("a good under an open contract must never dispatch, got %d launches", launched)
	}
	if under.skipContractGood == 0 {
		t.Fatalf("a contract-good skip must be counted, got %d", under.skipContractGood)
	}

	clear, _, launcher2 := idleArbHarnessGoods(t, 2, IdleArbConfig{ReserveHulls: 1}, contractGoodsOf("FUEL"))
	if launched := clear.DispatchOnce(context.Background()); launched != 1 || launcher2.launches[0].Good != "MACHINERY" {
		t.Fatalf("a good NOT under any contract must dispatch, got %d launches", launched)
	}

	failing, _, launcher3 := idleArbHarnessGoods(t, 2, IdleArbConfig{ReserveHulls: 1}, fakeContractGoods{err: fmt.Errorf("contract store down")})
	if launched := failing.DispatchOnce(context.Background()); launched != 0 || len(launcher3.launches) != 0 {
		t.Fatalf("a contract-read failure must fail CLOSED (0 launches), got %d", launched)
	}
}

// Guard 2 (leash): a market inside the outer hub-radius but beyond the leash is
// skipped; widening the leash admits it. The leg-time cap bites where the radius
// does not — an in-leash market whose projected CRUISE leg exceeds the cap is
// also skipped. Both increment the leash counter.
func TestIdleArb_Leash_SkipsBeyondRadiusAndLegTime(t *testing.T) {
	build := func(cfg IdleArbConfig) (*IdleArbDispatcher, *fakeIdleArbLauncher) {
		hub := idleArbWaypoint(t, "X1-HUB-E42", 0, 0)
		mid := idleArbWaypoint(t, "X1-HUB-M50", 0, 150) // inside HubRadius 250, beyond leash 80
		repo := &idleArbFakeShipRepo{ships: []*navigation.Ship{
			idleArbHull(t, "TORWIND-1", hub, testFleet),
			idleArbHull(t, "TORWIND-2", hub, testFleet),
		}}
		graph := &fakeGraphProvider{waypoints: map[string]*shared.Waypoint{hub.Symbol: hub, mid.Symbol: mid}}
		markets := &idleArbFakeMarketRepo{markets: map[string]*market.Market{
			hub.Symbol: marketAt(t, hub.Symbol, tradeGood(t, "MACHINERY", 90, 100)),
			mid.Symbol: marketAt(t, mid.Symbol, tradeGood(t, "MACHINERY", 300, 320)),
		}}
		clock := shared.NewRealClock()
		launcher := &fakeIdleArbLauncher{repo: repo, clock: clock}
		return NewIdleArbDispatcher(repo, markets, graph, launcher, nil, nil, clock, shared.MustNewPlayerID(1), testFleet, cfg), launcher
	}

	leashed, launcher := build(IdleArbConfig{ReserveHulls: 1, HubRadius: 250, LeashRadius: 80})
	if launched := leashed.DispatchOnce(context.Background()); launched != 0 || len(launcher.launches) != 0 {
		t.Fatalf("a market beyond the leash (but within hub-radius) must be skipped, got %d launches", launched)
	}
	if leashed.skipLeash == 0 {
		t.Fatalf("a leash skip must be counted, got %d", leashed.skipLeash)
	}

	admitted, launcher2 := build(IdleArbConfig{ReserveHulls: 1, HubRadius: 250, LeashRadius: 250})
	if launched := admitted.DispatchOnce(context.Background()); launched != 1 || len(launcher2.launches) != 1 {
		t.Fatalf("widening the leash to admit the market must dispatch it, got %d launches", launched)
	}

	// Leg-time cap: NEAR (@50u, inside the 80u leash) but a 10s max-leg-time is
	// shorter than its ~51s CRUISE leg → skipped by the leg-time branch.
	legCapped, _, launcher3 := idleArbHarness(t, 2, IdleArbConfig{ReserveHulls: 1, MaxLegDuration: 10 * time.Second})
	if launched := legCapped.DispatchOnce(context.Background()); launched != 0 || len(launcher3.launches) != 0 {
		t.Fatalf("a leg exceeding the max-leg-time must be skipped, got %d launches", launched)
	}
	if legCapped.skipLeash == 0 {
		t.Fatalf("a leg-time skip must count as a leash skip, got %d", legCapped.skipLeash)
	}
}

// Guard 5 (counters): the per-pass harvest summary carries the attempt rate and
// the per-reason skip counts in MESSAGE TEXT (the CLI renderer drops metadata
// maps), so the captain's acceptance and the fleet-sizing rule can read them.
func TestIdleArb_HarvestSummary_CountsInMessageText(t *testing.T) {
	logger := &idleArbCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	d, _, _ := idleArbHarness(t, 2, IdleArbConfig{ReserveHulls: 1, Blacklist: []string{"MACHINERY"}})
	d.DispatchOnce(ctx)

	summary := logger.messageWithPrefix(t, "Idle-arb harvest:")
	for _, want := range []string{"blacklist", "contract-good", "leash", "/hr"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("harvest summary must carry %q in message TEXT, got: %s", want, summary)
		}
	}
}

// sp-nw9v per-candidate verdict logging: every positive-margin candidate emits a
// terse line carrying the COMPUTED distance the leash used, the two endpoints
// (with coordinates) it measured between, the quoted margin, and the verdict — in
// MESSAGE TEXT. This is the candidate list an all-pairs analyst scan is diffed
// against; without it, a masked mis-pick (wrong distance, stale row, over-broad
// exclusion) is invisible (the diagnosis that produced this observable had to be
// reconstructed from the DB). An ELIGIBLE lane and a leash-SKIPPED lane both log.
func TestIdleArb_CandidateLogging_PerLaneVerdictInMessageText(t *testing.T) {
	// (a) An eligible in-leash candidate: hub(0,0)->near(0,50), margin 50, dist 50<80.
	loggerA := &idleArbCapturingLogger{}
	dA, _, _ := idleArbHarness(t, 2, IdleArbConfig{ReserveHulls: 1})
	dA.DispatchOnce(common.WithLogger(context.Background(), loggerA))

	eligible := loggerA.messageWithPrefix(t, "Idle-arb candidate:")
	for _, want := range []string{
		"MACHINERY", "buy@X1-HUB-E42(0,0)", "sell@X1-HUB-D40(0,50)",
		"dist 50u", "leash 80", "margin 50/u", "bid 150 - ask 100", "verdict eligible",
	} {
		if !strings.Contains(eligible, want) {
			t.Fatalf("eligible candidate line must carry %q in message TEXT, got: %s", want, eligible)
		}
	}

	// (b) A leash-skipped candidate: hub(0,0)->mid(0,150), inside hub-radius 250 but
	// beyond leash 80 — the exact masking shape (a profitable lane the distance
	// pushes past the leash). The line must name the computed distance AND the
	// verdict so the skip is attributable to distance, not guessed.
	hub := idleArbWaypoint(t, "X1-HUB-E42", 0, 0)
	mid := idleArbWaypoint(t, "X1-HUB-M50", 0, 150)
	repo := &idleArbFakeShipRepo{ships: []*navigation.Ship{
		idleArbHull(t, "TORWIND-1", hub, testFleet),
		idleArbHull(t, "TORWIND-2", hub, testFleet),
	}}
	graph := &fakeGraphProvider{waypoints: map[string]*shared.Waypoint{hub.Symbol: hub, mid.Symbol: mid}}
	markets := &idleArbFakeMarketRepo{markets: map[string]*market.Market{
		hub.Symbol: marketAt(t, hub.Symbol, tradeGood(t, "MACHINERY", 90, 100)),
		mid.Symbol: marketAt(t, mid.Symbol, tradeGood(t, "MACHINERY", 300, 320)),
	}}
	clock := shared.NewRealClock()
	loggerB := &idleArbCapturingLogger{}
	dB := NewIdleArbDispatcher(repo, markets, graph, &fakeIdleArbLauncher{repo: repo, clock: clock}, nil, nil,
		clock, shared.MustNewPlayerID(1), testFleet, IdleArbConfig{ReserveHulls: 1, HubRadius: 250, LeashRadius: 80})
	dB.DispatchOnce(common.WithLogger(context.Background(), loggerB))

	skipped := loggerB.messageWithPrefix(t, "Idle-arb candidate:")
	for _, want := range []string{"dist 150u", "leash 80", "verdict skipped:leash"} {
		if !strings.Contains(skipped, want) {
			t.Fatalf("leash-skipped candidate line must carry %q in message TEXT, got: %s", want, skipped)
		}
	}
}

// sp-nw9v: the dispatcher start-log must surface the LEASH radius (and max-leg
// cap). Its omission is exactly what hid an effective-80 leash while the operator
// believed a 150 retune was live — the retune had silently no-op'd, and the
// start-log printed only the hub radius. Run logs the start line before its first
// select, so an already-cancelled context exercises it without a tick.
func TestIdleArb_StartLog_SurfacesLeashRadius(t *testing.T) {
	logger := &idleArbCapturingLogger{}
	ctx, cancel := context.WithCancel(common.WithLogger(context.Background(), logger))
	cancel()

	d, _, _ := idleArbHarness(t, 1, IdleArbConfig{ReserveHulls: 1, LeashRadius: 123})
	d.Run(ctx)

	start := logger.messageWithPrefix(t, "Idle-gap arb dispatcher running:")
	if !strings.Contains(start, "leash radius 123") {
		t.Fatalf("start-log must surface the leash radius (hidden leash was the masking vector), got: %s", start)
	}
}

// --- sp-8bpr post-leg re-homing tests --------------------------------------

// fakeShipHomer records the hulls the dispatcher asked to re-home, standing in
// for the coordinator's mediator-backed HomeShipCommand dispatch. failNext lets
// a test simulate a homing dispatch that could not even be initiated.
type fakeShipHomer struct {
	homed    []string
	failNext bool
}

func (f *fakeShipHomer) HomeShip(_ context.Context, shipSymbol string) error {
	if f.failNext {
		f.failNext = false
		return fmt.Errorf("home dispatch refused for %s", shipSymbol)
	}
	f.homed = append(f.homed, shipSymbol)
	return nil
}

// idleArbRehomeHarness builds a dispatcher wired with a fake homer over a
// two-market layout where BOTH the hub (0,0) and near (0,50) markets have a
// profitable outbound MACHINERY lane, so an idle hull at EITHER waypoint has a
// lane the arb loop would take absent re-homing. Tests pre-populate repo.ships
// (placing hulls on- or off-station) and pass the standby-station set; the
// dispatcher's cfg.StandbyStations is set from it.
func idleArbRehomeHarness(t *testing.T, repo *idleArbFakeShipRepo, standby []string, cfg IdleArbConfig) (*IdleArbDispatcher, *fakeIdleArbLauncher, *fakeShipHomer) {
	t.Helper()

	hub := idleArbWaypoint(t, "X1-HUB-E42", 0, 0)
	near := idleArbWaypoint(t, "X1-HUB-D40", 0, 50)

	graph := &fakeGraphProvider{waypoints: map[string]*shared.Waypoint{
		hub.Symbol: hub, near.Symbol: near,
	}}
	// Both markets buy MACHINERY at 150 vs a 100 sell — a profitable lane out of
	// hub (hub->near) AND out of near (near->hub), so an idle hull at either end
	// would be arbed if re-homing didn't hold it back.
	markets := &idleArbFakeMarketRepo{markets: map[string]*market.Market{
		hub.Symbol:  marketAt(t, hub.Symbol, tradeGood(t, "MACHINERY", 150, 100)),
		near.Symbol: marketAt(t, near.Symbol, tradeGood(t, "MACHINERY", 150, 100)),
	}}

	cfg.StandbyStations = standby
	clock := shared.NewRealClock()
	launcher := &fakeIdleArbLauncher{repo: repo, clock: clock}
	homer := &fakeShipHomer{}
	d := NewIdleArbDispatcher(repo, markets, graph, launcher, homer, nil, clock, shared.MustNewPlayerID(1), testFleet, cfg)
	return d, launcher, homer
}

// The core gap (sp-8bpr): a hull left idle OFF-station after a leg is re-homed to
// its standby station and is NOT re-arbed from the drift position that pass,
// while an ON-station hull still arbs normally. One pass proves both.
func TestIdleArb_DriftedHullReHomed_OnStationHullStillArbs(t *testing.T) {
	hub := idleArbWaypoint(t, "X1-HUB-E42", 0, 0)
	near := idleArbWaypoint(t, "X1-HUB-D40", 0, 50)
	repo := &idleArbFakeShipRepo{ships: []*navigation.Ship{
		idleArbHull(t, "AT-HUB", hub, testFleet),   // on-station: arb candidate
		idleArbHull(t, "DRIFTED", near, testFleet), // off-station post-leg: re-home
	}}

	d, launcher, homer := idleArbRehomeHarness(t, repo, []string{hub.Symbol}, IdleArbConfig{ReserveHulls: 1})
	d.DispatchOnce(context.Background())

	// The drifted hull is homed, not re-arbed...
	if len(homer.homed) != 1 || homer.homed[0] != "DRIFTED" {
		t.Fatalf("expected the off-station hull DRIFTED to be re-homed, got %v", homer.homed)
	}
	// ...and the on-station hull still flew an arb leg (from the hub), so
	// re-homing did not suppress the harvest for hulls that are already home.
	if len(launcher.launches) != 1 {
		t.Fatalf("expected exactly one arb launch (the on-station hull), got %d", len(launcher.launches))
	}
	if launcher.launches[0].ShipSymbol != "AT-HUB" {
		t.Fatalf("the arb leg must be the on-station hull AT-HUB, never the drifted one, got %s", launcher.launches[0].ShipSymbol)
	}
	if launcher.launches[0].BuyAt != hub.Symbol {
		t.Fatalf("the arb leg must buy at the hub the hull sits on, got %s", launcher.launches[0].BuyAt)
	}
}

// A hull already parked at a configured standby station is left alone — no
// re-home dispatch — so the balancer isn't re-run every tick shuffling home
// hulls between stations (churn).
func TestIdleArb_HullAtStandby_NotReHomed(t *testing.T) {
	hub := idleArbWaypoint(t, "X1-HUB-E42", 0, 0)
	repo := &idleArbFakeShipRepo{ships: []*navigation.Ship{
		idleArbHull(t, "AT-HUB", hub, testFleet),
	}}

	d, _, homer := idleArbRehomeHarness(t, repo, []string{hub.Symbol}, IdleArbConfig{ReserveHulls: 0})
	d.DispatchOnce(context.Background())

	if len(homer.homed) != 0 {
		t.Fatalf("a hull already at a standby station must not be re-homed, got %v", homer.homed)
	}
}

// Re-homing is inert with no standby stations configured (mirroring
// HomeShipCommand's own contract): the drifted hull is left for the arb loop,
// which harvests it exactly as before this change.
func TestIdleArb_NoStandbyStations_ReHomeOff_ArbUnchanged(t *testing.T) {
	near := idleArbWaypoint(t, "X1-HUB-D40", 0, 50)
	repo := &idleArbFakeShipRepo{ships: []*navigation.Ship{
		idleArbHull(t, "DRIFTED-1", near, testFleet),
		idleArbHull(t, "DRIFTED-2", near, testFleet),
	}}

	// Empty standby set → homing disabled even though a homer is wired.
	d, launcher, homer := idleArbRehomeHarness(t, repo, nil, IdleArbConfig{ReserveHulls: 1})
	d.DispatchOnce(context.Background())

	if len(homer.homed) != 0 {
		t.Fatalf("no standby stations means no re-home dispatch, got %v", homer.homed)
	}
	if len(launcher.launches) != 1 {
		t.Fatalf("with re-homing off the arb harvest is unchanged: expected 1 launch, got %d", len(launcher.launches))
	}
}

// RULINGS #7 / requirement: re-homing never touches a hull with an active claim.
// A claimed hull is invisible to FindIdleShipsByFleet, so the sweep never even
// sees it — the atomic ownership model is upheld by reusing the same idle
// discovery every other flow uses.
func TestIdleArb_ClaimedHull_NeverReHomed(t *testing.T) {
	near := idleArbWaypoint(t, "X1-HUB-D40", 0, 50)
	claimed := idleArbHull(t, "CLAIMED", near, testFleet)
	if err := claimed.AssignToContainer("worker-1", shared.NewRealClock()); err != nil {
		t.Fatalf("AssignToContainer: %v", err)
	}
	repo := &idleArbFakeShipRepo{ships: []*navigation.Ship{claimed}}

	d, _, homer := idleArbRehomeHarness(t, repo, []string{"X1-HUB-E42"}, IdleArbConfig{ReserveHulls: 0})
	d.DispatchOnce(context.Background())

	if len(homer.homed) != 0 {
		t.Fatalf("a claimed hull must never be re-homed (active-claim wins, RULINGS #7), got %v", homer.homed)
	}
}

// A failed home dispatch leaves the hull for the next pass AND does not exclude
// it from arb (it was never actually sent home), so a transient homing failure
// can't strand a hull.
func TestIdleArb_HomeDispatchFails_HullNotExcludedFromArb(t *testing.T) {
	near := idleArbWaypoint(t, "X1-HUB-D40", 0, 50)
	repo := &idleArbFakeShipRepo{ships: []*navigation.Ship{
		idleArbHull(t, "DRIFTED-1", near, testFleet),
		idleArbHull(t, "DRIFTED-2", near, testFleet),
	}}

	d, launcher, homer := idleArbRehomeHarness(t, repo, []string{"X1-HUB-E42"}, IdleArbConfig{ReserveHulls: 1})
	homer.failNext = true // the first home dispatch is refused
	d.DispatchOnce(context.Background())

	// One hull's home dispatch failed (recorded nothing); the other homed. The
	// failed one was NOT excluded from arb, so the harvest still ran a leg.
	if len(homer.homed) != 1 {
		t.Fatalf("expected exactly one successful home dispatch (the second hull), got %v", homer.homed)
	}
	if len(launcher.launches) != 1 {
		t.Fatalf("a hull whose home dispatch failed stays arb-eligible: expected 1 launch, got %d", len(launcher.launches))
	}
}

// Re-home activity surfaces in the guard-5 harvest summary message text, so the
// captain's acceptance can read it (the CLI renderer drops metadata maps).
func TestIdleArb_ReHome_CountedInHarvestSummary(t *testing.T) {
	logger := &idleArbCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	near := idleArbWaypoint(t, "X1-HUB-D40", 0, 50)
	repo := &idleArbFakeShipRepo{ships: []*navigation.Ship{
		idleArbHull(t, "DRIFTED", near, testFleet),
	}}
	d, _, _ := idleArbRehomeHarness(t, repo, []string{"X1-HUB-E42"}, IdleArbConfig{ReserveHulls: 0})
	d.DispatchOnce(ctx)

	summary := logger.messageWithPrefix(t, "Idle-arb harvest:")
	if !strings.Contains(summary, "re-homed") {
		t.Fatalf("harvest summary must carry the re-home count in message TEXT, got: %s", summary)
	}
}

// idleArbCapturingLogger records log message text so the guard-5 summary can be
// asserted (the CLI drops metadata, so the counts must live in the text).
type idleArbCapturingLogger struct {
	messages []string
}

func (l *idleArbCapturingLogger) Log(_ string, message string, _ map[string]interface{}) {
	l.messages = append(l.messages, message)
}

func (l *idleArbCapturingLogger) messageWithPrefix(t *testing.T, prefix string) string {
	t.Helper()
	for _, m := range l.messages {
		if strings.HasPrefix(m, prefix) {
			return m
		}
	}
	t.Fatalf("no log message with prefix %q; got %v", prefix, l.messages)
	return ""
}
