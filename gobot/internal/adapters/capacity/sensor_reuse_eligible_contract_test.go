package capacity

import (
	"testing"

	"github.com/stretchr/testify/require"

	domcap "github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// tier1_reuse_idle: the SENSE-side reuse-eligible
// filter is the ONLY channel an idle hull reaches the DIFF ladder's reassign path through. It already
// requires DedicatedFleet == "", so a hull dedicated to the exclusive "contract" fleet — the
// reserve floor's stamp — is INVISIBLE to tier1: an armed reconciler can never reassign the
// contract reserve, exactly as it can never poach it via the shared idle pool. This pins the
// guarantee directly on the pure filter so a future edit cannot
// silently drop the dedication check and re-open the poach vector.
func TestReuseEligibleIdleHulls_ExcludesContractDedicatedHull(t *testing.T) {
	hulls := []domcap.HullUtilization{
		{ShipSymbol: "CONTRACT-RESERVE", DedicatedFleet: "contract", Idle: true},
		{ShipSymbol: "FREE-1", DedicatedFleet: "", Idle: true},
		{ShipSymbol: "DEPOT-PIN", DedicatedFleet: "depot-delivery", Idle: true},
		{ShipSymbol: "BUSY-1", DedicatedFleet: "", Idle: false},
	}

	eligible := reuseEligibleIdleHulls(hulls, nil)

	symbols := make([]string, 0, len(eligible))
	for _, h := range eligible {
		symbols = append(symbols, h.ShipSymbol)
	}
	require.Equal(t, []string{"FREE-1"}, symbols,
		"only the idle UNDEDICATED hull is reuse-eligible: a contract-dedicated reserve hull (and any other fleet's hull) is excluded, so tier1_reuse_idle cannot reassign it")
}

// sp-7r7w never-poach: the exclusive PURCHASING ship (the pivoted command frigate) stands by idle
// between buys, so it MUST be invisible to the reconciler's tier-1 reuse-idle — otherwise the reconciler
// re-dedicates it to contract-delivery and the deterministic buy ship is lost (the pt7d redux the
// Admiral flagged as a hard requirement). The reuse-eligible filter already requires DedicatedFleet=="",
// so a "purchasing"-dedicated idle hull is excluded automatically; this pins that guarantee on the pure
// filter (keyed to navigation.PurchasingFleet) so a future edit cannot silently re-open the poach.
func TestReuseEligibleIdleHulls_ExcludesPurchasingDedicatedHull(t *testing.T) {
	hulls := []domcap.HullUtilization{
		{ShipSymbol: "FRIGATE-BUYER", DedicatedFleet: navigation.PurchasingFleet, Idle: true},
		{ShipSymbol: "FREE-1", DedicatedFleet: "", Idle: true},
	}

	eligible := reuseEligibleIdleHulls(hulls, nil)

	symbols := make([]string, 0, len(eligible))
	for _, h := range eligible {
		symbols = append(symbols, h.ShipSymbol)
	}
	require.Equal(t, []string{"FREE-1"}, symbols,
		"the exclusive purchasing ship (dedicated_fleet=purchasing) is invisible to tier-1 reuse-idle — the reconciler can never poach it into contract-delivery")
}

// A hull already holding a cluster role stays excluded even when idle+undedicated —
// so the contract-dedication check added value is isolated (the "contract" exclusion
// is not accidentally load-bearing for the cluster-role case).
func TestReuseEligibleIdleHulls_ExcludesClusterRoleHull(t *testing.T) {
	hulls := []domcap.HullUtilization{
		{ShipSymbol: "WAREHOUSE-1", DedicatedFleet: "", Idle: true},
		{ShipSymbol: "FREE-1", DedicatedFleet: "", Idle: true},
	}
	clusters := []domcap.ClusterState{{Warehouses: []domcap.WarehouseState{{ShipSymbol: "WAREHOUSE-1"}}}}

	eligible := reuseEligibleIdleHulls(hulls, clusters)

	require.Len(t, eligible, 1)
	require.Equal(t, "FREE-1", eligible[0].ShipSymbol, "a hull anchoring a cluster warehouse is not reuse-eligible")
}

// sp-2jrz (fix b, restart-recovery pin): the operator's remedy for a re-stranding
// reconciler is `fleet assign --fleet trade` on the lights. That dedication must be
// INVIOLABLE to the reconciler — including a reconciler rebuilt by daemon recovery,
// which comes up through the SAME buildCommandForType -> SAME handler -> SAME SENSE
// filter, so pinning the pure filter pins the recovery guarantee too: a trade-pinned
// hull is never reuse-eligible, so tier-1 reassign can never poach it and a restart
// cannot re-dedicate it away from trade. (The captain's live remedy was being undone;
// with the dedication guard this filter enforces, it no longer can be.)
func TestReuseEligibleIdleHulls_ExcludesTradeDedicatedHull(t *testing.T) {
	hulls := []domcap.HullUtilization{
		{ShipSymbol: "LIGHT-TRADE", DedicatedFleet: "trade", Idle: true},
		{ShipSymbol: "FREE-1", DedicatedFleet: "", Idle: true},
	}

	eligible := reuseEligibleIdleHulls(hulls, nil)

	symbols := make([]string, 0, len(eligible))
	for _, h := range eligible {
		symbols = append(symbols, h.ShipSymbol)
	}
	require.Equal(t, []string{"FREE-1"}, symbols,
		"a hull the operator pinned to trade is invisible to the reconciler's tier-1 reassign — recovered or not — so a restart can never re-dedicate it away from trade")
}
