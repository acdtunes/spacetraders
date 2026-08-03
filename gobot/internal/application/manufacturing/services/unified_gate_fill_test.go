package services

import (
	"context"
	"testing"
)

// sp-vh1s Part A — the lane-B integration contract. IsUnifiedGateNode is the single
// predicate the per-node gates (input_source_selector, input_price_ceiling — lane B)
// call to decide whether a node runs in MARGIN-BLIND gate mode. It must be true EXACTLY
// when the run's delivery target is a construction site; an unstamped run and a
// resale-sink (profit-factory) run both keep today's gates. This truth table pins that
// contract.
//
// sp-k87tl: this table once had a fourth case pinning "toggle off + construction-site
// target is not a gate node". That case is gone because the toggle is gone — it had no
// config key and no struct field, and its sole stamper passed a literal true next to the
// target, so "toggle off" was never reachable in production. The three cases below are
// the whole reachable space.
//
// It also carried a second assertion on DeliveryTargetFromContext(ctx).IsConstructionSite().
// That was independent only while the fourth case existed (it was the one row where the two
// columns disagreed); without it the expression is verbatim the body of IsUnifiedGateNode, so
// asserting it here could never fail on its own. The target builder and the zero-value default
// are pinned directly by TestDeliveryTargetFromContext_CarriesGateWaypoint below.
func TestIsUnifiedGateNode_TrueExactlyWhenTargetIsConstructionSite(t *testing.T) {
	site := ConstructionSiteTarget("X1-VB74-I55")
	sink := DeliveryTarget{} // zero value == resale sink (profit-factory behavior)

	cases := []struct {
		name         string
		stampTarget  bool
		target       DeliveryTarget
		wantGateNode bool
	}{
		{name: "unstamped context is never a gate node", wantGateNode: false},
		{name: "resale-sink target is not a gate node", stampTarget: true, target: sink, wantGateNode: false},
		{name: "construction-site target IS a gate node", stampTarget: true, target: site, wantGateNode: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.stampTarget {
				ctx = WithDeliveryTarget(ctx, tc.target)
			}

			if got := IsUnifiedGateNode(ctx); got != tc.wantGateNode {
				t.Fatalf("IsUnifiedGateNode = %v, want %v", got, tc.wantGateNode)
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
