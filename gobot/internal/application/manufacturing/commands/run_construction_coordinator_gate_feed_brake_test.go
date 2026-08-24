package commands

import (
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// THE PER-MATERIAL FEED BRAKE. Every other throttle in the feed engine yields or declines, and the
// never-starve fallback redeems a yield — so a chain an operator wants stopped keeps buying its
// inputs until its bill closes. These pin the one control that is a REFUSAL: a braked root
// contributes no steps at all, and the fallback has nothing of that chain's to fall back to.
//
// Driven through the drain's own seam (feedGateLeg) like the rest of the feed suite.

// brakedFixture is a feed fixture whose ONLY outstanding gate material is the secondary one, with a
// recipe of its own so its chain really does plan steps.
//
// The primary material's bill is closed rather than absent: gateMaterialsNeediestFirst drops a met
// bill, which is how a single-root pass is reached without inventing a pipeline shape the drain
// never sees.
func brakedFixture(t *testing.T) *gateFeedFixture {
	t.Helper()
	f := newGateFactoryHandler(t)
	f.pipeline.delivered[gateMaterialPrimary] = f.pipeline.target[gateMaterialPrimary]
	f.topo.recipes[gateMaterialSecondary] = []string{"ELECTRONICS", "MICROPROCESSORS"}
	return f
}

// compressEverySource loads the ledger past the pacing bound on every source in the fixture, so the
// consult yields every step and only the never-starve fallback can still feed anything.
func compressEverySource(t *testing.T, f *gateFeedFixture, goods ...string) {
	t.Helper()
	ledger := trading.NewLaneCooldownLedger(0, 0, 0)
	now := f.handler.clock.Now()
	for i, good := range goods {
		// Distinct debts so the fallback has a determinate least-compressed pick rather than a tie.
		ledger.Accrue(trading.SourceDrainKey(gateSourceFor(good), good),
			(i+3)*gateTestTradeVolume, gateTestTradeVolume, now)
	}
	f.handler.SetSourceCooldown(ledger)
}

// THE CONTROL. With feeding on auto the fallback DOES buy for this chain even though every source is
// compressed — which is the behaviour the brake below has to be able to stop. Without this the
// braked assertion could pass against a fixture that was never going to feed anything.
func TestFeedGateLeg_CompressedChainStillFeedsWhenFeedingIsOnAuto(t *testing.T) {
	f := brakedFixture(t)
	compressEverySource(t, f, "ELECTRONICS", "MICROPROCESSORS")

	f.runFeed(t, "GF-1")

	if calls := f.buyer.calls(); calls != 1 {
		t.Fatalf("BuyAtTerminalFactory called %d time(s); on auto the never-starve fallback must still feed the least-compressed step, or the braked case below proves nothing", calls)
	}
	if want := "least-compressed"; !strings.Contains(f.logLines(), want) {
		t.Fatalf("the fallback never announced itself, so this fixture does not reach the path the brake must cut:\n%s", f.logLines())
	}
}

// THE REGRESSION THIS BRAKE EXISTS FOR. Feeding is operator-off for the chain and EVERY step is
// yielded by pacing, so the never-starve fallback is the last thing standing between the operator's
// intent and a six-figure feed buy. A braked root contributes nothing to the yielded list, so the
// fallback has nothing to redeem: no buy, no feed, at any depth.
func TestFeedGateLeg_FeedOffSuppressesTheChainEvenOnTheNeverStarveFallback(t *testing.T) {
	f := brakedFixture(t)
	compressEverySource(t, f, "ELECTRONICS", "MICROPROCESSORS")
	f.pipeline.goodOverrides = manufacturing.GoodGatingOverrides{
		gateMaterialSecondary: {Feed: manufacturing.FeedModeOff},
	}

	f.runFeed(t, "GF-1")

	if calls := f.buyer.calls(); calls != 0 {
		t.Fatalf("BuyAtTerminalFactory called %d time(s) for a chain whose feeding is operator-off; the fallback must have no step of that chain to redeem", calls)
	}
	if feeds := f.feeder.feeds(); len(feeds) != 0 {
		t.Fatalf("fed %+v; a braked chain contributes no feed steps at all", feeds)
	}
	if got := f.logLines(); strings.Contains(got, "least-compressed") {
		t.Fatalf("the never-starve fallback still fired for a braked chain:\n%s", got)
	}
	if want := "operator-off"; !strings.Contains(f.logLines(), want) {
		t.Fatalf("the brake held silently — an operator cannot see it holding:\n%s", f.logLines())
	}
}

// THE BRAKE IS ROOT-SCOPED, NOT DEPTH-SCOPED. A chain's deeper steps are the ones the shallow
// declines let through, so a filter that only cut the terminal step would still buy for the chain.
func TestFeedGateLeg_FeedOffSuppressesTheDeepStepsToo(t *testing.T) {
	f := brakedFixture(t)
	// A depth-2 step under the braked root, reached because its own terminal factory is ABUNDANT
	// and both depth-1 steps decline.
	f.topo.recipes["ELECTRONICS"] = []string{"SILICON_CRYSTALS"}
	f.topo.mineable["SILICON_CRYSTALS"] = true
	f.topo.supplyByGood = map[string]string{gateMaterialSecondary: "ABUNDANT"}

	f.pipeline.goodOverrides = nil
	f.runFeed(t, "GF-1")
	if calls := f.buyer.calls(); calls != 1 {
		t.Fatalf("on auto the depth-2 step must be the one this fixture feeds (got %d buy call(s)); otherwise the braked run below proves nothing", calls)
	}
	if fed := f.feeder.feeds(); len(fed) != 1 || fed[0].inputs[0] != "SILICON_CRYSTALS" {
		t.Fatalf("fed %+v; the fixture must reach the DEPTH-2 step, or this test does not test depth", fed)
	}

	f = brakedFixture(t)
	f.topo.recipes["ELECTRONICS"] = []string{"SILICON_CRYSTALS"}
	f.topo.mineable["SILICON_CRYSTALS"] = true
	f.topo.supplyByGood = map[string]string{gateMaterialSecondary: "ABUNDANT"}
	f.pipeline.goodOverrides = manufacturing.GoodGatingOverrides{
		gateMaterialSecondary: {Feed: manufacturing.FeedModeOff},
	}

	f.runFeed(t, "GF-1")

	if calls := f.buyer.calls(); calls != 0 {
		t.Fatalf("BuyAtTerminalFactory called %d time(s) for a depth-2 step under a braked root; the whole chain is suppressed, not just its terminal step", calls)
	}
}

// ONE CHAIN OFF LEAVES THE OTHERS RUNNING. This is the difference between a per-material brake and
// stopping the pipeline: the bill's other materials keep being fed on the same pass.
func TestFeedGateLeg_FeedOffOnOneChainLeavesTheOtherFeeding(t *testing.T) {
	f := newGateFactoryHandler(t)
	f.topo.recipes[gateMaterialSecondary] = []string{"ELECTRONICS", "MICROPROCESSORS"}
	// The braked chain is the NEEDIEST, so it is walked first and would win the leg unbraked —
	// without that the primary chain would be fed either way and the assertion would be vacuous.
	f.pipeline.target[gateMaterialSecondary] = 4 * f.pipeline.target[gateMaterialPrimary]
	f.pipeline.goodOverrides = manufacturing.GoodGatingOverrides{
		gateMaterialSecondary: {Feed: manufacturing.FeedModeOff},
	}

	f.runFeed(t, "GF-1")

	feeds := f.feeder.feeds()
	if len(feeds) != 1 {
		t.Fatalf("feeds = %+v; braking one chain must not stand the leg down while another chain is feedable", feeds)
	}
	fed := feeds[0].inputs[0]
	for _, braked := range []string{"ELECTRONICS", "MICROPROCESSORS"} {
		if fed == braked {
			t.Fatalf("fed %s, an input of the braked chain — suppression must remove the chain, not merely reorder it", fed)
		}
	}
	if feeds[0].waypoint == gateMaterialSecondary+"-EXPORTER" {
		t.Fatalf("fed the braked chain's own factory at %s", feeds[0].waypoint)
	}
}

// sharedStepFixture gives the two gate materials a factory in common: both import IRON, and the IRON
// factory's own IRON_ORE step therefore appears in BOTH plans identically. Every step that targets a
// gate material's own factory is declined (both are ABUNDANT), leaving the shared depth-2 step as the
// only survivor — so what the leg feeds says exactly whether the shared step lived.
func sharedStepFixture(t *testing.T) *gateFeedFixture {
	t.Helper()
	f := newGateFactoryHandler(t)
	f.topo.recipes[gateMaterialSecondary] = []string{"IRON", "ELECTRONICS"}
	f.topo.supplyByGood = map[string]string{
		gateMaterialPrimary:   "ABUNDANT",
		gateMaterialSecondary: "ABUNDANT",
	}
	return f
}

// A STEP TWO CHAINS WANT SURVIVES WHILE EITHER OF THEM IS ON. Suppression removes a REQUESTING ROOT,
// never a step: a factory feeding both a braked chain and a live one is still the live one's
// bottleneck, and cutting it would brake a chain nobody braked.
func TestFeedGateLeg_ASharedStepSurvivesWhileTheOtherChainIsOn(t *testing.T) {
	f := sharedStepFixture(t)
	f.pipeline.goodOverrides = manufacturing.GoodGatingOverrides{
		gateMaterialPrimary: {Feed: manufacturing.FeedModeOff},
	}

	f.runFeed(t, "GF-1")

	feeds := f.feeder.feeds()
	if len(feeds) != 1 {
		t.Fatalf("feeds = %+v; IRON_ORE->IRON is requested by BOTH chains and one of them is still on, so it must be fed", feeds)
	}
	if input := feeds[0].inputs[0]; input != "IRON_ORE" {
		t.Fatalf("fed %s; the shared depth-2 step is the only survivor in this fixture", input)
	}
}

// SUPPRESS ONLY WHEN EVERY REQUESTING ROOT IS OFF. The same shared step, with both chains braked,
// must go — otherwise "shared" would be a loophole that keeps a fully braked pipeline buying.
func TestFeedGateLeg_ASharedStepDiesWhenEveryRequestingChainIsOff(t *testing.T) {
	f := sharedStepFixture(t)
	f.pipeline.goodOverrides = manufacturing.GoodGatingOverrides{
		gateMaterialPrimary:   {Feed: manufacturing.FeedModeOff},
		gateMaterialSecondary: {Feed: manufacturing.FeedModeOff},
	}

	f.runFeed(t, "GF-1")

	if calls := f.buyer.calls(); calls != 0 {
		t.Fatalf("BuyAtTerminalFactory called %d time(s) with every requesting chain braked", calls)
	}
	if feeds := f.feeder.feeds(); len(feeds) != 0 {
		t.Fatalf("fed %+v with every requesting chain braked", feeds)
	}
}

// AUTO IS TODAY'S BEHAVIOUR, EXACTLY. The delivered control ships in auto, so an armed-but-auto
// override must plan the same step the untouched fixture does — pinned against that fixture rather
// than against a restatement of it.
func TestFeedGateLeg_AutoPlansExactlyWhatAnUnsetOverridePlans(t *testing.T) {
	baseline := newGateFactoryHandler(t)
	baseline.runFeed(t, "GF-1")
	want := baseline.feeder.feeds()
	if len(want) != 1 {
		t.Fatalf("baseline fed %+v, want one feed", want)
	}

	for _, mode := range []string{manufacturing.FeedModeAuto, "", "AUTO", " auto "} {
		f := newGateFactoryHandler(t)
		f.pipeline.goodOverrides = manufacturing.GoodGatingOverrides{
			gateMaterialPrimary: {Feed: mode},
		}

		f.runFeed(t, "GF-1")

		got := f.feeder.feeds()
		if len(got) != 1 || got[0].waypoint != want[0].waypoint || got[0].inputs[0] != want[0].inputs[0] {
			t.Fatalf("feed=%q fed %+v; auto must plan exactly what an unset override plans (%+v)", mode, got, want)
		}
		if calls := f.buyer.calls(); calls != baseline.buyer.calls() {
			t.Fatalf("feed=%q made %d buy call(s), baseline made %d", mode, calls, baseline.buyer.calls())
		}
	}
}

// FAIL-SAFE: AN UNREADABLE MODE IS AUTO. A hand-edited row or a value from a newer writer must never
// silently stop feeding a chain — a brake nobody asked for stalls the gate and looks like a bug in
// the drain. Only an explicit off brakes.
func TestFeedGateLeg_AnUnknownFeedModeFeedsExactlyLikeAuto(t *testing.T) {
	f := newGateFactoryHandler(t)
	f.pipeline.goodOverrides = manufacturing.GoodGatingOverrides{
		gateMaterialPrimary: {Feed: "paused-ish"},
	}

	f.runFeed(t, "GF-1")

	if feeds := f.feeder.feeds(); len(feeds) != 1 {
		t.Fatalf("feeds = %+v; an unrecognised feed mode must fall back to auto, never to off", feeds)
	}
	if strings.Contains(f.logLines(), "operator-off") {
		t.Fatalf("an unrecognised mode was treated as a brake:\n%s", f.logLines())
	}
}

// THE BRAKE STOPS BUYING, NOT UNLOADING. A hull that is already full of a braked chain's feedstock
// has nowhere else to put it: the site refuses an input, and no other leg empties a factory hull. So
// the no-free-hold path still delivers what is aboard — it issues no purchase, which is the only
// thing this brake is about, and refusing there would wedge the hull until the brake came off.
func TestFeedGateLeg_FeedOffStillUnloadsAHullThatIsAlreadyFull(t *testing.T) {
	f := newGateFactoryHandler(t)
	f.pipeline.goodOverrides = manufacturing.GoodGatingOverrides{
		gateMaterialPrimary: {Feed: manufacturing.FeedModeOff},
	}
	ship := gateTestFactoryHullFullOf(t, "GF-FULL", gateHoldItem{good: "IRON", units: gateTestHoldCapacity})

	f.runFeedWith(t, ship)

	if calls := f.buyer.calls(); calls != 0 {
		t.Fatalf("the no-free-hold path bought %d time(s); it must never purchase, braked or not", calls)
	}
	feeds := f.feeder.feeds()
	if len(feeds) != 1 {
		t.Fatalf("feeds = %+v; a full hull must still be emptied — braking a chain's BUYING must not strand cargo already paid for", feeds)
	}
}
