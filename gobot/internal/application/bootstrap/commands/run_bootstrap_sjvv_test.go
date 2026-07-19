package commands

import (
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
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
	h.SetScoutPostDeclarer(&fakeDeclarer{})
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

// DISABLED (kill-switch): even with the autosizer running, bootstrap buys its contract hauler exactly as the
// pre-arm behavior — the arbitration is inert once disabled.
func TestBootstrap_HaulerArbitration_Disabled_BuysAsToday(t *testing.T) {
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}
	// Autosizer running, arbitration DISABLED: the defer must NOT engage.
	h := sjvvHandler(sjvvIncomeObs(true, 0), &fakeLiveConfig{snap: liveconfig.Snapshot{"autosizer_early_scaling_disabled": 1}}, &fakeHandoff{}, acq)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if acq.buys != 1 || res.HaulersBought != 1 {
		t.Fatalf("disabled: bootstrap must buy its contract hauler as pre-arm (buys=%d haulers_bought=%d blocker=%q)", acq.buys, res.HaulersBought, res.Blocker)
	}
	if res.Blocker == "deferred_to_autosizer" {
		t.Fatalf("disabled must NOT defer, got blocker=%q", res.Blocker)
	}
}

// ARMED + autosizer running but ZERO haulers: bootstrap does NOT defer — it seeds the cash-flow-critical
// FIRST hauler. With an idle purchaser present it buys directly at acv5's cushion; the autosizer takes over
// only at/above the tier (sp-mvo8 seeds 0→tier). The below-tier seed is what dissolves the arbitration bypass.
func TestBootstrap_HaulerArbitration_ArmedAndAutosizerRunning_FirstHaulerKept(t *testing.T) {
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}
	armed := &fakeLiveConfig{snap: liveconfig.Snapshot{"autosizer_early_scaling": 1}}
	h := sjvvHandler(sjvvIncomeObs(true, 0), armed, &fakeHandoff{}, acq) // 0 haulers ⇒ the first hauler

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if acq.buys != 1 || res.HaulersBought != 1 {
		t.Fatalf("armed + autosizer running + 0 haulers: bootstrap must KEEP the first hauler (buys=%d haulers_bought=%d blocker=%q)", acq.buys, res.HaulersBought, res.Blocker)
	}
	if res.Blocker == "deferred_to_autosizer" {
		t.Fatalf("the FIRST hauler must NOT be deferred (Option 1), got blocker=%q", res.Blocker)
	}
}

// --- sp-mvo8: seed-tier ownership by range (bootstrap owns 0→tier, the autosizer owns tier→N) ---

// ARMED + autosizer running but the pool is BELOW the tier (capacity.ContractHaulerTierSaturation):
// bootstrap SEEDS the tier ITSELF — it BUYS, it does NOT defer. Below the tier the reconciler withholds
// ALL contract-delivery demand (ComputeDesired returns empty), so a deferral here would leave nobody to
// buy and the pool would stick at 1 — the two-buyer deadlock. The buy runs behind the existing capital
// gate (an idle purchaser executes it). One hauler is strictly below the tier of 2.
func TestBootstrap_SeedsHaulerTier_BuysBelowTier_WhenArbitrationArmed(t *testing.T) {
	if capacity.ContractHaulerTierSaturation < 2 {
		t.Skip("test assumes a tier ≥ 2 so len==1 is strictly below it")
	}
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}
	armed := &fakeLiveConfig{snap: liveconfig.Snapshot{"autosizer_early_scaling": 1}}
	// One hauler = below the tier → bootstrap must seed (buy), not defer.
	h := sjvvHandler(sjvvIncomeObs(true, capacity.ContractHaulerTierSaturation-1), armed, &fakeHandoff{}, acq)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if acq.buys != 1 || res.HaulersBought != 1 {
		t.Fatalf("armed + autosizer running + below tier: bootstrap must SEED the tier (buy), not defer (buys=%d haulers_bought=%d blocker=%q)", acq.buys, res.HaulersBought, res.Blocker)
	}
	if res.Blocker == "deferred_to_autosizer" {
		t.Fatalf("below the tier bootstrap owns the buy (0→tier) — must NOT defer, got blocker=%q", res.Blocker)
	}
}

// ARMED + autosizer running + the pool AT the tier (capacity.ContractHaulerTierSaturation): bootstrap
// DEFERS tier→N scaling to the autosizer — the single buyer now that the reconciler emits demand (at the
// tier ComputeDesired stops withholding). Anti-theatre half: the at-tier defer holds on BOTH the buggy
// and the fixed code (only the below-tier path changed), so a green below-tier test is a validated flip,
// not a tautology.
func TestBootstrap_DefersToAutosizer_AtOrAboveTier(t *testing.T) {
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}
	armed := &fakeLiveConfig{snap: liveconfig.Snapshot{"autosizer_early_scaling": 1}}
	// At the tier → the autosizer owns scaling; bootstrap defers.
	h := sjvvHandler(sjvvIncomeObs(true, capacity.ContractHaulerTierSaturation), armed, &fakeHandoff{}, acq)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if acq.buys != 0 || res.HaulersBought != 0 {
		t.Fatalf("armed + autosizer running + at tier: bootstrap must DEFER scaling (buys=%d haulers_bought=%d)", acq.buys, res.HaulersBought)
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

// DEFAULT-ON (sp-5nd2), no tune: the autosizer launches early in the scaling window with NO explicit arm —
// the awakened behavior a fresh cold start now runs. (3 haulers = desired, isolating the launch from the
// hauler decision.)
func TestBootstrap_EarlyAutosizer_DefaultOn_LaunchesInScalingWindow(t *testing.T) {
	ho := &fakeHandoff{}
	h := sjvvHandler(sjvvIncomeObs(false, 3), &fakeLiveConfig{snap: liveconfig.Snapshot{}}, ho, &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true})

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if ho.autosizer != 1 || !res.AutosizerLaunchedEarly {
		t.Fatalf("default-on + INCOME + autosizer down: must launch the autosizer early with NO tune (autosizer_launches=%d early=%v)", ho.autosizer, res.AutosizerLaunchedEarly)
	}
}

// DISABLED (sp-5nd2 live kill-switch): the autosizer is NEVER launched even in the scaling window — the
// operator's no-restart OFF, restoring the pre-arm behavior (autosizer off the whole run).
func TestBootstrap_EarlyAutosizer_Disabled_NeverLaunches(t *testing.T) {
	ho := &fakeHandoff{}
	h := sjvvHandler(sjvvIncomeObs(false, 3), &fakeLiveConfig{snap: liveconfig.Snapshot{"autosizer_early_scaling_disabled": 1}}, ho, &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true})

	if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if ho.autosizer != 0 {
		t.Fatalf("disabled: the autosizer must NEVER be launched during bootstrap, got autosizer_launches=%d", ho.autosizer)
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

// --- config resolution: DEFAULT-ON, live-disable-able, force-on override, disable-wins (sp-5nd2) ---

func TestBootstrap_AutosizerEarlyScaling_DefaultOnAndDisablable(t *testing.T) {
	// The arm now lives in the config default (the cmd disable-flag negation), NOT a positive const:
	// the FORCE-ON knob's default stays 0 (not-forced), so the tunable-defaults mirror still reads 0 —
	// the real default is the RESOLVED config below (nil live ⇒ armed).
	if BootstrapTunableDefaults()["autosizer_early_scaling"] != 0 {
		t.Fatalf("the force-on knob's default must stay 0 (not-forced) — the arm is the config default")
	}
	if on := resolveBootstrapConfig(baseCmd(), nil); !on.AutosizerEarlyScaling {
		t.Fatalf("autosizer_early_scaling must be armed by DEFAULT (sp-5nd2, nil live config)")
	}
	// Live kill-switch: the *_disabled tune stands it down next tick (no restart).
	if d := resolveBootstrapConfig(baseCmd(), liveconfig.Snapshot{"autosizer_early_scaling_disabled": 1}); d.AutosizerEarlyScaling {
		t.Fatalf("autosizer_early_scaling_disabled=1 must stand the feature down")
	}
	// Force-on override still arms (backward compat with the original positive knob).
	if f := resolveBootstrapConfig(baseCmd(), liveconfig.Snapshot{"autosizer_early_scaling": 1}); !f.AutosizerEarlyScaling {
		t.Fatalf("autosizer_early_scaling=1 must force-arm")
	}
	// Disable WINS over force-on — the kill-switch is the safety.
	if both := resolveBootstrapConfig(baseCmd(), liveconfig.Snapshot{"autosizer_early_scaling": 1, "autosizer_early_scaling_disabled": 1}); both.AutosizerEarlyScaling {
		t.Fatalf("the disable kill-switch must WIN over the force-on override")
	}
}
