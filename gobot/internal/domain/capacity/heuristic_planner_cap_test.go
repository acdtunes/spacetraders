package capacity_test

// sp-rt3b6 PART 2 — the planner-side hard cap on TOTAL desired contract-delivery
// hulls. The autosizer's fleet_ceiling_contract_delivery is toothless (the demand
// bridge reports Current:0 so the class guard never binds), so the ONLY real bound
// on reuse+buy is the planner: ComputeDesired must stop admitting NEW hubs once the
// cumulative desired hull total (Σ warehouse+stocker+worker across admitted hubs)
// would exceed the ceiling — a whole-hub stop (never split a hub to fit), and never
// evicting a covered/producing hub (the planner north-star).

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
)

// cappableHub is one add-eligible hub of a fixed, known hull cost. Each hub buffers
// one near good so it plans exactly 1 warehouse + 1 stocker + N workers; a high
// per-hull marginal keeps every hub clear of the add gate, so the CAP is the only
// thing that can stop the walk.
func cappableHub(symbol string, frequency, payment, cycleSeconds float64) capacity.HubDemand {
	return capacity.HubDemand{
		HubSymbol:         symbol,
		ContractFrequency: frequency,
		AvgPaymentCredits: payment,
		GoodMix:           []capacity.GoodDemand{{Good: "ORE", Frequency: frequency, AvgUnits: 20}},
	}
}

// fourEqualRichHubs are four interchangeable high-value hubs. Each plans 1 warehouse
// + 1 stocker + 1 worker = 3 hulls (0.5/hr × 1h cycle ⇒ 1 worker; 20-unit ORE buffer
// ⇒ 1 warehouse + 1 stocker) and clears any sane add gate (marginal 0.5×40000/3 ≈
// 6667 cr/hr), so admission is bounded ONLY by the ceiling. Total desired if all four
// admit: 12 hulls.
func fourEqualRichHubs() capacity.Signals {
	symbols := []string{"X1-CAP-A", "X1-CAP-B", "X1-CAP-C", "X1-CAP-D"}
	var demand []capacity.HubDemand
	var perf []capacity.HubPerformance
	var dist []capacity.GoodSourceDistance
	for _, s := range symbols {
		demand = append(demand, cappableHub(s, 0.5, 40000, 3600))
		perf = append(perf, capacity.HubPerformance{HubSymbol: s, CycleTimeSeconds: 3600})
		dist = append(dist, capacity.GoodSourceDistance{HubSymbol: s, Good: "ORE", Distance: 60})
	}
	return capacity.Signals{
		Demand:      capacity.DemandSignals{Hubs: demand},
		Performance: capacity.PerformanceSignals{Hubs: perf},
		Economics: capacity.EconomicsSignals{
			ContractHaulerCount: capacity.ContractHaulerTierSaturation,
			SourceDistances:     dist,
		},
	}
}

func totalDesiredHulls(desired capacity.DesiredTopology) int {
	total := 0
	for _, hub := range desired.Hubs {
		total += hub.WarehouseCount + hub.StockerCount + hub.WorkerCount
	}
	return total
}

// Behavior: with the ceiling set, the planner admits ranked hubs UNTIL the cumulative
// desired hull total would exceed it, then STOPS — the total desired contract-delivery
// hulls never exceed the ceiling. Four 3-hull hubs (12 total) under a ceiling of 6
// admit exactly two whole hubs (6 hulls); the third would push the total to 9 > 6, so
// the walk stops. Prefer whole hubs — never split one to fit.
func TestHeuristicPlanner_CapsTotalDesiredContractDeliveryHullsAtCeiling(t *testing.T) {
	cal := capacity.DefaultCalibration()
	cal.ContractDeliveryHullCeiling = 6

	desired := computeDesired(t, fourEqualRichHubs(), cal)

	require.Len(t, desired.Hubs, 2, "a 6-hull ceiling admits exactly two 3-hull hubs (whole-hub boundary)")
	require.LessOrEqual(t, totalDesiredHulls(desired), 6,
		"the summed desired contract-delivery hulls must never exceed the ceiling")
}

// Behavior (whole-hub boundary): a ceiling that falls BETWEEN hub boundaries clamps
// DOWN to the last whole hub — a ceiling of 7 over 3-hull hubs still admits only two
// (6 hulls); the third (→9) would blow the cap, so it is not admitted (never a partial
// 1-hull sliver to reach exactly 7).
func TestHeuristicPlanner_CapClampsToWholeHubBoundary(t *testing.T) {
	cal := capacity.DefaultCalibration()
	cal.ContractDeliveryHullCeiling = 7

	desired := computeDesired(t, fourEqualRichHubs(), cal)

	require.Len(t, desired.Hubs, 2, "7-hull ceiling over 3-hull hubs stops at 6 — no partial hub to reach 7")
	require.LessOrEqual(t, totalDesiredHulls(desired), 7)
}

// Behavior: a ceiling at or above the natural total is a no-op — every gate-clearing
// hub is admitted exactly as with no cap (12 hulls across 4 hubs).
func TestHeuristicPlanner_CapAtOrAboveNaturalTotalAdmitsEveryHub(t *testing.T) {
	cal := capacity.DefaultCalibration()
	cal.ContractDeliveryHullCeiling = 12

	desired := computeDesired(t, fourEqualRichHubs(), cal)

	require.Len(t, desired.Hubs, 4, "a ceiling >= the natural total admits every hub")
	require.Equal(t, 12, totalDesiredHulls(desired))
}

// Behavior (byte-identical): ceiling 0 = NO cap. The planner admits every gate-clearing
// hub exactly as before the cap existed — the default-off contract.
func TestHeuristicPlanner_ZeroCeilingIsNoCapByteIdentical(t *testing.T) {
	cal := capacity.DefaultCalibration()
	cal.ContractDeliveryHullCeiling = 0

	desired := computeDesired(t, fourEqualRichHubs(), cal)

	require.Len(t, desired.Hubs, 4, "ceiling 0 must not cap — byte-identical to pre-cap behavior")
	require.Equal(t, 12, totalDesiredHulls(desired))
}

// Behavior (north-star): the cap gates only NEW coverage — it must NEVER evict a
// covered/producing hub from the desired topology (that would stop the reconciler
// gap-healing the existing machine). With every hub already covered in the ACTUAL
// topology and a ceiling far below their total, all covered hubs are still kept.
func TestHeuristicPlanner_CapNeverEvictsCoveredHubs(t *testing.T) {
	signals := fourEqualRichHubs()
	// Cover all four hubs: a live cluster with >=1 hull each (the existing machine).
	for _, s := range []string{"X1-CAP-A", "X1-CAP-B", "X1-CAP-C", "X1-CAP-D"} {
		signals.Topology.Clusters = append(signals.Topology.Clusters, capacity.ClusterState{
			HubSymbol: s,
			Workers:   []capacity.WorkerState{{ShipSymbol: "COVERED-" + s, Waypoint: s}},
		})
	}
	cal := capacity.DefaultCalibration()
	cal.ContractDeliveryHullCeiling = 3 // one hub's worth — far below the covered total

	desired := computeDesired(t, signals, cal)

	require.Len(t, desired.Hubs, 4,
		"a covered producing hub must never be evicted by the cap — the cap gates only NEW coverage")
}
