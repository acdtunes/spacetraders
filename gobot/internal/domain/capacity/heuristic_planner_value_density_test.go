package capacity_test

// Falsifiability proofs for the sp-lk9x buffer value-density score. These drive
// the frozen Planner port (ComputeDesired) and assert the DesiredTopology buffer
// — the same observable the behavior tests use — but each isolates ONE property
// the fix turns on:
//
//   - DIRECTION: the corrected score MULTIPLIES by source distance, so a far
//     source out-ranks a near one of equal frequency (the pre-fix score DIVIDED
//     by distance and inverted this). A tight one-slot budget makes the far good
//     WIN the slot the near good used to steal.
//   - LOAD-BEARING: at the real far-sourced home hub's DILUTED frequencies the
//     corrected score still selects the far goods (non-empty buffer), while the
//     pre-fix divide-by-distance score drove every one below the 2e-5 floor
//     (empty buffer, 0 warehouses — the arming hazard this bead removes).
//
// These are the sign-flip + mutation guards the bead calls out; reverting the
// production score to divide-by-distance makes both fail (the direction test
// flips the winner, the mutation test empties the buffer).

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
)

// twoGoodHub builds a single uncovered hub with exactly two contract goods of
// EQUAL frequency and avg units, differing only in source distance — the clean
// controlled comparison for the sign of the distance term. Payment is high so
// the hub clears the coverage gate and its buffer is observable.
func twoGoodHub(nearDistance, farDistance float64) capacity.Signals {
	return capacity.Signals{
		Demand: capacity.DemandSignals{Hubs: []capacity.HubDemand{{
			HubSymbol:         "X1-DIR-H1",
			ContractFrequency: 2.0,
			AvgPaymentCredits: 15000,
			GoodMix: []capacity.GoodDemand{
				{Good: "NEAR_GOOD", Frequency: 1.0, AvgUnits: 30},
				{Good: "FAR_GOOD", Frequency: 1.0, AvgUnits: 30},
			},
		}}},
		Performance: capacity.PerformanceSignals{Hubs: []capacity.HubPerformance{
			{HubSymbol: "X1-DIR-H1", CycleTimeSeconds: 3600},
		}},
		Economics: capacity.EconomicsSignals{SourceDistances: []capacity.GoodSourceDistance{
			{HubSymbol: "X1-DIR-H1", Good: "NEAR_GOOD", Distance: nearDistance},
			{HubSymbol: "X1-DIR-H1", Good: "FAR_GOOD", Distance: farDistance},
		}},
	}
}

// DIRECTION PROOF (the sign flip). With frequency and volume held equal, the
// only difference is source distance. The corrected value density
// (freq × (0.030·dist + 0.147·avg) ÷ avg) RISES with distance, so FAR_GOOD
// (0.847) out-ranks NEAR_GOOD (0.207); the pre-fix freq ÷ (avg·dist) FELL with
// distance and ranked NEAR first. A one-slot budget makes the ranking decisive:
// the far good takes the slot the near good used to win.
func TestHeuristicPlanner_FarSourceOutranksNearSourceOfEqualFrequency(t *testing.T) {
	t.Run("full budget lists the far good FIRST", func(t *testing.T) {
		desired := computeDesired(t, twoGoodHub(60, 700), plannerCalibration(100, 0))

		require.Equal(t, []string{"X1-DIR-H1"}, hubSymbols(desired))
		require.Equal(t, []string{"FAR_GOOD", "NEAR_GOOD"}, bufferedGoodNames(desired.Hubs[0].BufferedGoods),
			"value density rises with distance: the far good must rank ahead of the near one of equal frequency")
	})

	t.Run("a one-slot budget is won by the far good, not the near one", func(t *testing.T) {
		// Budget 30 avg-units holds exactly one good. The higher-ranked FAR_GOOD
		// takes it; under the pre-fix divide-by-distance score NEAR_GOOD would.
		desired := computeDesired(t, twoGoodHub(60, 700), plannerCalibration(100, 30))

		require.Equal(t, []string{"X1-DIR-H1"}, hubSymbols(desired))
		require.Equal(t, []capacity.DesiredBufferedGood{{Good: "FAR_GOOD", UnitsCap: 45}},
			desired.Hubs[0].BufferedGoods,
			"the single slot goes to the FAR good — the distance term rewards, not penalizes, distance")
	})
}

// farSourcedHomeHubDilutedSignals reproduces the real X1-J58-A1 shape the bead
// measured: 8 far-sourced contract goods (source 617–751) whose 57 contracts are
// DILUTED to ~0.05/hr each by a long history. High payment keeps the hub covered;
// the point is the BUFFER, not the coverage.
func farSourcedHomeHubDilutedSignals() (capacity.Signals, []farGood) {
	goods := []farGood{
		{good: "MEDICINE", frequency: 0.06, avgUnits: 28, distance: 700},
		{good: "EQUIPMENT", frequency: 0.055, avgUnits: 26, distance: 680},
		{good: "POLYNUCLEOTIDES", frequency: 0.05, avgUnits: 26, distance: 720},
		{good: "ASSAULT_RIFLES", frequency: 0.045, avgUnits: 25, distance: 640},
		{good: "FIREARMS", frequency: 0.05, avgUnits: 24, distance: 660},
		{good: "EXPLOSIVES", frequency: 0.04, avgUnits: 24, distance: 751},
		{good: "CLOTHING", frequency: 0.06, avgUnits: 22, distance: 617},
		{good: "AMMONIA_ICE", frequency: 0.05, avgUnits: 20, distance: 751},
	}
	goodMix := make([]capacity.GoodDemand, 0, len(goods))
	distances := make([]capacity.GoodSourceDistance, 0, len(goods))
	for _, g := range goods {
		goodMix = append(goodMix, capacity.GoodDemand{Good: g.good, Frequency: g.frequency, AvgUnits: g.avgUnits})
		distances = append(distances, capacity.GoodSourceDistance{HubSymbol: "X1-J58-A1", Good: g.good, Distance: g.distance})
	}
	signals := capacity.Signals{
		Demand: capacity.DemandSignals{Hubs: []capacity.HubDemand{{
			HubSymbol:         "X1-J58-A1",
			ContractFrequency: 0.06, // 57 contracts diluted over a long history
			AvgPaymentCredits: 500000,
			GoodMix:           goodMix,
		}}},
		Economics: capacity.EconomicsSignals{SourceDistances: distances},
	}
	return signals, goods
}

type farGood struct {
	good      string
	frequency float64
	avgUnits  float64
	distance  float64
}

// preFixNeverBufferFloor is the value of the removed minBufferSelectionScore —
// the 2e-5 floor the pre-fix divide-by-distance score dropped goods below. Kept
// here as a historical constant so the mutation guard can prove the recovered
// goods are EXACTLY the ones the old score deleted.
const preFixNeverBufferFloor = 0.00002

// MUTATION GUARD (load-bearing). At the home hub's diluted frequencies the
// corrected score recovers a NON-EMPTY buffer dominated by the far-sourced
// goods — MEDICINE included — while every one of those goods sits below the
// pre-fix 2e-5 floor under the old freq ÷ (avg·dist) score. Reverting the
// production score to divide-by-distance (with its floor) empties this buffer
// and fails the non-empty assertion: the fix is load-bearing, not cosmetic.
func TestHeuristicPlanner_CorrectedScoreRecoversDilutedFarSourcedHub_MutationGuard(t *testing.T) {
	signals, goods := farSourcedHomeHubDilutedSignals()

	desired := computeDesired(t, signals, plannerCalibration(0, 0))

	require.Equal(t, []string{"X1-J58-A1"}, hubSymbols(desired))
	buffered := bufferedGoodNames(desired.Hubs[0].BufferedGoods)
	require.NotEmpty(t, buffered, "the corrected score must recover the home hub's buffer, not leave it empty")

	for _, g := range goods {
		require.Contains(t, buffered, g.good,
			"the far-sourced good must be selected under the corrected value-density score")
		oldScore := g.frequency / (g.avgUnits * g.distance)
		require.Less(t, oldScore, preFixNeverBufferFloor,
			"pre-fix proof: %s scored %.2e < the old 2e-5 floor — divide-by-distance deleted it (empty buffer)", g.good, oldScore)
	}
	require.Contains(t, buffered, "MEDICINE", "the analyst's headline recovery good must be buffered")
}
