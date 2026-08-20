package commands

import (
	"testing"
	"time"
)

// THE STALLED BUY SHIP. A frigate parked in "purchasing" is the SECOND starvation shape: the pivot
// dedicates it, hauler #1 lands, an accepted-but-unprofitable contract (RULINGS #1) drains the treasury,
// and the next cold-start acquisition goes capital-gated — leaving the hull docked and idle for as long
// as capital takes to regrow, invisible to the starved-TRADE fallback (whose guard opens on the trade
// tag) and refused to the contract coordinator that asks for it. It funnels through the SAME mechanism:
// clear the dedication, let the last-resort admission take it, restore the dedication on return.
//
// The two halves close on the SAME two bounds. This one was first built not to lapse at all, on the
// reasoning that a stalled buy ship has no coordinator to re-take it and that capital coming back is the
// close instead — but capital comes back only if something is EARNING, and when the contract engine's own
// available work is blocked by the same empty treasury (sp-uuejg, live) nothing earns, no leg ever claims
// the hull, and both states are dead ends. So the offer lapses, and lapsed sends the hull to TRADE: the
// income gated on neither the buy nor the contract. What does NOT lapse is the stall's SHAPE — the
// release that unpins a buy ship from a buy it cannot make fires at every park age.

const stalledSeedAsk int64 = 313_730

// stalledPurchaserObs is the live wedge at `parked` old: hauler #1 owned, the trade seed unbought, the
// frigate purchasing/docked/idle/empty, a treasury the ask sits far outside — and a PRODUCTIVE last
// tour, so nothing here is trade starvation.
func stalledPurchaserObs(parked time.Duration) Observation {
	obs := incomeObs()
	obs.BatchContractRunning = true // isolate: the contract engine is already up
	obs.GrowthRunning = true        // isolate: the early autosizer launch is a no-op
	obs.CommandFrigateID = "FRIGATE-1"
	obs.CommandFrigateOnTrade = false
	obs.CommandFrigatePurchasing = true
	obs.CommandFrigateIdle = true
	obs.FrigateCargoEmpty = true
	obs.HasIdlePurchaser = false // the dedicated buy ship is outside the free-hull search
	obs.Haulers = []HaulerSnapshot{{Symbol: "HAULER-1", Waypoint: "X1-HUBA"}}
	obs.TradeHullCount = 0
	obs.Treasury = 13_390
	obs.CommandFrigateLastRunEnd = starvedNow.Add(-parked)
	obs.CommandFrigateLastRunStart = obs.CommandFrigateLastRunEnd.Add(-40 * time.Minute)
	return obs
}

// stalledAcquirer prices the seed at the live ask, readable — the frigate is standing at the yard.
func stalledAcquirer() *fakeHaulerAcquirer {
	return &fakeHaulerAcquirer{price: stalledSeedAsk, yard: "X1-YARD", readable: true}
}

// --- THE SYMPTOM: a capital-gated buy ship is offered to contracts instead of idling ---

func TestBootstrap_StalledPurchaser_ReleasedToContractLastResortPool(t *testing.T) {
	obs := stalledPurchaserObs(frigateStarvedDwell + time.Minute)
	ret, acq := &fakeRetirer{}, stalledAcquirer()
	h := starvedHandler(obs, ret, acq, &fakeContractRunner{}, &fakeHandoff{})

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if len(ret.ships) != 1 || ret.ships[0] != "FRIGATE-1" {
		t.Fatalf("a capital-gated purchasing frigate, idle and empty, must have its dedication CLEARED so the contract pool's last-resort admission can see it (an undedicated command hull is the only shape it admits), got retires=%v blocker=%q", ret.ships, res.Blocker)
	}
	if !res.FrigateContractFallback {
		t.Fatalf("the fallback must be recorded on the tick result so the heartbeat shows it")
	}
	if len(ret.tradeDedications) != 0 || len(ret.dedications) != 0 {
		t.Fatalf("the same tick must not re-tag the frigate, got trade=%v purchasing=%v", ret.tradeDedications, ret.dedications)
	}
	if acq.buys != 0 || acq.dedicateBuys != 0 {
		t.Fatalf("the release spends nothing — the buy is out of reach, which is the whole reason it fires; buys=%d seeds=%d", acq.buys, acq.dedicateBuys)
	}
}

// The dwell is a settle buffer, the SAME 15s the trade half opens on.
func TestBootstrap_StalledPurchaser_HoldsUntilTheDwellElapses(t *testing.T) {
	obs := stalledPurchaserObs(frigateStarvedDwell - time.Second)
	ret := &fakeRetirer{}
	h := starvedHandler(obs, ret, stalledAcquirer(), &fakeContractRunner{}, &fakeHandoff{})

	h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.ships) != 0 {
		t.Fatalf("inside the dwell the buy ship is left where it stands, got retires=%v", ret.ships)
	}
}

// One bootstrap tick in, the hull is already offered — the point of matching the trade half's dwell.
func TestBootstrap_StalledPurchaser_OpensWithinOneBootstrapTick(t *testing.T) {
	obs := stalledPurchaserObs(defaultBootstrapTickSeconds * time.Second)
	ret := &fakeRetirer{}
	h := starvedHandler(obs, ret, stalledAcquirer(), &fakeContractRunner{}, &fakeHandoff{})

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.ships) != 1 || !res.FrigateContractFallback {
		t.Fatalf("one bootstrap tick (%ds) into the stall the frigate must already be offered to contracts, got retires=%v fallback=%v blocker=%q", defaultBootstrapTickSeconds, ret.ships, res.FrigateContractFallback, res.Blocker)
	}
}

// --- RULINGS #1 + PLAYBOOK §9, proven for the PURCHASING shape in its own right ---

// The free-and-empty guard is INDEPENDENT of the dwell, at EVERY park age. In-flight purchases are
// covered by the same read: a purchase container CLAIMS its buy ship, so it never reads idle.
func TestBootstrap_StalledPurchaser_FreeAndEmptyGuardHoldsAtEveryParkAge(t *testing.T) {
	parkAges := []time.Duration{
		0,
		frigateStarvedDwell,
		defaultBootstrapTickSeconds * time.Second,
		frigateStarvedDwell + frigateContractFallbackWindow + time.Minute,
		4 * time.Hour,
	}
	unfree := map[string]func(*Observation){
		"mid-navigation to the shipyard":    func(o *Observation) { o.CommandFrigateIdle = false },
		"a buy transaction still in flight": func(o *Observation) { o.CommandFrigateIdle = false },
		"still holding cargo":               func(o *Observation) { o.FrigateCargoEmpty = false },
		"flying AND laden":                  func(o *Observation) { o.CommandFrigateIdle, o.FrigateCargoEmpty = false, false },
	}

	for name, makeUnfree := range unfree {
		for _, parked := range parkAges {
			obs := stalledPurchaserObs(parked)
			makeUnfree(&obs)
			ret := &fakeRetirer{}
			h := starvedHandler(obs, ret, stalledAcquirer(), &fakeContractRunner{}, &fakeHandoff{})

			h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
			if len(ret.ships) != 0 {
				t.Fatalf("a buy ship %s must never be reassigned, whatever the park age (%s), got retires=%v", name, parked, ret.ships)
			}
		}
	}
}

// A buy about to happen is never interrupted: one treasury, one ask, the same untouched floor.
func TestBootstrap_StalledPurchaser_AffordableSeedKeepsItsBuyShip(t *testing.T) {
	obs := stalledPurchaserObs(frigateStarvedDwell + time.Minute)
	obs.Treasury = 2_000_000 // the ask is comfortably inside the working-capital floor
	ret, acq := &fakeRetirer{}, stalledAcquirer()
	h := starvedHandler(obs, ret, acq, &fakeContractRunner{}, &fakeHandoff{})

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.ships) != 0 || res.FrigateContractFallback {
		t.Fatalf("a purchaser that is about to buy must NEVER be released — that is the ping-pong; retires=%v fallback=%v", ret.ships, res.FrigateContractFallback)
	}
	if acq.dedicateBuys != 1 || !res.TradeHullSeeded {
		t.Fatalf("the affordable seed must still be bought with the frigate; seeds=%d seeded=%v blocker=%q", acq.dedicateBuys, res.TradeHullSeeded, res.Blocker)
	}
}

// A yard that never priced a hauler reports 0 — an absence of evidence, and no reason to move the hull.
func TestBootstrap_StalledPurchaser_NoAskOnRecordIsNotAStall(t *testing.T) {
	obs := stalledPurchaserObs(frigateStarvedDwell + time.Minute)
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: stalledSeedAsk, yard: "X1-YARD", readable: false} // cold yard, lastAsk 0
	h := starvedHandler(obs, ret, acq, &fakeContractRunner{}, &fakeHandoff{})

	h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.ships) != 0 {
		t.Fatalf("no ask on record is no evidence of a stall, got retires=%v", ret.ships)
	}
}

// --- WHERE THE HULL COMES BACK TO. Both outcomes are reachable; neither is assumed ---

// releasedPurchaserObs is what the fallback leaves behind: the frigate untagged and free again.
func releasedPurchaserObs(parked time.Duration) Observation {
	obs := stalledPurchaserObs(parked)
	obs.CommandFrigatePurchasing = false
	return obs
}

// Outcome 1 — the seed is still pending: the hull goes back to being the BUY SHIP, not out on a tour.
func TestBootstrap_StalledPurchaser_ReturnsToPurchasingWhenTheSeedIsStillPending(t *testing.T) {
	obs := releasedPurchaserObs(0) // the contract leg just ended, so the clock is fresh
	obs.Treasury = 2_000_000       // ...and the treasury recovered while it was on loan
	ret := &fakeRetirer{}
	h := starvedHandler(obs, ret, stalledAcquirer(), &fakeContractRunner{}, &fakeHandoff{})

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.dedications) != 1 || ret.dedications[0] != "FRIGATE-1" || !res.FrigateBuyShipRestored {
		t.Fatalf("with the trade seed still unbought the returning frigate must be restored as the exclusive BUY SHIP, by symbol; purchasing=%v restored=%v blocker=%q", ret.dedications, res.FrigateBuyShipRestored, res.Blocker)
	}
	if len(ret.tradeDedications) != 0 || res.FrigateTrading {
		t.Fatalf("it must NOT be handed to the trade fleet — that abandons the hauler-buy sequencing; trade=%v", ret.tradeDedications)
	}
}

// Outcome 2 — the pivot finished while it was on loan: no buy left, so it goes home to TRADE.
func TestBootstrap_StalledPurchaser_ReturnsToTradeWhenTheSeedCompletedWhileOnLoan(t *testing.T) {
	obs := releasedPurchaserObs(0)
	obs.Treasury = 2_000_000
	obs.TradeHullCount = 1 // seeded while the frigate was away
	ret := &fakeRetirer{}
	h := starvedHandler(obs, ret, stalledAcquirer(), &fakeContractRunner{}, &fakeHandoff{})

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.tradeDedications) != 1 || ret.tradeDedications[0] != "FRIGATE-1" || !res.FrigateTrading {
		t.Fatalf("with the cold-start buys done the frigate must go back to the TRADE fleet; trade=%v FrigateTrading=%v blocker=%q", ret.tradeDedications, res.FrigateTrading, res.Blocker)
	}
	if len(ret.dedications) != 0 || res.FrigateBuyShipRestored {
		t.Fatalf("nothing wants a buy ship any more, so none must be re-dedicated; purchasing=%v", ret.dedications)
	}
}

// While the stall persists the hull stays UNTAGGED: re-tagging it would take it back before the
// contract coordinator's own pass ran. One predicate drives both steps, which is what forbids that.
func TestBootstrap_StalledPurchaser_StaysUntaggedWhileTheBuyIsOutOfReach(t *testing.T) {
	obs := releasedPurchaserObs(frigateStarvedDwell + time.Minute)
	ret := &fakeRetirer{}
	h := starvedHandler(obs, ret, stalledAcquirer(), &fakeContractRunner{}, &fakeHandoff{})

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.tradeDedications) != 0 || len(ret.dedications) != 0 {
		t.Fatalf("the open fallback must hold BOTH re-dedications off; trade=%v purchasing=%v", ret.tradeDedications, ret.dedications)
	}
	if res.FrigateTrading || res.FrigateBuyShipRestored {
		t.Fatalf("neither re-dedication may be reported while the window is open; trading=%v restored=%v", res.FrigateTrading, res.FrigateBuyShipRestored)
	}
}

// Outcome 3 — NOBODY took the offer and the buy never came back within reach (sp-uuejg). The stall's
// capital test can never close on its own here: the one contract available is blocked by the SAME empty
// treasury, so nothing earns and the treasury cannot move. The offer lapses on the trade half's own
// window and the hull goes TRADING — the income that is gated on neither this buy nor this contract.
func TestBootstrap_StalledPurchaser_UnclaimedOfferLapsesToTrade(t *testing.T) {
	obs := releasedPurchaserObs(frigateStarvedDwell + frigateContractFallbackWindow + time.Second)
	ret := &fakeRetirer{}
	h := starvedHandler(obs, ret, stalledAcquirer(), &fakeContractRunner{}, &fakeHandoff{})

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if len(ret.tradeDedications) != 1 || ret.tradeDedications[0] != "FRIGATE-1" || !res.FrigateTrading {
		t.Fatalf("an offer no contract leg ever took must LAPSE to trade rather than hold the hull in limbo for good; trade=%v trading=%v blocker=%q", ret.tradeDedications, res.FrigateTrading, res.Blocker)
	}
	if len(ret.dedications) != 0 || res.FrigateBuyShipRestored {
		t.Fatalf("PURCHASING is the state that stalled — re-entering it changes nothing; purchasing=%v restored=%v", ret.dedications, res.FrigateBuyShipRestored)
	}
}

// The offer stays open for the WHOLE window first — the lapse is the exit from a dead end, not a
// shortcut past the contract pool's own next pass.
func TestBootstrap_StalledPurchaser_OfferHoldsForTheFullWindow(t *testing.T) {
	obs := releasedPurchaserObs(frigateStarvedDwell + frigateContractFallbackWindow - time.Second)
	ret := &fakeRetirer{}
	h := starvedHandler(obs, ret, stalledAcquirer(), &fakeContractRunner{}, &fakeHandoff{})

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.tradeDedications) != 0 || len(ret.dedications) != 0 || res.FrigateTrading {
		t.Fatalf("one second inside the window the hull is still offered to contracts; trade=%v purchasing=%v", ret.tradeDedications, ret.dedications)
	}
}

// CAPITAL RECOVERY IS UNCHANGED, on both sides of the lapse: the buy coming back within reach is the
// stall's real close, and it still takes the hull straight back to the yard. Only the UNRESOLVED
// timeout is what this bead re-answers.
func TestBootstrap_StalledPurchaser_RecoveredCapitalRestoresTheBuyShipEitherSideOfTheLapse(t *testing.T) {
	ages := map[string]time.Duration{
		"inside the window": frigateStarvedDwell + time.Minute,
		"after it lapsed":   frigateStarvedDwell + frigateContractFallbackWindow + time.Minute,
	}
	for when, parked := range ages {
		obs := releasedPurchaserObs(parked)
		obs.Treasury = 2_000_000 // the ask is comfortably inside the working-capital floor again
		ret := &fakeRetirer{}
		h := starvedHandler(obs, ret, stalledAcquirer(), &fakeContractRunner{}, &fakeHandoff{})

		res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
		if len(ret.dedications) != 1 || !res.FrigateBuyShipRestored {
			t.Fatalf("capital recovering %s must still restore the exclusive BUY SHIP — the lapse only answers a stall that never resolved; purchasing=%v restored=%v blocker=%q", when, ret.dedications, res.FrigateBuyShipRestored, res.Blocker)
		}
		if len(ret.tradeDedications) != 0 || res.FrigateTrading {
			t.Fatalf("an affordable seed is not a lapse case: the hull belongs at the yard, got trade=%v", ret.tradeDedications)
		}
	}
}

// A stall found long after the fact — a fresh era's frigate has NO run on record, so it reads parked
// since the era began — must still be UNPINNED from the buy it cannot make. The clock bounds how long
// the hull then waits untagged, never whether the release may fire: a purchasing tag nothing can use
// would otherwise be permanent, which is the very stall the release exists to end.
func TestBootstrap_StalledPurchaser_ReleaseIsNeverGatedByTheLapse(t *testing.T) {
	obs := stalledPurchaserObs(0)
	obs.CommandFrigateLastRunStart = time.Time{}
	obs.CommandFrigateLastRunEnd = time.Time{}
	ret := &fakeRetirer{}
	h := starvedHandler(obs, ret, stalledAcquirer(), &fakeContractRunner{}, &fakeHandoff{})

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.ships) != 1 || ret.ships[0] != "FRIGATE-1" || !res.FrigateContractFallback {
		t.Fatalf("a buy ship with no run on record is stalled at every park age and must still be released; retires=%v fallback=%v blocker=%q", ret.ships, res.FrigateContractFallback, res.Blocker)
	}
}

// ONE window, ONE pair of constants (RULINGS #5): the purchasing half lapses exactly where the trade
// half does, pinned from both sides. The SHAPE test stays unbounded — that is the release's, not the
// wait's.
func TestBootstrap_ContractFallback_BothHalvesLapseOnTheSameBound(t *testing.T) {
	lapse := frigateStarvedDwell + frigateContractFallbackWindow

	if !frigateBuyShipFallbackOpen(releasedPurchaserObs(lapse-time.Second), stalledSeedAsk, starvedNow) {
		t.Fatalf("the purchasing half must stay open for the whole %s", lapse)
	}
	if frigateBuyShipFallbackOpen(releasedPurchaserObs(lapse), stalledSeedAsk, starvedNow) {
		t.Fatalf("the purchasing half must lapse at %s, the trade half's own bound", lapse)
	}
	if !frigateContractFallbackOpen(starvedObs(lapse-time.Second), starvedNow) {
		t.Fatalf("the trade half's bound is unchanged: still open one second inside %s", lapse)
	}
	if frigateContractFallbackOpen(starvedObs(lapse), starvedNow) {
		t.Fatalf("the trade half's bound is unchanged: still lapsed at %s", lapse)
	}
	if !frigateBuyShipStalled(stalledPurchaserObs(lapse+time.Hour), stalledSeedAsk, starvedNow) {
		t.Fatalf("the stall SHAPE must not lapse — a hull pinned to a buy it cannot make is stalled at every park age")
	}
}

// OSCILLATION. Once the lapse hands the hull to trade it STAYS there while capital is short: the
// first-hauler pivot is the only step that conscripts an idle-in-trade frigate into purchasing, and it
// is scoped to a hull-less operation (len(Haulers)==0) — the exact opposite of the stall's shape, which
// needs hauler #1 owned. The two are mutually exclusive by construction, so trade → stall → lapse →
// trade cannot thrash, and no dampening guard is needed.
func TestBootstrap_StalledPurchaser_TradingFrigateIsNotReConscriptedWhileCapitalIsShort(t *testing.T) {
	obs := releasedPurchaserObs(frigateStarvedDwell + frigateContractFallbackWindow + time.Minute)
	obs.CommandFrigateOnTrade = true // where the lapse just put it
	ret, acq := &fakeRetirer{}, stalledAcquirer()
	h := starvedHandler(obs, ret, acq, &fakeContractRunner{}, &fakeHandoff{})

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.dedications) != 0 || res.FrigatePivoted {
		t.Fatalf("nothing may pull the hull back into purchasing while the buy is still out of reach — that is the thrash; purchasing=%v pivoted=%v", ret.dedications, res.FrigatePivoted)
	}
	if len(ret.ships) != 0 || res.FrigateContractFallback {
		t.Fatalf("nor may it be re-offered to contracts on a park age the window already lapsed on; retires=%v fallback=%v", ret.ships, res.FrigateContractFallback)
	}
	if acq.buys != 0 || acq.dedicateBuys != 0 {
		t.Fatalf("and the money guard still buys nothing at this treasury; buys=%d seeds=%d", acq.buys, acq.dedicateBuys)
	}
}

// A laden hull goes to trade, which is what sells the hold; the restore is for an EMPTY frigate only.
func TestBootstrap_StalledPurchaser_LadenReturnGoesToTradeToSellItsHold(t *testing.T) {
	obs := releasedPurchaserObs(frigateStarvedDwell + time.Minute)
	obs.FrigateCargoEmpty = false
	ret := &fakeRetirer{}
	h := starvedHandler(obs, ret, stalledAcquirer(), &fakeContractRunner{}, &fakeHandoff{})

	h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.tradeDedications) != 1 || len(ret.dedications) != 0 {
		t.Fatalf("a frigate coming back laden must go to trade to sell its hold, not stand at a yard; trade=%v purchasing=%v", ret.tradeDedications, ret.dedications)
	}
}

// A graduated player buys no cold-start hulls, so their frigate is never pinned as a buy ship.
func TestBootstrap_StalledPurchaser_GraduatedPlayerGetsNoBuyShipPin(t *testing.T) {
	obs := releasedPurchaserObs(frigateStarvedDwell + time.Minute)
	obs.ContractGraduated = true
	ret := &fakeRetirer{}
	h := starvedHandler(obs, ret, stalledAcquirer(), &fakeContractRunner{}, &fakeHandoff{})

	h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.dedications) != 0 || len(ret.tradeDedications) != 1 {
		t.Fatalf("a graduated player's frigate must go to trade, never be pinned as a buy ship; purchasing=%v trade=%v", ret.dedications, ret.tradeDedications)
	}
}

// --- the "capital ops outrank one opportunistic leg" guard, read at both hauler counts ---

// With hauler #1 owned the guard's len(Haulers)==0 term is false, so it does NOT block — the live case.
func TestBootstrap_StalledPurchaser_ZeroHaulerGuardDoesNotBlockOnceAHaulerExists(t *testing.T) {
	obs := stalledPurchaserObs(frigateStarvedDwell + time.Minute)
	if len(obs.Haulers) == 0 {
		t.Fatalf("precondition: this fixture must own hauler #1")
	}
	if !contractOpsWarranted(obs, defaultContractStartTreasuryThreshold) {
		t.Fatalf("precondition: an owned hauler must latch the contract op warranted, so the guard is genuinely exercised")
	}
	ret := &fakeRetirer{}
	h := starvedHandler(obs, ret, stalledAcquirer(), &fakeContractRunner{}, &fakeHandoff{})

	h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.ships) != 1 {
		t.Fatalf("the outranks guard is scoped to a HULL-LESS operation; with a hauler owned it must not block the release, got retires=%v", ret.ships)
	}
}

// With ZERO haulers the frigate is the pivot's only purchaser, so the fallback leaves it at its post.
// That stall keeps its own untouched cure: the stranded-purchaser release hands it back to TRADE.
func TestBootstrap_StalledPurchaser_ZeroHaulerPivotKeepsItsPurchaser(t *testing.T) {
	obs := stalledPurchaserObs(frigateStarvedDwell + time.Minute)
	obs.Haulers = nil
	obs.Treasury = 423_434 // 423434 − 313730 = 109704, under the 150k working-capital floor
	ret := &fakeRetirer{}
	h := starvedHandler(obs, ret, stalledAcquirer(), &fakeContractRunner{}, &fakeHandoff{})

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.ships) != 0 || res.FrigateContractFallback {
		t.Fatalf("with no hauler owned the frigate is the pivot's purchaser; the contract fallback must not take it, got retires=%v fallback=%v", ret.ships, res.FrigateContractFallback)
	}
	if !res.PurchaserReleased || len(ret.tradeDedications) != 1 {
		t.Fatalf("the zero-hauler strand keeps its own cure — back to TRADE to earn its way to the ask; released=%v trade=%v blocker=%q", res.PurchaserReleased, ret.tradeDedications, res.Blocker)
	}
}

// A contract leg leaves the frigate wherever it ended, and a yard prices only while something stands
// at it — so the seed answers a cold yard by SENDING its named buyer, as the hauler buy already does.
// Without this the fallback would trade an idle stall for a permanently unbuyable trade seed.
func TestBootstrap_TradeSeed_ColdYard_SendsTheBuyShipBackToTheShipyard(t *testing.T) {
	obs := stalledPurchaserObs(0)
	obs.Treasury = 2_000_000
	acq := &fakeHaulerAcquirer{price: stalledSeedAsk, yard: "X1-YARD", readable: false} // the frigate is away: cold
	scanner := &fakeScanner{dispatched: true}
	h := starvedHandler(obs, &fakeRetirer{}, acq, &fakeContractRunner{}, &fakeHandoff{})
	h.SetShipyardScanner(scanner)

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if scanner.calls != 1 || len(scanner.purchasers) != 1 || scanner.purchasers[0] != "FRIGATE-1" {
		t.Fatalf("a cold yard on the trade seed must send its named buy ship BY SYMBOL; calls=%d purchasers=%v blocker=%q", scanner.calls, scanner.purchasers, res.Blocker)
	}
	if acq.dedicateBuys != 0 {
		t.Fatalf("an unreadable price still buys NOTHING this tick (RULINGS #4 fail-closed); seeds=%d", acq.dedicateBuys)
	}
}

// --- THE DEADLOCK ARC: stalled → offered → nobody takes it → lapsed → TRADING ---

// The live wedge end to end. The treasury never moves across it because in this shape nothing can move
// it: the one contract available is blocked by the same 13390cr that blocks the buy, so the frigate is
// the only hull left that can go and earn.
func TestBootstrap_StalledPurchaser_UnclaimedArcEndsInTrade(t *testing.T) {
	ret, acq := &fakeRetirer{}, stalledAcquirer()
	h := starvedHandler(stalledPurchaserObs(frigateStarvedDwell), ret, acq, &fakeContractRunner{}, &fakeHandoff{})

	// (1) The dwell is up: the dedication is cleared and the hull is offered to the contract pool.
	if res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); !res.FrigateContractFallback {
		t.Fatalf("tick 1 must offer the stalled buy ship to contracts (blocker=%q)", res.Blocker)
	}

	// (2) Mid-window the pool still cannot use it — its own work is capital-blocked — so no leg ever
	// claims the hull and the clock, which advances only when a run ENDS, keeps counting.
	h.SetWorldObserver(&fakeObserver{obs: releasedPurchaserObs(frigateStarvedDwell + frigateContractFallbackWindow - time.Second)})
	if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if len(ret.tradeDedications) != 0 || len(ret.dedications) != 0 {
		t.Fatalf("mid-window the offer stands; trade=%v purchasing=%v", ret.tradeDedications, ret.dedications)
	}

	// (3) The window lapses with the stall unresolved: TRADE, not back to a yard it cannot buy at.
	h.SetWorldObserver(&fakeObserver{obs: releasedPurchaserObs(frigateStarvedDwell + frigateContractFallbackWindow)})
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.tradeDedications) != 1 || ret.tradeDedications[0] != "FRIGATE-1" || !res.FrigateTrading {
		t.Fatalf("tick 3 must send the hull TRADING — the deadlock's only exit; trade=%v trading=%v blocker=%q", ret.tradeDedications, res.FrigateTrading, res.Blocker)
	}
	if len(ret.dedications) != 0 || len(ret.ships) != 1 {
		t.Fatalf("each transition happens exactly once, and none of them re-enters purchasing; purchasing=%v retires=%v", ret.dedications, ret.ships)
	}
	if acq.buys != 0 || acq.dedicateBuys != 0 {
		t.Fatalf("nothing in the arc weakens the money guard: the treasury never covered the ask; buys=%d seeds=%d", acq.buys, acq.dedicateBuys)
	}
}

// --- THE ARC: stalled → lent → recovered → restored → bought ---

func TestBootstrap_StalledPurchaser_LentAndRestoredReachesTheSeed(t *testing.T) {
	obs := stalledPurchaserObs(frigateStarvedDwell + time.Minute)
	ret, acq := &fakeRetirer{}, stalledAcquirer()
	h := starvedHandler(obs, ret, acq, &fakeContractRunner{}, &fakeHandoff{})

	// (1) Stalled: the dedication is cleared and the hull is offered to the contract pool.
	if res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); !res.FrigateContractFallback {
		t.Fatalf("tick 1 must offer the stalled buy ship to contracts (blocker=%q)", res.Blocker)
	}

	// (2) Taken: the contract coordinator claims it, so it is no longer free — nothing may touch it.
	onLoan := releasedPurchaserObs(frigateStarvedDwell + 2*time.Minute)
	onLoan.CommandFrigateIdle = false
	h.SetWorldObserver(&fakeObserver{obs: onLoan})
	if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if len(ret.tradeDedications) != 0 || len(ret.dedications) != 0 {
		t.Fatalf("a hull mid-contract-leg must never be re-tagged; trade=%v purchasing=%v", ret.tradeDedications, ret.dedications)
	}

	// (3) The leg ends and it earned: the hull is restored as the BUY SHIP — protected before it is used,
	// as the pivot dedicates before it buys — and the seed lands on the same tick.
	recovered := releasedPurchaserObs(0)
	recovered.Treasury = 2_000_000
	h.SetWorldObserver(&fakeObserver{obs: recovered})
	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.dedications) != 1 || !res.FrigateBuyShipRestored {
		t.Fatalf("tick 3 must restore the buy ship; purchasing=%v restored=%v blocker=%q", ret.dedications, res.FrigateBuyShipRestored, res.Blocker)
	}
	if acq.dedicateBuys != 1 || !res.TradeHullSeeded || len(acq.dedicatePurch) != 1 || acq.dedicatePurch[0] != "FRIGATE-1" {
		t.Fatalf("tick 3 must complete the trade seed with the restored buy ship; seeds=%d seeded=%v purchasers=%v blocker=%q", acq.dedicateBuys, res.TradeHullSeeded, acq.dedicatePurch, res.Blocker)
	}
	if len(ret.ships) != 1 || len(ret.tradeDedications) != 0 {
		t.Fatalf("each transition must happen exactly once across the arc, and none of them is a trade re-tag; retires=%v trade=%v", ret.ships, ret.tradeDedications)
	}
}
