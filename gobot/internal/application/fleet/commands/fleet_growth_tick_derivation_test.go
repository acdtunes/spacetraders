package commands

// THE TICK IS DERIVED, AND THIS FILE IS WHY THAT CLAIM IS CHECKABLE (sp-739gf item 4).
//
// defaultGrowthTickSeconds used to be a bare 900 inherited from the deleted autosizer — a cadence
// sized for a coordinator whose shipyard price walk ran before its guards and cost 14,053 Get
// Shipyard calls. With the walk off the decision path (item 1) the tick can be re-derived from what a
// tick actually costs, and these tests hold the derivation to its inputs so the next lane inherits
// reasoning rather than another number.

import (
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/ship"
)

// THE LOCKSTEP GUARD. The derivation divides by the fleet's shipyard allowance, and this package
// cannot import that float into a const expression — so it carries a milli copy. A copy that drifts
// silently would leave the tick claiming a derivation it no longer has.
func TestGrowthTickDerivationTracksTheYardAllowance(t *testing.T) {
	if growthYardAllowanceMilliReqPerSec != ship.YardBudgetMilliReqPerSec {
		t.Fatalf("the growth tick derives from %d milli-req/s but the shipyard allowance is %d — "+
			"the copy has drifted, so defaultGrowthTickSeconds (%d) is no longer derived from anything",
			growthYardAllowanceMilliReqPerSec, ship.YardBudgetMilliReqPerSec, defaultGrowthTickSeconds)
	}
}

// THE ARITHMETIC ITSELF, stated independently of the const expression that computes it: reads per
// tick divided by growth's share of the allowance. A test that merely re-ran the same expression
// would pin nothing.
func TestGrowthTickIsReadsOverAllowanceShare(t *testing.T) {
	readsPerSecond := float64(growthYardAllowanceShareMilli) / 1000 * (float64(ship.YardBudgetMilliReqPerSec) / 1000)
	want := int(float64(growthTickYardReads) / readsPerSecond)
	if defaultGrowthTickSeconds != want {
		t.Fatalf("defaultGrowthTickSeconds = %d, want %d (= %d reads / (%.2f x %.2f req/s))",
			defaultGrowthTickSeconds, want, growthTickYardReads,
			float64(growthYardAllowanceShareMilli)/1000, float64(ship.YardBudgetMilliReqPerSec)/1000)
	}
}

// AND IT MUST ACTUALLY BE FASTER THAN WHAT IT REPLACES, or item 1 bought nothing. 900 seconds was
// the inherited cadence; the fleet reacts to a shortfall it can afford to serve, not to a walk it no
// longer performs.
func TestGrowthTickIsFasterThanTheInheritedAutosizerCadence(t *testing.T) {
	const inheritedAutosizerCadence = 900
	if defaultGrowthTickSeconds >= inheritedAutosizerCadence {
		t.Fatalf("the derived tick (%ds) is no faster than the bare constant it replaced (%ds)",
			defaultGrowthTickSeconds, inheritedAutosizerCadence)
	}
	// A floor, not a race: a tick under the burst bucket's own refill would outrun the allowance it
	// draws on however the shares are set.
	if defaultGrowthTickSeconds < growthTickYardReads {
		t.Fatalf("the derived tick (%ds) is shorter than one second per read (%d reads) — "+
			"a share that low is not a share", defaultGrowthTickSeconds, growthTickYardReads)
	}
}

// THE ANTI-THRASH WINDOW IS THE LANE SURFACE'S OWN, not a second opinion about how long that surface
// takes to settle. Two independently-tuned windows over one quantity is how the old 3-ticks-times-
// 900-seconds product came about.
func TestShortfallDwellIsTheLaneSurfacesOwnWindow(t *testing.T) {
	if defaultGrowthShortfallDwell != resolveShortfallDwell(0) {
		t.Fatalf("an unconfigured dwell resolves to %v, want the documented %v",
			resolveShortfallDwell(0), defaultGrowthShortfallDwell)
	}
	if defaultGrowthShortfallDwell <= 0 {
		t.Fatalf("the anti-thrash window must be positive, got %v", defaultGrowthShortfallDwell)
	}
	// The window now spans MANY ticks rather than three, which is the point: it is a property of the
	// surface, so a faster tick observes the same shortfall more often without shortening the wait.
	if ticks := defaultGrowthShortfallDwell / (defaultGrowthTickSeconds * time.Second); ticks < 3 {
		t.Fatalf("the dwell spans only %d ticks — it must not have become a tick count again", ticks)
	}
}
