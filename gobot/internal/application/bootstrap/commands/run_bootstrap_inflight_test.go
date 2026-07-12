package commands

import (
	"context"
	"sync"
	"testing"
)

// --- D2 (st-drm.6): in-flight-aware acquisition dispatch -----------------------------------------
//
// A staged buy dispatches an acquisition (a batch-purchase that must navigate its buyer to the yard
// and buy) whose new hull does NOT surface in the observation the same tick. The count-only guard
// therefore re-derives need>0 and re-dispatches on every lagging tick — buying 3 probes for a target
// of 3 already met, or 6 satellites for a target of 3 at high compression. The invariant: at most ONE
// in-flight acquisition per (player, shipType); a tick observing need>0 while one is active dispatches
// NOTHING (the next tick after the acquisition lands re-derives need from the world and proceeds).

// fakeAcquisitionTracker is a black-box double for the pending-acquisition port.
type fakeAcquisitionTracker struct {
	inFlight bool
	err      error
	calls    int
	lastType string
}

func (f *fakeAcquisitionTracker) InFlight(ctx context.Context, playerID int, shipType string) (bool, error) {
	f.calls++
	f.lastType = shipType
	return f.inFlight, f.err
}

// --- single-tick pins: an active acquisition blocks; none active dispatches once ------------------

func TestBootstrap_ProbeBuy_SkippedWhileAcquisitionInFlight(t *testing.T) {
	obs := Observation{HomeSystem: "X1-HQ", ProbeCount: 1, ProbesScouting: 1, HasIdlePurchaser: true, Treasury: 150000, Readable: true}
	acq := &fakeAcquirer{price: 40000, yard: "Y", readable: true}
	h := newWiredHandler(obs, acq, &fakeScouter{})
	tracker := &fakeAcquisitionTracker{inFlight: true}
	h.SetAcquisitionTracker(tracker)

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())

	if acq.buys != 0 || acq.priceChks != 0 || res.Purchased != 0 {
		t.Fatalf("in-flight acquisition: the tick must dispatch nothing (no price-check, no buy); priceChks=%d buys=%d purchased=%d", acq.priceChks, acq.buys, res.Purchased)
	}
	if res.Blocker != "acquisition_in_flight" {
		t.Fatalf("expected acquisition_in_flight blocker, got %q", res.Blocker)
	}
	if tracker.lastType != "SHIP_PROBE" {
		t.Fatalf("the tracker must be consulted for the probe ship type, got %q", tracker.lastType)
	}
}

func TestBootstrap_ProbeBuy_DispatchesWhenNoneInFlight(t *testing.T) {
	obs := Observation{HomeSystem: "X1-HQ", ProbeCount: 1, ProbesScouting: 1, HasIdlePurchaser: true, Treasury: 150000, Readable: true}
	acq := &fakeAcquirer{price: 40000, yard: "Y", readable: true}
	h := newWiredHandler(obs, acq, &fakeScouter{})
	h.SetAcquisitionTracker(&fakeAcquisitionTracker{inFlight: false})

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())

	if acq.buys != 1 || res.Purchased != 1 {
		t.Fatalf("no in-flight acquisition: the tick must dispatch exactly one buy; buys=%d purchased=%d", acq.buys, res.Purchased)
	}
}

func TestBootstrap_ProbeBuy_TrackerReadError_FailsClosed(t *testing.T) {
	// A tracker read miss must fail CLOSED (treat as in-flight, dispatch nothing) — a repo hiccup must
	// never green-light a double-buy.
	obs := Observation{HomeSystem: "X1-HQ", ProbeCount: 1, ProbesScouting: 1, HasIdlePurchaser: true, Treasury: 150000, Readable: true}
	acq := &fakeAcquirer{price: 40000, yard: "Y", readable: true}
	h := newWiredHandler(obs, acq, &fakeScouter{})
	h.SetAcquisitionTracker(&fakeAcquisitionTracker{err: context.DeadlineExceeded})

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())

	if acq.buys != 0 || res.Purchased != 0 {
		t.Fatalf("tracker error must fail closed (no buy); buys=%d purchased=%d", acq.buys, res.Purchased)
	}
	if res.Blocker != "acquisition_in_flight" {
		t.Fatalf("expected acquisition_in_flight blocker on read miss, got %q", res.Blocker)
	}
}

// --- multi-tick: the in-flight guard absorbs the read-after-write lag with no overshoot -----------

// laggingAcquisitionModel is the observer + acquirer + tracker for one player: a dispatched probe buy
// stays in flight for ticksToLand ticks (its hull not yet in the observation), then the hull lands
// (ProbeCount rises) and the acquisition exits (in-flight clears). It reproduces exactly the lag the
// live incident exposed, so the multi-tick run proves the guard buys the target EXACTLY once.
type laggingAcquisitionModel struct {
	mu          sync.Mutex
	probeCount  int
	pending     []int // ticks-to-land for each dispatched-but-unlanded hull
	ticksToLand int
	buys        int
}

func (m *laggingAcquisitionModel) advance() {
	m.mu.Lock()
	defer m.mu.Unlock()
	var still []int
	for _, remaining := range m.pending {
		remaining--
		if remaining <= 0 {
			m.probeCount++ // the hull finally surfaces in the world
		} else {
			still = append(still, remaining)
		}
	}
	m.pending = still
}

func (m *laggingAcquisitionModel) Observe(ctx context.Context, playerID int) (Observation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Observation{
		HomeSystem:       "X1-HQ",
		ProbeCount:       m.probeCount,
		ProbesScouting:   m.probeCount, // already scouting — keep the DATA act on the buy path only
		HasIdlePurchaser: true,
		MarketsTotal:     10,
		MarketsCovered:   0, // stay in DATA
		Treasury:         500000,
		Readable:         true,
	}, nil
}

func (m *laggingAcquisitionModel) InFlight(ctx context.Context, playerID int, shipType string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.pending) > 0, nil
}

func (m *laggingAcquisitionModel) PriceCheck(ctx context.Context, playerID int, shipType string) (int64, string, bool, error) {
	return 40000, "X1-HQ-YARD", true, nil
}

func (m *laggingAcquisitionModel) Buy(ctx context.Context, playerID int, shipType, yard string) (BuyResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.buys++
	m.pending = append(m.pending, m.ticksToLand)
	return BuyResult{ShipSymbol: "PROBE-DISPATCHED", Price: 40000}, nil
}

func TestBootstrap_ProbeBuy_NoOvershoot_WhenHullLagsBehindObservation(t *testing.T) {
	model := &laggingAcquisitionModel{ticksToLand: 2} // each dispatched hull surfaces two ticks later
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(model)
	h.SetProbeAcquirer(model)
	h.SetScoutAssigner(&fakeScouter{})
	h.SetAcquisitionTracker(model)

	// Default ProbeTarget is 3. Drive plenty of ticks; the hull lands with a 2-tick lag, so without the
	// guard the reconciler would re-dispatch on every lagging tick and blow past 3.
	for i := 0; i < 12; i++ {
		model.advance() // the world advances between ticks: any landed hull now shows up
		res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
		if err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
		if res.Purchased > 1 {
			t.Fatalf("tick %d dispatched %d buys — at most one acquisition per tick", i, res.Purchased)
		}
	}

	model.mu.Lock()
	buys, probes := model.buys, model.probeCount
	model.mu.Unlock()
	if buys != 3 {
		t.Fatalf("in-flight guard: must dispatch EXACTLY 3 probe buys for a target of 3 (no overshoot despite the lag), got %d", buys)
	}
	if probes != 3 {
		t.Fatalf("expected 3 probes to have landed, got %d", probes)
	}
}
