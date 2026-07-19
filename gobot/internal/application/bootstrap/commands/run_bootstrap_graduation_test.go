package commands

import (
	"testing"
)

// sp-difa.1 — the durable contract-graduation gate on bootstrap's INCOME workstream (the SECONDARY
// re-spawner: a gate-built fleet whose realized income is still below the bar derives INCOME, where
// actIncome would (re)start batch-contract / the frigate sole-earner loop / staged hauler buys). When a
// player is graduated, actIncome must do NOTHING — no contract earner is started or maintained, durably
// across restarts — while DATA scanning and GATE construction (and trade) run untouched.

// graduationIncomeHandler wires an INCOME-phase handler with every contract collaborator, so a
// non-empty contract action would fire if the phase were not gated.
func graduationIncomeHandler(obs Observation, ret *fakeRetirer, acq *fakeHaulerAcquirer, run *fakeContractRunner, loop *fakeFrigateLoop) *RunBootstrapCoordinatorHandler {
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(&fakeObserver{obs: obs})
	h.SetProbeAcquirer(&fakeAcquirer{price: 40000, yard: "Y", readable: true})
	h.SetScoutAssigner(&fakeScouter{})
	h.SetFrigateRetirer(ret)
	h.SetHaulerAcquirer(acq)
	h.SetContractRunner(run)
	h.SetFrigateContractLoopStarter(loop)
	return h
}

// graduationIncomeObs is an INCOME-phase observation primed so ALL four contract actions would fire:
// a tagged frigate to retire, no batch-contract running, probes provisioned + no frigate loop, and an
// unserved hub with an idle purchaser + treasury for a hauler buy.
func graduationIncomeObs() Observation {
	obs := incomeObs() // coverage met, income 0 < bar → INCOME
	obs.CommandFrigateID = "FRIGATE-1"
	obs.CommandFrigateOnContract = true // would retire
	obs.ProbeCount = 3                   // >= default probe_target → frigate loop eligible
	obs.FrigateContractLoopRunning = false
	obs.BatchContractRunning = false // would launch batch-contract
	// Haulers empty + hubs present + idle purchaser + treasury → would buy a hauler.
	return obs
}

// GRADUATED: actIncome starts/maintains NO contract earner — no retire, no batch-contract, no frigate
// loop, no hauler buy — and the tick surfaces the graduated state. This is the durable fix for a
// boot-standing bootstrap re-establishing contracts on a graduated fleet.
func TestBootstrap_Income_ContractGraduated_NoContractActions(t *testing.T) {
	obs := graduationIncomeObs()
	obs.ContractGraduated = true
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}
	run := &fakeContractRunner{}
	loop := &fakeFrigateLoop{}
	h := graduationIncomeHandler(obs, ret, acq, run, loop)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if res.Phase != PhaseIncome {
		t.Fatalf("expected INCOME phase, got %s", res.Phase)
	}
	if ret.calls != 0 || run.calls != 0 || loop.calls != 0 || acq.buys != 0 {
		t.Fatalf("graduated: NO contract action may fire — retire=%d batch=%d frigate_loop=%d hauler_buys=%d", ret.calls, run.calls, loop.calls, acq.buys)
	}
	if res.ContractRun || res.FrigateLoopStarted || res.HaulersBought != 0 || res.FrigateRetired {
		t.Fatalf("graduated: reconcileResult must show no contract effect, got %+v", res)
	}
	if res.Blocker != "contract_graduated" {
		t.Fatalf("graduated: expected blocker=contract_graduated for heartbeat visibility, got %q", res.Blocker)
	}
}

// NOT GRADUATED (baseline / byte-identical): the same observation runs the full INCOME workstream —
// proving the graduation flag is exactly what suppresses it. All four contract actions fire.
func TestBootstrap_Income_NotGraduated_RunsContractsAsToday(t *testing.T) {
	obs := graduationIncomeObs()
	obs.ContractGraduated = false
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}
	run := &fakeContractRunner{}
	loop := &fakeFrigateLoop{}
	h := graduationIncomeHandler(obs, ret, acq, run, loop)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if ret.calls != 1 {
		t.Fatalf("un-graduated: the tagged frigate is retired, got calls=%d", ret.calls)
	}
	if run.calls != 1 || !res.ContractRun {
		t.Fatalf("un-graduated: batch-contract launches (the funding floor), got calls=%d ran=%v", run.calls, res.ContractRun)
	}
	if loop.calls != 1 || !res.FrigateLoopStarted {
		t.Fatalf("un-graduated: the frigate sole-earner loop starts, got calls=%d started=%v", loop.calls, res.FrigateLoopStarted)
	}
	if acq.buys != 1 {
		t.Fatalf("un-graduated: a contract hauler is bought, got buys=%d", acq.buys)
	}
}
