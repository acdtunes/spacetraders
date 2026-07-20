package capacity

// sp-3idiw — the gate-shortfall value model. gateHubDemand shapes a live construction
// shortfall into a single gate HubDemand: the hub RANK scales with total remaining
// (so it ranks high during the fill and collapses as remaining → 0), each material is
// buffered as a bounded working-set lot, and the hub is tagged HubKindGate so the
// planner's contract hauler-first stage does not suppress it.

import (
	"testing"

	"github.com/stretchr/testify/require"

	domainCapacity "github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
)

func TestGateHubDemand_ShapesRemainingMaterialsIntoGateHub(t *testing.T) {
	shortfall := GateShortfall{
		GateWaypoint: "X1-UM5-I56",
		Materials: []GateMaterialShortfall{
			{TradeSymbol: "FAB_MATS", Remaining: 1600},
			{TradeSymbol: "ADVANCED_CIRCUITRY", Remaining: 400},
		},
	}

	hubs := gateHubDemand(shortfall)

	require.Len(t, hubs, 1, "one gate site → one gate demand hub")
	hub := hubs[0]
	require.Equal(t, "X1-UM5-I56", hub.HubSymbol)
	require.Equal(t, domainCapacity.HubKindGate, hub.Kind, "the gate hub must be tagged so the hauler-first stage spares it")
	require.Positive(t, hub.ContractFrequency, "a synthetic fill cadence so the hub scores")
	require.Positive(t, hub.AvgPaymentCredits, "value ∝ total remaining units")
	require.Len(t, hub.GoodMix, 2, "both outstanding materials are buffered goods")
	byGood := map[string]domainCapacity.GoodDemand{}
	for _, g := range hub.GoodMix {
		byGood[g.Good] = g
	}
	require.LessOrEqual(t, byGood["FAB_MATS"].AvgUnits, gateBufferWorkingSetUnits,
		"a large order is buffered as a bounded working-set lot, not the whole outstanding volume")
	require.Positive(t, byGood["ADVANCED_CIRCUITRY"].AvgUnits)
}

// Behavior (dissolution): a finished gate — no outstanding materials — yields NO gate
// hub, so the planner desires no gate depot and the existing shrink path frees the hulls.
func TestGateHubDemand_NoRemainingMaterialsYieldsNoHub(t *testing.T) {
	require.Nil(t, gateHubDemand(GateShortfall{GateWaypoint: "X1-UM5-I56"}),
		"a gate with no unfulfilled materials produces no demand — the depot dissolves")
	require.Nil(t, gateHubDemand(GateShortfall{
		GateWaypoint: "X1-UM5-I56",
		Materials:    []GateMaterialShortfall{{TradeSymbol: "FAB_MATS", Remaining: 0}},
	}), "a fully-fulfilled material contributes nothing")
	require.Nil(t, gateHubDemand(GateShortfall{Materials: []GateMaterialShortfall{{TradeSymbol: "FAB_MATS", Remaining: 10}}}),
		"no gate waypoint → no hub")
}

// Behavior: the hub value falls as the fill completes — fewer remaining units rank the
// gate lower, so the depot's priority (and eventually its existence) tracks the shortfall.
func TestGateHubDemand_ValueTracksRemainingVolume(t *testing.T) {
	full := gateHubDemand(GateShortfall{
		GateWaypoint: "X1-UM5-I56",
		Materials:    []GateMaterialShortfall{{TradeSymbol: "FAB_MATS", Remaining: 1600}},
	})
	nearlyDone := gateHubDemand(GateShortfall{
		GateWaypoint: "X1-UM5-I56",
		Materials:    []GateMaterialShortfall{{TradeSymbol: "FAB_MATS", Remaining: 100}},
	})

	require.Greater(t, full[0].AvgPaymentCredits, nearlyDone[0].AvgPaymentCredits,
		"a larger remaining shortfall ranks the gate higher — value → 0 as the fill completes")
}
