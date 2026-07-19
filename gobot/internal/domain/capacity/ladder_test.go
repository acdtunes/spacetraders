package capacity

// Behavioral tests for the DIFF escalation ladder. Every test drives
// the frozen Differ port (LadderDiffer.Diff) and asserts one observable
// outcome: the ordered action list. Nothing reaches into ladder internals.
//
// Test budget: 13 distinct behaviors × 2 = 26 max; 13 test functions below
// (input variations of the same behavior are table cases, not new tests).

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// ---- fixture helpers ---------------------------------------------------------

func diffActions(t *testing.T, desired DesiredTopology, actual TopologySignals) []Action {
	t.Helper()
	actions, err := NewLadderDiffer().Diff(context.Background(), desired, actual, DefaultCalibration())
	require.NoError(t, err)
	return actions
}

func idleHull(ship string) HullUtilization {
	return HullUtilization{ShipSymbol: ship, Idle: true}
}

func actionsByVerb(actions []Action, verb ActionVerb) []Action {
	var matched []Action
	for _, action := range actions {
		if action.Verb == verb {
			matched = append(matched, action)
		}
	}
	return matched
}

func actionsByTier(actions []Action, tier Tier) []Action {
	var matched []Action
	for _, action := range actions {
		if action.Tier == tier {
			matched = append(matched, action)
		}
	}
	return matched
}

// richScenario exercises every ladder rung at once: hub A needs a warehouse
// repositioned plus a warehouse top-up (one idle hull, one buy), three buffer
// goods fixed (one missing, one cap-wrong, one extra — its stockers are
// converged, so the demand gate stays open), and a worker covered by fleet
// surplus from the no-longer-desired hub D; hub B is entirely uncovered with
// the idle pool and worker surplus exhausted, so it escalates to an
// add-cluster.
func richScenario() (DesiredTopology, TopologySignals) {
	desired := DesiredTopology{Hubs: []DesiredHub{
		{
			HubSymbol:         "X1-HUB-A",
			WarehouseCount:    3,
			StockerCount:      1,
			WorkerCount:       2,
			WarehouseWaypoint: "X1-HUB-A-W2",
			BufferedGoods: []DesiredBufferedGood{
				{Good: "IRON", UnitsCap: 80},
				{Good: "GOLD", UnitsCap: 40},
			},
		},
		{HubSymbol: "X1-HUB-B", WarehouseCount: 1, WorkerCount: 1},
	}}
	actual := TopologySignals{
		Clusters: []ClusterState{
			{
				HubSymbol: "X1-HUB-A",
				Warehouses: []WarehouseState{{
					ShipSymbol: "SHIP-WH1",
					Waypoint:   "X1-HUB-A-W1",
					GoodCaps:   map[string]int{"IRON": 50, "SILVER": 20},
				}},
				Stockers: []StockerState{{ShipSymbol: "SHIP-S1", Waypoint: "X1-HUB-A"}},
				Workers:  []WorkerState{{ShipSymbol: "SHIP-K1", Waypoint: "X1-HUB-A"}},
			},
			{
				HubSymbol: "X1-HUB-D",
				Workers:   []WorkerState{{ShipSymbol: "SHIP-K9", Waypoint: "X1-HUB-D"}},
			},
		},
		IdleHulls: []HullUtilization{idleHull("SHIP-IDLE-1")},
	}
	return desired, actual
}

// ---- behaviors ---------------------------------------------------------------

// Behavior: an empty desired topology yields ZERO actions, whatever the actual
// topology looks like — the no-op-planner invariant that keeps today's
// NoOp-wired engine provably inert (it must NEVER read live clusters as
// something to tear down or grow).
func TestLadderDiffer_EmptyDesiredYieldsZeroActions(t *testing.T) {
	cases := []struct {
		name   string
		actual TopologySignals
	}{
		{name: "over an empty universe", actual: TopologySignals{}},
		{
			name: "over a live topology with clusters and idle hulls",
			actual: TopologySignals{
				Clusters: []ClusterState{{
					HubSymbol: "X1-HUB-A",
					Workers:   []WorkerState{{ShipSymbol: "SHIP-K1", Waypoint: "X1-HUB-A"}},
				}},
				IdleHulls: []HullUtilization{idleHull("SHIP-IDLE-1")},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			actions := diffActions(t, DesiredTopology{}, tc.actual)

			require.Empty(t, actions, "empty desired MUST yield zero actions")
		})
	}
}

// Behavior (cheapest-first): an uncovered hub whose need an idle hull can fill
// gets a FREE tier-1 reassign — never a capital action. The reassign targets
// the role's waypoint (defaulting to the hub itself).
func TestLadderDiffer_UncoveredHubReusesIdleHullInsteadOfBuying(t *testing.T) {
	cases := []struct {
		name         string
		hub          DesiredHub
		idle         []HullUtilization
		wantTargets  []string
		wantShipUsed []string
		wantKinds    []GapKind
	}{
		{
			name:         "one worker gap, one idle hull, target defaults to the hub",
			hub:          DesiredHub{HubSymbol: "X1-HUB-B", WorkerCount: 1},
			idle:         []HullUtilization{idleHull("SHIP-IDLE-1")},
			wantTargets:  []string{"X1-HUB-B"},
			wantShipUsed: []string{"SHIP-IDLE-1"},
			wantKinds:    []GapKind{GapWorkerShort},
		},
		{
			name: "warehouse + worker gaps, two idle hulls, explicit role waypoints",
			hub: DesiredHub{
				HubSymbol:         "X1-HUB-B",
				WarehouseCount:    1,
				WorkerCount:       1,
				WarehouseWaypoint: "X1-HUB-B-W1",
				WorkerWaypoint:    "X1-HUB-B-K1",
			},
			idle:         []HullUtilization{idleHull("SHIP-IDLE-1"), idleHull("SHIP-IDLE-2")},
			wantTargets:  []string{"X1-HUB-B-W1", "X1-HUB-B-K1"},
			wantShipUsed: []string{"SHIP-IDLE-1", "SHIP-IDLE-2"},
			wantKinds:    []GapKind{GapWarehouseShort, GapWorkerShort},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			desired := DesiredTopology{Hubs: []DesiredHub{tc.hub}}
			actual := TopologySignals{IdleHulls: tc.idle}

			actions := diffActions(t, desired, actual)

			reassigns := actionsByVerb(actions, VerbReassignHull)
			require.Len(t, actions, len(tc.wantTargets), "idle coverage must fill the whole gap")
			require.Len(t, reassigns, len(tc.wantTargets))
			require.Empty(t, actionsByTier(actions, TierCapital),
				"a gap an idle hull can close must NOT raise capital")
			for i, reassign := range reassigns {
				require.Equal(t, TierReuseIdle, reassign.Tier)
				require.Equal(t, "X1-HUB-B", reassign.HubSymbol)
				require.Equal(t, tc.wantShipUsed[i], reassign.ShipSymbol)
				require.Equal(t, tc.wantTargets[i], reassign.TargetWaypoint)
				require.Equal(t, tc.wantKinds[i], reassign.GapKind,
					"the reassign carries its role machine-readably via GapKind")
				require.Zero(t, reassign.EstimatedCostCredits, "tier 1 is FREE")
				require.Zero(t, reassign.HullDelta, "reuse adds no hull to the fleet")
			}
		})
	}
}

// Behavior (never poach): a hull that is pinned to another fleet, not actually
// idle, or already serving a cluster role is NOT reusable — the gap escalates
// up the ladder instead of stealing the hull.
func TestLadderDiffer_IneligibleIdleHullsAreNeverReassigned(t *testing.T) {
	cases := []struct {
		name   string
		actual TopologySignals
	}{
		{
			name: "pinned to another operation's fleet",
			actual: TopologySignals{IdleHulls: []HullUtilization{
				{ShipSymbol: "SHIP-PINNED", DedicatedFleet: "MANUFACTURING_C", Idle: true},
			}},
		},
		{
			name: "listed but not actually idle",
			actual: TopologySignals{IdleHulls: []HullUtilization{
				{ShipSymbol: "SHIP-BUSY", Idle: false},
			}},
		},
		{
			name: "already serving a cluster role",
			actual: TopologySignals{
				Clusters: []ClusterState{{
					HubSymbol: "X1-HUB-A",
					Workers:   []WorkerState{{ShipSymbol: "SHIP-K1", Waypoint: "X1-HUB-A"}},
				}},
				IdleHulls: []HullUtilization{idleHull("SHIP-K1")},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Hub A (when present) is already converged; hub B needs one
			// warehouse and only capital can provide it.
			desired := DesiredTopology{Hubs: []DesiredHub{
				{HubSymbol: "X1-HUB-A", WorkerCount: len(tc.actual.Clusters)},
				{HubSymbol: "X1-HUB-B", WarehouseCount: 1},
			}}

			actions := diffActions(t, desired, tc.actual)

			require.Empty(t, actionsByVerb(actions, VerbReassignHull),
				"an ineligible hull must NEVER be reassigned")
			require.Len(t, actionsByVerb(actions, VerbAddCluster), 1,
				"with no eligible idle hull the gap escalates to capital")
		})
	}
}

// Behavior: an uncovered hub with NO idle hull available escalates to ONE
// tier-4 add-cluster carrying the governor's ROI arithmetic — HullDelta is
// the cluster's warehouse+stocker+worker count and the cost estimate is
// HullDelta × the per-hull estimate. A zero-valued differ falls back to the
// documented conservative estimate rather than pricing capital at zero.
func TestLadderDiffer_UncoveredHubWithoutIdleEscalatesToAddCluster(t *testing.T) {
	cases := []struct {
		name     string
		differ   LadderDiffer
		wantCost int64
	}{
		{name: "constructed differ uses its estimate", differ: NewLadderDiffer(), wantCost: 4 * DefaultHullCostEstimateCredits},
		{name: "zero-valued differ fails closed to the default estimate", differ: LadderDiffer{}, wantCost: 4 * DefaultHullCostEstimateCredits},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			desired := DesiredTopology{Hubs: []DesiredHub{{
				HubSymbol:      "X1-HUB-B",
				WarehouseCount: 1,
				StockerCount:   1,
				WorkerCount:    2,
				BufferedGoods:  []DesiredBufferedGood{{Good: "IRON", UnitsCap: 80}},
			}}}

			actions, err := tc.differ.Diff(context.Background(), desired, TopologySignals{}, DefaultCalibration())
			require.NoError(t, err)

			require.Len(t, actions, 1, "one uncovered hub = one add-cluster, nothing else")
			addCluster := actions[0]
			require.Equal(t, TierCapital, addCluster.Tier)
			require.Equal(t, VerbAddCluster, addCluster.Verb)
			require.Equal(t, "X1-HUB-B", addCluster.HubSymbol)
			require.Equal(t, 4, addCluster.HullDelta, "HullDelta = warehouse+stocker+worker counts")
			require.Equal(t, GapHubUncovered, addCluster.GapKind)
			require.Equal(t, 1, addCluster.WarehouseDelta, "composition is machine-readable, not Reason prose")
			require.Equal(t, 1, addCluster.StockerDelta)
			require.Equal(t, 2, addCluster.WorkerDelta)
			require.Equal(t, tc.wantCost, addCluster.EstimatedCostCredits)
			require.Empty(t, actionsByTier(actions, TierBufferAdjust),
				"no warehouse exists yet to configure — the whitelist gap self-heals next tick")
		})
	}
}

// Behavior: a covered hub short on a role tops up idle-first and only buys
// the remainder — each buy is its own capital decision (HullDelta 1) so the
// per-decision cap judges hulls one at a time.
func TestLadderDiffer_CoveredHubShortfallTopsUpIdleFirstThenBuys(t *testing.T) {
	desired := DesiredTopology{Hubs: []DesiredHub{{
		HubSymbol:       "X1-HUB-A",
		StockerCount:    3,
		StockerWaypoint: "X1-HUB-A-S1",
	}}}
	actual := TopologySignals{
		Clusters: []ClusterState{{
			HubSymbol: "X1-HUB-A",
			Stockers:  []StockerState{{ShipSymbol: "SHIP-S1", Waypoint: "X1-HUB-A-S1"}},
		}},
		IdleHulls: []HullUtilization{idleHull("SHIP-IDLE-1")},
	}

	actions := diffActions(t, desired, actual)

	require.Len(t, actions, 2)
	reassigns := actionsByVerb(actions, VerbReassignHull)
	require.Len(t, reassigns, 1, "the idle hull covers one of the two missing stockers")
	require.Equal(t, "SHIP-IDLE-1", reassigns[0].ShipSymbol)
	require.Equal(t, "X1-HUB-A-S1", reassigns[0].TargetWaypoint)
	require.Equal(t, GapStockerShort, reassigns[0].GapKind)
	buys := actionsByVerb(actions, VerbBuyHull)
	require.Len(t, buys, 1, "only the remainder is bought")
	require.Equal(t, TierCapital, buys[0].Tier)
	require.Equal(t, 1, buys[0].HullDelta, "each buy is one hull — one capital decision")
	require.Equal(t, GapStockerShort, buys[0].GapKind,
		"the bought hull's ROLE is machine-readable via GapKind, not Reason prose")
	require.Equal(t, 1, buys[0].StockerDelta, "per-role delta decomposes HullDelta")
	require.Zero(t, buys[0].WarehouseDelta)
	require.Zero(t, buys[0].WorkerDelta)
	require.Equal(t, DefaultHullCostEstimateCredits, buys[0].EstimatedCostCredits)
	require.Equal(t, "X1-HUB-A-S1", buys[0].TargetWaypoint)
}

// Behavior: a worker shortfall coverable by the fleet's own worker surplus
// (over-covered desired hubs AND hubs the plan no longer wants) rebalances for
// free at tier 2; only the surplus-exhausted remainder raises capital.
func TestLadderDiffer_WorkerShortfallRebalancesFleetSurplusBeforeBuying(t *testing.T) {
	cases := []struct {
		name           string
		clusters       []ClusterState
		wantRebalances int
		wantMoved      int
		wantBuys       int
	}{
		{
			name: "surplus at an over-covered desired hub fully covers the gap",
			clusters: []ClusterState{
				{HubSymbol: "X1-HUB-A", Workers: []WorkerState{
					{ShipSymbol: "SHIP-K1"}, {ShipSymbol: "SHIP-K2"}, {ShipSymbol: "SHIP-K3"},
				}},
				{HubSymbol: "X1-HUB-B"},
			},
			wantRebalances: 1,
			wantMoved:      2,
			wantBuys:       0,
		},
		{
			name: "surplus at a hub the plan dropped covers the gap",
			clusters: []ClusterState{
				{HubSymbol: "X1-HUB-A", Workers: []WorkerState{{ShipSymbol: "SHIP-K1"}}},
				{HubSymbol: "X1-HUB-B"},
				{HubSymbol: "X1-HUB-D", Workers: []WorkerState{
					{ShipSymbol: "SHIP-K8"}, {ShipSymbol: "SHIP-K9"},
				}},
			},
			wantRebalances: 1,
			wantMoved:      2,
			wantBuys:       0,
		},
		{
			name: "exhausted surplus buys only the remainder",
			clusters: []ClusterState{
				{HubSymbol: "X1-HUB-A", Workers: []WorkerState{
					{ShipSymbol: "SHIP-K1"}, {ShipSymbol: "SHIP-K2"},
				}},
				{HubSymbol: "X1-HUB-B"},
			},
			wantRebalances: 1,
			wantMoved:      1,
			wantBuys:       1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			desired := DesiredTopology{Hubs: []DesiredHub{
				{HubSymbol: "X1-HUB-A", WorkerCount: 1},
				{HubSymbol: "X1-HUB-B", WorkerCount: 2},
			}}
			actual := TopologySignals{Clusters: tc.clusters}

			actions := diffActions(t, desired, actual)

			rebalances := actionsByVerb(actions, VerbRebalanceWorkers)
			require.Len(t, rebalances, tc.wantRebalances)
			for _, rebalance := range rebalances {
				require.Equal(t, TierRebalance, rebalance.Tier)
				require.Equal(t, "X1-HUB-B", rebalance.HubSymbol, "the rebalance targets the shortfall hub")
				require.Equal(t, GapWorkerShort, rebalance.GapKind)
				require.Equal(t, tc.wantMoved, rebalance.Count,
					"the moved-worker count is machine-readable via Count, not Reason prose")
				require.Zero(t, rebalance.EstimatedCostCredits, "tier 2 is FREE")
				require.Zero(t, rebalance.HullDelta)
			}
			require.Len(t, actionsByVerb(actions, VerbBuyHull), tc.wantBuys,
				"capital only for what free surplus cannot cover")
		})
	}
}

// Behavior: a warehouse anchored at the wrong waypoint is repositioned to the
// desired anchor (tier 2); a converged or position-unknown warehouse is left
// alone (acting on missing position data would thrash).
func TestLadderDiffer_MisplacedWarehouseIsRepositioned(t *testing.T) {
	cases := []struct {
		name            string
		desiredWaypoint string
		actualWaypoint  string
		wantReposition  bool
		wantTarget      string
	}{
		{name: "wrong waypoint moves to the desired anchor", desiredWaypoint: "X1-HUB-A-W9", actualWaypoint: "X1-HUB-A-OLD", wantReposition: true, wantTarget: "X1-HUB-A-W9"},
		{name: "empty desired waypoint anchors at the hub itself", desiredWaypoint: "", actualWaypoint: "X1-HUB-A-OLD", wantReposition: true, wantTarget: "X1-HUB-A"},
		{name: "already at the anchor stays put", desiredWaypoint: "X1-HUB-A-W9", actualWaypoint: "X1-HUB-A-W9", wantReposition: false},
		{name: "unknown position is never acted on", desiredWaypoint: "X1-HUB-A-W9", actualWaypoint: "", wantReposition: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			desired := DesiredTopology{Hubs: []DesiredHub{{
				HubSymbol:         "X1-HUB-A",
				WarehouseCount:    1,
				WarehouseWaypoint: tc.desiredWaypoint,
			}}}
			actual := TopologySignals{Clusters: []ClusterState{{
				HubSymbol:  "X1-HUB-A",
				Warehouses: []WarehouseState{{ShipSymbol: "SHIP-WH1", Waypoint: tc.actualWaypoint}},
			}}}

			actions := diffActions(t, desired, actual)

			repositions := actionsByVerb(actions, VerbRepositionHull)
			if !tc.wantReposition {
				require.Empty(t, actions, "a converged hub must emit nothing")
				return
			}
			require.Len(t, actions, 1)
			require.Len(t, repositions, 1)
			require.Equal(t, TierRebalance, repositions[0].Tier)
			require.Equal(t, "SHIP-WH1", repositions[0].ShipSymbol)
			require.Equal(t, tc.wantTarget, repositions[0].TargetWaypoint)
			require.Equal(t, GapHullMisplaced, repositions[0].GapKind)
			require.Zero(t, repositions[0].EstimatedCostCredits, "tier 2 is FREE")
		})
	}
}

// Behavior: the buffer whitelist + caps converge to the desired set at tier 3
// — a missing good is whitelisted at its cap, a stale cap is corrected, an
// unwanted good is de-whitelisted (cap 0), and a hub whose warehouses jointly
// hold the desired cap is already converged.
func TestLadderDiffer_BufferWhitelistAndCapsConvergeAtTierThree(t *testing.T) {
	cases := []struct {
		name       string
		desired    []DesiredBufferedGood
		warehouses []WarehouseState
		wantVerb   ActionVerb
		wantGood   string
		wantCap    int
		wantKind   GapKind
		converged  bool
	}{
		{
			name:       "missing good joins the whitelist at its cap",
			desired:    []DesiredBufferedGood{{Good: "IRON", UnitsCap: 80}},
			warehouses: []WarehouseState{{ShipSymbol: "SHIP-WH1", Waypoint: "X1-HUB-A", GoodCaps: map[string]int{}}},
			wantVerb:   VerbAdjustBufferWhitelist,
			wantGood:   "IRON",
			wantCap:    80,
			wantKind:   GapBufferGoodMissing,
		},
		{
			name:       "wrong cap is corrected to the desired cap",
			desired:    []DesiredBufferedGood{{Good: "IRON", UnitsCap: 80}},
			warehouses: []WarehouseState{{ShipSymbol: "SHIP-WH1", Waypoint: "X1-HUB-A", GoodCaps: map[string]int{"IRON": 50}}},
			wantVerb:   VerbAdjustBufferCap,
			wantGood:   "IRON",
			wantCap:    80,
			wantKind:   GapBufferCapWrong,
		},
		{
			name:       "unwanted good is de-whitelisted with cap zero",
			desired:    nil,
			warehouses: []WarehouseState{{ShipSymbol: "SHIP-WH1", Waypoint: "X1-HUB-A", GoodCaps: map[string]int{"GOLD": 30}}},
			wantVerb:   VerbAdjustBufferWhitelist,
			wantGood:   "GOLD",
			wantCap:    0,
			wantKind:   GapBufferGoodExtra,
		},
		{
			name:    "caps merged across the hub's warehouses count as converged",
			desired: []DesiredBufferedGood{{Good: "IRON", UnitsCap: 80}},
			warehouses: []WarehouseState{
				{ShipSymbol: "SHIP-WH1", Waypoint: "X1-HUB-A", GoodCaps: map[string]int{"IRON": 50}},
				{ShipSymbol: "SHIP-WH2", Waypoint: "X1-HUB-A", GoodCaps: map[string]int{"IRON": 30}},
			},
			converged: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			desired := DesiredTopology{Hubs: []DesiredHub{{
				HubSymbol:      "X1-HUB-A",
				WarehouseCount: len(tc.warehouses),
				BufferedGoods:  tc.desired,
			}}}
			actual := TopologySignals{Clusters: []ClusterState{{
				HubSymbol:  "X1-HUB-A",
				Warehouses: tc.warehouses,
			}}}

			actions := diffActions(t, desired, actual)

			if tc.converged {
				require.Empty(t, actions, "a converged whitelist must emit nothing")
				return
			}
			require.Len(t, actions, 1)
			adjust := actions[0]
			require.Equal(t, TierBufferAdjust, adjust.Tier)
			require.Equal(t, tc.wantVerb, adjust.Verb)
			require.Equal(t, "X1-HUB-A", adjust.HubSymbol)
			require.Equal(t, tc.wantGood, adjust.Good)
			require.Equal(t, tc.wantCap, adjust.UnitsCap)
			require.Equal(t, tc.wantKind, adjust.GapKind)
			require.Zero(t, adjust.EstimatedCostCredits, "tier 3 is cheap — no capital")
			require.Zero(t, adjust.HullDelta)
		})
	}
}

// Behavior (spec DIFF tier-3 gate: "adjust buffer whitelist + caps — cheap →
// auto, GATED if it forces a new stocker"): a hub whose desired stocker
// capacity has NOT landed yet (desired StockerCount > actual stockers) gets
// its demand-EXPANDING buffer adjustments WITHHELD — auto-executing a
// whitelist add or cap raise tiers ahead of the approval-gated stocker would
// draw restock demand the hub cannot serve for the whole approval window.
// Demand-SHEDDING adjustments (cap reductions, de-whitelists) always flow,
// the stocker shortfall itself still escalates normally, and a withheld
// adjustment self-heals on a later tick, once the stocker capacity lands.
func TestLadderDiffer_StockerShortHubWithholdsDemandExpandingBufferAdjusts(t *testing.T) {
	twoStockers := []StockerState{{ShipSymbol: "SHIP-S1", Waypoint: "X1-HUB-A"}, {ShipSymbol: "SHIP-S2", Waypoint: "X1-HUB-A"}}
	cases := []struct {
		name            string
		desiredStockers int
		desiredGoods    []DesiredBufferedGood
		goodCaps        map[string]int
		wantAdjusts     int
		wantGood        string
		wantCap         int
		wantStockerBuys int
	}{
		{
			name:            "whitelist ADD withheld while the extra stocker awaits approval",
			desiredStockers: 3,
			desiredGoods:    []DesiredBufferedGood{{Good: "GOLD", UnitsCap: 40}},
			goodCaps:        map[string]int{},
			wantAdjusts:     0,
			wantStockerBuys: 1,
		},
		{
			name:            "cap RAISE withheld while the extra stocker awaits approval",
			desiredStockers: 3,
			desiredGoods:    []DesiredBufferedGood{{Good: "IRON", UnitsCap: 80}},
			goodCaps:        map[string]int{"IRON": 50},
			wantAdjusts:     0,
			wantStockerBuys: 1,
		},
		{
			name:            "cap REDUCTION still flows — shedding demand forces no stocker",
			desiredStockers: 3,
			desiredGoods:    []DesiredBufferedGood{{Good: "IRON", UnitsCap: 30}},
			goodCaps:        map[string]int{"IRON": 50},
			wantAdjusts:     1,
			wantGood:        "IRON",
			wantCap:         30,
			wantStockerBuys: 1,
		},
		{
			name:            "de-whitelist still flows — shedding demand forces no stocker",
			desiredStockers: 3,
			desiredGoods:    nil,
			goodCaps:        map[string]int{"GOLD": 30},
			wantAdjusts:     1,
			wantGood:        "GOLD",
			wantCap:         0,
			wantStockerBuys: 1,
		},
		{
			name:            "stocker capacity landed — the withheld add self-heals and emits",
			desiredStockers: 2,
			desiredGoods:    []DesiredBufferedGood{{Good: "GOLD", UnitsCap: 40}},
			goodCaps:        map[string]int{},
			wantAdjusts:     1,
			wantGood:        "GOLD",
			wantCap:         40,
			wantStockerBuys: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			desired := DesiredTopology{Hubs: []DesiredHub{{
				HubSymbol:      "X1-HUB-A",
				WarehouseCount: 1,
				StockerCount:   tc.desiredStockers,
				BufferedGoods:  tc.desiredGoods,
			}}}
			actual := TopologySignals{Clusters: []ClusterState{{
				HubSymbol: "X1-HUB-A",
				Warehouses: []WarehouseState{{
					ShipSymbol: "SHIP-WH1", Waypoint: "X1-HUB-A", GoodCaps: tc.goodCaps,
				}},
				Stockers: twoStockers,
			}}}

			actions := diffActions(t, desired, actual)

			adjusts := actionsByTier(actions, TierBufferAdjust)
			require.Len(t, adjusts, tc.wantAdjusts,
				"only demand-shedding buffer adjustments may flow on a stocker-short hub")
			if tc.wantAdjusts > 0 {
				require.Equal(t, tc.wantGood, adjusts[0].Good)
				require.Equal(t, tc.wantCap, adjusts[0].UnitsCap)
			}
			require.Len(t, actionsByVerb(actions, VerbBuyHull), tc.wantStockerBuys,
				"withholding the buffer adjust must NOT suppress the stocker escalation itself")
		})
	}
}

// Behavior (cheapest-lever-first at hub standup): an UNCOVERED hub's worker
// shortfall draws the fleet's own worker surplus — ONE tier-2
// rebalance_workers toward the hub, exactly like a covered hub — BEFORE
// capital: the add_cluster carries only the hulls no free lever could cover.
// Without this, dropping hub D (its workers stranded there — no teardown in
// v1) while adding hub B proposes BUYING workers the fleet already owns — a
// permanent over-buy if approved.
func TestLadderDiffer_UncoveredHubDrawsWorkerSurplusBeforeProposingCapital(t *testing.T) {
	cases := []struct {
		name            string
		warehouseCount  int
		droppedWorkers  int
		wantMoved       int
		wantAddClusters int
		wantHullDelta   int
		wantWorkerDelta int
	}{
		{
			name:           "dropped hub's surplus covers all workers — the proposal shrinks to the warehouse",
			warehouseCount: 1, droppedWorkers: 2,
			wantMoved: 2, wantAddClusters: 1, wantHullDelta: 1, wantWorkerDelta: 0,
		},
		{
			name:           "partial surplus — capital carries only the uncovered remainder",
			warehouseCount: 1, droppedWorkers: 1,
			wantMoved: 1, wantAddClusters: 1, wantHullDelta: 2, wantWorkerDelta: 1,
		},
		{
			name:           "no surplus — the whole cluster is capital",
			warehouseCount: 1, droppedWorkers: 0,
			wantMoved: 0, wantAddClusters: 1, wantHullDelta: 3, wantWorkerDelta: 2,
		},
		{
			name:           "worker-only hub fully covered by surplus raises NO capital at all",
			warehouseCount: 0, droppedWorkers: 2,
			wantMoved: 2, wantAddClusters: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			desired := DesiredTopology{Hubs: []DesiredHub{{
				HubSymbol:      "X1-HUB-B",
				WarehouseCount: tc.warehouseCount,
				WorkerCount:    2,
			}}}
			dropped := ClusterState{HubSymbol: "X1-HUB-D"}
			for i := 0; i < tc.droppedWorkers; i++ {
				dropped.Workers = append(dropped.Workers, WorkerState{ShipSymbol: fmt.Sprintf("SHIP-K%d", i+1), Waypoint: "X1-HUB-D"})
			}
			actual := TopologySignals{Clusters: []ClusterState{dropped}}

			actions := diffActions(t, desired, actual)

			rebalances := actionsByVerb(actions, VerbRebalanceWorkers)
			if tc.wantMoved == 0 {
				require.Empty(t, rebalances)
			} else {
				require.Len(t, rebalances, 1, "ONE rebalance_workers per shortfall hub")
				require.Equal(t, "X1-HUB-B", rebalances[0].HubSymbol)
				require.Equal(t, tc.wantMoved, rebalances[0].Count)
				require.Zero(t, rebalances[0].EstimatedCostCredits, "the surplus draw is FREE")
			}
			addClusters := actionsByVerb(actions, VerbAddCluster)
			require.Len(t, addClusters, tc.wantAddClusters,
				"capital only for what the fleet's own surplus cannot cover")
			if tc.wantAddClusters > 0 {
				require.Equal(t, tc.wantHullDelta, addClusters[0].HullDelta,
					"surplus-coverable workers must NOT be bought")
				require.Equal(t, tc.wantWorkerDelta, addClusters[0].WorkerDelta)
				require.Equal(t, tc.warehouseCount, addClusters[0].WarehouseDelta)
				require.Equal(t, int64(tc.wantHullDelta)*DefaultHullCostEstimateCredits,
					addClusters[0].EstimatedCostCredits)
			}
		})
	}
}

// Behavior (CONVERGE backstop compatibility): every emitted verb carries its
// canonical tier from action.go — dispatch refuses a mislabeled action, so a
// wrong label here would dead-letter the whole ladder.
func TestLadderDiffer_EmittedVerbsCarryCanonicalTiers(t *testing.T) {
	canonical := map[ActionVerb]Tier{
		VerbReassignHull:          TierReuseIdle,
		VerbRepositionHull:        TierRebalance,
		VerbRebalanceWorkers:      TierRebalance,
		VerbAdjustBufferWhitelist: TierBufferAdjust,
		VerbAdjustBufferCap:       TierBufferAdjust,
		VerbAddCluster:            TierCapital,
		VerbBuyHull:               TierCapital,
	}
	desired, actual := richScenario()

	actions := diffActions(t, desired, actual)

	seen := map[ActionVerb]bool{}
	for _, action := range actions {
		wantTier, known := canonical[action.Verb]
		require.True(t, known, "unknown verb %s would be refused by converge", action.Verb)
		require.Equal(t, wantTier, action.Tier,
			"%s must carry its canonical tier or converge refuses it", action.Verb)
		seen[action.Verb] = true
	}
	for verb := range canonical {
		require.True(t, seen[verb], "the rich scenario must exercise verb %s", verb)
	}
}

// Behavior: actions come out cheapest-first — ascending tier order — so the
// free levers land before a single credit of capital is proposed.
func TestLadderDiffer_ActionsEmergeInAscendingTierOrder(t *testing.T) {
	desired, actual := richScenario()

	actions := diffActions(t, desired, actual)

	require.NotEmpty(t, actions)
	for i := 1; i < len(actions); i++ {
		require.LessOrEqual(t, actions[i-1].Tier, actions[i].Tier,
			"action %d (%s tier %d) may not precede action %d (%s tier %d) — cheapest lever first",
			i-1, actions[i-1].Verb, actions[i-1].Tier, i, actions[i].Verb, actions[i].Tier)
	}
	require.Equal(t, TierReuseIdle, actions[0].Tier, "the free reuse rung leads")
	require.Equal(t, TierCapital, actions[len(actions)-1].Tier, "capital trails")
}

// Behavior: the ladder is deterministic — identical inputs produce the
// identical action list (the loop is stateless per tick; a jittering plan
// would re-file proposals under new identities every tick).
func TestLadderDiffer_IdenticalInputsProduceIdenticalActions(t *testing.T) {
	desired, actual := richScenario()

	first := diffActions(t, desired, actual)
	second := diffActions(t, desired, actual)

	require.Equal(t, first, second, "same desired + actual must reproduce the same plan")
}
