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
	// "X1-TEST-A" is the in-transit ship's DESTINATION: StartTransit sets
	// currentLocation to the destination while IN_TRANSIT, so a wait's ship is
	// always sitting at (heading to) this waypoint. A resync fixture placed at a
	// DIFFERENT waypoint via newArrivalWaitTestShipAt therefore models the stale
	// pre-departure snapshot still at the ORIGIN (sp-d6gl).
	return newArrivalWaitTestShipAt(t, navStatus, "X1-TEST-A")
}

// newArrivalWaitTestShipAt is newArrivalWaitTestShip with an explicit current
// location so a resync fixture can be placed at the ORIGIN (a waypoint distinct
// from the in-transit ship's "X1-TEST-A" destination) to model the stale
// pre-departure snapshot the sp-d6gl safety-resync must reject as arrival.
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
// future-vs-past-ETA distinction (sp-ht1f) on the ship a resync returns.
func newArrivalWaitTestShipWithArrival(t *testing.T, navStatus domainNavigation.NavStatus, arrival time.Time) *domainNavigation.Ship {
	t.Helper()
	ship := newArrivalWaitTestShip(t, navStatus)
	ship.SetArrivalTime(arrival)
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

	err := waitForShipArrivalCore(context.Background(), repo, sub, ship, shared.MustNewPlayerID(1), 5, noopLogger{}, 50*time.Millisecond, 200*time.Millisecond)
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

	err := waitForShipArrivalCore(context.Background(), repo, sub, ship, shared.MustNewPlayerID(1), 5, noopLogger{}, 20*time.Millisecond, 200*time.Millisecond)
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
// is sp-ht1f's headline regression case: a resync keeps showing IN_TRANSIT
// with an ArrivalTime still in the future (a legitimately long transit, e.g.
// the real-world navigate-TORWIND-F-a36d793d 23-minute DF9E->B10D leg that
// sp-pafv's fixed 3*30s=~90s budget aborted at ~2 minutes). The wait must
// keep polling until the ship actually leaves transit - it must NOT park just
// because attempts accumulated while the ship was still healthy. Under the
// ETA-aligned schedule (sp-7yej invariant 5) each still-future resync sleeps
// to the snapshot's own ArrivalTime plus one grace of slack, so the fixture
// keeps that ETA a short, freshly-computed step ahead on every call.
func TestWaitForShipArrivalCore_ResyncStillInTransitFutureETA_KeepsWaitingUntilArrival(t *testing.T) {
	ship := newArrivalWaitTestShip(t, domainNavigation.NavStatusInTransit)
	sub := &fakeArrivalSubscriber{ch: make(chan domainNavigation.ShipArrivedEvent, 1)} // never fed: event lost for the whole wait

	const callsUntilArrival = 6 // more than sp-pafv's old fixed DefaultArrivalMaxAttempts=3
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
	err := waitForShipArrivalCore(context.Background(), repo, sub, ship, shared.MustNewPlayerID(1), 1500, noopLogger{}, 5*time.Millisecond, 2*time.Second)
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

// TestWaitForShipArrivalCore_HealthyTransit_PollsAreETAAligned pins sp-7yej
// invariant 5's schedule contract: during a healthy transit the wait must NOT
// wake every grace period (the old behavior — ~46 resyncs and ~46 WARNING
// lines for a 23-minute leg); after the one fast first poll it sleeps to the
// ship's own expected arrival in a single tick. A ~200ms transit with a 5ms
// grace would have cost ~40 polls under the old fixed cadence; ETA-aligned it
// costs exactly 2 (the fast first check + the one aimed at the ETA).
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

	err := waitForShipArrivalCore(context.Background(), repo, sub, ship, shared.MustNewPlayerID(1), 0, noopLogger{}, 5*time.Millisecond, 5*time.Second)
	if err != nil {
		t.Fatalf("expected the resync to confirm arrival, got: %v", err)
	}
	if ship.NavStatus() != domainNavigation.NavStatusInOrbit {
		t.Fatalf("expected ship to have Arrive()'d, got status %s", ship.NavStatus())
	}
	// The schedule contract: one fast first poll + one ETA-aligned poll. A few
	// extra ticks of scheduler slop are tolerated; the old fixed cadence would
	// have burned ~40.
	if repo.calls > 4 {
		t.Fatalf("expected ETA-aligned polling (~2 resyncs for this transit), got %d — the wait is ticking at the grace period during a healthy transit again", repo.calls)
	}
}

// TestWaitForShipArrivalCore_ResyncStillInTransitPastETA_ParksWithinOneResync
// is the genuine lost-event case sp-pafv originally targeted, now
// distinguished explicitly (sp-ht1f) from a healthy future-ETA transit: a
// resync shows IN_TRANSIT but the ship's own ArrivalTime has already passed.
// Waiting out the rest of a large budget cannot help - the event is gone -
// so the wait must park immediately on this first resync rather than
// burning through the whole remaining budget first.
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
	err := waitForShipArrivalCore(context.Background(), repo, sub, ship, shared.MustNewPlayerID(1), 5, noopLogger{}, 10*time.Millisecond, 5*time.Second)
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
// proves the wait still does NOT hang forever (sp-pafv's original guarantee):
// if the event never arrives AND every resync shows IN_TRANSIT with no ETA
// that can be proven past (unknown/nil ArrivalTime on the resync snapshot),
// the only backstop left is the overall wait budget itself. This is the
// genuine-exhaustion path, now driven by a deadline instead of a fixed
// attempt count (sp-ht1f).
func TestWaitForShipArrivalCore_BudgetExhausted_NoResolvingSignal_ParksWithTypedError(t *testing.T) {
	ship := newArrivalWaitTestShip(t, domainNavigation.NavStatusInTransit)
	sub := &fakeArrivalSubscriber{ch: make(chan domainNavigation.ShipArrivedEvent, 1)} // never fed
	repo := &fakeShipQueryRepo{findBySymbolFunc: func() (*domainNavigation.Ship, error) {
		// Source of truth still shows IN_TRANSIT on every resync attempt,
		// with no ArrivalTime to prove the event was lost vs merely pending.
		return newArrivalWaitTestShip(t, domainNavigation.NavStatusInTransit), nil
	}}

	err := waitForShipArrivalCore(context.Background(), repo, sub, ship, shared.MustNewPlayerID(1), 5, noopLogger{}, 15*time.Millisecond, 40*time.Millisecond)
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

	err := waitForShipArrivalCore(ctx, repo, sub, ship, shared.MustNewPlayerID(1), 5, noopLogger{}, 50*time.Millisecond, time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
}

// --- sp-d6gl: stale pre-departure snapshot must not confirm arrival ----------

// TestWaitForShipArrivalCore_StalePreDepartureSnapshotBeforeETA_DoesNotConfirmUntilAtDestination
// is the sp-d6gl headline regression. A safety resync fired BEFORE the
// scheduled arrival reads a STALE PRE-DEPARTURE snapshot: the nav-cache has not
// yet caught up to this leg's departure, so FindBySymbol returns the hull NOT
// in transit and still sitting at the ORIGIN (a waypoint distinct from the
// destination it is really heading to) — the sp-n7yp/sp-ynuf nav-cache race, in
// the "hasn't caught up to the departure" direction.
//
// Before the fix, arrival_wait.go accepted any not-in-transit resync as arrival
// and returned success off that first stale poll, reporting the hull at its
// destination while it was still at the start of the leg (TORWIND-37: a 2m33s
// gate hop "confirmed" ~30s in, the tour completed with the hull at origin, the
// downstream jump hard-crashed). The wait must instead reject the stale
// snapshot and confirm ONLY once the resync shows the hull genuinely arrived AT
// THE DESTINATION.
//
// Harness-honesty (the sp-trnp miss this bead exists to correct): the fixture
// MODELS THE STALL — the first resync returns the pre-departure origin snapshot,
// a later one the true destination arrival — instead of arriving synchronously
// and hiding the race. On unfixed code this test fails (success after 1 resync,
// repo.calls==1); on fixed code the stale poll is ignored and confirmation lands
// on the destination poll (repo.calls==2).
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
	err := waitForShipArrivalCore(context.Background(), repo, sub, ship, shared.MustNewPlayerID(1), 1, noopLogger{}, 5*time.Millisecond, 200*time.Millisecond)
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
// is the sp-d6gl over-correction guard: a genuine EARLY arrival — the resync
// shows the hull not in transit and AT THE DESTINATION well before the
// scheduled ETA — must still confirm. The position, not the clock, is the proof
// the hull actually landed, so the fix must not reject real early arrivals just
// because the ETA has not elapsed.
func TestWaitForShipArrivalCore_GenuineEarlyArrivalBeforeETA_ConfirmsFromPosition(t *testing.T) {
	ship := newArrivalWaitTestShip(t, domainNavigation.NavStatusInTransit)             // heading to X1-TEST-A
	sub := &fakeArrivalSubscriber{ch: make(chan domainNavigation.ShipArrivedEvent, 1)} // event lost
	repo := &fakeShipQueryRepo{findBySymbolFunc: func() (*domainNavigation.Ship, error) {
		// Live data: the hull genuinely arrived early, sitting AT THE DESTINATION.
		return newArrivalWaitTestShip(t, domainNavigation.NavStatusInOrbit), nil // at X1-TEST-A
	}}

	// waitTimeSeconds=1 puts the resync firmly BEFORE the scheduled arrival.
	err := waitForShipArrivalCore(context.Background(), repo, sub, ship, shared.MustNewPlayerID(1), 1, noopLogger{}, 5*time.Millisecond, 2*time.Second)
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
// that the fix leaves the normal on-time path untouched: once the scheduled
// arrival is due (dueIn<=0), a not-in-transit resync confirms on the status
// change alone, exactly as before sp-d6gl — the position guard applies only
// BEFORE the ETA. Modeled with a resync reporting a location that is NOT the
// destination to prove the past-ETA branch is deliberately position-independent
// (it must not start rejecting an on-time arrival whose reported waypoint
// differs).
func TestWaitForShipArrivalCore_OnTimeArrivalPastETA_ConfirmsOnStatusUnchanged(t *testing.T) {
	ship := newArrivalWaitTestShip(t, domainNavigation.NavStatusInTransit)
	sub := &fakeArrivalSubscriber{ch: make(chan domainNavigation.ShipArrivedEvent, 1)} // event lost
	repo := &fakeShipQueryRepo{findBySymbolFunc: func() (*domainNavigation.Ship, error) {
		return newArrivalWaitTestShipAt(t, domainNavigation.NavStatusInOrbit, "X1-TEST-ELSEWHERE"), nil
	}}

	// waitTimeSeconds=0: the scheduled arrival is immediately due, so the first
	// resync fires AT/PAST the ETA — the normal on-time arrival window.
	err := waitForShipArrivalCore(context.Background(), repo, sub, ship, shared.MustNewPlayerID(1), 0, noopLogger{}, 5*time.Millisecond, 2*time.Second)
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

// --- firstArrivalPoll (first-poll grace shrink) ------------------------------

// TestFirstArrivalPoll pins the reconcile-latency cut: the FIRST safety poll is
// shrunk to ~1s (DefaultFirstArrivalPoll), NOT the full 30s
// DefaultArrivalGracePeriod, so a lost first ARRIVED event recovers in ~1s
// instead of stalling 30s (increasingly likely as travel shrinks under twin
// compression). The steady-state cadence keeps using the full gracePeriod, and a
// test injecting a sub-second grace still polls at that tiny grace so the
// existing fast-resync tests are unchanged.
func TestFirstArrivalPoll(t *testing.T) {
	tests := []struct {
		name        string
		gracePeriod time.Duration
		want        time.Duration
	}{
		{
			name:        "production 30s grace shrinks the first poll to 1s",
			gracePeriod: DefaultArrivalGracePeriod,
			want:        DefaultFirstArrivalPoll,
		},
		{
			name:        "exactly 1s grace stays 1s",
			gracePeriod: 1 * time.Second,
			want:        1 * time.Second,
		},
		{
			name:        "a tiny injected grace is left untouched (tests stay fast)",
			gracePeriod: 5 * time.Millisecond,
			want:        5 * time.Millisecond,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstArrivalPoll(tc.gracePeriod); got != tc.want {
				t.Fatalf("firstArrivalPoll(%v) = %v, want %v", tc.gracePeriod, got, tc.want)
			}
		})
	}
}

// --- calculateArrivalWaitBudget ----------------------------------------------

// TestCalculateArrivalWaitBudget_ScalesWithETA pins the sp-ht1f budget
// formula: budget = max(eta*marginFactor, eta+minMargin). This is what
// replaces sp-pafv's fixed grace*maxAttempts (~90s) budget that aborted
// every transit longer than ~90s regardless of its real ETA - the direct
// root cause of sp-ht1f (waitTimeSeconds was computed correctly by every
// call site but only ever used in a log line, never in the wait budget).
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
