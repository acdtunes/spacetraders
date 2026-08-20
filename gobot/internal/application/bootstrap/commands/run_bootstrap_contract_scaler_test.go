package commands

import (
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
)

// The dedicated contract auto-scaler is ensured during the cold-start window, once the contract-start
// treasury gate passes, so it ramps the exclusive contract fleet behind the 200000 cushion. The
// once-only guarantee lives in the LAUNCHER, which skips a coordinator already RUNNING/PENDING —
// bootstrap holds no arbitration state. The gate itself is pinned in run_bootstrap_contract_start_test.go.

// contractScalerColdStartObs is a cold-start observation with the contract operation already under way
// (haulers at desired, autosizer running so the early autosizer launch is an idempotent no-op). The 3
// contract haulers put it in the POST-trade-seed state via sjvvColdStartObs (TradeHullCount=1).
func contractScalerColdStartObs() Observation {
	return sjvvColdStartObs(true, 3) // 3 haulers = desired → no hauler buy; autosizer "running" → no autosizer launch
}

// --- ensured in the cold-start window, not in GATE ---

// In cold start: bootstrap ensures the contract scaler and surfaces it, without dragging in anything else.
func TestBootstrap_ContractScaler_InColdStart_Ensured(t *testing.T) {
	ho := &fakeHandoff{}
	h := sjvvHandler(contractScalerColdStartObs(), &fakeLiveConfig{snap: liveconfig.Snapshot{}}, ho, &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true})

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if ho.contractScaler != 1 || !res.ContractScalerLaunchedEarly {
		t.Fatalf("cold start: must ensure the contract scaler (launches=%d early=%v)", ho.contractScaler, res.ContractScalerLaunchedEarly)
	}
	// The contract-scaler launch must NOT drag in the standing coordinators; the autosizer is already running.
	if ho.standing != 0 {
		t.Fatalf("the contract-scaler launch must launch ONLY the scaler (standing=%d)", ho.standing)
	}
}

// In GATE: the ensure is scoped to cold start (GATE repurposes haulers to construction), so the scaler is
// NOT ensured during GATE.
func TestBootstrap_ContractScaler_NotLaunchedDuringGate(t *testing.T) {
	ho := &fakeHandoff{}
	obs := gateObs() // GATE phase
	h := gateHandler(obs, &fakeConstruction{}, &fakeManufacturing{}, &fakeRepurposer{}, &fakeGateAcquirer{}, ho)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if res.Phase != PhaseGate {
		t.Fatalf("expected GATE phase, got %s", res.Phase)
	}
	if ho.contractScaler != 0 || res.ContractScalerLaunchedEarly {
		t.Fatalf("GATE: the contract scaler must NOT be ensured (launches=%d early=%v)", ho.contractScaler, res.ContractScalerLaunchedEarly)
	}
}

// The launch FAILS: bootstrap surfaces nothing terminal (background launch), leaves
// res.ContractScalerLaunchedEarly false, and never claims the blocker (its own ERROR line only).
func TestBootstrap_ContractScaler_LaunchError_IsBackground(t *testing.T) {
	ho := &fakeHandoff{scalerErr: errors.New("boom")}
	h := sjvvHandler(contractScalerColdStartObs(), &fakeLiveConfig{snap: liveconfig.Snapshot{}}, ho, &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true})

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
