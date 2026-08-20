package commands

import (
	"testing"
)

// sp-difa.1 — the durable contract-graduation gate on bootstrap's contract workstream (the SECONDARY
// re-spawner: a gate-built fleet whose realized income is still below the bar derives COLDSTART, where
// actIncome would (re)start batch-contract / the frigate sole-earner loop / staged hauler buys). When a
// player is graduated, actIncome must do NOTHING — no contract earner is started or maintained, durably
// across restarts — while scanning and GATE construction (and trade) run untouched.

// graduationIncomeHandler wires a cold-start handler with every contract collaborator, so a
// non-empty contract action would fire if the phase were not gated.
func graduationIncomeHandler(obs Observation, ret *fakeRetirer, acq *fakeHaulerAcquirer, run *fakeContractRunner, loop *fakeFrigateLoop) *RunBootstrapCoordinatorHandler {
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(&fakeObserver{obs: obs})
	h.SetProbeAcquirer(&fakeAcquirer{price: 40000, yard: "Y", readable: true})
	h.SetFrigateRetirer(ret)
	h.SetHaulerAcquirer(acq)
	h.SetContractRunner(run)
	h.SetFrigateContractLoopStarter(loop)
	return h
}

// graduationIncomeObs is a cold-start observation primed so ALL four contract actions would fire:
// a tagged frigate to retire, no batch-contract running, probes provisioned + no frigate loop, and an
// unserved hub with an idle purchaser + treasury for a hauler buy.
func graduationIncomeObs() Observation {
	obs := incomeObs() // coverage met, no economic signal → COLDSTART
	obs.CommandFrigateID = "FRIGATE-1"
	obs.CommandFrigateOnTrade = true // idle in trade → the first-hauler pivot would fire
	obs.CommandFrigateIdle = true
	obs.FrigateCargoEmpty = true
	obs.ProbeCount = 3
	obs.BatchContractRunning = false // would launch the contract-fleet coordinator
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
	if res.Phase != PhaseColdStart {
		t.Fatalf("expected COLDSTART phase, got %s", res.Phase)
	}
	if len(ret.dedications) != 0 || run.calls != 0 || loop.stopCalls != 0 || acq.buys != 0 {
		t.Fatalf("graduated: NO contract action may fire — pivot=%v batch=%d loop_stops=%d hauler_buys=%d", ret.dedications, run.calls, loop.stopCalls, acq.buys)
	}
	if res.ContractRun || res.FrigatePivoted || res.HaulersBought != 0 || res.FrigateTrading {
		t.Fatalf("graduated: reconcileResult must show no contract effect, got %+v", res)
	}
	if res.Blocker != "contract_graduated" {
		t.Fatalf("graduated: expected blocker=contract_graduated for heartbeat visibility, got %q", res.Blocker)
	}
}

// GRADUATED, the TRADE side: graduation retires CONTRACTS, not the command hull. The frigate is still
// put in the trade fleet and the trade coordinator still ensured, so a graduated fleet's command hull
// earns from tick 1 instead of sitting idle — the same standing home every other cold-start tick gives it.
func TestBootstrap_Income_ContractGraduated_FrigateStillTradesAndIsToured(t *testing.T) {
	obs := graduationIncomeObs()
	obs.ContractGraduated = true
	obs.CommandFrigateOnTrade = false // fresh era / a stale tag: the trade dedication has not landed yet
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}
	run := &fakeContractRunner{}
	ho := &fakeHandoff{}
	h := graduationIncomeHandler(obs, ret, acq, run, &fakeFrigateLoop{})
	h.SetHandoffLauncher(ho)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if len(ret.tradeDedications) != 1 || ret.tradeDedications[0] != "FRIGATE-1" || !res.FrigateTrading {
		t.Fatalf("graduated: the frigate must still be dedicated TRADE; trade=%v FrigateTrading=%v", ret.tradeDedications, res.FrigateTrading)
	}
	if ho.tradeCoord < 1 {
		t.Fatalf("graduated: the trade-fleet coordinator must still be ensured so the frigate actually tours, got %d", ho.tradeCoord)
	}
	// The CONTRACT workstream is still fully off, and the heartbeat still says why.
	if run.calls != 0 || acq.buys != 0 || len(ret.dedications) != 0 {
		t.Fatalf("graduated: no contract action may fire — batch=%d buys=%d pivot=%v", run.calls, acq.buys, ret.dedications)
	}
	if res.Blocker != "contract_graduated" {
		t.Fatalf("graduated: expected blocker=contract_graduated, got %q", res.Blocker)
	}
}

// NOT GRADUATED (baseline / byte-identical): the same observation runs the full contract workstream —
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
	if run.calls != 1 || !res.ContractRun {
		t.Fatalf("un-graduated: the contract-fleet coordinator launches (the funding floor), got calls=%d ran=%v", run.calls, res.ContractRun)
	}
	if !res.FrigatePivoted || len(ret.dedications) != 1 {
		t.Fatalf("un-graduated: the idle-in-trade frigate pivots to buy hauler #1, got pivoted=%v dedications=%v", res.FrigatePivoted, ret.dedications)
	}
	if acq.buys != 1 {
		t.Fatalf("un-graduated: a contract hauler is bought, got buys=%d", acq.buys)
	}
}
