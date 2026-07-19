package depot

import (
	"math"
	"testing"
)

// fakeDistance builds a DistanceBetween from a waypoint->coordinate map, computing the SAME
// Euclidean separation the production graph oracle does (shared.Waypoint.DistanceTo). An
// unmapped waypoint resolves ok=false (uncharted / off-graph), so the selector's fail-open
// path — an unknown position never displaces a placed pick — is exercisable.
func fakeDistance(coords map[string][2]float64) DistanceBetween {
	return func(from, to string) (float64, bool) {
		a, okA := coords[from]
		b, okB := coords[to]
		if !okA || !okB {
			return 0, false
		}
		dx, dy := b[0]-a[0], b[1]-a[1]
		return math.Sqrt(dx*dx + dy*dy), true
	}
}

// The load-bearing element: a depot fulfils a routed contract with the delivery
// hull whose parked hub is NEAREST to the contract's destination. This is what makes a MULTI-hub
// delivery fleet deliver locally for ALL destinations — each contract routes to its cluster's
// nearest hull — instead of shuttling a single [0] hull to every destination (which compressed
// the haul only for destinations adjacent to where that one hull parked). The distance is the
// SAME in-system coordinate separation the rest of the routing ranks by (SelectClosestShip).
//
// Regression-critical: a SINGLE-hull depot returns that hull byte-identically (nearest-of-one is
// that one, the distance oracle never consulted), and a nil distance oracle falls open to config
// order — so an un-wired / uncharted / degraded deployment behaves exactly as before.
func TestContractDepot_SelectsNearestDeliveryHullToDestination(t *testing.T) {
	const dest = "X1-VB74-J58"
	// A 9-cluster-style map fragment: the destination cluster plus off-cluster hubs at
	// increasing distance. Coordinates only — placement is config; the selector reads distance.
	coords := map[string][2]float64{
		dest:         {0, 0},
		"X1-VB74-A1": {100, 0}, // far hub
		"X1-VB74-B2": {40, 0},  // nearer hub
		"X1-VB74-C3": {0, 40},  // equidistant with B2 (both 40 from dest)
	}
	nearest := fakeDistance(coords)

	cases := []struct {
		name          string
		deliveryHulls []Element
		distance      DistanceBetween
		wantOk        bool
		wantShip      string
	}{
		{
			// A hull co-located at the destination is preferred even when config placed it
			// SECOND — nearest wins over config order.
			name: "co-located hull placed second is now PREFERRED (nearest to destination)",
			deliveryHulls: []Element{
				{Waypoint: "X1-VB74-A1", ShipSymbol: "FAR-1"},
				{Waypoint: dest, ShipSymbol: "LOCAL-1"},
			},
			distance: nearest,
			wantOk:   true,
			wantShip: "LOCAL-1",
		},
		{
			name: "among off-cluster hubs the geometrically nearest hub's hull is chosen",
			deliveryHulls: []Element{
				{Waypoint: "X1-VB74-A1", ShipSymbol: "FAR-1"}, // dist 100
				{Waypoint: "X1-VB74-B2", ShipSymbol: "MID-1"}, // dist 40 — nearest
			},
			distance: nearest,
			wantOk:   true,
			wantShip: "MID-1",
		},
		{
			// REGRESSION: a single-hull depot returns that hull unchanged — nearest-of-one is that
			// one, distance not consulted.
			name:          "single hull: returned unchanged (byte-identical, distance not consulted)",
			deliveryHulls: []Element{{Waypoint: "X1-VB74-A1", ShipSymbol: "ONLY-1"}},
			distance:      nearest,
			wantOk:        true,
			wantShip:      "ONLY-1",
		},
		{
			// REGRESSION fail-open: multiple hulls but NO distance oracle (un-wired / degraded) ->
			// config order [0].
			name: "nil distance oracle falls open to config order (first configured)",
			deliveryHulls: []Element{
				{Waypoint: "X1-VB74-A1", ShipSymbol: "FIRST-1"},
				{Waypoint: dest, ShipSymbol: "LOCAL-1"},
			},
			distance: nil,
			wantOk:   true,
			wantShip: "FIRST-1",
		},
		{
			// Determinism: two equidistant hubs keep config order (the first configured wins the
			// tie) so the pick is stable pass-to-pass.
			name: "equidistant hubs break the tie by config order (deterministic)",
			deliveryHulls: []Element{
				{Waypoint: "X1-VB74-B2", ShipSymbol: "TIE-A"}, // dist 40
				{Waypoint: "X1-VB74-C3", ShipSymbol: "TIE-B"}, // dist 40
			},
			distance: nearest,
			wantOk:   true,
			wantShip: "TIE-A",
		},
		{
			// Fail-open per-hull: a hull whose hub is uncharted (ok=false) never displaces a hull
			// with a known, nearer position — so a stale-graph hull can't hijack the route.
			name: "hull with an uncharted hub never displaces a known nearer hull",
			deliveryHulls: []Element{
				{Waypoint: "X1-VB74-UNCHARTED", ShipSymbol: "GHOST-1"}, // ok=false
				{Waypoint: "X1-VB74-B2", ShipSymbol: "KNOWN-1"},        // dist 40
			},
			distance: nearest,
			wantOk:   true,
			wantShip: "KNOWN-1",
		},
		{
			name:          "no delivery hull -> no local delivery available",
			deliveryHulls: nil,
			distance:      nearest,
			wantOk:        false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := NewContractDepot("depot-vb74", warehousesAt(dest), nil, tc.deliveryHulls, nil)
			if err != nil {
				t.Fatalf("build depot: %v", err)
			}
			hull, ok := c.SelectDeliveryHull(dest, tc.distance)
			if ok != tc.wantOk {
				t.Fatalf("SelectDeliveryHull ok = %v, want %v", ok, tc.wantOk)
			}
			if !tc.wantOk {
				return
			}
			if hull.ShipSymbol != tc.wantShip {
				t.Errorf("selected ship %q, want %q", hull.ShipSymbol, tc.wantShip)
			}
		})
	}
}
