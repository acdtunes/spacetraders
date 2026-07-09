package services

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/tactics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

// sp-9aoc (P0 money-integrity): FACTORY input-buying had NO spend floor. After the
// bp6f trade-circuit floor landed, re-enabling 4 goods factories at ~848k treasury
// let them buy inputs in a ~1min burst that crashed the float to 23k — bp6f guarded
// TRADE circuits, not factory INPUT buys. This suite drives buyGood (via ProduceGood
// with a BUY node) through the shared dock-race harness and asserts the per-buy
// backstop: PARK the input purchase (zero-spend result, no dispatch) when it would
// drop live treasury below defaultWorkingCapitalReserve, and PARK fail-CLOSED whenever
// the live treasury read itself fails — a solvency guard must never spend blind.
//
// The harness's economics are deterministic: dockRaceMarketRepo prices dockRaceGood at
// 10/unit with trade_volume 10, and the hull starts with an empty 40-unit hold, so the
// projected input buy is min(40,10)=10 units x 10 = 100 credits every time. Treasury
// figures below are chosen relative to that fixed 100-credit cost and the 50000 reserve.

// spendFloorFakeAPIClient is a minimal live-treasury fake (mirrors the trade floor's
// sfFakeAPIClient): it embeds the port so only GetAgent needs overriding, and a non-nil
// err simulates the live read itself failing — distinct from "no client wired at all",
// whose fail-open contract the whole package's nil-passing tests already depend on.
type spendFloorFakeAPIClient struct {
	domainPorts.APIClient
	credits int
	err     error
}

func (c *spendFloorFakeAPIClient) GetAgent(_ context.Context, _ string) (*player.AgentData, error) {
	if c.err != nil {
		return nil, c.err
	}
	return &player.AgentData{Credits: c.credits}, nil
}

// newSpendFloorExecutor mirrors newDockRaceExecutor exactly (same ship/market/mediator
// economics) but injects a live apiClient so the working-capital floor is ACTIVE. The
// shared newDockRaceExecutor always passes nil (floor disabled); this suite is the one
// place that wires the real port, the same way newCrushedSinkExecutor forks the harness
// to exercise a guard the base helper leaves off.
func newSpendFloorExecutor(t *testing.T, apiClient domainPorts.APIClient) (*ProductionExecutor, *dockRaceShipRepo, *dockRaceMediator) {
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
	marketRepo := &dockRaceMarketRepo{}
	marketLocator := NewMarketLocator(marketRepo, nil, nil, nil)

	executor := NewProductionExecutorWithConfig(
		mediator,
		repo,
		marketRepo,
		marketLocator,
		&dockRaceClock{},
		[]time.Duration{time.Millisecond},
		apiClient,
	)
	return executor, repo, mediator
}

func spendFloorWarnContains(entries []dwellCapturedLogEntry, substr string) bool {
	for _, e := range entries {
		if strings.Contains(e.message, substr) {
			return true
		}
	}
	return false
}

// A treasury far above the 100-credit buy cost but below the 50000 reserve must PARK:
// the buy is trivially affordable, so the ONLY reason to refuse it is the working-
// capital floor. Proves the floor — not affordability — is what stops the drain.
func TestBuyGood_SpendFloor_ParksWhenBuyWouldBreachReserve(t *testing.T) {
	// 40000 - 100 = 39900 < 50000 reserve -> breach.
	executor, repo, mediator := newSpendFloorExecutor(t, &spendFloorFakeAPIClient{credits: 40000})
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(common.WithPlayerToken(context.Background(), "TOKEN-9AOC"), logger)

	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	result, err := executor.ProduceGood(ctx, repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err != nil {
		t.Fatalf("a breaching input buy must be parked gracefully, not surfaced as an error: got %v", err)
	}
	if result == nil || result.QuantityAcquired != 0 || result.TotalCost != 0 {
		t.Fatalf("a parked (spend-floor) buy must yield a zero-spend result, got %+v", result)
	}
	if mediator.purchaseAttempts() != 0 {
		t.Fatalf("a breaching buy must dispatch ZERO purchases, got %d", mediator.purchaseAttempts())
	}

	warns := logger.entriesWithLevel("WARNING")
	// The park cause must be legible in the MESSAGE text (sp-iqyq): the good, the
	// waypoint, and the reserve breach — not buried in a dropped metadata map.
	if !spendFloorWarnContains(warns, "working-capital reserve") {
		t.Fatalf("expected a WARNING naming the working-capital reserve breach, got: %+v", warns)
	}
	if !spendFloorWarnContains(warns, dockRaceGood) || !spendFloorWarnContains(warns, dockRaceMarketWP) {
		t.Fatalf("expected the park WARNING to name the good %s and market %s, got: %+v", dockRaceGood, dockRaceMarketWP, warns)
	}
}

// A treasury that comfortably clears the reserve after the buy must proceed exactly as
// it did before the floor existed: one real purchase, a non-zero result. Proves the
// guard is not overly aggressive once wired.
func TestBuyGood_SpendFloor_ProceedsWhenTreasuryClearsReserve(t *testing.T) {
	// 500000 - 100 = 499900 >= 50000 reserve -> proceed.
	executor, repo, mediator := newSpendFloorExecutor(t, &spendFloorFakeAPIClient{credits: 500000})
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(common.WithPlayerToken(context.Background(), "TOKEN-9AOC"), logger)

	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	result, err := executor.ProduceGood(ctx, repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err != nil {
		t.Fatalf("a clearing input buy must proceed, got error: %v", err)
	}
	if result == nil || result.QuantityAcquired <= 0 {
		t.Fatalf("expected a successful purchase when the treasury clears the reserve, got %+v", result)
	}
	if mediator.purchaseAttempts() != 1 {
		t.Fatalf("expected exactly 1 purchase for a clearing buy, got %d", mediator.purchaseAttempts())
	}
	if spendFloorWarnContains(logger.entriesWithLevel("WARNING"), "working-capital reserve") {
		t.Fatalf("a clearing buy must not log a spend-floor park WARNING, got: %+v", logger.entriesWithLevel("WARNING"))
	}
}

// The live treasury read itself failing (GetAgent erroring) must fail CLOSED: the buy
// is parked, never dispatched. A solvency guard that let a buy through on a blind read
// would defeat its own purpose — the exact hole that let the drain happen.
func TestBuyGood_SpendFloor_ParksFailClosedWhenLiveReadErrors(t *testing.T) {
	executor, repo, mediator := newSpendFloorExecutor(t, &spendFloorFakeAPIClient{err: errors.New("agent API unavailable")})
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(common.WithPlayerToken(context.Background(), "TOKEN-9AOC"), logger)

	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	result, err := executor.ProduceGood(ctx, repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err != nil {
		t.Fatalf("a blind live-read must park gracefully, not surface an error: got %v", err)
	}
	if result == nil || result.QuantityAcquired != 0 || result.TotalCost != 0 {
		t.Fatalf("a fail-closed park must yield a zero-spend result, got %+v", result)
	}
	if mediator.purchaseAttempts() != 0 {
		t.Fatalf("a fail-closed park must dispatch ZERO purchases, got %d", mediator.purchaseAttempts())
	}
	if !spendFloorWarnContains(logger.entriesWithLevel("WARNING"), "fail-closed") {
		t.Fatalf("expected a fail-closed WARNING explaining the blind read, got: %+v", logger.entriesWithLevel("WARNING"))
	}
}

// An unresolvable player token must also fail CLOSED — even with an ample treasury the
// guard cannot verify, so it must park rather than spend. The generous credits prove
// the park comes from the blind token, not the treasury figure.
func TestBuyGood_SpendFloor_ParksFailClosedWhenTokenMissing(t *testing.T) {
	executor, repo, mediator := newSpendFloorExecutor(t, &spendFloorFakeAPIClient{credits: 1000000})
	logger := &dwellCapturingLogger{}
	// No WithPlayerToken: PlayerTokenFromContext must fail and the guard must park.
	ctx := common.WithLogger(context.Background(), logger)

	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	result, err := executor.ProduceGood(ctx, repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err != nil {
		t.Fatalf("a missing token must park gracefully, not surface an error: got %v", err)
	}
	if result == nil || result.QuantityAcquired != 0 || result.TotalCost != 0 {
		t.Fatalf("a fail-closed park must yield a zero-spend result, got %+v", result)
	}
	if mediator.purchaseAttempts() != 0 {
		t.Fatalf("a missing-token park must dispatch ZERO purchases, got %d", mediator.purchaseAttempts())
	}
	if !spendFloorWarnContains(logger.entriesWithLevel("WARNING"), "fail-closed") {
		t.Fatalf("expected a fail-closed WARNING for the unresolvable token, got: %+v", logger.entriesWithLevel("WARNING"))
	}
}

// No apiClient wired (e.apiClient == nil) fails OPEN: the floor is simply unavailable,
// so the buy proceeds. This is the optional-port contract every other test in this
// package relies on by passing nil — an explicit guard so a future change that made nil
// fail-closed (silently parking every factory buy in the whole suite) is caught here.
func TestBuyGood_SpendFloor_ProceedsWhenNoClientWired_FailOpen(t *testing.T) {
	executor, repo, mediator := newSpendFloorExecutor(t, nil)

	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	result, err := executor.ProduceGood(context.Background(), repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err != nil {
		t.Fatalf("with no client wired the floor must fail open and proceed, got error: %v", err)
	}
	if result == nil || result.QuantityAcquired <= 0 {
		t.Fatalf("a fail-open (no-client) buy must proceed to a real purchase, got %+v", result)
	}
	if mediator.purchaseAttempts() != 1 {
		t.Fatalf("expected exactly 1 purchase on the fail-open path, got %d", mediator.purchaseAttempts())
	}
}
