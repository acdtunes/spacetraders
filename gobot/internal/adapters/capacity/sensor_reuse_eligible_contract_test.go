package capacity

import (
	"testing"

	"github.com/stretchr/testify/require"

	domcap "github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// tier1_reuse_idle: the SENSE-side reuse-eligible filter is the ONLY channel an
// idle hull reaches the DIFF ladder's reassign path through. An idle hull tagged
// "contract" is the contract op's OWN reserve hauler, so REPOSITIONING it into a
// depot role is reuse, not poaching (Admiral: reuse before buy) — it belongs in
// the reuse pool alongside undedicated hulls. The COMMAND frigate can itself carry
// the "contract" tag yet must NEVER be poached (RULINGS #7 — last-resort hauler),
// so the role guard keeps it out; every OTHER fleet's pin (depot roles, trade,
// manufacturing) stays excluded. This pins the widened-but-safe eligibility on the
// pure filter so a future edit can neither drop the contract idle nor re-open the
// frigate/other-fleet poach.
func TestReuseEligibleIdleHulls_IncludesContractHaulersExcludesFrigate(t *testing.T) {
	// All cargo-capable, so dedication/role/idle are the ONLY exclusion reasons under test.
	hulls := []domcap.HullUtilization{
		{ShipSymbol: "CONTRACT-RESERVE", DedicatedFleet: "contract", Role: "HAULER", Idle: true, CargoCapacity: 80},
		{ShipSymbol: "FREE-1", DedicatedFleet: "", Role: "HAULER", Idle: true, CargoCapacity: 80},
		{ShipSymbol: "FRIGATE", DedicatedFleet: "contract", Role: "COMMAND", Idle: true, CargoCapacity: 80},
		{ShipSymbol: "DEPOT-PIN", DedicatedFleet: "depot-delivery", Role: "HAULER", Idle: true, CargoCapacity: 80},
		{ShipSymbol: "BUSY-1", DedicatedFleet: "", Role: "HAULER", Idle: false, CargoCapacity: 80},
	}

	eligible := reuseEligibleIdleHulls(hulls, nil)

	symbols := make([]string, 0, len(eligible))
	for _, h := range eligible {
		symbols = append(symbols, h.ShipSymbol)
	}
	require.ElementsMatch(t, []string{"CONTRACT-RESERVE", "FREE-1"}, symbols,
		"an idle contract-dedicated hauler AND an idle undedicated hauler are reuse-eligible; the command frigate (contract + role COMMAND), a depot pin, and a busy hull are excluded")
}

// sp-7r7w never-poach: the exclusive PURCHASING ship (the pivoted command frigate) stands by idle
// between buys, so it MUST be invisible to the reconciler's tier-1 reuse-idle — otherwise the reconciler
// re-dedicates it to contract-delivery and the deterministic buy ship is lost (the pt7d redux the
// Admiral flagged as a hard requirement). The reuse-eligible filter already requires DedicatedFleet=="",
// so a "purchasing"-dedicated idle hull is excluded automatically; this pins that guarantee on the pure
// filter (keyed to navigation.PurchasingFleet) so a future edit cannot silently re-open the poach.
func TestReuseEligibleIdleHulls_ExcludesPurchasingDedicatedHull(t *testing.T) {
	hulls := []domcap.HullUtilization{
		{ShipSymbol: "FRIGATE-BUYER", DedicatedFleet: navigation.PurchasingFleet, Idle: true, CargoCapacity: 80},
		{ShipSymbol: "FREE-1", DedicatedFleet: "", Idle: true, CargoCapacity: 80},
	}

	eligible := reuseEligibleIdleHulls(hulls, nil)

	symbols := make([]string, 0, len(eligible))
	for _, h := range eligible {
		symbols = append(symbols, h.ShipSymbol)
	}
	require.Equal(t, []string{"FREE-1"}, symbols,
		"the exclusive purchasing ship (dedicated_fleet=purchasing) is invisible to tier-1 reuse-idle — the reconciler can never poach it into contract-delivery")
}

// sp-cr2v staging-gate count (Admiral: "a light hauler is a light hauler"): the tier counts
// the FREE contract-fulfillment LIGHT-hauler pool — role "HAULER", cargo-capable, and either
// undedicated (a fresh autosizer buy lands here) or "contract". It EXCLUDES the COMMAND
// frigate (role "COMMAND"), probes/satellites (0-cargo / non-hauler role), the DEPOT roles
// (depot-delivery / warehouse / stocker — the depot the tier gates, not the free pool that
// must bind first), and trade/manufacturing haulers (wrong op).
func TestCountContractHaulers_CountsFreeLightHaulersExcludingFrigateAndDepot(t *testing.T) {
	cases := []struct {
		name  string
		hulls []domcap.HullUtilization
		want  int
	}{
		{
			name: "the command frigate + one free light hauler is ONE — the frigate never lifts the tier",
			hulls: []domcap.HullUtilization{
				{ShipSymbol: "TORWIND-1", Role: "COMMAND", DedicatedFleet: "contract", CargoCapacity: 80},
				{ShipSymbol: "LIGHT-1", Role: "HAULER", DedicatedFleet: "", CargoCapacity: 80},
			},
			want: 1,
		},
		{
			name: "two free light haulers (a fresh undedicated buy + the contract pool) reach the tier",
			hulls: []domcap.HullUtilization{
				{ShipSymbol: "LIGHT-1", Role: "HAULER", DedicatedFleet: "", CargoCapacity: 80},         // undedicated fresh buy
				{ShipSymbol: "LIGHT-2", Role: "HAULER", DedicatedFleet: "contract", CargoCapacity: 80}, // contract pool
			},
			want: 2,
		},
		{
			name: "depot roles / other-op / non-light / 0-cargo are all excluded",
			hulls: []domcap.HullUtilization{
				{ShipSymbol: "LIGHT-1", Role: "HAULER", DedicatedFleet: "contract", CargoCapacity: 80},       // the only one that counts
				{ShipSymbol: "FRIGATE", Role: "COMMAND", DedicatedFleet: "contract", CargoCapacity: 80},      // command frigate
				{ShipSymbol: "DELIV-1", Role: "HAULER", DedicatedFleet: "depot-delivery", CargoCapacity: 80}, // the depot the tier gates
				{ShipSymbol: "WH-1", Role: "HAULER", DedicatedFleet: "warehouse", CargoCapacity: 80},         // depot infra
				{ShipSymbol: "TRADE-1", Role: "HAULER", DedicatedFleet: "trade", CargoCapacity: 80},          // other op
				{ShipSymbol: "PROBE", Role: "SATELLITE", DedicatedFleet: "", CargoCapacity: 0},               // 0-cargo / non-hauler
			},
			want: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, countContractHaulers(tc.hulls))
		})
	}
}

// sp-5nd2 never-mispick: an idle, undedicated hull that CANNOT haul (a 0-cargo
// probe/satellite) is NOT reuse-eligible — every reconciler reuse target is a
// cargo-required hauling role (sp-r6f1), so offering a can't-haul hull only emits
// a reassign the assign gate BLOCKS, erroring CONVERGE. The filter mirrors the
// ladder's own cargo re-verify (domcap.MinReuseCargoCapacity) so the two layers
// cannot drift.
func TestReuseEligibleIdleHulls_ExcludesCargoIncapableHull(t *testing.T) {
	hulls := []domcap.HullUtilization{
		{ShipSymbol: "PROBE-0", DedicatedFleet: "", Idle: true, CargoCapacity: 0},
		{ShipSymbol: "HAULER-1", DedicatedFleet: "", Idle: true, CargoCapacity: 80},
	}

	eligible := reuseEligibleIdleHulls(hulls, nil)

	symbols := make([]string, 0, len(eligible))
	for _, h := range eligible {
		symbols = append(symbols, h.ShipSymbol)
	}
	require.Equal(t, []string{"HAULER-1"}, symbols,
		"a 0-cargo probe cannot haul, so it is never reuse-eligible for the reconciler's cargo-required hauling roles; only the cargo-capable hull is offered")
}

// A hull already holding a cluster role stays excluded even when idle+undedicated —
// so the contract-dedication check added value is isolated (the "contract" exclusion
// is not accidentally load-bearing for the cluster-role case).
func TestReuseEligibleIdleHulls_ExcludesClusterRoleHull(t *testing.T) {
	hulls := []domcap.HullUtilization{
		{ShipSymbol: "WAREHOUSE-1", DedicatedFleet: "", Idle: true, CargoCapacity: 80},
		{ShipSymbol: "FREE-1", DedicatedFleet: "", Idle: true, CargoCapacity: 80},
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
		{ShipSymbol: "LIGHT-TRADE", DedicatedFleet: "trade", Idle: true, CargoCapacity: 80},
		{ShipSymbol: "FREE-1", DedicatedFleet: "", Idle: true, CargoCapacity: 80},
	}

	eligible := reuseEligibleIdleHulls(hulls, nil)

	symbols := make([]string, 0, len(eligible))
	for _, h := range eligible {
		symbols = append(symbols, h.ShipSymbol)
	}
	require.Equal(t, []string{"FREE-1"}, symbols,
		"a hull the operator pinned to trade is invisible to the reconciler's tier-1 reassign — recovered or not — so a restart can never re-dedicate it away from trade")
}
