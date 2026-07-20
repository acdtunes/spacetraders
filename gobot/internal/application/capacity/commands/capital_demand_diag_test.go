package commands

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
)

// contractDeliveryCapitalGap must sum ONLY the tier-4 (capital) actions, per role
// — the same fold CapexEmitter emits — and ignore the cheap tiers.
func TestContractDeliveryCapitalGap_SumsTier4PerRole(t *testing.T) {
	actions := []capacity.Action{
		{Tier: capacity.TierReuseIdle, Verb: capacity.VerbReassignHull, HullDelta: 0, WarehouseDelta: 0},
		{Tier: capacity.TierCapital, Verb: capacity.VerbAddCluster, HullDelta: 5, WarehouseDelta: 3, StockerDelta: 1, WorkerDelta: 1},
		{Tier: capacity.TierCapital, Verb: capacity.VerbBuyHull, HullDelta: 1, StockerDelta: 1},
	}
	hulls, warehouse, stocker, delivery := contractDeliveryCapitalGap(actions)
	require.Equal(t, 6, hulls)
	require.Equal(t, 3, warehouse)
	require.Equal(t, 2, stocker)
	require.Equal(t, 1, delivery)
}

// zeroCapitalDemandReason's load-bearing branch: an empty desired topology with
// the trade-blind arm OFF must name the trade-inflated-average suppression AND the
// config knob — the one-line signal that would have surfaced the mis-nested arm.
func TestZeroCapitalDemandReason_DisarmedEmptyNamesTheArm(t *testing.T) {
	cal := capacity.DefaultCalibration()
	cal.ContractAddGateTradeBlind = false
	reason := zeroCapitalDemandReason(cal, capacity.DesiredTopology{})
	require.Contains(t, reason, "trade-blind arm is OFF")
	require.Contains(t, reason, "contract_add_gate_trade_blind")

	cal.ContractAddGateTradeBlind = true
	require.Contains(t, zeroCapitalDemandReason(cal, capacity.DesiredTopology{}), "per-hull floor")

	nonEmpty := capacity.DesiredTopology{Hubs: []capacity.DesiredHub{{HubSymbol: "X1-A1"}}}
	require.True(t, strings.Contains(zeroCapitalDemandReason(cal, nonEmpty), "cheap tiers"))
}
