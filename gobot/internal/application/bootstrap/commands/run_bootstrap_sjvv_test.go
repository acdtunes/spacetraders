package commands

import (
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
)

// sp-sjvv (ktio-B): the cold-start contract-scaling feature. ONE tunable flag (autosizer_early_scaling,
// default OFF) arms TWO coupled behaviors so the capacity reconciler's emitted contract-delivery demand
// finally has a buyer during cold start: (1) bootstrap LAUNCHES the fleet autosizer EARLY during the
// DATA/INCOME scaling window, and (2) bootstrap DEFERS its own contract-hauler buys to that autosizer once
// it is running (single-buyer arbitration — the sibling of the sp-tsn2 probe→freshsizer deferral). Default
// OFF is byte-identical: the autosizer stays off the whole bootstrap run and bootstrap buys its haulers
// itself, exactly as today.

// sjvvHandler wires a bootstrap handler with the INCOME collaborators plus a hand-off launcher and a
// live-config reader (carrying — or not — the arbitration flag), so a single tick exercises both the
// hauler-defer arbitration and the early autosizer launch.
func sjvvHandler(obs Observation, live *fakeLiveConfig, ho *fakeHandoff, haul *fakeHaulerAcquirer) *RunBootstrapCoordinatorHandler {
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(&fakeObserver{obs: obs})
	h.SetProbeAcquirer(&fakeAcquirer{price: 40000, yard: "Y", readable: true}) // present, unused in INCOME
	h.SetScoutAssigner(&fakeScouter{})
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

// sjvvIncomeObs is an INCOME-phase observation (coverage met, income under the bar) with the autosizer
// running-state and hauler pool set by the caller. BatchContractRunning=true isolates the hauler decision
// (step 4) from the batch-contract launch (step 2).
func sjvvIncomeObs(autosizerRunning bool, haulers int) Observation {
	o := incomeObs()
	o.AutosizerRunning = autosizerRunning
	o.BatchContractRunning = true
	o.Haulers = make([]HaulerSnapshot, haulers)
	return o
}

// --- single-buyer arbitration: bootstrap defers the contract-hauler buy to the autosizer ---

// Default OFF: even with the autosizer running, bootstrap buys its contract hauler exactly as today — the
// byte-identical guarantee (the arbitration is inert until armed).
func TestBootstrap_HaulerArbitration_DefaultOff_BuysAsToday(t *testing.T) {
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}
	// Autosizer running, flag OFF: the defer must NOT engage.
	h := sjvvHandler(sjvvIncomeObs(true, 0), &fakeLiveConfig{snap: liveconfig.Snapshot{}}, &fakeHandoff{}, acq)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if acq.buys != 1 || res.HaulersBought != 1 {
		t.Fatalf("default-off: bootstrap must buy its contract hauler as today (buys=%d haulers_bought=%d blocker=%q)", acq.buys, res.HaulersBought, res.Blocker)
	}
	if res.Blocker == "deferred_to_autosizer" {
		t.Fatalf("default-off must NOT defer, got blocker=%q", res.Blocker)
	}
}

// ARMED + autosizer running: bootstrap DEFERS the hauler buy to the autosizer (the single buyer during the
// conflict window). No buy fires and the heartbeat names the deferral.
func TestBootstrap_HaulerArbitration_ArmedAndAutosizerRunning_Defers(t *testing.T) {
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}
	armed := &fakeLiveConfig{snap: liveconfig.Snapshot{"autosizer_early_scaling": 1}}
	h := sjvvHandler(sjvvIncomeObs(true, 0), armed, &fakeHandoff{}, acq)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if acq.buys != 0 || res.HaulersBought != 0 {
		t.Fatalf("armed + autosizer running: bootstrap must DEFER the hauler buy (buys=%d haulers_bought=%d)", acq.buys, res.HaulersBought)
	}
	if res.Blocker != "deferred_to_autosizer" {
		t.Fatalf("the deferral must be surfaced on the heartbeat, got blocker=%q", res.Blocker)
	}
}

// ARMED but autosizer NOT running yet (the cold-start bootstrapping tick): bootstrap STILL buys its hauler
// — it never defers into a vacuum, so the no_purchaser deadlock cannot wedge the cold start. On the SAME
// tick it launches the autosizer early, so the NEXT tick will defer. This is the non-wedge dynamic.
func TestBootstrap_HaulerArbitration_ArmedButAutosizerDown_BuysAndLaunchesEarly(t *testing.T) {
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}
	ho := &fakeHandoff{}
	armed := &fakeLiveConfig{snap: liveconfig.Snapshot{"autosizer_early_scaling": 1}}
	h := sjvvHandler(sjvvIncomeObs(false, 0), armed, ho, acq) // autosizer down

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if acq.buys != 1 || res.HaulersBought != 1 {
		t.Fatalf("armed but autosizer down: bootstrap must still buy its hauler (never defer into a vacuum) (buys=%d)", acq.buys)
	}
	if ho.autosizer != 1 || !res.AutosizerLaunchedEarly {
		t.Fatalf("armed + autosizer down + scaling window: bootstrap must launch the autosizer EARLY this tick (autosizer_launches=%d early=%v)", ho.autosizer, res.AutosizerLaunchedEarly)
	}
}

// --- early autosizer launch: only in the DATA/INCOME scaling window, idempotent, off by default ---

// ARMED in the INCOME scaling window with the autosizer down: bootstrap launches it once. (Haulers at the
// target isolate the launch from the hauler decision.)
func TestBootstrap_EarlyAutosizer_ArmedInIncome_Launches(t *testing.T) {
	ho := &fakeHandoff{}
	armed := &fakeLiveConfig{snap: liveconfig.Snapshot{"autosizer_early_scaling": 1}}
	obs := sjvvIncomeObs(false, 3) // 3 haulers = desired (3 viable hubs) → no hauler buy this tick
	h := sjvvHandler(obs, armed, ho, &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true})

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if ho.autosizer != 1 || !res.AutosizerLaunchedEarly {
		t.Fatalf("armed + INCOME + autosizer down: must launch the autosizer early once (autosizer_launches=%d early=%v)", ho.autosizer, res.AutosizerLaunchedEarly)
	}
	if ho.standing != 0 {
		t.Fatalf("the EARLY launch must NOT launch the standing coordinators (siting/rebalancer) — that is the COMPLETE hand-off's job, got standing=%d", ho.standing)
	}
}

// Default OFF: the autosizer is NEVER launched during bootstrap (byte-identical — it stays off the whole
// run, exactly as today).
func TestBootstrap_EarlyAutosizer_DefaultOff_NeverLaunches(t *testing.T) {
	ho := &fakeHandoff{}
	h := sjvvHandler(sjvvIncomeObs(false, 0), &fakeLiveConfig{snap: liveconfig.Snapshot{}}, ho, &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true})

	if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if ho.autosizer != 0 {
		t.Fatalf("default-off: the autosizer must NEVER be launched during bootstrap, got autosizer_launches=%d", ho.autosizer)
	}
}

// ARMED but the autosizer is already running: no relaunch (idempotent — the steady state once launched).
func TestBootstrap_EarlyAutosizer_AlreadyRunning_NoRelaunch(t *testing.T) {
	ho := &fakeHandoff{}
	armed := &fakeLiveConfig{snap: liveconfig.Snapshot{"autosizer_early_scaling": 1}}
	h := sjvvHandler(sjvvIncomeObs(true, 3), armed, ho, &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true})

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if ho.autosizer != 0 || res.AutosizerLaunchedEarly {
		t.Fatalf("armed + autosizer already running: must NOT relaunch (autosizer_launches=%d early=%v)", ho.autosizer, res.AutosizerLaunchedEarly)
	}
}

// ARMED but in the GATE phase: the autosizer is NOT launched early — the early launch is scoped to the
// DATA/INCOME scaling window (GATE repurposes haulers to construction; a running autosizer scaling the
// contract op would contend).
func TestBootstrap_EarlyAutosizer_NotLaunchedDuringGate(t *testing.T) {
	ho := &fakeHandoff{}
	obs := gateObs() // GATE phase (construction started + adopted), autosizer not running
	h := gateHandler(obs, &fakeConstruction{}, &fakeManufacturing{}, &fakeRepurposer{}, &fakeGateAcquirer{}, ho)
	h.SetLiveConfigReader(&fakeLiveConfig{snap: liveconfig.Snapshot{"autosizer_early_scaling": 1}})

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if res.Phase != PhaseGate {
		t.Fatalf("expected GATE phase, got %s", res.Phase)
	}
	if ho.autosizer != 0 || res.AutosizerLaunchedEarly {
		t.Fatalf("armed but GATE: the autosizer must NOT be launched early during GATE (autosizer_launches=%d early=%v)", ho.autosizer, res.AutosizerLaunchedEarly)
	}
}

// --- COMPLETE hand-off collision: an early-launched autosizer must still get the standing coordinators ---

// ARMED + COMPLETE + autosizer already running (launched early): the autosizer is NOT relaunched, but the
// standing coordinators (siting + rebalancer) — which the early launch did NOT start — ARE launched, and
// bootstrap exits. This is the collision fix: the COMPLETE hand-off's autosizer-gated path is skipped, so
// its second half must still run.
func TestBootstrap_Complete_ArmedEarlyLaunched_LaunchesStandingCoordinators(t *testing.T) {
	obs := gateObs()
	obs.ConstructionComplete = true // derives COMPLETE
	obs.AutosizerRunning = true     // launched early during the scaling window
	ho := &fakeHandoff{}
	h := gateHandler(obs, &fakeConstruction{}, &fakeManufacturing{}, &fakeRepurposer{}, &fakeGateAcquirer{}, ho)
	h.SetLiveConfigReader(&fakeLiveConfig{snap: liveconfig.Snapshot{"autosizer_early_scaling": 1}})

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if ho.autosizer != 0 {
		t.Fatalf("the autosizer was already launched early — it must NOT be relaunched at COMPLETE, got autosizer=%d", ho.autosizer)
	}
	if ho.standing != 1 {
		t.Fatalf("the standing coordinators (siting + rebalancer) MUST be launched at COMPLETE even when the autosizer was launched early, got standing=%d", ho.standing)
	}
	if !res.HandoffLaunched || !res.Done {
		t.Fatalf("COMPLETE must finish the hand-off and exit, got HandoffLaunched=%v Done=%v", res.HandoffLaunched, res.Done)
	}
}

// ARMED + COMPLETE + autosizer running, but the standing-coordinator launch FAILS: bootstrap HOLDS (does
// not exit) and retries — it never exits with the mature economy half-handed-off.
func TestBootstrap_Complete_ArmedEarlyLaunched_HoldsWhenStandingLaunchFails(t *testing.T) {
	obs := gateObs()
	obs.ConstructionComplete = true
	obs.AutosizerRunning = true
	ho := &fakeHandoff{standErr: errors.New("boom")}
	h := gateHandler(obs, &fakeConstruction{}, &fakeManufacturing{}, &fakeRepurposer{}, &fakeGateAcquirer{}, ho)
	h.SetLiveConfigReader(&fakeLiveConfig{snap: liveconfig.Snapshot{"autosizer_early_scaling": 1}})

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if res.Done {
		t.Fatalf("a failed standing-coordinator launch must NOT exit (Done must stay false)")
	}
	if res.Blocker != "standing_launch_error" {
		t.Fatalf("expected blocker standing_launch_error, got %q", res.Blocker)
	}
}

// --- config resolution: the flag is a tunable, default OFF, armable live ---

func TestBootstrap_AutosizerEarlyScaling_DefaultOffAndArmable(t *testing.T) {
	if BootstrapTunableDefaults()["autosizer_early_scaling"] != 0 {
		t.Fatalf("autosizer_early_scaling default must be 0 (OFF)")
	}
	off := resolveBootstrapConfig(baseCmd(), nil)
	if off.AutosizerEarlyScaling {
		t.Fatalf("autosizer_early_scaling must default OFF (nil live config)")
	}
	armed := resolveBootstrapConfig(baseCmd(), liveconfig.Snapshot{"autosizer_early_scaling": 1})
	if !armed.AutosizerEarlyScaling {
		t.Fatalf("autosizer_early_scaling must arm when the live snapshot carries a positive value")
	}
}
