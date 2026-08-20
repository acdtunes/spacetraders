package commands

import (
	"testing"
	"time"
)

// THE SECOND PIVOT (sp-l2tsg). Live on staging: hauler #1 bought and working, treasury past 735k, the
// frigate idle and empty between PROFITABLE tours — and the trade seed (acquisition #2) stuck for many
// minutes on "price unreadable… none is free to send", because the only hull that buy names by symbol was
// sitting in the TRADE fleet and nothing could take it back out.
//
// ensureFrigateTrading returned unconditionally on CommandFrigateOnTrade, so once the frigate was on
// trade the function did nothing further for it — including its own restore-to-purchasing branch. The
// first-hauler pivot is scoped to len(Haulers)==0 on purpose and is over by then, and maybeSeedTradeHull
// has no pivot of its own: it ASSUMED the frigate had stayed dedicated purchasing continuously since
// acquisition #1. sp-uuejg's lapse-to-TRADE broke that assumption — legitimately, for the deadlock it
// cured — leaving "trade" a reachable resting place with the seed still unbought and no way back.
//
// So the frigate can leave trade a SECOND time, for the trade seed alone: same safety shape as the first
// pivot (idle-in-trade, cargo-empty, never mid-tour — PLAYBOOK §9), keyed on frigateBuyShipWanted plus the
// untouched capital test. It disarms itself — the seed landing makes TradeHullCount 1 and the want false.

// tradeSeedPivotObs is the live wedge: hauler #1 owned, the trade seed unbought, the frigate back ON TRADE
// idle and empty between two PRODUCTIVE tours (so nothing here is trade starvation), and a treasury the
// seed's ask sits comfortably inside.
func tradeSeedPivotObs() Observation {
	obs := incomeObs()
	obs.BatchContractRunning = true // isolate: the contract engine is already up
	obs.GrowthRunning = true        // isolate: the early autosizer launch is a no-op
	obs.CommandFrigateID = "FRIGATE-1"
	obs.CommandFrigateOnTrade = true
	obs.CommandFrigatePurchasing = false
	obs.CommandFrigateIdle = true
	obs.FrigateCargoEmpty = true
	obs.HasIdlePurchaser = false // the real cold start: nothing else is free to buy with
	obs.Haulers = []HaulerSnapshot{{Symbol: "HAULER-1", Waypoint: "X1-HUBA"}}
	obs.TradeHullCount = 0
	obs.Treasury = 735_000 // the live treasury: ask 313_730 clears the working-capital floor with room
	obs.CommandFrigateLastRunEnd = starvedNow.Add(-2 * time.Minute)
	obs.CommandFrigateLastRunStart = obs.CommandFrigateLastRunEnd.Add(-40 * time.Minute)
	return obs
}

// coldSeedYard is the live yard: nothing is standing at it, so the read fails — but it has priced a hauler
// before (contract #1 was bought there), which is the evidence the capital test weighs.
func coldSeedYard() *fakeHaulerAcquirer {
	return &fakeHaulerAcquirer{price: stalledSeedAsk, yard: "X1-YARD", readable: false, lastAsk: stalledSeedAsk}
}

// --- AC1: THE REGRESSION. The stuck seed's frigate is pivoted back to purchasing ---

func TestBootstrap_TradeSeedPivot_IdleOnTradeWithTheSeedUnboughtBecomesTheBuyShip(t *testing.T) {
	obs := tradeSeedPivotObs()
	ret, acq := &fakeRetirer{}, coldSeedYard()
	h := starvedHandler(obs, ret, acq, &fakeContractRunner{}, &fakeHandoff{})

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if len(ret.dedications) != 1 || ret.dedications[0] != "FRIGATE-1" || !res.FrigateBuyShipRestored {
		t.Fatalf("an idle-in-trade, empty frigate with the trade seed still unbought and affordable NOW must be pivoted back to the EXCLUSIVE buy ship, by symbol — otherwise the seed is stuck forever; purchasing=%v restored=%v blocker=%q", ret.dedications, res.FrigateBuyShipRestored, res.Blocker)
	}
	if len(ret.ships) != 0 || res.FrigateContractFallback {
		t.Fatalf("the pivot clears no dedication — it writes one; retires=%v fallback=%v", ret.ships, res.FrigateContractFallback)
	}
	if len(ret.tradeDedications) != 0 || res.FrigateTrading {
		t.Fatalf("the same tick must not hand it straight back to trade; trade=%v FrigateTrading=%v", ret.tradeDedications, res.FrigateTrading)
	}
}

// The point of the pivot: with the yard readable the SAME tick buys the seed, using the frigate.
func TestBootstrap_TradeSeedPivot_ReadableYardSeedsOnThePivotTick(t *testing.T) {
	obs := tradeSeedPivotObs()
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: stalledSeedAsk, yard: "X1-YARD", readable: true}
	ho := &fakeHandoff{}
	h := starvedHandler(obs, ret, acq, &fakeContractRunner{}, ho)

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.dedications) != 1 || !res.FrigateBuyShipRestored {
		t.Fatalf("the pivot must fire before the seed runs; purchasing=%v restored=%v blocker=%q", ret.dedications, res.FrigateBuyShipRestored, res.Blocker)
	}
	if acq.dedicateBuys != 1 || !res.TradeHullSeeded {
		t.Fatalf("the trade seed must then actually BUY: seeds=%d seeded=%v blocker=%q", acq.dedicateBuys, res.TradeHullSeeded, res.Blocker)
	}
	if len(acq.dedicatePurch) != 1 || acq.dedicatePurch[0] != "FRIGATE-1" {
		t.Fatalf("the seed must buy with the pivoted frigate, by symbol, got %v", acq.dedicatePurch)
	}
	if ho.tradeCoord < 1 {
		t.Fatalf("the seeded hull must have a coordinator to tour it, got %d launches", ho.tradeCoord)
	}
}

// --- AC2: PLAYBOOK §9. A hull that is not honestly free is never taken ---

func TestBootstrap_TradeSeedPivot_NeverTakesAFrigateThatIsNotFree(t *testing.T) {
	unfree := map[string]func(*Observation){
		"mid-tour (still flying)":     func(o *Observation) { o.CommandFrigateIdle = false },
		"claimed by a running tour":   func(o *Observation) { o.CommandFrigateIdle = false },
		"laden (a tour sells its ho)": func(o *Observation) { o.FrigateCargoEmpty = false },
		"flying AND laden":            func(o *Observation) { o.CommandFrigateIdle, o.FrigateCargoEmpty = false, false },
	}

	for name, makeUnfree := range unfree {
		obs := tradeSeedPivotObs()
		makeUnfree(&obs)
		ret, acq := &fakeRetirer{}, coldSeedYard()
		h := starvedHandler(obs, ret, acq, &fakeContractRunner{}, &fakeHandoff{})

		h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
		if len(ret.dedications) != 0 || len(ret.ships) != 0 {
			t.Fatalf("a frigate %s must never be reassigned for the seed (PLAYBOOK §9): purchasing=%v retires=%v", name, ret.dedications, ret.ships)
		}
	}
}

// --- AC3: RULINGS #4. The capital test is READ, never moved ---

func TestBootstrap_TradeSeedPivot_HeldWhileTheSeedIsOutOfReach(t *testing.T) {
	obs := tradeSeedPivotObs()
	obs.Treasury = 400_000 // cushion (400_000−313_730) sits under the working-capital floor
	ret, acq := &fakeRetirer{}, coldSeedYard()
	h := starvedHandler(obs, ret, acq, &fakeContractRunner{}, &fakeHandoff{})

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.dedications) != 0 || res.FrigateBuyShipRestored {
		t.Fatalf("a seed the treasury cannot cover must NOT take the earner off trade — stopping the only thing earning is how the treasury never reaches the ask; purchasing=%v restored=%v", ret.dedications, res.FrigateBuyShipRestored)
	}
	if len(ret.tradeDedications) != 0 || len(ret.ships) != 0 {
		t.Fatalf("and it stays exactly where it is, trading; trade=%v retires=%v", ret.tradeDedications, ret.ships)
	}
	if acq.dedicateBuys != 0 {
		t.Fatalf("nothing is bought under the floor, got %d seeds", acq.dedicateBuys)
	}
}

// A yard that has never priced a hauler reports 0 — an absence of evidence. The first pivot reads it the
// same way (no evidence is no reason to hold), so the frigate goes and warms the yard.
func TestBootstrap_TradeSeedPivot_NoAskOnRecordStillPivots(t *testing.T) {
	obs := tradeSeedPivotObs()
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: stalledSeedAsk, yard: "X1-YARD", readable: false} // lastAsk 0
	h := starvedHandler(obs, ret, acq, &fakeContractRunner{}, &fakeHandoff{})

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.dedications) != 1 || !res.FrigateBuyShipRestored {
		t.Fatalf("with no ask on record there is nothing to hold against, so the buy ship is claimed and the yard gets warmed; purchasing=%v restored=%v blocker=%q", ret.dedications, res.FrigateBuyShipRestored, res.Blocker)
	}
}

// --- AC4: the trigger disarms itself the moment the seed lands ---

func TestBootstrap_TradeSeedPivot_StopsFiringOnceTheSeedIsBought(t *testing.T) {
	obs := tradeSeedPivotObs()
	obs.TradeHullCount = 1 // the seed landed → frigateBuyShipWanted goes false on its own
	ret, acq := &fakeRetirer{}, coldSeedYard()
	h := starvedHandler(obs, ret, acq, &fakeContractRunner{}, &fakeHandoff{})

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.dedications) != 0 || res.FrigateBuyShipRestored {
		t.Fatalf("with the trade hull owned there is no buy left to stand by for — the frigate must be left TRADING; purchasing=%v restored=%v", ret.dedications, res.FrigateBuyShipRestored)
	}
	if len(ret.ships) != 0 || len(ret.tradeDedications) != 0 {
		t.Fatalf("and it is not re-tagged either; retires=%v trade=%v", ret.ships, ret.tradeDedications)
	}
	if acq.dedicateBuys != 0 || res.TradeHullSeeded {
		t.Fatalf("nor re-seeded; seeds=%d seeded=%v", acq.dedicateBuys, res.TradeHullSeeded)
	}
}

// A graduated player buys no cold-start hull at all, so its frigate is never pinned to one.
func TestBootstrap_TradeSeedPivot_NeverFiresForAGraduatedPlayer(t *testing.T) {
	obs := tradeSeedPivotObs()
	obs.ContractGraduated = true
	ret := &fakeRetirer{}
	h := starvedHandler(obs, ret, coldSeedYard(), &fakeContractRunner{}, &fakeHandoff{})

	h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.dedications) != 0 {
		t.Fatalf("a graduated player's frigate must never be pinned to a buy that will not happen, got %v", ret.dedications)
	}
}

// --- AC5: the two mechanisms this pivot sits between are untouched ---

// The FIRST pivot still owns hauler #1: with zero haulers the seed's want is false, so the new trigger is
// silent and maybeBuyHauler's own len(Haulers)==0 pivot does the work exactly as before.
func TestBootstrap_TradeSeedPivot_FirstHaulerPivotUnchanged(t *testing.T) {
	obs := tradeSeedPivotObs()
	obs.Haulers = nil // acquisition #1: the first-hauler pivot's own scope
	obs.Treasury = 2_000_000
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 300_000, yard: "X1-YARD", readable: true}
	h := starvedHandler(obs, ret, acq, &fakeContractRunner{}, &fakeHandoff{})

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if !res.FrigatePivoted || len(ret.dedications) != 1 || ret.dedications[0] != "FRIGATE-1" {
		t.Fatalf("the first-hauler pivot must still fire, once, from maybeBuyHauler; pivoted=%v purchasing=%v blocker=%q", res.FrigatePivoted, ret.dedications, res.Blocker)
	}
	if res.FrigateBuyShipRestored {
		t.Fatalf("and it must NOT be the trade-seed pivot doing it — acquisition #1 is not the seed's")
	}
	if acq.buys != 1 || res.HaulersBought != 1 || acq.dedicateBuys != 0 {
		t.Fatalf("acquisition #1 is still a CONTRACT hull bought by the pivoted frigate; buys=%d haulers=%d seeds=%d", acq.buys, res.HaulersBought, acq.dedicateBuys)
	}
}

// The STARVED-TRADE fallback still owns a starved frigate: its window is the proven mechanism for that
// shape, and the new pivot defers to it rather than writing a tag the same tick would clear.
func TestBootstrap_TradeSeedPivot_StarvedTradeFallbackUnchanged(t *testing.T) {
	obs := tradeSeedPivotObs()
	obs.CommandFrigateLastRunEnd = starvedNow.Add(-(frigateStarvedDwell + time.Minute))
	obs.CommandFrigateLastRunStart = obs.CommandFrigateLastRunEnd.Add(-20 * time.Second) // a fast-fail tour
	ret := &fakeRetirer{}
	h := starvedHandler(obs, ret, coldSeedYard(), &fakeContractRunner{}, &fakeHandoff{})

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.ships) != 1 || ret.ships[0] != "FRIGATE-1" || !res.FrigateContractFallback {
		t.Fatalf("a starved frigate must still be offered to the contract pool, unchanged; retires=%v fallback=%v blocker=%q", ret.ships, res.FrigateContractFallback, res.Blocker)
	}
	if len(ret.dedications) != 0 || res.FrigateBuyShipRestored {
		t.Fatalf("the seed pivot must not fight that offer by re-tagging the same hull on the same tick; purchasing=%v restored=%v", ret.dedications, res.FrigateBuyShipRestored)
	}
}

// …and once that offer LAPSES the seed is still not stranded: the untagged frigate comes back through
// ensureFrigateTrading's own restore branch, which is the path sp-uuejg left in place.
func TestBootstrap_TradeSeedPivot_LapsedStarvedOfferStillReturnsToTheBuyShip(t *testing.T) {
	obs := tradeSeedPivotObs()
	obs.CommandFrigateOnTrade = false // the fallback cleared the tag
	obs.CommandFrigateLastRunEnd = starvedNow.Add(-(frigateStarvedDwell + frigateContractFallbackWindow + time.Second))
	obs.CommandFrigateLastRunStart = obs.CommandFrigateLastRunEnd.Add(-20 * time.Second)
	ret := &fakeRetirer{}
	h := starvedHandler(obs, ret, coldSeedYard(), &fakeContractRunner{}, &fakeHandoff{})

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.dedications) != 1 || !res.FrigateBuyShipRestored {
		t.Fatalf("a lapsed offer with the seed still unbought and in reach must restore the buy ship; purchasing=%v restored=%v blocker=%q", ret.dedications, res.FrigateBuyShipRestored, res.Blocker)
	}
}
