package gate

import "testing"

// The GATE phase buys 4 hulls tagged D/F/F/D. The interleaving matters at every
// PARTIAL-purchase state, which is the state that actually occurs when treasury is
// tight: stopping after two must leave one of each, not two of the same.
func TestNextRole_PurchaseOrderIsDeliveryFactoryFactoryDelivery(t *testing.T) {
	want := []Role{RoleDelivery, RoleFactory, RoleFactory, RoleDelivery}

	delivery, factory := 0, 0
	for i, expected := range want {
		got := NextRole(delivery, factory)
		if got != expected {
			t.Fatalf("hull %d: NextRole(%d, %d) = %v, want %v", i+1, delivery, factory, got, expected)
		}
		if got == RoleDelivery {
			delivery++
		} else {
			factory++
		}
	}
	if delivery != 2 || factory != 2 {
		t.Fatalf("after 4 hulls the mix is %dD/%dF, want 2D/2F", delivery, factory)
	}
}

// The spec pins the mix at every N, not just at 4: 1->1D, 2->1D/1F, 3->1D/2F, 4->2D/2F.
func TestNextRole_MixIsCorrectAtEveryPartialPurchase(t *testing.T) {
	cases := []struct{ n, wantDelivery, wantFactory int }{
		{1, 1, 0},
		{2, 1, 1},
		{3, 1, 2},
		{4, 2, 2},
	}
	for _, tc := range cases {
		delivery, factory := 0, 0
		for i := 0; i < tc.n; i++ {
			if NextRole(delivery, factory) == RoleDelivery {
				delivery++
			} else {
				factory++
			}
		}
		if delivery != tc.wantDelivery || factory != tc.wantFactory {
			t.Fatalf("after %d hull(s) the mix is %dD/%dF, want %dD/%dF",
				tc.n, delivery, factory, tc.wantDelivery, tc.wantFactory)
		}
	}
}

// NextRole must be TOTAL: a count past the 4-hull target (a miscount, a manual
// re-tag) must still answer, and must keep the 2:2 balance rather than degenerating.
func TestNextRole_IsTotalPastTheFourHullTarget(t *testing.T) {
	delivery, factory := 2, 2
	for i := 0; i < 4; i++ {
		if NextRole(delivery, factory) == RoleDelivery {
			delivery++
		} else {
			factory++
		}
	}
	if delivery != 4 || factory != 4 {
		t.Fatalf("after 8 hulls the mix is %dD/%dF, want 4D/4F — the order must keep cycling in balance", delivery, factory)
	}
}

// The tag is the dedicated_fleet column value AND the ClaimShip operation string.
// They must be the same value, so round-tripping cannot drift.
func TestFleetTag_RoundTripsThroughParseFleetTag(t *testing.T) {
	for _, role := range []Role{RoleDelivery, RoleFactory} {
		tag := role.FleetTag()
		if tag == "" {
			t.Fatalf("%v has an empty fleet tag", role)
		}
		got, ok := ParseFleetTag(tag)
		if !ok || got != role {
			t.Fatalf("ParseFleetTag(%q) = (%v, %v), want (%v, true)", tag, got, ok, role)
		}
	}
	if RoleDelivery.FleetTag() == RoleFactory.FleetTag() {
		t.Fatal("the two roles share one fleet tag — ClaimShip would authorize either hull for either drain")
	}
}

// The LEGACY "manufacturing" tag carries NO role (phase 3 re-roles live hulls), but it
// IS a gate fleet tag: the observer counts all three, which is what keeps the ramp
// stopping at gateWorkerTarget instead of over-buying.
func TestIsGateFleetTag_CoversAllThreeTagsButOnlyTwoParseToARole(t *testing.T) {
	for _, tag := range []string{LegacyFleetTag, DeliveryFleetTag, FactoryFleetTag} {
		if !IsGateFleetTag(tag) {
			t.Fatalf("IsGateFleetTag(%q) = false, want true — the observer must count it as a gate worker", tag)
		}
	}
	if _, ok := ParseFleetTag(LegacyFleetTag); ok {
		t.Fatalf("ParseFleetTag(%q) reported a role; a legacy hull carries none until phase 3 re-roles it", LegacyFleetTag)
	}
	for _, tag := range []string{"", "contract", "trade", "purchasing", "warehouse"} {
		if IsGateFleetTag(tag) {
			t.Fatalf("IsGateFleetTag(%q) = true — a foreign or undedicated hull must never read as a gate hull", tag)
		}
	}
}

// RoleFleetTags is what discovery iterates. It must list exactly the ROLE tags and
// must not include the legacy tag, which the drain already discovers by default.
func TestRoleFleetTags_ListsExactlyTheTwoRoleTags(t *testing.T) {
	tags := RoleFleetTags()
	if len(tags) != 2 {
		t.Fatalf("RoleFleetTags() = %v, want exactly the two role tags", tags)
	}
	// TIE THE LITERAL TO THE MAP. RoleFleetTags is HAND-WRITTEN while roleTags is the single source
	// of truth, and nothing in the tree connected the two. A third entry in the map changes
	// ParseFleetTag, FleetTag and the coordinator's routing switch — and every exhaustiveness test
	// that drives RoleFleetTags(), including the one whose header names "a third role added to
	// gate.roleTags" as its regression, would keep passing without ever seeing it. Discovery also
	// uses RoleFleetTags(), so such a role would be tagged, undiscovered, and silently idle.
	if len(tags) != len(roleTags) {
		t.Fatalf("RoleFleetTags() lists %d tag(s) but roleTags holds %d; a role in the map and not in this literal is invisible to discovery and to every test that drives it", len(tags), len(roleTags))
	}
	seen := map[string]bool{}
	for _, tag := range tags {
		if tag == LegacyFleetTag {
			t.Fatalf("RoleFleetTags() includes the legacy tag %q, which the drain already discovers by default", tag)
		}
		if seen[tag] {
			t.Fatalf("RoleFleetTags() = %v contains a duplicate; discovery would claim-check the same pool twice", tags)
		}
		// Equal counts alone would still admit a literal listing two tags the map does not hold.
		// ParseFleetTag searches roleTags by value, so this is the membership half of the bijection.
		if _, ok := ParseFleetTag(tag); !ok {
			t.Fatalf("RoleFleetTags() lists %q, which is not a value in roleTags — discovery would scan a pool no role can ever be claimed under", tag)
		}
		seen[tag] = true
	}
}
