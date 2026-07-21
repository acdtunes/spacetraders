package commands

import (
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
)

// The DEFAULT-OFF arm for the dedicated contract auto-scaler. One positive tunable-only flag
// (contract_scaler_early_scaling) launches the standing scaler EARLY in the DATA/INCOME window so it
// ramps the exclusive contract fleet behind the 200000 cushion. Default OFF is byte-identical: nothing
// launches the scaler until the flag is armed after validation.

// contractScalerIncomeObs is an INCOME-phase observation (haulers at desired, autosizer running so the
// autosizer arm — silenced in these tests — has nothing to do) with the contract-scaler running-state
// set by the caller.
func contractScalerIncomeObs(scalerRunning bool) Observation {
	o := sjvvIncomeObs(true, 3) // 3 haulers = desired → no hauler buy; autosizer "running" → no autosizer launch
	o.ContractScalerRunning = scalerRunning
	return o
}

// armedScalerOnly arms the contract-scaler flag and DISABLES the (default-on) autosizer arm, so a single
// tick exercises ONLY the contract-scaler launch — no autosizer launch, no hauler arbitration noise.
func armedScalerOnly() *fakeLiveConfig {
	return &fakeLiveConfig{snap: liveconfig.Snapshot{
		"contract_scaler_early_scaling":    1,
		"autosizer_early_scaling_disabled": 1,
	}}
}

// --- config resolution: DEFAULT-OFF, positive-tune-armable (mirrors defer_probe_to_freshsizer) ---

func TestBootstrap_ContractScalerEarlyScaling_DefaultOffAndArmable(t *testing.T) {
	// The tunable-defaults mirror carries the force-off default (0), like every positive tunable-only flag.
	if BootstrapTunableDefaults()["contract_scaler_early_scaling"] != 0 {
		t.Fatalf("contract_scaler_early_scaling default must be 0 (OFF) — the shipwright arms it")
	}
	// DEFAULT-OFF: nil live config (a bare cold start) leaves the arm off — byte-identical.
	if on := resolveBootstrapConfig(baseCmd(), nil); on.ContractScalerEarlyScaling {
		t.Fatalf("contract_scaler_early_scaling must be OFF by default (nil live config)")
	}
	// Armable live via the positive tune, no restart.
	if a := resolveBootstrapConfig(baseCmd(), liveconfig.Snapshot{"contract_scaler_early_scaling": 1}); !a.ContractScalerEarlyScaling {
		t.Fatalf("contract_scaler_early_scaling=1 must arm the contract scaler")
	}
	// An unrelated tune never arms it (no accidental arming).
	if o := resolveBootstrapConfig(baseCmd(), liveconfig.Snapshot{"probe_target": 5}); o.ContractScalerEarlyScaling {
		t.Fatalf("an unrelated tune must not arm the contract scaler")
	}
}

// --- early launch: armed in the DATA/INCOME window, idempotent, off by default, not in GATE ---

// ARMED in INCOME with the scaler down: bootstrap launches it once and surfaces the flag.
func TestBootstrap_ContractScaler_ArmedInIncome_Launches(t *testing.T) {
	ho := &fakeHandoff{}
	h := sjvvHandler(contractScalerIncomeObs(false), armedScalerOnly(), ho, &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true})

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if ho.contractScaler != 1 || !res.ContractScalerLaunchedEarly {
		t.Fatalf("armed + INCOME + scaler down: must launch the contract scaler once (launches=%d early=%v)", ho.contractScaler, res.ContractScalerLaunchedEarly)
	}
	// The contract-scaler arm must NOT drag in the autosizer or the standing coordinators.
	if ho.autosizer != 0 || ho.standing != 0 {
		t.Fatalf("the contract-scaler arm must launch ONLY the scaler (autosizer=%d standing=%d)", ho.autosizer, ho.standing)
	}
}

// DEFAULT-OFF (no arm): the scaler is NEVER launched in the scaling window — byte-identical to today.
func TestBootstrap_ContractScaler_DefaultOff_NeverLaunches(t *testing.T) {
	ho := &fakeHandoff{}
	// Silence the default-on autosizer arm too, so the ONLY variable under test is the (absent) scaler arm.
	off := &fakeLiveConfig{snap: liveconfig.Snapshot{"autosizer_early_scaling_disabled": 1}}
	h := sjvvHandler(contractScalerIncomeObs(false), off, ho, &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true})

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if ho.contractScaler != 0 || res.ContractScalerLaunchedEarly {
		t.Fatalf("default-off: the contract scaler must NEVER launch (launches=%d early=%v)", ho.contractScaler, res.ContractScalerLaunchedEarly)
	}
}

// ARMED but the scaler is already running: no relaunch (idempotent — armed once, runs forever).
func TestBootstrap_ContractScaler_AlreadyRunning_NoRelaunch(t *testing.T) {
	ho := &fakeHandoff{}
	h := sjvvHandler(contractScalerIncomeObs(true), armedScalerOnly(), ho, &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true})

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if ho.contractScaler != 0 || res.ContractScalerLaunchedEarly {
		t.Fatalf("armed + scaler already running: must NOT relaunch (launches=%d early=%v)", ho.contractScaler, res.ContractScalerLaunchedEarly)
	}
}

// ARMED but in GATE: the early launch is scoped to DATA/INCOME (GATE repurposes haulers to construction),
// so the scaler is NOT launched early during GATE.
func TestBootstrap_ContractScaler_NotLaunchedDuringGate(t *testing.T) {
	ho := &fakeHandoff{}
	obs := gateObs() // GATE phase; ContractScalerRunning=false by default
	h := gateHandler(obs, &fakeConstruction{}, &fakeManufacturing{}, &fakeRepurposer{}, &fakeGateAcquirer{}, ho)
	h.SetLiveConfigReader(armedScalerOnly())

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if res.Phase != PhaseGate {
		t.Fatalf("expected GATE phase, got %s", res.Phase)
	}
	if ho.contractScaler != 0 || res.ContractScalerLaunchedEarly {
		t.Fatalf("armed but GATE: the contract scaler must NOT be launched early (launches=%d early=%v)", ho.contractScaler, res.ContractScalerLaunchedEarly)
	}
}

// ARMED + scaler down but the launch FAILS: bootstrap surfaces nothing terminal (background launch),
// leaves res.ContractScalerLaunchedEarly false, and never claims the blocker (its own ERROR line only).
func TestBootstrap_ContractScaler_LaunchError_IsBackground(t *testing.T) {
	ho := &fakeHandoff{scalerErr: errors.New("boom")}
	h := sjvvHandler(contractScalerIncomeObs(false), armedScalerOnly(), ho, &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true})

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if ho.contractScaler != 1 {
		t.Fatalf("a launch attempt must still be made (launches=%d)", ho.contractScaler)
	}
	if res.ContractScalerLaunchedEarly {
		t.Fatalf("a FAILED launch must not report ContractScalerLaunchedEarly")
	}
	if res.Blocker == "contract_scaler_launch_error" {
		t.Fatalf("the background launch must NOT claim the heartbeat blocker")
	}
}
