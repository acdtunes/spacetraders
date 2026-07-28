package common

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// This file holds the PER-OPERATION CAPITAL BUDGET (sp-ftqgp). Capital used to be a shared POOL
// guarded only by floors, and a floor ALLOCATES NOTHING: it says "do not spend below X", never
// "this much is yours". So the engine with the LOWEST floor structurally outranked every engine
// above it, and the asymmetry between the two non-contract spenders decided who ate — trade caps
// ITSELF at 25% of live treasury (tourDefaultMaxSpendTreasuryPct, RULINGS #6) while construction
// had no proportional cap at all, so gate-fill consumed 100% of capital above its floor including
// the whole band trade needs to function. Measured live (era 5): the gate pipeline drove treasury
// 1.18M -> 193k, after which the tour engine planned +50,890 across five legs and the floor shrank
// leg 0 from 25 CLOTHING to 12 and a FOOD tranche to ONE unit. Trade is polite, construction is
// greedy, construction wins.
//
// Raising construction's floor instead only RELOCATES the starvation — sp-q8bon did exactly that
// one tier down (gate-fill dragged 638k->142k and parked the contract engine against its 50k
// floor; the fix made the 50k-150k band contract-exclusive). Flooring construction at 300k+ would
// simply stall the gate on every treasury dip: it swaps the victim rather than removing the class.
//
// The fix is the shape expansion already uses for per-cycle post-declaration capacity
// (frontierCapacitySplit, discovery_share.go): a split that is PROPORTIONAL (each side gets a
// configured share, neither can crowd the other out), CLAMPED (the share is bounded, so it cannot
// be misconfigured into total starvation), and GRACEFULLY DEGRADING (a side with no work yields
// its whole budget to the side that has work). That last property is the load-bearing one here:
// no capital may idle because construction is paused or trade has no lanes.
//
// TWO INVARIANTS HOLD THIS TO RULINGS #4 AND #5:
//
//   - The budget is a SECOND constraint layered OVER the existing reserve floors, never a
//     replacement. BudgetedSpendFloor can only ever RAISE a caller's floor, so no engine may
//     spend one credit anywhere the floors did not already permit. Money guards are never
//     weakened (RULINGS #4); the floors stay underneath as the fail-closed backstop.
//   - No new floor constant is introduced. CapitalDeployable takes the CALLER'S OWN floor, so
//     every budget derives from the one base in reserve_floor.go and can never drift from it
//     (RULINGS #5 — ONE base, derived tiers).

const (
	// TradeCapitalSharePct is TRADE's share of deployable capital when both sides have work;
	// construction takes the remainder (Admiral-approved 60/40). Trade is the compounding
	// earner that FUNDS the gate, so it takes the larger share — the measured counterfactual
	// is a single manual FOOD leg that bought 60 units for 89,520 and returned 153,840
	// (+64,320 on one round trip), more capital deployed on one good than the whole tour
	// engine was permitted. 40% of a ~290k deployable still puts ~116k per cycle into
	// FAB_MATS, so the gate keeps filling. ONE clamped integer, retunable by edit — not a
	// config knob, not an arm seam.
	TradeCapitalSharePct = 60

	// capitalShareMax / capitalShareMin bound the ratio so it can never be edited into total
	// starvation of either side: 100 = pure trade, 0 = pure construction. CapitalSplit clamps
	// into this range before it divides anything.
	capitalShareMax = 100
	capitalShareMin = 0
)

// CapitalDeployable is the capital a spend guard may allocate this cycle: everything the live
// treasury holds ABOVE the caller's own working-capital floor. The floor is a PARAMETER, not a
// constant read here, so the deployable pool is always derived from the single base the caller
// already enforces (RULINGS #5) — there is no second copy of 50k/150k/300k in this file to drift.
// A treasury at or under the floor deploys nothing (never negative), which makes the split below
// total-conserving for every input.
func CapitalDeployable(treasury, floor int64) int64 {
	if treasury <= floor {
		return 0
	}
	return treasury - floor
}

// CapitalSplit divides deployable capital between TRADE (buy low, sell high — the compounding
// earner) and CONSTRUCTION (factory inputs and fabricated-output harvests feeding the gate) by
// the share ratio, then applies GRACEFUL DEGRADATION. It is a TARGET split, not a hard one: a
// side with no work yields its whole budget to the side that has work, so capital never idles
// because construction is stopped or no trade hull is live. When NEITHER side has work the split
// is empty (nobody is asking to spend). share is clamped to [0,100] and deployable floored at 0,
// so the returned budgets always sum to deployable whenever any work exists — the invariant each
// caller relies on to never over- or under-allocate.
//
// Callers pass `true` for their OWN side: an engine asking this question is, by construction, an
// engine with work, so no engine can ever budget itself to zero and park its whole operation on a
// sensor miss. Only the OTHER side's flag is sensed (see CapitalWorkSensor).
func CapitalSplit(share int, deployable int64, tradeHasWork, constructionHasWork bool) (trade, construction int64) {
	if share < capitalShareMin {
		share = capitalShareMin
	}
	if share > capitalShareMax {
		share = capitalShareMax
	}
	if deployable < 0 {
		deployable = 0
	}

	if !tradeHasWork && !constructionHasWork {
		return 0, 0
	}
	if !constructionHasWork {
		return deployable, 0 // gate stopped / no pipeline executing -> the whole pool trades
	}
	if !tradeHasWork {
		return 0, deployable // no trade hull live -> the whole pool fills the gate
	}

	trade = (int64(share)*deployable + 50) / 100 // round to nearest credit
	construction = deployable - trade
	return trade, construction
}

// BudgetedSpendFloor converts a caller's own capital budget into the spend floor its existing
// money guard enforces: the base floor RAISED by the share of deployable capital reserved for the
// other side. It is the single point where the budget becomes a real constraint, and it is
// written so it can only ever ADD one — the returned floor is >= floor for EVERY input, including
// a nonsensical budget larger than deployable or a negative one (RULINGS #4: a money guard is
// never weakened). At the graceful-degradation extreme, where this side is handed the whole
// deployable pool, it returns exactly floor — byte-identical to the pre-budget behavior.
func BudgetedSpendFloor(floor, deployable, ownBudget int64) int64 {
	reservedForOther := deployable - ownBudget
	if reservedForOther <= 0 {
		return floor
	}
	return floor + reservedForOther
}

// CapitalWorkSensor answers whether a capital-consuming engine currently has work — the input
// CapitalSplit's graceful degradation needs. Each engine passes `true` for its own side and reads
// only the OTHER side through this port, so a sensor that is wrong or unreadable can never zero
// out the engine that is asking.
//
// Both engines resolve through ONE implementation over ONE source of truth, so their two views of
// the split agree; two independent signals could each report "the other side is idle" and hand
// both sides 100%, which is the shared pool again with extra steps.
type CapitalWorkSensor interface {
	// TradeHasWork reports whether any trade engine is live for the player. Read by the
	// CONSTRUCTION side to size its own budget.
	TradeHasWork(ctx context.Context, playerID int) (bool, error)
	// ConstructionHasWork reports whether the construction-supply drain is live for the
	// player. Read by the TRADE side to size its own budget.
	ConstructionHasWork(ctx context.Context, playerID int) (bool, error)
}

// tradeCapitalContainerTypes are the container types whose containers spend TRADE capital: the
// per-hull worker type every trading verb runs under (tour_run / trade_route / arb_run / stocker
// all persist as ContainerTypeTrading) plus the fleet coordinator that launches them.
var tradeCapitalContainerTypes = []string{
	string(container.ContainerTypeTrading),
	string(container.ContainerTypeTradeFleetCoordinator),
}

// constructionCapitalContainerTypes are the container types whose containers spend CONSTRUCTION
// capital. Post-sp-382j exactly one type runs the shared ProductionExecutor — the dedicated
// construction-supply drain — and it is the same set the bootstrap GATE adoption check looks for
// (executorContainerTypes in bootstrap_ports_gate.go). The vestigial PARALLEL_MANUFACTURING /
// MANUFACTURING_TASK_WORKER types retired with the goods-factory ops (sp-hoj8u) have no creator
// left in the daemon and no registered command type, so including them would sense nothing.
var constructionCapitalContainerTypes = []string{
	string(container.ContainerTypeConstructionCoordinator),
}

// ActiveContainerTypeReader reports whether a container of any given type is currently RUNNING or
// PENDING for a player. PENDING counts: a container that is about to start is a side that is
// about to spend, and counting it keeps the split proportional through the launch window instead
// of briefly handing the other side 100%.
type ActiveContainerTypeReader interface {
	HasActiveContainerOfType(ctx context.Context, playerID int, containerTypes ...string) (bool, error)
}

// ContainerCapitalWorkSensor resolves both hasWork signals from the container registry: an engine
// "has work" exactly when a container that runs it is live. That is the operationally correct
// reading for capital allocation — a stopped construction drain cannot spend a credit no matter
// what its pipeline rows say, and a live one re-polls its worklist every tick.
type ContainerCapitalWorkSensor struct {
	containers ActiveContainerTypeReader
}

// NewContainerCapitalWorkSensor builds the container-backed sensor the daemon wires into both
// spend guards.
func NewContainerCapitalWorkSensor(containers ActiveContainerTypeReader) *ContainerCapitalWorkSensor {
	return &ContainerCapitalWorkSensor{containers: containers}
}

// TradeHasWork reports whether any trade container is live for the player.
func (s *ContainerCapitalWorkSensor) TradeHasWork(ctx context.Context, playerID int) (bool, error) {
	return s.hasActive(ctx, playerID, tradeCapitalContainerTypes)
}

// ConstructionHasWork reports whether the construction-supply drain is live for the player.
func (s *ContainerCapitalWorkSensor) ConstructionHasWork(ctx context.Context, playerID int) (bool, error) {
	return s.hasActive(ctx, playerID, constructionCapitalContainerTypes)
}

// hasActive answers CONSERVATIVELY when no reader is wired (a construction-time bug, not a
// supported mode): "the other side is busy", which costs the asking engine its proportional share
// and never hands it 100% of the treasury on a blind read.
func (s *ContainerCapitalWorkSensor) hasActive(ctx context.Context, playerID int, types []string) (bool, error) {
	if s == nil || s.containers == nil {
		return true, nil
	}
	return s.containers.HasActiveContainerOfType(ctx, playerID, types...)
}
