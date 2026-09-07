package commands

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// probe_sensing_pass_budget_test.go covers the coordinator's half of the burst-budget
// scaling: the reading it feeds the engines, and the used/limit report that finally makes
// "which cap bound this tick" answerable.
//
// Coverage stopped at 2.6% of the map for six days with nobody able to say which of six
// budgets was holding it, because every pass reported only what it DID. The report half is
// not a nicety attached to the scaling — it is the half that stops the next era guessing.

// fakeSensingSaturation is a fixed request-budget reading.
type fakeSensingSaturation struct{ permille int }

func (r *fakeSensingSaturation) SaturationPermille(_ context.Context) int { return r.permille }

// saturatedReader reads a fully committed request budget, which is the regime the engine's
// own constants were sized for.
func saturatedReader() APISaturationReader {
	return &fakeSensingSaturation{permille: trading.APISaturationPermilleMax}
}

// --- the reading -----------------------------------------------------------------

// THE INVERSION, PINNED SO NOBODY "FIXES" IT. An unwired estimator reads 0, and for THIS
// consumer 0 is a fully idle budget and therefore full headroom — the opposite of the tour
// objective's fail-open, which treats the same 0 as "no opinion, do not price the budget".
//
// Reading it the other way round would be the quiet failure: a coordinator whose estimator
// was never wired, or whose window is too thin to price, would pace every pass as though
// the limiter were saturated and the whole change would ship inert. Nothing here spends
// credits, so there is no guard to fail closed.
func TestSensingBudgets_AnUnwiredEstimatorIsAFullyIdleBudgetNotASaturatedOne(t *testing.T) {
	h := &RunProbeSensingCoordinatorHandler{}

	require.Zero(t, h.saturationPermille(context.Background()),
		"a nil reader must read as a budget nobody is queued on")

	unwired := h.budgetsFor(context.Background(), sensingConfig{ExpansionHeadroomMultiple: parkedsensing.ExpansionHeadroomMultiple})
	idle := resolveSensingBudgets(0, parkedsensing.ExpansionHeadroomMultiple)
	require.Equal(t, idle, unwired, "an unwired estimator gives the engine its full headroom")

	saturated := resolveSensingBudgets(trading.APISaturationPermilleMax, parkedsensing.ExpansionHeadroomMultiple)
	require.NotEqual(t, saturated, unwired,
		"reading a missing estimator as a SATURATED budget would ship the whole change inert")
	require.Greater(t, unwired.gate, saturated.gate)
}

// The wired reading reaches every pass, and every pass is scaled from the same one.
func TestSensingBudgets_EveryPacedPassScalesOffTheOneReading(t *testing.T) {
	const sixPercent = 60
	h := &RunProbeSensingCoordinatorHandler{}
	h.SetAPISaturationReader(&fakeSensingSaturation{permille: sixPercent})

	got := h.budgetsFor(context.Background(), sensingConfig{ExpansionHeadroomMultiple: parkedsensing.ExpansionHeadroomMultiple})

	require.Equal(t, sixPercent, got.permille, "the reading travels with the budgets it produced")
	require.Equal(t, 17, got.gate, "3 gate reads a tick becomes 17 — the number the change was sized on")
	require.Equal(t, 114, got.expand)
	require.Equal(t, 57, got.place)
	require.Equal(t, 45, got.yards)
	require.Equal(t, 11, got.presence)
	require.Equal(t, 114, got.reap)
}

// A saturated budget resolves to the engine's own constants, exactly.
func TestSensingBudgets_AFullyBoundBudgetResolvesToTheShippedConstants(t *testing.T) {
	h := &RunProbeSensingCoordinatorHandler{}
	h.SetAPISaturationReader(saturatedReader())

	got := h.budgetsFor(context.Background(), sensingConfig{ExpansionHeadroomMultiple: parkedsensing.ExpansionHeadroomMultiple})

	require.Equal(t, parkedsensing.MaxGateReads, got.gate)
	require.Equal(t, parkedsensing.MaxExpansionActions, got.expand)
	require.Equal(t, parkedsensing.DefaultMaxPlacementActions, got.place)
	require.Equal(t, parkedsensing.MaxYardCatalogReads, got.yards)
	require.Equal(t, parkedsensing.MaxYardPresenceDispatches, got.presence)
	require.Equal(t, parkedsensing.DefaultMaxReaps, got.reap)
	require.Equal(t, DefaultMaxAdoptions, got.adopt)
}

// The knob binds through the resolved config: a multiple of one is the operator's way back
// to the pre-scaling pacing even against a completely idle budget.
func TestSensingBudgets_AHeadroomMultipleOfOneRestoresTheShippedPacing(t *testing.T) {
	h := &RunProbeSensingCoordinatorHandler{} // unwired reader = fully idle budget

	got := h.budgetsFor(context.Background(), sensingConfig{ExpansionHeadroomMultiple: 1})

	require.Equal(t, parkedsensing.MaxGateReads, got.gate)
	require.Equal(t, parkedsensing.MaxExpansionActions, got.expand)
	require.Equal(t, parkedsensing.DefaultMaxPlacementActions, got.place)
	require.Equal(t, parkedsensing.MaxYardCatalogReads, got.yards)
	require.Equal(t, parkedsensing.MaxYardPresenceDispatches, got.presence)
	require.Equal(t, parkedsensing.DefaultMaxReaps, got.reap)
	require.Equal(t, DefaultMaxAdoptions, got.adopt)
}

// --- the report ---------------------------------------------------------------------

// exhaustedPlacementTick is the shape an operator has to be able to read at a glance: the
// placement machine spent every move it had while expansion stopped for want of work.
func exhaustedPlacementTick() heartbeat {
	return heartbeat{
		budgets: resolveSensingBudgets(600, parkedsensing.ExpansionHeadroomMultiple),
		place:   parkedsensing.PlacementReport{Actions: 10, ActionLimit: 10, Failures: 4, FailureLimit: 30},
		expand: parkedsensing.ExpandReport{
			Actions: 12, ActionLimit: 20,
			GatesRead: 3, GateReadLimit: 3,
		},
		yard:     parkedsensing.YardCatalogReport{Read: 0, ReadLimit: 8},
		presence: parkedsensing.YardPresenceReport{Dispatched: 1, DispatchLimit: 2},
		reap:     parkedsensing.ReapReport{Reaped: 4, ReapLimit: 20},
		adopt:    adoptReport{Adopted: 2, Attempts: 3, Limit: 15},
	}
}

// A tick that exhausts placement but not expansion says so in the one line an operator
// reads: place at its limit, expand below it.
func TestBudgetSummary_NamesTheBudgetThatBoundTheTick(t *testing.T) {
	require.Equal(t,
		"gate 3/3 expand 12/20 place 10/10 place_refused 4/30 yards 0/8 presence 1/2 reap 4/20 adopt 3/15",
		budgetSummary(exhaustedPlacementTick()))
}

// THE REFUSAL BUDGET CAN BIND ALONE. A tick that spent every one of its refusals while
// barely touching its accepted-command budget names a bound pass here and nowhere else —
// without the second row it reads as `place 2/57`, a machine with plenty of headroom, on
// the tick a wall of refusals ended.
func TestBudgetSummary_TheRefusalBudgetIsAPassOfItsOwn(t *testing.T) {
	refused := heartbeat{
		budgets: resolveSensingBudgets(0, parkedsensing.ExpansionHeadroomMultiple),
		place:   parkedsensing.PlacementReport{Actions: 2, ActionLimit: 57, Failures: 171, FailureLimit: 171},
	}

	summary := budgetSummary(refused)
	require.Contains(t, summary, "place 2/57")
	require.Contains(t, summary, "place_refused 171/171")
}

// EVERY LIMIT COMES FROM ITS OWN PASS'S REPORT, and this is the shape that proves it: a
// tick where no pass spent its budget, so a limit derived from a used count — or copied
// from a neighbouring pass — reads visibly wrong. Without it the exhausted fixture above
// passes just as happily against a report that never carried a limit at all.
func TestBudgetSummary_EachLimitIsThePassesOwnAndNotItsSpend(t *testing.T) {
	quiet := heartbeat{
		budgets:  resolveSensingBudgets(0, parkedsensing.ExpansionHeadroomMultiple),
		place:    parkedsensing.PlacementReport{Actions: 1, ActionLimit: 57, Failures: 9, FailureLimit: 171},
		expand:   parkedsensing.ExpandReport{Actions: 2, ActionLimit: 114, GatesRead: 4, GateReadLimit: 17},
		yard:     parkedsensing.YardCatalogReport{Read: 5, ReadLimit: 45},
		presence: parkedsensing.YardPresenceReport{Dispatched: 6, DispatchLimit: 11},
		reap:     parkedsensing.ReapReport{Reaped: 7, ReapLimit: 114},
		adopt:    adoptReport{Adopted: 8, Attempts: 8, Limit: 60},
	}

	require.Equal(t,
		"gate 4/17 expand 2/114 place 1/57 place_refused 9/171 yards 5/45 presence 6/11 reap 7/114 adopt 8/60",
		budgetSummary(quiet))
}

// The same pairs reach the cycle line itself, message and payload alike — a number written
// into a struct nothing logs is a number nobody can read.
func TestCycleLine_CarriesThePerPassBudgets(t *testing.T) {
	log := &messageLogger{}
	h := &RunProbeSensingCoordinatorHandler{}

	h.heartbeat(common.WithLogger(context.Background(), log),
		&RunProbeSensingCoordinatorCommand{ContainerID: "probe_sensing_coordinator-player-1-24f32043"},
		sensingConfig{ProbeCap: 800}, exhaustedPlacementTick())

	require.Len(t, log.messages, 1)
	require.Contains(t, log.messages[0],
		"budgets=gate 3/3 expand 12/20 place 10/10 place_refused 4/30 yards 0/8 presence 1/2 reap 4/20 adopt 3/15")
	require.Contains(t, log.messages[0], "at 600‰ saturation",
		"the reading that sized the limits belongs beside them")

	require.Equal(t, 600, log.fields[0]["api_saturation_permille"])
	rows, ok := log.fields[0]["pass_budgets"].([]map[string]interface{})
	require.True(t, ok, "the payload carries the pairs as queryable rows")
	require.Len(t, rows, 8, "every paced pass is written, including the ones at zero")
	require.Equal(t, map[string]interface{}{"pass": passBudgetPlace, "used": 10, "limit": 10}, rows[2])
	require.Equal(t, map[string]interface{}{"pass": passBudgetPlaceRefused, "used": 4, "limit": 30}, rows[3])
	require.Equal(t, map[string]interface{}{"pass": passBudgetYards, "used": 0, "limit": 8}, rows[4])
	require.Equal(t, map[string]interface{}{"pass": passBudgetAdopt, "used": 3, "limit": 15}, rows[7],
		"adoption is a paced pass now, so it reports used/limit like the rest (sp-v7mtk)")
}

// USED IS ATTEMPTS, not successes, wherever the pass charges attempts: a gate the API
// refuses and a yard read that fails both spent their call and their budget, and a used
// count that ignored them would report a pass as idle on the tick it burned its whole
// budget on refusals.
func TestPassBudgets_CountAttemptsWhereThePassChargesAttempts(t *testing.T) {
	got := passBudgets(heartbeat{
		expand: parkedsensing.ExpandReport{
			GatesRead: 1, GatesUnreadable: 5, GatesFailed: 2, GateReadLimit: 8,
		},
		yard: parkedsensing.YardCatalogReport{Read: 2, Failed: 6, ReadLimit: 8},
		reap: parkedsensing.ReapReport{Reaped: 3, Skipped: 17, ReapLimit: 20},
	})

	byPass := map[string]passBudget{}
	for _, b := range got {
		byPass[b.pass] = b
	}
	require.Equal(t, passBudget{passBudgetGate, 8, 8}, byPass[passBudgetGate],
		"an unreadable gate spent its call, so it spent its budget")
	require.Equal(t, passBudget{passBudgetYards, 8, 8}, byPass[passBudgetYards])
	require.Equal(t, passBudget{passBudgetReap, 20, 20},
		byPass[passBudgetReap], "a lost race still consumed a reap turn")
}

// --- the gauge ------------------------------------------------------------------------

// The scrape surface, which is the half an observability change fails silently at: a pair
// computed for the log and never published leaves "which cap is binding" exactly where it
// was — an inference from a log payload rather than a query.
func TestPublishPassBudgets_EveryPassReachesTheGaugeEveryTick(t *testing.T) {
	h := &RunProbeSensingCoordinatorHandler{}
	rec := newFakeRecorder()
	h.SetMetricsRecorder(rec)

	h.publishPassBudgets(testPlayerID, exhaustedPlacementTick())

	published, writes := rec.recordedPassBudgets()
	require.Equal(t, 8, writes, "every paced pass is published on every tick, zeros included")
	require.Equal(t, recordedPassBudget{used: 4, limit: 30}, published[passBudgetPlaceRefused],
		"the refusal budget is its own series — it ends ticks the accepted-command one does not")
	require.Equal(t, recordedPassBudget{used: 10, limit: 10}, published[passBudgetPlace],
		"the gauge carries BOTH numbers — used alone cannot say whether a pass was bound")
	require.Equal(t, recordedPassBudget{used: 12, limit: 20}, published[passBudgetExpand])
	require.Equal(t, recordedPassBudget{used: 0, limit: 8}, published[passBudgetYards],
		"a pass that did nothing must still publish its limit, or a drained backlog reads as jammed")
}

// A nil recorder is metrics being off, never a panicking tick (RULINGS #4).
func TestPublishPassBudgets_IsInertWithoutARecorder(t *testing.T) {
	require.NotPanics(t, func() {
		(&RunProbeSensingCoordinatorHandler{}).publishPassBudgets(testPlayerID, exhaustedPlacementTick())
	})
}

// --- the knob's own contract -------------------------------------------------------

// The registry bounds this key at 1, so a negative can only arrive from a hand-edited
// config row — and it is silently destructive in the way its neighbours are: it becomes 6
// and the operator sees a paced fleet they thought they had unpaced. Warning is the only
// trace it would otherwise leave.
func TestExpansionHeadroomMultiple_ANegativeWarnsAndFallsBackToTheDefault(t *testing.T) {
	log := &messageLogger{}
	cmd := &RunProbeSensingCoordinatorCommand{ExpansionHeadroomMultiple: -4}

	cfg := resolveSensingConfig(common.WithLogger(context.Background(), log), cmd, nil)

	require.Equal(t, defaultExpansionHeadroomMultiple, cfg.ExpansionHeadroomMultiple)
	_, fields, logged := loggedUnder(log, "parked_sensing_knob_rejected")
	require.True(t, logged, "a negative must not be absorbed in silence, like every knob beside it")
	require.Equal(t, "expansion_headroom_multiple", fields["knob"])
}

// ZERO IS THE REVERT AND IT IS SILENT, the same contract every other key here carries:
// `tune <key> 0` means the documented default fleet-wide, and an absent key resolves to 0
// too — which is the normal state of every default launch.
func TestExpansionHeadroomMultiple_ZeroRevertsSilently(t *testing.T) {
	log := &messageLogger{}

	cfg := resolveSensingConfig(common.WithLogger(context.Background(), log), &RunProbeSensingCoordinatorCommand{}, nil)

	require.Equal(t, defaultExpansionHeadroomMultiple, cfg.ExpansionHeadroomMultiple)
	_, _, logged := loggedUnder(log, "parked_sensing_knob_rejected")
	require.False(t, logged, "the absent-key revert is every default launch and must not warn")
}

// A value past the advertised maximum clamps rather than reaching a multiple nobody sized.
func TestExpansionHeadroomMultiple_ClampsToTheAdvertisedMaximum(t *testing.T) {
	cfg := resolveSensingConfig(context.Background(),
		&RunProbeSensingCoordinatorCommand{ExpansionHeadroomMultiple: 500}, nil)

	require.Equal(t, parkedsensing.MaxExpansionHeadroomMultiple, cfg.ExpansionHeadroomMultiple)
}
