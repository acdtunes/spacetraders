package services

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/tactics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

// BuyAtTerminalFactory buys at the waypoint phase 1's GateTopology already resolved as this
// era's EXPORTER of a gate material. The whole point is that it does NOT re-resolve: the
// source selector picks by supply/activity/price and is free to choose a DIFFERENT market,
// which would silently undo the topology resolution the caller just performed.
//
// Proving "it did not re-resolve" needs a fixture in which re-resolving is OBSERVABLE, so the
// harness serves TWO in-system exporters of the good and pins the buy to the one the selector
// would NOT pick. A substituted source then shows up in two independent places: the result's
// waypoint (which comes from the source struct the fill loop was handed) and the ship's own
// persisted position (which comes from the navigation the executor actually performed).
const (
	// pinnedFactoryWP is the terminal factory phase 1 resolved by EXPORT ROLE. Synthetic:
	// production code carries no waypoint literals.
	pinnedFactoryWP = "X1-DR-TERMINALFAC"
	// decoyMarketWP is the market the supply-first selector strictly PREFERS — ABUNDANT
	// outranks the factory's HIGH, and supply outranks activity and price in
	// FindExportMarketBySupplyPriority. Any re-resolve lands here, never at the factory.
	decoyMarketWP = "X1-DR-DECOY"
	// ladderFactoryWP / ladderPeerWP drive the pinned-source price-ceiling test: a terminal
	// factory whose ask ladders under our own draw, plus a stable peer that anchors the
	// cross-market eligible median low so the ladder crosses the ceiling mid-fill.
	ladderFactoryWP = "X1-DR-LADDERFAC"
	ladderPeerWP    = "X1-DR-LADDERPEER"
)

// pinnedSourceMarketRepo serves two exporters of dockRaceGood at the SAME ask (10) and the same
// per-transaction trade volume (10), differing only in supply/activity:
//
//	decoyMarketWP    ABUNDANT / WEAK    <- what selectInputSource would pick
//	pinnedFactoryWP  HIGH / STRONG      <- what phase 1 resolved by EXPORT role
//
// Equal asks keep the cross-market price ceiling (median 10 over 2 eligible sources, ceiling
// 15) out of the way, so the ONLY thing this fixture varies is which waypoint a re-resolve
// would choose.
type pinnedSourceMarketRepo struct {
	market.MarketRepository
}

func (r *pinnedSourceMarketRepo) FindAllMarketsInSystem(_ context.Context, _ string, _ int) ([]string, error) {
	return []string{pinnedFactoryWP, decoyMarketWP}, nil
}

func (r *pinnedSourceMarketRepo) GetMarketData(_ context.Context, waypointSymbol string, _ int) (*market.Market, error) {
	supply, activity := supplyHigh, "STRONG"
	switch waypointSymbol {
	case decoyMarketWP:
		supply, activity = "ABUNDANT", "WEAK"
	case pinnedFactoryWP:
	default:
		return nil, nil
	}
	good, err := market.NewTradeGood(dockRaceGood, &supply, &activity, 10, 8, 10, market.TradeTypeExport)
	if err != nil {
		return nil, err
	}
	return market.NewMarket(waypointSymbol, []market.TradeGood{*good}, time.Now())
}

// FindCheapestMarketSelling is a FORWARD guard, not the load-bearing pin assertion: no
// production code in this package currently routes source selection through it
// (selectInputSource goes FindExportMarketBySupplyPriority -> FindAllMarketsInSystem +
// GetMarketData, and FindExportMarket the same way). It refuses loudly so a future re-wiring
// of the selector onto this method cannot quietly restore a fallback. The assertion that
// actually pins the source is the decoy waypoint above.
func (r *pinnedSourceMarketRepo) FindCheapestMarketSelling(_ context.Context, _, _ string, _ int) (*market.CheapestMarketResult, error) {
	return nil, errors.New("source selector consulted: BuyAtTerminalFactory must buy at the PINNED terminal factory")
}

func (r *pinnedSourceMarketRepo) FindBestMarketBuying(_ context.Context, _, _ string, _ int) (*market.BestMarketBuyingResult, error) {
	return nil, nil
}

// shipLocation reports the waypoint the harness persists the ship at — the second, independent
// observation of WHERE a buy happened. result.WaypointSymbol echoes the source struct the fill
// loop was handed; this echoes the navigation the executor actually performed.
func (r *dockRaceShipRepo) shipLocation() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.location
}

// newPinnedSourceExecutor mirrors newDockRaceExecutor but over the two-market repo, and
// optionally with a live-treasury client so the money floor can be driven. cargoCapacity 40,
// market trade_volume 10, ask 10 — same numbers as the hull-fill harness.
func newPinnedSourceExecutor(t *testing.T, apiClient domainPorts.APIClient) (*ProductionExecutor, *dockRaceShipRepo, *dockRaceMediator) {
	t.Helper()

	repo := &dockRaceShipRepo{
		location:      dockRaceOrigin,
		navStatus:     navigation.NavStatusDocked,
		cargoCapacity: 40,
	}
	mediator := &dockRaceMediator{
		repo:        repo,
		dockHandler: tactics.NewDockShipHandler(repo),
	}
	marketRepo := &pinnedSourceMarketRepo{}
	executor := NewProductionExecutorWithConfig(
		mediator,
		repo,
		marketRepo,
		NewMarketLocator(marketRepo, nil, nil, nil),
		&dockRaceClock{},
		[]time.Duration{time.Millisecond},
		apiClient,
	)
	return executor, repo, mediator
}

// pinnedSource is the terminal-factory quote phase 1's TerminalFactory hands back.
func pinnedSource() *MarketLocatorResult {
	return &MarketLocatorResult{
		WaypointSymbol: pinnedFactoryWP,
		Supply:         supplyHigh,
		Activity:       "STRONG",
		Price:          10,
		TradeVolume:    10,
	}
}

// THE PINNING. The buy happens at the source it was GIVEN. Re-resolving would be free to pick
// a different market and silently undo the topology resolution — so the fixture serves a market
// the selector strictly prefers, and landing there fails the test.
func TestBuyAtTerminalFactory_BuysAtThePinnedSourceWithoutConsultingTheSelector(t *testing.T) {
	executor, repo, mediator := newPinnedSourceExecutor(t, nil)

	result, err := executor.BuyAtTerminalFactory(context.Background(), repo.buildShip(),
		dockRaceGood, pinnedSource(), 30, "X1-DR", 1, nil)
	if err != nil {
		t.Fatalf("BuyAtTerminalFactory returned error: %v", err)
	}
	if result == nil || result.WaypointSymbol != pinnedFactoryWP {
		t.Fatalf("result = %+v, want the buy recorded at the pinned terminal factory %s; a re-resolved source lands at %s", result, pinnedFactoryWP, decoyMarketWP)
	}
	if repo.shipLocation() != pinnedFactoryWP {
		t.Fatalf("ship flew to %s, want the pinned terminal factory %s — the source was substituted, not honoured", repo.shipLocation(), pinnedFactoryWP)
	}
	if result.QuantityAcquired != 30 {
		t.Fatalf("QuantityAcquired = %d, want the 30 units planned", result.QuantityAcquired)
	}
	if mediator.purchaseAttempts() != 3 {
		t.Fatalf("30 units at trade_volume 10 must take 3 tranche buys, got %d", mediator.purchaseAttempts())
	}
}

// The units the fill planner allocated are a CAP, not a suggestion: buying past them would
// over-supply a material whose bill the trip already sized, or steal hold space the trip
// allocated to the other material.
func TestBuyAtTerminalFactory_NeverBuysPastTheUnitsItWasGiven(t *testing.T) {
	executor, repo, mediator := newPinnedSourceExecutor(t, nil)

	result, err := executor.BuyAtTerminalFactory(context.Background(), repo.buildShip(),
		dockRaceGood, pinnedSource(), 12, "X1-DR", 1, nil)
	if err != nil {
		t.Fatalf("BuyAtTerminalFactory returned error: %v", err)
	}
	if result.QuantityAcquired != 12 {
		t.Fatalf("QuantityAcquired = %d, want exactly the 12 units allocated (hull holds 40)", result.QuantityAcquired)
	}
	if mediator.purchaseAttempts() != 2 {
		t.Fatalf("12 units at trade_volume 10 must take exactly 2 tranche buys (10+2), got %d", mediator.purchaseAttempts())
	}
}

// MONEY GUARD, UNCHANGED. The per-tranche working-capital floor still governs this buy and
// still fails CLOSED: when the next tranche would breach, the fill stops and delivers what is
// aboard rather than forcing the buy. Treasury is scripted to deplete mid-fill, so the STOP
// here is the floor tripping between tranches — not the guard going blind, which would park
// everything and is what the exact counts below distinguish.
//
// Each tranche is 10 units x ask 10 = 100 credits. Credits floor+1000 clears tranche 1
// (…-100 >= floor); credits floor-1 breaches on tranche 2. So: 10 units over exactly 1 buy.
func TestBuyAtTerminalFactory_StopsWhenTheNextTrancheWouldBreachTheSpendFloor(t *testing.T) {
	depleting := &sequentialCreditsAPIClient{credits: []int{
		effectiveReserveFloor() + 1000,
		effectiveReserveFloor() - 1,
	}}
	executor, repo, mediator := newPinnedSourceExecutor(t, depleting)
	logger := &dwellCapturingLogger{}
	// The token is load-bearing: without it the treasury read fails and the floor parks the
	// FIRST tranche fail-closed, so the scripted credits would never be consulted and the test
	// would assert nothing about a depleting treasury.
	ctx := common.WithLogger(common.WithPlayerToken(context.Background(), "TOKEN-GATE-BUY"), logger)

	result, err := executor.BuyAtTerminalFactory(ctx, repo.buildShip(),
		dockRaceGood, pinnedSource(), 40, "X1-DR", 1, nil)
	if err != nil {
		t.Fatalf("BuyAtTerminalFactory returned error: %v", err)
	}
	if result.QuantityAcquired >= 40 {
		t.Fatalf("QuantityAcquired = %d — the fill ran to completion through a breached spend floor", result.QuantityAcquired)
	}
	if result.QuantityAcquired != 10 {
		t.Fatalf("QuantityAcquired = %d, want exactly the 10 units of the tranche that CLEARED the floor before the next one breached", result.QuantityAcquired)
	}
	if mediator.purchaseAttempts() != 1 {
		t.Fatalf("purchaseAttempts = %d, want exactly 1 — the floor must stop the loop after the clearing tranche, not merely slow it", mediator.purchaseAttempts())
	}
	if !spendFloorWarnContains(logger.entriesWithLevel("WARNING"), "working-capital reserve") {
		t.Fatalf("expected a WARNING naming the working-capital reserve when the fill stopped, got: %+v", logger.entriesWithLevel("WARNING"))
	}
}

// MONEY GUARD, UNCHANGED (second one). The cross-container concurrent-spend reservation also
// still governs this buy, and also fails CLOSED: a rejected reservation parks the gate buy with
// zero spend and zero dispatch. Treasury clears the floor comfortably, so the ONLY thing that
// can refuse this buy is the cap.
func TestBuyAtTerminalFactory_ParksWhenTheConcurrentSpendCapRefuses(t *testing.T) {
	executor, repo, mediator := newPinnedSourceExecutor(t, &spendFloorFakeAPIClient{credits: 5000000})
	ledger := &fakeSpendLedger{reserveOK: false}
	executor.SetSpendLedger(ledger)
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(common.WithPlayerToken(context.Background(), "TOKEN-GATE-BUY"), logger)

	result, err := executor.BuyAtTerminalFactory(ctx, repo.buildShip(),
		dockRaceGood, pinnedSource(), 40, "X1-DR", 1, nil)
	if err != nil {
		t.Fatalf("a cap-parked gate buy must be graceful, not an error: got %v", err)
	}
	if result == nil || result.QuantityAcquired != 0 || result.TotalCost != 0 {
		t.Fatalf("a cap-parked gate buy must yield a zero-spend result, got %+v", result)
	}
	if mediator.purchaseAttempts() != 0 {
		t.Fatalf("a cap-parked gate buy must dispatch ZERO purchases, got %d", mediator.purchaseAttempts())
	}
	if ledger.reserveCalls != 1 {
		t.Fatalf("the gate buy must consult the concurrent-spend ledger exactly once, got %d calls", ledger.reserveCalls)
	}
	if !spendFloorWarnContains(logger.entriesWithLevel("WARNING"), "concurrent spend cap") {
		t.Fatalf("expected a WARNING naming the concurrent spend cap, got: %+v", logger.entriesWithLevel("WARNING"))
	}
}

// PRICE CEILING, UNCHANGED — and the only enforcement of the sourceModeEligible argument.
// trancheAsk gates the per-tranche cross-market ceiling on `mode == sourceModeEligible`
// (production_executor.go, trancheAsk), so handing fillFromSource a weaker mode —
// sourceModeGateFill or sourceModeRescue — would silently switch an existing guard off while
// every other test in this file stayed green. Nothing but this test stands behind that
// argument, so it asserts the STOP CAUSE, not merely that a stop happened.
//
// The pinned source ladders under our own draw (ask 100, +1000 per completed buy) against a
// stable peer at 120 that anchors the eligible cross-market median low. Tranche 1 clears (ask
// 100 vs ceiling 1.5 x median(100,120)=165); tranche 2's ask 1100 exceeds 1.5 x
// median(1100,120)=915, so the fill stops and the hull delivers the 10 units aboard rather
// than ladder-chasing the factory it is pinned to.
func TestBuyAtTerminalFactory_StopsWhenThePinnedSourceLaddersPastThePriceCeiling(t *testing.T) {
	shipRepo := &dockRaceShipRepo{location: dockRaceOrigin, navStatus: navigation.NavStatusDocked, cargoCapacity: 40}
	mediator := &dockRaceMediator{repo: shipRepo, dockHandler: tactics.NewDockShipHandler(shipRepo)}
	marketRepo := &ladderingMarketRepo{
		mediator: mediator,
		sourceWP: ladderFactoryWP, peerWP: ladderPeerWP,
		baseAsk: 100, step: 1000, peerAsk: 120, tvol: 10,
	}
	executor := NewProductionExecutorWithConfig(
		mediator, shipRepo, marketRepo, NewMarketLocator(marketRepo, nil, nil, nil),
		&dockRaceClock{}, []time.Duration{time.Millisecond}, nil,
	)
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	pinnedLadder := &MarketLocatorResult{
		WaypointSymbol: ladderFactoryWP,
		Supply:         supplyHigh,
		Activity:       "STRONG",
		Price:          100,
		TradeVolume:    10,
	}

	result, err := executor.BuyAtTerminalFactory(ctx, shipRepo.buildShip(),
		dockRaceGood, pinnedLadder, 40, "X1-DR", 1, nil)
	if err != nil {
		t.Fatalf("BuyAtTerminalFactory returned error: %v", err)
	}
	if result == nil || result.QuantityAcquired != 10 {
		t.Fatalf("result = %+v, want the fill STOPPED at 10 units when the pinned source laddered past the price ceiling", result)
	}
	if mediator.purchaseAttempts() != 1 {
		t.Fatalf("purchaseAttempts = %d, want exactly 1 — the ceiling must stop the loop after the clearing tranche", mediator.purchaseAttempts())
	}
	if result.WaypointSymbol != ladderFactoryWP {
		t.Fatalf("result.WaypointSymbol = %s, want the pinned laddering factory %s", result.WaypointSymbol, ladderFactoryWP)
	}
	// The STOP CAUSE, named: without this the test would also pass if the fill stopped for an
	// unrelated reason (drained market, no forward progress) with the ceiling switched off.
	if !spendFloorWarnContains(logger.entriesWithLevel("INFO"), "exceeds price ceiling") {
		t.Fatalf("expected the per-tranche price ceiling to be the stop cause and to say so, got: %+v", logger.entriesWithLevel("INFO"))
	}
}

// FAIL CLOSED on a source that cannot be bought from. A zero trade volume means no transaction
// size exists; buying blind against it is how a fill spins.
func TestBuyAtTerminalFactory_RefusesAnUnbuyableSource(t *testing.T) {
	executor, repo, mediator := newPinnedSourceExecutor(t, nil)

	zeroVolume := pinnedSource()
	zeroVolume.TradeVolume = 0
	if _, err := executor.BuyAtTerminalFactory(context.Background(), repo.buildShip(),
		dockRaceGood, zeroVolume, 30, "X1-DR", 1, nil); err == nil {
		t.Fatal("BuyAtTerminalFactory accepted a source with trade_volume 0; want a refusal")
	}
	if mediator.purchaseAttempts() != 0 {
		t.Fatalf("purchaseAttempts = %d — nothing may be bought against an unbuyable source", mediator.purchaseAttempts())
	}
}

// A nil/unnamed source, or a non-positive unit allocation, is a caller bug. Refuse rather than
// resolve a source ourselves — resolving one here is exactly the pinning this method exists to
// preserve, and substituting a market is the failure mode the whole phase is about.
func TestBuyAtTerminalFactory_RefusesANilSourceOrANonPositiveAllocation(t *testing.T) {
	cases := []struct {
		name   string
		source *MarketLocatorResult
		units  int
	}{
		{"nil source", nil, 30},
		{"source with no waypoint", &MarketLocatorResult{Price: 10, TradeVolume: 10}, 30},
		{"zero-unit allocation", pinnedSource(), 0},
		{"negative allocation", pinnedSource(), -5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			executor, repo, mediator := newPinnedSourceExecutor(t, nil)

			result, err := executor.BuyAtTerminalFactory(context.Background(), repo.buildShip(),
				dockRaceGood, tc.source, tc.units, "X1-DR", 1, nil)
			if err == nil {
				t.Fatalf("BuyAtTerminalFactory accepted %s; want a refusal, never a self-resolved fallback (got %+v)", tc.name, result)
			}
			if mediator.purchaseAttempts() != 0 {
				t.Fatalf("purchaseAttempts = %d — a refused gate buy must dispatch nothing", mediator.purchaseAttempts())
			}
			if repo.shipLocation() != dockRaceOrigin {
				t.Fatalf("ship flew to %s — a refused gate buy must not even navigate, let alone substitute a market", repo.shipLocation())
			}
		})
	}
}

// BuyAtTerminalFactory takes its source as an argument precisely so no waypoint is ever
// decided here. A literal in this file would be a source the caller did not choose.
func TestBuyAtTerminalFactorySource_ContainsNoWaypointLiterals(t *testing.T) {
	const file = "production_executor_gate_buy.go"
	waypointLiteral := regexp.MustCompile(`"[A-Z]\d+-[A-Z0-9]+-[A-Z0-9]+"`)

	// Four shapes, not one. A regression that TIGHTENS the pattern still matches X1-KP23-F46,
	// so a single-string calibration stays green while the guard goes blind to the rest.
	for _, known := range []string{
		`x := "X1-KP23-F46"`,
		`"X1-UM5-J59"`,
		`"X1-DR-GATE"`,
		`"X1-BETA-MARKETPLACE"`,
	} {
		if !waypointLiteral.MatchString(known) {
			t.Fatalf("waypoint-literal pattern failed its own calibration on %s", known)
		}
	}
	// The goods are the invariant. A pattern that flagged them would be unusable and deleted.
	for _, invariant := range []string{`good := "FAB_MATS"`, `good := "ADVANCED_CIRCUITRY"`} {
		if waypointLiteral.MatchString(invariant) {
			t.Fatalf("waypoint-literal pattern flags %s; goods are era-invariant and must be nameable directly", invariant)
		}
	}

	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("reading %s: %v", file, err)
	}
	// Prove the sweep read the buy itself. An emptied or renamed file would otherwise pass
	// this guard forever while scanning nothing.
	if !strings.Contains(string(src), "BuyAtTerminalFactory") {
		t.Fatalf("%s does not contain BuyAtTerminalFactory; the guard is reading the wrong file and would pass vacuously", file)
	}
	if found := waypointLiteral.FindAllString(string(src), -1); len(found) > 0 {
		t.Fatalf("%s contains hardcoded waypoint symbols %v — the source is the caller's, never this file's", file, found)
	}
	if strings.Contains(string(src), "X1-") {
		t.Fatalf("%s references an X1- prefixed symbol", file)
	}
}
