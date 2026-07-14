package cluster

import "testing"

// warehousesAt builds destination-warehouse elements at the given waypoints — a tiny
// helper so the routing tests read as topology, not plumbing.
func warehousesAt(waypoints ...string) []Element {
	var whs []Element
	for _, w := range waypoints {
		whs = append(whs, Element{Waypoint: w})
	}
	return whs
}

func mustCluster(t *testing.T, id string, warehouseWaypoints ...string) *ContractCluster {
	t.Helper()
	c, err := NewContractCluster(id, warehousesAt(warehouseWaypoints...), nil, nil, nil)
	if err != nil {
		t.Fatalf("build cluster %s: %v", id, err)
	}
	return c
}

// The contract engine routes the single active contract to the cluster that OWNS its
// destination geometry: the cluster whose destination warehouse(s) cover the most of
// the contract's delivery destinations, ties broken by lowest cluster id for
// determinism. Every destination is a waypoint PARAMETER — none is hardcoded.
func TestRegistry_RoutesContractToOwningCluster(t *testing.T) {
	central := mustCluster(t, "cluster-central", "X1-CENTRAL-1")
	far := mustCluster(t, "cluster-far", "X1-FAR-58")
	overlapA := mustCluster(t, "cluster-a", "X1-DEST-1", "X1-DEST-2")
	overlapB := mustCluster(t, "cluster-b", "X1-DEST-2")

	cases := []struct {
		name         string
		clusters     []*ContractCluster
		destinations []string
		wantOwner    string
	}{
		{
			name:         "single-destination contract routes to the covering cluster",
			clusters:     []*ContractCluster{central, far},
			destinations: []string{"X1-FAR-58"},
			wantOwner:    "cluster-far",
		},
		{
			name:         "cluster covering MORE of the destinations wins",
			clusters:     []*ContractCluster{overlapA, overlapB},
			destinations: []string{"X1-DEST-1", "X1-DEST-2"},
			wantOwner:    "cluster-a",
		},
		{
			name:         "equal coverage breaks the tie by lowest cluster id (deterministic)",
			clusters:     []*ContractCluster{overlapB, mustCluster(t, "cluster-a2", "X1-DEST-2")},
			destinations: []string{"X1-DEST-2"},
			wantOwner:    "cluster-a2",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := NewRegistry(tc.clusters)
			owner := reg.RouteContract(tc.destinations)
			if owner == nil {
				t.Fatalf("expected owner %q, got nil", tc.wantOwner)
			}
			if owner.ID() != tc.wantOwner {
				t.Errorf("routed to %q, want %q", owner.ID(), tc.wantOwner)
			}
		})
	}
}

// REGRESSION safety: when no cluster owns a contract's destination — an uncovered
// destination, an empty registry (destination warehousing entirely OFF), or a
// contract with no destinations — RouteContract returns nil, so the caller falls back
// to the legacy long-haul path and behavior is unchanged.
func TestRegistry_ReturnsNilWhenNoClusterOwns(t *testing.T) {
	far := mustCluster(t, "cluster-far", "X1-FAR-58")

	cases := []struct {
		name         string
		clusters     []*ContractCluster
		destinations []string
	}{
		{name: "uncovered destination", clusters: []*ContractCluster{far}, destinations: []string{"X1-UNCOVERED-9"}},
		{name: "empty registry (warehousing OFF)", clusters: nil, destinations: []string{"X1-FAR-58"}},
		{name: "contract with no destinations", clusters: []*ContractCluster{far}, destinations: nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := NewRegistry(tc.clusters)
			if owner := reg.RouteContract(tc.destinations); owner != nil {
				t.Errorf("expected nil (legacy fallback), got %q", owner.ID())
			}
		})
	}
}
