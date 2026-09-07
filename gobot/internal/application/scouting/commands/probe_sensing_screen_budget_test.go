package commands

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// probe_sensing_screen_budget_test.go covers the screening sweep's pacing. A system nobody
// has judged earns no placement, no charting crew and no place in the buy queue, and this
// sweep is the only path that learns a system's waypoint catalogue without first flying a
// hull there — so this budget is the rate at which a discovered system becomes workable.

// idleReader reads a request budget nobody is queued on.
func idleReader() APISaturationReader { return &fakeSensingSaturation{permille: 0} }

// THE BYTE-IDENTICAL FLOOR, at every multiple an operator can reach: the scaling can only
// ever hand this pass MORE work than before, never less.
func TestScreenBudget_AFullyBoundRequestBudgetIsTheShippedBatch(t *testing.T) {
	for _, multiple := range []int{1, parkedsensing.ExpansionHeadroomMultiple, parkedsensing.MaxExpansionHeadroomMultiple} {
		got := resolveSensingBudgets(trading.APISaturationPermilleMax, multiple, defaultSurgeInFlightCap)
		require.Equal(t, screenSweepBatch, got.screen,
			"a saturated budget at multiple %d must be the shipped batch, exactly", multiple)
	}
}

// The floor holds at every reading — the safety argument for scaling a pass whose unit
// cost is a paginated catalogue sweep.
func TestScreenBudget_NeverPacesBelowTheShippedBatch(t *testing.T) {
	for permille := 0; permille <= trading.APISaturationPermilleMax; permille += 50 {
		got := resolveSensingBudgets(permille, parkedsensing.ExpansionHeadroomMultiple, defaultSurgeInFlightCap)
		require.GreaterOrEqual(t, got.screen, screenSweepBatch,
			"the sweep must never be paced below its shipped batch (reading %d‰)", permille)
	}
}

// An idle budget reaches the full multiple, which is the throughput the change is for.
func TestScreenBudget_AnIdleRequestBudgetReachesTheFullMultiple(t *testing.T) {
	require.Equal(t, screenSweepBatch*parkedsensing.ExpansionHeadroomMultiple,
		resolveSensingBudgets(0, parkedsensing.ExpansionHeadroomMultiple, defaultSurgeInFlightCap).screen)
	require.Equal(t, screenSweepBatch*parkedsensing.MaxExpansionHeadroomMultiple,
		resolveSensingBudgets(0, parkedsensing.MaxExpansionHeadroomMultiple, defaultSurgeInFlightCap).screen)
	require.Equal(t, screenSweepBatch, resolveSensingBudgets(0, 1, defaultSurgeInFlightCap).screen,
		"a multiple of one is the operator's way back to the pre-scaling pacing")
}

// THE SAME READING as every pass beside it: a second estimator here would let the screen
// disagree with the passes downstream about how loaded the fleet is.
func TestScreenBudget_ScalesOffTheOneReadingLikeEveryPassBesideIt(t *testing.T) {
	const sixPercent = 60
	h := &RunProbeSensingCoordinatorHandler{}
	h.SetAPISaturationReader(&fakeSensingSaturation{permille: sixPercent})

	got := h.budgetsFor(context.Background(), sensingConfig{ExpansionHeadroomMultiple: parkedsensing.ExpansionHeadroomMultiple})

	require.Equal(t, sixPercent, got.permille)
	require.Equal(t, parkedsensing.PacedBudget(screenSweepBatch, sixPercent, parkedsensing.ExpansionHeadroomMultiple),
		got.screen, "the screen is paced by the same function, off the same reading")
	require.Greater(t, got.screen, screenSweepBatch)
}

// A NON-POSITIVE BUDGET IS THE CALLER'S SENTINEL, NOT AN OFF SWITCH: reading a zero
// literally would let a coordinator that forgot the argument dark the whole engine.
func TestScreenSweep_ANonPositiveBudgetFallsBackToTheShippedBatch(t *testing.T) {
	world := stillChartingWorld(t, []string{"X1-G", "X1-F", "X1-E", "X1-D", "X1-C", "X1-B", "X1-A"})
	cyc := sensingCycle{
		cmd:   world.cmd,
		cfg:   resolveSensingConfig(world.ctx, world.cmd, nil),
		ports: world.ports,
	}

	rep, err := world.handler.screenSweep(world.ctx, cyc, 0)

	require.NoError(t, err)
	require.Equal(t, screenSweepBatch, rep.Limit, "zero means the pass's own base, never nothing")
	require.Equal(t, screenSweepBatch, rep.Screened)
}

// pendingBacklog names n systems that stay PENDING, reverse-alphabetically so the
// least-recently-screened ordering is not the alphabetical one by accident.
func pendingBacklog(n int) []string {
	systems := make([]string, 0, n)
	for i := n - 1; i >= 0; i-- {
		systems = append(systems, fmt.Sprintf("X1-S%03d", i))
	}
	return systems
}

// THE CHANGE, END TO END: a backlog the ceiling-era batch spreads over three ticks (the
// rotation test beside this one) is judged in ONE while the request budget is idle.
func TestScreenSweep_AnIdleRequestBudgetJudgesInOneTickWhatTheCeilingSpreadsOverSeveral(t *testing.T) {
	all := pendingBacklog(12)
	world := stillChartingWorld(t, all)
	world.handler.SetAPISaturationReader(idleReader())

	screened := screenedDuring(t, world)

	require.Len(t, screened, len(all), "an idle request budget reaches the whole backlog in one tick")
	for _, system := range all {
		require.True(t, screened[system], "%s waited a tick it did not need to", system)
	}
}

// THE BACKLOG-NOT-LOST PROPERTY, WHICH THE SCALING MUST NOT COST US. A system past the
// budget is not written off and not forgotten: it is still PENDING, and the next tick
// reaches it. A burst bound must defer work rather than drop it.
func TestScreenSweep_SystemsPastTheBudgetStayPendingAndAreReachedNextTick(t *testing.T) {
	budget := resolveSensingBudgets(0, parkedsensing.ExpansionHeadroomMultiple, defaultSurgeInFlightCap).screen
	all := pendingBacklog(budget + 4)
	world := stillChartingWorld(t, all)
	world.handler.SetAPISaturationReader(idleReader())

	first := screenedDuring(t, world)
	require.Len(t, first, budget, "the tick screens its budget and stops")

	leftOver := make([]string, 0, 4)
	for _, system := range all {
		if first[system] {
			continue
		}
		leftOver = append(leftOver, system)
		require.Equal(t, parkedsensing.VerdictPending, world.ledger.systems[system].Verdict,
			"%s was past the budget, so it must still be awaiting screening", system)
	}
	require.Len(t, leftOver, 4, "exactly the systems past the budget waited")

	second := screenedDuring(t, world)
	for _, system := range leftOver {
		require.True(t, second[system],
			"%s waited a tick and must be reached by the next one, or the backlog is lost", system)
	}
}

// THE FIELD EVERY DIAGNOSIS NEEDS: a screened count alone cannot say whether the sweep had
// nothing left to judge or was pinned at its cap, and it must reach the LIVE cycle line —
// a number computed and never logged is a number nobody can act on.
func TestScreenSweep_TheCycleLineNamesTheScreenBudgetItWasChargedAgainst(t *testing.T) {
	budget := resolveSensingBudgets(0, parkedsensing.ExpansionHeadroomMultiple, defaultSurgeInFlightCap).screen
	world := stillChartingWorld(t, pendingBacklog(budget+2))
	world.handler.SetAPISaturationReader(idleReader())

	log := &messageLogger{}
	world.ctx = common.WithLogger(world.ctx, log)
	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

	require.NotEmpty(t, log.messages)
	require.Contains(t, log.messages[0], fmt.Sprintf("budgets=screen %d/%d", budget, budget),
		"the sweep reports used/limit on the cycle line beside every other paced pass")

	rows, ok := log.fields[0]["pass_budgets"].([]map[string]interface{})
	require.True(t, ok)
	require.Equal(t, map[string]interface{}{"pass": passBudgetScreen, "used": budget, "limit": budget}, rows[0],
		"and reaches the queryable payload the gauge is published from")
}
