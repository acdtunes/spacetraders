package contract

import (
	"context"
	"testing"
)

// A leg's buy must be bounded by the sink's own depth, not only by the hull's hold and
// the per-leg spend cap. The dispatcher reads the sink's trade volume every pass (it
// already feeds the absorption consult); these tests pin that the same number now also
// reaches the arb run as its units cap, so the run cannot commit to a quantity whose
// own sale would drive the bid below the floor that sale arms.

// The binding case: a hull with room and budget to spare, aimed at a THIN sink. Both
// the hold (40) and the spend cap (100k/100 = 1000 units) sit far past the depth bound,
// so the bound is the only term that can decide the size — without it the leg buys the
// full hold into a sink two units deep.
func TestIdleArb_ThinSinkBoundsTheLaunchedUnitsCap(t *testing.T) {
	d, launcher, _, _ := idleArbDepthHarness(t, 2, IdleArbConfig{ReserveHulls: 1}, 2)

	if launched := d.DispatchOnce(context.Background()); launched != 1 {
		t.Fatalf("a profitable thin-sink lane must still fly (bounded, not refused), got %d launches", launched)
	}
	spec := launcher.launches[0]
	// depth 2, a 20% tolerated fall at 1.5% per full depth => 26 units.
	if want := 26; spec.MaxUnits != want {
		t.Fatalf("launched units cap: got %d, want %d (sink depth 2)", spec.MaxUnits, want)
	}
	if spec.MaxUnits >= 40 {
		t.Fatalf("the cap must actually bind below the hull's 40-unit hold, got %d", spec.MaxUnits)
	}
}

// The regression the fix must not break: a sink deep enough to take the whole leg is
// UNCHANGED. The cap is still carried, but it sits above every other term, so the run
// sizes exactly as it did before — the fix must not shrink a healthy buy.
func TestIdleArb_DeepSinkLeavesTheBuyUnchanged(t *testing.T) {
	d, launcher, _, _ := idleArbDepthHarness(t, 2, IdleArbConfig{ReserveHulls: 1}, 200)

	if launched := d.DispatchOnce(context.Background()); launched != 1 {
		t.Fatalf("a deep-sink lane must fly, got %d launches", launched)
	}
	spec := launcher.launches[0]
	// The hull's hold is 40 and the spend cap allows 1000; a cap at or above the hold
	// cannot reduce the buy, which is what "unchanged" means at this seam.
	if spec.MaxUnits < 40 {
		t.Fatalf("a deep sink must not bound the buy below the hull's 40-unit hold, got %d", spec.MaxUnits)
	}
}

// The bound tracks the sink's depth rather than a fixed ceiling: a deeper sink must
// admit strictly more units than a thinner one.
func TestIdleArb_UnitsCapScalesWithSinkDepth(t *testing.T) {
	thin, thinLauncher, _, _ := idleArbDepthHarness(t, 2, IdleArbConfig{ReserveHulls: 1}, 2)
	thick, thickLauncher, _, _ := idleArbDepthHarness(t, 2, IdleArbConfig{ReserveHulls: 1}, 3)

	thin.DispatchOnce(context.Background())
	thick.DispatchOnce(context.Background())

	if len(thinLauncher.launches) != 1 || len(thickLauncher.launches) != 1 {
		t.Fatalf("both lanes must fly: thin %d, thick %d", len(thinLauncher.launches), len(thickLauncher.launches))
	}
	if thickLauncher.launches[0].MaxUnits <= thinLauncher.launches[0].MaxUnits {
		t.Fatalf("a deeper sink must admit more units: depth 2 -> %d, depth 3 -> %d",
			thinLauncher.launches[0].MaxUnits, thickLauncher.launches[0].MaxUnits)
	}
}

// A tighter sell floor tolerates less decay, so it must bound the buy harder — the cap
// and the floor the sale arms move together off the one knob.
func TestIdleArb_TighterSellFloorBoundsTheBuyHarder(t *testing.T) {
	loose, looseLauncher, _, _ := idleArbDepthHarness(t, 2, IdleArbConfig{ReserveHulls: 1, MarginVerifyFraction: 0.80}, 2)
	tight, tightLauncher, _, _ := idleArbDepthHarness(t, 2, IdleArbConfig{ReserveHulls: 1, MarginVerifyFraction: 0.95}, 2)

	loose.DispatchOnce(context.Background())
	tight.DispatchOnce(context.Background())

	if len(looseLauncher.launches) != 1 || len(tightLauncher.launches) != 1 {
		t.Fatalf("both lanes must fly: loose %d, tight %d", len(looseLauncher.launches), len(tightLauncher.launches))
	}
	if tightLauncher.launches[0].MaxUnits >= looseLauncher.launches[0].MaxUnits {
		t.Fatalf("a 95%% sell floor must bound harder than an 80%% one: loose %d, tight %d",
			looseLauncher.launches[0].MaxUnits, tightLauncher.launches[0].MaxUnits)
	}
}

// Unmeasurable sink depth fails CLOSED: no cap the run would read as "uncapped" is ever
// handed out, so a sink whose depth could not be read is not flown at all.
func TestIdleArb_UnmeasurableSinkDepthRefusesTheLane(t *testing.T) {
	d, launcher, _, _ := idleArbDepthHarness(t, 2, IdleArbConfig{ReserveHulls: 1}, 0)

	if launched := d.DispatchOnce(context.Background()); launched != 0 {
		t.Fatalf("a sink of unreadable depth must not be flown, got %d launches", launched)
	}
	if len(launcher.launches) != 0 {
		t.Fatalf("no leg may launch into a sink of unreadable depth, got %d", len(launcher.launches))
	}
}
