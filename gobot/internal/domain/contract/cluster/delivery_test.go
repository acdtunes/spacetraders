package cluster

import "testing"

// The load-bearing element: a cluster fulfils a routed contract with its PINNED
// delivery hull, chosen by PURE CONFIGURATION (the config-assigned hull — first in
// config order). The mechanism does NOT prefer, favor, or special-case a hull that
// happens to be co-located at the destination: co-location is whatever the config
// produced, never a built-in selection rule. A hull the analyst parked at the
// destination delivers locally (~0 haul), but ONLY when the config placed it first;
// a co-located hull placed second is NOT promoted over the config-assigned first
// hull. No delivery hull -> no local delivery available (ok=false) and the caller
// keeps the long haul. The delivered outcome (co-located or not) is observable via
// the returned hull's waypoint.
func TestContractCluster_SelectsDeliveryHullPurelyByConfig(t *testing.T) {
	const dest = "X1-FAR-58"

	cases := []struct {
		name          string
		deliveryHulls []Element
		wantOk        bool
		wantShip      string
		wantColocated bool
	}{
		{
			// The anti-preference pin: a co-located hull exists but is placed SECOND.
			// Pure-config selection returns the FIRST configured hull (off-site) and
			// does NOT promote the co-located one — proving co-location is not favored.
			name: "co-located hull placed second is NOT preferred over the config-assigned first hull",
			deliveryHulls: []Element{
				{Waypoint: "X1-ELSEWHERE-1", ShipSymbol: "OFF-1"},
				{Waypoint: dest, ShipSymbol: "LOCAL-1"},
			},
			wantOk:        true,
			wantShip:      "OFF-1",
			wantColocated: false,
		},
		{
			// Co-location is honored ONLY because the config placed the local hull
			// first — it is selected per config order, not because it is co-located.
			name: "config-assigned first hull that happens to be co-located is selected (co-location incidental)",
			deliveryHulls: []Element{
				{Waypoint: dest, ShipSymbol: "LOCAL-1"},
				{Waypoint: "X1-ELSEWHERE-1", ShipSymbol: "OFF-1"},
			},
			wantOk:        true,
			wantShip:      "LOCAL-1",
			wantColocated: true,
		},
		{
			name: "single off-site hull: returned exactly per config, never deprioritized",
			deliveryHulls: []Element{
				{Waypoint: "X1-ELSEWHERE-1", ShipSymbol: "OFF-1"},
			},
			wantOk:        true,
			wantShip:      "OFF-1",
			wantColocated: false,
		},
		{
			name:          "no delivery hull -> no local delivery available",
			deliveryHulls: nil,
			wantOk:        false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := NewContractCluster("cluster-far", warehousesAt(dest), nil, tc.deliveryHulls, nil)
			if err != nil {
				t.Fatalf("build cluster: %v", err)
			}
			hull, ok := c.SelectDeliveryHull(dest)
			if ok != tc.wantOk {
				t.Fatalf("SelectDeliveryHull ok = %v, want %v", ok, tc.wantOk)
			}
			if !tc.wantOk {
				return
			}
			if hull.ShipSymbol != tc.wantShip {
				t.Errorf("selected ship %q, want %q", hull.ShipSymbol, tc.wantShip)
			}
			if colocated := hull.Waypoint == dest; colocated != tc.wantColocated {
				t.Errorf("co-located = %v (hull at %q), want %v", colocated, hull.Waypoint, tc.wantColocated)
			}
		})
	}
}
