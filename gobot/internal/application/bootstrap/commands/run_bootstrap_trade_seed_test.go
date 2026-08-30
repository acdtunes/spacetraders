package commands

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
)

// Cold-start hull-routing: cold-start light-hull acquisitions are routed by ORDER — #1 → the contract
// fleet, #2 → the TRADE fleet (the trade-seed, held until the first contract hull exists), #3… → contract
// again. The trade hull EXISTING (obs.TradeHullCount) is the durable, observable "seeded" signal — no stored
// flag. The contract/trade coordinators + the scaler stay phase-BLIND; all the phase logic lives in the
// bootstrap.

// tradeSeedHandler wires an contract handler through sjvvHandler (contract collaborators + a hand-off launcher +
// a live-config reader), so a single tick exercises the trade-seed routing.
func tradeSeedHandler(obs Observation, ho *fakeHandoff, acq *fakeHaulerAcquirer) *RunBootstrapCoordinatorHandler {
	return sjvvHandler(obs, &fakeLiveConfig{snap: liveconfig.Snapshot{}}, ho, acq)
}

// --- (a) 1 contract hull + 0 trade → seed the trade hull (BuyAndDedicate "trade" + LaunchTradeFleetCoordinator),
// and do NOT buy a 2nd contract hauler (acquisition #2 is the trade seed) ---

func TestBootstrap_TradeSeed_SecondAcquisitionRoutedToTradeFleet(t *testing.T) {
	obs := incomeObs()              // COLDSTART (probes 3/3), 3 viable hubs, treasury 2M
	obs.BatchContractRunning = true // isolate: don't launch batch-contract
	obs.GrowthRunning = true        // isolate: the early autosizer launch is an idempotent no-op
	obs.CommandFrigateID = "FRIGATE-1"
	obs.CommandFrigatePurchasing = true                                 // the exclusive purchasing ship (post first-hauler pivot)
	obs.Haulers = []HaulerSnapshot{{Symbol: "H1", Waypoint: "X1-HUBA"}} // ONE contract hull already
	obs.TradeHullCount = 0                                              // no trade hull yet → the seed fires
	acq := &fakeHaulerAcquirer{price: 300000, yard: "X1-YARD", readable: true}
	ho := &fakeHandoff{}
	h := tradeSeedHandler(obs, ho, acq)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	// The trade seed fired: exactly one BuyAndDedicate, dedicated to the "trade" fleet, bought with the frigate.
	if acq.dedicateBuys != 1 || len(acq.dedicatedFleets) != 1 || acq.dedicatedFleets[0] != tradeFleetTag {
		t.Fatalf("acquisition #2 must be BOUGHT + dedicated to the TRADE fleet: dedicateBuys=%d fleets=%v (blocker=%q)", acq.dedicateBuys, acq.dedicatedFleets, res.Blocker)
	}
	if len(acq.dedicatePurch) != 1 || acq.dedicatePurch[0] != "FRIGATE-1" {
		t.Fatalf("the trade seed must buy with the exclusive purchasing frigate, got %v", acq.dedicatePurch)
	}
	// The trade coordinator was ensured, and the marker set.
	if ho.tradeCoord < 1 {
		t.Fatalf("the trade seed must ensure the trade-fleet coordinator, got %d", ho.tradeCoord)
	}
	if !res.TradeHullSeeded {
		t.Fatalf("res.TradeHullSeeded should be true after the seed")
	}
	// Crucially, it must NOT buy a 2nd contract hauler — acquisition #2 is the trade seed, and the branch RETURNs.
	if acq.buys != 0 || res.HaulersBought != 0 {
		t.Fatalf("the trade seed must NOT also buy a 2nd contract hauler: contractBuys=%d haulersBought=%d", acq.buys, res.HaulersBought)
	}
}

// sp-fr55v: the trade-seed buy shares contractWorkingCapitalFloor with the hauler buy it re-routes
// acquisition #2 from (RULINGS #4/#5 — re-routing must never weaken that guard), so its OWN boundary
// moves with the raise too: a cushion that cleared the pre-raise 150k floor must now be BLOCKED.
func TestBootstrap_TradeSeed_WorkingCapitalFloor_RaiseBlocksTheOldBoundary(t *testing.T) {
	const price = int64(300000)
	obs := incomeObs()
	obs.BatchContractRunning = true
	obs.GrowthRunning = true
	obs.CommandFrigateID = "FRIGATE-1"
	obs.CommandFrigatePurchasing = true
	obs.Haulers = []HaulerSnapshot{{Symbol: "H1", Waypoint: "X1-HUBA"}}
	obs.TradeHullCount = 0
	obs.Treasury = price + 150_000 // cushion = 150_000 exactly: the pre-raise floor, not the current one
	acq := &fakeHaulerAcquirer{price: price, yard: "X1-YARD", readable: true}
	ho := &fakeHandoff{}
	h := tradeSeedHandler(obs, ho, acq)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if acq.dedicateBuys != 0 || res.TradeHullSeeded {
		t.Fatalf("cushion=150000 cleared the pre-sp-fr55v floor but sits below the raised %d floor: must NOT seed, got dedicateBuys=%d seeded=%v", contractWorkingCapitalFloor, acq.dedicateBuys, res.TradeHullSeeded)
	}
	if res.Blocker != "capital_gate" {
		t.Fatalf("expected capital_gate blocker at the pre-raise boundary, got %q", res.Blocker)
	}
}

// --- (c) idempotent: with a trade hull already present, the seed does NOT re-fire ---

func TestBootstrap_TradeSeed_Idempotent_NoReseedWhenTradeHullExists(t *testing.T) {
	obs := incomeObs()
	obs.BatchContractRunning = true
	obs.GrowthRunning = true
	obs.CommandFrigateID = "FRIGATE-1"
	obs.CommandFrigatePurchasing = true
	obs.TradeHullCount = 1 // the trade hull already exists → the durable seeded signal → no re-seed
	// haulers at desired (3 hubs) so the contract branch does not buy either — isolates the seed assertion.
	obs.Haulers = []HaulerSnapshot{{Waypoint: "X1-HUBA"}, {Waypoint: "X1-HUBB"}, {Waypoint: "X1-HUBC"}}
	acq := &fakeHaulerAcquirer{price: 300000, yard: "X1-YARD", readable: true}
	ho := &fakeHandoff{}
	h := tradeSeedHandler(obs, ho, acq)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if acq.dedicateBuys != 0 {
		t.Fatalf("a trade hull already present: must NOT re-seed, got %d BuyAndDedicate calls", acq.dedicateBuys)
	}
	if res.TradeHullSeeded {
		t.Fatalf("res.TradeHullSeeded must be false when the trade hull already exists")
	}
}

// --- (d) 0 contract haulers still buys contract #1 unchanged (NOT the trade seed) ---

func TestBootstrap_TradeSeed_ZeroContract_BuysContractFirstNotTrade(t *testing.T) {
	obs := incomeObs()              // 3 viable hubs, treasury 2M, idle purchaser present
	obs.BatchContractRunning = true // isolate the buy
	obs.GrowthRunning = true        // isolate the early autosizer launch
	obs.Haulers = nil               // ZERO contract haulers → acquisition #1
	obs.TradeHullCount = 0
	acq := &fakeHaulerAcquirer{price: 300000, yard: "X1-YARD", readable: true}
	ho := &fakeHandoff{}
	h := tradeSeedHandler(obs, ho, acq)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	// Acquisition #1 is a CONTRACT hull (placed on the top hub), bought via BuyAndPlace — unchanged.
	if acq.buys != 1 || res.HaulersBought != 1 {
		t.Fatalf("0 contract haulers must buy contract #1 (unchanged): contractBuys=%d haulersBought=%d (blocker=%q)", acq.buys, res.HaulersBought, res.Blocker)
	}
	if len(acq.placedOn) != 1 || acq.placedOn[0] != "X1-HUBA" {
		t.Fatalf("contract #1 must be placed on the top hub X1-HUBA, got %v", acq.placedOn)
	}
	// It is NOT the trade seed — the trade-seed branch only fires once a contract hull exists.
	if acq.dedicateBuys != 0 || res.TradeHullSeeded {
		t.Fatalf("acquisition #1 must be a CONTRACT hull, not the trade seed: dedicateBuys=%d seeded=%v", acq.dedicateBuys, res.TradeHullSeeded)
	}
}

// --- (b) the contract scaler is ensured throughout cold start, INDEPENDENT of the trade seed: the hull
// routing above is bootstrap's alone, so the scaler's launch is not coupled to it. A paired reading on the
// same isolated fixture (no contract haulers, no delivery slots → neither the trade-seed nor the
// contract-buy branch fires) at both trade-hull counts. ---

// scalerGateObs is a cold-start fixture isolated to the scaler launch: 0 contract haulers (the trade-seed
// branch needs ≥1, so it never fires), no delivery slots (the contract buy has nowhere to place, so it
// fails closed), autosizer running (the early autosizer launch is a no-op).
func scalerGateObs(tradeHulls int) Observation {
	obs := incomeObs()
	obs.BatchContractRunning = true
	obs.GrowthRunning = true
	obs.Haulers = nil
	obs.ContractPlacementSlots = nil
	obs.TradeHullCount = tradeHulls
	return obs
}

func TestBootstrap_ContractScaler_EnsuredBeforeTradeHullSeeded(t *testing.T) {
	ho := &fakeHandoff{}
	h := tradeSeedHandler(scalerGateObs(0), ho, &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true})

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if res.Phase != PhaseColdStart {
		t.Fatalf("expected COLDSTART, got %s", res.Phase)
	}
	if ho.contractScaler != 1 || !res.ContractScalerLaunchedEarly {
		t.Fatalf("0 trade hulls: the contract scaler is still ensured (its launch is not coupled to the seed), got launches=%d early=%v", ho.contractScaler, res.ContractScalerLaunchedEarly)
	}
}

func TestBootstrap_ContractScaler_EnsuredOnceTradeHullSeeded(t *testing.T) {
	ho := &fakeHandoff{}
	h := tradeSeedHandler(scalerGateObs(1), ho, &fakeHaulerAcquirer{price: 300000, yard: "Y", readable: true})

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if ho.contractScaler != 1 || !res.ContractScalerLaunchedEarly {
		t.Fatalf("≥1 trade hull: the contract scaler must be ensured, got launches=%d early=%v", ho.contractScaler, res.ContractScalerLaunchedEarly)
	}
}

// --- nil-safe: the seed skips (no buy) when the hand-off launcher is missing — never strand a purchased hull ---

func TestBootstrap_TradeSeed_NilHandoff_SkipsBuyAndBlocks(t *testing.T) {
	obs := incomeObs()
	obs.BatchContractRunning = true
	obs.GrowthRunning = true
	obs.CommandFrigateID = "FRIGATE-1"
	obs.CommandFrigatePurchasing = true
	obs.Haulers = []HaulerSnapshot{{Symbol: "H1", Waypoint: "X1-HUBA"}}
	obs.TradeHullCount = 0
	acq := &fakeHaulerAcquirer{price: 300000, yard: "X1-YARD", readable: true}
	// Deliberately NO hand-off launcher (sjvvHandler skips wiring it when ho is nil).
	h := sjvvHandler(obs, &fakeLiveConfig{snap: liveconfig.Snapshot{}}, nil, acq)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if acq.dedicateBuys != 0 {
		t.Fatalf("no launcher wired: must NOT buy a trade hull it cannot then manage, got %d BuyAndDedicate calls", acq.dedicateBuys)
	}
	if res.TradeHullSeeded {
		t.Fatalf("res.TradeHullSeeded must be false when the seed is skipped")
	}
	if res.Blocker != "no_handoff_launcher" {
		t.Fatalf("a missing launcher must surface the no_handoff_launcher blocker, got %q", res.Blocker)
	}
}

// --- cold-start hull-routing acceptance: from a provisioned fixture the arc buys contract #1, seeds the TRADE
// hull as acquisition #2 (+ ensures the trade coordinator), then resumes contract buying #3… — the trade hull
// is DECOUPLED (it does not consume a contract slot). ---

func TestBootstrap_TradeSeedAcceptance_RoutesSecondAcquisitionToTrade(t *testing.T) {
	world := &incomeWorld{
		treasury: 5000000, homeSystem: "X1", marketsTotal: 10, marketsCovered: 10,
		frigateID: "FRIGATE-1", frigateOnContract: false, batchRunning: true,
		probeCount:               3, // provisioned (probe_target 3) → COLDSTART-labeled
		placementSlots:           incomeSlots(),
		incomePerHour:            0,
		hasPurchaser:             true,
		commandFrigatePurchasing: true, // the exclusive purchasing ship (post first-hauler pivot)
	}
	acq := &fakeHaulerAcquirer{price: 200000, yard: "X1-YARD", readable: true, world: world}
	ho := &fakeHandoff{}
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(&fakeIncomeObserver{world: world})
	h.SetProbeAcquirer(&fakeAcquirer{price: 40000, yard: "Y", readable: true})
	h.SetFrigateRetirer(&fakeRetirer{world: world})
	h.SetHaulerAcquirer(acq)
	h.SetContractRunner(&fakeContractRunner{})
	h.SetHandoffLauncher(ho)

	for i := 0; i < 12; i++ {
		if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	final := world.snapshot()
	// The contract ramp reaches its resolved target, PLUS exactly 1 TRADE hull (acquisition #2) — the trade
	// hull did not consume a contract slot (decoupled from the contract scaler's ceiling).
	haulerTarget := defaultTargets().HaulerTarget
	if len(final.Haulers) != haulerTarget {
		t.Fatalf("acceptance: expected %d contract haulers (the resolved target), got %d", haulerTarget, len(final.Haulers))
	}
	if final.TradeHullCount != 1 {
		t.Fatalf("acceptance: expected exactly 1 trade hull (acquisition #2), got %d", final.TradeHullCount)
	}
	// The trade hull was seeded exactly once (BuyAndDedicate 'trade'), and the trade coordinator ensured once.
	if acq.dedicateBuys != 1 || len(acq.dedicatedFleets) != 1 || acq.dedicatedFleets[0] != tradeFleetTag {
		t.Fatalf("acceptance: exactly 1 trade-seed buy dedicated 'trade', got dedicateBuys=%d fleets=%v", acq.dedicateBuys, acq.dedicatedFleets)
	}
	if ho.tradeCoord < 1 {
		t.Fatalf("acceptance: the trade coordinator must be ensured, got %d", ho.tradeCoord)
	}
	// Exactly haulerTarget CONTRACT buys — the trade hull is separate, not one of them.
	if acq.buys != haulerTarget {
		t.Fatalf("acceptance: expected exactly %d contract-hauler buys, got %d", haulerTarget, acq.buys)
	}
	// The scaler is ensured throughout the run, independent of the hull routing above.
	if ho.contractScaler < 1 {
		t.Fatalf("acceptance: the contract scaler must be ensured during cold start, got %d", ho.contractScaler)
	}
}
