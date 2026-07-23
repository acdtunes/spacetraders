package commands

import "testing"

// sp-muc5x — the cold-start hauler DEADLOCK guard. The first-hauler pivot (ensureHaulerPriceReadable)
// frees the command frigate — STOP its sole-earner contract loop + dedicate it the exclusive purchasing
// ship — to position it at the yard for the presence-gated price read. Before this fix it freed the frigate
// BEFORE affordability was ever known, so on a real cold start (treasury below price + working-capital
// floor) the frigate was freed, stopped earning, and the treasury never grew → the hauler stayed
// unaffordable forever → permanent deadlock (the frigate idle at the yard).
//
// The fix caches the last readable hauler price (seeded in DATA while a probe-buy hull is already at the
// yard, refreshed on every readable hauler PriceCheck) and gates the free on it: the frigate keeps EARNING
// until treasury − haulerPrice ≥ ContractWorkingCapitalFloor, and only THEN is it freed to position+buy.
// The money guard is unchanged (same cushion≥floor test) — the fix only changes WHEN the frigate is freed.

// mucUnaffordableObs is the live deadlock (TORWIND_DEV12): a cold-start INCOME pivot observation with the
// hauler price UNREADABLE (cold yard) and the treasury set so treasury − price is far below the 150k floor.
func mucUnaffordableObs() Observation {
	obs := pivotObs()      // 0 haulers, frigate on its loop, cargo empty, no idle purchaser, viable hubs
	obs.Treasury = 127_060 // 127_060 − 363_473 = −236_413 ≪ 150k floor ⇒ UNAFFORDABLE
	return obs
}

// UNAFFORDABLE + cached price present ⇒ the frigate is NEVER freed: no loop stop, no purchasing dedication,
// no positioning. It stays on its contract loop earning; the buy blocks capital_gate and retries. This is
// the invariant the deadlock violated (frigate freed while permanently unaffordable → no earner left).
func TestBootstrap_Muc5x_ColdPrice_UnaffordableCached_KeepsFrigateEarning(t *testing.T) {
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 363_473, yard: "Y", readable: false} // presence-gated: unreadable at the yard
	loop := &fakeFrigateLoop{}
	scanner := &fakeScanner{positionDispatched: true}
	h := pivotHandlerScanned(mucUnaffordableObs(), ret, acq, loop, scanner)
	// Seed the cache exactly as the DATA phase would have (a hull at the yard read the hauler price earlier).
	h.cacheHaulerPrice("boot-1", 363_473)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if loop.stopCalls != 0 {
		t.Fatalf("INVARIANT: an unaffordable hauler must NOT stop the frigate's earning loop; stopCalls=%d (blocker=%q)", loop.stopCalls, res.Blocker)
	}
	if len(ret.dedications) != 0 {
		t.Fatalf("an unaffordable hauler must NOT dedicate the frigate as purchaser (it is not freed); dedications=%v", ret.dedications)
	}
	if scanner.positionCalls != 0 {
		t.Fatalf("an unaffordable hauler must NOT position a purchaser (the frigate is untouched); positionCalls=%d", scanner.positionCalls)
	}
	if acq.buys != 0 {
		t.Fatalf("an unaffordable hauler must not buy; buys=%d", acq.buys)
	}
	if res.FrigatePivoted {
		t.Fatalf("no pivot may be recorded when the frigate is held (not freed)")
	}
	if res.Blocker != "capital_gate" {
		t.Fatalf("an unaffordable cached first-hauler must block capital_gate (frigate keeps earning), got %q", res.Blocker)
	}
}

// AFFORDABLE + cached price present ⇒ byte-identical to the pre-fix happy path: the pivot FREES the frigate
// (stop loop + dedicate purchaser) and positions it at the yard. Proves the guard only tightens the
// unaffordable case and never blocks a legitimate free.
func TestBootstrap_Muc5x_ColdPrice_AffordableCached_FreesAndPositionsAsBefore(t *testing.T) {
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 300_000, yard: "Y", readable: false}
	loop := &fakeFrigateLoop{}
	scanner := &fakeScanner{positionDispatched: true}
	obs := pivotObs() // treasury 2_000_000 ⇒ 2_000_000 − 300_000 = 1_700_000 ≥ 150k floor ⇒ affordable
	h := pivotHandlerScanned(obs, ret, acq, loop, scanner)
	h.cacheHaulerPrice("boot-1", 300_000) // cache present AND affordable

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if loop.stopCalls != 1 || len(loop.stopped) != 1 || loop.stopped[0] != "FRIGATE-1" {
		t.Fatalf("an affordable cached price must FREE the frigate exactly as before; stopCalls=%d stopped=%v (blocker=%q)", loop.stopCalls, loop.stopped, res.Blocker)
	}
	if len(ret.dedications) != 1 || ret.dedications[0] != "FRIGATE-1" {
		t.Fatalf("an affordable cached price must dedicate the frigate as purchaser; dedications=%v", ret.dedications)
	}
	if scanner.positionCalls != 1 || len(scanner.positioned) != 1 || scanner.positioned[0] != "FRIGATE-1" {
		t.Fatalf("an affordable cached price must position the freed frigate; positionCalls=%d positioned=%v", scanner.positionCalls, scanner.positioned)
	}
	if !res.FrigatePivoted || res.Blocker != "positioning_purchaser_at_shipyard" {
		t.Fatalf("an affordable cached price must record the pivot + position; FrigatePivoted=%v blocker=%q", res.FrigatePivoted, res.Blocker)
	}
}

// NO cache yet (first-ever read, e.g. a fresh boot before the DATA seed) ⇒ the guard is INERT: the free
// proceeds to the existing free+position (byte-identical to pre-fix). The guard only tightens on a POSITIVE
// cache, so an absent cache never changes behavior. (This is also the documented restart-residual boundary.)
func TestBootstrap_Muc5x_ColdPrice_NoCache_ProceedsToFreeAsBefore(t *testing.T) {
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 363_473, yard: "Y", readable: false}
	loop := &fakeFrigateLoop{}
	scanner := &fakeScanner{positionDispatched: true}
	h := pivotHandlerScanned(mucUnaffordableObs(), ret, acq, loop, scanner)
	// NO cacheHaulerPrice call — the cache is empty (0 = no evidence).

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if loop.stopCalls != 1 || scanner.positionCalls != 1 {
		t.Fatalf("with no cache the free must proceed exactly as before (byte-identical); stopCalls=%d positionCalls=%d (blocker=%q)", loop.stopCalls, scanner.positionCalls, res.Blocker)
	}
	if !res.FrigatePivoted || res.Blocker != "positioning_purchaser_at_shipyard" {
		t.Fatalf("with no cache the pivot's free step must fire + position; FrigatePivoted=%v blocker=%q", res.FrigatePivoted, res.Blocker)
	}
}

// The DATA seed: while a probe-buy hull is at the yard, bootstrap ALSO reads + caches the hauler price, so
// INCOME knows affordability before it ever frees the frigate. Proves the seed populates the cache through a
// real DATA-phase reconcile (probes under target, a readable yard) — the mechanism that closes the deadlock
// on a normal cold start without ever freeing the frigate first.
func TestBootstrap_Muc5x_DataPhase_SeedsHaulerPriceCache(t *testing.T) {
	obs := Observation{
		HomeSystem: "X1-HQ", ProbeCount: 1, ProbesScouting: 1, // under target 3 ⇒ DATA, acquireProbesToTarget runs
		HasIdlePurchaser: true, Treasury: 2_000_000,
		BatchContractRunning: true, // isolate: don't also try to launch batch-contract (no runner wired)
		Readable:             true,
	}
	probeAcq := &fakeAcquirer{price: 40_000, yard: "X1-HQ-YARD", readable: true}   // yard readable ⇒ a hull is present
	haulAcq := &fakeHaulerAcquirer{price: 363_473, yard: "X1-HQ-YARD", readable: true} // same yard prices the hauler too
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(&fakeObserver{obs: obs})
	h.SetProbeAcquirer(probeAcq)
	h.SetScoutPostDeclarer(&fakeDeclarer{})
	h.SetHaulerAcquirer(haulAcq) // wired so the DATA seed can read the hauler price at the yard

	if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if got := h.cachedHaulerPrice("boot-1"); got != 363_473 {
		t.Fatalf("the DATA phase must seed the hauler-price cache from the yard read; cached=%d want=363473 (haulAcq priceChks=%d)", got, haulAcq.priceChks)
	}
}

// End-to-end (anti-theatre): the DATA seed feeds the INCOME guard within one handler. Tick 1 is a DATA tick
// that seeds the cache from the yard; then an INCOME tick with an UNAFFORDABLE treasury must keep the frigate
// earning (no free) purely off that seeded cache — no direct cache priming.
func TestBootstrap_Muc5x_DataSeedFeedsIncomeGuard_EndToEnd(t *testing.T) {
	// Tick 1: DATA — a probe-buy hull at the yard seeds the hauler price into the cache.
	dataObs := Observation{
		HomeSystem: "X1-HQ", ProbeCount: 1, ProbesScouting: 1,
		HasIdlePurchaser: true, Treasury: 2_000_000, BatchContractRunning: true, Readable: true,
	}
	probeAcq := &fakeAcquirer{price: 40_000, yard: "X1-HQ-YARD", readable: true}
	haulAcq := &fakeHaulerAcquirer{price: 363_473, yard: "X1-HQ-YARD", readable: true}
	ret := &fakeRetirer{}
	loop := &fakeFrigateLoop{}
	scanner := &fakeScanner{positionDispatched: true}
	obsSrc := &fakeObserver{obs: dataObs}

	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(obsSrc)
	h.SetProbeAcquirer(probeAcq)
	h.SetScoutPostDeclarer(&fakeDeclarer{})
	h.SetFrigateRetirer(ret)
	h.SetHaulerAcquirer(haulAcq)
	h.SetContractRunner(&fakeContractRunner{})
	h.SetFrigateContractLoopStarter(loop)
	h.SetShipyardScanner(scanner)

	if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
		t.Fatalf("tick 1 (DATA seed): %v", err)
	}
	if h.cachedHaulerPrice("boot-1") != 363_473 {
		t.Fatalf("tick 1 must seed the cache; cached=%d", h.cachedHaulerPrice("boot-1"))
	}

	// Tick 2: INCOME — provisioned, viable hubs, cold hauler price, UNAFFORDABLE treasury. The seeded cache
	// must hold the frigate on its loop (no free) with blocker=capital_gate.
	haulAcq.readable = false // the yard is cold again for the hauler (frigate on its loop, probes scouting)
	incomeObs := mucUnaffordableObs()
	obsSrc.obs = incomeObs

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("tick 2 (INCOME guard): %v", err)
	}
	if loop.stopCalls != 0 || len(ret.dedications) != 0 || scanner.positionCalls != 0 {
		t.Fatalf("tick 2 must keep the frigate earning off the DATA-seeded cache; stopCalls=%d dedications=%v positionCalls=%d (blocker=%q)", loop.stopCalls, ret.dedications, scanner.positionCalls, res.Blocker)
	}
	if res.Blocker != "capital_gate" {
		t.Fatalf("tick 2 must block capital_gate (frigate keeps earning), got %q", res.Blocker)
	}
}
