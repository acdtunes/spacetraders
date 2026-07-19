package capacity_test

// Behavioral tests for the heuristic planner. Every test drives the
// frozen Planner port (ComputeDesired) and asserts the DesiredTopology — the
// planner's one observable outcome. Pure function ⇒ table-driven fixtures.
//
// Fixtures are calibrated to the 2026-07-15 design-spec narrative: a slow-cycle
// high-frequency contract hub (the J58 shape observed across 854 prod
// contracts) whose good-mix contains the spec's canonical never-buffer example
// (AMMONIA_ICE: 59 units × ~751 source distance).
//
// Expected values are hand-derived literals from the documented policy, never
// recomputed from production constants (no circular verification).
//
// Test budget: 8 distinct behaviors × 2 = 16 max; 10 test functions below.
// (Behavior 1 spans three functions: the ranked-walk table plus two dedicated
// fixtures pinning the ranking's cycle-penalty term and the until-stop
// semantics — same behavior, input shapes the shared fixture cannot express.)

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
)

// ---- fixtures -----------------------------------------------------------------

// plannerCalibration is DefaultCalibration with the two planner knobs set.
func plannerCalibration(addThresholdPerHullCrHr float64, stockerBudgetUnits int) capacity.Calibration {
	calibration := capacity.DefaultCalibration()
	calibration.AddThresholdPerHullCrHr = addThresholdPerHullCrHr
	calibration.StockerCapacityBudget = stockerBudgetUnits
	return calibration
}

// contractMachineSignals is the two-hub scenario:
//
//	X1-J58-A1 — high-frequency (2.0/hr), high-payment (15000), SLOW cycle
//	  (5400s): rank score 2.0 × (1+5400/3600) × 15000 = 75000. Good-mix,
//	  value_density = freq × (0.030·dist + 0.147·avg) ÷ avg:
//	  IRON  (1.2/hr × 30u, source 60)  → 1.2×(1.8+4.41)/30    ≈ 0.248
//	  FUEL  (0.8/hr × 20u, source 40)  → 0.8×(1.2+2.94)/20    ≈ 0.166
//	  AMMONIA_ICE (0.3/hr × 59u, source 751) → 0.3×(22.53+8.673)/59 ≈ 0.159 —
//	    the FAR-sourced good the pre-fix divide-by-distance score wrongly
//	    floored out; value density RANKS it IN, just behind the near goods.
//	X1-QQ7-B2 — marginal (0.2/hr × 2500, cycle 900s): rank score 625. Its
//	  3-hull minimal plan (COPPER buffered) yields 500/3 ≈ 167 cr/hr per hull.
func contractMachineSignals() capacity.Signals {
	return capacity.Signals{
		PlayerID: 1,
		Demand: capacity.DemandSignals{Hubs: []capacity.HubDemand{
			{
				HubSymbol:         "X1-J58-A1",
				ContractFrequency: 2.0,
				AvgPaymentCredits: 15000,
				GoodMix: []capacity.GoodDemand{
					{Good: "IRON", Frequency: 1.2, AvgUnits: 30},
					{Good: "FUEL", Frequency: 0.8, AvgUnits: 20},
					{Good: "AMMONIA_ICE", Frequency: 0.3, AvgUnits: 59},
				},
			},
			{
				HubSymbol:         "X1-QQ7-B2",
				ContractFrequency: 0.2,
				AvgPaymentCredits: 2500,
				GoodMix: []capacity.GoodDemand{
					{Good: "COPPER", Frequency: 0.15, AvgUnits: 25},
				},
			},
		}},
		Performance: capacity.PerformanceSignals{Hubs: []capacity.HubPerformance{
			{HubSymbol: "X1-J58-A1", CycleTimeSeconds: 5400, StallEvents: 6},
			{HubSymbol: "X1-QQ7-B2", CycleTimeSeconds: 900, StallEvents: 0},
		}},
		Economics: capacity.EconomicsSignals{
			TreasuryCredits:  500000,
			FleetPerHullCrHr: 2000,
			FleetHullCount:   12,
			SourceDistances: []capacity.GoodSourceDistance{
				{HubSymbol: "X1-J58-A1", Good: "IRON", Distance: 60},
				{HubSymbol: "X1-J58-A1", Good: "FUEL", Distance: 40},
				{HubSymbol: "X1-J58-A1", Good: "AMMONIA_ICE", Distance: 751},
				{HubSymbol: "X1-QQ7-B2", Good: "COPPER", Distance: 120},
			},
		},
	}
}

// singleGoodSignals is a one-hub/one-good scenario for cap and count arithmetic.
func singleGoodSignals(hubFrequency, payment, cycleSeconds, goodFrequency, averageUnits, distance float64) capacity.Signals {
	signals := capacity.Signals{
		Demand: capacity.DemandSignals{Hubs: []capacity.HubDemand{{
			HubSymbol:         "X1-CAP-H1",
			ContractFrequency: hubFrequency,
			AvgPaymentCredits: payment,
			GoodMix:           []capacity.GoodDemand{{Good: "ORE", Frequency: goodFrequency, AvgUnits: averageUnits}},
		}}},
		Economics: capacity.EconomicsSignals{
			SourceDistances: []capacity.GoodSourceDistance{{HubSymbol: "X1-CAP-H1", Good: "ORE", Distance: distance}},
		},
	}
	if cycleSeconds > 0 {
		signals.Performance = capacity.PerformanceSignals{Hubs: []capacity.HubPerformance{
			{HubSymbol: "X1-CAP-H1", CycleTimeSeconds: cycleSeconds},
		}}
	}
	return signals
}

func computeDesired(t *testing.T, signals capacity.Signals, calibration capacity.Calibration) capacity.DesiredTopology {
	t.Helper()
	desired, err := capacity.NewHeuristicPlanner().ComputeDesired(context.Background(), signals, calibration)
	require.NoError(t, err, "the heuristic planner is pure: it must never fail, only plan conservatively")
	return desired
}

func hubSymbols(desired capacity.DesiredTopology) []string {
	var symbols []string
	for _, hub := range desired.Hubs {
		symbols = append(symbols, hub.HubSymbol)
	}
	return symbols
}

// ---- behaviors ------------------------------------------------------------------

// Behavior 1: hub coverage walks the frequency × cycle_penalty × payment
// ranking and STOPS at the first NOT-yet-covered hub whose marginal hull falls
// below the ADD requirement — the max of the universal floor and the live
// fleet average (the absorption ceiling). The desired topology self-limits.
// The fixture's actual topology is EMPTY, so every hub here is an add. The
// universal floor is the calibrated add-threshold; while uncalibrated (0) it
// resolves to the planner's documented cold-start floor (500 cr/hr), so even
// a fleet with no history plans conservatively instead of covering every
// paying hub — an explicit calibrated value overrides the cold-start floor.
//
// Marginal arithmetic (fixture): J58 plans 3 workers + 1 stocker + 2 warehouses
// (164 buffered cap-units over two 120-holds) for 2.0/hr ×
// 15000 = 30000 cr/hr ⇒ 5000 cr/hr per hull. QQ7 plans 3 hulls for 500 cr/hr ⇒
// ≈167 cr/hr per hull. Every case threshold below still sits clear of 5000/167.
func TestHeuristicPlanner_CoversRankedHubsUntilMarginalHullFallsBelowRequirement(t *testing.T) {
	cases := []struct {
		name             string
		addThreshold     float64
		fleetPerHullCrHr float64
		wantHubs         []string
	}{
		{
			name:             "high-frequency high-payment slow-cycle hub covered; marginal hub below add-threshold is not",
			addThreshold:     1500,
			fleetPerHullCrHr: 2000,
			wantHubs:         []string{"X1-J58-A1"},
		},
		{
			name:             "absorption ceiling: a fleet averaging above an UNCOVERED top hub's marginal hull adds nothing",
			addThreshold:     0,
			fleetPerHullCrHr: 7000,
			wantHubs:         nil,
		},
		{
			name:             "cold start (no threshold, no fleet history) plans under the cold-start floor, not every paying hub",
			addThreshold:     0,
			fleetPerHullCrHr: 0,
			wantHubs:         []string{"X1-J58-A1"}, // QQ7's ≈167 cr/hr marginal hull is junk even for a cold fleet
		},
		{
			name:             "an explicitly calibrated low threshold overrides the cold-start floor",
			addThreshold:     100,
			fleetPerHullCrHr: 0,
			wantHubs:         []string{"X1-J58-A1", "X1-QQ7-B2"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			signals := contractMachineSignals()
			signals.Economics.FleetPerHullCrHr = testCase.fleetPerHullCrHr

			desired := computeDesired(t, signals, plannerCalibration(testCase.addThreshold, 0))

			require.Equal(t, testCase.wantHubs, hubSymbols(desired))
			require.Equal(t, len(testCase.wantHubs) == 0, desired.IsEmpty())
		})
	}
}

// Behavior 1 (ranking term): cycle-time is THE control lever — the cycle
// penalty must be able to INVERT the naive frequency × payment order, and
// with the add gate sitting between the two hubs' marginals the ranking
// decides WHICH hub is covered, not merely the listing order:
//
//	SLOW X1-SLW-B2: 0.9/hr × 20000 = 18000 raw; ×(1 + 2h) = 54000 score.
//	  2 workers (ceil(0.9 × 2h)) ⇒ marginal 9000 cr/hr — clears the 6000 gate.
//	FAST X1-FST-A1: 8.0/hr × 2500 = 20000 raw; ×(1 + 0.5h) = 30000 score.
//	  4 workers (ceil(8.0 × 0.5h)) ⇒ marginal 5000 cr/hr — fails the 6000 gate.
//
// Correct ranking walks SLOW first (covered) and stops at FAST. A regression
// dropping or inverting the cycle-penalty term ranks FAST first, fails the
// gate immediately, and plans NOTHING — this fixture fails loudly instead of
// letting the ranking silently degrade to naive freq × payment.
func TestHeuristicPlanner_CyclePenaltyInvertsRawRevenueRankAndDecidesCoverage(t *testing.T) {
	signals := capacity.Signals{
		Demand: capacity.DemandSignals{Hubs: []capacity.HubDemand{
			{HubSymbol: "X1-FST-A1", ContractFrequency: 8.0, AvgPaymentCredits: 2500},
			{HubSymbol: "X1-SLW-B2", ContractFrequency: 0.9, AvgPaymentCredits: 20000},
		}},
		Performance: capacity.PerformanceSignals{Hubs: []capacity.HubPerformance{
			{HubSymbol: "X1-FST-A1", CycleTimeSeconds: 1800},
			{HubSymbol: "X1-SLW-B2", CycleTimeSeconds: 7200},
		}},
	}

	desired := computeDesired(t, signals, plannerCalibration(6000, 0))

	require.Equal(t, []string{"X1-SLW-B2"}, hubSymbols(desired),
		"the slow-cycle hub has the most cycle-time to recover from co-location: it must outrank the higher-raw-revenue fast hub and be the one covered")
}

// addWalkRankedSignals is the three-hub until-semantics fixture. Rank score is
// NOT monotonic with the marginal per-hull gate — the hull-heavy rank-2 hub
// fails a 5000 gate the lean rank-3 hub would clear:
//
//	X1-TOP-R1:  2.0/hr × 20000, cycle 3600s ⇒ score 80000; 2 workers  ⇒ marginal 20000.
//	X1-FAT-R2:  5.0/hr ×  2400, cycle 7200s ⇒ score 36000; 10 workers ⇒ marginal 1200.
//	X1-LEAN-R3: 1.0/hr ×  9000, cycle 1800s ⇒ score 13500; 1 worker   ⇒ marginal 9000.
func addWalkRankedSignals() capacity.Signals {
	return capacity.Signals{
		Demand: capacity.DemandSignals{Hubs: []capacity.HubDemand{
			{HubSymbol: "X1-TOP-R1", ContractFrequency: 2.0, AvgPaymentCredits: 20000},
			{HubSymbol: "X1-FAT-R2", ContractFrequency: 5.0, AvgPaymentCredits: 2400},
			{HubSymbol: "X1-LEAN-R3", ContractFrequency: 1.0, AvgPaymentCredits: 9000},
		}},
		Performance: capacity.PerformanceSignals{Hubs: []capacity.HubPerformance{
			{HubSymbol: "X1-TOP-R1", CycleTimeSeconds: 3600},
			{HubSymbol: "X1-FAT-R2", CycleTimeSeconds: 7200},
			{HubSymbol: "X1-LEAN-R3", CycleTimeSeconds: 1800},
		}},
	}
}

// Behavior 1 (stop semantics): the ranked ADD walk STOPS at the first hub
// whose marginal hull falls below the requirement — the spec's "cover the top
// hubs UNTIL the marginal hull's projected per-hull-$/hr falls below
// threshold" is a stop-walk, not a filter. The lean rank-3 hub's own marginal
// (9000) clears the 5000 gate, but it sits behind the rank-2 failure and must
// NOT be covered.
func TestHeuristicPlanner_AddWalkStopsAtFirstMarginalFailureNotFilteringPastIt(t *testing.T) {
	desired := computeDesired(t, addWalkRankedSignals(), plannerCalibration(5000, 0))

	require.Equal(t, []string{"X1-TOP-R1"}, hubSymbols(desired),
		"a gate-clearing hub ranked behind the first add failure must be excluded (until ⇒ stop, not filter)")
}

// coveredCluster is one live cluster with a single co-located worker hull —
// the minimal ACTUAL coverage of a hub.
func coveredCluster(hubSymbol string) capacity.ClusterState {
	return capacity.ClusterState{
		HubSymbol: hubSymbol,
		Workers:   []capacity.WorkerState{{ShipSymbol: "COVERED-HULL-1", Waypoint: hubSymbol}},
	}
}

// Behavior 7: the absorption ceiling gates only NEW coverage (spec north-star:
// it "stops ADDING capacity"). A hub the contract machine already covers — a
// live cluster with ≥1 hull in Signals.Topology — is keep-gated at the
// universal floor only, so a fleet-wide average inflated by arb/mining hulls
// never erases a producing hub from the desired topology (erasing it would
// stop gap-healing and, once DIFF arms, present its capacity as surplus).
// Shrink survives: a covered hub below the universal floor still drops out.
// Design decision recorded in CONTRACTS.md; the differ lane builds to it.
func TestHeuristicPlanner_AbsorptionCeilingGatesOnlyNewCoverage(t *testing.T) {
	cases := []struct {
		name             string
		signals          func() capacity.Signals
		clusters         []capacity.ClusterState
		addThreshold     float64
		fleetPerHullCrHr float64
		wantHubs         []string
	}{
		{
			name:             "a fleet averaging above a COVERED hub's marginal hull keeps the hub — the ceiling gates adds, not keeps",
			signals:          contractMachineSignals,
			clusters:         []capacity.ClusterState{coveredCluster("X1-J58-A1")},
			addThreshold:     0,
			fleetPerHullCrHr: 7000,
			wantHubs:         []string{"X1-J58-A1"}, // kept; uncovered QQ7 (≈167) is still not added
		},
		{
			name:             "an empty cluster is not coverage: the ceiling still gates a hub with no live hulls",
			signals:          contractMachineSignals,
			clusters:         []capacity.ClusterState{{HubSymbol: "X1-J58-A1"}},
			addThreshold:     0,
			fleetPerHullCrHr: 7000,
			wantHubs:         nil,
		},
		{
			name:             "a covered hub whose marginal falls below the universal floor is still dropped — shrink stays intact",
			signals:          contractMachineSignals,
			clusters:         []capacity.ClusterState{coveredCluster("X1-QQ7-B2")},
			addThreshold:     1500,
			fleetPerHullCrHr: 2000,
			wantHubs:         []string{"X1-J58-A1"}, // QQ7's ≈167 < the 1500 floor: covered or not, it goes
		},
		{
			name:             "a covered hub ranked behind a refused add is still kept — the add-stop never erases existing coverage",
			signals:          addWalkRankedSignals,
			clusters:         []capacity.ClusterState{coveredCluster("X1-LEAN-R3")},
			addThreshold:     5000,
			fleetPerHullCrHr: 0,
			wantHubs:         []string{"X1-TOP-R1", "X1-LEAN-R3"}, // FAT's add failure stops adds, not keeps
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			signals := testCase.signals()
			signals.Topology = capacity.TopologySignals{Clusters: testCase.clusters}
			signals.Economics.FleetPerHullCrHr = testCase.fleetPerHullCrHr

			desired := computeDesired(t, signals, plannerCalibration(testCase.addThreshold, 0))

			require.Equal(t, testCase.wantHubs, hubSymbols(desired))
		})
	}
}

// Behavior 8: pathological telemetry cannot manifest as an unbounded desired
// topology — worker and stocker counts carry absolute per-hub sanity
// ceilings. The sizing models trust the measured cycle and good-frequency;
// a wedged hub measuring a 200h cycle at 2.0/hr would otherwise "want" 400
// workers and feed DIFF/GOVERN unbounded churn and proposal spam.
func TestHeuristicPlanner_ClampsCountsUnderPathologicalTelemetry(t *testing.T) {
	t.Run("a wedged 200h measured cycle clamps workers to the sanity ceiling", func(t *testing.T) {
		// Distance 0 ⇒ the good cannot be costed ⇒ nothing buffered: workers only.
		signals := singleGoodSignals(2.0, 50000, 720000, 1.0, 30, 0)

		desired := computeDesired(t, signals, plannerCalibration(0, 0))

		require.Len(t, desired.Hubs, 1)
		require.Equal(t, 12, desired.Hubs[0].WorkerCount,
			"raw work conservation wants ceil(2.0/hr × 200h) = 400 workers; the ceiling caps the plan")
	})

	t.Run("spurious good-frequency clamps stockers to the sanity ceiling", func(t *testing.T) {
		// 40/hr × 60 units = 2400 units/hr consumption over 700 distance ⇒ raw
		// cadence sizing wants ceil(2400 ÷ ≈189.5) = 13 stockers.
		signals := singleGoodSignals(2.0, 50000, 3600, 40, 60, 700)

		desired := computeDesired(t, signals, plannerCalibration(0, 0))

		require.Len(t, desired.Hubs, 1)
		require.Equal(t, 6, desired.Hubs[0].StockerCount,
			"restock-cadence sizing on spurious frequency must hit the ceiling, not want 13 hulls")
	})
}

// Behavior 2: buffer goods are selected per hub by VALUE DENSITY = frequency ×
// (0.030·source_distance + 0.147·avg_units) ÷ avg_units, highest first, filling
// the buffered-volume budget in avg_units. There is NO value
// floor: the FAR-sourced good the pre-fix divide-by-distance score wrongly
// deleted (AMMONIA_ICE: 59u × 751 distance) now RANKS IN. Goods with no known
// source distance still cannot be costed and are dropped; a good too big for the
// remaining budget is skipped while smaller lower-ranked goods still make it in.
//
// Value densities for the X1-J58-A1 mix (see contractMachineSignals):
//
//	IRON  → 1.2×(0.030·60 + 0.147·30)/30 = 1.2×6.21/30    ≈ 0.2484
//	FUEL  → 0.8×(0.030·40 + 0.147·20)/20 = 0.8×4.14/20    ≈ 0.1656
//	AMMONIA_ICE → 0.3×(0.030·751 + 0.147·59)/59 = 0.3×31.203/59 ≈ 0.1587
//
// so the rank order is IRON ≻ FUEL ≻ AMMONIA_ICE (near goods lead, but the far
// bulky good is IN, not floored out).
func TestHeuristicPlanner_SelectsBufferGoodsByValueDensity(t *testing.T) {
	cases := []struct {
		name         string
		mutate       func(signals *capacity.Signals)
		budgetUnits  int
		wantBuffered []capacity.DesiredBufferedGood
	}{
		{
			name:        "ranks near goods first but the far bulky good is now IN (no floor), whole mix fits the default budget",
			mutate:      func(*capacity.Signals) {},
			budgetUnits: 0, // planner's documented default budget (240 avg-units); 30+20+59=109 all fit
			wantBuffered: []capacity.DesiredBufferedGood{
				{Good: "IRON", UnitsCap: 45},        // vd 0.2484 — highest
				{Good: "FUEL", UnitsCap: 30},        // vd 0.1656
				{Good: "AMMONIA_ICE", UnitsCap: 89}, // vd 0.1587 — FAR good recovered, ceil(59×1.5)
			},
		},
		{
			name: "a good with no known source distance cannot be costed and is not buffered",
			mutate: func(signals *capacity.Signals) {
				signals.Economics.SourceDistances = []capacity.GoodSourceDistance{
					{HubSymbol: "X1-J58-A1", Good: "FUEL", Distance: 40},
					{HubSymbol: "X1-J58-A1", Good: "AMMONIA_ICE", Distance: 751},
					{HubSymbol: "X1-QQ7-B2", Good: "COPPER", Distance: 120},
				}
			},
			budgetUnits: 0,
			wantBuffered: []capacity.DesiredBufferedGood{ // IRON dropped (uncostable); FUEL ≻ AMMONIA_ICE, both fit
				{Good: "FUEL", UnitsCap: 30},
				{Good: "AMMONIA_ICE", UnitsCap: 89},
			},
		},
		{
			name: "a tight budget skips the goods that do not fit but still admits a smaller lower-ranked one",
			mutate: func(signals *capacity.Signals) {
				signals.Demand.Hubs[0].GoodMix = append(signals.Demand.Hubs[0].GoodMix,
					capacity.GoodDemand{Good: "COPPER", Frequency: 0.5, AvgUnits: 18}) // vd 0.5×(2.7+2.646)/18≈0.1485, cap 27
				signals.Economics.SourceDistances = append(signals.Economics.SourceDistances,
					capacity.GoodSourceDistance{HubSymbol: "X1-J58-A1", Good: "COPPER", Distance: 90})
			},
			// Budget 48 avg-units. Rank IRON(30) ≻ FUEL(20) ≻ AMMONIA_ICE(59) ≻ COPPER(18):
			// IRON fits (rem 18); FUEL(20) and AMMONIA_ICE(59) exceed the 18 left and are
			// skipped; COPPER(18) still fits — the knapsack skips past the too-big goods.
			budgetUnits: 48,
			wantBuffered: []capacity.DesiredBufferedGood{
				{Good: "IRON", UnitsCap: 45},
				{Good: "COPPER", UnitsCap: 27},
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			signals := contractMachineSignals()
			testCase.mutate(&signals)

			desired := computeDesired(t, signals, plannerCalibration(1500, testCase.budgetUnits))

			require.Equal(t, []string{"X1-J58-A1"}, hubSymbols(desired))
			require.Equal(t, testCase.wantBuffered, desired.Hubs[0].BufferedGoods,
				"value density ranks the mix and only the volume budget bounds it — no value floor deletes the far good")
		})
	}
}

// TestHeuristicPlanner_BufferSelectionRoutesThroughTheSharedGate proves the reconciler planner
// applies the SAME candidate gate (domain/buffer.Gate) the LIVE depot selector uses: a good
// the hub produces LOCALLY (gate 2) and a good sourced too NEAR (gate 3) are excluded from the
// buffer whitelist BEFORE ranking, while a remote non-local hub contract good still buffers.
func TestHeuristicPlanner_BufferSelectionRoutesThroughTheSharedGate(t *testing.T) {
	signals := contractMachineSignals()
	hub := &signals.Demand.Hubs[0] // the covered hub X1-J58-A1
	hub.GoodMix = append(hub.GoodMix,
		capacity.GoodDemand{Good: "FAR_GOOD", Frequency: 1.0, AvgUnits: 30},   // remote, not local -> buffered
		capacity.GoodDemand{Good: "LOCAL_GOOD", Frequency: 1.0, AvgUnits: 30}, // remote, but produced locally -> gate 2
		capacity.GoodDemand{Good: "NEAR_GOOD", Frequency: 1.0, AvgUnits: 30},  // near-sourced -> gate 3
	)
	signals.Economics.SourceDistances = append(signals.Economics.SourceDistances,
		capacity.GoodSourceDistance{HubSymbol: "X1-J58-A1", Good: "FAR_GOOD", Distance: 100},
		capacity.GoodSourceDistance{HubSymbol: "X1-J58-A1", Good: "LOCAL_GOOD", Distance: 100}, // far, yet gate 2 still excludes it
		capacity.GoodSourceDistance{HubSymbol: "X1-J58-A1", Good: "NEAR_GOOD", Distance: 10},
	)
	signals.Economics.LocalProduction = []capacity.GoodLocalProduction{
		{HubSymbol: "X1-J58-A1", Good: "LOCAL_GOOD"},
	}

	desired := computeDesired(t, signals, plannerCalibration(1500, 0))
	require.Equal(t, []string{"X1-J58-A1"}, hubSymbols(desired))

	buffered := bufferedGoodNames(desired.Hubs[0].BufferedGoods)
	require.Contains(t, buffered, "FAR_GOOD", "a remote, non-local hub contract good still buffers")
	require.NotContains(t, buffered, "LOCAL_GOOD", "gate 2: a good the hub produces locally is never buffered")
	require.NotContains(t, buffered, "NEAR_GOOD", "gate 3: a near-sourced good is never buffered")
}

func bufferedGoodNames(goods []capacity.DesiredBufferedGood) []string {
	names := make([]string, 0, len(goods))
	for _, good := range goods {
		names = append(names, good.Good)
	}
	return names
}

// Behavior 3: every buffered good's cap ≈ avg_units + margin (policy: 50%
// margin, minimum 1) — an uncapped whitelist over-fills the first good and
// starves the rest.
func TestHeuristicPlanner_CapsBufferedGoodsAtAvgUnitsPlusMargin(t *testing.T) {
	cases := []struct {
		averageUnits float64
		wantCap      int
	}{
		{averageUnits: 20, wantCap: 30},
		{averageUnits: 30, wantCap: 45},
		{averageUnits: 40, wantCap: 60},
		{averageUnits: 59, wantCap: 89}, // ceil(88.5)
		{averageUnits: 1, wantCap: 2},
		{averageUnits: 0.5, wantCap: 1}, // margin never rounds a real good to zero
	}
	for _, testCase := range cases {
		t.Run("", func(t *testing.T) {
			signals := singleGoodSignals(1.0, 10000, 3600, 1.0, testCase.averageUnits, 50)

			desired := computeDesired(t, signals, plannerCalibration(0, 0))

			require.Len(t, desired.Hubs, 1)
			require.Equal(t, []capacity.DesiredBufferedGood{{Good: "ORE", UnitsCap: testCase.wantCap}},
				desired.Hubs[0].BufferedGoods)
		})
	}
}

// Behavior 4: counts are sized to buffered volume + restock cadence.
// Workers by work conservation (contracts/hr × cycle-hours, min 1);
// warehouses by total buffered volume; stockers so restock_throughput ≥
// consumption_rate (consumption × distance grows the count). No buffered
// goods ⇒ no warehouse and no stocker — coverage alone is the co-located worker.
func TestHeuristicPlanner_SizesCountsToBufferedVolumeAndRestockCadence(t *testing.T) {
	t.Run("workers by work conservation on the observed cycle", func(t *testing.T) {
		desired := computeDesired(t, contractMachineSignals(), plannerCalibration(1500, 0))

		require.Len(t, desired.Hubs, 1)
		hub := desired.Hubs[0]
		require.Equal(t, 3, hub.WorkerCount, "2.0 contracts/hr × 1.5h cycle = 3 concurrent deliveries")
		require.Equal(t, 2, hub.WarehouseCount, "IRON+FUEL+AMMONIA_ICE = 45+30+89 = 164 cap-units need two 120-holds (sp-lk9x recovers the far good)")
		require.Equal(t, 1, hub.StockerCount, "≈69.7 units/hr over ≈231 mean distance is still one stocker's cadence")
	})

	t.Run("buffered volume above one hold adds a warehouse", func(t *testing.T) {
		signals := capacity.Signals{
			Demand: capacity.DemandSignals{Hubs: []capacity.HubDemand{{
				HubSymbol:         "X1-VOL-H1",
				ContractFrequency: 1.5,
				AvgPaymentCredits: 20000,
				GoodMix: []capacity.GoodDemand{
					{Good: "ALUMINUM", Frequency: 1.0, AvgUnits: 40}, // cap 60
					{Good: "BARRELS", Frequency: 0.9, AvgUnits: 50},  // cap 75
					{Good: "CABLES", Frequency: 0.8, AvgUnits: 30},   // cap 45
				},
			}}},
			Performance: capacity.PerformanceSignals{Hubs: []capacity.HubPerformance{
				{HubSymbol: "X1-VOL-H1", CycleTimeSeconds: 3600},
			}},
			Economics: capacity.EconomicsSignals{SourceDistances: []capacity.GoodSourceDistance{
				{HubSymbol: "X1-VOL-H1", Good: "ALUMINUM", Distance: 50},
				{HubSymbol: "X1-VOL-H1", Good: "BARRELS", Distance: 60},
				{HubSymbol: "X1-VOL-H1", Good: "CABLES", Distance: 70},
			}},
		}

		desired := computeDesired(t, signals, plannerCalibration(0, 0))

		require.Len(t, desired.Hubs, 1)
		require.Equal(t, 2, desired.Hubs[0].WarehouseCount, "180 buffered units need two 120-unit holds")
	})

	t.Run("consumption over a long source distance adds a stocker", func(t *testing.T) {
		signals := singleGoodSignals(8, 6000, 2700, 8, 50, 400)

		desired := computeDesired(t, signals, plannerCalibration(0, 0))

		require.Len(t, desired.Hubs, 1)
		hub := desired.Hubs[0]
		require.Equal(t, 6, hub.WorkerCount, "8 contracts/hr × 0.75h cycle = 6 concurrent deliveries")
		require.Equal(t, 2, hub.StockerCount,
			"400 units/hr restocked over 400 distance exceeds one stocker's round-trip cadence")
	})

	t.Run("a hub with nothing buffered gets a worker but no warehouse or stocker", func(t *testing.T) {
		signals := capacity.Signals{
			Demand: capacity.DemandSignals{Hubs: []capacity.HubDemand{{
				HubSymbol:         "X1-BARE-H1",
				ContractFrequency: 2.0,
				AvgPaymentCredits: 5000,
				GoodMix:           []capacity.GoodDemand{{Good: "IRON", Frequency: 1.0, AvgUnits: 30}},
			}}},
			// No performance history and no source distances: cycle unknown ⇒ one
			// worker; goods uncostable ⇒ nothing buffered.
		}

		desired := computeDesired(t, signals, plannerCalibration(0, 0))

		require.Len(t, desired.Hubs, 1)
		hub := desired.Hubs[0]
		require.Empty(t, hub.BufferedGoods)
		require.Equal(t, 1, hub.WorkerCount)
		require.Zero(t, hub.WarehouseCount)
		require.Zero(t, hub.StockerCount)
	})
}

// Behavior 5: empty or insufficient signals produce a conservative plan —
// possibly empty (IsEmpty ⇒ zero actions downstream) — and never a panic.
func TestHeuristicPlanner_SparseSignalsYieldConservativePlanWithoutPanic(t *testing.T) {
	cases := []struct {
		name    string
		signals capacity.Signals
	}{
		{name: "zero-valued signals", signals: capacity.Signals{}},
		{name: "demand family present but empty", signals: capacity.Signals{Demand: capacity.DemandSignals{Hubs: []capacity.HubDemand{}}}},
		{
			name: "hubs with no observed paying demand",
			signals: capacity.Signals{Demand: capacity.DemandSignals{Hubs: []capacity.HubDemand{
				{HubSymbol: "X1-DEAD-H1", ContractFrequency: 0, AvgPaymentCredits: 9000},
				{HubSymbol: "X1-FREE-H2", ContractFrequency: 1.0, AvgPaymentCredits: 0},
			}}},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			desired := computeDesired(t, testCase.signals, plannerCalibration(0, 0))

			require.True(t, desired.IsEmpty(), "insufficient signals must want nothing, not guess")
		})
	}
}

// Behavior 6: the planner is deterministic — the same measurement produces the
// byte-identical DesiredTopology on every run, independent of the ORDER the
// SENSE lane happened to list hubs, goods, or distances in.
func TestHeuristicPlanner_DeterministicAcrossRunsAndInputOrder(t *testing.T) {
	calibration := plannerCalibration(0, 0)
	reference := computeDesired(t, contractMachineSignals(), calibration)
	require.False(t, reference.IsEmpty(), "the fixture must produce a real plan for determinism to mean anything")

	for run := 0; run < 3; run++ {
		require.Equal(t, reference, computeDesired(t, contractMachineSignals(), calibration))
	}

	shuffled := contractMachineSignals()
	reverseHubs(shuffled.Demand.Hubs)
	reversePerformance(shuffled.Performance.Hubs)
	reverseDistances(shuffled.Economics.SourceDistances)
	for _, hub := range shuffled.Demand.Hubs {
		reverseGoods(hub.GoodMix)
	}

	require.Equal(t, reference, computeDesired(t, shuffled, calibration),
		"the same measurement listed in a different order must plan the identical topology")
}

func reverseHubs(hubs []capacity.HubDemand) {
	for left, right := 0, len(hubs)-1; left < right; left, right = left+1, right-1 {
		hubs[left], hubs[right] = hubs[right], hubs[left]
	}
}

func reversePerformance(hubs []capacity.HubPerformance) {
	for left, right := 0, len(hubs)-1; left < right; left, right = left+1, right-1 {
		hubs[left], hubs[right] = hubs[right], hubs[left]
	}
}

func reverseDistances(distances []capacity.GoodSourceDistance) {
	for left, right := 0, len(distances)-1; left < right; left, right = left+1, right-1 {
		distances[left], distances[right] = distances[right], distances[left]
	}
}

func reverseGoods(goods []capacity.GoodDemand) {
	for left, right := 0, len(goods)-1; left < right; left, right = left+1, right-1 {
		goods[left], goods[right] = goods[right], goods[left]
	}
}
