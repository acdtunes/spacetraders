package commands

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	mfgServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// newNoWorkFixture builds the same FAB_PLATE<-IRON coordinator wiring as
// newFactoryFixture (two haulers discoverable as idle, so Step 3's
// waitForIdleHaulers succeeds immediately) but accepts an injectable clock
// and rejects every ClaimShip call. This models sp-2q2o's "no claimable
// in-system hull" scenario: ships are visible to discovery, but none can
// actually be claimed for a production node, so every node parks (the
// sp-vsfn catch-all in executeLevelParallel) and the run completes having
// done zero work.
func newNoWorkFixture(t *testing.T, clock shared.Clock) *factoryFixture {
	t.Helper()

	shipA := newTestHauler(t, "CRAFTY-2", nil)
	shipB := newTestHauler(t, "CRAFTY-3", nil)

	shipRepo := &factoryFakeShipRepo{
		ships: map[string]*navigation.Ship{
			shipA.ShipSymbol(): shipA,
			shipB.ShipSymbol(): shipB,
		},
		order:    []string{shipA.ShipSymbol(), shipB.ShipSymbol()},
		claimErr: errors.New("no claimable ship for factory node"),
	}
	marketRepo := &factoryFakeMarketRepo{}
	fakeMediator := &factoryFakeMediator{}

	resolver := mfgServices.NewSupplyChainResolver(
		map[string][]string{testOutputGood: {testInputGood}},
		marketRepo,
	)
	marketLocator := mfgServices.NewMarketLocator(marketRepo, nil, nil, nil)

	handler := NewRunFactoryCoordinatorHandler(
		fakeMediator, shipRepo, marketRepo, resolver, marketLocator, clock, nil,
	)

	cmd := &RunFactoryCoordinatorCommand{
		PlayerID:      1,
		TargetGood:    testOutputGood,
		SystemSymbol:  testSystem,
		ContainerID:   testContainerID,
		MaxIterations: -1, // infinite mode - the mode the spin bug requires
	}

	return &factoryFixture{handler: handler, shipRepo: shipRepo, mediator: fakeMediator, cmd: cmd}
}

// sp-2q2o: a -1 factory with no claimable in-system hull must back off
// noWorkIterationDelay before Handle returns, and log the reason exactly
// once (not on every iteration) - the fix for the ~280 no-op iterations/sec
// spin (8,377 in 30s) that rotated the chain-margin guard's own park verdict
// out of the per-container log ring.
func TestFactoryCoordinator_NoWorkIteration_BacksOffAndLogsOnce(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	f := newNoWorkFixture(t, clock)

	logger := &capturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	before := clock.CurrentTime
	resp, err := f.handler.Handle(ctx, f.cmd)
	if err != nil {
		t.Fatalf("expected a clean completed response for the no-claimable-hull scenario, got error: %v", err)
	}
	coordResp, ok := resp.(*RunFactoryCoordinatorResponse)
	if !ok || !coordResp.Completed {
		t.Fatalf("expected a completed response despite zero claimable nodes, got %+v", resp)
	}
	if coordResp.NodesCompleted != 0 {
		t.Fatalf("expected zero nodes completed (every claim rejected), got %d", coordResp.NodesCompleted)
	}
	if coordResp.NoWorkReason == "" {
		t.Fatalf("expected NoWorkReason to be set for a no-op iteration")
	}

	if elapsed := clock.CurrentTime.Sub(before); elapsed < noWorkIterationDelay {
		t.Fatalf("expected Handle to back off at least noWorkIterationDelay (%v) before returning, clock only advanced by %v", noWorkIterationDelay, elapsed)
	}

	waitingLines := 0
	entries := logger.snapshot()
	for _, e := range entries {
		if strings.Contains(e.message, "waiting for workers") {
			waitingLines++
		}
	}
	if waitingLines != 1 {
		t.Fatalf("expected exactly one 'waiting for workers' log line, got %d (entries: %+v)", waitingLines, entries)
	}
}

// A productive iteration - even one run in infinite (-1) mode, the same mode
// the no-work backoff applies to - must return at its normal pace. The
// backoff must never punish a factory that's actually producing.
func TestFactoryCoordinator_WorkFoundIteration_NoBackoffAdded(t *testing.T) {
	f := newFactoryFixture(t)
	f.cmd.MaxIterations = -1

	logger := &capturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	resp, err := f.handler.Handle(ctx, f.cmd)
	if err != nil {
		t.Fatalf("coordinator failed: %v", err)
	}
	coordResp, ok := resp.(*RunFactoryCoordinatorResponse)
	if !ok || !coordResp.Completed {
		t.Fatalf("expected a completed response, got %+v", resp)
	}
	if coordResp.NodesCompleted == 0 {
		t.Fatalf("sanity check failed: expected the standard fixture to complete at least one node")
	}
	if coordResp.NoWorkReason != "" {
		t.Fatalf("expected NoWorkReason empty for a productive iteration, got %q", coordResp.NoWorkReason)
	}

	entries := logger.snapshot()
	for _, e := range entries {
		if strings.Contains(e.message, "waiting for workers") {
			t.Fatalf("expected no no-work backoff log line for a productive iteration, got: %+v", entries)
		}
	}
}

// sp-2q2o also requires that container shutdown not be delayed by the
// backoff: sleepInterruptibly must race the (uncancellable) clock sleep
// against ctx.Done() and return the instant the context is cancelled, not
// after the full noWorkIterationDelay.
func TestSleepInterruptibly_ContextCancelled_ReturnsPromptly(t *testing.T) {
	handler := newFactoryHandlerWithClock(t, shared.NewRealClock())

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	handler.sleepInterruptibly(ctx, 5*time.Second)
	elapsed := time.Since(start)

	if elapsed >= 1*time.Second {
		t.Fatalf("expected context cancellation to interrupt a 5s sleep promptly, took %v", elapsed)
	}
}
