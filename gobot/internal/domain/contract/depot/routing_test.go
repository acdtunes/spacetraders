package depot

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

func mustDepot(t *testing.T, id string, warehouseWaypoints ...string) *ContractDepot {
	t.Helper()
	c, err := NewContractDepot(id, warehousesAt(warehouseWaypoints...), nil, nil, nil)
	if err != nil {
		t.Fatalf("build depot %s: %v", id, err)
	}
	return c
}

// The contract engine routes the single active contract to the depot that OWNS its
// destination geometry: the depot whose destination warehouse(s) cover the most of
// the contract's delivery destinations, ties broken by lowest depot id for
// determinism. Every destination is a waypoint PARAMETER — none is hardcoded.
func TestRegistry_RoutesContractToOwningDepot(t *testing.T) {
	central := mustDepot(t, "depot-central", "X1-CENTRAL-1")
	far := mustDepot(t, "depot-far", "X1-FAR-58")
	overlapA := mustDepot(t, "depot-a", "X1-DEST-1", "X1-DEST-2")
	overlapB := mustDepot(t, "depot-b", "X1-DEST-2")

	cases := []struct {
		name         string
		depots       []*ContractDepot
		destinations []string
		wantOwner    string
	}{
		{
			name:         "single-destination contract routes to the covering depot",
			depots:       []*ContractDepot{central, far},
			destinations: []string{"X1-FAR-58"},
			wantOwner:    "depot-far",
		},
		{
			name:         "depot covering MORE of the destinations wins",
			depots:       []*ContractDepot{overlapA, overlapB},
			destinations: []string{"X1-DEST-1", "X1-DEST-2"},
			wantOwner:    "depot-a",
		},
		{
			name:         "equal coverage breaks the tie by lowest depot id (deterministic)",
			depots:       []*ContractDepot{overlapB, mustDepot(t, "depot-a2", "X1-DEST-2")},
			destinations: []string{"X1-DEST-2"},
			wantOwner:    "depot-a2",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := NewRegistry(tc.depots)
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

// REGRESSION safety: when no depot owns a contract's destination — an uncovered
// destination, an empty registry (destination warehousing entirely OFF), or a
// contract with no destinations — RouteContract returns nil, so the caller falls back
// to the legacy long-haul path and behavior is unchanged.
func TestRegistry_ReturnsNilWhenNoDepotOwns(t *testing.T) {
	far := mustDepot(t, "depot-far", "X1-FAR-58")

	cases := []struct {
		name         string
		depots       []*ContractDepot
		destinations []string
	}{
		{name: "uncovered destination", depots: []*ContractDepot{far}, destinations: []string{"X1-UNCOVERED-9"}},
		{name: "empty registry (warehousing OFF)", depots: nil, destinations: []string{"X1-FAR-58"}},
		{name: "contract with no destinations", depots: []*ContractDepot{far}, destinations: nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := NewRegistry(tc.depots)
			if owner := reg.RouteContract(tc.destinations); owner != nil {
				t.Errorf("expected nil (legacy fallback), got %q", owner.ID())
			}
		})
	}
}
