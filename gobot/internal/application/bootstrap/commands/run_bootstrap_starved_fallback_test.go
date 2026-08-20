package commands

import (
	"testing"
	"time"
)

// THE STARVED-TRADE CONTRACT FALLBACK (sp-bvf20). Live on staging: the command frigate traded
// profitably twice, legitimately exhausted the only 2 sinks in a 21-market cold-start system, and
// then every tour exited exit_reason=starvation with escalating cooldowns — the hull idle-retried a
// starved system for hours. Repositioning out is impossible pre-gate. So when trade is locally
// starved the frigate does CONTRACT work instead of idling: bootstrap clears its "trade" tag, which
// is the ONLY shape the contract pool's existing last-resort admission accepts (ship_pool_manager.go
// admits an UNDEDICATED command hull when no hauler is idle — unmodified, reused verbatim), and
// re-dedicates it to trade once the leg is over so the normal relaunch loop resumes.
//
// The signal is derived per tick from the two PERSISTED assignment timestamps (RULINGS #2): a last
// run shorter than the fast-fail line never traded, and one still parked past the dwell has been
// ESCALATED by the trade coordinator rather than simply relaunched.

// fixedClock pins "now" so a test states the frigate's park age instead of racing real time.
type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time        { return c.now }
func (c fixedClock) Sleep(_ time.Duration) {}

// starvedNow is the wall time every fixture in this file is anchored to.
var starvedNow = time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

// starvedHandler is tradeIdleHandler on a pinned clock, so the fallback's dwell/window arithmetic is
// deterministic.
func starvedHandler(obs Observation, ret *fakeRetirer, acq *fakeHaulerAcquirer, run *fakeContractRunner, ho *fakeHandoff) *RunBootstrapCoordinatorHandler {
	h := NewRunBootstrapCoordinatorHandler(fixedClock{now: starvedNow})
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(&fakeObserver{obs: obs})
	h.SetProbeAcquirer(&fakeAcquirer{price: 40000, yard: "Y", readable: true})
	h.SetFrigateRetirer(ret)
	h.SetHaulerAcquirer(acq)
	h.SetContractRunner(run)
	h.SetHandoffLauncher(ho)
	h.SetFrigateContractLoopStarter(&fakeFrigateLoop{})
	return h
}

// starvedObs is the exact live symptom: deep in COLDSTART (treasury far below the contract-start
// threshold), frigate dedicated TRADE, parked and empty, its last tour a 20-second fast-fail that
// ended `parked` ago.
func starvedObs(parked time.Duration) Observation {
	obs := tradeIdleObs()
	obs.Treasury = 60_000
	obs.CommandFrigateLastRunEnd = starvedNow.Add(-parked)
	obs.CommandFrigateLastRunStart = obs.CommandFrigateLastRunEnd.Add(-20 * time.Second)
	return obs
}

// --- THE SYMPTOM: a starved, idle frigate does contract work instead of nothing ---

func TestBootstrap_StarvedFrigate_ReleasedToContractLastResortPool(t *testing.T) {
	obs := starvedObs(frigateStarvedDwell + time.Minute)
	ret, run := &fakeRetirer{}, &fakeContractRunner{}
	h := starvedHandler(obs, ret, &fakeHaulerAcquirer{price: 100_000, yard: "Y", readable: true}, run, &fakeHandoff{})

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if run.calls != 1 {
		t.Fatalf("the contract ENGINE must be running from tick 1 of COLDSTART (zero-capital) or there is nothing to hand the frigate to, got %d launches (blocker=%q)", run.calls, res.Blocker)
	}
	if len(ret.ships) != 1 || ret.ships[0] != "FRIGATE-1" {
		t.Fatalf("a starved, idle, empty frigate must have its trade tag CLEARED so the contract pool's last-resort admission can see it (an undedicated command hull is the only shape it admits), got retires=%v", ret.ships)
	}
	if !res.FrigateContractFallback {
		t.Fatalf("the fallback must be recorded on the tick result so the heartbeat shows it")
	}
	if len(ret.tradeDedications) != 0 {
		t.Fatalf("the same tick must not hand the frigate straight back to trade, got %v", ret.tradeDedications)
	}
}

// --- PLAYBOOK §9: a frigate that is not genuinely free is never redirected ---

func TestBootstrap_StarvedFallback_NeverTakesAMidTourFrigate(t *testing.T) {
	obs := starvedObs(frigateStarvedDwell + time.Minute)
	obs.CommandFrigateIdle = false // mid-tour / still flying
	ret := &fakeRetirer{}
	h := starvedHandler(obs, ret, &fakeHaulerAcquirer{price: 100_000, yard: "Y", readable: true}, &fakeContractRunner{}, &fakeHandoff{})

	h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.ships) != 0 {
		t.Fatalf("a hull mid-tour must never be reassigned (PLAYBOOK §9), got retires=%v", ret.ships)
	}
}

// A tour that actually traded is not starvation, however long the hull has been parked since.
func TestBootstrap_StarvedFallback_ProductiveTourIsNotStarvation(t *testing.T) {
	obs := starvedObs(frigateStarvedDwell + time.Minute)
	obs.CommandFrigateLastRunStart = obs.CommandFrigateLastRunEnd.Add(-40 * time.Minute)
	ret := &fakeRetirer{}
	h := starvedHandler(obs, ret, &fakeHaulerAcquirer{price: 100_000, yard: "Y", readable: true}, &fakeContractRunner{}, &fakeHandoff{})

	h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.ships) != 0 {
		t.Fatalf("a frigate whose last tour actually traded must stay in the trade fleet, got retires=%v", ret.ships)
	}
}

// The dwell sits above the trade coordinator's base relaunch cooldown, so a hull merely BETWEEN
// normal tours is left alone — trade gets every chance before contracts do.
func TestBootstrap_StarvedFallback_HoldsUntilTheDwellElapses(t *testing.T) {
	obs := starvedObs(frigateStarvedDwell - time.Second)
	ret := &fakeRetirer{}
	h := starvedHandler(obs, ret, &fakeHaulerAcquirer{price: 100_000, yard: "Y", readable: true}, &fakeContractRunner{}, &fakeHandoff{})

	h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.ships) != 0 {
		t.Fatalf("inside the dwell the trade coordinator still owns the relaunch, got retires=%v", ret.ships)
	}
}

// A laden frigate sells its hold on its next trade tour; handing it to contracts would strand that
// cargo (and the contract pool's unrelated-cargo filter would refuse it anyway).
func TestBootstrap_StarvedFallback_LeavesALadenFrigateInTrade(t *testing.T) {
	obs := starvedObs(frigateStarvedDwell + time.Minute)
	obs.FrigateCargoEmpty = false
	ret := &fakeRetirer{}
	h := starvedHandler(obs, ret, &fakeHaulerAcquirer{price: 100_000, yard: "Y", readable: true}, &fakeContractRunner{}, &fakeHandoff{})

	h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.ships) != 0 {
		t.Fatalf("a frigate still holding trade cargo must stay in trade to sell it, got retires=%v", ret.ships)
	}
}

// --- THE CYCLE: trade → starve → contract → trade, repeating ---

// While the window is open the untagged frigate is LEFT untagged: re-tagging it on the very next
// bootstrap tick would take it back before the contract coordinator's own tick ever ran.
func TestBootstrap_StarvedFallback_DoesNotYankTheFrigateBackMidWindow(t *testing.T) {
	obs := starvedObs(frigateStarvedDwell + time.Minute)
	obs.CommandFrigateOnTrade = false // released by the fallback on an earlier tick
	ret := &fakeRetirer{}
	h := starvedHandler(obs, ret, &fakeHaulerAcquirer{price: 100_000, yard: "Y", readable: true}, &fakeContractRunner{}, &fakeHandoff{})

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.tradeDedications) != 0 || res.FrigateTrading {
		t.Fatalf("the fallback window must hold the trade re-dedication off, got trade=%v FrigateTrading=%v", ret.tradeDedications, res.FrigateTrading)
	}
}

// Once the contract leg is over the hull comes back from a run that is no longer a fast-fail, so the
// standing trade re-dedication fires and the trade coordinator resumes checking for profitable lanes.
// This is the SECOND half of the cycle — not a one-time switch.
func TestBootstrap_StarvedFallback_ReDedicatesToTradeAfterTheContractLeg(t *testing.T) {
	obs := starvedObs(0)
	obs.CommandFrigateOnTrade = false                                  // handed to contracts, now handed back
	obs.CommandFrigateLastRunStart = starvedNow.Add(-25 * time.Minute) // a real contract leg
	obs.CommandFrigateLastRunEnd = starvedNow.Add(-10 * time.Second)   // just completed
	ret := &fakeRetirer{}
	h := starvedHandler(obs, ret, &fakeHaulerAcquirer{price: 100_000, yard: "Y", readable: true}, &fakeContractRunner{}, &fakeHandoff{})

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.tradeDedications) != 1 || ret.tradeDedications[0] != "FRIGATE-1" || !res.FrigateTrading {
		t.Fatalf("after the contract leg the frigate must go straight back to the TRADE fleet, got trade=%v FrigateTrading=%v", ret.tradeDedications, res.FrigateTrading)
	}
}

// A fallback nobody took must never strand the hull: the window LAPSES and trade gets it back, so the
// cycle re-arms instead of leaving the frigate untagged and idle for good.
func TestBootstrap_StarvedFallback_WindowLapsesBackToTrade(t *testing.T) {
	obs := starvedObs(frigateStarvedDwell + frigateContractFallbackWindow + time.Second)
	obs.CommandFrigateOnTrade = false
	ret := &fakeRetirer{}
	h := starvedHandler(obs, ret, &fakeHaulerAcquirer{price: 100_000, yard: "Y", readable: true}, &fakeContractRunner{}, &fakeHandoff{})

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.tradeDedications) != 1 || !res.FrigateTrading {
		t.Fatalf("a lapsed fallback window must hand the hull back to trade, got trade=%v FrigateTrading=%v", ret.tradeDedications, res.FrigateTrading)
	}
}

// --- RULINGS #4: the engine runs early, the CAPITAL gate does not move ---

// The whole point of part 2: below the threshold the contract ENGINE is up, and nothing is priced,
// bought, pivoted or scaled.
func TestBootstrap_EngineFromTickOne_SpendsNothingBelowTheThreshold(t *testing.T) {
	obs := starvedObs(frigateStarvedDwell + time.Minute)
	acq, run, ho, ret := &fakeHaulerAcquirer{price: 100_000, yard: "Y", readable: true}, &fakeContractRunner{}, &fakeHandoff{}, &fakeRetirer{}
	h := starvedHandler(obs, ret, acq, run, ho)

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if run.calls != 1 {
		t.Fatalf("the contract engine must be launched below the threshold (zero-capital), got %d", run.calls)
	}
	if acq.buys != 0 || acq.dedicateBuys != 0 || acq.priceChks != 0 {
		t.Fatalf("below the threshold nothing is bought and no yard is priced; buys=%d seeds=%d priceChks=%d", acq.buys, acq.dedicateBuys, acq.priceChks)
	}
	if len(ret.dedications) != 0 {
		t.Fatalf("below the threshold the first-hauler pivot must not fire, got purchaser dedications=%v", ret.dedications)
	}
	if ho.contractScaler != 0 || res.ContractScalerLaunchedEarly {
		t.Fatalf("below the threshold the contract scaler (which spends) must NOT be ensured, got %d early=%v", ho.contractScaler, res.ContractScalerLaunchedEarly)
	}
	if res.Blocker != "contract_start_deferred" {
		t.Fatalf("the capital deferral must still be visible on the heartbeat, got blocker=%q", res.Blocker)
	}
}

// THE LATCH HOLE this bead had to close. contractOpsUnderway used to count "the coordinator is
// running" as proof the operation had started and spent — a fair reading when the launch itself was
// behind the threshold. Now the engine is boot-standing, so a running coordinator says nothing about
// capital and must NEVER open the gate: only a hull actually owned, or a frigate committed
// mid-purchase, latches it (RULINGS #4 — the guard may only ever get stricter).
func TestBootstrap_RunningEngineDoesNotOpenTheCapitalGate(t *testing.T) {
	obs := tradeIdleObs()
	obs.Treasury = 1_000 // effectively broke
	obs.BatchContractRunning = true
	obs.HasIdlePurchaser = true
	acq, ho := &fakeHaulerAcquirer{price: 100_000, yard: "Y", readable: true}, &fakeHandoff{}
	h := starvedHandler(obs, &fakeRetirer{}, acq, &fakeContractRunner{}, ho)

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if res.Blocker != "contract_start_deferred" {
		t.Fatalf("a boot-standing contract engine must NOT latch the contract-start threshold open, got blocker=%q", res.Blocker)
	}
	if acq.buys != 0 || acq.dedicateBuys != 0 || acq.priceChks != 0 {
		t.Fatalf("a broke fleet must buy nothing however long the engine has been up; buys=%d seeds=%d priceChks=%d", acq.buys, acq.dedicateBuys, acq.priceChks)
	}
	if ho.contractScaler != 0 {
		t.Fatalf("nor may the running engine drag the spending scaler up with it, got %d", ho.contractScaler)
	}
}

// The capital latch that DOES matter is untouched: a hull already owned keeps the operation running
// through the treasury dip its own buys caused (RULINGS #1).
func TestBootstrap_OwnedHaulerStillLatchesTheThresholdOpen(t *testing.T) {
	obs := tradeIdleObs()
	obs.Treasury = 460_000
	obs.Haulers = []HaulerSnapshot{{Symbol: "H1", Waypoint: "X1-HUBA"}}
	obs.TradeHullCount = 1
	h := starvedHandler(obs, &fakeRetirer{}, &fakeHaulerAcquirer{price: 100_000, yard: "Y", readable: true}, &fakeContractRunner{}, &fakeHandoff{})

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if res.Blocker == "contract_start_deferred" {
		t.Fatalf("an operation that already owns a hull must never be stood back down by a treasury dip")
	}
}

// Capital ops outrank one opportunistic contract leg: once the operation is funded and still
// hull-less the frigate is the first-hauler pivot's purchaser, so the fallback leaves it in trade.
func TestBootstrap_StarvedFallback_YieldsToTheFirstHaulerPivot(t *testing.T) {
	obs := starvedObs(frigateStarvedDwell + time.Minute)
	obs.Treasury = defaultContractStartTreasuryThreshold + 1
	obs.Haulers = nil
	ret := &fakeRetirer{}
	h := starvedHandler(obs, ret, &fakeHaulerAcquirer{price: 100_000, yard: "Y", readable: true}, &fakeContractRunner{}, &fakeHandoff{})

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.ships) != 0 {
		t.Fatalf("the funded first-hauler pivot owns the frigate; the fallback must not take it, got retires=%v", ret.ships)
	}
	if !res.FrigatePivoted {
		t.Fatalf("precondition: the pivot must still fire (blocker=%q)", res.Blocker)
	}
}

// A graduated player has durably retired contracts: no engine, and no fallback into one.
func TestBootstrap_StarvedFallback_SilentForAGraduatedPlayer(t *testing.T) {
	obs := starvedObs(frigateStarvedDwell + time.Minute)
	obs.ContractGraduated = true
	ret, run := &fakeRetirer{}, &fakeContractRunner{}
	h := starvedHandler(obs, ret, &fakeHaulerAcquirer{price: 100_000, yard: "Y", readable: true}, run, &fakeHandoff{})

	h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if run.calls != 0 || len(ret.ships) != 0 {
		t.Fatalf("a contract-graduated player gets neither engine nor fallback; launches=%d retires=%v", run.calls, ret.ships)
	}
}

// A frigate with no run on record (a fresh era) is not "starved" — absence of evidence is not
// evidence, and the fallback must not fire before trade has ever run.
func TestBootstrap_StarvedFallback_NoRunOnRecordIsNotStarvation(t *testing.T) {
	obs := starvedObs(frigateStarvedDwell + time.Minute)
	obs.CommandFrigateLastRunStart = time.Time{}
	obs.CommandFrigateLastRunEnd = time.Time{}
	ret := &fakeRetirer{}
	h := starvedHandler(obs, ret, &fakeHaulerAcquirer{price: 100_000, yard: "Y", readable: true}, &fakeContractRunner{}, &fakeHandoff{})

	h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.ships) != 0 {
		t.Fatalf("no run on record is no evidence of starvation, got retires=%v", ret.ships)
	}
}

// The dwell must sit ABOVE the trade coordinator's 180s base relaunch cooldown, and the window must
// close at its 600s backoff ceiling — that alignment is what guarantees trade always has the hull
// back in time for its own next relaunch. Pinned so a retune cannot silently break the interlock.
func TestBootstrap_StarvedFallback_WindowIsAlignedToTheTradeBackoffLadder(t *testing.T) {
	if frigateStarvedDwell <= 180*time.Second {
		t.Fatalf("the dwell (%s) must exceed the trade coordinator's 180s base relaunch cooldown, or a hull merely between normal tours is taken", frigateStarvedDwell)
	}
	if frigateStarvedDwell+frigateContractFallbackWindow > 600*time.Second {
		t.Fatalf("the window must close by the trade coordinator's 600s backoff ceiling, got %s", frigateStarvedDwell+frigateContractFallbackWindow)
	}
	if frigateStarvedTourMax != 90*time.Second {
		t.Fatalf("the fast-fail line must mirror the trade coordinator's own minProductiveTourDuration (90s), got %s", frigateStarvedTourMax)
	}
}
