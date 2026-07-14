package services

import (
	"context"
	"testing"
)

// sp-vh1s Part A — the lane-B integration contract. IsUnifiedGateNode is the single
// predicate the per-node gates (input_source_selector, input_price_ceiling — lane B)
// call to decide whether a node runs in MARGIN-BLIND gate mode. It must be true ONLY
// when the toggle is ON *and* the run's delivery target is a construction site; every
// other combination (toggle off, or a resale-sink run) keeps today's gates, so an
// OFF fleet is byte-identical. This truth table pins that contract.
func TestIsUnifiedGateNode_TrueOnlyWhenToggleOnAndTargetIsConstructionSite(t *testing.T) {
	site := ConstructionSiteTarget("X1-VB74-I55")
	sink := DeliveryTarget{} // zero value == resale sink (unchanged behavior)

	cases := []struct {
		name           string
		stampToggle    bool
		toggle         bool
		stampTarget    bool
		target         DeliveryTarget
		wantGateNode   bool
		wantIsConstSit bool
	}{
		{name: "unstamped context is never a gate node", wantGateNode: false},
		{name: "toggle on but resale-sink target is not a gate node", stampToggle: true, toggle: true, stampTarget: true, target: sink, wantGateNode: false},
		{name: "toggle off with construction-site target is not a gate node", stampToggle: true, toggle: false, stampTarget: true, target: site, wantGateNode: false, wantIsConstSit: true},
		{name: "toggle on with construction-site target IS a gate node", stampToggle: true, toggle: true, stampTarget: true, target: site, wantGateNode: true, wantIsConstSit: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.stampToggle {
				ctx = WithUnifiedGateFill(ctx, tc.toggle)
			}
			if tc.stampTarget {
				ctx = WithDeliveryTarget(ctx, tc.target)
			}

			if got := IsUnifiedGateNode(ctx); got != tc.wantGateNode {
				t.Fatalf("IsUnifiedGateNode = %v, want %v", got, tc.wantGateNode)
			}
			if got := DeliveryTargetFromContext(ctx).IsConstructionSite(); got != tc.wantIsConstSit {
				t.Fatalf("DeliveryTargetFromContext().IsConstructionSite() = %v, want %v", got, tc.wantIsConstSit)
			}
		})
	}
}

// The construction-site target carries the gate waypoint through the run context so the
// terminal switch (run_factory_coordinator) knows WHERE to deliver the root output; a
// zero (sink) target carries none. This is the second half of the lane-B/terminal contract.
func TestDeliveryTargetFromContext_CarriesGateWaypoint(t *testing.T) {
	ctx := WithDeliveryTarget(context.Background(), ConstructionSiteTarget("X1-VB74-I55"))
	target := DeliveryTargetFromContext(ctx)

	if !target.IsConstructionSite() {
		t.Fatal("a construction-site target must report IsConstructionSite() == true")
	}
	if target.SiteWaypoint() != "X1-VB74-I55" {
		t.Fatalf("expected the gate waypoint X1-VB74-I55 on the target, got %q", target.SiteWaypoint())
	}

	// An unstamped context defaults to a resale sink carrying no waypoint (unchanged behavior).
	sink := DeliveryTargetFromContext(context.Background())
	if sink.IsConstructionSite() || sink.SiteWaypoint() != "" {
		t.Fatalf("an unstamped context must default to a resale sink with no waypoint, got %+v", sink)
	}
}
