package parkedsensing

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
	"github.com/andrescamacho/spacetraders-go/internal/domain/yardscan"
)

// pacing_passes_test.go is the other half of pacing_test.go: the helper's arithmetic is
// worth nothing if a pass computes a budget and then loops on its own constant anyway.
//
// EVERY PASS BELOW IS DRIVEN TWICE, at full saturation and against an idle budget, over a
// fixture holding strictly more work than either budget admits. A pass ignoring the value
// handed to it returns the SAME number both times — the mutant these exist to kill — and a
// fixture with less work than the cap would pass against any bound at all, including none.

func idleBudget(base int) int { return PacedBudget(base, 0, ExpansionHeadroomMultiple) }
func boundBudget(base int) int {
	return PacedBudget(base, trading.APISaturationPermilleMax, ExpansionHeadroomMultiple)
}

// unreadFrontier is wideFrontier sized to order: n systems hanging off X1-HOME at the SAME
// distance with no stored adjacency, so only the cap decides how many are read.
func unreadFrontier(n int) (*expandHarness, *gateStore) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{{System: "X1-HOME", Verdict: VerdictInScope}}
	gates := newGateStore()
	ring := make([]string, 0, n)
	for i := 0; i < n; i++ {
		sys := fmt.Sprintf("X1-F%03d", i)
		h.ledger.systems = append(h.ledger.systems, ExpandSystem{System: sys, Verdict: VerdictInScope})
		ring = append(ring, sys)
		gates.live[sys] = []string{"X1-HOME"}
	}
	gates.stored["X1-HOME"] = ring
	h.ledger.slots = []QueuedSlot{{
		Waypoint: "X1-HOME-A", System: "X1-HOME", Kind: SlotKindSpare,
		State: SlotStateParked, AssignedShip: "PROBE-HOME",
	}}
	return h, gates
}

func runGateReadAt(t *testing.T, maxGateReads int) (ExpandReport, *gateStore) {
	t.Helper()
	h, gates := unreadFrontier(idleBudget(MaxGateReads) + 5)
	for i := range h.ledger.systems {
		h.ledger.systems[i].CatalogKnown = true
	}
	p := h.ports()
	p.Gates = gates
	p.GateRead = gates
	rep, err := AdvanceExpansion(context.Background(), p, testPlayerID, ExpandKnobs{
		SeedsEnabled: true, MinBudgetRate: 0.05, Whitelist: h.whitelist,
		MaxGateReads: maxGateReads,
	}, 1.0)
	require.NoError(t, err)
	return rep, gates
}

// The pass this change exists for: an idle budget reads 17 gates a tick rather than 3.
func TestGateRead_SpendsTheScaledBudgetAgainstAnIdleRequestBudget(t *testing.T) {
	want := idleBudget(MaxGateReads)
	require.Equal(t, MaxGateReads*ExpansionHeadroomMultiple, want, "the fixture must exercise the whole headroom")

	rep, gates := runGateReadAt(t, want)

	require.Len(t, gates.reads, want, "the pass must spend the budget it was handed, not its own constant")
	require.Equal(t, want, rep.GatesRead)
	require.Equal(t, want, rep.GateReadLimit, "the report carries the budget the loop actually used")
}

func TestGateRead_AFullyBoundBudgetReadsExactlyTheShippedCap(t *testing.T) {
	rep, gates := runGateReadAt(t, boundBudget(MaxGateReads))

	require.Len(t, gates.reads, MaxGateReads, "a saturated budget must pace exactly as it always did")
	require.Equal(t, MaxGateReads, rep.GatesRead)
	require.Equal(t, MaxGateReads, rep.GateReadLimit)
}

// An unwired coordinator paces exactly as before.
func TestGateRead_ANonPositiveBudgetIsTheShippedCap(t *testing.T) {
	rep, gates := runGateReadAt(t, 0)

	require.Len(t, gates.reads, MaxGateReads)
	require.Equal(t, MaxGateReads, rep.GateReadLimit)
}

// chartingFleet is n systems each holding a seed ready to chart one waypoint, so every
// system on the board is worth exactly one action.
func chartingFleet(n int) (*expandHarness, *fakeUncharted) {
	h := newExpandHarness()
	uncharted := map[string][]string{}
	for i := 0; i < n; i++ {
		sys := fmt.Sprintf("X1-C%03d", i)
		h.ledger.systems = append(h.ledger.systems, ExpandSystem{
			System: sys, Verdict: VerdictPending, UnchartedCount: 2,
			SeedShip: "PROBE-" + sys, SeedState: SeedStateCharting,
		})
		h.ships.positions["PROBE-"+sys] = ShipPos{
			Waypoint: sys + "-A1", NavStatus: navigation.NavStatusDocked, Found: true,
		}
		uncharted[sys] = []string{sys + "-A1"}
	}
	return h, &fakeUncharted{bySystem: uncharted}
}

func runExpandAt(t *testing.T, maxActions int) ExpandReport {
	t.Helper()
	h, uncharted := chartingFleet(idleBudget(MaxExpansionActions) + 2)
	rep, err := h.runWithKnobs(t, uncharted, ExpandKnobs{
		SeedsEnabled: true, MinBudgetRate: 0.05, Whitelist: h.whitelist,
		MaxActions: maxActions,
	})
	require.NoError(t, err)
	require.Equal(t, rep.Actions, h.seed.countOf("chart"), "every action here is one chart command")
	return rep
}

func TestExpansionActions_SpendTheScaledBudgetAgainstAnIdleRequestBudget(t *testing.T) {
	want := idleBudget(MaxExpansionActions)

	rep := runExpandAt(t, want)

	require.Equal(t, want, rep.Actions, "the seed machinery must spend the budget it was handed")
	require.Equal(t, want, rep.ActionLimit)
}

func TestExpansionActions_AFullyBoundBudgetStopsAtTheShippedCap(t *testing.T) {
	rep := runExpandAt(t, boundBudget(MaxExpansionActions))

	require.Equal(t, MaxExpansionActions, rep.Actions)
	require.Equal(t, MaxExpansionActions, rep.ActionLimit)
}

func TestExpansionActions_ANonPositiveBudgetIsTheShippedCap(t *testing.T) {
	rep := runExpandAt(t, 0)

	require.Equal(t, MaxExpansionActions, rep.Actions)
	require.Equal(t, MaxExpansionActions, rep.ActionLimit)
}

func runYardSweepAt(t *testing.T, maxReads int) (YardCatalogReport, *yardWorld) {
	t.Helper()
	world := newYardWorld()
	for i := 0; i < idleBudget(MaxYardCatalogReads)+3; i++ {
		world.yard(fmt.Sprintf("X1-AA11-P%03d", i), 1, "SHIP_PROBE")
	}
	rep, err := ReadYardCatalogues(context.Background(),
		YardCatalogPorts{Frontier: world, Catalog: world}, testPlayerID, maxReads)
	require.NoError(t, err)
	return rep, world
}

func TestYardCatalogues_SpendTheScaledBudgetAgainstAnIdleRequestBudget(t *testing.T) {
	want := idleBudget(MaxYardCatalogReads)

	rep, world := runYardSweepAt(t, want)

	require.Len(t, world.reads, want, "the sweep must read up to the budget it was handed")
	require.Equal(t, want, rep.Read)
	require.Equal(t, want, rep.ReadLimit)
}

func TestYardCatalogues_AFullyBoundBudgetReadsExactlyTheShippedCap(t *testing.T) {
	rep, world := runYardSweepAt(t, boundBudget(MaxYardCatalogReads))

	require.Len(t, world.reads, MaxYardCatalogReads)
	require.Equal(t, MaxYardCatalogReads, rep.ReadLimit)
}

// unpricedYards is n systems each holding one dark yard and two redundant market probes,
// so every request has a releasable hull of its own and only the cap can bind.
func unpricedYards(n int) ([]QueuedSlot, *fakePresenceDemand) {
	slots := make([]QueuedSlot, 0, 3*n)
	requests := make([]yardscan.PresenceRequest, 0, n)
	for i := 0; i < n; i++ {
		system := fmt.Sprintf("X1-P%03d", i)
		slots = append(slots,
			presenceMarket(system, system+"-A1", "HULL-A"+system, 100, "FUEL", "IRON"),
			presenceMarket(system, system+"-A2", "HULL-B"+system, 200, "FUEL", "IRON"),
			wantedYard(system, system+"-Y1", "GOLD"),
		)
		requests = append(requests, presenceRequest(system+"-Y1", system, true))
	}
	// Tokens past the cap, so the ALLOWANCE cannot be what stops the pass.
	return slots, &fakePresenceDemand{requests: requests, tokens: 4 * n}
}

func runPresenceAt(t *testing.T, maxDispatches int) YardPresenceReport {
	t.Helper()
	slots, demand := unpricedYards(idleBudget(MaxYardPresenceDispatches) + 2)
	ports, _, _ := presenceWorld(t, slots, nil, demand)
	rep, err := DispatchYardPresence(context.Background(), ports, testPlayerID, maxDispatches)
	require.NoError(t, err)
	return rep
}

func TestYardPresence_SpendsTheScaledBudgetAgainstAnIdleRequestBudget(t *testing.T) {
	want := idleBudget(MaxYardPresenceDispatches)

	rep := runPresenceAt(t, want)

	require.Equal(t, want, rep.Dispatched, "the pass must dispatch up to the budget it was handed")
	require.Equal(t, want, rep.DispatchLimit)
}

func TestYardPresence_AFullyBoundBudgetStopsAtTheShippedCap(t *testing.T) {
	rep := runPresenceAt(t, boundBudget(MaxYardPresenceDispatches))

	require.Equal(t, MaxYardPresenceDispatches, rep.Dispatched)
	require.Equal(t, MaxYardPresenceDispatches, rep.DispatchLimit)
}

func runPlacementAt(t *testing.T, maxActions int) (PlacementReport, *fakeMover) {
	t.Helper()
	slots := make([]QueuedSlot, 0, idleBudget(DefaultMaxPlacementActions)+3)
	positions := map[string]ShipPos{}
	for i := 0; i < cap(slots); i++ {
		hull := fmt.Sprintf("PROBE-%03d", i)
		slots = append(slots, QueuedSlot{
			Waypoint: fmt.Sprintf("X1-AA-M%03d", i), System: "X1-AA",
			Kind: SlotKindMarket, State: SlotStateBought, AssignedShip: hull,
		})
		positions[hull] = ShipPos{Waypoint: "X1-AA-Y1", NavStatus: navigation.NavStatusDocked, Found: true}
	}
	mover := &fakeMover{}
	ports := PlacementPorts{
		Ledger: &fakeBuyLedger{slots: slots},
		Ships:  &fakeShipReader{positions: positions},
		Mover:  mover,
		Fleet:  &fakeFleet{},
	}

	rep, err := AdvancePlacements(context.Background(), ports, testPlayerID, maxActions)
	require.NoError(t, err)
	return rep, mover
}

func TestPlacements_SpendTheScaledBudgetAgainstAnIdleRequestBudget(t *testing.T) {
	want := idleBudget(DefaultMaxPlacementActions)

	rep, mover := runPlacementAt(t, want)

	require.Len(t, mover.navigates, want, "the placement machine must move up to the budget it was handed")
	require.Equal(t, want, rep.Actions)
	require.Equal(t, want, rep.ActionLimit)
}

func TestPlacements_AFullyBoundBudgetStopsAtTheShippedCap(t *testing.T) {
	rep, mover := runPlacementAt(t, boundBudget(DefaultMaxPlacementActions))

	require.Len(t, mover.navigates, DefaultMaxPlacementActions)
	require.Equal(t, DefaultMaxPlacementActions, rep.ActionLimit)
}

func runReapAt(t *testing.T, maxReaps int) (ReapReport, *fakeReapLedger) {
	t.Helper()
	led := &fakeReapLedger{systems: []ExpandSystem{{System: "X1-GONE", Verdict: VerdictNoWhitelist}}}
	for i := 0; i < idleBudget(DefaultMaxReaps)+5; i++ {
		led.slots = append(led.slots, QueuedSlot{
			Waypoint: fmt.Sprintf("X1-GONE-M%03d", i), System: "X1-GONE",
			Kind: SlotKindMarket, State: SlotStateQueued,
		})
	}
	rep, err := ReapStrandedClaims(context.Background(), reapPortsFor(led), testPlayerID, maxReaps)
	require.NoError(t, err)
	return rep, led
}

func TestReap_SpendsTheScaledBudgetAgainstAnIdleRequestBudget(t *testing.T) {
	want := idleBudget(DefaultMaxReaps)

	rep, led := runReapAt(t, want)

	require.Len(t, led.transitions, want, "the reaper must release up to the budget it was handed")
	require.Equal(t, want, rep.Reaped)
	require.Equal(t, want, rep.ReapLimit)
}

func TestReap_AFullyBoundBudgetStopsAtTheShippedCap(t *testing.T) {
	rep, led := runReapAt(t, boundBudget(DefaultMaxReaps))

	require.Len(t, led.transitions, DefaultMaxReaps)
	require.Equal(t, DefaultMaxReaps, rep.ReapLimit)
}

// --- what a tick that never ran reports -------------------------------------------

// A SKIPPED OR ERRORED TICK MUST REPORT 0/limit, NEVER 0/0, and the gate read is where
// that bites: its budget is resolved inside a pass a skipped tick never reaches, so a
// limit stamped there alone would publish limit=0 beside used=0 — which satisfies the
// gauge's own "used >= limit means this pass bound the tick" predicate, and does it during
// the rate-limit storm that skipped the tick, when the number most has to be true.
func TestExpandReport_ABudgetSkippedTickStillNamesBothBudgets(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{{System: "X1-A", Verdict: VerdictInScope, CatalogKnown: true}}

	rep, err := AdvanceExpansion(context.Background(), h.ports(), testPlayerID, ExpandKnobs{
		SeedsEnabled: true, MinBudgetRate: 0.020, Whitelist: h.whitelist,
		MaxActions: idleBudget(MaxExpansionActions), MaxGateReads: idleBudget(MaxGateReads),
	}, 0.009)
	require.NoError(t, err)
	require.Equal(t, SkippedBudget, rep.Skipped, "the fixture must actually be held by the gate")

	require.Equal(t, idleBudget(MaxGateReads), rep.GateReadLimit,
		"a skipped tick reports the gate budget it did not spend; 0/0 reads as a bound pass")
	require.Equal(t, idleBudget(MaxExpansionActions), rep.ActionLimit)
	require.Zero(t, rep.GatesRead+rep.GatesUnreadable+rep.GatesFailed, "and it spent none of it")
}

// The same shape with NO budgets handed in: a skipped tick names the engine's own
// constants rather than zeros.
func TestExpandReport_ASkippedTickWithNoBudgetsNamesTheShippedConstants(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{{System: "X1-A", Verdict: VerdictInScope, CatalogKnown: true}}

	rep, err := AdvanceExpansion(context.Background(), h.ports(), testPlayerID, ExpandKnobs{
		SeedsEnabled: true, MinBudgetRate: 0.020, Whitelist: h.whitelist,
	}, 0.009)
	require.NoError(t, err)

	require.Equal(t, MaxGateReads, rep.GateReadLimit)
	require.Equal(t, MaxExpansionActions, rep.ActionLimit)
}

// The placement machine carries the same hazard one layer down, and both of its budgets
// are stamped before the worklist read that can fail.
func TestPlacementReport_AFailedWorklistReadStillNamesBothBudgets(t *testing.T) {
	ports := PlacementPorts{
		Ledger: &fakeBuyLedger{slotsErr: errors.New("ledger down")},
		Ships:  &fakeShipReader{positions: map[string]ShipPos{}},
		Mover:  &fakeMover{},
		Fleet:  &fakeFleet{},
	}

	rep, err := AdvancePlacements(context.Background(), ports, testPlayerID, idleBudget(DefaultMaxPlacementActions))
	require.Error(t, err)

	require.Equal(t, idleBudget(DefaultMaxPlacementActions), rep.ActionLimit)
	require.Equal(t, idleBudget(DefaultMaxPlacementActions)*placementFailureBudgetMultiple, rep.FailureLimit)
}

// THE REFUSAL BUDGET IS ITS OWN BUDGET, and it can end a tick while the accepted-command
// one is barely touched — the exact shape a report carrying only `place` cannot describe.
func TestPlacements_TheRefusalBudgetIsReportedBesideTheAcceptedOne(t *testing.T) {
	slots := make([]QueuedSlot, 0, 64)
	positions := map[string]ShipPos{}
	for i := 0; i < cap(slots); i++ {
		hull := fmt.Sprintf("PROBE-%03d", i)
		slots = append(slots, QueuedSlot{
			Waypoint: fmt.Sprintf("X1-AA-M%03d", i), System: "X1-AA",
			Kind: SlotKindMarket, State: SlotStateBought, AssignedShip: hull,
		})
		positions[hull] = ShipPos{Waypoint: "X1-AA-Y1", NavStatus: navigation.NavStatusDocked, Found: true}
	}
	// Every move refused, so only the refusal budget can stop the tick.
	mover := &fakeMover{navErr: errors.New("refused")}
	ports := PlacementPorts{
		Ledger: &fakeBuyLedger{slots: slots},
		Ships:  &fakeShipReader{positions: positions},
		Mover:  mover,
		Fleet:  &fakeFleet{},
	}

	rep, err := AdvancePlacements(context.Background(), ports, testPlayerID, 4)
	require.NoError(t, err)

	require.Equal(t, 4*placementFailureBudgetMultiple, rep.FailureLimit)
	require.Equal(t, rep.FailureLimit, rep.Failures, "the refusal budget is what ended this tick")
	require.Zero(t, rep.Actions, "while the accepted-command budget went entirely unspent")
	require.Equal(t, 4, rep.ActionLimit)
}
