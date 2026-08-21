package commands

import (
	"testing"
)

// sp-k7o41 — GATE's COLD HOME SHIPYARD. A yard prices its hulls only while something is standing at it,
// so a gate-worker price that reads cold stays cold until somebody flies there. Every other acquisition
// answers that by SENDING a hull (the probe buy, the hauler buy, the trade seed); the gate-worker ramp
// did not, so it re-checked an unreadable price every tick forever with idle hulls sitting right there —
// live on staging: treasury 697k, haulers 4/4, gate_workers=0/4, blocker=price_unreadable, 7+
// consecutive ticks, zero progress. These pin the third caller of the SAME positioning mechanism.

// gateYardObs is a GATE observation shaped so planGateWorkers calls for exactly ONE staged worker buy and
// the capital gate would clear it — so the cold yard is the ONLY thing between the ramp and its purchase.
func gateYardObs() Observation {
	obs := gateObs()
	obs.Haulers = nHaulers(4) // the exclusive contract fleet, fully ramped — never drawn on for workers
	obs.GateWorkers = 0       // desired = gateWorkerTarget (4) > 0 ⇒ plan.Buy = 1
	obs.Treasury = 697_000    // the live staging treasury at the stall
	return obs
}

// THE LIVE REGRESSION (AC1). An unreadable gate-worker price with an idle hull available must DISPATCH
// that hull to the home shipyard, not just log and return. This is the whole stall: nothing else in the
// GATE arc ever makes the yard readable, so without the dispatch the ramp never progresses.
func TestBootstrap_Gate_ColdYard_PositionsAHullAtTheShipyard(t *testing.T) {
	acq := &fakeGateAcquirer{price: 200_000, yard: "", readable: false} // cold: nothing is standing at the yard
	scanner := &fakeScanner{dispatched: true}
	h := gateHandler(gateYardObs(), &fakeConstruction{}, &fakeManufacturing{}, &fakeRepurposer{}, acq, &fakeHandoff{})
	h.SetShipyardScanner(scanner)

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())

	if scanner.calls != 1 || len(scanner.homeSystems) != 1 || scanner.homeSystems[0] != "X1-HQ" {
		t.Fatalf("a cold gate-worker yard must send a hull to the home shipyard (the live stall: nothing else ever warms it); calls=%d systems=%v blocker=%q",
			scanner.calls, scanner.homeSystems, res.Blocker)
	}
	if res.Blocker != "positioning_purchaser_at_shipyard" {
		t.Fatalf("a dispatched hull must surface as positioning on the heartbeat, got blocker=%q", res.Blocker)
	}
	if acq.buys != 0 || res.GateWorkersBought != 0 {
		t.Fatalf("an unreadable price still buys NOTHING this tick (RULINGS #4 fail-closed), got buys=%d bought=%d", acq.buys, res.GateWorkersBought)
	}
	if acq.priceChks != 1 {
		t.Fatalf("the positioning must sit on the price-check's unreadable branch, got %d price checks", acq.priceChks)
	}
}

// WHICH OF THE TWO EXISTING SHAPES. Gate workers are pinned to no hull, so this caller takes the
// UN-NAMED form and lets hullToSend's free-hull search choose — the NAMED-purchaser shape belongs to the
// trade seed. What it does supply is a LEND candidate, because that search demands an undedicated hull
// and a matured fleet has none: without it the ramp waits on a yard nothing will ever warm. The lend
// re-tags nothing and claims nothing, so RULINGS #7 holds — no fleet loses a hull, one makes a trip.
func TestBootstrap_Gate_ColdYard_NamesNoPurchaser_ButOffersTheLendCandidate(t *testing.T) {
	obs := gateYardObs()
	obs.BorrowableHull = "H1" // every haul hull is dedicated; this one is idle between legs
	acq := &fakeGateAcquirer{price: 200_000, readable: false}
	scanner := &fakeScanner{dispatched: true}
	h := gateHandler(obs, &fakeConstruction{}, &fakeManufacturing{}, &fakeRepurposer{}, acq, &fakeHandoff{})
	h.SetShipyardScanner(scanner)

	h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())

	if len(scanner.purchasers) != 1 || scanner.purchasers[0] != "" {
		t.Fatalf("gate workers are pinned to no hull — the scanner must pick a free one itself; purchasers=%v", scanner.purchasers)
	}
	if len(scanner.borrows) != 1 || scanner.borrows[0] != "H1" {
		t.Fatalf("the ramp must offer the observed lend candidate, or the free-hull search has nothing to fall back to; borrows=%v", scanner.borrows)
	}
}

// NOTHING TO LEND IS NOT A DISPATCH. An observation that found no free cargo hull offers "", which
// leaves hullToSend's borrow fallback inert — the ramp keeps waiting rather than naming a hull it never saw.
func TestBootstrap_Gate_ColdYard_OffersNoLendWhenNothingIsFree(t *testing.T) {
	obs := gateYardObs() // BorrowableHull unset: every cargo hull is flying or claimed
	acq := &fakeGateAcquirer{price: 200_000, readable: false}
	scanner := &fakeScanner{dispatched: false}
	h := gateHandler(obs, &fakeConstruction{}, &fakeManufacturing{}, &fakeRepurposer{}, acq, &fakeHandoff{})
	h.SetShipyardScanner(scanner)

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())

	if len(scanner.borrows) != 1 || scanner.borrows[0] != "" {
		t.Fatalf("no observed candidate must reach the scanner as no lend; borrows=%v", scanner.borrows)
	}
	if res.Blocker != "price_unreadable" || acq.buys != 0 {
		t.Fatalf("nothing dispatched ⇒ still waiting on the read, still spending nothing; blocker=%q buys=%d", res.Blocker, acq.buys)
	}
}

// THE STALL ACTUALLY CLEARS. Tick 0 meets a cold yard and positions a hull; the hull arrives, so tick 1
// reads a live price and buys the staged worker. This is the bounded, observable progress the live
// environment is waiting on — proof the fix is a self-clearing arc, not merely one extra port call.
func TestBootstrap_Gate_ColdYard_PositionsThenBuysNextTick(t *testing.T) {
	acq := &fakeGateAcquirer{price: 200_000, yard: "X1-HQ-YARD", readable: false} // starts cold
	scanner := &fakeScanner{dispatched: true, readyGate: acq}                     // the hull arrives → the price reads next tick
	h := gateHandler(gateYardObs(), &fakeConstruction{}, &fakeManufacturing{}, &fakeRepurposer{}, acq, &fakeHandoff{})
	h.SetShipyardScanner(scanner)

	res0, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if acq.buys != 0 || scanner.calls != 1 {
		t.Fatalf("tick 0 (cold yard): position, do not buy; got buys=%d scanner calls=%d blocker=%q", acq.buys, scanner.calls, res0.Blocker)
	}

	res1, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if acq.buys != 1 || res1.GateWorkersBought != 1 {
		t.Fatalf("tick 1 (price now readable): the staged worker must be bought; got buys=%d bought=%d blocker=%q", acq.buys, res1.GateWorkersBought, res1.Blocker)
	}
	if scanner.calls != 1 {
		t.Fatalf("a readable yard needs no second dispatch (no re-navigation churn), got %d scanner calls", scanner.calls)
	}
}

// THE READINESS GATE STILL COMES FIRST. With no idle hull anywhere the tick blocks on no_purchaser as
// before and never reaches the yard read or the scanner — the positioning is added BELOW the readiness
// gate, not around it, so a fleet with nothing free never churns a navigation it cannot honor.
func TestBootstrap_Gate_NoIdleHull_BlocksBeforeConsultingTheScanner(t *testing.T) {
	obs := gateYardObs()
	obs.HasIdlePurchaser = false
	acq := &fakeGateAcquirer{price: 200_000, readable: false}
	scanner := &fakeScanner{dispatched: true}
	h := gateHandler(obs, &fakeConstruction{}, &fakeManufacturing{}, &fakeRepurposer{}, acq, &fakeHandoff{})
	h.SetShipyardScanner(scanner)

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())

	if res.Blocker != "no_purchaser" {
		t.Fatalf("no idle hull must still block on no_purchaser, got %q", res.Blocker)
	}
	if scanner.calls != 0 || acq.priceChks != 0 {
		t.Fatalf("the readiness gate blocks cheaply — no yard read, no dispatch; got price checks=%d scanner calls=%d", acq.priceChks, scanner.calls)
	}
}

// RULINGS #4 — THE CAPITAL GATE BELOW THE PRICE-CHECK IS UNTOUCHED (AC3), and the positioning is
// strictly the unreadable branch's business: with a READABLE price the scanner is never consulted, and
// the cushion≥floor comparison still governs exactly at the boundary — one credit short refuses, exactly
// at the floor buys. A positioning call that leaked past the price-check would show up as a scanner
// consult on the affordable side.
func TestBootstrap_Gate_ReadableYard_CushionBoundaryStillGovernsAndNothingIsDispatched(t *testing.T) {
	const price = int64(200_000)

	short := gateYardObs()
	short.Treasury = price + contractWorkingCapitalFloor - 1
	shortAcq := &fakeGateAcquirer{price: price, yard: "X1-HQ-YARD", readable: true}
	shortScanner := &fakeScanner{dispatched: true}
	hShort := gateHandler(short, &fakeConstruction{}, &fakeManufacturing{}, &fakeRepurposer{}, shortAcq, &fakeHandoff{})
	hShort.SetShipyardScanner(shortScanner)

	resShort, _ := hShort.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if shortAcq.buys != 0 || resShort.Blocker != "capital_gate" {
		t.Fatalf("cushion=%d is one credit under the floor %d — the buy must be REFUSED on the capital gate; buys=%d blocker=%q",
			short.Treasury-price, contractWorkingCapitalFloor, shortAcq.buys, resShort.Blocker)
	}
	if shortScanner.calls != 0 {
		t.Fatalf("a readable yard must dispatch NOTHING — the positioning belongs to the unreadable branch alone; got %d scanner calls", shortScanner.calls)
	}

	atFloor := gateYardObs()
	atFloor.Treasury = price + contractWorkingCapitalFloor
	floorAcq := &fakeGateAcquirer{price: price, yard: "X1-HQ-YARD", readable: true}
	floorScanner := &fakeScanner{dispatched: true}
	hFloor := gateHandler(atFloor, &fakeConstruction{}, &fakeManufacturing{}, &fakeRepurposer{}, floorAcq, &fakeHandoff{})
	hFloor.SetShipyardScanner(floorScanner)

	if _, _ = hFloor.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); floorAcq.buys != 1 {
		t.Fatalf("cushion exactly at the floor %d must buy (proving the refusal above is the floor, not a dead ramp), got %d buys", contractWorkingCapitalFloor, floorAcq.buys)
	}
	if floorScanner.calls != 0 {
		t.Fatalf("the buying tick dispatches nothing either, got %d scanner calls", floorScanner.calls)
	}
}

// AC2 — THE THREE CALLERS KEEP THEIR DISTINCT SHAPES. The trade seed is pinned to ONE hull and must
// still send it BY SYMBOL; the gate ramp is pinned to none and must still name nobody. A third caller
// that converged the two would either strand the seed's committed buy ship or make the gate ramp demand a
// hull it never had.
func TestBootstrap_ColdYard_TradeSeedNamesItsBuyShip_GateWorkerNamesNobody(t *testing.T) {
	seedObs := stalledPurchaserObs(0)
	seedObs.Treasury = 2_000_000
	seedAcq := &fakeHaulerAcquirer{price: stalledSeedAsk, yard: "X1-YARD", readable: false} // the frigate is away: cold
	seedScanner := &fakeScanner{dispatched: true}
	hSeed := starvedHandler(seedObs, &fakeRetirer{}, seedAcq, &fakeContractRunner{}, &fakeHandoff{})
	hSeed.SetShipyardScanner(seedScanner)

	resSeed, _ := hSeed.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if seedScanner.calls != 1 || len(seedScanner.purchasers) != 1 || seedScanner.purchasers[0] != "FRIGATE-1" {
		t.Fatalf("the trade seed still sends its NAMED buy ship by symbol; calls=%d purchasers=%v blocker=%q",
			seedScanner.calls, seedScanner.purchasers, resSeed.Blocker)
	}

	gateAcq := &fakeGateAcquirer{price: 200_000, readable: false}
	gateScanner := &fakeScanner{dispatched: true}
	hGate := gateHandler(gateYardObs(), &fakeConstruction{}, &fakeManufacturing{}, &fakeRepurposer{}, gateAcq, &fakeHandoff{})
	hGate.SetShipyardScanner(gateScanner)

	resGate, _ := hGate.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if gateScanner.calls != 1 || len(gateScanner.purchasers) != 1 || gateScanner.purchasers[0] != "" {
		t.Fatalf("the gate ramp names nobody and leaves the free-hull search to choose; calls=%d purchasers=%v blocker=%q",
			gateScanner.calls, gateScanner.purchasers, resGate.Blocker)
	}
}
