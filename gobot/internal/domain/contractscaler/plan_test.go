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

// TopDeliverySlots is the ONE fixed placement set: central parks ranked highest-demand first,
// symbol-stable, capped at MaxDeliveryHulls. Both the scaler buy sequence and the fixed homing
// consume this SAME set (no drift). Demand is an input at arm ONLY.
//
// An era that resolved NO anchors (every durable predicate missed) is exactly this original
// central-only ranking — the whole-set fail-open floor of the anchor ordering.
func TestTopDeliverySlots_RanksDemandDescCappedAtKnee(t *testing.T) {
	demand := map[string]float64{"P1": 1, "P2": 9, "P3": 5, "P4": 2}
	got := TopDeliverySlots(EraRoles{CentralParks: []string{"P1", "P2", "P3", "P4"}}, demand)

	want := []string{"P2", "P3", "P4", "P1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TopDeliverySlots = %v, want demand-ranked %v", got, want)
	}

	// Capped at the MaxDeliveryHulls knee even with more distinct parks.
	parks := make([]string, MaxDeliveryHulls+3)
	manyDemand := map[string]float64{}
	for i := range parks {
		parks[i] = string(rune('A' + i))
		manyDemand[parks[i]] = float64(i)
	}
	if got := TopDeliverySlots(EraRoles{CentralParks: parks}, manyDemand); len(got) != MaxDeliveryHulls {
		t.Fatalf("TopDeliverySlots on %d parks = %d slots, want the MaxDeliveryHulls cap %d", len(parks), len(got), MaxDeliveryHulls)
	}
}

// RoleTargets exposes the per-role fill targets (delivery D / warehouse W / stocker S)
// the role-aware ramp fills IN ORDER, so it can reconcile the existing depot up to each
// role's target rather than treat every plan unit as a delivery buy. A hub-less era (no
// central park) has no warehouse/stocker bundle, so all three are zero.
func TestRoleTargets_CountsEachRoleOfTheFixedPlan(t *testing.T) {
	demand := map[string]float64{}
	sevenParks := make([]string, 7)
	for i := range sevenParks {
		sevenParks[i] = string(rune('A' + i))
		demand[sevenParks[i]] = float64(i)
	}

	// 7 parks + a hub → delivery CAPPED at the MaxDeliveryHulls knee (6, not 7), the full
	// warehouse + stocker bundle.
	if d, w, s := RoleTargets(BuildPlan(EraRoles{CentralParks: sevenParks}, demand)); d != MaxDeliveryHulls || w != WarehouseUnits || s != StockerUnits {
		t.Fatalf("RoleTargets(7 parks) = (%d,%d,%d), want (%d,%d,%d) — delivery capped at the knee", d, w, s, MaxDeliveryHulls, WarehouseUnits, StockerUnits)
	}
	// 0 parks → empty plan (no hull anchors the warehouse bundle) → no roles.
	if d, w, s := RoleTargets(BuildPlan(EraRoles{}, demand)); d != 0 || w != 0 || s != 0 {
		t.Fatalf("RoleTargets(0 parks) = (%d,%d,%d), want (0,0,0)", d, w, s)
	}
	// 3 parks → 3 delivery + the full warehouse + stocker bundle (bundle is hub-gated, not park-scaled).
	if d, w, s := RoleTargets(BuildPlan(EraRoles{CentralParks: []string{"P1", "P2", "P3"}}, demand)); d != 3 || w != WarehouseUnits || s != StockerUnits {
		t.Fatalf("RoleTargets(3 parks) = (%d,%d,%d), want (3,%d,%d)", d, w, s, WarehouseUnits, StockerUnits)
	}
}

// FarSourceGoods is the FIXED, universe-invariant far-source whitelist the contract depot stocks
// (economy-analyst authoritative, st-wisp-2h6r5): the ores / precious metals+stones / drugs — IDENTICAL
// every era, NOT demand-mined, NOT export-derived, NOT value-ranked. DepotTargetUnits pins each at the
// flat per-good buffer cap (~2× a typical ~70-unit contract quantity), never below one delivery.
func TestFarSourceGoods_FixedWhitelistAndFlatCaps(t *testing.T) {
	want := []string{"COPPER_ORE", "IRON_ORE", "ALUMINUM_ORE", "GOLD", "SILVER", "DIAMONDS", "PRECIOUS_STONES", "DRUGS"}
	if !reflect.DeepEqual(FarSourceGoods, want) {
		t.Fatalf("FarSourceGoods = %v, want the fixed 8-symbol far-source set %v", FarSourceGoods, want)
	}
	if DepotUnitsPerGood != 140 {
		t.Fatalf("DepotUnitsPerGood = %d, want 140 (~2× the ~70 typical contract qty; never the useless 40/good)", DepotUnitsPerGood)
	}
	caps := DepotTargetUnits()
	if len(caps) != len(FarSourceGoods) {
		t.Fatalf("DepotTargetUnits has %d goods, want %d (one cap per whitelist good)", len(caps), len(FarSourceGoods))
	}
	for _, good := range FarSourceGoods {
		if caps[good] != DepotUnitsPerGood {
			t.Fatalf("DepotTargetUnits[%s] = %d, want the flat cap DepotUnitsPerGood=%d", good, caps[good], DepotUnitsPerGood)
		}
	}
	// A fresh map each call — a caller mutating its caps never corrupts the fixed definition.
	DepotTargetUnits()["IRON_ORE"] = 1
	if DepotTargetUnits()["IRON_ORE"] != DepotUnitsPerGood {
		t.Fatalf("DepotTargetUnits must return a fresh map each call (the fixed definition is immutable)")
	}
}

// The FULL fixed plan at the delivery knee: delivery caps at MaxDeliveryHulls (6,
// NEVER 8), and the warehouse deepens to one per far-source good (len(FarSourceGoods)=8) — the
// reconcile budget then draws from this depth to fill the live ceiling (ceiling−delivery−stocker).
// The plan CARRIES the full depth cap; the ceiling decides how much of it is bought.
func TestBuildPlan_DeliveryKneeAtSixWarehouseDepthAtFarSourceGoodCount(t *testing.T) {
	parks := make([]string, MaxDeliveryHulls+2) // more parks than the knee → delivery still caps at 6
	demand := map[string]float64{}
	for i := range parks {
		parks[i] = string(rune('A' + i))
		demand[parks[i]] = float64(i)
	}
	plan := BuildPlan(EraRoles{CentralParks: parks}, demand)

	wantLen := MaxDeliveryHulls + len(FarSourceGoods) + StockerUnits // 6 + 8 + 1 = 15
	if len(plan) != wantLen {
		t.Fatalf("plan length = %d, want %d (6 delivery + %d warehouse + 1 stocker)", len(plan), wantLen, len(FarSourceGoods))
	}
	if d, w, s := RoleTargets(plan); d != MaxDeliveryHulls || w != len(FarSourceGoods) || s != StockerUnits {
		t.Fatalf("RoleTargets = (%d,%d,%d), want (6,%d,1) — delivery knee 6, warehouse one-per-far-source-good", d, w, s, len(FarSourceGoods))
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
