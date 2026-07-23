package commands

import (
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
)

// The dedicated contract auto-scaler is launched EARLY (unconditional, sp-1cbxz) during the DATA/INCOME
// window so it ramps the exclusive contract fleet behind the 200000 cushion.

// contractScalerIncomeObs is an INCOME-phase observation (haulers at desired, autosizer running so the early
// autosizer launch is an idempotent no-op) with the contract-scaler running-state set by the caller. The 3
// contract haulers put it in the POST-trade-seed state via sjvvIncomeObs (TradeHullCount=1), so the scaler
// DELAY-LAUNCH gate (sp-192k4) is open and these tests pin the launch/idempotency behavior as intended; the
// "held until the trade hull exists" half is covered by TestBootstrap_ContractScaler_HeldUntilTradeHullSeeded.
func contractScalerIncomeObs(scalerRunning bool) Observation {
	o := sjvvIncomeObs(true, 3) // 3 haulers = desired → no hauler buy; autosizer "running" → no autosizer launch
	o.ContractScalerRunning = scalerRunning
	return o
}

// --- early launch: in the DATA/INCOME window, idempotent, not in GATE ---

// In INCOME with the scaler down: bootstrap launches it once and surfaces the launch.
func TestBootstrap_ContractScaler_InIncome_Launches(t *testing.T) {
	ho := &fakeHandoff{}
	h := sjvvHandler(contractScalerIncomeObs(false), &fakeLiveConfig{snap: liveconfig.Snapshot{}}, ho, &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true})

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if ho.contractScaler != 1 || !res.ContractScalerLaunchedEarly {
		t.Fatalf("INCOME + scaler down: must launch the contract scaler once (launches=%d early=%v)", ho.contractScaler, res.ContractScalerLaunchedEarly)
	}
	// The contract-scaler launch must NOT drag in the standing coordinators; the autosizer is already running.
	if ho.autosizer != 0 || ho.standing != 0 {
		t.Fatalf("the contract-scaler launch must launch ONLY the scaler (autosizer=%d standing=%d)", ho.autosizer, ho.standing)
	}
}

// The scaler is already running: no relaunch (idempotent — launched once, runs forever).
func TestBootstrap_ContractScaler_AlreadyRunning_NoRelaunch(t *testing.T) {
	ho := &fakeHandoff{}
	h := sjvvHandler(contractScalerIncomeObs(true), &fakeLiveConfig{snap: liveconfig.Snapshot{}}, ho, &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true})

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if ho.contractScaler != 0 || res.ContractScalerLaunchedEarly {
		t.Fatalf("scaler already running: must NOT relaunch (launches=%d early=%v)", ho.contractScaler, res.ContractScalerLaunchedEarly)
	}
}

// In GATE: the early launch is scoped to DATA/INCOME (GATE repurposes haulers to construction), so the
// scaler is NOT launched early during GATE.
func TestBootstrap_ContractScaler_NotLaunchedDuringGate(t *testing.T) {
	ho := &fakeHandoff{}
	obs := gateObs() // GATE phase; ContractScalerRunning=false by default
	h := gateHandler(obs, &fakeConstruction{}, &fakeManufacturing{}, &fakeRepurposer{}, &fakeGateAcquirer{}, ho)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if res.Phase != PhaseGate {
		t.Fatalf("expected GATE phase, got %s", res.Phase)
	}
	if ho.contractScaler != 0 || res.ContractScalerLaunchedEarly {
		t.Fatalf("GATE: the contract scaler must NOT be launched early (launches=%d early=%v)", ho.contractScaler, res.ContractScalerLaunchedEarly)
	}
}

// Scaler down but the launch FAILS: bootstrap surfaces nothing terminal (background launch), leaves
// res.ContractScalerLaunchedEarly false, and never claims the blocker (its own ERROR line only).
func TestBootstrap_ContractScaler_LaunchError_IsBackground(t *testing.T) {
	ho := &fakeHandoff{scalerErr: errors.New("boom")}
	h := sjvvHandler(contractScalerIncomeObs(false), &fakeLiveConfig{snap: liveconfig.Snapshot{}}, ho, &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true})

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
