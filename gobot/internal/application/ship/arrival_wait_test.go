package ship

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// --- Test doubles (arrival wait) --------------------------------------------

// fakeArrivalSubscriber is a controllable ShipEventSubscriber: SubscribeArrived
// returns a caller-supplied channel so a test can simulate an ARRIVED event
// being delivered promptly, or never delivered at all (the lost/raced event
// that sp-pafv guards against).
type fakeArrivalSubscriber struct {
	ch chan domainNavigation.ShipArrivedEvent
}

func (f *fakeArrivalSubscriber) SubscribeArrived(string) <-chan domainNavigation.ShipArrivedEvent {
	return f.ch
}
func (f *fakeArrivalSubscriber) UnsubscribeArrived(string, <-chan domainNavigation.ShipArrivedEvent) {
}
func (f *fakeArrivalSubscriber) SubscribeWorkerCompleted(string) <-chan domainNavigation.WorkerCompletedEvent {
	return nil
}
func (f *fakeArrivalSubscriber) UnsubscribeWorkerCompleted(string, <-chan domainNavigation.WorkerCompletedEvent) {
}
func (f *fakeArrivalSubscriber) SubscribeTasksBecameReady(int) <-chan domainNavigation.TasksBecameReadyEvent {
	return nil
}
func (f *fakeArrivalSubscriber) UnsubscribeTasksBecameReady(int, <-chan domainNavigation.TasksBecameReadyEvent) {
}
func (f *fakeArrivalSubscriber) SubscribeTransportRequested(int) <-chan domainNavigation.TransportRequestedEvent {
	return nil
}
func (f *fakeArrivalSubscriber) UnsubscribeTransportRequested(int, <-chan domainNavigation.TransportRequestedEvent) {
}
func (f *fakeArrivalSubscriber) SubscribeTransferCompleted(int) <-chan domainNavigation.TransferCompletedEvent {
	return nil
}
func (f *fakeArrivalSubscriber) UnsubscribeTransferCompleted(int, <-chan domainNavigation.TransferCompletedEvent) {
}

// fakeShipQueryRepo scripts what a resync reload returns, so tests can drive
// the "resync confirms arrival" and "resync still shows in transit" paths
// deterministically without a real database.
type fakeShipQueryRepo struct {
	findBySymbolFunc func() (*domainNavigation.Ship, error)
	calls            int
}

func (f *fakeShipQueryRepo) FindBySymbol(_ context.Context, _ string, _ shared.PlayerID) (*domainNavigation.Ship, error) {
	f.calls++
	return f.findBySymbolFunc()
}
func (f *fakeShipQueryRepo) GetShipData(_ context.Context, _ string, _ shared.PlayerID) (*domainNavigation.ShipData, error) {
	return nil, fmt.Errorf("fakeShipQueryRepo: GetShipData not implemented")
}
func (f *fakeShipQueryRepo) FindAllByPlayer(_ context.Context, _ shared.PlayerID) ([]*domainNavigation.Ship, error) {
	return nil, fmt.Errorf("fakeShipQueryRepo: FindAllByPlayer not implemented")
}

// noopLogger discards every log call.
type noopLogger struct{}

func (noopLogger) Log(string, string, map[string]interface{}) {}

func newArrivalWaitTestShip(t *testing.T, navStatus domainNavigation.NavStatus) *domainNavigation.Ship {
	t.Helper()
	loc := mustWaypoint(t, "X1-TEST-A", 0, 0)
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	cargo, err := shared.NewCargo(40, 0, nil)
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	ship, err := domainNavigation.NewShip(
		"ARRIVAL-WAIT-1",
		shared.MustNewPlayerID(1),
		loc,
		fuel,
		100,
		40,
		cargo,
		9,
		"FRAME_HAULER",
		"HAULER",
		nil,
		navStatus,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	return ship
}

// --- Tests -------------------------------------------------------------------

// TestWaitForShipArrivalCore_EventArrives_HappyPathUnchanged pins the existing
// behavior: when the ARRIVED event shows up, the wait returns success
// immediately and never touches the resync path (sp-pafv must not change this).
func TestWaitForShipArrivalCore_EventArrives_HappyPathUnchanged(t *testing.T) {
	ship := newArrivalWaitTestShip(t, domainNavigation.NavStatusInTransit)
	sub := &fakeArrivalSubscriber{ch: make(chan domainNavigation.ShipArrivedEvent, 1)}
	sub.ch <- domainNavigation.ShipArrivedEvent{
		ShipSymbol: ship.ShipSymbol(),
		Location:   "X1-TEST-A",
		Status:     domainNavigation.NavStatusInOrbit,
	}
	repo := &fakeShipQueryRepo{findBySymbolFunc: func() (*domainNavigation.Ship, error) {
		t.Fatalf("resync should not be triggered when the event arrives promptly")
		return nil, nil
	}}

	err := waitForShipArrivalCore(context.Background(), repo, sub, ship, shared.MustNewPlayerID(1), 5, noopLogger{}, 50*time.Millisecond, 2)
	if err != nil {
		t.Fatalf("expected success on event arrival, got: %v", err)
	}
	if ship.NavStatus() != domainNavigation.NavStatusInOrbit {
		t.Fatalf("expected ship to have Arrive()'d, got status %s", ship.NavStatus())
	}
}

// TestWaitForShipArrivalCore_EventLost_ResyncConfirmsArrival is sp-pafv's core
// case: the ARRIVED event never shows up (lost/raced against subscription,
// ShipEventBus's non-blocking, non-replaying send), but the repository -
// already updated by ShipStateScheduler before it published - confirms the
// ship has left transit. The wait must resync and succeed instead of hanging.
func TestWaitForShipArrivalCore_EventLost_ResyncConfirmsArrival(t *testing.T) {
	ship := newArrivalWaitTestShip(t, domainNavigation.NavStatusInTransit)
	sub := &fakeArrivalSubscriber{ch: make(chan domainNavigation.ShipArrivedEvent, 1)} // never fed: simulates a lost event
	repo := &fakeShipQueryRepo{findBySymbolFunc: func() (*domainNavigation.Ship, error) {
		return newArrivalWaitTestShip(t, domainNavigation.NavStatusInOrbit), nil
	}}

	err := waitForShipArrivalCore(context.Background(), repo, sub, ship, shared.MustNewPlayerID(1), 5, noopLogger{}, 20*time.Millisecond, 3)
	if err != nil {
		t.Fatalf("expected resync to recover from a lost event, got: %v", err)
	}
	if ship.NavStatus() != domainNavigation.NavStatusInOrbit {
		t.Fatalf("expected ship to have Arrive()'d after resync, got status %s", ship.NavStatus())
	}
	if repo.calls != 1 {
		t.Fatalf("expected exactly one resync attempt before success, got %d", repo.calls)
	}
}

// TestWaitForShipArrivalCore_EventLostRepeatedly_ExhaustsToTypedError proves the
// wait does NOT hang forever: if the event never arrives AND every resync still
// shows IN_TRANSIT, the wait gives up after maxAttempts with a typed error
// instead of blocking indefinitely.
func TestWaitForShipArrivalCore_EventLostRepeatedly_ExhaustsToTypedError(t *testing.T) {
	ship := newArrivalWaitTestShip(t, domainNavigation.NavStatusInTransit)
	sub := &fakeArrivalSubscriber{ch: make(chan domainNavigation.ShipArrivedEvent, 1)} // never fed
	repo := &fakeShipQueryRepo{findBySymbolFunc: func() (*domainNavigation.Ship, error) {
		// Source of truth still shows IN_TRANSIT on every resync attempt.
		return newArrivalWaitTestShip(t, domainNavigation.NavStatusInTransit), nil
	}}

	err := waitForShipArrivalCore(context.Background(), repo, sub, ship, shared.MustNewPlayerID(1), 5, noopLogger{}, 10*time.Millisecond, 3)
	if err == nil {
		t.Fatalf("expected exhaustion error, got nil (wait must not silently succeed while still IN_TRANSIT)")
	}
	var exhausted *ErrArrivalWaitExhausted
	if !errors.As(err, &exhausted) {
		t.Fatalf("expected *ErrArrivalWaitExhausted, got %T: %v", err, err)
	}
	if repo.calls != 3 {
		t.Fatalf("expected exactly maxAttempts=3 resync attempts, got %d", repo.calls)
	}
	if ship.NavStatus() != domainNavigation.NavStatusInTransit {
		t.Fatalf("ship state must be untouched on exhaustion, got %s", ship.NavStatus())
	}
}

// TestWaitForShipArrivalCore_ContextCancelled_ReturnsCtxErrImmediately pins the
// existing cancellation behavior: an already-cancelled context returns
// immediately without waiting for the grace period or attempting a resync.
func TestWaitForShipArrivalCore_ContextCancelled_ReturnsCtxErrImmediately(t *testing.T) {
	ship := newArrivalWaitTestShip(t, domainNavigation.NavStatusInTransit)
	sub := &fakeArrivalSubscriber{ch: make(chan domainNavigation.ShipArrivedEvent, 1)}
	repo := &fakeShipQueryRepo{findBySymbolFunc: func() (*domainNavigation.Ship, error) {
		t.Fatalf("resync should not be triggered on context cancellation")
		return nil, nil
	}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := waitForShipArrivalCore(ctx, repo, sub, ship, shared.MustNewPlayerID(1), 5, noopLogger{}, 50*time.Millisecond, 3)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
}
