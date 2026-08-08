package commands

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
)

// sp-sjvv (ktio-B), as it stands after sp-5pclx deleted the fleet autosizer.
//
// The cold-start contract op still has to scale — the Admiral's constraint is "we still need contract
// hull buying during bootstrap" — and after the cut it scales through TWO surviving mechanisms, both
// pinned here:
//
//   - the DEDICATED CONTRACT SCALER, ensured every cold-start tick (ensureContractScalerEarly). It owned
//     contract-fleet capacity everywhere else already; now it owns it in cold start too, so there is one
//     contract buyer instead of two. Its own tests live in run_bootstrap_contract_scaler_test.go.
//   - BOOTSTRAP'S OWN staged hauler buys, which are STATICALLY owned: bootstrap seeds the cold-start
//     hulls behind its capital gates regardless of which standing coordinators happen to be up. There is
//     no runtime hand-off decision between the two, so deleting a coordinator cannot open a buying vacuum.
//
// What is GONE is the early launch of the autosizer itself (maybeLaunchAutosizerEarly) — it existed to
// give the capacity reconciler's contract-delivery demand a buyer, and BOTH ends of that bridge were
// already deleted (the reconciler by sp-y2ptq, the autosizer's contract class with it) before this cut.

// sjvvHandler wires a bootstrap handler with the contract collaborators plus a hand-off launcher and a
// live-config reader, so a single tick exercises the staged hauler buy and the cold-start scaler ensure.
func sjvvHandler(obs Observation, live *fakeLiveConfig, ho *fakeHandoff, haul *fakeHaulerAcquirer) *RunBootstrapCoordinatorHandler {
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(&fakeObserver{obs: obs})
	h.SetProbeAcquirer(&fakeAcquirer{price: 40000, yard: "Y", readable: true}) // present, unused by the contract workstream
	h.SetFrigateRetirer(&fakeRetirer{})
	h.SetContractRunner(&fakeContractRunner{})
	if haul != nil {
		h.SetHaulerAcquirer(haul)
	}
	if ho != nil {
		h.SetHandoffLauncher(ho)
	}
	if live != nil {
		h.SetLiveConfigReader(live)
	}
	return h
}

// sjvvColdStartObs is a cold-start observation with the growth running-state and hauler pool set by the
// caller. BatchContractRunning=true isolates the hauler decision (step 4) from the batch-contract launch
// (step 2).
func sjvvColdStartObs(growthRunning bool, haulers int) Observation {
	o := incomeObs()
	o.GrowthRunning = growthRunning
	o.BatchContractRunning = true
	o.Haulers = make([]HaulerSnapshot, haulers)
	// The trade hull is seeded once the FIRST contract hull exists (acquisition #2 → trade), so a
	// fixture with ≥1 contract hull models the POST-seed state (TradeHullCount=1) — the contract-scaling
	// behavior these tests pin happens after the trade seed. A 0-hauler fixture is pre-seed.
	if haulers >= 1 {
		o.TradeHullCount = 1
	}
	return o
}

// --- static ownership: a running standing coordinator never diverts bootstrap's own staged buy ---

// THE ADMIRAL'S CONSTRAINT, pinned: contract hull buying survives bootstrap. A standing coordinator is
// RUNNING and the contract pool sits where the old runtime arbitration would have handed the buy over.
// Bootstrap's ownership is STATIC, so it buys its staged hauler regardless: no observation of another
// coordinator can suppress a seed acquisition, and no "deferred" blocker reaches the heartbeat.
//
// This is the test that would fail if deleting a coordinator had opened a buying vacuum.
func TestBootstrap_StaticOwnership_StandingCoordinatorRunningStillBuysHauler(t *testing.T) {
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}
	h := sjvvHandler(sjvvColdStartObs(true, 2), &fakeLiveConfig{snap: liveconfig.Snapshot{}}, &fakeHandoff{}, acq)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if acq.buys != 1 || res.HaulersBought != 1 {
		t.Fatalf("standing coordinator running: bootstrap still owns its staged hauler buy (buys=%d haulers_bought=%d blocker=%q)", acq.buys, res.HaulersBought, res.Blocker)
	}
	if res.Blocker != "" {
		t.Fatalf("a running standing coordinator must not surface any blocker on bootstrap's own buy, got %q", res.Blocker)
	}
}

// The same, with NO standing coordinator up: bootstrap buys its hauler on exactly the same terms. The
// two fixtures together are the point — the buy is invariant under the observation the old arbitration
// branched on, so there is no state in which the contract op stops being seeded.
func TestBootstrap_StaticOwnership_NoStandingCoordinator_StillBuysHauler(t *testing.T) {
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}
	h := sjvvHandler(sjvvColdStartObs(false, 2), &fakeLiveConfig{snap: liveconfig.Snapshot{}}, &fakeHandoff{}, acq)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if acq.buys != 1 || res.HaulersBought != 1 {
		t.Fatalf("no standing coordinator: bootstrap must still buy its staged hauler (buys=%d haulers_bought=%d blocker=%q)", acq.buys, res.HaulersBought, res.Blocker)
	}
}

// --- the hand-off does NOT run early: growth is an EXPANSION launch, not a cold-start one ---

// In the cold-start scaling window bootstrap ensures the CONTRACT SCALER but must NOT launch the standing
// fleet-growth coordinator: growth is the EXPANSION hand-off, and starting the fleet's heavy buyer while
// bootstrap is still seeding would put two spenders on one treasury — the exact condition the single
// hand-off exists to prevent. (3 haulers = desired, isolating this from the hauler decision.)
func TestBootstrap_ColdStart_EnsuresScalerButNotGrowth(t *testing.T) {
	ho := &fakeHandoff{}
	obs := sjvvColdStartObs(false, 3)
	h := sjvvHandler(obs, &fakeLiveConfig{snap: liveconfig.Snapshot{}}, ho, &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true})

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if ho.contractScaler != 1 || !res.ContractScalerLaunchedEarly {
		t.Fatalf("cold start must ensure the dedicated contract scaler — it is what keeps contract hull buying alive during bootstrap (scaler_launches=%d early=%v)", ho.contractScaler, res.ContractScalerLaunchedEarly)
	}
	if ho.standing != 0 {
		t.Fatalf("cold start must NOT launch the standing fleet-growth coordinator — that is the EXPANSION hand-off's job, got standing=%d", ho.standing)
	}
}

// In the GATE phase neither the contract scaler nor growth is launched: GATE repurposes haulers to
// construction, and a coordinator scaling the contract op would contend for the same hulls.
func TestBootstrap_Gate_LaunchesNeitherScalerNorGrowth(t *testing.T) {
	ho := &fakeHandoff{}
	obs := gateObs() // GATE phase (construction started + adopted), no standing coordinator running
	h := gateHandler(obs, &fakeConstruction{}, &fakeManufacturing{}, &fakeRepurposer{}, &fakeGateAcquirer{}, ho)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if res.Phase != PhaseGate {
		t.Fatalf("expected GATE phase, got %s", res.Phase)
	}
	if ho.contractScaler != 0 || ho.standing != 0 {
		t.Fatalf("GATE must launch neither the scaler nor growth (scaler=%d standing=%d)", ho.contractScaler, ho.standing)
	}
}

// --- EXPANSION: ONE launch, ONE latch (terminal idempotency on the thing actually launched) ---

// EXPANSION with growth NOT yet running: the standing fleet-growth coordinator IS launched and bootstrap
// exits.
func TestBootstrap_Expansion_GrowthDown_LaunchesGrowthAndExits(t *testing.T) {
	obs := gateObs()
	obs.ConstructionComplete = true // derives EXPANSION
	obs.GrowthRunning = false
	ho := &fakeHandoff{}
	h := gateHandler(obs, &fakeConstruction{}, &fakeManufacturing{}, &fakeRepurposer{}, &fakeGateAcquirer{}, ho)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if ho.standing != 1 {
		t.Fatalf("EXPANSION with growth down must launch the standing fleet-growth coordinator exactly once, got standing=%d", ho.standing)
	}
	if !res.HandoffLaunched || !res.Done {
		t.Fatalf("EXPANSION must finish the hand-off and exit, got HandoffLaunched=%v Done=%v", res.HandoffLaunched, res.Done)
	}
}

// EXPANSION with growth ALREADY running (a restart post-gate): the latch holds — growth is NOT relaunched
// and bootstrap exits straight away. This is the terminal idempotency the latch exists for, and it is now
// keyed on the SAME coordinator the hand-off launches, so "already handed off" is a fact about the thing
// that was handed off rather than about a second coordinator that merely rode along.
func TestBootstrap_Expansion_GrowthRunning_NoRelaunchAndExits(t *testing.T) {
	obs := gateObs()
	obs.ConstructionComplete = true
	obs.GrowthRunning = true
	ho := &fakeHandoff{}
	h := gateHandler(obs, &fakeConstruction{}, &fakeManufacturing{}, &fakeRepurposer{}, &fakeGateAcquirer{}, ho)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if ho.standing != 0 {
		t.Fatalf("growth already running: the hand-off must NOT relaunch it, got standing=%d", ho.standing)
	}
	if !res.Done {
		t.Fatalf("EXPANSION with growth already up must exit (Done), got Done=%v", res.Done)
	}
}
