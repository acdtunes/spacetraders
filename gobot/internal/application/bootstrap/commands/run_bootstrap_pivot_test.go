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

// pivotObs is a cold-start cold-start observation primed for the pivot: 0 haulers, NO idle purchaser (the
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
	obs.TradeHullCount = 1                  // Post-seed — the subsequent-hauler path, not the trade-seed
	h := pivotHandler(obs, ret, acq, loop)

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if loop.stopCalls != 0 || len(ret.dedications) != 0 {
		t.Fatalf("a subsequent hauler must NOT pivot the frigate; stopCalls=%d dedications=%v", loop.stopCalls, ret.dedications)
	}
	if res.Blocker != "no_purchaser" {
		t.Fatalf("subsequent hauler, no idle hull, no pivot ⇒ no_purchaser, got %q", res.Blocker)
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

// --- sp-5nd2 fault-2: cold-price positioning (free the frigate → position it at the yard → buy) ---
//
// The live deadlock: the pivot is warranted (0 haulers, frigate on its loop, cargo empty) but the LIGHT_SHUTTLE
// price is UNREADABLE because the SpaceTraders shipyard listing is presence-gated and nothing is at the yard
// (frigate on its loop, probes scouting). The pre-fix pivot price-checks BEFORE positioning, so it failed
// closed forever (price_unreadable). The fix FREES the frigate at the inter-contract window and POSITIONS it
// at the shipyard so the next tick's read succeeds and the buy runs behind the working-capital floor.

// pivotHandlerScanned wires the pivot handler AND the shipyard scanner (the fault-2 positioner).
func pivotHandlerScanned(obs Observation, ret *fakeRetirer, acq *fakeHaulerAcquirer, loop *fakeFrigateLoop, scanner *fakeScanner) *RunBootstrapCoordinatorHandler {
	h := pivotHandler(obs, ret, acq, loop)
	h.SetShipyardScanner(scanner)
	return h
}

// COLD PRICE (the deadlock): pivot warranted + price unreadable → FREE the frigate (stop loop + dedicate) and
// SEND it to the home shipyard. No buy this tick (fail closed on the price guard, RULINGS #4).
func TestBootstrap_Pivot_ColdPrice_FreesFrigateAndPositionsAtShipyard(t *testing.T) {
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: false} // presence-gated: unreadable
	loop := &fakeFrigateLoop{}
	scanner := &fakeScanner{dispatched: true}
	h := pivotHandlerScanned(pivotObs(), ret, acq, loop, scanner)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if loop.stopCalls != 1 || len(loop.stopped) != 1 || loop.stopped[0] != "FRIGATE-1" {
		t.Fatalf("cold price must FREE the frigate (stop its loop) to send it; stopCalls=%d stopped=%v (blocker=%q)", loop.stopCalls, loop.stopped, res.Blocker)
	}
	if len(ret.dedications) != 1 || ret.dedications[0] != "FRIGATE-1" {
		t.Fatalf("cold price must dedicate the frigate as the purchasing ship before sending it; dedications=%v", ret.dedications)
	}
	if scanner.calls != 1 || len(scanner.purchasers) != 1 || scanner.purchasers[0] != "FRIGATE-1" {
		t.Fatalf("cold price must send the freed frigate to the home shipyard BY SYMBOL; calls=%d purchasers=%v", scanner.calls, scanner.purchasers)
	}
	if acq.buys != 0 {
		t.Fatalf("cold price must NOT buy this tick (fail closed on the price guard); buys=%d", acq.buys)
	}
	if res.Blocker != "positioning_purchaser_at_shipyard" {
		t.Fatalf("expected positioning_purchaser_at_shipyard, got %q", res.Blocker)
	}
	if !res.FrigatePivoted {
		t.Fatalf("res.FrigatePivoted must be true (the pivot's free step fired)")
	}
}

// COMPLETION: once the freed frigate is dedicated (committed purchaser) and standing at the yard so the price
// reads, the buy runs WITH the frigate as the purchaser and WITHOUT re-stopping a loop (already stopped) or
// consulting the scanner again.
func TestBootstrap_Pivot_ColdPrice_CommittedPurchaserBuysOncePriceReads(t *testing.T) {
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true} // frigate now at the yard → readable
	loop := &fakeFrigateLoop{}
	scanner := &fakeScanner{dispatched: true}
	obs := pivotObs()
	obs.FrigateContractLoopRunning = false // loop already stopped on the prior (free) tick
	obs.CommandFrigatePurchasing = true    // already dedicated as the exclusive purchasing ship
	obs.HasIdlePurchaser = true            // the frigate now stands idle at the yard
	h := pivotHandlerScanned(obs, ret, acq, loop, scanner)

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if loop.stopCalls != 0 || scanner.calls != 0 {
		t.Fatalf("a committed purchaser at a readable yard must NOT re-stop or move again; stopCalls=%d scanner calls=%d", loop.stopCalls, scanner.calls)
	}
	if acq.buys != 1 || len(acq.purchasers) != 1 || acq.purchasers[0] != "FRIGATE-1" {
		t.Fatalf("the committed purchaser must buy hauler #1 with the frigate; buys=%d purchasers=%v (blocker=%q)", acq.buys, acq.purchasers, res.Blocker)
	}
	if res.HaulersBought != 1 {
		t.Fatalf("res.HaulersBought must be 1")
	}
}

// SAFE POINT under a cold price: a frigate carrying contract cargo is NOT freed even when the price is
// unreadable — stopping mid-delivery loses cargo. Block no_purchaser BEFORE the price-check; nothing stops
// and nothing moves.
func TestBootstrap_Pivot_ColdPrice_LoadedFrigate_NotFreed(t *testing.T) {
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: false}
	loop := &fakeFrigateLoop{}
	scanner := &fakeScanner{dispatched: true}
	obs := pivotObs()
	obs.FrigateCargoEmpty = false // mid-delivery: not a safe point
	h := pivotHandlerScanned(obs, ret, acq, loop, scanner)

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if loop.stopCalls != 0 || len(ret.dedications) != 0 || scanner.calls != 0 {
		t.Fatalf("a loaded frigate must NOT be freed or sent anywhere (no cargo loss); stopCalls=%d dedications=%v scanner calls=%d", loop.stopCalls, ret.dedications, scanner.calls)
	}
	if acq.priceChks != 0 {
		t.Fatalf("no_purchaser must block BEFORE the price-check; priceChks=%d", acq.priceChks)
	}
	if res.Blocker != "no_purchaser" {
		t.Fatalf("expected no_purchaser, got %q", res.Blocker)
	}
}

// IDEMPOTENCY: a committed purchaser still en route to (or already standing at) the yard is NEVER sent
// again — the scanner reports nothing dispatched, and the tick still reads as positioning, because the
// hull that will do the buy is on its way. Reading it as a plain unreadable price would hide the pivot's
// progress on the heartbeat.
func TestBootstrap_Pivot_ColdPrice_CommittedPurchaserEnRoute_WaitsWithoutReSending(t *testing.T) {
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: false} // still cold: the frigate has not arrived
	loop := &fakeFrigateLoop{}
	scanner := &fakeScanner{dispatched: false} // already at/heading to the yard → nothing to send
	obs := pivotObs()
	obs.FrigateContractLoopRunning = false // freed on the prior tick
	obs.CommandFrigatePurchasing = true    // already the committed purchaser
	h := pivotHandlerScanned(obs, ret, acq, loop, scanner)

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if loop.stopCalls != 0 || len(ret.dedications) != 0 {
		t.Fatalf("a committed purchaser must NOT be freed again; stopCalls=%d dedications=%v", loop.stopCalls, ret.dedications)
	}
	if scanner.calls != 1 || scanner.purchasers[0] != "FRIGATE-1" {
		t.Fatalf("the committed purchaser must be the hull consulted on, by symbol; calls=%d purchasers=%v", scanner.calls, scanner.purchasers)
	}
	if acq.buys != 0 {
		t.Fatalf("an unreadable price must buy nothing; buys=%d", acq.buys)
	}
	if res.Blocker != "positioning_purchaser_at_shipyard" {
		t.Fatalf("a purchaser already on its way is still positioning, not a bare unreadable price; got %q", res.Blocker)
	}
}

// NIL-SAFE: pivot warranted + price unreadable but NO shipyard scanner wired → fail closed
// (price_unreadable) and NEVER stop the frigate — an earning loop is never halted for a trip that cannot
// be made.
func TestBootstrap_Pivot_ColdPrice_NoScanner_FailsClosedNoFree(t *testing.T) {
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: false}
	loop := &fakeFrigateLoop{}
	h := pivotHandler(pivotObs(), ret, acq, loop) // NO scanner wired
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if loop.stopCalls != 0 || len(ret.dedications) != 0 {
		t.Fatalf("no scanner: must NOT free a frigate it cannot send; stopCalls=%d dedications=%v", loop.stopCalls, ret.dedications)
	}
	if acq.buys != 0 || res.Blocker != "price_unreadable" {
		t.Fatalf("no scanner: fail closed price_unreadable, no buy; buys=%d blocker=%q", acq.buys, res.Blocker)
	}
}

// END-TO-END (anti-theatre): the cold-start deadlock → cure across ticks. Tick 1: pivot warranted, price
// unreadable → free the frigate + send it (the scanner's dispatch models the frigate reaching the yard,
// flipping the price readable + marking it idle). Tick 2: price reads → the buy executes with the frigate.
func TestBootstrap_Pivot_ColdPrice_EndToEnd_DeadlockToBuy(t *testing.T) {
	world := &incomeWorld{
		treasury: 2000000, homeSystem: "X1", marketsTotal: 10, marketsCovered: 10,
		frigateID: "FRIGATE-1", probeCount: 3, batchRunning: true,
		frigateLoopRunning: true, frigateCargoEmpty: true, hasPurchaser: false,
		placementSlots: incomeSlots(),
	}
	acq := &fakeHaulerAcquirer{price: 300000, yard: "X1-YARD", readable: false, world: world}
	ret := &fakeRetirer{world: world}
	loop := &fakeFrigateLoop{world: world}
	scanner := &fakeScanner{dispatched: true, readyHaul: acq, world: world}

	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(&fakeIncomeObserver{world: world})
	h.SetProbeAcquirer(&fakeAcquirer{price: 40000, yard: "Y", readable: true})
	h.SetScoutPostDeclarer(&fakeDeclarer{})
	h.SetFrigateRetirer(ret)
	h.SetHaulerAcquirer(acq)
	h.SetContractRunner(&fakeContractRunner{world: world})
	h.SetFrigateContractLoopStarter(loop)
	h.SetShipyardScanner(scanner)

	// Tick 1: cold price → free + send, buy nothing.
	res1, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if loop.stopCalls != 1 || scanner.calls != 1 || acq.buys != 0 {
		t.Fatalf("tick 1 must free + send the frigate and buy nothing; stopCalls=%d scanner calls=%d buys=%d blocker=%q", loop.stopCalls, scanner.calls, acq.buys, res1.Blocker)
	}

	// Tick 2: the frigate is at the yard (price readable, idle) → the buy executes with the frigate.
	res2, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if acq.buys != 1 || len(acq.purchasers) != 1 || acq.purchasers[0] != "FRIGATE-1" {
		t.Fatalf("tick 2 must buy hauler #1 with the freed frigate; buys=%d purchasers=%v blocker=%q", acq.buys, acq.purchasers, res2.Blocker)
	}
	if len(world.haulers) != 1 {
		t.Fatalf("the world must reflect one hauler bought; haulers=%d", len(world.haulers))
	}
}
