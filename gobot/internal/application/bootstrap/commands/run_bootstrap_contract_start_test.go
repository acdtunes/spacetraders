package commands

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
)

// THE CONTRACT-START TREASURY THRESHOLD. Cold start no longer runs the contract engine from tick 1.
// The command frigate is dedicated TRADE and toured by the standing trade-fleet coordinator from tick 1;
// the contract operation — the contract-fleet coordinator and the whole hauler ramp — waits until the
// FLAT treasury clears contract_start_treasury_threshold (default 500000, deliberately NOT netted against
// any reserve floor: a different reading from the GATE-entry surplus bar, which is untouched).
//
// The threshold gates NEW work only. Once the operation is under way — the coordinator running, a hauler
// owned, or the frigate committed mid-purchase — a treasury dip (including the one its own buys cause)
// never stands it back down: sequencing may be deferred, running contract work is never withdrawn
// (RULINGS #1). That latch is read from the live world every tick, so a restart re-derives it.

// tradeIdleHandler wires every collaborator the cold-start income workstream drives, so one tick
// exercises the whole shape: the trade dedication, the trade-coordinator ensure, the threshold gate,
// the first-hauler pivot and the staged buys.
func tradeIdleHandler(obs Observation, ret *fakeRetirer, acq *fakeHaulerAcquirer, run *fakeContractRunner, ho *fakeHandoff, loop *fakeFrigateLoop) *RunBootstrapCoordinatorHandler {
	h := newIncomeHandler(obs, ret, acq, run)
	h.SetHandoffLauncher(ho)
	if loop != nil {
		h.SetFrigateContractLoopStarter(loop)
	}
	return h
}

// tradeIdleObs is the new cold-start steady state: the frigate resolved, dedicated TRADE, genuinely idle
// (an honest inter-tour tick), cargo empty, and nothing contract-side running or owned yet.
func tradeIdleObs() Observation {
	obs := incomeObs()
	obs.CommandFrigateID = "FRIGATE-1"
	obs.CommandFrigateOnTrade = true
	obs.CommandFrigateIdle = true
	obs.FrigateCargoEmpty = true
	obs.HasIdlePurchaser = false // the real cold start: nothing else is free
	obs.BatchContractRunning = false
	obs.Haulers = nil
	obs.TradeHullCount = 0
	return obs
}

// --- (1) BELOW the threshold: the frigate trades, and NOTHING contract-side starts ---

func TestBootstrap_BelowThreshold_FrigateTradesAndNoContractOpsStart(t *testing.T) {
	obs := tradeIdleObs()
	obs.Treasury = defaultContractStartTreasuryThreshold - 1
	obs.CommandFrigateOnTrade = false // fresh era: the frigate carries no dedication yet
	ret, acq, run, ho := &fakeRetirer{}, &fakeHaulerAcquirer{price: 100_000, yard: "Y", readable: true}, &fakeContractRunner{}, &fakeHandoff{}
	h := tradeIdleHandler(obs, ret, acq, run, ho, &fakeFrigateLoop{})

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if len(ret.tradeDedications) != 1 || ret.tradeDedications[0] != "FRIGATE-1" || !res.FrigateTrading {
		t.Fatalf("below the threshold the frigate must be dedicated TRADE, by symbol; trade=%v FrigateTrading=%v (blocker=%q)", ret.tradeDedications, res.FrigateTrading, res.Blocker)
	}
	if ho.tradeCoord < 1 {
		t.Fatalf("below the threshold the trade-fleet coordinator must be ensured so the frigate actually tours, got %d", ho.tradeCoord)
	}
	if run.calls != 0 {
		t.Fatalf("below the threshold the contract-fleet coordinator must NOT launch, got %d", run.calls)
	}
	if acq.buys != 0 || acq.dedicateBuys != 0 || acq.priceChks != 0 {
		t.Fatalf("below the threshold nothing is bought and no yard is priced; buys=%d seeds=%d priceChks=%d", acq.buys, acq.dedicateBuys, acq.priceChks)
	}
	if len(ret.dedications) != 0 {
		t.Fatalf("below the threshold the frigate is never made the purchasing ship, got %v", ret.dedications)
	}
	if res.Blocker != "contract_start_deferred" {
		t.Fatalf("the deferral must be visible on the heartbeat, got blocker=%q", res.Blocker)
	}
}

// Idempotent: an already-trading frigate is not re-tagged every tick.
func TestBootstrap_BelowThreshold_AlreadyTrading_NoReDedication(t *testing.T) {
	obs := tradeIdleObs()
	obs.Treasury = 10_000
	ret := &fakeRetirer{}
	h := tradeIdleHandler(obs, ret, &fakeHaulerAcquirer{price: 100_000, yard: "Y", readable: true}, &fakeContractRunner{}, &fakeHandoff{}, &fakeFrigateLoop{})

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.tradeDedications) != 0 || res.FrigateTrading {
		t.Fatalf("a frigate already in the trade fleet must not be re-dedicated; trade=%v FrigateTrading=%v", ret.tradeDedications, res.FrigateTrading)
	}
}

// PLAYBOOK §9: a frigate mid-tour is never re-tagged — the re-dedication waits for an honest idle tick.
func TestBootstrap_BelowThreshold_MidTourFrigate_NotReTagged(t *testing.T) {
	obs := tradeIdleObs()
	obs.Treasury = 10_000
	obs.CommandFrigateOnTrade = false
	obs.CommandFrigateIdle = false // mid-tour / still flying
	ret := &fakeRetirer{}
	h := tradeIdleHandler(obs, ret, &fakeHaulerAcquirer{price: 100_000, yard: "Y", readable: true}, &fakeContractRunner{}, &fakeHandoff{}, &fakeFrigateLoop{})

	h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.tradeDedications) != 0 {
		t.Fatalf("a hull that is not genuinely free must never be re-assigned mid-task, got %v", ret.tradeDedications)
	}
}

// The scanning workstream is untouched by the threshold: probes still buy to target below it.
func TestBootstrap_BelowThreshold_ProbeWorkstreamUnaffected(t *testing.T) {
	obs := tradeIdleObs()
	obs.Treasury = 300_000
	obs.ProbeCount = 1
	obs.HasIdlePurchaser = true
	probes := &fakeAcquirer{price: 40_000, yard: "Y", readable: true}
	h := tradeIdleHandler(obs, &fakeRetirer{}, &fakeHaulerAcquirer{price: 100_000, yard: "Y", readable: true}, &fakeContractRunner{}, &fakeHandoff{}, &fakeFrigateLoop{})
	h.SetProbeAcquirer(probes)

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if probes.buys != 2 || res.Purchased != 2 {
		t.Fatalf("the probe ramp must be unaffected by the contract-start threshold: buys=%d purchased=%d", probes.buys, res.Purchased)
	}
}

// The frigate NEVER runs a dedicated contract loop in this design — below or above the threshold.
func TestBootstrap_FrigateContractLoop_NeverStarted(t *testing.T) {
	for name, treasury := range map[string]int64{
		"below the threshold": defaultContractStartTreasuryThreshold - 1,
		"above the threshold": defaultContractStartTreasuryThreshold * 4,
	} {
		t.Run(name, func(t *testing.T) {
			obs := tradeIdleObs()
			obs.Treasury = treasury
			loop := &fakeFrigateLoop{}
			h := tradeIdleHandler(obs, &fakeRetirer{}, &fakeHaulerAcquirer{price: 100_000, yard: "Y", readable: true}, &fakeContractRunner{}, &fakeHandoff{}, loop)

			h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
			if loop.calls != 0 {
				t.Fatalf("the frigate must never be put on a dedicated contract loop, got %d starts", loop.calls)
			}
		})
	}
}

// --- (1b) THE CONTRACT SCALER WAITS ON THE SAME GATE ---
//
// The scaler is the contract fleet's OTHER buyer: once running it autonomously buys contract-fleet hulls
// behind its own 200000 cushion, which a treasury far under this threshold already clears. Launching it
// below the bar would spend exactly the capex the threshold defers — and its hulls carry the contract
// fleet tag, which would then latch the whole contract workstream open. So the ensure reads the SAME gate
// the rest of the operation does: sequencing only, no money guard is touched either way (RULINGS #4/#6).

func TestBootstrap_BelowThreshold_ContractScalerIsNotLaunched(t *testing.T) {
	obs := tradeIdleObs()
	obs.Treasury = defaultContractStartTreasuryThreshold - 1
	ho := &fakeHandoff{}
	h := tradeIdleHandler(obs, &fakeRetirer{}, &fakeHaulerAcquirer{price: 300_000, yard: "Y", readable: true}, &fakeContractRunner{}, ho, &fakeFrigateLoop{})

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if res.Phase != PhaseColdStart {
		t.Fatalf("expected COLDSTART, got %s", res.Phase)
	}
	if ho.contractScaler != 0 || res.ContractScalerLaunchedEarly {
		t.Fatalf("below the threshold the contract scaler must NOT be launched — it buys contract hulls behind its own 200000 cushion, well under this bar (launches=%d early=%v)", ho.contractScaler, res.ContractScalerLaunchedEarly)
	}
	if ho.tradeCoord < 1 {
		t.Fatalf("the TRADE side is unaffected by the contract gate: the trade coordinator must still be ensured, got %d", ho.tradeCoord)
	}
}

func TestBootstrap_AtThreshold_ContractScalerIsEnsured(t *testing.T) {
	obs := tradeIdleObs()
	obs.Treasury = defaultContractStartTreasuryThreshold // exactly at the bar
	ho := &fakeHandoff{}
	h := tradeIdleHandler(obs, &fakeRetirer{}, &fakeHaulerAcquirer{price: 300_000, yard: "Y", readable: true}, &fakeContractRunner{}, ho, &fakeFrigateLoop{})

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if ho.contractScaler != 1 || !res.ContractScalerLaunchedEarly {
		t.Fatalf("at the threshold the contract scaler must be ensured — it owns contract-fleet capacity (launches=%d early=%v)", ho.contractScaler, res.ContractScalerLaunchedEarly)
	}
}

// The same latch the rest of the workstream uses: an operation already under way keeps its scaler
// ensured through the treasury dip its own hull buys cause (RULINGS #1 — never stand running work down).
func TestBootstrap_BelowThreshold_ContractOpsUnderway_ScalerStillEnsured(t *testing.T) {
	cases := map[string]func(*Observation){
		"the coordinator is running": func(o *Observation) { o.BatchContractRunning = true },
		"a hauler is already owned": func(o *Observation) {
			o.Haulers = []HaulerSnapshot{{Symbol: "H1", Waypoint: "X1-HUBA"}}
			o.TradeHullCount = 1
		},
		"the frigate is mid-purchase": func(o *Observation) { o.CommandFrigatePurchasing = true },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			obs := tradeIdleObs()
			obs.Treasury = 460_000 // below the start threshold
			mutate(&obs)
			ho := &fakeHandoff{}
			h := tradeIdleHandler(obs, &fakeRetirer{}, &fakeHaulerAcquirer{price: 300_000, yard: "Y", readable: true}, &fakeContractRunner{}, ho, &fakeFrigateLoop{})

			res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
			if ho.contractScaler != 1 || !res.ContractScalerLaunchedEarly {
				t.Fatalf("an operation already under way must keep its capacity owner ensured (launches=%d early=%v)", ho.contractScaler, res.ContractScalerLaunchedEarly)
			}
		})
	}
}

// --- (2) AT/ABOVE the threshold: contract ops start, and the pivot fires off IDLE-IN-TRADE ---

func TestBootstrap_AtThreshold_StartsContractOpsAndPivotsOffIdleInTrade(t *testing.T) {
	obs := tradeIdleObs()
	obs.Treasury = defaultContractStartTreasuryThreshold // exactly at the bar: >= starts the operation
	ret, acq, run, ho := &fakeRetirer{}, &fakeHaulerAcquirer{price: 300_000, yard: "X1-YARD", readable: true}, &fakeContractRunner{}, &fakeHandoff{}
	h := tradeIdleHandler(obs, ret, acq, run, ho, &fakeFrigateLoop{})

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if run.calls != 1 || !res.ContractRun {
		t.Fatalf("at the threshold the contract-fleet coordinator must launch: calls=%d ran=%v (blocker=%q)", run.calls, res.ContractRun, res.Blocker)
	}
	if !res.FrigatePivoted || len(ret.dedications) != 1 || ret.dedications[0] != "FRIGATE-1" {
		t.Fatalf("the idle-in-trade frigate must pivot to the exclusive purchasing ship: pivoted=%v dedications=%v", res.FrigatePivoted, ret.dedications)
	}
	if acq.buys != 1 || len(acq.purchasers) != 1 || acq.purchasers[0] != "FRIGATE-1" {
		t.Fatalf("hauler #1 must be bought with the frigate as the purchaser: buys=%d purchasers=%v", acq.buys, acq.purchasers)
	}
}

// The pivot waits for an honest idle tick: a frigate mid-tour is never yanked (PLAYBOOK §9).
func TestBootstrap_AboveThreshold_MidTourFrigate_PivotDefers(t *testing.T) {
	obs := tradeIdleObs()
	obs.Treasury = 2_000_000
	obs.CommandFrigateIdle = false // assigned: a tour is genuinely in flight
	ret, acq := &fakeRetirer{}, &fakeHaulerAcquirer{price: 300_000, yard: "Y", readable: true}
	h := tradeIdleHandler(obs, ret, acq, &fakeContractRunner{}, &fakeHandoff{}, &fakeFrigateLoop{})

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.dedications) != 0 || acq.buys != 0 {
		t.Fatalf("a mid-tour frigate must NOT be pivoted; dedications=%v buys=%d", ret.dedications, acq.buys)
	}
	if res.Blocker != "no_purchaser" {
		t.Fatalf("no free hull and no available pivot must block no_purchaser, got %q", res.Blocker)
	}
}

// --- (3) THE RELEASE (sp-onps): the purchasing frigate is handed BACK to trade once its buys are done ---

// The tail of the trade seed: acquisition #2 lands and the frigate returns to the trade fleet in the
// SAME tick, so it resumes touring instead of standing by idle-dedicated forever.
func TestBootstrap_TradeSeed_ReleasesPurchaserBackToTrade(t *testing.T) {
	obs := incomeObs()
	obs.BatchContractRunning = true
	obs.CommandFrigateID = "FRIGATE-1"
	obs.CommandFrigatePurchasing = true
	obs.Haulers = []HaulerSnapshot{{Symbol: "H1", Waypoint: "X1-HUBA"}}
	obs.TradeHullCount = 0
	ret, acq, ho := &fakeRetirer{}, &fakeHaulerAcquirer{price: 300_000, yard: "X1-YARD", readable: true}, &fakeHandoff{}
	h := tradeIdleHandler(obs, ret, acq, &fakeContractRunner{}, ho, &fakeFrigateLoop{})

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if !res.TradeHullSeeded || acq.dedicateBuys != 1 {
		t.Fatalf("precondition: the trade seed must fire; seeded=%v dedicateBuys=%d (blocker=%q)", res.TradeHullSeeded, acq.dedicateBuys, res.Blocker)
	}
	if len(ret.tradeDedications) != 1 || ret.tradeDedications[0] != "FRIGATE-1" {
		t.Fatalf("sp-onps: the frigate must be RELEASED back to the trade fleet once its cold-start buys land, got %v", ret.tradeDedications)
	}
	if !res.PurchaserReleased {
		t.Fatalf("res.PurchaserReleased must record the hand-back")
	}
}

// The same release is a standing, observation-derived step, so a fleet that is ALREADY stuck — the live
// sp-onps shape: buys long finished, frigate still dedicated 'purchasing' — is cured on the next tick,
// and so is a daemon restart that dropped the seed tick.
func TestBootstrap_StuckPurchaser_ReleasedBackToTradeOnALaterTick(t *testing.T) {
	obs := incomeObs()
	obs.BatchContractRunning = true
	obs.CommandFrigateID = "FRIGATE-1"
	obs.CommandFrigatePurchasing = true
	obs.Haulers = []HaulerSnapshot{{Symbol: "H1", Waypoint: "X1-HUBA"}}
	obs.TradeHullCount = 1 // the cold-start buys are DONE — nothing is left for the frigate to buy
	ret, acq := &fakeRetirer{}, &fakeHaulerAcquirer{price: 300_000, yard: "X1-YARD", readable: true}
	h := tradeIdleHandler(obs, ret, acq, &fakeContractRunner{}, &fakeHandoff{}, &fakeFrigateLoop{})

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if len(ret.tradeDedications) != 1 || ret.tradeDedications[0] != "FRIGATE-1" || !res.PurchaserReleased {
		t.Fatalf("a purchasing frigate with nothing left to buy must be handed back to trade; trade=%v released=%v (blocker=%q)", ret.tradeDedications, res.PurchaserReleased, res.Blocker)
	}
}

// ACCEPTANCE across ticks (not one observation): the frigate is released exactly once and STAYS in the
// trade fleet — never re-drafted as a standing purchaser on any later tick.
func TestBootstrap_PurchaserRelease_FrigateStaysInTradeAcrossTicks(t *testing.T) {
	world := &incomeWorld{
		treasury: 5_000_000, homeSystem: "X1", marketsTotal: 10, marketsCovered: 10,
		frigateID: "FRIGATE-1", probeCount: 3, batchRunning: true,
		frigateCargoEmpty: true, commandFrigatePurchasing: true,
		placementSlots: incomeSlots(), hasPurchaser: true,
	}
	ret := &fakeRetirer{world: world}
	acq := &fakeHaulerAcquirer{price: 200_000, yard: "X1-YARD", readable: true, world: world}
	ho := &fakeHandoff{}
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(&fakeIncomeObserver{world: world})
	h.SetProbeAcquirer(&fakeAcquirer{price: 40_000, yard: "Y", readable: true})
	h.SetFrigateRetirer(ret)
	h.SetHaulerAcquirer(acq)
	h.SetContractRunner(&fakeContractRunner{world: world})
	h.SetHandoffLauncher(ho)

	for i := 0; i < 12; i++ {
		if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	final := world.snapshot()
	if final.CommandFrigatePurchasing {
		t.Fatalf("sp-onps: the frigate must NOT still be dedicated 'purchasing' after its buys complete")
	}
	if !final.CommandFrigateOnTrade {
		t.Fatalf("the released frigate must end up in the TRADE fleet, earning")
	}
	if len(ret.tradeDedications) != 1 {
		t.Fatalf("the hand-back must fire exactly once across the arc, got %v", ret.tradeDedications)
	}
	if len(ret.dedications) != 0 {
		t.Fatalf("nothing may re-draft the released frigate as a standing purchaser, got %v", ret.dedications)
	}
}

// --- (4) MID-ERA UPGRADE: a frigate already mid-purchase completes through the unchanged committed path ---

func TestBootstrap_MidEraUpgrade_CommittedPurchaserCompletesTheBuy(t *testing.T) {
	obs := incomeObs()
	obs.BatchContractRunning = true
	obs.CommandFrigateID = "FRIGATE-1"
	obs.CommandFrigatePurchasing = true // dedicated before this shipped; no trade tag, no loop signal
	obs.HasIdlePurchaser = false
	obs.Haulers = nil
	obs.TradeHullCount = 0
	ret, acq := &fakeRetirer{}, &fakeHaulerAcquirer{price: 300_000, yard: "X1-YARD", readable: true}
	h := tradeIdleHandler(obs, ret, acq, &fakeContractRunner{}, &fakeHandoff{}, &fakeFrigateLoop{})

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if acq.buys != 1 || len(acq.purchasers) != 1 || acq.purchasers[0] != "FRIGATE-1" {
		t.Fatalf("an in-flight purchase must complete through the unchanged committed-purchaser path; buys=%d purchasers=%v (blocker=%q)", acq.buys, acq.purchasers, res.Blocker)
	}
	if len(ret.tradeDedications) != 0 {
		t.Fatalf("a frigate mid-purchase must NEVER be pulled back to trade before its buy lands, got %v", ret.tradeDedications)
	}
}

// The same in-flight purchase below the threshold: the gate controls NEW work only, so a committed
// purchaser is never interrupted by a treasury that its own buy pushed under the bar.
func TestBootstrap_MidPurchaseBelowThreshold_IsNeverInterrupted(t *testing.T) {
	obs := incomeObs()
	obs.Treasury = 460_000 // under the threshold: the pivot's own hauler buy spent the difference
	obs.BatchContractRunning = false
	obs.CommandFrigateID = "FRIGATE-1"
	obs.CommandFrigatePurchasing = true
	obs.HasIdlePurchaser = false
	obs.Haulers = nil
	obs.TradeHullCount = 0
	ret, acq := &fakeRetirer{}, &fakeHaulerAcquirer{price: 300_000, yard: "X1-YARD", readable: true}
	h := tradeIdleHandler(obs, ret, acq, &fakeContractRunner{}, &fakeHandoff{}, &fakeFrigateLoop{})

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if len(ret.tradeDedications) != 0 {
		t.Fatalf("a treasury dip must NOT yank a committed purchaser back to trade, got %v", ret.tradeDedications)
	}
	if acq.buys != 1 {
		t.Fatalf("the in-flight purchase must still complete; buys=%d (blocker=%q)", acq.buys, res.Blocker)
	}
}

// --- THE LATCH: contract ops already under way survive a treasury under the threshold ---

func TestBootstrap_ContractOpsUnderway_SurviveATreasuryDip(t *testing.T) {
	cases := map[string]func(*Observation){
		"the coordinator is running": func(o *Observation) { o.BatchContractRunning = true },
		"a hauler is already owned": func(o *Observation) {
			o.Haulers = []HaulerSnapshot{{Symbol: "H1", Waypoint: "X1-HUBA"}}
			o.TradeHullCount = 1
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			obs := tradeIdleObs()
			obs.Treasury = 460_000 // below the start threshold
			obs.HasIdlePurchaser = true
			mutate(&obs)
			acq, run := &fakeHaulerAcquirer{price: 100_000, yard: "Y", readable: true}, &fakeContractRunner{}
			h := tradeIdleHandler(obs, &fakeRetirer{}, acq, run, &fakeHandoff{}, &fakeFrigateLoop{})

			res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
			if res.Blocker == "contract_start_deferred" {
				t.Fatalf("an operation already under way must never be stood back down by a treasury dip")
			}
			if acq.buys+acq.dedicateBuys+run.calls == 0 {
				t.Fatalf("the running operation must keep ramping: buys=%d seeds=%d launches=%d (blocker=%q)", acq.buys, acq.dedicateBuys, run.calls, res.Blocker)
			}
		})
	}
}

// --- (5) RESTART SAFETY: one tick from a FRESH handler at each point of the arc re-derives correctly ---

func TestBootstrap_RestartSafety_ReDerivesAtEveryPoint(t *testing.T) {
	t.Run("below threshold, mid trade tour", func(t *testing.T) {
		obs := tradeIdleObs()
		obs.Treasury = 200_000
		obs.CommandFrigateIdle = false // a tour is in flight across the restart
		ret, acq, run := &fakeRetirer{}, &fakeHaulerAcquirer{price: 300_000, yard: "Y", readable: true}, &fakeContractRunner{}
		h := tradeIdleHandler(obs, ret, acq, run, &fakeHandoff{}, &fakeFrigateLoop{})
		h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
		if len(ret.tradeDedications) != 0 || len(ret.dedications) != 0 || acq.buys != 0 || run.calls != 0 {
			t.Fatalf("a restart mid-tour below the threshold must touch nothing; trade=%v purchasing=%v buys=%d launches=%d", ret.tradeDedications, ret.dedications, acq.buys, run.calls)
		}
	})

	t.Run("at the pivot moment", func(t *testing.T) {
		obs := tradeIdleObs()
		obs.Treasury = 2_000_000
		ret, acq := &fakeRetirer{}, &fakeHaulerAcquirer{price: 300_000, yard: "Y", readable: true}
		h := tradeIdleHandler(obs, ret, acq, &fakeContractRunner{}, &fakeHandoff{}, &fakeFrigateLoop{})
		res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
		if !res.FrigatePivoted || len(ret.dedications) != 1 || acq.buys != 1 {
			t.Fatalf("a restart at the pivot must pivot exactly once and buy once; pivoted=%v dedications=%v buys=%d", res.FrigatePivoted, ret.dedications, acq.buys)
		}
	})

	t.Run("mid purchase", func(t *testing.T) {
		obs := tradeIdleObs()
		obs.Treasury = 2_000_000
		obs.CommandFrigateOnTrade = false
		obs.CommandFrigatePurchasing = true // dedicated, hauler not bought yet
		ret, acq := &fakeRetirer{}, &fakeHaulerAcquirer{price: 300_000, yard: "Y", readable: true}
		h := tradeIdleHandler(obs, ret, acq, &fakeContractRunner{}, &fakeHandoff{}, &fakeFrigateLoop{})
		h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
		if len(ret.dedications) != 0 {
			t.Fatalf("a committed purchaser must not be re-dedicated after a restart, got %v", ret.dedications)
		}
		if acq.buys != 1 || acq.purchasers[0] != "FRIGATE-1" {
			t.Fatalf("the restart must complete the in-flight buy with the committed purchaser; buys=%d purchasers=%v", acq.buys, acq.purchasers)
		}
	})

	t.Run("after release", func(t *testing.T) {
		obs := tradeIdleObs()
		obs.Treasury = 2_000_000
		obs.BatchContractRunning = true
		obs.Haulers = []HaulerSnapshot{{Waypoint: "X1-HUBA"}, {Waypoint: "X1-HUBB"}, {Waypoint: "X1-HUBC"}, {Waypoint: "X1-HUBD"}}
		obs.TradeHullCount = 1
		ret, acq, run := &fakeRetirer{}, &fakeHaulerAcquirer{price: 300_000, yard: "Y", readable: true}, &fakeContractRunner{}
		h := tradeIdleHandler(obs, ret, acq, run, &fakeHandoff{}, &fakeFrigateLoop{})
		h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
		if acq.buys != 0 || acq.dedicateBuys != 0 || run.calls != 0 {
			t.Fatalf("a restart with the ramp complete must not double-buy or double-launch; buys=%d seeds=%d launches=%d", acq.buys, acq.dedicateBuys, run.calls)
		}
		if len(ret.dedications) != 0 || len(ret.tradeDedications) != 0 {
			t.Fatalf("a restart after the release must leave the trading frigate alone; purchasing=%v trade=%v", ret.dedications, ret.tradeDedications)
		}
	})
}

// --- MIGRATION: a legacy frigate contract loop left running by an earlier deploy is stopped ONCE ---

func TestBootstrap_LegacyFrigateLoop_StoppedAtTheCargoEmptySafePoint(t *testing.T) {
	obs := tradeIdleObs()
	obs.Treasury = 2_000_000
	obs.CommandFrigateOnTrade = false
	obs.CommandFrigateIdle = false        // the loop container holds the claim
	obs.FrigateContractLoopRunning = true // left over from the retired pre-hauler earner design
	loop := &fakeFrigateLoop{}
	h := tradeIdleHandler(obs, &fakeRetirer{}, &fakeHaulerAcquirer{price: 300_000, yard: "Y", readable: true}, &fakeContractRunner{}, &fakeHandoff{}, loop)

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if loop.stopCalls != 1 || len(loop.stopped) != 1 || loop.stopped[0] != "FRIGATE-1" || !res.FrigateLoopStopped {
		t.Fatalf("a legacy frigate contract loop must be stopped once, by symbol; stopCalls=%d stopped=%v stopped_flag=%v", loop.stopCalls, loop.stopped, res.FrigateLoopStopped)
	}
}

// The migration is part of the CONTRACT workstream, so below the threshold the legacy loop is LEFT
// RUNNING: no contract-fleet coordinator is up to re-adopt the contracts it is working, so stopping it
// there would strand them (RULINGS #1). It keeps earning until the operation starts and retires it.
func TestBootstrap_BelowThreshold_LegacyFrigateLoop_LeftRunning(t *testing.T) {
	obs := tradeIdleObs()
	obs.Treasury = defaultContractStartTreasuryThreshold - 1
	obs.BatchContractRunning = false
	obs.CommandFrigateOnTrade = false
	obs.CommandFrigateIdle = false // the loop container holds the claim
	obs.FrigateContractLoopRunning = true
	loop, ret := &fakeFrigateLoop{}, &fakeRetirer{}
	h := tradeIdleHandler(obs, ret, &fakeHaulerAcquirer{price: 300_000, yard: "Y", readable: true}, &fakeContractRunner{}, &fakeHandoff{}, loop)

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if loop.stopCalls != 0 {
		t.Fatalf("below the threshold the legacy loop must be left earning — nothing would take over its contracts, got %d stops", loop.stopCalls)
	}
	if len(ret.tradeDedications) != 0 {
		t.Fatalf("a frigate the loop still claims is not free, so it must not be re-tagged either, got %v", ret.tradeDedications)
	}
	if res.Blocker != "contract_start_deferred" {
		t.Fatalf("the deferral must still be what the heartbeat shows, got %q", res.Blocker)
	}
}

// RULINGS #1: never abandon an accepted contract's cargo — the legacy loop is stopped only when the
// frigate is empty (between contracts), never mid-delivery.
func TestBootstrap_LegacyFrigateLoop_LoadedFrigate_NotStopped(t *testing.T) {
	obs := tradeIdleObs()
	obs.Treasury = 2_000_000
	obs.CommandFrigateOnTrade = false
	obs.CommandFrigateIdle = false
	obs.FrigateContractLoopRunning = true
	obs.FrigateCargoEmpty = false // mid-delivery
	loop := &fakeFrigateLoop{}
	h := tradeIdleHandler(obs, &fakeRetirer{}, &fakeHaulerAcquirer{price: 300_000, yard: "Y", readable: true}, &fakeContractRunner{}, &fakeHandoff{}, loop)

	h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if loop.stopCalls != 0 {
		t.Fatalf("a loaded frigate's loop must NOT be stopped mid-delivery, got %d stops", loop.stopCalls)
	}
}

// --- (6) GATE-phase entry is untouched by the new threshold: a different, independent check ---

func TestBootstrap_GateEntry_IndependentOfTheContractStartThreshold(t *testing.T) {
	// GATE entry nets the treasury against the immutable reserve floor and requires a scaled fleet;
	// the contract-start threshold does neither. A treasury far over the contract-start bar but under
	// the GATE surplus bar must NOT gate.
	obs := Observation{
		Haulers:              []HaulerSnapshot{{Symbol: "H1"}, {Symbol: "H2"}},
		ContractScalerTarget: 2,
		Treasury:             defaultContractStartTreasuryThreshold + 1, // clears contract start, not GATE
	}
	if gateFunded(obs) {
		t.Fatalf("the contract-start threshold must not move GATE entry: treasury=%d", obs.Treasury)
	}
	obs.Treasury = gateSurplusFloor + 50_000 // the untouched netted bar
	if !gateFunded(obs) {
		t.Fatalf("GATE entry must still gate on its own (netted) surplus bar at treasury=%d", obs.Treasury)
	}
}

// --- (7) the threshold is real config + a live tune key, not a compile-time constant ---

func TestBootstrap_ContractStartThreshold_IsLiveTunable(t *testing.T) {
	obs := tradeIdleObs()
	obs.Treasury = 600_000 // over the DEFAULT threshold, under the tuned one
	acq, run := &fakeHaulerAcquirer{price: 100_000, yard: "Y", readable: true}, &fakeContractRunner{}
	h := tradeIdleHandler(obs, &fakeRetirer{}, acq, run, &fakeHandoff{}, &fakeFrigateLoop{})
	h.SetLiveConfigReader(&fakeLiveConfig{snap: liveconfig.Snapshot{"contract_start_treasury_threshold": 1_000_000}})

	res, _ := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if run.calls != 0 || acq.buys != 0 {
		t.Fatalf("a live-tuned threshold must govern the very next tick: launches=%d buys=%d", run.calls, acq.buys)
	}
	if res.Blocker != "contract_start_deferred" {
		t.Fatalf("expected the deferral blocker under the tuned threshold, got %q", res.Blocker)
	}
}
