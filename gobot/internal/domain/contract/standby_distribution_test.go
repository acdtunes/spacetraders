package contract

import (
	"reflect"
	"testing"
)

// uniformDistance builds a per-hull distance map that is IDENTICAL for every
// station, so distance can never spread the hulls — only the demand-ranked
// occupancy-spread can. This is the co-located pile-up scenario: N idle hulls all
// sitting at the same sink, homed against the same snapshot.
func uniformDistance(stations ...string) map[string]float64 {
	d := make(map[string]float64, len(stations))
	for _, s := range stations {
		d[s] = 1
	}
	return d
}

// The load-bearing RED proof: three co-located idle hulls and three demand-ranked
// standby waypoints must DISTRIBUTE one-per-waypoint (highest demand first), NOT
// pile on a single point. A distance-only balancer collapses them all onto one
// station (every station is equidistant); the demand-ranked spread must fan them
// out.
func TestDistributeIdleHullsAcrossStandby_CoLocatedHulls_SpreadNotPiled(t *testing.T) {
	waypoints := []StandbyWaypoint{
		{Symbol: "X1-A", DemandWeight: 3},
		{Symbol: "X1-B", DemandWeight: 2},
		{Symbol: "X1-C", DemandWeight: 1},
	}
	hulls := []IdleHullToPlace{
		{ShipSymbol: "H-1", Distance: uniformDistance("X1-A", "X1-B", "X1-C")},
		{ShipSymbol: "H-2", Distance: uniformDistance("X1-A", "X1-B", "X1-C")},
		{ShipSymbol: "H-3", Distance: uniformDistance("X1-A", "X1-B", "X1-C")},
	}

	got := DistributeIdleHullsAcrossStandby(hulls, waypoints, nil)

	// Every hull placed, and across three distinct waypoints (no pile-up).
	if len(got) != 3 {
		t.Fatalf("expected all 3 hulls placed, got %d: %v", len(got), got)
	}
	distinct := map[string]struct{}{}
	for _, wp := range got {
		distinct[wp] = struct{}{}
	}
	if len(distinct) != 3 {
		t.Fatalf("expected hulls spread across 3 distinct waypoints, got %d (%v) - hulls piled up", len(distinct), got)
	}

	// Highest-demand waypoints filled first: with equal occupancy and distance the
	// demand order A>B>C decides, and symbol-sorted hulls take them in turn.
	want := map[string]string{"H-1": "X1-A", "H-2": "X1-B", "H-3": "X1-C"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("demand-ranked spread mismatch:\n got  %v\n want %v", got, want)
	}
}

// Determinism: identical inputs must yield an identical distribution, and the
// placement must be symbol-tie-broken so hull identity (not map iteration order)
// decides who takes the top waypoint.
func TestDistributeIdleHullsAcrossStandby_Deterministic_SymbolTiebroken(t *testing.T) {
	waypoints := []StandbyWaypoint{
		{Symbol: "X1-A", DemandWeight: 3},
		{Symbol: "X1-B", DemandWeight: 2},
	}
	// Hulls supplied out of symbol order; the highest-demand waypoint must go to
	// the symbol-smallest hull regardless of input order.
	hulls := []IdleHullToPlace{
		{ShipSymbol: "H-9", Distance: uniformDistance("X1-A", "X1-B")},
		{ShipSymbol: "H-2", Distance: uniformDistance("X1-A", "X1-B")},
	}

	first := DistributeIdleHullsAcrossStandby(hulls, waypoints, nil)
	for i := 0; i < 20; i++ {
		again := DistributeIdleHullsAcrossStandby(hulls, waypoints, nil)
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("non-deterministic distribution on run %d:\n first %v\n again %v", i, first, again)
		}
	}
	if first["H-2"] != "X1-A" || first["H-9"] != "X1-B" {
		t.Fatalf("symbol-smallest hull must take the highest-demand waypoint, got %v", first)
	}
}

// Regression: a single standby waypoint (M==1) is the degenerate case — every
// idle hull homes there, with no panic and no dropped hull.
func TestDistributeIdleHullsAcrossStandby_SingleWaypoint_AllHomeThere(t *testing.T) {
	waypoints := []StandbyWaypoint{{Symbol: "X1-ONLY", DemandWeight: 5}}
	hulls := []IdleHullToPlace{
		{ShipSymbol: "H-1", Distance: uniformDistance("X1-ONLY")},
		{ShipSymbol: "H-2", Distance: uniformDistance("X1-ONLY")},
		{ShipSymbol: "H-3", Distance: uniformDistance("X1-ONLY")},
	}

	got := DistributeIdleHullsAcrossStandby(hulls, waypoints, nil)

	want := map[string]string{"H-1": "X1-ONLY", "H-2": "X1-ONLY", "H-3": "X1-ONLY"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("single-waypoint degenerate case mismatch:\n got  %v\n want %v", got, want)
	}
}

// More hulls than waypoints: the fleet still spreads evenly (round-robin), and the
// EXTRA hulls stack onto the highest-demand waypoints first — so a 5-hull, 3-hub
// set puts a second hull on the top-2 demand hubs, none piled three-deep while
// another sits single.
func TestDistributeIdleHullsAcrossStandby_MoreHullsThanWaypoints_DemandRankedRoundRobin(t *testing.T) {
	waypoints := []StandbyWaypoint{
		{Symbol: "X1-A", DemandWeight: 3},
		{Symbol: "X1-B", DemandWeight: 2},
		{Symbol: "X1-C", DemandWeight: 1},
	}
	hulls := make([]IdleHullToPlace, 0, 5)
	for _, s := range []string{"H-1", "H-2", "H-3", "H-4", "H-5"} {
		hulls = append(hulls, IdleHullToPlace{ShipSymbol: s, Distance: uniformDistance("X1-A", "X1-B", "X1-C")})
	}

	got := DistributeIdleHullsAcrossStandby(hulls, waypoints, nil)

	counts := map[string]int{}
	for _, wp := range got {
		counts[wp]++
	}
	// Even round-robin: 5 hulls / 3 hubs → 2,2,1. The extra two land on the
	// highest-demand hubs A and B; C keeps a single hull.
	if counts["X1-A"] != 2 || counts["X1-B"] != 2 || counts["X1-C"] != 1 {
		t.Fatalf("expected demand-ranked round-robin A:2 B:2 C:1, got %v (%v)", counts, got)
	}
}

// Existing occupancy (peers already parked at a hub) is respected: the next hull
// avoids the occupied hub even though it is the highest-demand one, so the fleet
// evens out rather than double-stacking.
func TestDistributeIdleHullsAcrossStandby_RespectsExistingOccupancy(t *testing.T) {
	waypoints := []StandbyWaypoint{
		{Symbol: "X1-A", DemandWeight: 3}, // highest demand, but already occupied
		{Symbol: "X1-B", DemandWeight: 2},
	}
	hulls := []IdleHullToPlace{
		{ShipSymbol: "H-1", Distance: uniformDistance("X1-A", "X1-B")},
	}

	got := DistributeIdleHullsAcrossStandby(hulls, waypoints, map[string]int{"X1-A": 1})

	if got["H-1"] != "X1-B" {
		t.Fatalf("hull must avoid the already-occupied high-demand hub, got %v", got)
	}
}

// Backward-compatibility: with a uniform (unranked) demand set, the equal-demand
// tie-break falls through to distance, preserving plain nearest-station homing —
// so this fix never regresses an operator's existing flat standby set.
func TestDistributeIdleHullsAcrossStandby_UniformDemand_FallsBackToNearest(t *testing.T) {
	waypoints := []StandbyWaypoint{
		{Symbol: "X1-FAR", DemandWeight: 0},
		{Symbol: "X1-NEAR", DemandWeight: 0},
	}
	hulls := []IdleHullToPlace{
		{ShipSymbol: "H-1", Distance: map[string]float64{"X1-FAR": 100, "X1-NEAR": 10}},
	}

	got := DistributeIdleHullsAcrossStandby(hulls, waypoints, nil)

	if got["H-1"] != "X1-NEAR" {
		t.Fatalf("uniform demand must fall back to nearest-station homing, got %v", got)
	}
}

// Ownership: the distribution only ever places the hulls handed to it - it never
// fabricates an assignment for a hull outside the supplied (dedicated-fleet)
// set. Ownership filtering is the caller's job; the mechanism must not widen it.
func TestDistributeIdleHullsAcrossStandby_OnlyPlacesGivenHulls(t *testing.T) {
	waypoints := []StandbyWaypoint{
		{Symbol: "X1-A", DemandWeight: 2},
		{Symbol: "X1-B", DemandWeight: 1},
	}
	hulls := []IdleHullToPlace{
		{ShipSymbol: "DEDICATED-1", Distance: uniformDistance("X1-A", "X1-B")},
		{ShipSymbol: "DEDICATED-2", Distance: uniformDistance("X1-A", "X1-B")},
	}

	got := DistributeIdleHullsAcrossStandby(hulls, waypoints, nil)

	if len(got) != len(hulls) {
		t.Fatalf("expected exactly the %d supplied hulls placed, got %d: %v", len(hulls), len(got), got)
	}
	allowed := map[string]struct{}{"DEDICATED-1": {}, "DEDICATED-2": {}}
	for sym := range got {
		if _, ok := allowed[sym]; !ok {
			t.Fatalf("distribution fabricated an assignment for a hull it was never given: %q", sym)
		}
	}
}
