package capacity_test

// sp-3idiw — the reconciler must DESIRE a gate-construction depot (it is blind to gate
// haul demand today) and must NOT gate that desire on the CONTRACT op's hauler count.
// A gate HubDemand (Kind gate) flows through the SAME planner as contract hubs, so it
// earns a warehouse + stockers + delivery worker, sized by the shared machinery. The
// hauler-first staging early-return is scoped to CONTRACT hubs only: gate demand
// survives it (the gate fill is not gated on the contract light-hauler pool), while
// contract demand is still withheld until the pool saturates.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
)

const gateWaypoint = "X1-UM5-I56"

// gateDemandHub is one jump-gate construction depot's demand: a high remaining-volume
// fill (rank ∝ remaining via payment) buffering two far-sourced materials sized to a
// haulable working set, so it plans a warehouse + stockers + a delivery worker.
func gateDemandHub() capacity.HubDemand {
	return capacity.HubDemand{
		HubSymbol:         gateWaypoint,
		Kind:              capacity.HubKindGate,
		ContractFrequency: 1.0,
		AvgPaymentCredits: 2_000_000, // ∝ total remaining units — ranks high during the active fill
		GoodMix: []capacity.GoodDemand{
			{Good: "FAB_MATS", Frequency: 1.0, AvgUnits: 120},
			{Good: "ADVANCED_CIRCUITRY", Frequency: 1.0, AvgUnits: 120},
		},
	}
}

func gateSourceDistances() []capacity.GoodSourceDistance {
	return []capacity.GoodSourceDistance{
		{HubSymbol: gateWaypoint, Good: "FAB_MATS", Distance: 200},
		{HubSymbol: gateWaypoint, Good: "ADVANCED_CIRCUITRY", Distance: 300},
	}
}

// contractDemandHub is one ordinary contract-delivery hub used to prove the two ops
// coexist (both served) and that the hauler-first stage still withholds CONTRACT demand.
func contractDemandHub() capacity.HubDemand {
	return capacity.HubDemand{
		HubSymbol:         "X1-UM5-C1",
		ContractFrequency: 0.5, // Kind defaults to contract (zero value)
		AvgPaymentCredits: 40000,
		GoodMix:           []capacity.GoodDemand{{Good: "IRON_ORE", Frequency: 0.5, AvgUnits: 40}},
	}
}

func desiredHubsBySymbol(desired capacity.DesiredTopology) map[string]capacity.DesiredHub {
	byHub := make(map[string]capacity.DesiredHub, len(desired.Hubs))
	for _, hub := range desired.Hubs {
		byHub[hub.HubSymbol] = hub
	}
	return byHub
}

// Behavior: with an active gate fill and ZERO contract haulers, the reconciler DESIRES a
// gate depot — a warehouse at the gate, stockers, and a delivery worker. Today the
// hauler-first early-return zeroes EVERYTHING at 0 haulers, so it desires 0 (the bug).
func TestHeuristicPlanner_DesiresGateDepotEvenWithNoContractHaulers(t *testing.T) {
	signals := capacity.Signals{
		Demand: capacity.DemandSignals{Hubs: []capacity.HubDemand{gateDemandHub()}},
		Economics: capacity.EconomicsSignals{
			ContractHaulerCount: 0, // below the contract hauler tier — must NOT suppress the gate depot
			SourceDistances:     gateSourceDistances(),
		},
	}

	desired := computeDesired(t, signals, capacity.DefaultCalibration())

	gate, ok := desiredHubsBySymbol(desired)[gateWaypoint]
	require.True(t, ok, "the reconciler must desire a gate-construction depot during the fill")
	require.Positive(t, gate.WarehouseCount, "gate depot needs a warehouse to compress the supply chain")
	require.Positive(t, gate.StockerCount, "gate depot needs stockers to fill the warehouse")
	require.Positive(t, gate.WorkerCount, "gate depot needs a delivery worker")
}

// Behavior: the hauler-first stage is CONTRACT-scoped. Below the contract hauler tier,
// contract demand is still withheld (premature capital) but the gate depot is desired.
func TestHeuristicPlanner_HaulerFirstStageSuppressesContractButNotGate(t *testing.T) {
	signals := capacity.Signals{
		Demand: capacity.DemandSignals{Hubs: []capacity.HubDemand{gateDemandHub(), contractDemandHub()}},
		Economics: capacity.EconomicsSignals{
			ContractHaulerCount: 0,
			SourceDistances: append(gateSourceDistances(),
				capacity.GoodSourceDistance{HubSymbol: "X1-UM5-C1", Good: "IRON_ORE", Distance: 60}),
		},
	}

	desired := computeDesired(t, signals, capacity.DefaultCalibration())

	byHub := desiredHubsBySymbol(desired)
	require.Contains(t, byHub, gateWaypoint, "the gate depot survives the contract hauler-first stage")
	require.NotContains(t, byHub, "X1-UM5-C1", "contract demand is still withheld below the contract hauler tier")
}

// Behavior (byte-identical): no gate demand + below the hauler tier → the reconciler
// desires NOTHING, exactly as before this lane (the OFF path is unchanged).
func TestHeuristicPlanner_NoGateDemandBelowHaulerTierDesiresNothing(t *testing.T) {
	signals := capacity.Signals{
		Demand: capacity.DemandSignals{Hubs: []capacity.HubDemand{contractDemandHub()}},
		Economics: capacity.EconomicsSignals{
			ContractHaulerCount: 0,
			SourceDistances:     []capacity.GoodSourceDistance{{HubSymbol: "X1-UM5-C1", Good: "IRON_ORE", Distance: 60}},
		},
	}

	desired := computeDesired(t, signals, capacity.DefaultCalibration())

	require.Empty(t, desired.Hubs, "no gate demand below the hauler tier must desire nothing — byte-identical OFF path")
}

// Behavior (both ops): with the contract hauler pool saturated, the reconciler serves
// BOTH the gate depot AND the contract hub through the one reconciler.
func TestHeuristicPlanner_ServesGateAndContractHubsTogetherWhenSaturated(t *testing.T) {
	signals := capacity.Signals{
		Demand: capacity.DemandSignals{Hubs: []capacity.HubDemand{gateDemandHub(), contractDemandHub()}},
		Economics: capacity.EconomicsSignals{
			ContractHaulerCount: capacity.ContractHaulerTierSaturation,
			SourceDistances: append(gateSourceDistances(),
				capacity.GoodSourceDistance{HubSymbol: "X1-UM5-C1", Good: "IRON_ORE", Distance: 60}),
		},
	}

	desired := computeDesired(t, signals, capacity.DefaultCalibration())

	byHub := desiredHubsBySymbol(desired)
	require.Contains(t, byHub, gateWaypoint, "the gate depot is served alongside contract hubs")
	require.Contains(t, byHub, "X1-UM5-C1", "contract hubs are still served once the hauler pool saturates")
}
