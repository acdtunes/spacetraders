package commands

import (
	"errors"
	"testing"
)

// THE STRANDED PURCHASER. The first-hauler pivot takes the frigate out of the trade fleet and dedicates
// it the exclusive buy ship on the evidence that hauler #1 is within reach. When the ask then sits
// OUTSIDE the working-capital floor the frigate is neither trading nor buying: it stands idle-dedicated
// while the treasury is frozen at whatever it held, so the ask can never come within reach. Handing it
// back to the TRADE fleet is the whole cure — it resumes touring, earns, and re-pivots by itself once the
// treasury clears the floor.
//
// Reproduced on staging: treasury pinned at 423_434 for ~5 hours, income/hr decayed to 0, 14 contracts
// fulfilled and then nothing.

// strandedObs is that wedge: hauler #1 never bought, the frigate carrying the purchasing dedication out
// of the trade fleet, and a treasury that cannot reach the ask (423_434 − 363_473 = 59_961, under the
// 150k working-capital floor).
func strandedObs() Observation {
	obs := pivotObs()
	obs.CommandFrigateOnTrade = false   // the pivot took it out of trade
	obs.CommandFrigatePurchasing = true // ...and dedicated it the exclusive buy ship
	obs.Treasury = 423_434
	return obs
}

// The wedge itself: the frigate is handed back to the TRADE fleet, by symbol, and the tick spends nothing
// and re-dedicates nothing.
func TestBootstrap_StrandedPurchaser_ReleasedBackToTrade(t *testing.T) {
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 363_473, yard: "Y", readable: true} // parked at the yard: the ask reads
	h := pivotHandler(strandedObs(), ret, acq, &fakeFrigateLoop{})

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if len(ret.tradeDedications) != 1 || ret.tradeDedications[0] != "FRIGATE-1" {
		t.Fatalf("a stranded purchasing frigate must be handed back to the trade fleet, by symbol; trade=%v (blocker=%q)", ret.tradeDedications, res.Blocker)
	}
	if !res.PurchaserReleased {
		t.Fatalf("res.PurchaserReleased must record the release")
	}
	if acq.buys != 0 || len(ret.dedications) != 0 {
		t.Fatalf("the release must spend nothing and re-dedicate nothing; buys=%d dedications=%v", acq.buys, ret.dedications)
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
			// The strand is a PURCHASER's wedge; a frigate that is already trading has nothing to release.
			name: "the frigate is already trading", price: 363_473, readable: true,
			mutate: func(o *Observation) {
				o.CommandFrigatePurchasing = false
				o.CommandFrigateOnTrade = true
			},
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
			if res.PurchaserReleased || len(ret.tradeDedications) != 0 {
				t.Fatalf("the purchasing dedication must stand; PurchaserReleased=%v handed_back=%v (blocker=%q)", res.PurchaserReleased, ret.tradeDedications, res.Blocker)
			}
		})
	}
}

// A failed hand-back reports NO release and leaves the frigate dedicated, so the next tick simply tries again.
func TestBootstrap_StrandedPurchaser_HandBackFails_ReportsNoRelease(t *testing.T) {
	ret := &fakeRetirer{tradeErr: errors.New("assign boom")}
	acq := &fakeHaulerAcquirer{price: 363_473, yard: "Y", readable: true}
	h := pivotHandler(strandedObs(), ret, acq, &fakeFrigateLoop{})

	log := &capturingLogger{}
	res, err := h.reconcileOnce(ctxWithLogger(log), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if res.PurchaserReleased {
		t.Fatalf("a failed hand-back must NOT be reported as a release")
	}
	if _, ok := log.find("bootstrap_purchaser_release_error"); !ok {
		t.Fatalf("a failed hand-back must surface loudly, never silently")
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
	scanner := &fakeScanner{dispatched: true, readyHaul: acq, world: world}

	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(&fakeIncomeObserver{world: world})
	h.SetProbeAcquirer(&fakeAcquirer{price: 40_000, yard: "Y", readable: true})
	h.SetFrigateRetirer(ret)
	h.SetHaulerAcquirer(acq)
	h.SetContractRunner(&fakeContractRunner{world: world})
	h.SetHandoffLauncher(&fakeHandoff{})
	h.SetShipyardScanner(scanner)

	tick := func(t *testing.T, label string) reconcileResult {
		t.Helper()
		res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		return res
	}

	// Wedged: parked at the yard with the ask out of reach → the frigate goes back to trade.
	if res := tick(t, "wedged tick"); !res.PurchaserReleased {
		t.Fatalf("the wedged tick must release the frigate (blocker=%q)", res.Blocker)
	}
	snap := world.snapshot()
	if snap.CommandFrigatePurchasing || !snap.CommandFrigateOnTrade {
		t.Fatalf("the released frigate must be back in the trade fleet; purchasing=%v trade=%v", snap.CommandFrigatePurchasing, snap.CommandFrigateOnTrade)
	}

	// The frigate leaves for its tours, so the yard stops pricing. Every later tick weighs the ask the
	// readable read above put on record — nothing is planted by hand.
	acq.readable = false

	// Trading, ask still out of reach: the pivot HOLDS tick after tick. This is the anti-oscillation
	// property — a release that led straight back to a pivot would take the earner out again.
	for i := 0; i < 4; i++ {
		tick(t, "earning tick")
		if len(ret.dedications) != 0 {
			t.Fatalf("the pivot must not re-take the frigate the release just handed back; dedications=%v after earning tick %d", ret.dedications, i)
		}
		if len(ret.tradeDedications) != 1 {
			t.Fatalf("the release must fire exactly once; handed_back=%v after earning tick %d", ret.tradeDedications, i)
		}
	}

	// The earner does its job: the treasury clears the ask plus the working-capital floor.
	world.earn(500_000)

	// Recovered: the pivot fires ONCE against a cold yard — frigate dedicated, sent to price the hull.
	if res := tick(t, "recovered tick"); !res.FrigatePivoted {
		t.Fatalf("a recovered treasury must let the pivot fire (blocker=%q)", res.Blocker)
	}

	// The ping-pong tick: dedicated, no hauler — the wedge's exact shape, but the ask is within reach now,
	// so the frigate stays committed and buys.
	res := tick(t, "committed tick")
	if res.PurchaserReleased {
		t.Fatalf("a purchaser that is about to buy must NEVER be released — that is the ping-pong")
	}
	if res.HaulersBought != 1 {
		t.Fatalf("the committed purchaser must complete the buy; bought=%d (blocker=%q)", res.HaulersBought, res.Blocker)
	}

	if len(ret.tradeDedications) != 1 || len(ret.dedications) != 1 {
		t.Fatalf("each transition must happen exactly once across the arc; handed_back=%v dedications=%v", ret.tradeDedications, ret.dedications)
	}
	if acq.buys != 1 || len(world.snapshot().Haulers) != 1 {
		t.Fatalf("the arc must end with hauler #1 bought; buys=%d haulers=%d", acq.buys, len(world.snapshot().Haulers))
	}
}
