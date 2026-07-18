package commands

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
)

// These tests cover the single-probe-buyer arbitration between bootstrap DATA and the standing
// freshsizer (sp-tsn2). During cold start both coordinators can buy probes against ONE shared
// fleet: bootstrap buys toward probe_target while freshsizer starts demanding freshness-rotation
// probes as soon as markets are scanned — the era-3 multi-buyer lesson (independent buyers grew one
// fleet past the ceiling). The arbitration lets bootstrap DEFER its probe BUY to freshsizer once the
// first market is covered (coverage>0) AND a freshsizer coordinator is actually running to take over,
// so exactly one buyer grows the fleet during the conflict window. It is knob-gated
// (defer_probe_to_freshsizer, a tunable flag reusing the sp-r6yq tune path) and DEFAULT OFF —
// byte-identical to today until the captain arms it live.

// arbHandler wires a bootstrap handler for the arbitration tests: a fixed observation, a ready
// probe acquirer, a scout assigner, and a live-config reader carrying (or not) the arbitration flag.
func arbHandler(obs Observation, acq *fakeAcquirer, scout *fakeScouter, live *fakeLiveConfig) *RunBootstrapCoordinatorHandler {
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(&fakeObserver{obs: obs})
	h.SetProbeAcquirer(acq)
	h.SetScoutAssigner(scout)
	h.SetLiveConfigReader(live)
	return h
}

// dataObs is a DATA-phase observation short of probe_target so a buy is attempted: coverage is a
// caller-set fraction of a 10-market home system, well under the 0.90 bar (still DATA).
func dataObs(marketsCovered int, freshsizerActive bool) Observation {
	return Observation{
		HomeSystem: "X1-HQ", ProbeCount: 1, ProbesScouting: 1, HasIdlePurchaser: true,
		MarketsTotal: 10, MarketsCovered: marketsCovered,
		FreshsizerActive: freshsizerActive, Treasury: 1_000_000, Readable: true,
	}
}

// Default OFF (no arbitration knob): even with coverage>0 and freshsizer active, bootstrap buys
// toward its target exactly as today. This is the byte-identical guarantee — the arbitration is inert
// until armed.
func TestBootstrap_ProbeArbitration_DefaultOff_BuysAsToday(t *testing.T) {
	acq := &fakeAcquirer{price: 40000, yard: "X1-HQ-YARD", readable: true}
	h := arbHandler(dataObs(1, true), acq, &fakeScouter{}, &fakeLiveConfig{snap: liveconfig.Snapshot{}})

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	// buy-to-target (sp-hh0h): 1/3 probes → the 2-probe remainder buys this tick when not deferring.
	if res.Purchased != 2 || acq.buys != 2 {
		t.Fatalf("default-off: bootstrap must buy toward target as today (purchased=%d buys=%d blocker=%q)", res.Purchased, acq.buys, res.Blocker)
	}
}

// ARMED + coverage>0 + freshsizer active: bootstrap DEFERS the probe buy to freshsizer (the single
// buyer during the conflict window). The buy does not fire and the heartbeat names the deferral.
func TestBootstrap_ProbeArbitration_ArmedAndCovered_DefersBuyToFreshsizer(t *testing.T) {
	acq := &fakeAcquirer{price: 40000, yard: "X1-HQ-YARD", readable: true}
	armed := &fakeLiveConfig{snap: liveconfig.Snapshot{"defer_probe_to_freshsizer": 1}}
	h := arbHandler(dataObs(1, true), acq, &fakeScouter{}, armed)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if res.Purchased != 0 || acq.buys != 0 {
		t.Fatalf("armed + coverage>0 + freshsizer active: bootstrap must DEFER the buy (purchased=%d buys=%d)", res.Purchased, acq.buys)
	}
	if res.Blocker != "deferred_to_freshsizer" {
		t.Fatalf("the deferral must be surfaced on the heartbeat, got blocker=%q", res.Blocker)
	}
}

// ARMED but coverage==0 (cold start, no market scanned yet): bootstrap STILL buys. Freshsizer has no
// scanned markets to size on, so bootstrap remains the sole initial provisioner — the deferral engages
// only ONCE coverage>0.
func TestBootstrap_ProbeArbitration_ArmedButNoCoverageYet_StillBuys(t *testing.T) {
	acq := &fakeAcquirer{price: 40000, yard: "X1-HQ-YARD", readable: true}
	armed := &fakeLiveConfig{snap: liveconfig.Snapshot{"defer_probe_to_freshsizer": 1}}
	h := arbHandler(dataObs(0, true), acq, &fakeScouter{}, armed) // 0 markets covered

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if res.Purchased != 2 || acq.buys != 2 {
		t.Fatalf("armed but coverage==0: bootstrap must still provision the initial probes to target (purchased=%d buys=%d)", res.Purchased, acq.buys)
	}
}

// ARMED + coverage>0 but freshsizer NOT running: bootstrap STILL buys — it never defers into a vacuum.
// Deferring to an absent buyer would wedge the cold start (no one provisions probes, DATA never
// clears). This is the enumerate-the-members safety: confirm freshsizer exists before deferring to it.
func TestBootstrap_ProbeArbitration_ArmedButFreshsizerDown_StillBuys(t *testing.T) {
	acq := &fakeAcquirer{price: 40000, yard: "X1-HQ-YARD", readable: true}
	armed := &fakeLiveConfig{snap: liveconfig.Snapshot{"defer_probe_to_freshsizer": 1}}
	h := arbHandler(dataObs(1, false), acq, &fakeScouter{}, armed) // coverage>0 but freshsizer down

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if res.Purchased != 2 || acq.buys != 2 {
		t.Fatalf("armed but freshsizer down: bootstrap must keep buying to target (never defer into a vacuum) (purchased=%d buys=%d)", res.Purchased, acq.buys)
	}
}

// The deferral is BUY-ONLY: while deferring the probe buy, bootstrap still assigns its existing probes
// to scout-all-markets (the arbitration is about the fleet-total BUY, not scouting). A probe short of
// scouting is still assigned.
func TestBootstrap_ProbeArbitration_DefersBuyButStillAssignsScouting(t *testing.T) {
	obs := dataObs(1, true)
	obs.ProbeCount = 2
	obs.ProbesScouting = 0 // two probes exist, none scouting yet
	acq := &fakeAcquirer{price: 40000, yard: "X1-HQ-YARD", readable: true}
	scout := &fakeScouter{}
	armed := &fakeLiveConfig{snap: liveconfig.Snapshot{"defer_probe_to_freshsizer": 1}}
	h := arbHandler(obs, acq, scout, armed)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if res.Purchased != 0 || acq.buys != 0 {
		t.Fatalf("armed: the buy must be deferred (purchased=%d buys=%d)", res.Purchased, acq.buys)
	}
	if scout.calls != 1 || !res.Scouted {
		t.Fatalf("deferral is buy-only: bootstrap must still assign scouting (scout.calls=%d scouted=%v)", scout.calls, res.Scouted)
	}
}
