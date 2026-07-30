package commands

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// run_tour_coordinator_relocation_offer_hold_test.go — the WAITING LOOP itself (sp-e8d92).
//
// This loop deliberately stops a hull earning, so "bounded by construction" is not good enough: the
// failure it can produce is a permanently stalled trade hull, which is strictly worse than the
// non-spreading it exists to fix. Every property below is therefore proved against the REAL loop —
// driven at millisecond speed through the poll seam rather than replaced by a reimplementation that
// could pass while the real one is broken.

// offerHoldFakePersister records what the hold wrote, so a test asserts the durable outcome.
type offerHoldFakePersister struct {
	mu      sync.Mutex
	writes  []RelocationOffer
	failing bool
}

func (f *offerHoldFakePersister) PersistRelocationOffer(_ context.Context, _ string, _ int, offer RelocationOffer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, offer)
	if f.failing {
		return context.DeadlineExceeded
	}
	return nil
}

func (f *offerHoldFakePersister) recorded() []RelocationOffer {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]RelocationOffer(nil), f.writes...)
}

// offerHoldHarness wires the real handler to the shared tour fixture with a millisecond poll.
type offerHoldHarness struct {
	fx        *tourFixture
	handler   *RunTourCoordinatorHandler
	persister *offerHoldFakePersister
	cmd       *RunTourCoordinatorCommand
}

func newOfferHoldHarness(t *testing.T) *offerHoldHarness {
	t.Helper()
	fx := &tourFixture{cargo: map[string]int{}, location: "X1-HOME-A", cargoCap: 100}
	h := newTourHandler(t, fx, &tourFakeRoutingClient{}, &tourFakeTelemetry{})
	persister := &offerHoldFakePersister{}
	h.SetRelocationOfferPersister(persister)
	h.offerPollInterval = time.Millisecond
	return &offerHoldHarness{
		fx: fx, handler: h, persister: persister,
		cmd: &RunTourCoordinatorCommand{ShipSymbol: "HAULER-A", PlayerID: 1, ContainerID: "tour-1"},
	}
}

// relocateTo moves the fixture's hull to another system, under the SAME mutex buildShip reads it
// through, so the hold observes a genuine mid-wait position change without a data race.
func (h *offerHoldHarness) relocateTo(system string) {
	h.fx.mu.Lock()
	h.fx.location = system + "-A"
	h.fx.mu.Unlock()
}

func offerHoldCtx() context.Context {
	return common.WithLogger(context.Background(), &tradeCaptureLogger{})
}

// CASE 1: THE OFFER IS TAKEN. The relocator moves the hull mid-wait, and the hold must notice and
// return at once — the tour then re-plans on the NEW ground, which is the entire point of the feature.
// A hold that waited out the full window anyway would burn the rest of it for nothing.
func TestRelocationOfferHoldShould_ReturnAsSoonAsTheRelocatorMovesTheHull(t *testing.T) {
	h := newOfferHoldHarness(t)
	// A deliberately LONG window, so passing can only mean the hold noticed the move — not that it
	// waited the window out.
	deadline := time.Now().Add(30 * time.Second)
	go func() {
		time.Sleep(5 * time.Millisecond)
		h.relocateTo("X1-FAR")
	}()

	start := time.Now()
	taken := h.handler.holdForRelocationOffer(offerHoldCtx(), h.cmd, deadline, "X1-HOME")

	if !taken {
		t.Fatal("the hull was relocated during its window but the hold did not report the offer taken; the tour would keep waiting instead of planning on the new ground")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("the hold took %s to notice a move against a 30s window; it is waiting the window out rather than polling", elapsed)
	}
	// A taken offer is CLEARED with no backoff — the hull moved, so there is nothing to back off from.
	writes := h.persister.recorded()
	if len(writes) != 1 || !writes[0].OfferedUntil.IsZero() || !writes[0].BackoffUntil.IsZero() {
		t.Fatalf("a taken offer wrote %+v; it must clear the offer and stamp NO backoff", writes)
	}
}

// CASE 2: THE OFFER LAPSES. Nothing takes the hull, so the hold must END AT THE DEADLINE and stamp the
// backoff. This is the property whose absence is a permanently stalled hull.
func TestRelocationOfferHoldShould_EndAtTheDeadlineAndBackOffWhenNothingTakesTheHull(t *testing.T) {
	h := newOfferHoldHarness(t)
	deadline := time.Now().Add(40 * time.Millisecond)

	start := time.Now()
	taken := h.handler.holdForRelocationOffer(offerHoldCtx(), h.cmd, deadline, "X1-HOME")
	elapsed := time.Since(start)

	if taken {
		t.Fatal("the hold reported the offer taken though the hull never moved")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("the hold ran %s past a 40ms deadline — it is NOT bounded by the deadline, which is a stalled trade hull", elapsed)
	}
	// The backoff must be stamped, or a hull the relocator cannot move pays a window at every boundary.
	writes := h.persister.recorded()
	if len(writes) != 1 || writes[0].BackoffUntil.IsZero() {
		t.Fatalf("a lapsed offer wrote %+v; it must stamp a backoff so the hull is not re-offered next boundary", writes)
	}
	if !writes[0].OfferedUntil.IsZero() {
		t.Fatalf("a lapsed offer left an offer deadline set (%+v); the hull would still read as offered", writes[0])
	}
}

// CASE 3: A CANCELLED CONTEXT RETURNS IMMEDIATELY. A stop must never be delayed by an optimisation, and
// the daemon escalates a STOPPING container to a ctx cancel — a hold that ignored it would delay every
// shutdown by up to a full poll interval per waiting hull.
func TestRelocationOfferHoldShould_ReturnImmediatelyWhenTheContextIsCancelled(t *testing.T) {
	h := newOfferHoldHarness(t)
	h.handler.offerPollInterval = 30 * time.Second // so only the cancel can end this wait
	ctx, cancel := context.WithCancel(offerHoldCtx())
	cancel()

	start := time.Now()
	taken := h.handler.holdForRelocationOffer(ctx, h.cmd, time.Now().Add(time.Hour), "X1-HOME")

	if taken {
		t.Fatal("a cancelled hold reported the offer taken")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("a cancelled hold took %s to return against a 30s poll; a stop would be delayed by every waiting hull", elapsed)
	}
	// A cancelled hold must NOT stamp a backoff: the offer did not lapse on its merits, the daemon is
	// simply stopping, and recording a lapse would suppress this hull's next real offer.
	if writes := h.persister.recorded(); len(writes) != 0 {
		t.Fatalf("a cancelled hold wrote %+v; a shutdown is not a lapse and must not suppress the next offer", writes)
	}
}

// CASE 4: THE DEADLINE IS ABSOLUTE, so a restart cannot renew it. This is the property that turns a bug
// into a PERMANENTLY stalled hull: a relative window re-applied at every rebuild would extend the hold
// forever while each individual step looked correct.
//
// A deadline already in the past must be refused OUTRIGHT — no polling, no writes, no waiting — which is
// exactly what a restarted run holding a stale config key presents.
func TestRelocationOfferHoldShould_RefuseADeadlineThatHasAlreadyPassed(t *testing.T) {
	h := newOfferHoldHarness(t)

	start := time.Now()
	taken := h.handler.holdForRelocationOffer(offerHoldCtx(), h.cmd, time.Now().Add(-time.Second), "X1-HOME")

	if taken {
		t.Fatal("an expired offer reported as taken")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("an already-expired deadline still waited %s; a restarted run would hold its hull all over again", elapsed)
	}
	if writes := h.persister.recorded(); len(writes) != 0 {
		t.Fatalf("an expired offer wrote %+v; it is already over, so there is nothing to clear or back off", writes)
	}
}

// A FAILED CLEAR must not strand the hull. The waiter enforces the deadline from its OWN clock, so even
// when the durable clear fails the hull is not held — the next boundary simply re-reads an expired key,
// which does not stand.
func TestRelocationOfferHoldShould_StillReleaseTheHullWhenClearingTheOfferFails(t *testing.T) {
	h := newOfferHoldHarness(t)
	h.persister.failing = true

	taken := h.handler.holdForRelocationOffer(offerHoldCtx(), h.cmd, time.Now().Add(30*time.Millisecond), "X1-HOME")

	if taken {
		t.Fatal("the hold reported the offer taken though the hull never moved")
	}
	if writes := h.persister.recorded(); len(writes) != 1 {
		t.Fatalf("expected one attempted write, got %+v", writes)
	}
	// The hull is released by the RETURN, not by the write succeeding. Nothing further to assert than
	// that we got here: a hold that depended on the write would still be looping.
}
