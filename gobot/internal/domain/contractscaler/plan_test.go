package contractscaler

import (
	"reflect"
	"testing"
)

func fourParkRoles() EraRoles {
	return EraRoles{
		CentralParks: []string{"P1", "P2", "P3", "P4"},
		FarSources:   []string{"S1", "S2"},
		FarSink:      "J1",
	}
}

// The plan is delivery hulls FIRST (one per central park, ranked demand-desc),
// THEN the central far-source warehouse units, THEN the stocker — the fill order
// the analyst mandated (a naive marginal-$/hr greedy never starts the lumpy
// warehouse bundle).
func TestBuildPlan_DeliveryHullsFirstThenWarehouseThenStocker(t *testing.T) {
	demand := map[string]float64{"P1": 1, "P2": 9, "P3": 5, "P4": 2}
	plan := BuildPlan(fourParkRoles(), demand)

	roles := make([]UnitRole, len(plan))
	targets := make([]string, len(plan))
	for i, u := range plan {
		roles[i] = u.Role
		targets[i] = u.Target
	}

	// 4 delivery + WarehouseUnits + StockerUnits, in that order.
	wantLen := 4 + WarehouseUnits + StockerUnits
	if len(plan) != wantLen {
		t.Fatalf("plan length = %d, want %d (4 delivery + %d warehouse + %d stocker)", len(plan), wantLen, WarehouseUnits, StockerUnits)
	}
	for i := 0; i < 4; i++ {
		if roles[i] != DeliveryHauler {
			t.Fatalf("unit %d role = %v, want DeliveryHauler (delivery hulls lead)", i, roles[i])
		}
	}
	if roles[4] != Warehouse || roles[len(plan)-1] != Stocker {
		t.Fatalf("tail roles = %v, want warehouse(s) then stocker", roles[4:])
	}

	// Delivery hulls spread across DISTINCT parks, highest-demand first.
	wantParks := []string{"P2", "P3", "P4", "P1"}
	if !reflect.DeepEqual(targets[:4], wantParks) {
		t.Fatalf("delivery targets = %v, want demand-ranked distinct parks %v", targets[:4], wantParks)
	}
}

// Delivery-hull count saturates at the number of distinct central waypoints,
// never exceeding MaxDeliveryHulls even with more parks.
func TestBuildPlan_DeliveryHullsCapAtMaxEvenWithManyParks(t *testing.T) {
	parks := make([]string, MaxDeliveryHulls+3)
	demand := map[string]float64{}
	for i := range parks {
		parks[i] = string(rune('A' + i))
		demand[parks[i]] = float64(i)
	}
	plan := BuildPlan(EraRoles{CentralParks: parks}, demand)

	delivery := 0
	for _, u := range plan {
		if u.Role == DeliveryHauler {
			delivery++
		}
	}
	if delivery != MaxDeliveryHulls {
		t.Fatalf("delivery hulls = %d, want the MaxDeliveryHulls cap %d", delivery, MaxDeliveryHulls)
	}
}

// The warehouse + stocker anchor at the central hub (the top-demand park), the
// central far-source-storage insight — NOT at the far J sink (no J depot).
func TestBuildPlan_WarehouseAndStockerAnchorAtCentralHub(t *testing.T) {
	demand := map[string]float64{"P1": 1, "P2": 9, "P3": 5, "P4": 2}
	plan := BuildPlan(fourParkRoles(), demand)

	for _, u := range plan {
		if u.Role == Warehouse || u.Role == Stocker {
			if u.Target != "P2" {
				t.Fatalf("%v target = %q, want the central hub P2 (top-demand park)", u.Role, u.Target)
			}
		}
	}
}

// StandbyDemand carries the per-central-park weight to C1's demand-ranked homing.
func TestBuildPlan_StandbyDemandIsTheParkWeights(t *testing.T) {
	demand := map[string]float64{"P1": 1, "P2": 9, "P3": 5, "P4": 2}
	got := StandbyDemand(fourParkRoles(), demand)

	want := map[string]float64{"P1": 1, "P2": 9, "P3": 5, "P4": 2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("standby demand = %v, want the park weights %v", got, want)
	}
}

func TestTarget_IsPlanCappedByCeiling(t *testing.T) {
	if got := Target(10, 2); got != 2 {
		t.Fatalf("Target(10,2) = %d, want 2 (ceiling caps the plan)", got)
	}
	if got := Target(3, 12); got != 3 {
		t.Fatalf("Target(3,12) = %d, want 3 (plan smaller than ceiling)", got)
	}
	if got := Target(10, 0); got != 0 {
		t.Fatalf("Target(10,0) = %d, want 0 (a zero ceiling buys nothing)", got)
	}
}

// An all-zero (uniform) demand map still yields a deterministic delivery order
// (symbol-stable), so the plan never depends on map iteration order.
func TestBuildPlan_UniformDemandIsSymbolStable(t *testing.T) {
	demand := map[string]float64{"P1": 0, "P2": 0, "P3": 0, "P4": 0}
	plan := BuildPlan(fourParkRoles(), demand)

	got := []string{plan[0].Target, plan[1].Target, plan[2].Target, plan[3].Target}
	want := []string{"P1", "P2", "P3", "P4"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("uniform-demand delivery order = %v, want symbol-stable %v", got, want)
	}
}
