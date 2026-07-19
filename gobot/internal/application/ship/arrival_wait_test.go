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
// the wait must tolerate).
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
//
// getShipDataFunc scripts the AUTHORITATIVE live-API re-confirm (Fix A). It is left
// nil by tests that must never reach it (the happy path and the flag-OFF park path):
// in that case GetShipData fails loudly, so an unexpected API call surfaces as a
// test failure rather than passing silently. getShipDataCalls records the live-API
// call count so a test can assert the zero-extra-API-calls contract.
type fakeShipQueryRepo struct {
	findBySymbolFunc func() (*domainNavigation.Ship, error)
	calls            int
	getShipDataFunc  func() (*domainNavigation.ShipData, error)
	getShipDataCalls int
}

func (f *fakeShipQueryRepo) FindBySymbol(_ context.Context, _ string, _ shared.PlayerID) (*domainNavigation.Ship, error) {
	f.calls++
	return f.findBySymbolFunc()
}
func (f *fakeShipQueryRepo) GetShipData(_ context.Context, _ string, _ shared.PlayerID) (*domainNavigation.ShipData, error) {
	f.getShipDataCalls++
	if f.getShipDataFunc == nil {
		return nil, fmt.Errorf("fakeShipQueryRepo: GetShipData called unexpectedly (no live-API script set)")
	}
	return f.getShipDataFunc()
}
func (f *fakeShipQueryRepo) FindAllByPlayer(_ context.Context, _ shared.PlayerID) ([]*domainNavigation.Ship, error) {
	return nil, fmt.Errorf("fakeShipQueryRepo: FindAllByPlayer not implemented")
}

// noopLogger discards every log call.
type noopLogger struct{}

func (noopLogger) Log(string, string, map[string]interface{}) {}

func newArrivalWaitTestShip(t *testing.T, navStatus domainNavigation.NavStatus) *domainNavigation.Ship {
	t.Helper()
	// "X1-TEST-A" is the in-transit ship's DESTINATION: StartTransit sets
	// currentLocation to the destination while IN_TRANSIT, so a wait's ship is
	// always sitting at (heading to) this waypoint. A resync fixture placed at a
	// DIFFERENT waypoint via newArrivalWaitTestShipAt therefore models the stale
	// pre-departure snapshot still at the ORIGIN.
	return newArrivalWaitTestShipAt(t, navStatus, "X1-TEST-A")
}

// newArrivalWaitTestShipAt is newArrivalWaitTestShip with an explicit current
// location so a resync fixture can be placed at the ORIGIN (a waypoint distinct
// from the in-transit ship's "X1-TEST-A" destination) to model the stale
// pre-departure snapshot the safety-resync must reject as arrival.
func newArrivalWaitTestShipAt(t *testing.T, navStatus domainNavigation.NavStatus, locationSymbol string) *domainNavigation.Ship {
	t.Helper()
	loc := mustWaypoint(t, locationSymbol, 0, 0)
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

// newArrivalWaitTestShipWithArrival is newArrivalWaitTestShip plus the
// resync snapshot's own ArrivalTime set, so tests can drive the
// future-vs-past-ETA distinction on the ship a resync returns.
func newArrivalWaitTestShipWithArrival(t *testing.T, navStatus domainNavigation.NavStatus, arrival time.Time) *domainNavigation.Ship {
	t.Helper()
	ship := newArrivalWaitTestShip(t, navStatus)
	ship.SetArrivalTime(arrival)
	return ship
}

// --- Tests -------------------------------------------------------------------

// TestWaitForShipArrivalCore_EventArrives_HappyPathUnchanged pins the existing
// behavior: when the ARRIVED event shows up, the wait returns success
// immediately and never touches the resync path.
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

	err := waitForShipArrivalCore(context.Background(), repo, sub, ship, shared.MustNewPlayerID(1), 5, noopLogger{}, 50*time.Millisecond, 200*time.Millisecond, false)
	if err != nil {
		t.Fatalf("expected success on event arrival, got: %v", err)
	}
	if ship.NavStatus() != domainNavigation.NavStatusInOrbit {
		t.Fatalf("expected ship to have Arrive()'d, got status %s", ship.NavStatus())
	}
}

// TestWaitForShipArrivalCore_EventLost_ResyncConfirmsArrival is the core
// lost-event case: the ARRIVED event never shows up (lost/raced against
// subscription, ShipEventBus's non-blocking, non-replaying send), but the
// repository - already updated by ShipStateScheduler before it published -
// confirms the ship has left transit. The wait must resync and succeed
// instead of hanging.
func TestWaitForShipArrivalCore_EventLost_ResyncConfirmsArrival(t *testing.T) {
	ship := newArrivalWaitTestShip(t, domainNavigation.NavStatusInTransit)
	sub := &fakeArrivalSubscriber{ch: make(chan domainNavigation.ShipArrivedEvent, 1)} // never fed: simulates a lost event
	repo := &fakeShipQueryRepo{findBySymbolFunc: func() (*domainNavigation.Ship, error) {
		return newArrivalWaitTestShip(t, domainNavigation.NavStatusInOrbit), nil
	}}

	err := waitForShipArrivalCore(context.Background(), repo, sub, ship, shared.MustNewPlayerID(1), 5, noopLogger{}, 20*time.Millisecond, 200*time.Millisecond, false)
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

// TestWaitForShipArrivalCore_ResyncStillInTransitFutureETA_KeepsWaitingUntilArrival
// pins the headline case: a resync keeps showing IN_TRANSIT with an
// ArrivalTime still in the future (a legitimately long transit). The wait
// must keep polling until the ship actually leaves transit - it must NOT
// park just because attempts accumulated while the ship was still healthy.
// Under the ETA-aligned schedule each still-future resync sleeps to the
// snapshot's own ArrivalTime plus one grace of slack, so the fixture keeps
// that ETA a short, freshly-computed step ahead on every call.
func TestWaitForShipArrivalCore_ResyncStillInTransitFutureETA_KeepsWaitingUntilArrival(t *testing.T) {
	ship := newArrivalWaitTestShip(t, domainNavigation.NavStatusInTransit)
	sub := &fakeArrivalSubscriber{ch: make(chan domainNavigation.ShipArrivedEvent, 1)} // never fed: event lost for the whole wait

	const callsUntilArrival = 6
	repo := &fakeShipQueryRepo{}
	repo.findBySymbolFunc = func() (*domainNavigation.Ship, error) {
		if repo.calls < callsUntilArrival {
			// Still genuinely in flight - the ETA is ahead of "now" on every
			// resync (a transit whose arrival keeps being a beat away), so
			// this must NOT be treated as a lost/past-ETA event, and each
			// poll re-aims at this fresh ETA.
			return newArrivalWaitTestShipWithArrival(t, domainNavigation.NavStatusInTransit, time.Now().Add(25*time.Millisecond)), nil
		}
		return newArrivalWaitTestShip(t, domainNavigation.NavStatusInOrbit), nil
	}

	// gracePeriod is tiny so the ETA-aligned resyncs happen fast in test time;
	// budget is generous (2s) relative to callsUntilArrival*(25+5)ms so the
	// wait is never at risk of exhausting on wall-clock alone - this test is
	// about NOT parking early on a future ETA, not about budget sizing (that's
	// calculateArrivalWaitBudget's own test below).
	err := waitForShipArrivalCore(context.Background(), repo, sub, ship, shared.MustNewPlayerID(1), 1500, noopLogger{}, 5*time.Millisecond, 2*time.Second, false)
	if err != nil {
		t.Fatalf("expected the wait to survive past the old fixed-attempt budget and eventually succeed, got: %v", err)
	}
	if ship.NavStatus() != domainNavigation.NavStatusInOrbit {
		t.Fatalf("expected ship to have Arrive()'d, got status %s", ship.NavStatus())
	}
	if repo.calls != callsUntilArrival {
		t.Fatalf("expected exactly %d resync attempts before success, got %d", callsUntilArrival, repo.calls)
	}
}

// TestWaitForShipArrivalCore_HealthyTransit_PollsAreETAAligned pins the
// schedule contract: during a healthy transit the wait must NOT wake every
// grace period; after the one fast first poll it sleeps to the ship's own
// expected arrival in a single tick, costing exactly 2 resyncs regardless of
// transit length (the fast first check + the one aimed at the ETA).
func TestWaitForShipArrivalCore_HealthyTransit_PollsAreETAAligned(t *testing.T) {
	ship := newArrivalWaitTestShip(t, domainNavigation.NavStatusInTransit)
	sub := &fakeArrivalSubscriber{ch: make(chan domainNavigation.ShipArrivedEvent, 1)} // event lost; resync must still be cheap

	arrival := time.Now().Add(200 * time.Millisecond)
	repo := &fakeShipQueryRepo{}
	repo.findBySymbolFunc = func() (*domainNavigation.Ship, error) {
		if time.Now().Before(arrival) {
			return newArrivalWaitTestShipWithArrival(t, domainNavigation.NavStatusInTransit, arrival), nil
		}
		return newArrivalWaitTestShip(t, domainNavigation.NavStatusInOrbit), nil
	}

	err := waitForShipArrivalCore(context.Background(), repo, sub, ship, shared.MustNewPlayerID(1), 0, noopLogger{}, 5*time.Millisecond, 5*time.Second, false)
	if err != nil {
		t.Fatalf("expected the resync to confirm arrival, got: %v", err)
	}
	if ship.NavStatus() != domainNavigation.NavStatusInOrbit {
		t.Fatalf("expected ship to have Arrive()'d, got status %s", ship.NavStatus())
	}
	// The schedule contract: one fast first poll + one ETA-aligned poll. A few
	// extra ticks of scheduler slop are tolerated.
	if repo.calls > 4 {
		t.Fatalf("expected ETA-aligned polling (~2 resyncs for this transit), got %d — the wait is ticking at the grace period during a healthy transit again", repo.calls)
	}
}

// TestWaitForShipArrivalCore_ResyncStillInTransitPastETA_ParksWithinOneResync
// is the genuine lost-event case, distinguished from a healthy future-ETA
// transit: a resync shows IN_TRANSIT but the ship's own ArrivalTime has
// already passed. Waiting out the rest of a large budget cannot help - the
// event is gone - so the wait must park immediately on this first resync
// rather than burning through the whole remaining budget first.
func TestWaitForShipArrivalCore_ResyncStillInTransitPastETA_ParksWithinOneResync(t *testing.T) {
	ship := newArrivalWaitTestShip(t, domainNavigation.NavStatusInTransit)
	sub := &fakeArrivalSubscriber{ch: make(chan domainNavigation.ShipArrivedEvent, 1)} // never fed
	repo := &fakeShipQueryRepo{findBySymbolFunc: func() (*domainNavigation.Ship, error) {
		// Source of truth still shows IN_TRANSIT, but its own ETA is already
		// a minute in the past - the event was genuinely lost/dropped.
		return newArrivalWaitTestShipWithArrival(t, domainNavigation.NavStatusInTransit, time.Now().Add(-time.Minute)), nil
	}}

	// budget is deliberately large (5s) to prove parking happens because the
	// ETA is past, not because the budget ran out.
	err := waitForShipArrivalCore(context.Background(), repo, sub, ship, shared.MustNewPlayerID(1), 5, noopLogger{}, 10*time.Millisecond, 5*time.Second, false)
	if err == nil {
		t.Fatalf("expected exhaustion error, got nil (a past-ETA IN_TRANSIT resync must not succeed silently)")
	}
	var exhausted *ErrArrivalWaitExhausted
	if !errors.As(err, &exhausted) {
		t.Fatalf("expected *ErrArrivalWaitExhausted, got %T: %v", err, err)
	}
	if repo.calls != 1 {
		t.Fatalf("expected parking within exactly one resync once ETA is confirmed past, got %d calls", repo.calls)
	}
	if exhausted.Attempts != 1 {
		t.Fatalf("expected Attempts=1, got %d", exhausted.Attempts)
	}
	if ship.NavStatus() != domainNavigation.NavStatusInTransit {
		t.Fatalf("ship state must be untouched on exhaustion, got %s", ship.NavStatus())
	}
}

// TestWaitForShipArrivalCore_BudgetExhausted_NoResolvingSignal_ParksWithTypedError
// proves the wait does NOT hang forever: if the event never arrives AND
// every resync shows IN_TRANSIT with no ETA that can be proven past
// (unknown/nil ArrivalTime on the resync snapshot), the only backstop left
// is the overall wait budget itself — the genuine-exhaustion path, driven by
// a deadline rather than a fixed attempt count.
func TestWaitForShipArrivalCore_BudgetExhausted_NoResolvingSignal_ParksWithTypedError(t *testing.T) {
	ship := newArrivalWaitTestShip(t, domainNavigation.NavStatusInTransit)
	sub := &fakeArrivalSubscriber{ch: make(chan domainNavigation.ShipArrivedEvent, 1)} // never fed
	repo := &fakeShipQueryRepo{findBySymbolFunc: func() (*domainNavigation.Ship, error) {
		// Source of truth still shows IN_TRANSIT on every resync attempt,
		// with no ArrivalTime to prove the event was lost vs merely pending.
		return newArrivalWaitTestShip(t, domainNavigation.NavStatusInTransit), nil
	}}

	err := waitForShipArrivalCore(context.Background(), repo, sub, ship, shared.MustNewPlayerID(1), 5, noopLogger{}, 15*time.Millisecond, 40*time.Millisecond, false)
	if err == nil {
		t.Fatalf("expected exhaustion error, got nil (wait must not silently succeed while still IN_TRANSIT)")
	}
	var exhausted *ErrArrivalWaitExhausted
	if !errors.As(err, &exhausted) {
		t.Fatalf("expected *ErrArrivalWaitExhausted, got %T: %v", err, err)
	}
	if repo.calls < 2 {
		t.Fatalf("expected more than one resync attempt before exhausting the budget (not an immediate past-ETA park), got %d", repo.calls)
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

	err := waitForShipArrivalCore(ctx, repo, sub, ship, shared.MustNewPlayerID(1), 5, noopLogger{}, 50*time.Millisecond, time.Second, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
}

// --- Stale pre-departure snapshot must not confirm arrival ------------------

// TestWaitForShipArrivalCore_StalePreDepartureSnapshotBeforeETA_DoesNotConfirmUntilAtDestination
// covers the stale pre-departure snapshot case. A safety resync fired BEFORE
// the scheduled arrival can read a STALE PRE-DEPARTURE snapshot: the nav-cache
// has not yet caught up to this leg's departure, so FindBySymbol returns the
// hull NOT in transit and still sitting at the ORIGIN (a waypoint distinct
// from the destination it is really heading to). Confirming arrival off that
// snapshot would report the hull at its destination while it is still at the
// start of the leg, so the wait must reject the stale snapshot and confirm
// ONLY once the resync shows the hull genuinely arrived AT THE DESTINATION.
//
// The fixture MODELS THE STALL — the first resync returns the pre-departure
// origin snapshot, a later one the true destination arrival — instead of
// arriving synchronously and hiding the race: confirmation must land on the
// destination poll (repo.calls==2), not the first stale one.
func TestWaitForShipArrivalCore_StalePreDepartureSnapshotBeforeETA_DoesNotConfirmUntilAtDestination(t *testing.T) {
	ship := newArrivalWaitTestShip(t, domainNavigation.NavStatusInTransit)             // heading to X1-TEST-A
	sub := &fakeArrivalSubscriber{ch: make(chan domainNavigation.ShipArrivedEvent, 1)} // event lost / not yet published

	repo := &fakeShipQueryRepo{}
	repo.findBySymbolFunc = func() (*domainNavigation.Ship, error) {
		if repo.calls == 1 {
			// STALE pre-departure snapshot: not in transit, still at the ORIGIN.
			return newArrivalWaitTestShipAt(t, domainNavigation.NavStatusInOrbit, "X1-TEST-ORIGIN"), nil
		}
		// Cache caught up: the hull has genuinely arrived AT THE DESTINATION.
		return newArrivalWaitTestShip(t, domainNavigation.NavStatusInOrbit), nil // at X1-TEST-A
	}

	// waitTimeSeconds=1 keeps both resyncs firmly BEFORE the scheduled arrival
	// (dueIn>0) — the exact window the false positive lived in. The small budget
	// bounds the ETA-aligned reschedule so the destination poll lands fast.
	err := waitForShipArrivalCore(context.Background(), repo, sub, ship, shared.MustNewPlayerID(1), 1, noopLogger{}, 5*time.Millisecond, 200*time.Millisecond, false)
	if err != nil {
		t.Fatalf("expected the wait to ignore the stale pre-departure snapshot and confirm on the real destination arrival, got: %v", err)
	}
	if ship.NavStatus() != domainNavigation.NavStatusInOrbit {
		t.Fatalf("expected ship to have Arrive()'d after the true destination arrival, got status %s", ship.NavStatus())
	}
	if repo.calls != 2 {
		t.Fatalf("expected the wait to reject the 1 stale pre-departure (origin) snapshot and confirm only on the destination arrival (2 resyncs), got %d — a not-in-transit snapshot at the ORIGIN was accepted as arrival before the ETA (sp-d6gl false positive)", repo.calls)
	}
}

// TestWaitForShipArrivalCore_GenuineEarlyArrivalBeforeETA_ConfirmsFromPosition
// is the over-correction guard: a genuine EARLY arrival — the resync shows
// the hull not in transit and AT THE DESTINATION well before the scheduled
// ETA — must still confirm. The position, not the clock, is the proof the
// hull actually landed, so the guard must not reject real early arrivals
// just because the ETA has not elapsed.
func TestWaitForShipArrivalCore_GenuineEarlyArrivalBeforeETA_ConfirmsFromPosition(t *testing.T) {
	ship := newArrivalWaitTestShip(t, domainNavigation.NavStatusInTransit)             // heading to X1-TEST-A
	sub := &fakeArrivalSubscriber{ch: make(chan domainNavigation.ShipArrivedEvent, 1)} // event lost
	repo := &fakeShipQueryRepo{findBySymbolFunc: func() (*domainNavigation.Ship, error) {
		// Live data: the hull genuinely arrived early, sitting AT THE DESTINATION.
		return newArrivalWaitTestShip(t, domainNavigation.NavStatusInOrbit), nil // at X1-TEST-A
	}}

	// waitTimeSeconds=1 puts the resync firmly BEFORE the scheduled arrival.
	err := waitForShipArrivalCore(context.Background(), repo, sub, ship, shared.MustNewPlayerID(1), 1, noopLogger{}, 5*time.Millisecond, 2*time.Second, false)
	if err != nil {
		t.Fatalf("expected a genuine early destination arrival to confirm, got: %v", err)
	}
	if ship.NavStatus() != domainNavigation.NavStatusInOrbit {
		t.Fatalf("expected ship to have Arrive()'d on the early arrival, got status %s", ship.NavStatus())
	}
	if repo.calls != 1 {
		t.Fatalf("expected the early destination arrival to confirm on the first resync, got %d resyncs", repo.calls)
	}
}

// TestWaitForShipArrivalCore_OnTimeArrivalPastETA_ConfirmsOnStatusUnchanged pins
// that the normal on-time path stays untouched: once the scheduled arrival is
// due (dueIn<=0), a not-in-transit resync confirms on the status change alone —
// the position guard applies only BEFORE the ETA. Modeled with a resync
// reporting a location that is NOT the destination to prove the past-ETA
// branch is deliberately position-independent (it must not start rejecting an
// on-time arrival whose reported waypoint differs).
func TestWaitForShipArrivalCore_OnTimeArrivalPastETA_ConfirmsOnStatusUnchanged(t *testing.T) {
	ship := newArrivalWaitTestShip(t, domainNavigation.NavStatusInTransit)
	sub := &fakeArrivalSubscriber{ch: make(chan domainNavigation.ShipArrivedEvent, 1)} // event lost
	repo := &fakeShipQueryRepo{findBySymbolFunc: func() (*domainNavigation.Ship, error) {
		return newArrivalWaitTestShipAt(t, domainNavigation.NavStatusInOrbit, "X1-TEST-ELSEWHERE"), nil
	}}

	// waitTimeSeconds=0: the scheduled arrival is immediately due, so the first
	// resync fires AT/PAST the ETA — the normal on-time arrival window.
	err := waitForShipArrivalCore(context.Background(), repo, sub, ship, shared.MustNewPlayerID(1), 0, noopLogger{}, 5*time.Millisecond, 2*time.Second, false)
	if err != nil {
		t.Fatalf("expected an on-time (past-ETA) arrival to confirm on status alone, got: %v", err)
	}
	if ship.NavStatus() != domainNavigation.NavStatusInOrbit {
		t.Fatalf("expected ship to have Arrive()'d on the on-time arrival, got status %s", ship.NavStatus())
	}
	if repo.calls != 1 {
		t.Fatalf("expected the past-ETA arrival to confirm on the first resync, got %d resyncs", repo.calls)
	}
}

// --- Live-API re-confirm before parking (Fix A) + short-leg debounce -------
// --- (Fix B) + kill-switch --------------------------------------------------

// TestWaitForShipArrivalCore_StaleDBPastETA_LiveAPIConfirmsArrived_ReturnsSuccess
// is the headline live-API re-confirm case: the LOCAL DB row is stale (still
// IN_TRANSIT with its own ETA already past) because the async
// IN_TRANSIT->IN_ORBIT transition has not committed yet, but the AUTHORITATIVE
// live API says the hull has arrived. The wait must re-confirm against the API
// and RETURN SUCCESS instead of the false ErrArrivalWaitExhausted that
// crash-loops the container. This is the MUTATION sentinel for Fix A:
// reverting the live-API re-confirm makes this test see the crash-error again.
func TestWaitForShipArrivalCore_StaleDBPastETA_LiveAPIConfirmsArrived_ReturnsSuccess(t *testing.T) {
	ship := newArrivalWaitTestShip(t, domainNavigation.NavStatusInTransit)
	sub := &fakeArrivalSubscriber{ch: make(chan domainNavigation.ShipArrivedEvent, 1)} // event lost
	repo := &fakeShipQueryRepo{
		// The local row is stale on EVERY poll: still IN_TRANSIT, ETA a minute past.
		findBySymbolFunc: func() (*domainNavigation.Ship, error) {
			return newArrivalWaitTestShipWithArrival(t, domainNavigation.NavStatusInTransit, time.Now().Add(-time.Minute)), nil
		},
		// The live API is authoritative: the hull actually arrived (IN_ORBIT).
		getShipDataFunc: func() (*domainNavigation.ShipData, error) {
			return &domainNavigation.ShipData{NavStatus: string(domainNavigation.NavStatusInOrbit)}, nil
		},
	}

	err := waitForShipArrivalCore(context.Background(), repo, sub, ship, shared.MustNewPlayerID(1), 0, noopLogger{}, 5*time.Millisecond, 2*time.Second, true)
	if err != nil {
		t.Fatalf("expected the live-API re-confirm to recognise the stale-row arrival and succeed, got: %v", err)
	}
	if ship.NavStatus() != domainNavigation.NavStatusInOrbit {
		t.Fatalf("expected ship to have Arrive()'d after the live-API re-confirm, got status %s", ship.NavStatus())
	}
	// Fix B: two DB observations before the park path is entered; Fix A: exactly ONE
	// live-API call there — the whole-wait API budget.
	if repo.calls != requiredPastETAObservationsBeforePark {
		t.Fatalf("expected %d local-DB observations before the live re-confirm, got %d", requiredPastETAObservationsBeforePark, repo.calls)
	}
	if repo.getShipDataCalls != 1 {
		t.Fatalf("expected EXACTLY one live-API re-confirm call on the park path, got %d (must never be per-poll)", repo.getShipDataCalls)
	}
}

// TestWaitForShipArrivalCore_StaleDBPastETA_LiveAPIError_FallsBackToPark is Fix A's
// fail-safe: if the live-API re-confirm itself errors, the wait falls back to
// the DB-only park (never worse than status quo) rather than panicking or
// succeeding blind.
func TestWaitForShipArrivalCore_StaleDBPastETA_LiveAPIError_FallsBackToPark(t *testing.T) {
	ship := newArrivalWaitTestShip(t, domainNavigation.NavStatusInTransit)
	sub := &fakeArrivalSubscriber{ch: make(chan domainNavigation.ShipArrivedEvent, 1)} // event lost
	repo := &fakeShipQueryRepo{
		findBySymbolFunc: func() (*domainNavigation.Ship, error) {
			return newArrivalWaitTestShipWithArrival(t, domainNavigation.NavStatusInTransit, time.Now().Add(-time.Minute)), nil
		},
		getShipDataFunc: func() (*domainNavigation.ShipData, error) {
			return nil, fmt.Errorf("API unreachable")
		},
	}

	err := waitForShipArrivalCore(context.Background(), repo, sub, ship, shared.MustNewPlayerID(1), 0, noopLogger{}, 5*time.Millisecond, 2*time.Second, true)
	var exhausted *ErrArrivalWaitExhausted
	if !errors.As(err, &exhausted) {
		t.Fatalf("expected fallback to DB-only park (*ErrArrivalWaitExhausted) on live-API error, got %T: %v", err, err)
	}
	if ship.NavStatus() != domainNavigation.NavStatusInTransit {
		t.Fatalf("ship state must be untouched on the fail-safe park, got %s", ship.NavStatus())
	}
	if repo.getShipDataCalls != 1 {
		t.Fatalf("expected exactly one live-API attempt before the fallback park, got %d", repo.getShipDataCalls)
	}
}

// TestWaitForShipArrivalCore_StaleDBPastETA_LiveAlsoInTransit_StillParks is Fix A's
// true-stuck guard: when BOTH the local row AND the authoritative live API agree the
// hull is genuinely still IN_TRANSIT past its ETA, the wait must STILL park — the
// re-confirm must not mask a real lost-event / stuck hull.
func TestWaitForShipArrivalCore_StaleDBPastETA_LiveAlsoInTransit_StillParks(t *testing.T) {
	ship := newArrivalWaitTestShip(t, domainNavigation.NavStatusInTransit)
	sub := &fakeArrivalSubscriber{ch: make(chan domainNavigation.ShipArrivedEvent, 1)} // event lost
	repo := &fakeShipQueryRepo{
		findBySymbolFunc: func() (*domainNavigation.Ship, error) {
			return newArrivalWaitTestShipWithArrival(t, domainNavigation.NavStatusInTransit, time.Now().Add(-time.Minute)), nil
		},
		// Live API AGREES: genuinely still in transit.
		getShipDataFunc: func() (*domainNavigation.ShipData, error) {
			return &domainNavigation.ShipData{NavStatus: string(domainNavigation.NavStatusInTransit)}, nil
		},
	}

	err := waitForShipArrivalCore(context.Background(), repo, sub, ship, shared.MustNewPlayerID(1), 0, noopLogger{}, 5*time.Millisecond, 2*time.Second, true)
	var exhausted *ErrArrivalWaitExhausted
	if !errors.As(err, &exhausted) {
		t.Fatalf("expected a genuinely-stuck hull to still park (*ErrArrivalWaitExhausted), got %T: %v", err, err)
	}
	if repo.getShipDataCalls != 1 {
		t.Fatalf("expected exactly one live-API re-confirm before parking a genuinely-stuck hull, got %d", repo.getShipDataCalls)
	}
}

// TestWaitForShipArrivalCore_ShortLeg_DoesNotParkOnFirstPastETAObservation is Fix
// B: a short-ETA leg whose first poll lands past the ETA before the async
// IN_TRANSIT->IN_ORBIT transition commits must NOT park on that first observation.
// The debounce grants one more LOCAL DB re-read, by which the transition has landed
// — so the wait confirms arrival with ZERO live-API calls (the DB resolved it).
func TestWaitForShipArrivalCore_ShortLeg_DoesNotParkOnFirstPastETAObservation(t *testing.T) {
	ship := newArrivalWaitTestShip(t, domainNavigation.NavStatusInTransit)
	sub := &fakeArrivalSubscriber{ch: make(chan domainNavigation.ShipArrivedEvent, 1)} // event lost
	repo := &fakeShipQueryRepo{}
	repo.findBySymbolFunc = func() (*domainNavigation.Ship, error) {
		if repo.calls == 1 {
			// First poll: stale row, still IN_TRANSIT past its ETA (the race window).
			return newArrivalWaitTestShipWithArrival(t, domainNavigation.NavStatusInTransit, time.Now().Add(-time.Minute)), nil
		}
		// Second poll: the async transition landed — the hull is now IN_ORBIT at
		// its destination.
		return newArrivalWaitTestShip(t, domainNavigation.NavStatusInOrbit), nil
	}

	err := waitForShipArrivalCore(context.Background(), repo, sub, ship, shared.MustNewPlayerID(1), 0, noopLogger{}, 5*time.Millisecond, 2*time.Second, true)
	if err != nil {
		t.Fatalf("expected the short-leg debounce to catch the second (arrived) poll and succeed, got: %v", err)
	}
	if ship.NavStatus() != domainNavigation.NavStatusInOrbit {
		t.Fatalf("expected ship to have Arrive()'d on the second poll, got status %s", ship.NavStatus())
	}
	if repo.calls != 2 {
		t.Fatalf("expected the debounce to re-read the local row (2 polls) rather than park on the first, got %d", repo.calls)
	}
	if repo.getShipDataCalls != 0 {
		t.Fatalf("expected ZERO live-API calls when the DB debounce resolves the arrival, got %d", repo.getShipDataCalls)
	}
}

// TestWaitForShipArrivalCore_LiveReconfirmDisabled_ParksDBOnly is the kill-switch
// OFF proof: with liveReconfirm=false the wait parks on the FIRST past-ETA
// observation off the DB read alone, in exactly one resync, and NEVER touches
// the live API — even though the (scripted) API would have reported the hull
// arrived. Flipping the flag off instantly reverts to the DB-only park with
// no code rollback.
func TestWaitForShipArrivalCore_LiveReconfirmDisabled_ParksDBOnly(t *testing.T) {
	ship := newArrivalWaitTestShip(t, domainNavigation.NavStatusInTransit)
	sub := &fakeArrivalSubscriber{ch: make(chan domainNavigation.ShipArrivedEvent, 1)} // event lost
	repo := &fakeShipQueryRepo{
		findBySymbolFunc: func() (*domainNavigation.Ship, error) {
			return newArrivalWaitTestShipWithArrival(t, domainNavigation.NavStatusInTransit, time.Now().Add(-time.Minute)), nil
		},
		// Scripted to "arrived" to PROVE the flag-off path never consults it.
		getShipDataFunc: func() (*domainNavigation.ShipData, error) {
			return &domainNavigation.ShipData{NavStatus: string(domainNavigation.NavStatusInOrbit)}, nil
		},
	}

	err := waitForShipArrivalCore(context.Background(), repo, sub, ship, shared.MustNewPlayerID(1), 5, noopLogger{}, 10*time.Millisecond, 5*time.Second, false)
	var exhausted *ErrArrivalWaitExhausted
	if !errors.As(err, &exhausted) {
		t.Fatalf("expected DB-only park (*ErrArrivalWaitExhausted) with the flag OFF, got %T: %v", err, err)
	}
	if repo.calls != 1 || exhausted.Attempts != 1 {
		t.Fatalf("expected the pre-fix single-resync park (calls=1, Attempts=1), got calls=%d Attempts=%d", repo.calls, exhausted.Attempts)
	}
	if repo.getShipDataCalls != 0 {
		t.Fatalf("expected ZERO live-API calls with the kill-switch OFF, got %d", repo.getShipDataCalls)
	}
}

// TestSetArrivalWaitLiveReconfirm_TogglesPackageDefault pins the kill-switch wiring:
// the package default is ON, and SetArrivalWaitLiveReconfirm (the boot-time config
// hook) flips it both ways. Restores the default so the global does not leak across
// tests.
func TestSetArrivalWaitLiveReconfirm_TogglesPackageDefault(t *testing.T) {
	t.Cleanup(func() { SetArrivalWaitLiveReconfirm(true) })

	if !arrivalWaitLiveReconfirm.Load() {
		t.Fatalf("expected the arrival-wait live-reconfirm kill-switch to DEFAULT ON")
	}
	SetArrivalWaitLiveReconfirm(false)
	if arrivalWaitLiveReconfirm.Load() {
		t.Fatalf("expected SetArrivalWaitLiveReconfirm(false) to disable the fix")
	}
	SetArrivalWaitLiveReconfirm(true)
	if !arrivalWaitLiveReconfirm.Load() {
		t.Fatalf("expected SetArrivalWaitLiveReconfirm(true) to re-enable the fix")
	}
}

// --- calculateArrivalWaitBudget ----------------------------------------------

// TestCalculateArrivalWaitBudget_ScalesWithETA pins the budget formula:
// budget = max(eta*marginFactor, eta+minMargin) — so the wait budget scales
// with the route's real ETA rather than a fixed attempt count.
func TestCalculateArrivalWaitBudget_ScalesWithETA(t *testing.T) {
	const marginFactor = 1.25
	const minMargin = 2 * time.Minute

	tests := []struct {
		name string
		eta  time.Duration
		want time.Duration
	}{
		{
			name: "zero ETA floors to minMargin",
			eta:  0,
			want: 2 * time.Minute,
		},
		{
			name: "short ETA floors to eta+minMargin (flat margin dominates)",
			eta:  time.Minute,
			want: 3 * time.Minute, // eta*1.25=75s vs eta+2min=180s -> 180s wins
		},
		{
			name: "long ETA scales proportionally (percentage margin dominates)",
			eta:  25 * time.Minute,
			want: 31*time.Minute + 15*time.Second, // 25min*1.25=31.25min vs 25min+2min=27min -> 31.25min wins
		},
		{
			name: "the real-world regression case: ~23 minute transit",
			eta:  23 * time.Minute,
			want: 28*time.Minute + 45*time.Second, // 23min*1.25=28.75min vs 23min+2min=25min -> 28.75min wins
		},
		{
			name: "negative ETA (already overdue per API clock skew) clamps to zero before flooring",
			eta:  -30 * time.Second,
			want: 2 * time.Minute,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := calculateArrivalWaitBudget(tc.eta, marginFactor, minMargin)
			if got != tc.want {
				t.Fatalf("calculateArrivalWaitBudget(%v, %v, %v) = %v, want %v", tc.eta, marginFactor, minMargin, got, tc.want)
			}
		})
	}
}
