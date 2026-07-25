package scouting

import (
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"
)

// The whitelist is the scope filter: a row whose good is not whitelisted must
// contribute NOTHING — not to depth, not to the hot-market count — no matter
// how deep it is. FUEL at enormous volume here would dwarf the CLOTHING row if
// it leaked in.
func TestBuildSensingProfiles_WhitelistedOnlyAggregation(t *testing.T) {
	whitelist := map[string]bool{"CLOTHING": true}
	rows := []MarketDepthRow{
		{System: "X1-AA", Waypoint: "X1-AA-M1", Good: "CLOTHING", TradeVolume: 10, MidPrice: 100},
		{System: "X1-AA", Waypoint: "X1-AA-M2", Good: "FUEL", TradeVolume: 9999, MidPrice: 9999},
	}

	profiles := BuildSensingProfiles(rows, whitelist)

	require.Len(t, profiles, 1)
	require.Equal(t, "X1-AA", profiles[0].System)
	require.Equal(t, int64(1000), profiles[0].Depth, "only the CLOTHING row may contribute: 10×100")
	require.Equal(t, 1, profiles[0].HotMarkets, "the FUEL-only waypoint is not a hot market")
}

// Depth sums trade_volume × mid_price across every whitelisted row of every
// market in the system.
func TestBuildSensingProfiles_DepthSumsAcrossMarkets(t *testing.T) {
	whitelist := map[string]bool{"CLOTHING": true, "FOOD": true}
	rows := []MarketDepthRow{
		{System: "X1-AA", Waypoint: "X1-AA-M1", Good: "CLOTHING", TradeVolume: 10, MidPrice: 100},
		{System: "X1-AA", Waypoint: "X1-AA-M2", Good: "FOOD", TradeVolume: 20, MidPrice: 50},
		{System: "X1-BB", Waypoint: "X1-BB-M1", Good: "FOOD", TradeVolume: 3, MidPrice: 7},
	}

	profiles := BuildSensingProfiles(rows, whitelist)

	require.Len(t, profiles, 2)
	require.Equal(t, int64(10*100+20*50), profiles[0].Depth, "X1-AA sums both markets")
	require.Equal(t, int64(21), profiles[1].Depth)
}

// HotMarkets counts distinct WAYPOINTS carrying at least one whitelisted good,
// never rows: two whitelisted goods at one waypoint are one hot market.
func TestBuildSensingProfiles_HotMarketsCountsWaypointsNotRows(t *testing.T) {
	whitelist := map[string]bool{"CLOTHING": true, "FOOD": true}
	rows := []MarketDepthRow{
		{System: "X1-AA", Waypoint: "X1-AA-M1", Good: "CLOTHING", TradeVolume: 10, MidPrice: 100},
		{System: "X1-AA", Waypoint: "X1-AA-M1", Good: "FOOD", TradeVolume: 20, MidPrice: 50},
		{System: "X1-AA", Waypoint: "X1-AA-M2", Good: "FOOD", TradeVolume: 5, MidPrice: 5},
	}

	profiles := BuildSensingProfiles(rows, whitelist)

	require.Len(t, profiles, 1)
	require.Equal(t, 2, profiles[0].HotMarkets, "M1's two goods collapse to one hot market")
	require.Equal(t, int64(10*100+20*50+5*5), profiles[0].Depth, "depth still sums all three rows")
}

// Garbage rows — zero/negative trade volume or mid price — contribute nothing
// (fail closed): they must not add depth and must not make a waypoint hot.
func TestBuildSensingProfiles_GarbageRowsContributeNothing(t *testing.T) {
	whitelist := map[string]bool{"CLOTHING": true}
	rows := []MarketDepthRow{
		{System: "X1-AA", Waypoint: "X1-AA-M1", Good: "CLOTHING", TradeVolume: 0, MidPrice: 100},
		{System: "X1-AA", Waypoint: "X1-AA-M2", Good: "CLOTHING", TradeVolume: 10, MidPrice: -5},
		{System: "X1-AA", Waypoint: "X1-AA-M3", Good: "CLOTHING", TradeVolume: 4, MidPrice: 25},
	}

	profiles := BuildSensingProfiles(rows, whitelist)

	require.Len(t, profiles, 1)
	require.Equal(t, int64(100), profiles[0].Depth, "only the valid M3 row contributes")
	require.Equal(t, 1, profiles[0].HotMarkets, "a waypoint whose only rows are garbage is not hot")
}

// A system whose every row is filtered out (non-whitelisted or garbage) does
// not appear at all: it needs no probe, so it has no profile.
func TestBuildSensingProfiles_FullyFilteredSystemAbsent(t *testing.T) {
	whitelist := map[string]bool{"CLOTHING": true}
	rows := []MarketDepthRow{
		{System: "X1-ORE", Waypoint: "X1-ORE-M1", Good: "IRON_ORE", TradeVolume: 100, MidPrice: 10},
		{System: "X1-AA", Waypoint: "X1-AA-M1", Good: "CLOTHING", TradeVolume: 1, MidPrice: 1},
	}

	profiles := BuildSensingProfiles(rows, whitelist)

	require.Len(t, profiles, 1)
	require.Equal(t, "X1-AA", profiles[0].System)
}

// Deterministic: a shuffled copy of the same rows yields the identical,
// System-ascending output.
func TestBuildSensingProfiles_DeterministicUnderShuffle(t *testing.T) {
	whitelist := map[string]bool{"CLOTHING": true, "FOOD": true}
	rows := []MarketDepthRow{
		{System: "X1-CC", Waypoint: "X1-CC-M1", Good: "FOOD", TradeVolume: 2, MidPrice: 3},
		{System: "X1-AA", Waypoint: "X1-AA-M1", Good: "CLOTHING", TradeVolume: 10, MidPrice: 100},
		{System: "X1-BB", Waypoint: "X1-BB-M1", Good: "FOOD", TradeVolume: 5, MidPrice: 5},
		{System: "X1-AA", Waypoint: "X1-AA-M2", Good: "FOOD", TradeVolume: 1, MidPrice: 9},
	}

	want := BuildSensingProfiles(rows, whitelist)
	require.Equal(t, []string{"X1-AA", "X1-BB", "X1-CC"}, []string{want[0].System, want[1].System, want[2].System},
		"output is sorted by System ascending")

	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 20; i++ {
		shuffled := append([]MarketDepthRow(nil), rows...)
		rng.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
		require.Equal(t, want, BuildSensingProfiles(shuffled, whitelist))
	}
}

// The floor is inclusive: a system exactly AT the floor is in scope, one credit
// below it is out.
func TestPlanSensing_FloorBoundary(t *testing.T) {
	profiles := []SystemSensingProfile{
		{System: "X1-AT", Depth: 2_000_000, HotMarkets: 3},
		{System: "X1-BELOW", Depth: 1_999_999, HotMarkets: 3},
	}

	plan := PlanSensing(profiles, 2_000_000, 12)

	require.Equal(t, map[string]int{"X1-AT": 1}, plan.Hulls)
	require.Equal(t, 1, plan.TotalHulls)
}

// The floor is not negotiable by market count: a system just under the floor
// stays OUT no matter how many hot markets it carries.
func TestPlanSensing_HugeHotMarketsCannotBuyPastFloor(t *testing.T) {
	profiles := []SystemSensingProfile{
		{System: "X1-WIDE", Depth: 1_999_999, HotMarkets: 500},
	}

	plan := PlanSensing(profiles, 2_000_000, 12)

	require.Empty(t, plan.Hulls)
	require.Zero(t, plan.TotalHulls)
}

// The second probe arrives strictly ABOVE the threshold: at exactly the
// threshold a system keeps one probe; one more hot market earns the second.
func TestPlanSensing_SecondProbeThresholdBoundary(t *testing.T) {
	profiles := []SystemSensingProfile{
		{System: "X1-AT", Depth: 5_000_000, HotMarkets: 12},
		{System: "X1-ABOVE", Depth: 5_000_000, HotMarkets: 13},
	}

	plan := PlanSensing(profiles, 2_000_000, 12)

	require.Equal(t, map[string]int{"X1-AT": 1, "X1-ABOVE": 2}, plan.Hulls)
	require.Equal(t, 3, plan.TotalHulls)
}

// A non-positive floor disables the depth cut: everything with at least one
// hot market is in scope.
func TestPlanSensing_NonPositiveFloorDisablesDepthCut(t *testing.T) {
	profiles := []SystemSensingProfile{
		{System: "X1-THIN", Depth: 1, HotMarkets: 1},
		{System: "X1-ZERO", Depth: 0, HotMarkets: 2},
	}

	for _, floor := range []int64{0, -1} {
		plan := PlanSensing(profiles, floor, 12)
		require.Equal(t, map[string]int{"X1-THIN": 1, "X1-ZERO": 1}, plan.Hulls)
		require.Equal(t, 2, plan.TotalHulls)
	}
}

// A profile with no hot market is never in scope, even with depth above the
// floor: there is nothing whitelisted for a probe to look at.
func TestPlanSensing_NoHotMarketsMeansOutOfScope(t *testing.T) {
	profiles := []SystemSensingProfile{
		{System: "X1-GHOST", Depth: 50_000_000, HotMarkets: 0},
	}

	plan := PlanSensing(profiles, 2_000_000, 12)

	require.Empty(t, plan.Hulls)
	require.Zero(t, plan.TotalHulls)
}

// TotalHulls is exactly Σ Hulls, and every in-scope system is treated equally —
// no ranking, no ordering effects on the totals.
func TestPlanSensing_TotalHullsIsSumOfHulls(t *testing.T) {
	profiles := []SystemSensingProfile{
		{System: "X1-AA", Depth: 3_000_000, HotMarkets: 4},
		{System: "X1-BB", Depth: 9_000_000, HotMarkets: 20},
		{System: "X1-CC", Depth: 2_500_000, HotMarkets: 1},
	}

	plan := PlanSensing(profiles, 2_000_000, 12)

	sum := 0
	for _, n := range plan.Hulls {
		sum += n
	}
	require.Equal(t, sum, plan.TotalHulls)
	require.Equal(t, map[string]int{"X1-AA": 1, "X1-BB": 2, "X1-CC": 1}, plan.Hulls)
}
