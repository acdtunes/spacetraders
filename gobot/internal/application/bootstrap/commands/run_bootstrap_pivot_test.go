package commands

import (
	"errors"
	"testing"
)

// sp-7r7w — the FIRST-HAULER PIVOT. On cold start every hull is deliberately working (the command
// frigate on its sp-rype sole-earner loop; every probe claimed by the scout coordinator), so there is
// no idle hull to execute the first contract-hauler buy — the ktio/py5r no_purchaser stall. The pivot
// completes the behavior the design already documents (BatchContractWorkflow: "stops the returned
// container at the first-hauler pivot"): once the first hauler is affordable at acv5's cushion, STOP the
// frigate's contract loop (freeing it to idle), dedicate it the EXCLUSIVE purchasing ship, and buy
// hauler #1 with it. NO money guard changes — it rides acv5's existing working-capital cushion.

// pivotObs is a cold-start INCOME observation primed for the pivot: 0 haulers, NO idle purchaser (the
// real cold start), the frigate on its loop, cargo empty (the safe point), affordable (treasury ≫
// price + floor), viable hubs present.
func pivotObs() Observation {
	obs := incomeObs()
	obs.CommandFrigateID = "FRIGATE-1"
	obs.HasIdlePurchaser = false
	obs.FrigateContractLoopRunning = true
	obs.FrigateCargoEmpty = true
	obs.BatchContractRunning = true // isolate: don't also launch the coordinator
	obs.ProbeCount = 3
	return obs
}

func pivotHandler(obs Observation, ret *fakeRetirer, acq *fakeHaulerAcquirer, loop *fakeFrigateLoop) *RunBootstrapCoordinatorHandler {
	h := newIncomeHandler(obs, ret, acq, &fakeContractRunner{})
	h.SetFrigateContractLoopStarter(loop)
	return h
}

// Happy path: the pivot STOPS the frigate loop, DEDICATES the frigate as the exclusive purchasing ship,
// and buys hauler #1 WITH the frigate as the purchaser — all at acv5's cushion, no guard change.
func TestBootstrap_Pivot_FirstHauler_StopsLoopDedicatesBuysWithFrigate(t *testing.T) {
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}
	loop := &fakeFrigateLoop{}
	h := pivotHandler(pivotObs(), ret, acq, loop)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if loop.stopCalls != 1 || len(loop.stopped) != 1 || loop.stopped[0] != "FRIGATE-1" {
		t.Fatalf("pivot must STOP the frigate loop by symbol; stopCalls=%d stopped=%v (blocker=%q)", loop.stopCalls, loop.stopped, res.Blocker)
	}
	if len(ret.dedications) != 1 || ret.dedications[0] != "FRIGATE-1" {
		t.Fatalf("pivot must dedicate the frigate as the exclusive purchasing ship; dedications=%v", ret.dedications)
	}
	if acq.buys != 1 || len(acq.purchasers) != 1 || acq.purchasers[0] != "FRIGATE-1" {
		t.Fatalf("pivot must buy hauler #1 with the frigate as the purchaser; buys=%d purchasers=%v", acq.buys, acq.purchasers)
	}
	if !res.FrigatePivoted || res.HaulersBought != 1 {
		t.Fatalf("res must record the pivot + the buy; FrigatePivoted=%v HaulersBought=%d", res.FrigatePivoted, res.HaulersBought)
	}
}

// The frigate is THE first-hauler buyer even when a stray hull is idle: the pivot still fires (the
// exclusive purchasing ship must be established), stopping the loop and buying with the frigate.
func TestBootstrap_Pivot_FiresEvenWithAnIdlePurchaser(t *testing.T) {
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}
	loop := &fakeFrigateLoop{}
	obs := pivotObs()
	obs.HasIdlePurchaser = true // a stray idle hull exists — the pivot still fires
	h := pivotHandler(obs, ret, acq, loop)

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if loop.stopCalls != 1 || acq.purchasers == nil || acq.purchasers[0] != "FRIGATE-1" {
		t.Fatalf("the frigate must be the first-hauler buyer regardless of a stray idle hull; stopCalls=%d purchasers=%v (blocker=%q)", loop.stopCalls, acq.purchasers, res.Blocker)
	}
	if !res.FrigatePivoted {
		t.Fatalf("res.FrigatePivoted must be true")
	}
}

// SAFE POINT: a frigate carrying contract cargo is NOT pivoted (stopping mid-delivery loses cargo) — the
// buy waits (no_purchaser) and retries next tick once the loop delivers and the frigate empties.
func TestBootstrap_Pivot_LoadedFrigate_DefersNoCargoLoss(t *testing.T) {
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}
	loop := &fakeFrigateLoop{}
	obs := pivotObs()
	obs.FrigateCargoEmpty = false // mid-delivery: not a safe point
	h := pivotHandler(obs, ret, acq, loop)

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if loop.stopCalls != 0 || len(ret.dedications) != 0 || acq.buys != 0 {
		t.Fatalf("a loaded frigate must NOT be pivoted (no cargo loss); stopCalls=%d dedications=%v buys=%d", loop.stopCalls, ret.dedications, acq.buys)
	}
	if res.Blocker != "no_purchaser" {
		t.Fatalf("a loaded frigate with no idle hull must BLOCK no_purchaser and retry, got %q", res.Blocker)
	}
}

// No pivot when the frigate is not on its loop (nothing to free) and no idle hull exists → no_purchaser,
// with NO shipyard price-check (blocks cheaply, the pre-sp-7r7w efficiency).
func TestBootstrap_Pivot_NoLoopNoIdle_BlocksNoPurchaserBeforePriceCheck(t *testing.T) {
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}
	loop := &fakeFrigateLoop{}
	obs := pivotObs()
	obs.FrigateContractLoopRunning = false // frigate not on a loop
	h := pivotHandler(obs, ret, acq, loop)

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if loop.stopCalls != 0 || acq.buys != 0 {
		t.Fatalf("no loop + no idle hull must not pivot or buy; stopCalls=%d buys=%d", loop.stopCalls, acq.buys)
	}
	if acq.priceChks != 0 {
		t.Fatalf("no_purchaser must block BEFORE the shipyard price-check, got priceChks=%d", acq.priceChks)
	}
	if res.Blocker != "no_purchaser" {
		t.Fatalf("expected no_purchaser, got %q", res.Blocker)
	}
}

// The pivot is scoped to the FIRST hauler: with one already owned, a subsequent buy does NOT pivot the
// frigate (subsequent scaling is the autosizer's job when armed; here, no idle hull ⇒ no_purchaser).
func TestBootstrap_Pivot_SubsequentHauler_DoesNotPivot(t *testing.T) {
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}
	loop := &fakeFrigateLoop{}
	obs := pivotObs()
	obs.Haulers = make([]HaulerSnapshot, 1) // one hauler already ⇒ not the first
	h := pivotHandler(obs, ret, acq, loop)

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if loop.stopCalls != 0 || len(ret.dedications) != 0 {
		t.Fatalf("a subsequent hauler must NOT pivot the frigate; stopCalls=%d dedications=%v", loop.stopCalls, ret.dedications)
	}
	if res.Blocker != "no_purchaser" {
		t.Fatalf("subsequent hauler, no idle hull, no pivot ⇒ no_purchaser, got %q", res.Blocker)
	}
}

// DRY-RUN evaluates the pivot but takes NO action: no loop stop, no dedication, no buy.
func TestBootstrap_Pivot_DryRun_NoAction(t *testing.T) {
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}
	loop := &fakeFrigateLoop{}
	h := pivotHandler(pivotObs(), ret, acq, loop)
	cmd := baseCmd()
	cmd.DryRun = true

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), cmd)
	if loop.stopCalls != 0 || len(ret.dedications) != 0 || acq.buys != 0 {
		t.Fatalf("dry-run must take no action; stopCalls=%d dedications=%v buys=%d", loop.stopCalls, ret.dedications, acq.buys)
	}
	if res.WouldBuy != 1 {
		t.Fatalf("dry-run must record WouldBuy=1, got %d", res.WouldBuy)
	}
}

// A StopLoop failure aborts the pivot cleanly: blocker surfaced, and NEITHER a dedication NOR a buy
// happens (the frigate is not dedicated/bought against a loop we could not free).
func TestBootstrap_Pivot_StopLoopFails_AbortsNoBuy(t *testing.T) {
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}
	loop := &fakeFrigateLoop{stopErr: errors.New("stop boom")}
	h := pivotHandler(pivotObs(), ret, acq, loop)

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.dedications) != 0 || acq.buys != 0 {
		t.Fatalf("a StopLoop failure must abort before dedicate/buy; dedications=%v buys=%d", ret.dedications, acq.buys)
	}
	if res.Blocker != "frigate_loop_stop_error" {
		t.Fatalf("expected frigate_loop_stop_error, got %q", res.Blocker)
	}
}

// The pre-hauler loop start is gated OFF once the frigate is the purchasing ship (pivot durable across
// restarts): even at 0 haulers with the loop not running, a purchasing-dedicated frigate is never put
// back on the loop.
func TestBootstrap_Pivot_LoopNeverRestartsOnPurchasingFrigate(t *testing.T) {
	obs := frigateLoopObs() // 0 haulers, provisioned, loop not running (would normally start)
	obs.CommandFrigatePurchasing = true
	loop := &fakeFrigateLoop{}
	h := pivotHandler(obs, &fakeRetirer{}, &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}, loop)

	if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if loop.calls != 0 {
		t.Fatalf("a purchasing-dedicated frigate must NEVER be put back on the pre-hauler loop, got calls=%d", loop.calls)
	}
}
