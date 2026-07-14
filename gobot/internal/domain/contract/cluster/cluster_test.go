package cluster

import "testing"

// A contract cluster is a FULLY-PARAMETRIZED topology: EVERY waypoint and EVERY
// count is a parameter (bead sp-u9xa). Instantiating a cluster at a different
// waypoint, or with a different number of warehouses / stockers / delivery hulls /
// source hubs, must require ZERO code change. So the SAME constructor, driven by two
// totally different parameter sets, yields exactly the topology asked for — no "J58",
// no fixed counts, nothing baked in.
func TestContractCluster_ReflectsArbitraryParametrizedTopology(t *testing.T) {
	cases := []struct {
		name          string
		id            string
		warehouses    []Element
		stockers      []Element
		deliveryHulls []Element
		sourceHubs    []Element
	}{
		{
			name:          "single co-located unit at one waypoint",
			id:            "cluster-A",
			warehouses:    []Element{{Waypoint: "X1-AA-W1", ShipSymbol: "WH-1"}},
			stockers:      []Element{{Waypoint: "X1-SRC-1", ShipSymbol: "ST-1"}},
			deliveryHulls: []Element{{Waypoint: "X1-AA-W1", ShipSymbol: "DH-1"}},
			sourceHubs:    []Element{{Waypoint: "X1-SRC-1", ShipSymbol: "HUB-1"}},
		},
		{
			name: "different waypoint, 3 warehouses / 5 stockers / 2 hulls — zero code change",
			id:   "cluster-Z",
			warehouses: []Element{
				{Waypoint: "X9-ZZ-9", ShipSymbol: "WA"},
				{Waypoint: "X9-ZZ-9", ShipSymbol: "WB"},
				{Waypoint: "X9-ZZ-9", ShipSymbol: "WC"},
			},
			stockers: []Element{
				{Waypoint: "X9-FAR-1", ShipSymbol: "S1"},
				{Waypoint: "X9-FAR-2", ShipSymbol: "S2"},
				{Waypoint: "X9-FAR-3", ShipSymbol: "S3"},
				{Waypoint: "X9-FAR-4", ShipSymbol: "S4"},
				{Waypoint: "X9-FAR-5", ShipSymbol: "S5"},
			},
			deliveryHulls: []Element{
				{Waypoint: "X9-ZZ-9", ShipSymbol: "DA"},
				{Waypoint: "X9-ZZ-9", ShipSymbol: "DB"},
			},
			sourceHubs: []Element{{Waypoint: "X9-FAR-1", ShipSymbol: "H1"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := NewContractCluster(tc.id, tc.warehouses, tc.stockers, tc.deliveryHulls, tc.sourceHubs)
			if err != nil {
				t.Fatalf("NewContractCluster: %v", err)
			}
			if c.ID() != tc.id {
				t.Errorf("ID = %q, want %q", c.ID(), tc.id)
			}
			assertElements(t, "warehouses", c.Warehouses(), tc.warehouses)
			assertElements(t, "stockers", c.Stockers(), tc.stockers)
			assertElements(t, "deliveryHulls", c.DeliveryHulls(), tc.deliveryHulls)
			assertElements(t, "sourceHubs", c.SourceHubs(), tc.sourceHubs)
		})
	}
}

// A cluster with no destination warehouse localizes nothing — it can own no
// contract's destination geometry — so it is rejected at construction, the single
// structural invariant in an otherwise arbitrary topology.
func TestContractCluster_RejectsClusterWithoutDestinationWarehouse(t *testing.T) {
	_, err := NewContractCluster("cluster-empty", nil, nil, nil, nil)
	if err == nil {
		t.Fatalf("expected error for a cluster with no destination warehouse, got nil")
	}
}

func assertElements(t *testing.T, field string, got, want []Element) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s count = %d, want %d", field, len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s[%d] = %+v, want %+v", field, i, got[i], want[i])
		}
	}
}
