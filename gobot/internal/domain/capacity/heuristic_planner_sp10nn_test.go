package capacity_test

// sp-10nn — the contract-delivery ADD gate must be blind to trade revenue.
//
// The bug: newCoverageWalk gated a NEW contract depot at
// max(universal_floor, FleetPerHullCrHr). FleetPerHullCrHr is the fleet-wide
// income-per-hull average, which sums ALL transactions including arbitrage
// TRADING_REVENUE. Trade is CONCAVE (per-sell value crushed as volume rises),
// so the average is unachievable at the margin — a trade spike inflates it
// past any contract depot's marginal, the coverage walk stops, and 0 depot
// proposals are filed. The fix (ContractAddGateTradeBlind, DEFAULT-OFF) gates
// the depot on the CONTRACT op's own economics: the universal per-hull floor
// plus the depot's cycle-time-compression uplift, never the fleet average.
//
// These tests drive the frozen Planner port (ComputeDesired) and, for the
// end-to-end "files VerbAddCluster" acceptance, the LadderDiffer that turns an
// uncovered desired depot into the capital action. Fixtures + helpers
// (contractMachineSignals, plannerCalibration, computeDesired, hubSymbols) are
// shared with heuristic_planner_test.go.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
)

// armedTradeBlind is plannerCalibration with the sp-10nn ADD-gate knob ON.
func armedTradeBlind(addThresholdPerHullCrHr float64, stockerBudgetUnits int) capacity.Calibration {
	calibration := plannerCalibration(addThresholdPerHullCrHr, stockerBudgetUnits)
	calibration.ContractAddGateTradeBlind = true
	return calibration
}

// filesAddClusterFor reports whether the ladder differ, run over the desired
// topology against an EMPTY actual topology, emits a VerbAddCluster (a depot
// proposal) for the hub — the observable "files a contract-depot proposal" the
// sp-10nn acceptance is written against.
func filesAddClusterFor(t *testing.T, desired capacity.DesiredTopology, hubSymbol string) bool {
	t.Helper()
	actions, err := capacity.NewLadderDiffer().Diff(context.Background(), desired, capacity.TopologySignals{}, capacity.DefaultCalibration())
	require.NoError(t, err)
	for _, action := range actions {
		if action.Verb == capacity.VerbAddCluster && action.HubSymbol == hubSymbol {
			return true
		}
	}
	return false
}

// Acceptance #1 (the RED case): ≥2 contract haulers + a recurring destination
// hub with pre-stockable recurring goods, AND HIGH fleet-wide trade revenue
// (an inflated FleetPerHullCrHr) → the reconciler FILES a contract-depot
// proposal (VerbAddCluster) for the top value×cycle-saved hub. Pre-fix (or
// flag-off) the trade-inflated average suppresses the depot and 0 proposals
// are filed.
func TestReconciler_FilesDepotProposal_WhenContractROIPositive_DespiteTradeRevenue(t *testing.T) {
	signals := contractMachineSignals()
	// A live arbitrage run inflates the fleet-wide per-hull average far past the
	// top contract hub's 5000 cr/hr marginal (J58: 2.0/hr × 15000 ÷ 6 hulls).
	signals.Economics.FleetPerHullCrHr = 42000

	desired := computeDesired(t, signals, armedTradeBlind(0, 0))

	require.Equal(t, []string{"X1-J58-A1"}, hubSymbols(desired),
		"the contract op's own ROI stands the depot up; the inflated trade average must not suppress it")
	hub := desired.Hubs[0]
	require.Positive(t, hub.WarehouseCount, "a depot has buffer warehouses")
	require.Positive(t, hub.StockerCount, "a depot has stockers to keep the buffer filled")
	require.True(t, filesAddClusterFor(t, desired, "X1-J58-A1"),
		"an uncovered desired depot must surface to DIFF as a VerbAddCluster proposal")
}

// Acceptance #2 (the core regression): hold contract demand FIXED, vary
// TRADING_REVENUE (FleetPerHullCrHr) high→low. The contract-delivery desired
// topology must be INVARIANT to trade revenue — a trade spike must NOT suppress
// a positive-contract-ROI depot. The flag-OFF contrast proves the flag is what
// delivers the invariance (OFF is still trade-variant — the pre-fix behavior).
func TestContractAddGate_InvariantToTradeRevenue(t *testing.T) {
	tradeLevels := []float64{0, 2000, 7000, 42000, 500000}

	t.Run("armed: the desired topology is invariant to trade revenue", func(t *testing.T) {
		var reference []string
		for _, trade := range tradeLevels {
			signals := contractMachineSignals()
			signals.Economics.FleetPerHullCrHr = trade

			hubs := hubSymbols(computeDesired(t, signals, armedTradeBlind(0, 0)))
			require.Equal(t, []string{"X1-J58-A1"}, hubs,
				"a trade-revenue spike must never suppress the positive-contract-ROI depot")
			if reference == nil {
				reference = hubs
			}
			require.Equal(t, reference, hubs, "the contract topology must not move with trade revenue")
		}
	})

	t.Run("flag-off contrast: the gate is STILL trade-variant (byte-identical pre-fix)", func(t *testing.T) {
		low := contractMachineSignals()
		low.Economics.FleetPerHullCrHr = 0
		high := contractMachineSignals()
		high.Economics.FleetPerHullCrHr = 42000

		require.Equal(t, []string{"X1-J58-A1"}, hubSymbols(computeDesired(t, low, plannerCalibration(0, 0))),
			"with no trade inflation the pre-fix gate still admits the depot")
		require.Empty(t, hubSymbols(computeDesired(t, high, plannerCalibration(0, 0))),
			"the pre-fix gate suppresses the depot under high trade — this is exactly the bug the flag fixes")
	})
}

// Acceptance #4 (default-off / byte-identical): with the flag OFF the ADD gate
// still benchmarks against the fleet-wide average, matching the documented
// pre-fix behavior exactly — a high average suppresses the add, a zero average
// admits it. Flipping ONLY the flag changes the outcome.
func TestSp10nn_ByteIdentical_WhenFlagOff(t *testing.T) {
	cases := []struct {
		name             string
		fleetPerHullCrHr float64
		wantHubsOff      []string
	}{
		{name: "high trade average suppresses the add (pre-fix behavior)", fleetPerHullCrHr: 42000, wantHubsOff: nil},
		{name: "zero average admits the add (pre-fix behavior)", fleetPerHullCrHr: 0, wantHubsOff: []string{"X1-J58-A1"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			signals := contractMachineSignals()
			signals.Economics.FleetPerHullCrHr = testCase.fleetPerHullCrHr

			off := hubSymbols(computeDesired(t, signals, plannerCalibration(0, 0)))
			require.Equal(t, testCase.wantHubsOff, off, "flag OFF must be byte-identical to the pre-fix fleet-average gate")

			on := hubSymbols(computeDesired(t, signals, armedTradeBlind(0, 0)))
			require.Equal(t, []string{"X1-J58-A1"}, on, "flag ON gates on the contract op's own ROI, independent of the trade average")
		})
	}
}

// compressionHubSignals is a slow-cycle, far-sourced hub whose STANDALONE
// capacity-add marginal (freq×payment ÷ hulls = 0.3×6000 ÷ 3 = 600 cr/hr per
// hull) sits BELOW a 650 floor, but whose depot compresses a long source-and-
// haul leg on a 2h cycle — the value the standalone model omits. cycleSeconds
// 0 leaves the cycle unmeasured (no compression creditable) while keeping the
// same three-hull plan, isolating the compression term.
func compressionHubSignals(cycleSeconds float64) capacity.Signals {
	signals := capacity.Signals{
		Demand: capacity.DemandSignals{Hubs: []capacity.HubDemand{{
			HubSymbol:         "X1-CMP-H1",
			ContractFrequency: 0.3,
			AvgPaymentCredits: 6000,
			GoodMix:           []capacity.GoodDemand{{Good: "ORE", Frequency: 0.3, AvgUnits: 40}},
		}}},
		Economics: capacity.EconomicsSignals{
			// Saturated hauler tier + a HIGH trade average: the depot must earn its
			// place on contract compression ROI alone, never the trade average.
			ContractHaulerCount: capacity.ContractHaulerTierSaturation,
			FleetPerHullCrHr:    42000,
			SourceDistances:     []capacity.GoodSourceDistance{{HubSymbol: "X1-CMP-H1", Good: "ORE", Distance: 500}},
		},
	}
	if cycleSeconds > 0 {
		signals.Performance = capacity.PerformanceSignals{Hubs: []capacity.HubPerformance{
			{HubSymbol: "X1-CMP-H1", CycleTimeSeconds: cycleSeconds},
		}}
	}
	return signals
}

// Part 2 (credit cycle-time compression): the depot's real value is throughput
// uplift on the EXISTING contract stream, which the standalone freq×payment ÷
// hulls marginal under-values. A hub whose standalone marginal (600 cr/hr per
// hull) falls below the 650 floor must still be admitted once its compression
// ROI is credited — and must NOT be admitted when the cycle is unmeasured and
// no compression can be costed (proving compression, not part 1, tips it).
func TestContractAddGate_CreditsDepotCompressionROI(t *testing.T) {
	t.Run("a measured slow cycle credits compression and admits the sub-floor depot", func(t *testing.T) {
		desired := computeDesired(t, compressionHubSignals(7200), armedTradeBlind(650, 0))

		require.Equal(t, []string{"X1-CMP-H1"}, hubSymbols(desired),
			"standalone 600 cr/hr per hull is below the 650 floor; the +104 cr/hr compression uplift on the 2h cycle clears it")
		require.Positive(t, desired.Hubs[0].WarehouseCount, "the compressing depot buffers its far good")
	})

	t.Run("an unmeasured cycle credits no compression and the sub-floor depot is refused", func(t *testing.T) {
		desired := computeDesired(t, compressionHubSignals(0), armedTradeBlind(650, 0))

		require.Empty(t, hubSymbols(desired),
			"no cycle ⇒ no costable compression ⇒ the standalone 600 marginal stays below the 650 floor — compression, not part 1, is what admits the hub")
	})
}

// Acceptance #3 (fail-closed, RULINGS #4): the trade-blind gate is a MONEY
// guard and must fail CLOSED — a hub with unreadable contract economics (no
// frequency, or no payment) yields no add, even under a high trade average that
// the pre-fix gate would have measured against. The correction replaces a wrong
// input; it never relaxes the guard into a phantom-positive add.
func TestContractAddGate_FailsClosed_OnUnreadableContractEconomics(t *testing.T) {
	signals := capacity.Signals{
		Demand: capacity.DemandSignals{Hubs: []capacity.HubDemand{
			{HubSymbol: "X1-NOFREQ-1", ContractFrequency: 0, AvgPaymentCredits: 15000,
				GoodMix: []capacity.GoodDemand{{Good: "IRON", Frequency: 0, AvgUnits: 30}}},
			{HubSymbol: "X1-NOPAY-2", ContractFrequency: 2.0, AvgPaymentCredits: 0,
				GoodMix: []capacity.GoodDemand{{Good: "IRON", Frequency: 1.0, AvgUnits: 30}}},
		}},
		Performance: capacity.PerformanceSignals{Hubs: []capacity.HubPerformance{
			{HubSymbol: "X1-NOFREQ-1", CycleTimeSeconds: 5400},
			{HubSymbol: "X1-NOPAY-2", CycleTimeSeconds: 5400},
		}},
		Economics: capacity.EconomicsSignals{
			ContractHaulerCount: capacity.ContractHaulerTierSaturation,
			FleetPerHullCrHr:    42000,
			SourceDistances: []capacity.GoodSourceDistance{
				{HubSymbol: "X1-NOFREQ-1", Good: "IRON", Distance: 500},
				{HubSymbol: "X1-NOPAY-2", Good: "IRON", Distance: 500},
			},
		},
	}

	desired := computeDesired(t, signals, armedTradeBlind(0, 0))

	require.True(t, desired.IsEmpty(),
		"unreadable contract $/hr (no frequency, or no payment) must never manufacture a phantom-positive add")
}
