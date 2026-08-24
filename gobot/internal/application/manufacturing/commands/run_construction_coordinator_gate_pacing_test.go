package commands

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// The gate feed drains a source by buying from it, and nothing made it wait for the source to
// recover. These pin the pacing consult: a source this fleet has recently hammered yields its leg
// to a less-compressed step, the compression is accrued from OUR OWN traded volume so no price can
// ratchet the baseline, and — the property that keeps this from becoming a starvation bug — a leg
// where EVERY step is compressed still feeds the least-compressed one rather than standing down.

// gateSourceFor is the source waypoint the stub topology hands back for a good.
func gateSourceFor(good string) string { return good + "-EXPORTER" }

// pacedLedger is a ledger pre-loaded with `tranches` full trade-volumes of compression on one
// (source, factory, good), as if the fleet had just bought that much there.
func pacedLedger(source, good string, tranches int, at time.Time) *trading.LaneCooldownLedger {
	ledger := trading.NewLaneCooldownLedger(0, 0, 0) // era defaults
	ledger.Accrue(trading.SourceDrainKey(source, good), tranches*gateTestTradeVolume, gateTestTradeVolume, at)
	return ledger
}

// THE BINDING CASE. IRON is the scarcest input and would win the leg outright, but this fleet has
// just drained its source. The pacing consult yields it to QUARTZ_SAND, which is cheaper AND
// untouched — the source recovers while the walk feeds something else.
//
// The fixture saturates past the bound: two full tranches of debt against a bound of one, with
// IRON both affordable and scarcest, so nothing but the pacing consult can move this leg.
func TestFeedGateLeg_YieldsALeggedSourceToAnUntouchedStep(t *testing.T) {
	f := newGateFactoryHandler(t)
	f.topo.importSupply = map[string]string{
		importSupplyKey(gateFactoryWaypoint, "IRON"):        "SCARCE",
		importSupplyKey(gateFactoryWaypoint, "QUARTZ_SAND"): "MODERATE",
	}
	f.topo.priceByGood = map[string]int{"IRON": 23, "QUARTZ_SAND": 23}
	f.handler.SetSourceCooldown(pacedLedger(gateSourceFor("IRON"), "IRON", 2, f.handler.clock.Now()))

	f.runFeed(t, "GF-1")

	feeds := f.feeder.feeds()
	if len(feeds) != 1 {
		t.Fatalf("feeds = %+v; a compressed source must yield its leg, never stand the leg down while another step is feedable", feeds)
	}
	if named := strings.Join(feeds[0].inputs, ","); named != "QUARTZ_SAND" {
		t.Fatalf("fed %q; IRON ranks first on scarcity but its source is freshly drained, so the leg must yield to the untouched QUARTZ_SAND", named)
	}
	// NON-VACUITY: IRON must actually have been considered and deferred, not lost to the ranking.
	if want := "yielding the IRON feed"; !strings.Contains(f.logLines(), want) {
		t.Fatalf("no %q line: IRON was never deferred, so this test proves nothing about pacing:\n%s", want, f.logLines())
	}
}

// THE REGRESSION THAT MATTERS. An untouched source is fed exactly as before — the consult is a
// yield, not a tax, and a fleet that has not been hammering a market must not notice it exists.
func TestFeedGateLeg_AnUntouchedSourceIsUnchanged(t *testing.T) {
	f := newGateFactoryHandler(t)
	f.topo.importSupply = map[string]string{
		importSupplyKey(gateFactoryWaypoint, "IRON"):        "SCARCE",
		importSupplyKey(gateFactoryWaypoint, "QUARTZ_SAND"): "MODERATE",
	}
	f.handler.SetSourceCooldown(trading.NewLaneCooldownLedger(0, 0, 0)) // wired, but nothing traded

	f.runFeed(t, "GF-1")

	feeds := f.feeder.feeds()
	if len(feeds) != 1 {
		t.Fatalf("feeds = %+v; an empty ledger must leave the leg exactly as it was", feeds)
	}
	if named := strings.Join(feeds[0].inputs, ","); named != "IRON" {
		t.Fatalf("fed %q; with no compression recorded the scarcest input must still win the leg", named)
	}
	if strings.Contains(f.logLines(), "yielding the") {
		t.Fatalf("an untouched source must not be yielded:\n%s", f.logLines())
	}
}

// DEFER, NEVER REFUSE — the property that makes this safe to arm. When every feedable step's source
// is compressed there is nothing to yield TO, and standing the leg down would starve the very
// factory the pacing exists to keep supplied. The leg must fall back to the LEAST-compressed step
// and feed it.
func TestFeedGateLeg_EveryStepCompressedStillFeedsTheLeastCompressed(t *testing.T) {
	f := newGateFactoryHandler(t)
	f.topo.importSupply = map[string]string{
		importSupplyKey(gateFactoryWaypoint, "IRON"):        "SCARCE",
		importSupplyKey(gateFactoryWaypoint, "QUARTZ_SAND"): "MODERATE",
	}
	f.topo.priceByGood = map[string]int{"IRON": 23, "QUARTZ_SAND": 23}
	now := f.handler.clock.Now()
	ledger := trading.NewLaneCooldownLedger(0, 0, 0)
	// EVERY step in the plan must be past the bound, or the walk simply takes the one that is not
	// and this proves nothing about the fallback. The plan is IRON->FAB_MATS, QUARTZ_SAND->FAB_MATS
	// and the depth-2 IRON_ORE->IRON, so all three are drained — QUARTZ_SAND the least.
	for _, drained := range []struct {
		good     string
		tranches int
	}{{"IRON", 8}, {"IRON_ORE", 6}, {"QUARTZ_SAND", 3}} {
		ledger.Accrue(trading.SourceDrainKey(gateSourceFor(drained.good), drained.good),
			drained.tranches*gateTestTradeVolume, gateTestTradeVolume, now)
	}
	f.handler.SetSourceCooldown(ledger)

	f.runFeed(t, "GF-1")

	feeds := f.feeder.feeds()
	if len(feeds) != 1 {
		t.Fatalf("feeds = %+v; with every source compressed the leg must still feed the least-compressed step — pacing may never starve the factory", feeds)
	}
	if named := strings.Join(feeds[0].inputs, ","); named != "QUARTZ_SAND" {
		t.Fatalf("fed %q; QUARTZ_SAND carries less compression than IRON, so it is the one the fallback must pick", named)
	}
	if want := "least-compressed"; !strings.Contains(f.logLines(), want) {
		t.Fatalf("the fallback must announce itself — a silent pick here is indistinguishable from the normal path:\n%s", f.logLines())
	}
}

// COMPRESSION IS ACCRUED FROM WHAT WE BOUGHT. Without this the consult reads an empty ledger
// forever and yields nothing; and accruing from OUR OWN volume rather than the observed ask is what
// stops the escalated price from ratcheting its own baseline.
func TestFeedGateLeg_AccruesWhatItBoughtAgainstTheSource(t *testing.T) {
	f := newGateFactoryHandler(t)
	f.topo.importSupply = map[string]string{
		importSupplyKey(gateFactoryWaypoint, "IRON"): "SCARCE",
	}
	ledger := trading.NewLaneCooldownLedger(0, 0, 0)
	f.handler.SetSourceCooldown(ledger)
	key := trading.SourceDrainKey(gateSourceFor("IRON"), "IRON")
	if before := ledger.Debt(key, f.handler.clock.Now()); before != 0 {
		t.Fatalf("fixture is not clean: debt %v before the leg", before)
	}

	f.runFeed(t, "GF-1")

	if after := ledger.Debt(key, f.handler.clock.Now()); after <= 0 {
		t.Fatalf("debt %v after a completed buy; a leg that does not accrue leaves the consult blind and the guard inert", after)
	}
}

// The consult is an optional collaborator like every other on this handler: unwired, the leg is
// byte-identical to what it was, so no fixture that never wires it changes behaviour.
func TestFeedGateLeg_PacingIsInertWhenUnwired(t *testing.T) {
	f := newGateFactoryHandler(t)
	f.topo.importSupply = map[string]string{
		importSupplyKey(gateFactoryWaypoint, "IRON"):        "SCARCE",
		importSupplyKey(gateFactoryWaypoint, "QUARTZ_SAND"): "MODERATE",
	}

	f.runFeed(t, "GF-1")

	feeds := f.feeder.feeds()
	if len(feeds) != 1 || strings.Join(feeds[0].inputs, ",") != "IRON" {
		t.Fatalf("feeds = %+v; with no ledger wired the scarcest input must win exactly as before", feeds)
	}
}

// The bound is one full trade-volume's worth of impact — the model's own unit, not a new number.
// A single tranche does not yield the next leg; a second one, undecayed, does.
func TestLaneCooldownLedger_TrancheDebtIsTheModelsOwnUnit(t *testing.T) {
	ledger := trading.NewLaneCooldownLedger(0, 0, 0)
	key := trading.LaneKey{Source: "S", Dest: "D", Good: "G"}
	now := time.Now()

	if got, want := ledger.TrancheDebt(), trading.DefaultBuyImpactCoefficient+trading.DefaultSellImpactCoefficient; got != want {
		t.Fatalf("TrancheDebt = %v, want %v (one full trade volume's modelled impact)", got, want)
	}
	ledger.Accrue(key, 60, 60, now)
	if debt := ledger.Debt(key, now); debt > ledger.TrancheDebt() {
		t.Fatalf("one full tranche (%v) must not itself exceed the bound (%v) — the first buy is never paced", debt, ledger.TrancheDebt())
	}
	ledger.Accrue(key, 60, 60, now)
	if debt := ledger.Debt(key, now); debt <= ledger.TrancheDebt() {
		t.Fatalf("a second undecayed tranche (%v) must exceed the bound (%v), or nothing is ever paced", debt, ledger.TrancheDebt())
	}
}

// ONE SOURCE, MANY TARGETS is the norm rather than an edge case — IRON has a single exporter and
// four importers; EQUIPMENT and POLYNUCLEOTIDES have five. Keying the pacing consult by the full
// arbitrage lane splits one source's drain across a bucket per target, each sitting under the bound
// while the source is drained n times as fast. The consult must read the SOURCE-aggregate key.
func TestFeedGateLeg_PacingReadsTheSourceAggregateNotThePerTargetLane(t *testing.T) {
	f := newGateFactoryHandler(t)
	f.topo.importSupply = map[string]string{
		importSupplyKey(gateFactoryWaypoint, "IRON"):        "SCARCE",
		importSupplyKey(gateFactoryWaypoint, "QUARTZ_SAND"): "MODERATE",
	}
	f.topo.priceByGood = map[string]int{"IRON": 23, "QUARTZ_SAND": 23}
	ledger := trading.NewLaneCooldownLedger(0, 0, 0)
	// Drain recorded against the SOURCE alone — no destination. This is the shape a boot replay
	// rebuilds, because a purchase row records where we bought and never where we were taking it.
	ledger.Accrue(trading.SourceDrainKey(gateSourceFor("IRON"), "IRON"),
		2*gateTestTradeVolume, gateTestTradeVolume, f.handler.clock.Now())
	f.handler.SetSourceCooldown(ledger)

	f.runFeed(t, "GF-1")

	if named := strings.Join(f.feeder.feeds()[0].inputs, ","); named != "QUARTZ_SAND" {
		t.Fatalf("fed %q; drain recorded against the source alone must yield IRON — a consult keyed per target cannot see it", named)
	}
}

// The converse, and the reason the two keys are kept apart: drain recorded ONLY against a single
// target's lane must NOT pace the source. That entry is the trade engine's, and reading it here is
// exactly the per-target split this fix removes.
func TestFeedGateLeg_PerTargetLaneDebtAloneDoesNotPaceTheSource(t *testing.T) {
	f := newGateFactoryHandler(t)
	f.topo.importSupply = map[string]string{
		importSupplyKey(gateFactoryWaypoint, "IRON"):        "SCARCE",
		importSupplyKey(gateFactoryWaypoint, "QUARTZ_SAND"): "MODERATE",
	}
	ledger := trading.NewLaneCooldownLedger(0, 0, 0)
	ledger.Accrue(trading.LaneKey{Source: gateSourceFor("IRON"), Dest: gateFactoryWaypoint, Good: "IRON"},
		9*gateTestTradeVolume, gateTestTradeVolume, f.handler.clock.Now())
	f.handler.SetSourceCooldown(ledger)

	f.runFeed(t, "GF-1")

	if named := strings.Join(f.feeder.feeds()[0].inputs, ","); named != "IRON" {
		t.Fatalf("fed %q; a full-lane entry is the trade engine's and must not pace the source consult", named)
	}
}

// gateBreadth is the stub listing-breadth cache the depth-conditioned pacing prior reads. A
// waypoint absent from the map is the unreadable-breadth board state.
type gateBreadth map[string]int

func (g gateBreadth) ListingBreadth(_ context.Context, waypoint string) (int, bool) {
	n, ok := g[waypoint]
	return n, ok
}

// hammeredIronLedger is the board a run of the leg's OWN feeding leaves — nothing else writes these
// keys. IRON's exporter carries several full tranches of undecayed drain while QUARTZ_SAND's is
// untouched, so IRON — the scarcest input — is the one an unscaled read yields away.
func hammeredIronLedger(f *gateFeedFixture) *trading.LaneCooldownLedger {
	return pacedLedger(gateSourceFor("IRON"), "IRON", 2, f.handler.clock.Now())
}

// twoInputFixture stages the leg both depth cases share: IRON scarcest and freshly drained,
// QUARTZ_SAND adequate and untouched, priced so nothing but the pacing consult can move the leg.
func twoInputFixture(t *testing.T) *gateFeedFixture {
	t.Helper()
	f := newGateFactoryHandler(t)
	f.topo.importSupply = map[string]string{
		importSupplyKey(gateFactoryWaypoint, "IRON"):        "SCARCE",
		importSupplyKey(gateFactoryWaypoint, "QUARTZ_SAND"): "MODERATE",
	}
	f.topo.priceByGood = map[string]int{"IRON": 23, "QUARTZ_SAND": 23}
	return f
}

// THE BINDING CASE. A source the cache confirms is a deep hub takes a couple of tranches with an
// ask move too small to price against, so pacing off it for hours is the engine diverting its own
// legs onto whatever droplet is untouched while the factory the scarcest input belongs to starves.
// A hub's standing drain reads under the bound and the scarcest input keeps its leg.
func TestFeedGateLeg_DeepHubSourceKeepsItsLeg(t *testing.T) {
	const hubListings = 20
	f := twoInputFixture(t)
	ledger := hammeredIronLedger(f)
	ledger.SetSourceBreadthReader(gateBreadth{gateSourceFor("IRON"): hubListings})
	f.handler.SetSourceCooldown(ledger)

	f.runFeed(t, "GF-1")

	if named := strings.Join(f.feeder.feeds()[0].inputs, ","); named != "IRON" {
		t.Fatalf("fed %q; a confirmed deep hub's standing drain must read under the bound so the scarcest input keeps its leg", named)
	}
	if strings.Contains(f.logLines(), "yielding the IRON feed") {
		t.Fatalf("a deep hub must not be yielded:\n%s", f.logLines())
	}
}

// THE KILL SWITCH. A disabled prior paces the same hub at its full debt, so the leg yields exactly
// as an unscaled read makes it.
func TestFeedGateLeg_DeepHubSourceYieldsUnderTheKillSwitch(t *testing.T) {
	f := twoInputFixture(t)
	ledger := hammeredIronLedger(f)
	ledger.SetSourceDepthScaling(
		trading.SourceDepthScaling{ThinListings: 2, MinDebtScale: 0.2},
		gateBreadth{gateSourceFor("IRON"): 20},
	)
	f.handler.SetSourceCooldown(ledger)

	f.runFeed(t, "GF-1")

	if named := strings.Join(f.feeder.feeds()[0].inputs, ","); named != "QUARTZ_SAND" {
		t.Fatalf("fed %q; a disabled prior must pace the hub at its full debt and yield the leg", named)
	}
}

// THE CLASS THE PROTECTION EXISTS FOR. A source listing one or two goods really is taken off the
// board by a tranche, so it keeps full caution and yields its leg.
func TestFeedGateLeg_ThinSourceYields(t *testing.T) {
	for _, listings := range []int{1, 2} {
		f := twoInputFixture(t)
		ledger := hammeredIronLedger(f)
		ledger.SetSourceBreadthReader(gateBreadth{gateSourceFor("IRON"): listings})
		f.handler.SetSourceCooldown(ledger)

		f.runFeed(t, "GF-1")

		if named := strings.Join(f.feeder.feeds()[0].inputs, ","); named != "QUARTZ_SAND" {
			t.Fatalf("fed %q with a %d-listing source; a thin source must keep full caution", named, listings)
		}
	}
}

// UNREADABLE BREADTH PACES AT THE FULL DEBT. Not merely the same decision: the consult compares
// the debt against a fixed bound, so the value itself has to be the unscaled one.
func TestFeedGateLeg_UnreadableSourceBreadthPacesAtTheFullDebt(t *testing.T) {
	f := twoInputFixture(t)
	ledger := hammeredIronLedger(f)
	// The drained source is the one waypoint the cache cannot answer for.
	ledger.SetSourceBreadthReader(gateBreadth{"SOME-OTHER-MARKET": 20})
	f.handler.SetSourceCooldown(ledger)

	now := f.handler.clock.Now()
	key := trading.SourceDrainKey(gateSourceFor("IRON"), "IRON")
	paced, raw := ledger.PacedDebt(context.Background(), key, now), ledger.Debt(key, now)
	if paced != raw {
		t.Fatalf("paced debt %v against the raw %v; an unreadable market may not buy any relief", paced, raw)
	}

	f.runFeed(t, "GF-1")

	if named := strings.Join(f.feeder.feeds()[0].inputs, ","); named != "QUARTZ_SAND" {
		t.Fatalf("fed %q; unreadable breadth must yield exactly as the full debt does", named)
	}
}

// DEFER, NEVER REFUSE. Every source thin and drained means there is nothing to yield to, and the
// leg must still feed the least-compressed rather than stand down.
func TestFeedGateLeg_DepthPriorKeepsTheLeastCompressedFallback(t *testing.T) {
	f := twoInputFixture(t)
	now := f.handler.clock.Now()
	ledger := trading.NewLaneCooldownLedger(0, 0, 0)
	breadth := gateBreadth{}
	for _, drained := range []struct {
		good     string
		tranches int
	}{{"IRON", 8}, {"IRON_ORE", 6}, {"QUARTZ_SAND", 3}} {
		ledger.Accrue(trading.SourceDrainKey(gateSourceFor(drained.good), drained.good),
			drained.tranches*gateTestTradeVolume, gateTestTradeVolume, now)
		breadth[gateSourceFor(drained.good)] = 1
	}
	ledger.SetSourceBreadthReader(breadth)
	f.handler.SetSourceCooldown(ledger)

	f.runFeed(t, "GF-1")

	feeds := f.feeder.feeds()
	if len(feeds) != 1 {
		t.Fatalf("feeds = %+v; the depth prior may never let pacing starve the factory", feeds)
	}
	if named := strings.Join(feeds[0].inputs, ","); named != "QUARTZ_SAND" {
		t.Fatalf("fed %q; the fallback must still pick the least-compressed step", named)
	}
	if want := "least-compressed"; !strings.Contains(f.logLines(), want) {
		t.Fatalf("the fallback must announce itself:\n%s", f.logLines())
	}
}

// THE TRADE ENGINE STAYS BYTE-IDENTICAL. Its ranking reads the full-lane key, so the leg must keep
// accruing there as well as on the source aggregate. Asserted, not argued.
func TestFeedGateLeg_StillAccruesTheFullLaneTheTradeEngineReads(t *testing.T) {
	f := newGateFactoryHandler(t)
	f.topo.importSupply = map[string]string{importSupplyKey(gateFactoryWaypoint, "IRON"): "SCARCE"}
	ledger := trading.NewLaneCooldownLedger(0, 0, 0)
	f.handler.SetSourceCooldown(ledger)

	f.runFeed(t, "GF-1")

	now := f.handler.clock.Now()
	full := ledger.Debt(trading.LaneKey{Source: gateSourceFor("IRON"), Dest: gateFactoryWaypoint, Good: "IRON"}, now)
	source := ledger.Debt(trading.SourceDrainKey(gateSourceFor("IRON"), "IRON"), now)
	if full <= 0 {
		t.Fatalf("full-lane debt %v; dropping it would silently re-rank the trade engine's lanes", full)
	}
	if source <= 0 {
		t.Fatalf("source-aggregate debt %v; the pacing consult reads this key", source)
	}
}
