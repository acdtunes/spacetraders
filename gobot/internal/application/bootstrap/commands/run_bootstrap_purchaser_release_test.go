package commands

import (
	"errors"
	"testing"
)

// THE STRANDED PURCHASER. The first-hauler pivot stops the pre-hauler sole earner and dedicates it the
// exclusive buy ship on the evidence that hauler #1 is within reach. When the ask then sits OUTSIDE the
// working-capital floor the fleet has no earner at all: the loop is stopped, the treasury is frozen at
// whatever it held, and the ask can therefore never come within reach. Clearing the dedication is the
// whole cure — the pre-hauler loop gate reads it and restarts the earner on its own next tick.
//
// Reproduced on staging: treasury pinned at 423_434 for ~5 hours, income/hr decayed to 0, 14 contracts
// fulfilled and then nothing.

// strandedObs is that wedge: hauler #1 never bought, the frigate carrying the purchasing dedication with
// its loop stopped, and a treasury that cannot reach the ask (423_434 − 363_473 = 59_961, under the 150k
// working-capital floor).
func strandedObs() Observation {
	obs := pivotObs()
	obs.FrigateContractLoopRunning = false // the pivot stopped it
	obs.CommandFrigatePurchasing = true    // ...and dedicated the frigate the exclusive buy ship
	obs.Treasury = 423_434
	return obs
}

// The wedge itself: the dedication is cleared, by symbol, and the tick spends nothing and re-dedicates
// nothing. Clearing the tag is the entire action.
func TestBootstrap_StrandedPurchaser_ReleasedBackToEarning(t *testing.T) {
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 363_473, yard: "Y", readable: true} // parked at the yard: the ask reads
	loop := &fakeFrigateLoop{}
	h := pivotHandler(strandedObs(), ret, acq, loop)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if len(ret.ships) != 1 || ret.ships[0] != "FRIGATE-1" {
		t.Fatalf("a stranded purchasing frigate must have its dedication cleared, by symbol; ships=%v (blocker=%q)", ret.ships, res.Blocker)
	}
	if !res.PurchaserReleased {
		t.Fatalf("res.PurchaserReleased must record the release")
	}
	if acq.buys != 0 || len(ret.dedications) != 0 || loop.stopCalls != 0 {
		t.Fatalf("the release must spend nothing, re-dedicate nothing and stop nothing; buys=%d dedications=%v stopCalls=%d", acq.buys, ret.dedications, loop.stopCalls)
	}
}

// Everything OUTSIDE the wedge keeps the frigate exactly where it is. The release reads the SAME ask the
// pivot weighs, so a buy that is genuinely about to happen is never interrupted, absent evidence is never
// acted on, and a fleet that already has an earner is never touched.
func TestBootstrap_StrandedPurchaser_NotReleasedOutsideTheWedge(t *testing.T) {
	cases := []struct {
		name     string
		price    int64
		readable bool
		mutate   func(*Observation)
	}{
		{
			// 423_434 − 200_000 = 223_434 ≥ the 150k floor: the pivot is legitimately mid-purchase.
			name: "the ask is within reach", price: 200_000, readable: true,
		},
		{
			// A cold yard that has never priced a hauler reports 0 — an absence of evidence, which is no
			// more a reason to release than it is a reason to hold.
			name: "no ask on record", price: 363_473, readable: false,
		},
		{
			name: "a hauler is already earning", price: 363_473, readable: true,
			mutate: func(o *Observation) {
				o.Haulers = []HaulerSnapshot{{Symbol: "H1", Waypoint: "X1-HUBA"}}
				o.TradeHullCount = 1
			},
		},
		{
			name: "the frigate is still on its contract loop", price: 363_473, readable: true,
			mutate: func(o *Observation) { o.FrigateContractLoopRunning = true },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obs := strandedObs()
			if tc.mutate != nil {
				tc.mutate(&obs)
			}
			ret := &fakeRetirer{}
			acq := &fakeHaulerAcquirer{price: tc.price, yard: "Y", readable: tc.readable}
			h := pivotHandler(obs, ret, acq, &fakeFrigateLoop{})

			res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
			if err != nil {
				t.Fatalf("reconcileOnce: %v", err)
			}
			if res.PurchaserReleased || len(ret.ships) != 0 {
				t.Fatalf("the purchasing dedication must stand; PurchaserReleased=%v cleared=%v (blocker=%q)", res.PurchaserReleased, ret.ships, res.Blocker)
			}
		})
	}
}

// A failed clear reports NO release and leaves the frigate dedicated, so the next tick simply tries again.
func TestBootstrap_StrandedPurchaser_ClearFails_ReportsNoRelease(t *testing.T) {
	ret := &fakeRetirer{err: errors.New("assign boom")}
	acq := &fakeHaulerAcquirer{price: 363_473, yard: "Y", readable: true}
	h := pivotHandler(strandedObs(), ret, acq, &fakeFrigateLoop{})

	log := &capturingLogger{}
	res, err := h.reconcileOnce(ctxWithLogger(log), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if res.PurchaserReleased {
		t.Fatalf("a failed clear must NOT be reported as a release")
	}
	if _, ok := log.find("bootstrap_purchaser_release_error"); !ok {
		t.Fatalf("a failed clear must surface loudly, never silently")
	}
}

// THE FIXED POINT. Release and pivot are decided on the same treasury and the same ask, so they are
// mutually exclusive: the release fires only where the pivot would HOLD, and the pivot fires only where
// the release will not. This walks the whole arc — wedged, released, earning, recovered, bought — and
// pins that each transition happens EXACTLY ONCE.
//
// The tick that would ping-pong is the one after a cold-yard pivot: the frigate is dedicated and flying to
// the yard with no hauler bought yet, which is structurally identical to the wedge. What separates them is
// the ask, now within reach — so the frigate stays committed and completes the buy.
func TestBootstrap_StrandedPurchaser_ReleaseAndPivotReachAFixedPoint(t *testing.T) {
	world := &incomeWorld{
		treasury: 423_434, homeSystem: "X1", marketsTotal: 10, marketsCovered: 10,
		frigateID: "FRIGATE-1", probeCount: 3, batchRunning: true,
		frigateCargoEmpty: true, commandFrigatePurchasing: true,
		hasPurchaser:   false, // the dedicated buy ship is outside the free-hull search; nothing else is idle
		placementSlots: incomeSlots(),
	}
	acq := &fakeHaulerAcquirer{price: 363_473, yard: "X1-YARD", readable: true, world: world}
	ret := &fakeRetirer{world: world}
	loop := &fakeFrigateLoop{world: world}
	scanner := &fakeScanner{dispatched: true, readyHaul: acq, world: world}

	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(&fakeIncomeObserver{world: world})
	h.SetProbeAcquirer(&fakeAcquirer{price: 40_000, yard: "Y", readable: true})
	h.SetScoutPostDeclarer(&fakeDeclarer{})
	h.SetFrigateRetirer(ret)
	h.SetHaulerAcquirer(acq)
	h.SetContractRunner(&fakeContractRunner{world: world})
	h.SetFrigateContractLoopStarter(loop)
	h.SetShipyardScanner(scanner)

	tick := func(t *testing.T, label string) reconcileResult {
		t.Helper()
		res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		return res
	}

	// Wedged: parked at the yard with the ask out of reach → the dedication is cleared.
	if res := tick(t, "wedged tick"); !res.PurchaserReleased {
		t.Fatalf("the wedged tick must release the frigate (blocker=%q)", res.Blocker)
	}
	if world.snapshot().CommandFrigatePurchasing {
		t.Fatalf("the purchasing dedication must be gone after the release")
	}

	// The frigate leaves for contracts, so the yard stops pricing. Every later tick weighs the ask the
	// readable read above put on record — nothing is planted by hand.
	acq.readable = false

	// Earning, ask still out of reach: the loop restarts and the pivot HOLDS tick after tick. This is the
	// anti-oscillation property — a release that led straight back to a pivot would free the earner again.
	for i := 0; i < 4; i++ {
		tick(t, "earning tick")
		if loop.stopCalls != 0 {
			t.Fatalf("the pivot must not re-free the frigate the release just handed back; stopCalls=%d after earning tick %d", loop.stopCalls, i)
		}
		if len(ret.ships) != 1 {
			t.Fatalf("the release must fire exactly once; cleared=%v after earning tick %d", ret.ships, i)
		}
	}
	if loop.calls != 1 {
		t.Fatalf("the released frigate must be put back on its earning loop exactly once, got %d starts", loop.calls)
	}

	// The earner does its job: the treasury clears the ask plus the working-capital floor.
	world.earn(500_000)

	// Recovered: the pivot fires ONCE against a cold yard — frigate freed, dedicated, sent to price the hull.
	if res := tick(t, "recovered tick"); !res.FrigatePivoted {
		t.Fatalf("a recovered treasury must let the pivot fire (blocker=%q)", res.Blocker)
	}

	// The ping-pong tick: dedicated, no hauler, no loop — the wedge's exact shape, but the ask is within
	// reach now, so the frigate stays committed and buys.
	res := tick(t, "committed tick")
	if res.PurchaserReleased {
		t.Fatalf("a purchaser that is about to buy must NEVER be released — that is the ping-pong")
	}
	if res.HaulersBought != 1 {
		t.Fatalf("the committed purchaser must complete the buy; bought=%d (blocker=%q)", res.HaulersBought, res.Blocker)
	}

	if len(ret.ships) != 1 || loop.calls != 1 || loop.stopCalls != 1 || len(ret.dedications) != 1 {
		t.Fatalf("each transition must happen exactly once across the arc; released=%v loop_starts=%d loop_stops=%d dedications=%v",
			ret.ships, loop.calls, loop.stopCalls, ret.dedications)
	}
	if acq.buys != 1 || len(world.snapshot().Haulers) != 1 {
		t.Fatalf("the arc must end with hauler #1 bought; buys=%d haulers=%d", acq.buys, len(world.snapshot().Haulers))
	}
}
