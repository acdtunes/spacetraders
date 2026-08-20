package commands

import "testing"

// sp-muc5x — the cold-start hauler DEADLOCK guard. The first-hauler pivot (awaitHaulerPrice) frees the
// command frigate — STOP its sole-earner contract loop + dedicate it the exclusive purchasing ship — to
// send it to the yard for the presence-gated price read. Freeing the frigate BEFORE affordability is
// known deadlocks a real cold start (treasury below price + working-capital floor): the frigate stops
// earning, the treasury never grows, the hauler stays unaffordable forever and the frigate sits idle at
// the yard.
//
// The guard weighs the LAST ASK the yard gave for a hauler — taken in cold start while a probe-buy hull
// is standing there, refreshed on every readable hauler read, and reported back by the cold read itself.
// The frigate keeps EARNING until treasury − haulerPrice ≥ ContractWorkingCapitalFloor and only THEN is
// it freed to go buy. The money guard is the same cushion≥floor test — this decides only WHEN the
// frigate is freed.

// mucUnaffordableObs is the live deadlock (TORWIND_DEV12): a cold-start cold-start pivot observation with the
// hauler price UNREADABLE (cold yard) and the treasury set so treasury − price is far below the 150k floor.
func mucUnaffordableObs() Observation {
	obs := pivotObs()      // 0 haulers, frigate idle in trade, cargo empty, no idle purchaser, viable hubs
	obs.Treasury = 127_060 // 127_060 − 363_473 = −236_413 ≪ 150k floor ⇒ UNAFFORDABLE
	return obs
}

// UNAFFORDABLE + a last ask on record ⇒ the frigate is NEVER taken: no purchasing dedication, no hull
// sent. It stays in the trade fleet earning; the buy blocks capital_gate and retries. This is the
// invariant the deadlock violated (frigate taken while permanently unaffordable → no earner left).
func TestBootstrap_Muc5x_ColdPrice_UnaffordableCached_KeepsFrigateEarning(t *testing.T) {
	ret := &fakeRetirer{}
	// Presence-gated: unreadable at the yard, but it last asked 363_473 (a hull read it earlier).
	acq := &fakeHaulerAcquirer{price: 363_473, yard: "Y", readable: false, lastAsk: 363_473}
	loop := &fakeFrigateLoop{}
	scanner := &fakeScanner{dispatched: true}
	h := pivotHandlerScanned(mucUnaffordableObs(), ret, acq, loop, scanner)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if len(ret.dedications) != 0 {
		t.Fatalf("INVARIANT: an unaffordable hauler must NOT take the frigate out of the trade fleet; dedications=%v (blocker=%q)", ret.dedications, res.Blocker)
	}
	if scanner.calls != 0 {
		t.Fatalf("an unaffordable hauler must send NO hull to the yard (the frigate is untouched); scanner calls=%d purchasers=%v", scanner.calls, scanner.purchasers)
	}
	if acq.buys != 0 {
		t.Fatalf("an unaffordable hauler must not buy; buys=%d", acq.buys)
	}
	if res.FrigatePivoted {
		t.Fatalf("no pivot may be recorded when the frigate is held (left trading)")
	}
	if res.Blocker != "capital_gate" {
		t.Fatalf("an unaffordable cached first-hauler must block capital_gate (frigate keeps earning), got %q", res.Blocker)
	}
}

// AFFORDABLE + a last ask on record ⇒ the pivot TAKES the frigate (dedicate purchaser) and sends it to
// the yard by name. Proves the guard only tightens the unaffordable case and never blocks a legitimate
// take.
func TestBootstrap_Muc5x_ColdPrice_AffordableCached_TakesAndPositionsAsBefore(t *testing.T) {
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 300_000, yard: "Y", readable: false, lastAsk: 300_000}
	loop := &fakeFrigateLoop{}
	scanner := &fakeScanner{dispatched: true}
	obs := pivotObs() // treasury 2_000_000 ⇒ 2_000_000 − 300_000 = 1_700_000 ≥ 150k floor ⇒ affordable
	h := pivotHandlerScanned(obs, ret, acq, loop, scanner)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if len(ret.dedications) != 1 || ret.dedications[0] != "FRIGATE-1" {
		t.Fatalf("an affordable last ask must dedicate the frigate as purchaser; dedications=%v (blocker=%q)", ret.dedications, res.Blocker)
	}
	if scanner.calls != 1 || len(scanner.purchasers) != 1 || scanner.purchasers[0] != "FRIGATE-1" {
		t.Fatalf("an affordable last ask must send the taken frigate by name; calls=%d purchasers=%v", scanner.calls, scanner.purchasers)
	}
	if !res.FrigatePivoted || res.Blocker != "positioning_purchaser_at_shipyard" {
		t.Fatalf("an affordable last ask must record the pivot + position; FrigatePivoted=%v blocker=%q", res.FrigatePivoted, res.Blocker)
	}
}

// NO ask on record yet (a first-ever cold start, before any yard has priced a hauler) ⇒ the guard is
// INERT: the take proceeds, and the frigate goes to find out what the hull costs. The guard only tightens
// on a POSITIVE ask, so absent evidence never changes behavior — the cold start must never wedge the
// other way and leave hauler #1 unbought.
func TestBootstrap_Muc5x_ColdPrice_NoCache_ProceedsToTakeAsBefore(t *testing.T) {
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 363_473, yard: "Y", readable: false} // no lastAsk: the yard has never priced
	loop := &fakeFrigateLoop{}
	scanner := &fakeScanner{dispatched: true}
	h := pivotHandlerScanned(mucUnaffordableObs(), ret, acq, loop, scanner)

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.dedications) != 1 || scanner.calls != 1 || scanner.purchasers[0] != "FRIGATE-1" {
		t.Fatalf("with no ask on record the take must proceed; dedications=%v calls=%d purchasers=%v (blocker=%q)", ret.dedications, scanner.calls, scanner.purchasers, res.Blocker)
	}
	if !res.FrigatePivoted || res.Blocker != "positioning_purchaser_at_shipyard" {
		t.Fatalf("with no ask on record the pivot's take step must fire + send the frigate; FrigatePivoted=%v blocker=%q", res.FrigatePivoted, res.Blocker)
	}
}

// The probe-buy reading: while a probe-buy hull is standing at the yard, bootstrap ALSO reads the hauler
// ask, so the pivot knows affordability before it ever frees the frigate. Proves the reading is taken
// through a real cold-start reconcile (probes under target, a readable yard) — the mechanism that closes
// the deadlock on a normal cold start without ever freeing the frigate first.
func TestBootstrap_Muc5x_ProbeBuy_SeedsHaulerPriceCache(t *testing.T) {
	obs := Observation{
		HomeSystem: "X1-HQ", ProbeCount: 1, ProbesScouting: 1, // under target 3 ⇒ acquireProbesToTarget runs
		HasIdlePurchaser: true, Treasury: 2_000_000,
		BatchContractRunning: true, // isolate: don't also try to launch batch-contract (no runner wired)
		Readable:             true,
	}
	probeAcq := &fakeAcquirer{price: 40_000, yard: "X1-HQ-YARD", readable: true}       // yard readable ⇒ a hull is present
	haulAcq := &fakeHaulerAcquirer{price: 363_473, yard: "X1-HQ-YARD", readable: true} // same yard prices the hauler too
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(&fakeObserver{obs: obs})
	h.SetProbeAcquirer(probeAcq)
	h.SetHaulerAcquirer(haulAcq) // wired so the probe-buy seed can read the hauler price at the yard

	if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if haulAcq.priceChks != 1 {
		t.Fatalf("cold start must read the hauler ask while the probe-buy hull is at the yard; hauler priceChks=%d want=1", haulAcq.priceChks)
	}
}

// End-to-end (anti-theatre): the probe-buy reading feeds the contract guard within one handler. Tick 1 reads
// the hauler ask off a warm yard; then a cold-start tick with an UNAFFORDABLE treasury must keep the frigate
// earning (no free) purely off that reading — nothing is put on record by hand.
func TestBootstrap_Muc5x_SeedFeedsPivotGuard_EndToEnd(t *testing.T) {
	// Tick 1: a probe-buy hull at the yard puts the hauler ask on record.
	dataObs := Observation{
		HomeSystem: "X1-HQ", ProbeCount: 1, ProbesScouting: 1,
		HasIdlePurchaser: true, Treasury: 2_000_000, BatchContractRunning: true, Readable: true,
	}
	probeAcq := &fakeAcquirer{price: 40_000, yard: "X1-HQ-YARD", readable: true}
	haulAcq := &fakeHaulerAcquirer{price: 363_473, yard: "X1-HQ-YARD", readable: true}
	ret := &fakeRetirer{}
	loop := &fakeFrigateLoop{}
	scanner := &fakeScanner{dispatched: true}
	obsSrc := &fakeObserver{obs: dataObs}

	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(obsSrc)
	h.SetProbeAcquirer(probeAcq)
	h.SetFrigateRetirer(ret)
	h.SetHaulerAcquirer(haulAcq)
	h.SetContractRunner(&fakeContractRunner{})
	h.SetFrigateContractLoopStarter(loop)
	h.SetShipyardScanner(scanner)

	if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
		t.Fatalf("tick 1 (probe-buy reading): %v", err)
	}
	if haulAcq.priceChks != 1 {
		t.Fatalf("tick 1 must read the hauler ask at the yard; hauler priceChks=%d want=1", haulAcq.priceChks)
	}

	// Tick 2: provisioned, viable hubs, cold hauler price, UNAFFORDABLE treasury. The ask tick 1 put on
	// record must hold the frigate on its loop (no free) with blocker=capital_gate.
	haulAcq.readable = false // the yard is cold again for the hauler (frigate on its loop, probes scouting)
	incomeObs := mucUnaffordableObs()
	obsSrc.obs = incomeObs

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("tick 2 (contract guard): %v", err)
	}
	if loop.stopCalls != 0 || len(ret.dedications) != 0 || scanner.calls != 0 {
		t.Fatalf("tick 2 must keep the frigate earning off tick 1's reading; stopCalls=%d dedications=%v scanner calls=%d (blocker=%q)", loop.stopCalls, ret.dedications, scanner.calls, res.Blocker)
	}
	if res.Blocker != "capital_gate" {
		t.Fatalf("tick 2 must block capital_gate (frigate keeps earning), got %q", res.Blocker)
	}
}
