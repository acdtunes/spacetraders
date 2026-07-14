package services

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
)

// sp-to2v — fabrication efficiency feeding policy. These pin the PURE decision core the executor's
// delivery/feeding path consults: which goods are feed-responsive (#4), the balanced-to-limiting
// saturation-capped tranche (#2, #3), and the taproot-first ordering (#4a). Verified analyst
// mechanics (HIGH confidence): feeding ALL inputs balanced-to-the-scarcest is the ~4x lever; the
// tranche saturates at ~100-200u/window and <25u does nothing; the deepest limiting input gates
// everything above it; and EQUIPMENT/LAB_INSTRUMENTS/FOOD/MEDICINE do not respond to feeding.

// isFeedResponsive keys on the node's OUTPUT good: the analyst-verified non-responsive set is
// buy-or-skipped (feeding wastes hull-hours), everything else is fed. The default set is applied
// when the policy is stamped with no explicit override.
func TestFeedingPolicy_IsFeedResponsive_DefaultSet(t *testing.T) {
	cfg, engaged := feedingPolicyEngaged(WithFeedingPolicy(context.Background(), 0, 0, nil, false))
	if !engaged {
		t.Fatalf("a stamped, non-disabled policy must be engaged")
	}
	cases := []struct {
		good string
		want bool
	}{
		{"ADVANCED_CIRCUITRY", true}, // verified responder
		{"SHIP_PLATING", true},
		{"SHIP_PARTS", true},
		{"ELECTRONICS", true}, // an intermediate not in the non-responsive set — must stay fed (the recursion needs it)
		{"EQUIPMENT", false},  // verified non-responder — buy-or-skip
		{"LAB_INSTRUMENTS", false},
		{"FOOD", false},
		{"MEDICINE", false},
	}
	for _, c := range cases {
		if got := cfg.isFeedResponsive(c.good); got != c.want {
			t.Errorf("isFeedResponsive(%s) = %v, want %v", c.good, got, c.want)
		}
	}
}

// The non-responsive set is a config knob (RULINGS #5): a custom override replaces the default so
// the analyst can retune which goods are worth feeding live.
func TestFeedingPolicy_IsFeedResponsive_CustomSetOverridesDefault(t *testing.T) {
	cfg, _ := feedingPolicyEngaged(WithFeedingPolicy(context.Background(), 0, 0, []string{"FUEL"}, false))
	if cfg.isFeedResponsive("FUEL") {
		t.Errorf("a custom non-responsive set must mark FUEL buy-or-skip")
	}
	if !cfg.isFeedResponsive("EQUIPMENT") {
		t.Errorf("a custom set REPLACES the default: EQUIPMENT is no longer non-responsive when not listed")
	}
}

// A disabled policy (RULINGS #5 escape hatch) is not engaged — the executor reverts to the greedy
// byte-identical path.
func TestFeedingPolicy_DisabledIsNotEngaged(t *testing.T) {
	if _, engaged := feedingPolicyEngaged(WithFeedingPolicy(context.Background(), 0, 0, nil, true)); engaged {
		t.Fatalf("a disabled policy must not engage")
	}
	if _, engaged := feedingPolicyEngaged(context.Background()); engaged {
		t.Fatalf("an unstamped context must not engage the policy (OFF = byte-identical)")
	}
}

// balancedTranche is the ~4x lever (#2) fused with the saturation cap (#3): the per-input delivery
// this window is sized to the LIMITING (scarcest) input's sourceable flow, clamped into the
// saturation window [min,max]. Piling onto the ample input past the limiting flow is wasted.
func TestFeedingPolicy_BalancedTranche(t *testing.T) {
	cfg, _ := feedingPolicyEngaged(WithFeedingPolicy(context.Background(), 200, 25, nil, false))
	n := func() *goods.SupplyChainNode { return goods.NewSupplyChainNode("X", goods.AcquisitionBuy) }
	cases := []struct {
		name   string
		avails []int
		want   int
	}{
		{"scarce limiter floored to min-effective", []int{1, 48}, 25},   // SILICON scarce=1 gates; below 25-min → 25
		{"mid limiter passes through", []int{60, 80}, 60},               // limiting 60 within window
		{"ample limiter capped at saturation", []int{300, 400}, 200},    // both ample → saturate at 200
		{"unknown excluded from the limiter", []int{-1, 48}, 48},        // one unknown → limiter is the known 48
		{"all unknown → saturation (no balancing)", []int{-1, -1}, 200}, // nothing to balance to → full window
	}
	for _, c := range cases {
		cands := make([]feedCandidate, 0, len(c.avails))
		for _, a := range c.avails {
			cands = append(cands, feedCandidate{child: n(), avail: a})
		}
		if got := balancedTranche(cands, cfg); got != c.want {
			t.Errorf("%s: balancedTranche(%v) = %d, want %d", c.name, c.avails, got, c.want)
		}
	}
}

// orderTaprootFirst (#4a): the scarcest input is the taproot that gates everything above it, so it
// is fed FIRST; a deeper subtree breaks ties (the deepest limiting input); an un-sizeable input
// sorts last (feed what we can measure first).
func TestFeedingPolicy_OrderTaprootFirst_ScarcestFirst(t *testing.T) {
	silicon := goods.NewSupplyChainNode("SILICON_CRYSTALS", goods.AcquisitionBuy)
	copper := goods.NewSupplyChainNode("COPPER", goods.AcquisitionBuy)
	cands := []feedCandidate{{child: copper, avail: 48}, {child: silicon, avail: 1}}

	ordered := orderTaprootFirst(cands)
	if len(ordered) != 2 || ordered[0].child.Good != "SILICON_CRYSTALS" {
		t.Fatalf("the scarcest (SILICON avail 1) must be fed first, got order %v", goodsOf(ordered))
	}
}

func TestFeedingPolicy_OrderTaprootFirst_DeeperBreaksTie(t *testing.T) {
	shallow := goods.NewSupplyChainNode("SHALLOW", goods.AcquisitionBuy) // depth 1
	deep := goods.NewSupplyChainNode("DEEP", goods.AcquisitionFabricate)
	deep.AddChild(goods.NewSupplyChainNode("DEEP_INPUT", goods.AcquisitionBuy)) // depth 2
	cands := []feedCandidate{{child: shallow, avail: 10}, {child: deep, avail: 10}}

	ordered := orderTaprootFirst(cands)
	if ordered[0].child.Good != "DEEP" {
		t.Fatalf("on equal scarcity the DEEPER subtree is the taproot and must be fed first, got %v", goodsOf(ordered))
	}
}

func TestFeedingPolicy_OrderTaprootFirst_UnknownSortsLast(t *testing.T) {
	known := goods.NewSupplyChainNode("KNOWN", goods.AcquisitionBuy)
	unknown := goods.NewSupplyChainNode("UNKNOWN", goods.AcquisitionBuy)
	cands := []feedCandidate{{child: unknown, avail: -1}, {child: known, avail: 99}}

	ordered := orderTaprootFirst(cands)
	if ordered[0].child.Good != "KNOWN" {
		t.Fatalf("an un-sizeable input must sort AFTER a measurable one, got %v", goodsOf(ordered))
	}
}

func goodsOf(cands []feedCandidate) []string {
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.child.Good)
	}
	return out
}
