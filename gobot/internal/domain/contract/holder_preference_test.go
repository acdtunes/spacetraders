package contract

import "testing"

// The live case behind sp-5jce2, era 5, X1-KP23: contract cms41jtz0 needed 19
// ASSAULT_RIFLES at J56 sourced from E42. The incumbent held a PARTIAL load and
// sat 673.3 units from the source (it had ended its cycle standing on the
// delivery); two idle, fuelled, empty contract hulls sat 39.0 and 41.9 away. The
// unconditional sp-zve2q rule picked the incumbent, buying a ~1,346-unit round
// trip where ~712 would do — every cycle.
func liveJ56Case() HolderPlacement {
	return HolderPlacement{
		Holder:              "TORWIND-7",
		HeldUnits:           8,
		UnitsNeeded:         19,
		HolderSourceDist:    673.3,
		HolderAtDestination: true,
		NearestHull:         "TORWIND-5",
		NearestSourceDist:   39.0,
	}
}

func TestWeighHolderAgainstSource(t *testing.T) {
	cases := []struct {
		name             string
		placement        HolderPlacement
		wantDeliverFirst bool
		why              string
	}{
		{
			name:             "live J56 case: barely-loaded holder far from source, near hull decisively closer",
			placement:        liveJ56Case(),
			wantDeliverFirst: true,
			why:              "this is the measured defect — a 673.3-vs-39.0 gap on a 8-of-19 partial must split the cycle",
		},
		{
			name: "holder's load already covers the requirement — its run makes no source trip at all",
			placement: func() HolderPlacement {
				p := liveJ56Case()
				p.HeldUnits = 19
				return p
			}(),
			wantDeliverFirst: false,
			why:              "purchase needs compute to zero, so the holder delivers where it stands for free — nothing can beat that",
		},
		{
			name: "holder's load is nearly complete — topping up beats splitting the cycle",
			placement: func() HolderPlacement {
				p := liveJ56Case()
				p.HeldUnits = 15 // 15/19 ≈ 79%, past the nearly-complete band
				return p
			}(),
			wantDeliverFirst: false,
			why:              "at >=75% held the remaining buy is small; churning the assignment is not worth an extra run",
		},
		{
			name: "near-tie on source distance — hold the assignment steady, do not thrash",
			placement: func() HolderPlacement {
				p := liveJ56Case()
				p.HolderSourceDist = 41.9
				p.NearestSourceDist = 39.0
				return p
			}(),
			wantDeliverFirst: false,
			why:              "a 2.9-unit edge must not flip the assignment between two hulls pass after pass",
		},
		{
			name: "decisively closer by ratio but the absolute saving is trivial",
			placement: func() HolderPlacement {
				p := liveJ56Case()
				p.HolderSourceDist = 20.0
				p.NearestSourceDist = 5.0
				return p
			}(),
			wantDeliverFirst: false,
			why:              "4x closer is meaningless when both hulls are effectively on top of the source",
		},
		{
			name: "no idle hull available — fall back to the incumbent",
			placement: func() HolderPlacement {
				p := liveJ56Case()
				p.NearestHull = ""
				p.NearestSourceDist = 0
				return p
			}(),
			wantDeliverFirst: false,
			why:              "with nothing to hand the sourcing run to, the holder must keep it (RULINGS #1: never a skip)",
		},
		{
			name: "holder is NOT standing on the delivery — never strand its partial load",
			placement: func() HolderPlacement {
				p := liveJ56Case()
				p.HolderAtDestination = false
				return p
			}(),
			wantDeliverFirst: false,
			why:              "its units cannot be registered without travel, so handing the run away would orphan them (sp-1pf0r double-load)",
		},
		{
			name:             "no holder at all — the short-circuit never fired",
			placement:        HolderPlacement{NearestHull: "TORWIND-5", UnitsNeeded: 19},
			wantDeliverFirst: false,
			why:              "ordinary source-nearest selection already applies",
		},
		{
			name: "nothing left to source",
			placement: func() HolderPlacement {
				p := liveJ56Case()
				p.UnitsNeeded = 0
				return p
			}(),
			wantDeliverFirst: false,
			why:              "no requirement means no sourcing run to weigh",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := WeighHolderAgainstSource(tc.placement)
			if got.DeliverHeldFirst != tc.wantDeliverFirst {
				t.Fatalf("DeliverHeldFirst = %v, want %v (%s); reason given: %q",
					got.DeliverHeldFirst, tc.wantDeliverFirst, tc.why, got.Reason)
			}
			if got.Reason == "" {
				t.Fatalf("every decision must carry a reason so the coordinator can log WHY it went either way")
			}
		})
	}
}

// The margin is what keeps the preference from oscillating: a holder that is
// closer than the margin allows must KEEP the run, and the boundary itself must
// resolve to keep (fail-closed onto sp-zve2q).
func TestWeighHolderAgainstSource_MarginBoundaryKeepsTheHolder(t *testing.T) {
	p := liveJ56Case()
	p.NearestSourceDist = 200.0
	p.HolderSourceDist = 200.0 * HolderProximityMargin // exactly at the margin

	if got := WeighHolderAgainstSource(p); !got.DeliverHeldFirst {
		t.Fatalf("exactly AT the margin (and well past the absolute floor) should split; got keep: %q", got.Reason)
	}

	p.HolderSourceDist -= 0.1 // a hair inside the margin
	if got := WeighHolderAgainstSource(p); got.DeliverHeldFirst {
		t.Fatalf("inside the proximity margin the holder must keep the run so the assignment cannot thrash")
	}
}
