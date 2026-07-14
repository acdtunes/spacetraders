package cluster

import "testing"

// Granular live add / remove / place are the CLI's per-element operations (bead
// sp-u9xa). They are PARAMETRIZED BY ROLE — the caller names which element class
// (warehouse / stocker / delivery hull / source hub) to touch, and no role, waypoint,
// or count is hardcoded. Every operation is an IMMUTABLE functional update: it returns
// a NEW cluster and leaves the receiver untouched, so a durable store can apply an op,
// persist the result, and never corrupt the in-memory registry the contract engine is
// concurrently reading.

func mutBase(t *testing.T) *ContractCluster {
	t.Helper()
	c, err := NewContractCluster("cluster-mut",
		[]Element{{Waypoint: "X1-D-W1", ShipSymbol: "WH-1"}},
		[]Element{{Waypoint: "X1-S-1", ShipSymbol: "ST-1"}},
		[]Element{{Waypoint: "X1-D-W1", ShipSymbol: "DH-1"}},
		[]Element{{Waypoint: "X1-S-1", ShipSymbol: "HUB-1"}},
	)
	if err != nil {
		t.Fatalf("build base cluster: %v", err)
	}
	return c
}

// roleElements reads the slice for a role off a cluster, so the table tests can assert
// against the class they mutated without duplicating the accessor switch.
func roleElements(c *ContractCluster, role Role) []Element {
	switch role {
	case RoleWarehouse:
		return c.Warehouses()
	case RoleStocker:
		return c.Stockers()
	case RoleDeliveryHull:
		return c.DeliveryHulls()
	case RoleSourceHub:
		return c.SourceHubs()
	}
	return nil
}

func TestContractCluster_WithElementAdded_ParametrizedByRole(t *testing.T) {
	// Each role gets a new element appended; the receiver is never mutated.
	for _, role := range []Role{RoleWarehouse, RoleStocker, RoleDeliveryHull, RoleSourceHub} {
		t.Run(role.String(), func(t *testing.T) {
			base := mutBase(t)
			before := len(roleElements(base, role))
			add := Element{Waypoint: "X1-NEW-9", ShipSymbol: "ADDED-1"}

			got, err := base.WithElementAdded(role, add)
			if err != nil {
				t.Fatalf("WithElementAdded(%s): %v", role, err)
			}
			after := roleElements(got, role)
			if len(after) != before+1 {
				t.Fatalf("%s count = %d, want %d", role, len(after), before+1)
			}
			if last := after[len(after)-1]; last != add {
				t.Errorf("appended element = %+v, want %+v", last, add)
			}
			// Immutability: the receiver's slice is unchanged.
			if len(roleElements(base, role)) != before {
				t.Errorf("receiver mutated: %s count changed to %d", role, len(roleElements(base, role)))
			}
		})
	}
}

func TestContractCluster_WithElementRemoved_ByShipSymbol(t *testing.T) {
	base := mutBase(t)

	got, err := base.WithElementRemoved(RoleStocker, "ST-1")
	if err != nil {
		t.Fatalf("WithElementRemoved: %v", err)
	}
	if n := len(got.Stockers()); n != 0 {
		t.Fatalf("stocker count after removal = %d, want 0", n)
	}
	if n := len(base.Stockers()); n != 1 {
		t.Errorf("receiver mutated: base stocker count = %d, want 1", n)
	}
}

func TestContractCluster_WithElementRemoved_UnknownShipErrors(t *testing.T) {
	base := mutBase(t)
	if _, err := base.WithElementRemoved(RoleStocker, "NOPE"); err == nil {
		t.Fatalf("removing an absent ship must error so the CLI reports it")
	}
}

// Removing the LAST warehouse would leave a cluster that localizes nothing — the one
// structural invariant. The mutation must refuse it rather than produce an illegal cluster.
func TestContractCluster_WithElementRemoved_LastWarehouseRefused(t *testing.T) {
	base := mutBase(t)
	if _, err := base.WithElementRemoved(RoleWarehouse, "WH-1"); err == nil {
		t.Fatalf("removing the last warehouse must be refused (cluster would own no destination)")
	}
}

// Place repositions an existing element to a new waypoint (the parametrized positioning
// op — e.g. parking a delivery hull at its warehouse per the analyst's co-location
// policy). It preserves identity + order and never invents co-location: the caller
// supplies the waypoint.
func TestContractCluster_WithElementPlaced_RepositionsExistingElement(t *testing.T) {
	base := mutBase(t)

	got, err := base.WithElementPlaced(RoleDeliveryHull, "DH-1", "X1-MOVED-7")
	if err != nil {
		t.Fatalf("WithElementPlaced: %v", err)
	}
	hulls := got.DeliveryHulls()
	if len(hulls) != 1 || hulls[0].ShipSymbol != "DH-1" || hulls[0].Waypoint != "X1-MOVED-7" {
		t.Fatalf("placed hull = %+v, want DH-1 @ X1-MOVED-7", hulls)
	}
	if base.DeliveryHulls()[0].Waypoint != "X1-D-W1" {
		t.Errorf("receiver mutated: base hull moved to %q", base.DeliveryHulls()[0].Waypoint)
	}
}

func TestContractCluster_WithElementPlaced_UnknownShipErrors(t *testing.T) {
	base := mutBase(t)
	if _, err := base.WithElementPlaced(RoleDeliveryHull, "NOPE", "X1-MOVED-7"); err == nil {
		t.Fatalf("placing an absent ship must error (place repositions an existing element)")
	}
}

func TestRole_ParseRoundTrips(t *testing.T) {
	for _, role := range []Role{RoleWarehouse, RoleStocker, RoleDeliveryHull, RoleSourceHub} {
		got, err := ParseRole(role.String())
		if err != nil {
			t.Fatalf("ParseRole(%q): %v", role.String(), err)
		}
		if got != role {
			t.Errorf("ParseRole(%q) = %v, want %v", role.String(), got, role)
		}
	}
	if _, err := ParseRole("not-a-role"); err == nil {
		t.Fatalf("ParseRole must reject an unknown role name")
	}
}
