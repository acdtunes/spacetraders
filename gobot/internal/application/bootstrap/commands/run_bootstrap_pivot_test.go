package commands

import (
	"errors"
	"testing"
)

// sp-7r7w — the FIRST-HAULER PIVOT. On cold start every hull is deliberately working (the command
// frigate touring for the trade-fleet coordinator; every probe claimed by the scout coordinator), so
// there is no idle hull to execute the first contract-hauler buy — the ktio/py5r no_purchaser stall.
// The pivot takes the command frigate at an honest IDLE-IN-TRADE tick (PLAYBOOK §9 — never reassign a
// hull mid-tour), dedicates it the EXCLUSIVE purchasing ship, and buys hauler #1 with it. NO money
// guard changes — it rides acv5's existing working-capital cushion. Nothing is stopped: an idle-in-trade
// frigate holds no claim, so re-tagging it is the whole hand-over.

// pivotObs is a cold-start observation primed for the pivot: 0 haulers, NO idle purchaser (the real cold
// start), the frigate idle in the trade fleet, cargo empty (the safe point), affordable (treasury ≫
// price + floor, and over the contract-start threshold), viable hubs present.
func pivotObs() Observation {
	obs := incomeObs()
	obs.CommandFrigateID = "FRIGATE-1"
	obs.HasIdlePurchaser = false
	obs.CommandFrigateOnTrade = true
	obs.CommandFrigateIdle = true
	obs.FrigateCargoEmpty = true
	obs.BatchContractRunning = true // isolate: don't also launch the coordinator
	obs.ProbeCount = 3
	return obs
}

func pivotHandler(obs Observation, ret *fakeRetirer, acq *fakeHaulerAcquirer, loop *fakeFrigateLoop) *RunBootstrapCoordinatorHandler {
	h := newIncomeHandler(obs, ret, acq, &fakeContractRunner{})
	h.SetFrigateContractLoopStarter(loop)
	h.SetHandoffLauncher(&fakeHandoff{})
	return h
}

// Happy path: the pivot DEDICATES the idle-in-trade frigate as the exclusive purchasing ship and buys
// hauler #1 WITH it — all at acv5's cushion, no guard change, and nothing is stopped or interrupted.
func TestBootstrap_Pivot_FirstHauler_DedicatesBuysWithFrigate(t *testing.T) {
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}
	loop := &fakeFrigateLoop{}
	h := pivotHandler(pivotObs(), ret, acq, loop)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if len(ret.dedications) != 1 || ret.dedications[0] != "FRIGATE-1" {
		t.Fatalf("pivot must dedicate the frigate as the exclusive purchasing ship; dedications=%v (blocker=%q)", ret.dedications, res.Blocker)
	}
	if loop.stopCalls != 0 {
		t.Fatalf("an idle-in-trade frigate holds no claim — the pivot must stop nothing, got %d stops", loop.stopCalls)
	}
	if acq.buys != 1 || len(acq.purchasers) != 1 || acq.purchasers[0] != "FRIGATE-1" {
		t.Fatalf("pivot must buy hauler #1 with the frigate as the purchaser; buys=%d purchasers=%v", acq.buys, acq.purchasers)
	}
	if !res.FrigatePivoted || res.HaulersBought != 1 {
		t.Fatalf("res must record the pivot + the buy; FrigatePivoted=%v HaulersBought=%d", res.FrigatePivoted, res.HaulersBought)
	}
}

// The frigate is THE first-hauler buyer even when a stray hull is idle: the pivot still fires (the
// exclusive purchasing ship must be established), buying with the frigate.
func TestBootstrap_Pivot_FiresEvenWithAnIdlePurchaser(t *testing.T) {
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}
	obs := pivotObs()
	obs.HasIdlePurchaser = true // a stray idle hull exists — the pivot still fires
	h := pivotHandler(obs, ret, acq, &fakeFrigateLoop{})

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.dedications) != 1 || acq.purchasers == nil || acq.purchasers[0] != "FRIGATE-1" {
		t.Fatalf("the frigate must be the first-hauler buyer regardless of a stray idle hull; dedications=%v purchasers=%v (blocker=%q)", ret.dedications, acq.purchasers, res.Blocker)
	}
	if !res.FrigatePivoted {
		t.Fatalf("res.FrigatePivoted must be true")
	}
}

// SAFE POINT: a frigate still holding cargo is NOT pivoted — the buy waits (no_purchaser) and retries
// next tick once the tour sells and the hull empties.
func TestBootstrap_Pivot_LoadedFrigate_DefersNoCargoLoss(t *testing.T) {
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}
	obs := pivotObs()
	obs.FrigateCargoEmpty = false // still holding goods: not a safe point
	h := pivotHandler(obs, ret, acq, &fakeFrigateLoop{})

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.dedications) != 0 || acq.buys != 0 {
		t.Fatalf("a loaded frigate must NOT be pivoted (no cargo loss); dedications=%v buys=%d", ret.dedications, acq.buys)
	}
	if res.Blocker != "no_purchaser" {
		t.Fatalf("a loaded frigate with no idle hull must BLOCK no_purchaser and retry, got %q", res.Blocker)
	}
}

// No pivot when the frigate is mid-tour (not an honest idle tick) and no idle hull exists → no_purchaser,
// with NO shipyard price-check (blocks cheaply, the previous efficiency).
func TestBootstrap_Pivot_MidTourNoIdle_BlocksNoPurchaserBeforePriceCheck(t *testing.T) {
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}
	obs := pivotObs()
	obs.CommandFrigateIdle = false // a tour is in flight
	h := pivotHandler(obs, ret, acq, &fakeFrigateLoop{})

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.dedications) != 0 || acq.buys != 0 {
		t.Fatalf("mid-tour + no idle hull must not pivot or buy; dedications=%v buys=%d", ret.dedications, acq.buys)
	}
	if acq.priceChks != 0 {
		t.Fatalf("no_purchaser must block BEFORE the shipyard price-check, got priceChks=%d", acq.priceChks)
	}
	if res.Blocker != "no_purchaser" {
		t.Fatalf("expected no_purchaser, got %q", res.Blocker)
	}
}

// The pivot is scoped to the FIRST hauler: with one already owned, a subsequent buy does NOT pivot the
// frigate (it stays touring; here, no idle hull ⇒ no_purchaser).
func TestBootstrap_Pivot_SubsequentHauler_DoesNotPivot(t *testing.T) {
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}
	obs := pivotObs()
	obs.Haulers = make([]HaulerSnapshot, 1) // one hauler already ⇒ not the first
	obs.TradeHullCount = 1                  // Post-seed — the subsequent-hauler path, not the trade-seed
	h := pivotHandler(obs, ret, acq, &fakeFrigateLoop{})

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.dedications) != 0 {
		t.Fatalf("a subsequent hauler must NOT pivot the frigate; dedications=%v", ret.dedications)
	}
	if res.Blocker != "no_purchaser" {
		t.Fatalf("subsequent hauler, no idle hull, no pivot ⇒ no_purchaser, got %q", res.Blocker)
	}
}

// A dedication failure aborts the pivot cleanly: blocker surfaced, and NO buy happens against a
// purchaser the fleet could not actually reserve.
func TestBootstrap_Pivot_DedicateFails_AbortsNoBuy(t *testing.T) {
	ret := &fakeRetirer{dedicateErr: errors.New("assign boom")}
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true}
	h := pivotHandler(pivotObs(), ret, acq, &fakeFrigateLoop{})

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if acq.buys != 0 {
		t.Fatalf("a dedication failure must abort before the buy; buys=%d", acq.buys)
	}
	if res.Blocker != "frigate_dedicate_error" {
		t.Fatalf("expected frigate_dedicate_error, got %q", res.Blocker)
	}
}

// --- sp-5nd2 fault-2: cold-price positioning (free the frigate → position it at the yard → buy) ---
//
// The live deadlock: the pivot is warranted (0 haulers, frigate idle in trade, cargo empty) but the
// LIGHT_SHUTTLE price is UNREADABLE because the SpaceTraders shipyard listing is presence-gated and nothing
// is at the yard. The previous pivot price-checks BEFORE positioning, so it failed closed forever
// (price_unreadable). The fix DEDICATES the frigate at the safe point and POSITIONS it at the shipyard so
// the next tick's read succeeds and the buy runs behind the working-capital floor.

// pivotHandlerScanned wires the pivot handler AND the shipyard scanner (the fault-2 positioner).
func pivotHandlerScanned(obs Observation, ret *fakeRetirer, acq *fakeHaulerAcquirer, loop *fakeFrigateLoop, scanner *fakeScanner) *RunBootstrapCoordinatorHandler {
	h := pivotHandler(obs, ret, acq, loop)
	h.SetShipyardScanner(scanner)
	return h
}

// COLD PRICE (the deadlock): pivot warranted + price unreadable → DEDICATE the frigate and SEND it to the
// home shipyard. No buy this tick (fail closed on the price guard, RULINGS #4).
func TestBootstrap_Pivot_ColdPrice_DedicatesFrigateAndPositionsAtShipyard(t *testing.T) {
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: false} // presence-gated: unreadable
	scanner := &fakeScanner{dispatched: true}
	h := pivotHandlerScanned(pivotObs(), ret, acq, &fakeFrigateLoop{}, scanner)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if len(ret.dedications) != 1 || ret.dedications[0] != "FRIGATE-1" {
		t.Fatalf("cold price must dedicate the frigate as the purchasing ship before sending it; dedications=%v (blocker=%q)", ret.dedications, res.Blocker)
	}
	if scanner.calls != 1 || len(scanner.purchasers) != 1 || scanner.purchasers[0] != "FRIGATE-1" {
		t.Fatalf("cold price must send the freed frigate to the home shipyard BY SYMBOL; calls=%d purchasers=%v", scanner.calls, scanner.purchasers)
	}
	// Committing the earner is THIS decision's to take, against the capital test above. The hauler path
	// therefore lends nothing — the frigate reaches the yard as the dedicated purchaser or not at all.
	if len(scanner.borrows) != 1 || scanner.borrows[0] != "" {
		t.Fatalf("the hauler path must never LEND the trading frigate behind the pivot's back; borrows=%v", scanner.borrows)
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
// reads, the buy runs WITH the frigate as the purchaser and WITHOUT re-dedicating it or consulting the
// scanner again.
func TestBootstrap_Pivot_ColdPrice_CommittedPurchaserBuysOncePriceReads(t *testing.T) {
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true} // frigate now at the yard → readable
	scanner := &fakeScanner{dispatched: true}
	obs := pivotObs()
	obs.CommandFrigateOnTrade = false   // re-tagged out of trade on the prior (pivot) tick
	obs.CommandFrigatePurchasing = true // already dedicated as the exclusive purchasing ship
	obs.HasIdlePurchaser = true         // the frigate now stands idle at the yard
	h := pivotHandlerScanned(obs, ret, acq, &fakeFrigateLoop{}, scanner)

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.dedications) != 0 || scanner.calls != 0 {
		t.Fatalf("a committed purchaser at a readable yard must NOT be re-dedicated or moved again; dedications=%v scanner calls=%d", ret.dedications, scanner.calls)
	}
	if acq.buys != 1 || len(acq.purchasers) != 1 || acq.purchasers[0] != "FRIGATE-1" {
		t.Fatalf("the committed purchaser must buy hauler #1 with the frigate; buys=%d purchasers=%v (blocker=%q)", acq.buys, acq.purchasers, res.Blocker)
	}
	if res.HaulersBought != 1 {
		t.Fatalf("res.HaulersBought must be 1")
	}
}

// SAFE POINT under a cold price: a frigate still holding cargo is NOT taken out of the trade fleet even
// when the price is unreadable. Block no_purchaser BEFORE the price-check; nothing is dedicated and
// nothing moves.
func TestBootstrap_Pivot_ColdPrice_LoadedFrigate_NotTaken(t *testing.T) {
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: false}
	scanner := &fakeScanner{dispatched: true}
	obs := pivotObs()
	obs.FrigateCargoEmpty = false // still loaded: not a safe point
	h := pivotHandlerScanned(obs, ret, acq, &fakeFrigateLoop{}, scanner)

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.dedications) != 0 || scanner.calls != 0 {
		t.Fatalf("a loaded frigate must NOT be taken or sent anywhere; dedications=%v scanner calls=%d", ret.dedications, scanner.calls)
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
// hull that will do the buy is on its way.
func TestBootstrap_Pivot_ColdPrice_CommittedPurchaserEnRoute_WaitsWithoutReSending(t *testing.T) {
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: false} // still cold: the frigate has not arrived
	scanner := &fakeScanner{dispatched: false}                            // already at/heading to the yard → nothing to send
	obs := pivotObs()
	obs.CommandFrigateOnTrade = false
	obs.CommandFrigatePurchasing = true // already the committed purchaser
	h := pivotHandlerScanned(obs, ret, acq, &fakeFrigateLoop{}, scanner)

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.dedications) != 0 {
		t.Fatalf("a committed purchaser must NOT be re-dedicated; dedications=%v", ret.dedications)
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
// (price_unreadable) and NEVER take the frigate out of trade for a trip that cannot be made.
func TestBootstrap_Pivot_ColdPrice_NoScanner_FailsClosedNoTake(t *testing.T) {
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: false}
	h := pivotHandler(pivotObs(), ret, acq, &fakeFrigateLoop{}) // NO scanner wired
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.dedications) != 0 {
		t.Fatalf("no scanner: must NOT take a frigate it cannot send; dedications=%v", ret.dedications)
	}
	if acq.buys != 0 || res.Blocker != "price_unreadable" {
		t.Fatalf("no scanner: fail closed price_unreadable, no buy; buys=%d blocker=%q", acq.buys, res.Blocker)
	}
}

// END-TO-END (anti-theatre): the cold-start deadlock → cure across ticks. Tick 1: pivot warranted, price
// unreadable → dedicate the frigate + send it (the scanner's dispatch models the frigate reaching the yard,
// flipping the price readable + marking it idle). Tick 2: price reads → the buy executes with the frigate.
func TestBootstrap_Pivot_ColdPrice_EndToEnd_DeadlockToBuy(t *testing.T) {
	world := &incomeWorld{
		treasury: 2000000, homeSystem: "X1", marketsTotal: 10, marketsCovered: 10,
		frigateID: "FRIGATE-1", probeCount: 3, batchRunning: true,
		frigateOnTrade: true, frigateIdle: true, frigateCargoEmpty: true, hasPurchaser: false,
		placementSlots: incomeSlots(),
	}
	acq := &fakeHaulerAcquirer{price: 300000, yard: "X1-YARD", readable: false, world: world}
	ret := &fakeRetirer{world: world}
	scanner := &fakeScanner{dispatched: true, readyHaul: acq, world: world}

	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(&fakeIncomeObserver{world: world})
	h.SetProbeAcquirer(&fakeAcquirer{price: 40000, yard: "Y", readable: true})
	h.SetFrigateRetirer(ret)
	h.SetHaulerAcquirer(acq)
	h.SetContractRunner(&fakeContractRunner{world: world})
	h.SetHandoffLauncher(&fakeHandoff{})
	h.SetShipyardScanner(scanner)

	// Tick 1: cold price → dedicate + send, buy nothing.
	res1, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.dedications) != 1 || scanner.calls != 1 || acq.buys != 0 {
		t.Fatalf("tick 1 must dedicate + send the frigate and buy nothing; dedications=%v scanner calls=%d buys=%d blocker=%q", ret.dedications, scanner.calls, acq.buys, res1.Blocker)
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
