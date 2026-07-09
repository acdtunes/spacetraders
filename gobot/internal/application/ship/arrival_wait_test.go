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
