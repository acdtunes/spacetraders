package contractscaler

import (
	"reflect"
	"testing"
)

// A synthetic era whose four anchors are resolved and whose central pool is demand-ranked. The
// symbols are deliberately unlike any universe waypoint symbol — this fixture pins ORDER, and
// nothing here may be mistaken for production knowledge of a numbering.
func anchoredRoles() (EraRoles, map[string]float64) {
	roles := EraRoles{
		CentralParks: []string{"HSTACK", "ESTACK", "RICH", "MID", "POOR", "SPARE1", "SPARE2"},
		Anchors: EraAnchors{
			HStack:        "HSTACK",
			FarSink:       "FARSINK",
			FarSourceBase: "FARBASE",
			EStack:        "ESTACK",
		},
	}
	demand := map[string]float64{"RICH": 90, "MID": 50, "POOR": 10, "HSTACK": 5, "ESTACK": 4, "SPARE1": 3, "SPARE2": 2}
	return roles, demand
}

// THE PLACEMENT ORDER: (1) H-stack, (2) far sink, (3) far source base, (4) E-stack, and only
// then the demand-ranked central remainder. The order is the greedy marginal ranking over the
// era-5 corpus, and it is what a short fleet truncates from the END of — so it must NOT be the
// demand ranking (which would put RICH first and strand the far sink).
func TestTopDeliverySlots_PlacesTheFourAnchorsFirstInPlacementOrder(t *testing.T) {
	roles, demand := anchoredRoles()

	got := TopDeliverySlots(roles, demand)

	want := []string{"HSTACK", "FARSINK", "FARBASE", "ESTACK", "RICH", "MID"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("slots = %v, want the four anchors in placement order then the demand-ranked fill %v", got, want)
	}
}

// FAIL-OPEN, SLOT BY SLOT: whichever anchor this era's template failed to produce degrades to
// the next demand-ranked central park IN PLACE — the other three anchors keep their positions
// and the set keeps its length. A broken template costs one slot's placement quality, never the
// whole set and never a hole.
func TestTopDeliverySlots_AMissingAnchorDegradesToTheCentralSet(t *testing.T) {
	for _, tc := range []struct {
		name  string
		clear func(*EraAnchors)
		want  []string
	}{
		{
			name:  "h_stack missing",
			clear: func(a *EraAnchors) { a.HStack = "" },
			want:  []string{"RICH", "FARSINK", "FARBASE", "ESTACK", "MID", "POOR"},
		},
		{
			name:  "far_sink missing",
			clear: func(a *EraAnchors) { a.FarSink = "" },
			want:  []string{"HSTACK", "RICH", "FARBASE", "ESTACK", "MID", "POOR"},
		},
		{
			name:  "far_source_base missing",
			clear: func(a *EraAnchors) { a.FarSourceBase = "" },
			want:  []string{"HSTACK", "FARSINK", "RICH", "ESTACK", "MID", "POOR"},
		},
		{
			name:  "e_stack missing",
			clear: func(a *EraAnchors) { a.EStack = "" },
			want:  []string{"HSTACK", "FARSINK", "FARBASE", "RICH", "MID", "POOR"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			roles, demand := anchoredRoles()
			tc.clear(&roles.Anchors)

			got := TopDeliverySlots(roles, demand)

			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("slots = %v, want the missing slot filled by the demand-ranked central set %v", got, tc.want)
			}
		})
	}
}

// A central-band anchor IS a central park (era-5 H49 and E42 are charted marketplaces), so the
// demand-ranked fill must not hand a second hull the very waypoint slot 1 or slot 4 already
// took. Every slot is distinct.
func TestTopDeliverySlots_NeverPlacesTwoHullsOnOneWaypoint(t *testing.T) {
	roles, demand := anchoredRoles()

	got := TopDeliverySlots(roles, demand)

	seen := map[string]bool{}
	for _, slot := range got {
		if seen[slot] {
			t.Fatalf("slot %q appears twice in %v — two hulls on one park", slot, got)
		}
		seen[slot] = true
	}
	if len(got) != MaxDeliveryHulls {
		t.Fatalf("slots = %v (%d), want the full MaxDeliveryHulls=%d set", got, len(got), MaxDeliveryHulls)
	}
}

// A central-band anchor is ALSO a central park — era-5 H49 and E42 are charted marketplaces, and
// the resolver deliberately keeps the anchor itself in the pool after pruning its co-located
// siblings. The H-stack is the #1 SOURCE location every era, so it also ranks at the TOP of the
// demand order, which is exactly where the fill starts: without a claim ledger slot 5 hands a
// second hull the waypoint slot 1 is already parked on, and the spread quietly loses a park.
func TestTopDeliverySlots_ATopDemandCentralAnchorIsNotHandedOutTwice(t *testing.T) {
	roles := EraRoles{
		CentralParks: []string{"HSTACK", "ESTACK", "RICH", "MID"},
		Anchors:      EraAnchors{HStack: "HSTACK", FarSink: "FARSINK", FarSourceBase: "FARBASE", EStack: "ESTACK"},
	}
	demand := map[string]float64{"HSTACK": 200, "ESTACK": 150, "RICH": 90, "MID": 50}

	got := TopDeliverySlots(roles, demand)

	want := []string{"HSTACK", "FARSINK", "FARBASE", "ESTACK", "RICH", "MID"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("slots = %v, want %v — a top-demand anchor must not be re-issued as fill", got, want)
	}
}

// The knee still caps the set with the anchors in front — the anchor ordering repositions the
// delivery pool, it does not enlarge it. A thin era (fewer parks than slots) just returns what
// it has, anchors first, with no empty entries.
func TestTopDeliverySlots_CapsAtTheKneeAndShrinksWithATinyEra(t *testing.T) {
	roles, demand := anchoredRoles()
	roles.CentralParks = append(roles.CentralParks, "EXTRA1", "EXTRA2", "EXTRA3")

	if got := TopDeliverySlots(roles, demand); len(got) != MaxDeliveryHulls {
		t.Fatalf("slots = %v (%d), want the MaxDeliveryHulls cap %d", got, len(got), MaxDeliveryHulls)
	}

	thin := EraRoles{CentralParks: []string{"ONLYPARK"}, Anchors: EraAnchors{FarSink: "FARSINK"}}
	got := TopDeliverySlots(thin, map[string]float64{"ONLYPARK": 1})
	if !reflect.DeepEqual(got, []string{"ONLYPARK", "FARSINK"}) {
		t.Fatalf("thin-era slots = %v, want [ONLYPARK FARSINK] — the h_stack miss consumes the one park, no empty entry", got)
	}

	if got := TopDeliverySlots(EraRoles{}, nil); len(got) != 0 {
		t.Fatalf("empty era slots = %v, want none", got)
	}
}

// The warehouse + stocker hub must NOT follow the delivery reordering: it stays the
// highest-demand CENTRAL park (central far-source storage, never the far J sink), even though
// the far sink now leads the delivery slots.
func TestBuildPlan_HubStaysTheTopDemandCentralParkUnderTheAnchorOrdering(t *testing.T) {
	roles, demand := anchoredRoles()

	plan := BuildPlan(roles, demand)

	if plan[0].Target != "HSTACK" || plan[1].Target != "FARSINK" {
		t.Fatalf("delivery targets start %q,%q, want the anchor placement order", plan[0].Target, plan[1].Target)
	}
	for _, unit := range plan {
		if unit.Role != Warehouse && unit.Role != Stocker {
			continue
		}
		if unit.Target != "RICH" {
			t.Fatalf("%v target = %q, want the top-demand CENTRAL park RICH (never the far sink)", unit.Role, unit.Target)
		}
	}
}
