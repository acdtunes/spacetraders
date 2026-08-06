package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	mfgServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing/gate"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// THE FACTORY ROLE'S LEG. It feeds INPUTS into the factories that export the gate materials and
// never touches their output — that boundary is the whole two-fleet design.
//
// Driven through the drain's own seam (feedGateLeg), never through gate.PlanFeed, which has its
// own package tests.

// stubFactoryTopology answers all three role questions the feed leg asks: the exporter of a good
// (which doubles as the RAW SOURCE role, per phase 1), and the recipe seam.
//
// Its IsRaw/Inputs reproduce GateTopology's post-sp-4irrr biconditional exactly — Inputs returns
// nil for anything IsRaw calls raw — because the leg's termination rests on it.
type stubFactoryTopology struct {
	mu           sync.Mutex
	recipes      map[string][]string
	mineable     map[string]bool
	supplyByGood map[string]string
	errByGood    map[string]error
	// priceByGood overrides the default ask per good, so a fixture can reproduce the price SPREAD
	// that the affordability decline turns on. A single flat price cannot: every step would cost the
	// same, and "declined the expensive one" would be indistinguishable from "declined the first
	// one", which is the very confusion sp-9eor3 is about.
	priceByGood map[string]int
	// volumeByGood overrides the per-transaction trade volume. Needed to exercise the precheck's
	// minimum-tranche pricing at all: the default fixture volume is below the min-effective floor,
	// so min(units, volume) is already under it and the two pricings coincide.
	volumeByGood map[string]int
	// importSupply is the destination factory's IMPORT supply of an input, keyed
	// "FACTORY-WAYPOINT|INPUT" — the third quantity sp-q9um6 ranks on, deliberately NOT derivable
	// from supplyByGood. supplyByGood is what TerminalFactory reports (a market's EXPORT supply of
	// its own output); conflating the two in the fixture would make the tests pass for a
	// implementation that ranked on the wrong quantity, which is the exact failure sp-q9um6 warns
	// about. An absent key means UNREADABLE, which is the cold-cache default.
	importSupply map[string]string
	asked        []string
	// importsAsked records every ImportSupply lookup, kept apart from `asked` so a test asserting
	// which goods were resolved as SOURCES is not polluted by ranking reads.
	importsAsked []string
}

// importSupplyKey names one (factory, input) pair in the fixture's import table.
func importSupplyKey(factoryWaypoint, good string) string { return factoryWaypoint + "|" + good }

func (s *stubFactoryTopology) ImportSupply(_ context.Context, factoryWaypoint, good string, _ int) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.importsAsked = append(s.importsAsked, importSupplyKey(factoryWaypoint, good))
	level, ok := s.importSupply[importSupplyKey(factoryWaypoint, good)]
	if !ok {
		return "", false // unscanned: no basis to rank
	}
	return level, true
}

func (s *stubFactoryTopology) importsAskedList() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.importsAsked...)
}

func newStubFactoryTopology() *stubFactoryTopology {
	return &stubFactoryTopology{
		// FAB_MATS = {IRON, QUARTZ_SAND}; IRON = {IRON_ORE}. QUARTZ_SAND and IRON_ORE are raw.
		// gateMaterialSecondary is deliberately ABSENT from recipes, so IsRaw calls it raw and
		// PlanFeed yields no steps for it — the walk under test is the primary material's.
		recipes:  map[string][]string{gateMaterialPrimary: {"IRON", "QUARTZ_SAND"}, "IRON": {"IRON_ORE"}},
		mineable: map[string]bool{"QUARTZ_SAND": true, "IRON_ORE": true},
	}
}

func (s *stubFactoryTopology) IsRaw(good string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mineable[good] {
		return true
	}
	inputs, ok := s.recipes[good]
	return !ok || len(inputs) == 0
}

func (s *stubFactoryTopology) Inputs(good string) []string {
	if s.IsRaw(good) {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	inputs := s.recipes[good]
	out := make([]string, len(inputs))
	copy(out, inputs)
	return out
}

func (s *stubFactoryTopology) TerminalFactory(_ context.Context, good, _ string, _ int) (*mfgServices.MarketLocatorResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked = append(s.asked, good)
	if err, ok := s.errByGood[good]; ok {
		return nil, err
	}
	supply := "MODERATE"
	if level, ok := s.supplyByGood[good]; ok {
		supply = level
	}
	price := gateTestInputPrice
	if quote, ok := s.priceByGood[good]; ok {
		price = quote
	}
	volume := gateTestTradeVolume
	if v, ok := s.volumeByGood[good]; ok {
		volume = v
	}
	return &mfgServices.MarketLocatorResult{
		WaypointSymbol: good + "-EXPORTER",
		Supply:         supply,
		Price:          price,
		TradeVolume:    volume,
	}, nil
}

// gateTestInputPrice is the default ask every input quotes in this fixture.
const gateTestInputPrice = 100

func (s *stubFactoryTopology) goodsAsked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.asked...)
}

// recordingFeeder records every factory feed: where it went and what it carried.
type recordingFeeder struct {
	mu    sync.Mutex
	calls []feedCall
	err   error
	units int
	// tookNothing makes the feed report UnitsDelivered 0 while refusing nothing — the trip
	// happened, the market took none of it. A plain `units: 0` cannot express this: 0 is the zero
	// value and falls through to the 40-unit default below, so without this flag the outcome the
	// sp-kdsrh fail-closed path actually produces is unreachable from any test.
	tookNothing bool
}

type feedCall struct {
	waypoint string
	inputs   []string
}

func (f *recordingFeeder) FeedFactory(_ context.Context, _ *navigation.Ship, destination *mfgServices.MarketLocatorResult, inputs []string, _ int, _ *shared.OperationContext) (*mfgServices.FeedResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if destination == nil || destination.WaypointSymbol == "" {
		return nil, errors.New("feed attempted without a resolved destination")
	}
	f.calls = append(f.calls, feedCall{waypoint: destination.WaypointSymbol, inputs: append([]string(nil), inputs...)})
	units := f.units
	if units == 0 {
		units = 40
	}
	if f.tookNothing {
		units = 0
	}
	return &mfgServices.FeedResult{WaypointSymbol: destination.WaypointSymbol, UnitsDelivered: units}, nil
}

func (f *recordingFeeder) feeds() []feedCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]feedCall(nil), f.calls...)
}

// gateFeedFixture is one wired FACTORY-role drain plus every seam a test asserts on.
type gateFeedFixture struct {
	*gateLegFixture
	topo   *stubFactoryTopology
	feeder *recordingFeeder
	// lastLot is the lot the most recent runFeed staged. The task status a leg persisted is keyed
	// by task ID (drainStubTaskRepo.updated is a MAP, not an ordered log), so a test asserting
	// "this leg parked its task" has to name the task it staged.
	lastLot constructionLot
}

func newGateFactoryHandler(t *testing.T) *gateFeedFixture {
	t.Helper()
	base := newGateDeliveryHandler(t)
	topo := newStubFactoryTopology()
	feeder := &recordingFeeder{}
	base.handler.SetGateFactory(topo, base.buyer, feeder)
	return &gateFeedFixture{gateLegFixture: base, topo: topo, feeder: feeder}
}

// gateFactoryLot is an EXECUTING lot claimed under the FACTORY tag — the state
// claimTaskForSupply leaves behind before supplyTask routes to a leg.
func gateFactoryLot(t *testing.T, ship *navigation.Ship) constructionLot {
	t.Helper()
	lot := gateTestLot(t, ship)
	lot.claimIdentity = gate.FactoryFleetTag
	return lot
}

func (f *gateFeedFixture) runFeed(t *testing.T, hull string) bool {
	t.Helper()
	return f.runFeedWith(t, gateTestHull(t, hull, gate.FactoryFleetTag))
}

// runFeedWith drives one FACTORY leg with a caller-supplied hull, so a test can stage a hold.
func (f *gateFeedFixture) runFeedWith(t *testing.T, ship *navigation.Ship) bool {
	t.Helper()
	f.lastLot = gateFactoryLot(t, ship)
	return f.handler.feedGateLeg(f.ctx(), gateTestCmd(), gateTestSystem, f.lastLot, shared.MustNewPlayerID(1))
}

// THE BOUNDARY. The leg buys an INPUT and feeds it into the factory that needs it. It must never
// buy the gate material itself — that is the delivery fleet's leg, and doing both here is the
// serialized production-then-haul this design exists to split apart.
func TestFeedGateLeg_BuysAnInputAndFeedsItIntoTheFactoryThatNeedsIt(t *testing.T) {
	f := newGateFactoryHandler(t)

	f.runFeed(t, "GF-1")

	bought := f.buyer.goods()
	if len(bought) != 1 {
		t.Fatalf("bought %v, want exactly one input on a one-step leg", bought)
	}
	if _, boughtGateMaterial := bought[gateMaterialPrimary]; boughtGateMaterial {
		t.Fatalf("the factory leg bought %s — the gate material itself. It feeds INPUTS and never touches terminal output; buying it here re-serializes production and hauling", gateMaterialPrimary)
	}

	feeds := f.feeder.feeds()
	if len(feeds) != 1 {
		t.Fatalf("feeds = %+v, want exactly one factory fed", feeds)
	}
	if len(feeds[0].inputs) != 1 {
		t.Fatalf("feed carried %v; one step per leg means one input", feeds[0].inputs)
	}
	if _, ok := bought[feeds[0].inputs[0]]; !ok {
		t.Fatalf("fed %v but bought %v — the leg must deliver what it just bought", feeds[0].inputs, bought)
	}
}

// ADDED (not in the brief). ONE STEP PER LEG IS THE BOUND ON FACTORY-FLEET SPEND, and nothing in
// the brief's suite pins it. buyer.goods() is keyed by GOOD, so a leg that walked all three
// planned steps and bought IRON three times still reads len(bought) == 1 above — the bound would
// be gone and every assertion in this file would stay green.
//
// There is no bill for an INPUT (the site's bill is denominated in gate materials), so nothing
// downstream caps feedstock spend. The call COUNT is the cap, so the call count is what must be
// asserted.
func TestFeedGateLeg_BuysExactlyOnceEvenThoughThePlanHasSeveralSteps(t *testing.T) {
	f := newGateFactoryHandler(t)

	f.runFeed(t, "GF-1")

	// Non-vacuity: the bound only means something if the plan really did offer more than one step.
	// PlanFeed(FAB_MATS) over this fixture yields IRON->FAB_MATS, QUARTZ_SAND->FAB_MATS and
	// IRON_ORE->IRON; a single-step plan would make the assertion below trivially true.
	plan := gate.PlanFeed(gateMaterialPrimary, f.topo, gate.DefaultFeedDepthCap)
	if len(plan.Steps) < 2 {
		t.Fatalf("fixture is inert: the plan has %d step(s), so a leg that walked EVERY step would still buy once and this test could not see an unbounded leg", len(plan.Steps))
	}
	if calls := f.buyer.calls(); calls != 1 {
		t.Fatalf("BuyAtTerminalFactory called %d time(s) against a %d-step plan; ONE hull-load per leg is the only thing bounding factory-fleet spend, and a per-step loop spends the treasury against a bill that does not exist", calls, len(plan.Steps))
	}
	if feeds := f.feeder.feeds(); len(feeds) != 1 {
		t.Fatalf("fed %d factories on a one-step leg: %+v", len(feeds), feeds)
	}
}

// SHALLOWEST FIRST. The terminal factory's OWN inputs are what it is starved of, so the first
// step must target the terminal factory, not something three levels down.
func TestFeedGateLeg_FeedsTheTerminalFactoryBeforeAnythingDeeper(t *testing.T) {
	f := newGateFactoryHandler(t)

	// NON-VACUITY: "shallowest first" is only a claim if the plan HAS something deeper. Drop
	// IRON: {IRON_ORE} from the stub and the plan is two depth-1 steps, both targeting the
	// FAB_MATS exporter — every assertion below then passes for a leg with no ordering rule at all.
	// (Test 2's `len(plan.Steps) < 2` does not protect this: two depth-1 steps satisfy it.)
	plan := gate.PlanFeed(gateMaterialPrimary, f.topo, gate.DefaultFeedDepthCap)
	deepest := 0
	for _, step := range plan.Steps {
		if step.Depth > deepest {
			deepest = step.Depth
		}
	}
	if deepest < 2 {
		t.Fatalf("fixture is inert: the deepest planned step is depth %d (steps=%+v), so every step already targets the terminal factory and 'shallowest first' orders nothing", deepest, plan.Steps)
	}

	f.runFeed(t, "GF-1")

	feeds := f.feeder.feeds()
	if len(feeds) != 1 {
		t.Fatalf("feeds = %+v, want one", feeds)
	}
	// The destination is resolved by ROLE from the good — the exporter of FAB_MATS — never from
	// a hardcoded symbol.
	if feeds[0].waypoint != gateMaterialPrimary+"-EXPORTER" {
		t.Fatalf("fed %s; the first step must target the TERMINAL factory (the %s exporter), which is what the fleet is actually starved of", feeds[0].waypoint, gateMaterialPrimary)
	}
	// THE DETERMINISTIC ANSWER, not a disjunction. Both of FAB_MATS' inputs are depth-1, so
	// `input != "IRON" && input != "QUARTZ_SAND"` accepted either and pinned no order whatsoever.
	// PlanFeed walks the recipe in declaration order and planGateFeed takes the first step that
	// resolves, so IRON is the one and only correct answer here.
	if input := feeds[0].inputs[0]; input != "IRON" {
		t.Fatalf("fed %s to the %s factory; its recipe is {IRON, QUARTZ_SAND} in that order and the leg takes the first resolvable step, so the shallowest-first walk lands on IRON", input, gateMaterialPrimary)
	}
}

// THE ABUNDANT FAIL-SAFE. A factory already exporting at the top of the supply ladder does not
// need feeding; buying into a full warehouse burns treasury for nothing. Deliberately the ladder's
// TOP and nothing else — the moment this is a threshold it is a knob, and this phase adds none.
//
// EVERY TARGET IN THE PLAN must be abundant for this leg to feed nothing. The brief's fixture set
// only the two gate materials, leaving the IRON factory (target of the depth-2 step IRON_ORE->IRON)
// at MODERATE and feedable — the leg correctly fed it and the test failed on its own first
// assertion. The fixture was wrong, not the fail-safe.
func TestFeedGateLeg_SkipsAFactoryWhoseOutputIsAlreadyAbundant(t *testing.T) {
	f := newGateFactoryHandler(t)
	f.topo.supplyByGood = map[string]string{
		gateMaterialPrimary:   "ABUNDANT",
		gateMaterialSecondary: "ABUNDANT",
		"IRON":                "ABUNDANT",
	}

	f.runFeed(t, "GF-1")

	if feeds := f.feeder.feeds(); len(feeds) != 0 {
		t.Fatalf("fed %+v; a factory whose output is ABUNDANT needs no feedstock", feeds)
	}
	if bought := f.buyer.goods(); len(bought) != 0 {
		t.Fatalf("bought %v for a factory that is already full", bought)
	}
	// THE DECLINE'S OWN PHRASE, not the bare word. "ABUNDANT" is ALSO in the no-feedable-step
	// catch-all ("...already ABUNDANT at its factory..."), which fires in exactly this fixture
	// because every step declines — so matching the word alone survives deleting the decline's log
	// line outright, which is the one regression this assertion names.
	if want := "is already ABUNDANT — declining its IRON feed"; !strings.Contains(f.logLines(), want) {
		t.Fatalf("the skip is invisible in the log: no %q line, only the catch-all that fires whenever nothing could be planned for any reason:\n%s", want, f.logLines())
	}
	// ADDED (not in the brief): the two assertions above are BOTH satisfied by a leg that did
	// nothing at all, so on their own they cannot tell the fail-safe from a broken leg. The skip
	// must land BEFORE the input source is resolved — QUARTZ_SAND and IRON_ORE are only ever asked
	// about as SOURCES, so their absence proves the ABUNDANT check short-circuited the step rather
	// than the leg falling over earlier.
	asked := strings.Join(f.topo.goodsAsked(), ",")
	if !strings.Contains(asked, gateMaterialPrimary) {
		t.Fatalf("the leg never resolved the %s factory at all (asked %v) — it stopped short of the ABUNDANT check, so this test is not exercising the fail-safe", gateMaterialPrimary, asked)
	}
	for _, sourceOnly := range []string{"QUARTZ_SAND", "IRON_ORE"} {
		if strings.Contains(asked, sourceOnly) {
			t.Fatalf("the leg resolved the %s SOURCE (asked %v) for a step whose destination is already ABUNDANT — the fail-safe must decline the step before it prices its input", sourceOnly, asked)
		}
	}
}

// A step whose SOURCE cannot be resolved is skipped, and the leg moves on to the next step
// rather than standing the whole hull down. A refusal is never a substitution: sending a hull to
// some other waypoint is how cargo ends up somewhere it cannot be used (sp-b27a2).
func TestFeedGateLeg_SkipsAStepWhoseSourceCannotBeResolvedAndTakesTheNextOne(t *testing.T) {
	f := newGateFactoryHandler(t)
	f.topo.errByGood = map[string]error{"IRON": errors.New("no market exports IRON")}

	f.runFeed(t, "GF-1")

	feeds := f.feeder.feeds()
	if len(feeds) != 1 {
		t.Fatalf("feeds = %+v; an unresolvable IRON source must not stand the hull down while QUARTZ_SAND is still feedable", feeds)
	}
	if feeds[0].inputs[0] != "QUARTZ_SAND" {
		t.Fatalf("fed %v, want the next resolvable step (QUARTZ_SAND)", feeds[0].inputs)
	}
	// THE DECLINE'S OWN PHRASE. The bare token is already in the stream before any decline is
	// considered: planGateFeed logs plan.LogLine() unconditionally, and FeedPlan.LogLine renders
	// every step as Input->Target(dN) — so "IRON" is present even if the no_input_source line is
	// deleted entirely.
	if want := "nothing in " + gateTestSystem + " exports IRON"; !strings.Contains(f.logLines(), want) {
		t.Fatalf("the declined IRON step is invisible in the log: no %q line, and the bare symbol is already there from the plan's own summary:\n%s", want, f.logLines())
	}
}

// ---------------------------------------------------------------------------------------------
// sp-9eor3: AN UNAFFORDABLE STEP IS A DECLINE, NOT THE END OF THE LEG
//
// planGateFeed RETURNS the first passing step, so a condition it does not test as a `continue` ends
// the leg from the head of the line. Live, IRON's source resolved, its destination resolved and its
// destination was MODERATE rather than ABUNDANT — so IRON was selected every leg, refused every leg
// by the reserve, and QUARTZ_SAND (the next step, and the input its factory was actually starved
// of) was never reached for seven hours.
// ---------------------------------------------------------------------------------------------

// gateUnaffordableFixture stages the live shape: an EXPENSIVE first step and a CHEAP second one,
// with headroom in between. The prices are the incident's own — IRON laddering to ~3480 at a
// SCARCE export while QUARTZ_SAND sat at ~23 on an exchange.
//
// It sets a price SPREAD rather than one flat price on purpose. With a single price every step
// costs the same, so "declined the unaffordable step" and "declined the first step" become the same
// observation — and telling those two apart is the entire bead.
func gateUnaffordableFixture(t *testing.T) *gateFeedFixture {
	t.Helper()
	f := newGateFactoryHandler(t)
	f.topo.priceByGood = map[string]int{"IRON": 3480, "QUARTZ_SAND": 23}
	// One tranche is min(hold, trade volume) = 20 units: IRON costs 69,600 and QUARTZ_SAND 460, so
	// this headroom sits strictly between them and separates the two by affordability alone.
	f.buyer.spendHeadroom = 10_000
	return f
}

// assertIronIsPlannedBeforeQuartz is the non-vacuity every test in this cluster needs: the block is
// only a block if the expensive step really is planned FIRST. If PlanFeed's order ever changes,
// these tests would pass for a leg with no affordability rule at all.
func assertIronIsPlannedBeforeQuartz(t *testing.T, f *gateFeedFixture) {
	t.Helper()
	plan := gate.PlanFeed(gateMaterialPrimary, f.topo, gate.DefaultFeedDepthCap)
	iron, quartz := -1, -1
	for i, step := range plan.Steps {
		switch step.Input {
		case "IRON":
			if iron < 0 {
				iron = i
			}
		case "QUARTZ_SAND":
			if quartz < 0 {
				quartz = i
			}
		}
	}
	if iron < 0 || quartz < 0 || iron > quartz {
		t.Fatalf("fixture is inert: IRON is planned at index %d and QUARTZ_SAND at %d (steps=%+v). The head-of-line block only exists when the expensive step is planned FIRST", iron, quartz, plan.Steps)
	}
}

// THE BEAD. An unaffordable step must be passed over, not allowed to end the leg. Against the
// unmodified planner this fails: IRON resolves on both sides and is not ABUNDANT, so it is selected
// and QUARTZ_SAND is never reached.
func TestFeedGateLeg_TakesTheNextAffordableStepWhenTheFirstOneBreachesTheReserve(t *testing.T) {
	f := gateUnaffordableFixture(t)
	assertIronIsPlannedBeforeQuartz(t, f)

	f.runFeed(t, "GF-1")

	bought := f.buyer.goods()
	if _, boughtIron := bought["IRON"]; boughtIron {
		t.Fatalf("bought IRON (%v) at 3480/unit against %d headroom — the step the reserve refuses must be declined, not selected", bought, f.buyer.spendHeadroom)
	}
	if _, boughtQuartz := bought["QUARTZ_SAND"]; !boughtQuartz {
		t.Fatalf("bought %v; QUARTZ_SAND is affordable and planned right behind IRON, so an unaffordable IRON must not stand the whole leg down. THIS IS THE HEAD-OF-LINE BLOCK", bought)
	}

	feeds := f.feeder.feeds()
	if len(feeds) != 1 {
		t.Fatalf("feeds = %+v, want exactly one factory fed — the leg must still feed, just not with IRON", feeds)
	}
	if named := strings.Join(feeds[0].inputs, ","); named != "QUARTZ_SAND" {
		t.Fatalf("fed %q, want QUARTZ_SAND — the leg must deliver the step it actually afforded", named)
	}

	// ONE BUY PER LEG SURVIVES THE FIX. Skipping to the next step must not become a per-step buy
	// loop: an INPUT has no bill, so the call count is the only bound on factory-fleet spend.
	if calls := f.buyer.calls(); calls != 1 {
		t.Fatalf("BuyAtTerminalFactory called %d time(s); declining a step must SKIP it, never retry the buy — the call count is the only cap on factory-fleet spend", calls)
	}
}

// THE DECLINE NAMES ITSELF AND ITS NUMBERS, like every other decline in this walk. The function's
// own contract is that a declined step is legible; a silent skip would make a starved factory and a
// satisfied one look the same.
func TestFeedGateLeg_LogsWhyAnUnaffordableStepWasDeclined(t *testing.T) {
	f := gateUnaffordableFixture(t)
	assertIronIsPlannedBeforeQuartz(t, f)

	f.runFeed(t, "GF-1")

	// The precheck must have actually RUN. Every assertion below is also satisfied by a leg that
	// declined IRON for some unrelated reason, so without this the test cannot tell the affordability
	// rule from any other skip.
	if probes := f.buyer.probed(); len(probes) == 0 {
		t.Fatal("the planner never priced a single step: affordability was not evaluated at all, so any decline below came from another rule")
	}

	lines := f.logLines()
	// THE DECLINE'S OWN PHRASE, not a shared word. "reserve" now also appears in the
	// no-feedable-step catch-all, so matching that alone would survive deleting this line outright.
	if want := "declining the IRON feed for the " + gateMaterialPrimary + " factory"; !strings.Contains(lines, want) {
		t.Fatalf("no %q line: the skipped step is invisible, which is the log archaeology this bead was filed out of:\n%s", want, lines)
	}
	// THE NUMBERS IN THE MESSAGE. The container log renderer drops metadata maps and this leg emits
	// no metric — the log line IS the counter — so a decline whose cost lives only in metadata is one
	// no operator can size.
	if want := "would cost about 69600 against a reserve of 100000"; !strings.Contains(lines, want) {
		t.Fatalf("the decline does not price itself in the MESSAGE (want %q); metadata maps are dropped by the renderer, so the numbers would be lost:\n%s", want, lines)
	}
	// DISTINCT FROM THE ON-ARRIVAL REFUSAL (criterion 4). "no hull was sent" and "a hull flew and was
	// refused" must be tellable apart without log archaeology.
	if strings.Contains(lines, "acquired nothing of IRON") {
		t.Fatalf("the skipped step logged the ON-ARRIVAL refusal phrase; a leg that was never dispatched must not be reported as one that flew and was refused:\n%s", lines)
	}
}

// A LEG ENDS WITH NO FEED ONLY WHEN EVERY STEP DECLINES. The fix must not turn "this step is
// unaffordable" into "this leg is over" for the remaining steps too.
func TestFeedGateLeg_ParksOnlyWhenEveryStepIsUnaffordable(t *testing.T) {
	f := newGateFactoryHandler(t)
	f.topo.priceByGood = map[string]int{"IRON": 3480, "QUARTZ_SAND": 23}
	// Below even the cheapest step: 20 QUARTZ_SAND is 460, so nothing in the plan clears this.
	f.buyer.spendHeadroom = 100

	if drained := f.runFeed(t, "GF-1"); drained {
		t.Fatal("a leg that fed nothing must not report a drain")
	}

	if bought := f.buyer.goods(); len(bought) != 0 {
		t.Fatalf("bought %v; every step breaches the reserve, so nothing may be purchased", bought)
	}
	if calls := f.buyer.calls(); calls != 0 {
		t.Fatalf("BuyAtTerminalFactory called %d time(s); when every step is predicted unaffordable no hull should be dispatched at all", calls)
	}
	if feeds := f.feeder.feeds(); len(feeds) != 0 {
		t.Fatalf("fed %+v with nothing bought", feeds)
	}
	// NON-VACUITY, and it is what separates this from a leg that fell over early: EVERY step must
	// have been priced and rejected, not just the first.
	probes := f.buyer.probed()
	if len(probes) < 2 {
		t.Fatalf("only %d step(s) priced (%v); a leg that stopped at the first decline would produce exactly this park, so this test must see the walk continue past it", len(probes), probes)
	}
	if want := "found no feedable step this leg"; !strings.Contains(f.logLines(), want) {
		t.Fatalf("no %q line: the park came from some other exit:\n%s", want, f.logLines())
	}
	if status := f.taskRepo.statusOf(f.lastLot.task.ID()); status == "" {
		t.Fatal("the task was left EXECUTING; a silently-drained ready queue is indistinguishable from a finished gate")
	}
}

// REGRESSION (RULINGS #4). An AFFORDABLE fill behaves exactly as it did before the precheck existed:
// same step chosen, one buy, one feed. The precheck may only ever DECLINE earlier — it must never
// change what an affordable leg does.
func TestFeedGateLeg_AnAffordableStepDispatchesExactlyAsBefore(t *testing.T) {
	f := newGateFactoryHandler(t)
	f.topo.priceByGood = map[string]int{"IRON": 3480, "QUARTZ_SAND": 23}
	// Generous: 20 IRON at 3480 is 69,600, comfortably inside this.
	f.buyer.spendHeadroom = 1_000_000

	f.runFeed(t, "GF-1")

	// NON-VACUITY: the floor must have been CONSULTED and passed, not absent. With spendHeadroom
	// left at zero the probe is permissive and this test would prove nothing about an affordable
	// step — only about an unwired one.
	if probes := f.buyer.probed(); len(probes) == 0 {
		t.Fatal("no step was priced: the floor was never consulted, so this is not a test of an AFFORDABLE step")
	}
	bought := f.buyer.goods()
	if _, boughtIron := bought["IRON"]; !boughtIron {
		t.Fatalf("bought %v; IRON is the first planned step and it IS affordable here, so the leg must still choose it. A precheck that declines an affordable step has weakened nothing but broken everything", bought)
	}
	if calls := f.buyer.calls(); calls != 1 {
		t.Fatalf("BuyAtTerminalFactory called %d time(s), want exactly 1", calls)
	}
	feeds := f.feeder.feeds()
	if len(feeds) != 1 || strings.Join(feeds[0].inputs, ",") != "IRON" {
		t.Fatalf("feeds = %+v, want exactly one IRON feed — an affordable leg is unchanged", feeds)
	}
	if strings.Contains(f.logLines(), "declining the IRON feed") {
		t.Fatalf("an affordable step was logged as declined:\n%s", f.logLines())
	}
}

// THE PRECHECK DOES NOT REPLACE THE COMMIT-TIME GUARD (RULINGS #4). It reads a CACHED quote; only
// the per-tranche guard sees the live ask. A step that passes the prediction and is then refused on
// arrival must still fail closed exactly as before — no feed, no workaround.
func TestFeedGateLeg_StillFailsClosedWhenAPredictedAffordableBuyIsRefusedOnArrival(t *testing.T) {
	f := newGateFactoryHandler(t)
	f.buyer.spendHeadroom = 1_000_000 // the prediction passes...
	f.buyer.acquireZero = true        // ...and the live guard refuses anyway

	f.runFeed(t, "GF-1")

	// Non-vacuity FIRST: a leg that never reached the buy also feeds nothing.
	if calls := f.buyer.calls(); calls != 1 {
		t.Fatalf("the leg made %d buy attempt(s); with none, 'did not feed' says nothing about honouring a refused fill", calls)
	}
	if probes := f.buyer.probed(); len(probes) == 0 {
		t.Fatal("no step was priced, so this test is not exercising a PREDICTED-affordable step at all")
	}
	if feeds := f.feeder.feeds(); len(feeds) != 0 {
		t.Fatalf("flew to %+v with nothing aboard; the commit-time refusal stands whatever the prediction said", feeds)
	}
	if want := "acquired nothing of"; !strings.Contains(f.logLines(), want) {
		t.Fatalf("no %q line: the on-arrival refusal must still be recorded loudly:\n%s", want, f.logLines())
	}
}

// AN UNPRICED INPUT IS NOT AN UNAFFORDABLE ONE. With no cached quote there is nothing to predict, so
// the step proceeds and the commit-time guard decides on arrival as it always has.
//
// DIRECTIONAL GUARD, not a bead reproduction: it passes against the unmodified planner too (which
// had no precheck at all). It exists to pin the precheck's failure DIRECTION — rejecting on the
// ABSENCE of a quote would let a cold or unscanned price cache freeze the whole feed, which is a
// deadlock that appears exactly when the fleet is coldest and every quote is missing.
func TestFeedGateLeg_AnUnpricedInputIsNotTreatedAsUnaffordable(t *testing.T) {
	f := newGateFactoryHandler(t)
	f.topo.priceByGood = map[string]int{"IRON": 0} // never scanned
	f.buyer.spendHeadroom = 100                    // and there is almost no headroom

	f.runFeed(t, "GF-1")

	bought := f.buyer.goods()
	if _, boughtIron := bought["IRON"]; !boughtIron {
		t.Fatalf("bought %v; IRON has NO cached quote, so there is no prediction to make and the step must proceed to the commit-time guard. Declining on a missing price deadlocks a cold cache", bought)
	}
}

// ---------------------------------------------------------------------------------------------
// sp-q9um6: THE SCARCEST INPUT WINS, NOT THE FIRST ONE IN THE RECIPE
//
// Plan order encoded priority by accident. F45 read IRON IMPORT MODERATE and QUARTZ_SAND IMPORT
// SCARCE, and IRON won every leg for no better reason than appearing first in FAB_MATS' recipe.
// A factory cannot produce while short of ANY input, so the one it is shortest of is the one worth
// a hull-load.
// ---------------------------------------------------------------------------------------------

// gateFactoryWaypoint is the destination the FAB_MATS steps feed — the stub's exporter naming.
const gateFactoryWaypoint = gateMaterialPrimary + "-EXPORTER"

// THE BEAD. Both inputs are affordable and both resolve; only their scarcity at the destination
// differs. Against the unmodified planner this fails: it takes the first resolvable step, which is
// IRON.
func TestFeedGateLeg_FeedsTheInputTheFactoryIsShortestOfNotTheFirstInTheRecipe(t *testing.T) {
	f := newGateFactoryHandler(t)
	f.topo.importSupply = map[string]string{
		importSupplyKey(gateFactoryWaypoint, "IRON"):        "MODERATE",
		importSupplyKey(gateFactoryWaypoint, "QUARTZ_SAND"): "SCARCE",
	}
	assertIronIsPlannedBeforeQuartz(t, f)

	f.runFeed(t, "GF-1")

	feeds := f.feeder.feeds()
	if len(feeds) != 1 {
		t.Fatalf("feeds = %+v, want exactly one", feeds)
	}
	if named := strings.Join(feeds[0].inputs, ","); named != "QUARTZ_SAND" {
		t.Fatalf("fed %q; the %s factory is MODERATE on IRON and SCARCE on QUARTZ_SAND, so quartz is the input it actually cannot produce without. Feeding IRON is plan order winning on recipe position alone", named, gateMaterialPrimary)
	}
	if calls := f.buyer.calls(); calls != 1 {
		t.Fatalf("BuyAtTerminalFactory called %d time(s); ranking reorders the queue, it must not buy more than once", calls)
	}
}

// THE TEST THAT EARNS THE NEW SEAM. Two supply levels were already in hand and BOTH are the wrong
// quantity; this fixture is rigged so that ranking on the source's EXPORT supply gives the exact
// OPPOSITE answer to ranking on the destination's IMPORT supply.
//
// IRON's source is SCARCE and QUARTZ_SAND's is ABUNDANT, while the destination factory is MODERATE
// on IRON and SCARCE on QUARTZ_SAND. An implementation ranking on source.Supply picks IRON —
// preferring it precisely BECAUSE the market we buy it from is scarce, which is backwards. Only an
// implementation reading the destination's own import listing picks QUARTZ_SAND.
func TestFeedGateLeg_RanksOnTheFactorysImportSupplyNotTheSourcesExportSupply(t *testing.T) {
	f := newGateFactoryHandler(t)
	// Source-side EXPORT supplies, the perverse signal.
	f.topo.supplyByGood = map[string]string{"IRON": "SCARCE", "QUARTZ_SAND": "ABUNDANT"}
	// Destination-side IMPORT supplies, the correct signal — pointing the other way.
	f.topo.importSupply = map[string]string{
		importSupplyKey(gateFactoryWaypoint, "IRON"):        "MODERATE",
		importSupplyKey(gateFactoryWaypoint, "QUARTZ_SAND"): "SCARCE",
	}
	assertIronIsPlannedBeforeQuartz(t, f)

	f.runFeed(t, "GF-1")

	feeds := f.feeder.feeds()
	if len(feeds) != 1 {
		t.Fatalf("feeds = %+v, want exactly one", feeds)
	}
	if named := strings.Join(feeds[0].inputs, ","); named != "QUARTZ_SAND" {
		t.Fatalf("fed %q. IRON's SOURCE is SCARCE and QUARTZ_SAND's is ABUNDANT, but the FACTORY is MODERATE on IRON and SCARCE on QUARTZ_SAND. Choosing IRON means ranking on the source's export supply — preferring an input BECAUSE it is hard to buy, which says nothing about need", named)
	}
	// NON-VACUITY: the ranking must have actually consulted the destination's listing. Without this,
	// any rule that happened to land on QUARTZ_SAND would satisfy the assertion above.
	asked := strings.Join(f.topo.importsAskedList(), ",")
	if !strings.Contains(asked, importSupplyKey(gateFactoryWaypoint, "QUARTZ_SAND")) {
		t.Fatalf("the destination's IMPORT listing for QUARTZ_SAND was never read (asked %v); the choice came from somewhere else", asked)
	}
}

// THE SAFETY PROPERTY, and the reason this ranking is not riskier than the order it replaces: with
// NO readable import data the leg behaves EXACTLY as plan order did. The sort is stable and every
// unreadable step ties, so nothing moves.
//
// Passes against the unmodified planner too — deliberately. It pins the FALLBACK, and a fallback
// that only holds after the change would be no fallback at all.
func TestFeedGateLeg_KeepsPlanOrderWhenNoImportSupplyIsReadable(t *testing.T) {
	f := newGateFactoryHandler(t)
	// importSupply deliberately unset: every listing is unscanned.
	assertIronIsPlannedBeforeQuartz(t, f)

	f.runFeed(t, "GF-1")

	feeds := f.feeder.feeds()
	if len(feeds) != 1 {
		t.Fatalf("feeds = %+v, want exactly one", feeds)
	}
	if named := strings.Join(feeds[0].inputs, ","); named != "IRON" {
		t.Fatalf("fed %q with no supply data readable anywhere; an unrankable plan must keep plan order exactly, and IRON is first in FAB_MATS' recipe. Anything else means unreadable steps are being sorted rather than left alone", named)
	}
}

// UNREADABLE RANKS AS THE MIDDLE OF THE LADDER, never the front. One unscanned market must not
// capture every leg by outranking a factory we can see is fine.
func TestFeedGateLeg_AnUnreadableListingDoesNotOutrankAReadableOne(t *testing.T) {
	f := newGateFactoryHandler(t)
	// IRON unreadable (absent); QUARTZ_SAND readable and ABUNDANT — the factory has plenty.
	f.topo.importSupply = map[string]string{
		importSupplyKey(gateFactoryWaypoint, "QUARTZ_SAND"): "ABUNDANT",
	}
	assertIronIsPlannedBeforeQuartz(t, f)

	f.runFeed(t, "GF-1")

	feeds := f.feeder.feeds()
	if len(feeds) != 1 {
		t.Fatalf("feeds = %+v, want exactly one", feeds)
	}
	// Unknown ranks MODERATE(3), ABUNDANT ranks 5, so IRON legitimately wins here — but on the
	// neutral middle, not on being unknown. The inverse fixture below is what proves it is not
	// simply "unknown always wins".
	if named := strings.Join(feeds[0].inputs, ","); named != "IRON" {
		t.Fatalf("fed %q; an unreadable IRON ranks as the ladder's middle and an ABUNDANT quartz ranks above it, so IRON is correctly preferred", named)
	}

	// THE INVERSE, in the same test so the pair cannot drift apart: a readable SCARCE step must
	// still beat an unreadable one. If unknown sorted to the FRONT, IRON would win this too.
	f2 := newGateFactoryHandler(t)
	f2.topo.importSupply = map[string]string{
		importSupplyKey(gateFactoryWaypoint, "QUARTZ_SAND"): "SCARCE",
	}
	f2.runFeed(t, "GF-2")
	feeds2 := f2.feeder.feeds()
	if len(feeds2) != 1 {
		t.Fatalf("feeds = %+v, want exactly one", feeds2)
	}
	if named := strings.Join(feeds2[0].inputs, ","); named != "QUARTZ_SAND" {
		t.Fatalf("fed %q; IRON is UNREADABLE and QUARTZ_SAND is readably SCARCE. Choosing IRON means an unknown listing sorts to the front, which lets one unscanned market capture every leg", named)
	}
}

// RANKING COMPOSES WITH THE AFFORDABILITY DECLINE (sp-9eor3), it does not replace it. The scarcest
// input is tried FIRST; when the reserve refuses it the leg falls through to the next-scarcest
// rather than ending.
func TestFeedGateLeg_FallsPastTheScarcestInputWhenItIsUnaffordable(t *testing.T) {
	f := newGateFactoryHandler(t)
	f.topo.importSupply = map[string]string{
		importSupplyKey(gateFactoryWaypoint, "IRON"):        "MODERATE",
		importSupplyKey(gateFactoryWaypoint, "QUARTZ_SAND"): "SCARCE",
	}
	// The SCARCEST input is the expensive one: 20 units at 3480 = 69,600, past the headroom.
	f.topo.priceByGood = map[string]int{"QUARTZ_SAND": 3480, "IRON": 23}
	f.buyer.spendHeadroom = 10_000

	f.runFeed(t, "GF-1")

	feeds := f.feeder.feeds()
	if len(feeds) != 1 {
		t.Fatalf("feeds = %+v; the scarcest step being unaffordable must not stand the leg down while another step is affordable", feeds)
	}
	if named := strings.Join(feeds[0].inputs, ","); named != "IRON" {
		t.Fatalf("fed %q; QUARTZ_SAND ranks first on scarcity but breaches the reserve, so the leg must fall through to the affordable IRON", named)
	}
	// NON-VACUITY: the scarcest step must actually have been tried and declined, not skipped by the
	// ranking. Its decline names it.
	if want := "declining the QUARTZ_SAND feed"; !strings.Contains(f.logLines(), want) {
		t.Fatalf("no %q line: QUARTZ_SAND was never priced, so the ranking did not put it first and this test proves nothing about falling through:\n%s", want, f.logLines())
	}
}

// A SCARCE INPUT DOES NOT OVERRIDE THE ABUNDANT FAIL-SAFE. A factory whose OUTPUT is already at the
// top of the ladder needs no feedstock however short of an input it is — it must never be ranked
// into the queue at all, because ranking it would price an input the leg was never going to buy.
func TestFeedGateLeg_DoesNotRankAnAbundantTargetIntoTheQueue(t *testing.T) {
	f := newGateFactoryHandler(t)
	f.topo.supplyByGood = map[string]string{
		gateMaterialPrimary:   "ABUNDANT",
		gateMaterialSecondary: "ABUNDANT",
		"IRON":                "ABUNDANT",
	}
	// The most starved input in the system sits at a factory that needs nothing.
	f.topo.importSupply = map[string]string{
		importSupplyKey(gateFactoryWaypoint, "QUARTZ_SAND"): "SCARCE",
	}

	f.runFeed(t, "GF-1")

	if feeds := f.feeder.feeds(); len(feeds) != 0 {
		t.Fatalf("fed %+v; every target is ABUNDANT and needs no feedstock. A SCARCE input must not promote a full warehouse into the queue", feeds)
	}
	if bought := f.buyer.goods(); len(bought) != 0 {
		t.Fatalf("bought %v for factories that are already full", bought)
	}
	// The fail-safe must still decline BEFORE the source lookup — the ordering its own comment
	// protects. QUARTZ_SAND is only ever asked about as a SOURCE.
	if asked := strings.Join(f.topo.goodsAsked(), ","); strings.Contains(asked, "QUARTZ_SAND") {
		t.Fatalf("the leg resolved the QUARTZ_SAND SOURCE (asked %v) for a step whose destination is ABUNDANT — ranking must not move the source lookup ahead of the fail-safe", asked)
	}
}

// THE REORDERING ANNOUNCES ITSELF. A silent reorder feeds a different input than the plan lists
// first with nothing saying why — the same opacity from the other side.
func TestFeedGateLeg_LogsWhenScarcityReordersTheQueue(t *testing.T) {
	f := newGateFactoryHandler(t)
	f.topo.importSupply = map[string]string{
		importSupplyKey(gateFactoryWaypoint, "IRON"):        "MODERATE",
		importSupplyKey(gateFactoryWaypoint, "QUARTZ_SAND"): "SCARCE",
	}

	f.runFeed(t, "GF-1")

	lines := f.logLines()
	if want := "by SCARCITY, not recipe order"; !strings.Contains(lines, want) {
		t.Fatalf("no %q line: the leg fed a different input than the plan lists first and said nothing about why:\n%s", want, lines)
	}
	// The LEVELS in the message, not just the goods — the renderer drops metadata maps, and
	// "QUARTZ_SAND was chosen" without its supply level cannot be checked against the market.
	if want := "QUARTZ_SAND(SCARCE)"; !strings.Contains(lines, want) {
		t.Fatalf("the ranking does not name the supply levels it sorted on (want %q):\n%s", want, lines)
	}

	// AND IT STAYS QUIET WHEN NOTHING MOVED. A line per leg restating an unchanged plan order is
	// noise, and noise is how a real reordering stops being noticed.
	quiet := newGateFactoryHandler(t)
	quiet.runFeed(t, "GF-2")
	if strings.Contains(quiet.logLines(), "by SCARCITY, not recipe order") {
		t.Fatalf("the ranking announced itself on a leg where no supply was readable and nothing was reordered:\n%s", quiet.logLines())
	}
}

// A leg that can plan NOTHING parks its task through the SHARED completion machinery. Returning
// bare would leave the task EXECUTING forever: nothing re-stages the next load, the ready queue
// drains to nothing, and the drain reports RUNNING while doing nothing — a stall indistinguishable
// from a finished gate.
func TestFeedGateLeg_ParksItsTaskWhenNothingCanBeFed(t *testing.T) {
	f := newGateFactoryHandler(t)
	f.topo.errByGood = map[string]error{"IRON": errors.New("no exporter"), "QUARTZ_SAND": errors.New("no exporter")}

	if drained := f.runFeed(t, "GF-1"); drained {
		t.Fatal("a leg that fed nothing must not report a drain")
	}
	// NOTHING WAS FED, which is the premise the park is about and which neither assertion below can
	// see. A leg that plans, buys and feeds NORMALLY also reports !drained (feeding a factory
	// supplies no units to the SITE, so leg.delivered == 0 and completeOrDefer parks) and also
	// persists a status — so without this guard, a drift in the stub's errByGood keys turns this
	// into a silent duplicate of the happy-path test. Its sibling at the honest-park test carries
	// exactly this guard.
	if feeds := f.feeder.feeds(); len(feeds) != 0 {
		t.Fatalf("fed %+v; this test is about a leg that could plan NOTHING, and a leg that fed normally satisfies every other assertion here", feeds)
	}
	if want := "found no feedable step this leg"; !strings.Contains(f.logLines(), want) {
		t.Fatalf("no %q line: the park may have come from any other exit, and the branch this test names was never taken:\n%s", want, f.logLines())
	}
	if status := f.taskRepo.statusOf(f.lastLot.task.ID()); status == "" {
		t.Fatal("the task was left EXECUTING; a silently-drained ready queue is indistinguishable from a finished gate")
	}
}

// THE FLUSH. A factory hull holding a GATE MATERIAL cannot sell it at a terminal factory —
// marketBuys refuses an EXPORT listing — so that cargo would ride forever and the hold would never
// free. Unload it at the SITE first, through the SAME path the delivery leg uses.
func TestFeedGateLeg_UnloadsAnyGateMaterialAboardAtTheSiteBeforePlanning(t *testing.T) {
	f := newGateFactoryHandler(t)
	ship := gateTestLadenHull(t, "GF-1", gateMaterialPrimary, gateTestHoldCapacity)
	ship.SetDedicatedFleet(gate.FactoryFleetTag)

	f.runFeedWith(t, ship)

	if !f.deliveredGood(gateMaterialPrimary) {
		t.Fatalf("a factory hull arrived full of %s and never unloaded it; the terminal factory will not buy its own export, so that hold never frees and the hull is wedged forever", gateMaterialPrimary)
	}
}

// A buy that acquired NOTHING — the money or price guards stopped the fill — must not fly the
// hull to a factory with an empty hold. Fail-closed: the guards' refusal is honoured, not
// worked around.
func TestFeedGateLeg_DoesNotFeedWhenTheBuyAcquiredNothing(t *testing.T) {
	f := newGateFactoryHandler(t)
	f.buyer.acquireZero = true

	f.runFeed(t, "GF-1")

	// Non-vacuity FIRST, before the negative: a leg that never reached the buy at all also feeds
	// nothing, and would satisfy every assertion below while proving nothing about the guard.
	if calls := f.buyer.calls(); calls != 1 {
		t.Fatalf("the leg made %d buy attempt(s); with none, 'did not feed' says nothing about honouring a refused fill", calls)
	}
	if feeds := f.feeder.feeds(); len(feeds) != 0 {
		t.Fatalf("flew to %+v with nothing aboard; a money-guard refusal must stand, not be routed around", feeds)
	}
	if !strings.Contains(f.logLines(), "nothing") {
		t.Fatalf("a refused buy is invisible in the log:\n%s", f.logLines())
	}
}

// OBSERVABILITY. The spec requires the factory feed to record: good, resolved feed target,
// dispatched vs declined WITH the reason. All in the MESSAGE — the container log renderer drops
// metadata maps.
//
// THE DISPATCH HALF IS ASSERTED HERE AND NOWHERE ELSE. The declines are well covered ("ABUNDANT",
// the unresolvable "IRON", "nothing"), but the success line — the one line an operator reads to
// see the fleet actually working, and the only counter this leg has — was pinned by nothing: the
// three names below all appear in decline lines too, so they cannot tell a dispatch from a
// failure.
//
// The fed count is deliberately NOT the bought count. The leg buys a hull-load (80) and the
// factory takes 37, so a line that echoed what was BOUGHT rather than what the feeder reported
// ARRIVED reads 80 here and fails — the "reported a number it did not move" class, which a single
// shared figure would hide.
func TestFeedGateLeg_RecordsTheGoodTheTargetAndTheOutcome(t *testing.T) {
	const unitsTaken = 37
	f := newGateFactoryHandler(t)
	f.feeder.units = unitsTaken

	f.runFeed(t, "GF-1")

	lines := f.logLines()
	for _, want := range []string{gateMaterialPrimary, "-EXPORTER", "GF-1"} {
		if !strings.Contains(lines, want) {
			t.Fatalf("the feed leg's log does not name %q:\n%s", want, lines)
		}
	}
	// Non-vacuity: the hull really did buy a different number, so "fed 37" is a genuine separation
	// rather than an echo of the only figure in play.
	if bought := f.buyer.goods()["IRON"]; bought == unitsTaken {
		t.Fatalf("fixture is inert: the leg bought %d IRON and the factory took %d — with one number, a leg that logged what it BOUGHT would be indistinguishable from one logging what ARRIVED", bought, unitsTaken)
	}
	if want := fmt.Sprintf("fed %d IRON into", unitsTaken); !strings.Contains(lines, want) {
		t.Fatalf("the successful dispatch is not recorded as %q:\n%s\n\nEvery other line here is a DECLINE. Without the success marker and its unit count, a leg feeding 0 units forever and a leg feeding a full hold produce the same log, and this operation has no other counter", want, lines)
	}
}

// UNWIRED, the leg is a no-op that PARKS rather than a nil panic. The drain must survive a
// partially-wired build (existing coordinator tests construct one).
//
// The park is the load-bearing half and it is asserted FIRST. Returning false without parking
// satisfies every report-shaped assertion here — and it leaves the task EXECUTING forever: nothing
// re-stages the next load, the ready queue drains to nothing, and the drain reports RUNNING while
// doing nothing.
//
// Routing now gates the factory role on the SAME h.factory.enabled() this branch tests, so nothing
// in production reaches it — this test and its sibling below are the only callers. That makes the
// assertion MORE load-bearing, not less: no production traffic will ever exercise the guard that
// stops a future caller nil-panicking, so these two tests are the whole of its coverage.
func TestFeedGateLeg_IsSafeWhenTheFactoryCollaboratorsAreUnwired(t *testing.T) {
	f := newGateDeliveryHandler(t) // SetGateFactory deliberately NOT called
	ship := gateTestHull(t, "GF-1", gate.FactoryFleetTag)
	lot := gateFactoryLot(t, ship)

	drained := f.handler.feedGateLeg(f.ctx(), gateTestCmd(), gateTestSystem, lot, shared.MustNewPlayerID(1))

	// The exact status, not merely "something was persisted": deferTask parks via
	// ParkForResupply (-> PENDING) for the SupplyMonitor to re-activate, whereas a FAILED task
	// spends a retry against a condition that is not the leg's fault. Both are non-empty.
	if status := f.taskRepo.statusOf(lot.task.ID()); status != string(manufacturing.TaskStatusPending) {
		t.Fatalf("the unwired leg left task %s at %q, want %q — a leg that logs and returns without parking leaves the task EXECUTING forever, and nothing ever re-stages it",
			lot.task.ID(), status, manufacturing.TaskStatusPending)
	}
	if drained {
		t.Fatal("an unwired factory leg reported a drain")
	}
}

// ADDED (not in the brief). THE TWO STATES AN OPERATOR MUST BE ABLE TO TELL APART: a leg that was
// never invoked, and a leg invoked into an unwired handler that silently parks every hull. The
// brief's unwired test asserts only !drained, which a leg that returns false on line one satisfies
// — so both states produce an EMPTY LOG and look identical from outside.
//
// This leg emits no metric (the drain has no metrics seam at all; its whole observability surface
// is the container log), so the log line IS the counter and every path must leave one.
func TestFeedGateLeg_SaysSoWhenItIsUnwiredRatherThanParkingSilently(t *testing.T) {
	f := newGateDeliveryHandler(t) // SetGateFactory deliberately NOT called
	ship := gateTestHull(t, "GF-2", gate.FactoryFleetTag)

	f.handler.feedGateLeg(f.ctx(), gateTestCmd(), gateTestSystem, gateFactoryLot(t, ship), shared.MustNewPlayerID(1))

	lines := f.logLines()
	if lines == "" {
		t.Fatal("an unwired factory leg logged NOTHING; 'the leg was never invoked' and 'the leg ran and parked every hull' are then the same observation, which is the opacity this whole operation is being rebuilt to remove")
	}
	if !strings.Contains(lines, "GF-2") {
		t.Fatalf("the unwired leg's log does not name the hull it stood down:\n%s", lines)
	}
}

// ADDED (not in the brief). M4, AT THE CALL SITE. FeedFactory's `inputs` is the sp-b27a2 guard's
// SUBJECT — ValidateFeedDestination refuses the NAVIGATE unless the destination imports EVERY good
// named — while the delivery underneath (deliverInputs) offers the WHOLE HOLD, filtered by the
// destination's own listing.
//
// So the leg must name exactly what it acquired FOR THAT FACTORY, matching the fabricate path's
// run.haulingInputs(). Naming the whole hold instead would refuse the trip over unrelated cargo
// the hull merely happens to carry (sp-w2qg5: unsellable cargo aboard rides on, it does not veto
// the trip); naming nothing would fly a hull for no reason.
//
// The whole-hold half of that contract is pinned where the behaviour actually lives, against the
// real deliverInputs, in
// services.TestFeedFactory_OffersTheWholeHoldButTheDestinationsListingDecidesWhatLands.
func TestFeedGateLeg_NamesOnlyTheInputItBoughtWhenOtherCargoIsAboard(t *testing.T) {
	f := newGateFactoryHandler(t)
	// MACHINERY is neither a gate material (so the flush leaves it aboard) nor an input of any
	// factory in this plan — exactly the cargo a re-roled or restarted hull turns up carrying.
	const strayCargo = "MACHINERY"
	ship := gateTestLadenHull(t, "GF-1", strayCargo, 20)
	ship.SetDedicatedFleet(gate.FactoryFleetTag)

	f.runFeedWith(t, ship)

	feeds := f.feeder.feeds()
	if len(feeds) != 1 {
		t.Fatalf("feeds = %+v; unrelated cargo aboard must not veto the trip — sp-w2qg5 rules that unsellable cargo rides on", feeds)
	}
	named := strings.Join(feeds[0].inputs, ",")
	if named != "IRON" {
		t.Fatalf("the leg named %q as the feed's inputs, want exactly IRON — that list is the sp-b27a2 guard's subject and must be what this leg BOUGHT for this factory, not what the hold happens to contain", named)
	}
	if strings.Contains(named, strayCargo) {
		t.Fatalf("the leg named %s in its feed inputs; ValidateFeedDestination requires the destination to import EVERY named good, so naming stray cargo REFUSES a trip that would otherwise have fed the factory", strayCargo)
	}
}

// ADDED (not in the brief). The buy is sized against the free hold INCLUDING what the flush just
// released. DeliverToConstructionSite writes the emptied hold back through the repository and
// deliberately does NOT update the cached *Ship, so a leg that sized off the stale cargo figure
// alone would buy ZERO after a full-hold flush and the hull would cycle forever — the wedge phase 2
// hit on the delivery side, reproduced here.
func TestFeedGateLeg_SizesTheBuyAgainstTheHoldTheFlushJustFreed(t *testing.T) {
	f := newGateFactoryHandler(t)
	// Arrives FULL of a gate material: the cached hold reads 80/80, so free capacity is 0 until the
	// flush is counted.
	ship := gateTestLadenHull(t, "GF-1", gateMaterialPrimary, gateTestHoldCapacity)
	ship.SetDedicatedFleet(gate.FactoryFleetTag)

	f.runFeedWith(t, ship)

	// Non-vacuity: the flush must actually have freed something, or "bought > 0" would be a
	// statement about a hull that was never full.
	if !f.deliveredGood(gateMaterialPrimary) {
		t.Fatal("fixture is inert: the flush unloaded nothing, so there is no freed capacity for the buy to be sized against")
	}
	bought := f.buyer.goods()
	if bought["IRON"] <= 0 {
		t.Fatalf("bought %v after flushing a full hold; sizing off the stale cached cargo alone yields capacity 0 and the hull buys nothing forever", bought)
	}
	if bought["IRON"] != f.producer.delivered {
		t.Fatalf("bought %d IRON, want %d — the buy must be sized against exactly the hold the flush freed", bought["IRON"], f.producer.delivered)
	}
}

// ---------------------------------------------------------------------------------------------
// sp-2scwt — THE RESIDUAL WEDGE: a hull that ENDS a leg full parked forever
// ---------------------------------------------------------------------------------------------

// gateHoldItem is one good aboard a staged hull.
type gateHoldItem struct {
	good  string
	units int
}

// gateTestFactoryHullFullOf builds a FACTORY-role hull whose hold is EXACTLY FULL of the given
// goods — the state a SUCCESSFUL BUY FOLLOWED BY A FAILED FEED leaves behind, which the leg's own
// comments name ("the cargo stays aboard for the next leg").
//
// It ASSERTS the hold is full rather than trusting the caller's arithmetic. Every test below is
// about a hull with ZERO free capacity; one unit of slack and the leg takes the ordinary buy path,
// every assertion still passes, and the whole file would be pinning nothing.
func gateTestFactoryHullFullOf(t *testing.T, symbol string, hold ...gateHoldItem) *navigation.Ship {
	t.Helper()
	ship := gateTestHull(t, symbol, gate.FactoryFleetTag)

	items := make([]*shared.CargoItem, 0, len(hold))
	total := 0
	for _, entry := range hold {
		item, err := shared.NewCargoItem(entry.good, entry.good, "", entry.units)
		if err != nil {
			t.Fatalf("failed to build cargo item %s: %v", entry.good, err)
		}
		items = append(items, item)
		total += entry.units
	}
	if total != gateTestHoldCapacity {
		t.Fatalf("fixture is inert: the staged hold sums to %d units against a %d capacity, so this hull has free hold and takes the ordinary buy path — the wedge under test is a hull with NONE", total, gateTestHoldCapacity)
	}

	cargo, err := shared.NewCargo(gateTestHoldCapacity, total, items)
	if err != nil {
		t.Fatalf("failed to build laden cargo: %v", err)
	}
	ship.SetCargo(cargo)
	return ship
}

// THE WEDGE ITSELF. feedGateLeg BUYS BEFORE IT FEEDS and short-circuited the whole leg whenever
// free capacity was <= 0, so a hull that arrived full NEVER REACHED FeedFactory. Since the buy is
// sized to the whole free hold, one failed feed after a successful buy leaves the hull exactly
// full — and it then parked on every subsequent leg with nothing in the system to empty it. It was
// out of the fleet until a human intervened.
//
// The guard is inverted: no room to BUY means DELIVER WHAT YOU ALREADY HAVE.
func TestFeedGateLeg_FeedsWhatIsAlreadyAboardWhenTheHoldIsFull(t *testing.T) {
	f := newGateFactoryHandler(t)
	// IRON is an input of the FAB_MATS factory and is NOT a gate material, so the flush leaves it
	// aboard and the hull genuinely has nowhere to put a purchase.
	ship := gateTestFactoryHullFullOf(t, "GF-1", gateHoldItem{good: "IRON", units: gateTestHoldCapacity})

	f.runFeedWith(t, ship)

	// BEHAVIOUR FIRST. A leg that parks politely and a leg that delivers differ by one log line;
	// what separates them is whether a factory was actually fed.
	feeds := f.feeder.feeds()
	if len(feeds) != 1 {
		t.Fatalf("feeds = %+v; a factory hull with a full hold must DELIVER what it carries. Nothing else in the system empties it, so parking here is permanent", feeds)
	}
	if feeds[0].waypoint != gateMaterialPrimary+"-EXPORTER" {
		t.Fatalf("fed %s; the cargo aboard is IRON and the factory that consumes it is the %s exporter", feeds[0].waypoint, gateMaterialPrimary)
	}
	if named := strings.Join(feeds[0].inputs, ","); named != "IRON" {
		t.Fatalf("the leg named %q as the trip's subject, want exactly IRON — that list is the sp-b27a2 guard's subject, and naming anything the hull is not carrying refuses the very trip that would empty it", named)
	}
	// NO NEW SPEND PATH (RULINGS #4). This fix moves cargo that is already owned; a buy reached
	// from a zero-capacity hold would be a second purchasing path around the guard stack.
	if calls := f.buyer.calls(); calls != 0 {
		t.Fatalf("BuyAtTerminalFactory called %d time(s) for a hull with zero free hold; this path delivers already-owned cargo and must never spend", calls)
	}
}

// THE HOLD PICKS THE STEP, not the plan's running order. This is the assertion that separates a
// real fix from a leg that merely stops logging "no free hold": a walk which takes the first
// planned step and hopes the hold matches flies the hull to a factory that imports nothing it
// carries, lands ZERO units, and parks politely instead of parking loudly — the same wedge in a
// better log line. The named input is also the sp-b27a2 guard's subject, so naming a good the hull
// is not carrying refuses the very trip that would have emptied it.
func TestFeedGateLeg_FeedsTheGoodABOARDNotTheFirstStepInThePlan(t *testing.T) {
	f := newGateFactoryHandler(t)
	// QUARTZ_SAND is the SECOND step of the FAB_MATS plan; IRON is the first.
	ship := gateTestFactoryHullFullOf(t, "GF-1", gateHoldItem{good: "QUARTZ_SAND", units: gateTestHoldCapacity})

	// Non-vacuity, and it is the whole point of this test: if the plan's first step were already
	// QUARTZ_SAND, a hold-blind walk would pass every assertion below and this file would pin
	// nothing about hold-awareness at all.
	plan := gate.PlanFeed(gateMaterialPrimary, f.topo, gate.DefaultFeedDepthCap)
	if len(plan.Steps) == 0 || plan.Steps[0].Input == "QUARTZ_SAND" {
		t.Fatalf("fixture is inert: the plan's first step is %+v, so taking the first step blindly is indistinguishable from reading the hold", plan.Steps)
	}

	f.runFeedWith(t, ship)

	feeds := f.feeder.feeds()
	if len(feeds) != 1 {
		t.Fatalf("feeds = %+v, want exactly one", feeds)
	}
	if named := strings.Join(feeds[0].inputs, ","); named != "QUARTZ_SAND" {
		t.Fatalf("the leg named %q as the trip's subject while the hull carries only QUARTZ_SAND. The step must be chosen off the HOLD: naming %s dispatches a hull to deliver a good it does not have, and the sp-b27a2 guard judges exactly that list", named, plan.Steps[0].Input)
	}
}

// THE SOURCE IS NOT THE QUESTION on this path. planGateFeed refuses a step whose INPUT SOURCE does
// not resolve, which is correct for a leg that is about to buy. A leg that buys nothing must not
// inherit that requirement: an era where nothing exports IRON is exactly when a hull full of IRON
// is stuck, and requiring a source there re-creates the wedge under a new name.
func TestFeedGateLeg_DeliversAFullHoldEvenWhenNoInputSourceResolves(t *testing.T) {
	f := newGateFactoryHandler(t)
	f.topo.errByGood = map[string]error{
		"IRON":        errors.New("no market exports IRON"),
		"QUARTZ_SAND": errors.New("no market exports QUARTZ_SAND"),
		"IRON_ORE":    errors.New("no market exports IRON_ORE"),
	}
	ship := gateTestFactoryHullFullOf(t, "GF-1", gateHoldItem{good: "IRON", units: gateTestHoldCapacity})

	f.runFeedWith(t, ship)

	// Non-vacuity: with every input source unresolvable, the ordinary buy path can plan NOTHING —
	// so a leg reusing planGateFeed here would park, and this fixture is what tells the two apart.
	billSource, err := f.pipeline.FindByID(f.ctx(), gateTestPipelineID)
	if err != nil {
		t.Fatalf("reading the fixture pipeline: %v", err)
	}
	if _, _, _, planned := f.handler.planGateFeed(f.ctx(), gateTestCmd(), gateTestSystem, billSource, gateTestHoldCapacity); planned {
		t.Fatal("fixture is inert: the buy-side planner still found a step, so this test cannot see a hold path that wrongly demands a source")
	}

	feeds := f.feeder.feeds()
	if len(feeds) != 1 {
		t.Fatalf("feeds = %+v; the hull is full of IRON and the %s factory imports it. Nothing needs to be bought to empty it", feeds, gateMaterialPrimary)
	}
	if feeds[0].waypoint != gateMaterialPrimary+"-EXPORTER" {
		t.Fatalf("fed %s, want the %s exporter — the factory that consumes the IRON aboard", feeds[0].waypoint, gateMaterialPrimary)
	}
}

// THE ABUNDANT FAIL-SAFE IS A SPEND GUARD, and it still orders this path's CHOICE even though it
// cannot veto it: given two deliverable cargoes, the factory that is not already at the top of the
// supply ladder is the one that needs the feedstock.
func TestFeedGateLeg_PrefersANonAbundantFactoryWhenEmptyingAFullHold(t *testing.T) {
	f := newGateFactoryHandler(t)
	// The FAB_MATS factory is full up; the IRON factory (target of the deeper IRON_ORE step) is not.
	f.topo.supplyByGood = map[string]string{gateMaterialPrimary: "ABUNDANT"}
	ship := gateTestFactoryHullFullOf(t, "GF-1",
		gateHoldItem{good: "IRON", units: 40},     // consumed by the ABUNDANT FAB_MATS factory
		gateHoldItem{good: "IRON_ORE", units: 40}, // consumed by the MODERATE IRON factory
	)

	// NON-VACUITY, and it is the same guard its sibling carries. A PREFERENCE is only observable
	// when the thing being deprioritised comes FIRST: the abundant step (IRON->FAB_MATS) must be
	// planned ahead of the starved one (IRON_ORE->IRON), or a walk with NO preference at all
	// returns IRON_ORE too and every assertion below passes while pinning nothing. PlanFeed's
	// breadth-first order gives that today; if it ever changes, this test must fail loudly rather
	// than go quietly inert.
	plan := gate.PlanFeed(gateMaterialPrimary, f.topo, gate.DefaultFeedDepthCap)
	abundantAt, starvedAt := -1, -1
	for i, step := range plan.Steps {
		if step.Input == "IRON" && abundantAt < 0 {
			abundantAt = i
		}
		if step.Input == "IRON_ORE" && starvedAt < 0 {
			starvedAt = i
		}
	}
	if abundantAt < 0 || starvedAt < 0 || abundantAt > starvedAt {
		t.Fatalf("fixture is inert: the abundant-target step is at index %d and the starved one at %d (steps=%+v). The preference is only visible when the abundant step is planned FIRST", abundantAt, starvedAt, plan.Steps)
	}

	f.runFeedWith(t, ship)

	feeds := f.feeder.feeds()
	if len(feeds) != 1 {
		t.Fatalf("feeds = %+v, want exactly one — one step per leg holds on this path too", feeds)
	}
	// IRON->FAB_MATS is the FIRST step in the plan and its cargo IS aboard, so a walk that simply
	// took the first deliverable match would feed the abundant factory. Only a walk that prefers a
	// starved one reaches IRON_ORE->IRON, which is planned two steps later.
	if feeds[0].waypoint != "IRON-EXPORTER" {
		t.Fatalf("fed %s; both cargoes aboard are deliverable, and the %s factory is already ABUNDANT — the IRON factory is the one actually starved", feeds[0].waypoint, gateMaterialPrimary)
	}
	if named := strings.Join(feeds[0].inputs, ","); named != "IRON_ORE" {
		t.Fatalf("named %q, want IRON_ORE — the input the chosen factory consumes", named)
	}
}

// ...but ABUNDANT must never STRAND a hull. The fail-safe exists to stop a BUY into a full
// warehouse; this path buys nothing, so honouring it as a veto would trade an unwedged hull for no
// saving whatsoever.
func TestFeedGateLeg_EmptiesAFullHoldEvenWhenEveryTargetIsAbundant(t *testing.T) {
	f := newGateFactoryHandler(t)
	f.topo.supplyByGood = map[string]string{
		gateMaterialPrimary:   "ABUNDANT",
		gateMaterialSecondary: "ABUNDANT",
		"IRON":                "ABUNDANT",
	}
	ship := gateTestFactoryHullFullOf(t, "GF-1", gateHoldItem{good: "IRON", units: gateTestHoldCapacity})

	f.runFeedWith(t, ship)

	if feeds := f.feeder.feeds(); len(feeds) != 1 {
		t.Fatalf("feeds = %+v; the ABUNDANT check guards a PURCHASE, and this path makes none. Vetoing here leaves the hull wedged forever and saves nothing", feeds)
	}
	if calls := f.buyer.calls(); calls != 0 {
		t.Fatalf("BuyAtTerminalFactory called %d time(s); the abundant target must not be reached by way of a purchase", calls)
	}
	// NON-VACUITY, and this is the SOLE coverage of the haveAbundant fallback. Both assertions above
	// are satisfied by the ORDINARY branch: if supplyByGood's keys ever drift the target reads
	// MODERATE, planGateFeedFromHold returns at its non-abundant exit, and feeds == 1 && calls == 0
	// hold while the block this test exists to protect is never entered. Every sibling in this
	// cluster carries a guard of this shape.
	if want := "there anyway, since that check guards a PURCHASE"; !strings.Contains(f.logLines(), want) {
		t.Fatalf("no %q line: the leg took the ordinary non-abundant branch, so the abundant fallback is untested here:\n%s", want, f.logLines())
	}
}

// A FEED THAT MOVED ZERO UNITS IS NOT A RECOVERY. The destination refused nothing and the trip
// happened, but the market took none of it — so the hull is STILL FULL and re-enters this path
// next leg. This is not hypothetical: it is exactly what sp-kdsrh's fail-closed withhold produces
// when the arrival listing will not read (deliverInputs returns (0,0), which becomes
// FeedResult{UnitsDelivered: 0, Refused: false}).
//
// This leg emits no metric, so the log line IS the counter. One line covering both outcomes makes
// a freed hull and a permanently wedged one the same observation.
func TestFeedGateLeg_DoesNotClaimItFreedAHullWhenTheMarketTookNothing(t *testing.T) {
	f := newGateFactoryHandler(t)
	f.feeder.tookNothing = true
	ship := gateTestFactoryHullFullOf(t, "GF-1", gateHoldItem{good: "IRON", units: gateTestHoldCapacity})

	f.runFeedWith(t, ship)

	// Non-vacuity: the feed must actually have been attempted, or "did not claim a recovery" is
	// satisfied by a leg that never got there — the park path, which is a different case entirely.
	if feeds := f.feeder.feeds(); len(feeds) != 1 {
		t.Fatalf("feeds = %+v; this test is about a feed that HAPPENED and moved nothing, not about a leg that never fed", feeds)
	}

	log := f.logLines()
	if strings.Contains(log, "freeing the hull") {
		t.Fatalf("the leg reported freeing a hull the market took ZERO units from. It is still full and will re-enter this path forever; a recovery and a permanent wedge must not read the same:\n%s", log)
	}
	if !strings.Contains(log, "STILL FULL") {
		t.Fatalf("nothing in the log says the hull is still full after a zero-unit feed, so the wedge is invisible to an operator:\n%s", log)
	}
}

// THE HONEST PARK. A hull full of cargo NO factory in the plan imports genuinely has nowhere to
// go, and flying it to a factory that will refuse it is the sp-b27a2 incident. That park is
// correct — but it must be legible, and the task must still leave EXECUTING through the shared
// completion machinery.
func TestFeedGateLeg_ParksAFullHoldNoFactoryWillTakeAndNamesTheCargo(t *testing.T) {
	f := newGateFactoryHandler(t)
	// MACHINERY is neither a gate material (so the flush leaves it aboard) nor an input of any
	// factory in this plan.
	ship := gateTestFactoryHullFullOf(t, "GF-1", gateHoldItem{good: "MACHINERY", units: gateTestHoldCapacity})

	if drained := f.runFeedWith(t, ship); drained {
		t.Fatal("a leg that fed nothing must not report a drain")
	}
	if feeds := f.feeder.feeds(); len(feeds) != 0 {
		t.Fatalf("feeds = %+v; no factory in this plan imports MACHINERY, and dispatching a hull to one that will refuse it is exactly sp-b27a2", feeds)
	}
	if status := f.taskRepo.statusOf(f.lastLot.task.ID()); status == "" {
		t.Fatal("the task was left EXECUTING; a silently-drained ready queue is indistinguishable from a finished gate")
	}
	if !strings.Contains(f.logLines(), "MACHINERY") {
		t.Fatalf("the log never names the cargo that wedged the hull, so an operator cannot tell this park from an idle tick:\n%s", f.logLines())
	}
}

// The feed leg resolves every location through the topology at runtime. A waypoint literal here
// would pin the fleet to one era's map and then quietly send hulls nowhere.
//
// The gate package's own era-invariance sweep (fill_test.go) globs *.go RELATIVE TO ITS OWN
// PACKAGE DIR, so it does NOT cover this package. This is that guard, for this file.
func TestGateFeedLeg_ContainsNoWaypointLiterals(t *testing.T) {
	assertNoWaypointLiterals(t, "run_construction_coordinator_gate_feed.go", "feedGateLeg")
}

// pinFactoryRoleByPausing pauses every material on the pipeline, through the handler's own live
// buy policy, which is what lets a ONE-HULL fixture keep its factory tag through a whole drain tick.
//
// It is not decoration. reallocateGateRoles runs inside drainOnce, and an UNPAUSED gate's target is
// the D/F/F/D baseline — BaselineMix(1) is a single DELIVERY hull, so a lone factory hull is
// correctly re-roled to delivery before hauler discovery and the feeding leg never runs at all. A
// PAUSED gate targets all-factory, which is both the state that puts a hull on the factory role in
// production and the state these tests are about.
//
// The alternative — a second, delivery-tagged hull to make the census 1D/1F — would change the
// dispatch pool that two of these tests measure directly (claim count, which hull takes the lot).
//
// The floors come from the pipeline, not from "": policyFor caches per floor pair, so pausing
// through a different pair would build a SECOND policy object and the drain would read an unpaused
// one.
func pinFactoryRoleByPausing(handler *RunConstructionCoordinatorHandler, pipeline *manufacturing.ManufacturingPipeline) {
	policy := handler.gate.policyFor(pipeline.DeliveryBuyFloor(), pipeline.DeliveryResumeFloor())
	for _, material := range pipeline.Materials() {
		policy.Decide(material.TradeSymbol(), material.TradeSymbol()+"-EXPORTER", shared.SupplyLevelScarce)
	}
}

// gateFactoryReadyLot is a lot whose task is READY and whose claim identity is the FACTORY tag —
// the state supplyTask itself expects.
func gateFactoryReadyLot(t *testing.T, ship *navigation.Ship) constructionLot {
	t.Helper()
	lot := gateReadyLot(t, ship)
	lot.claimIdentity = gate.FactoryFleetTag
	return lot
}

// ---------------------------------------------------------------------------------------------
// THE ROUTING PIN — the test phase 2 could not write
// ---------------------------------------------------------------------------------------------
//
// The routing half of the shared predicate had NO behavioural pin: supplyTask claims the task
// UPSTREAM of the branch, and the pinning test asserts claimCount == 1, which is structurally
// incapable of observing which leg ran. Phase 3 widens routing, so it must be pinned by an
// observable that DIFFERS between the two legs.
//
// The observable is the driven port each leg terminates at: the delivery leg unloads at the
// construction SITE (DeliverToConstructionSite), the feeding leg calls FeedFactory. Neither is
// reachable from the other, so a mis-routed lot cannot satisfy both.

func TestSupplyTask_AFactoryTaggedLotRunsTheFEEDINGLegNotTheDeliveryLeg(t *testing.T) {
	f := newGateFactoryHandler(t)
	ship := gateTestHull(t, "GF-1", gate.FactoryFleetTag)

	f.handler.supplyTask(f.ctx(), gateTestCmd(), gateTestSystem, gateFactoryReadyLot(t, ship), shared.MustNewPlayerID(1))

	if len(f.feeder.feeds()) == 0 {
		t.Fatalf("a factory-tagged lot fed no factory — it was routed to the DELIVERY leg (or to the shared legacy path), and the whole factory role is inert.\nlog:\n%s", f.logLines())
	}
	if f.deliveredGood(gateMaterialPrimary) {
		t.Fatalf("a factory-tagged lot delivered %s to the construction site; the factory role feeds INPUTS and never touches terminal output", gateMaterialPrimary)
	}
}

func TestSupplyTask_ADeliveryTaggedLotStillRunsTheDELIVERYLeg(t *testing.T) {
	f := newGateFactoryHandler(t) // BOTH roles wired, so a mis-route has somewhere to go
	ship := gateTestHull(t, "GD-1", gate.DeliveryFleetTag)

	f.handler.supplyTask(f.ctx(), gateTestCmd(), gateTestSystem, gateReadyLot(t, ship), shared.MustNewPlayerID(1))

	if !f.deliveredGood(gateMaterialPrimary) {
		t.Fatalf("a delivery-tagged lot never supplied %s to the site — routing sent it to the feeding leg.\nlog:\n%s", gateMaterialPrimary, f.logLines())
	}
	if len(f.feeder.feeds()) != 0 {
		t.Fatalf("a delivery-tagged lot fed %+v; the delivery role buys terminal OUTPUT and never feeds", f.feeder.feeds())
	}
}

// An UNDEDICATED / legacy lot takes NEITHER gate leg. It runs the shared source-then-deliver path
// exactly as before, which is what keeps every pre-existing coordinator test valid.
func TestSupplyTask_ALegacyLotTakesNeitherGateLeg(t *testing.T) {
	f := newGateFactoryHandler(t)
	ship := gateTestHull(t, "GL-1", gate.LegacyFleetTag)
	lot := gateReadyLot(t, ship)
	lot.claimIdentity = gate.LegacyFleetTag

	f.handler.supplyTask(f.ctx(), gateTestCmd(), gateTestSystem, lot, shared.MustNewPlayerID(1))

	if len(f.feeder.feeds()) != 0 {
		t.Fatalf("a legacy lot fed %+v; it carries no role and must take the shared path", f.feeder.feeds())
	}
	if f.buyer.calls() != 0 {
		t.Fatalf("a legacy lot made %d pinned terminal-factory buy(s); that is a gate-leg operation", f.buyer.calls())
	}
	// The shared path is the producer's ProduceGood, not either gate leg's terminal. This assertion
	// is what stops a do-nothing router passing: the two negatives above are both satisfied by a
	// supplyTask that returned on line one.
	if len(f.producer.produceGoods) == 0 {
		t.Fatalf("a legacy lot reached neither gate leg NOR the shared source path — it did nothing at all.\nlog:\n%s", f.logLines())
	}
}

// ---------------------------------------------------------------------------------------------
// REACHABILITY — the whole point of this task, driven end to end through the drain
// ---------------------------------------------------------------------------------------------

// ADDED (not in the brief). TASK 4'S DEFERRED CONCERN 1. Until this task, feedGateLeg had NO
// production caller at all: `grep feedGateLeg` named its own definition and this test file, nothing
// else. The routing tests above enter at supplyTask with a hand-built lot whose claimIdentity is
// ASSIGNED BY THE TEST, so they pin the branch but say nothing about whether the drain ever mints
// that identity, survives dispatch, or is authorized to claim under it.
//
// Three things between discovery and the feed can each make the role silently inert, and none is
// visible from supplyTask:
//
//   - dispatchableByHold can drop the hull (the fleet-killer this task exists to avoid);
//   - claimIdentityFor must mint "gate-factory", because ClaimShip authorizes a dedicated hull only
//     when tag == operation — under any other identity the DB rejects it and the hull never works;
//   - the routing branch must then be reached with THAT frozen identity.
//
// The two buyers are DISTINCT here so the assertion can tell the legs apart: a mis-routed factory
// lot spends through the delivery buyer, a correctly-routed one through the factory buyer.
func TestConstructionDrain_DrivesAFactoryTaggedHullAllTheWayToTheFeedingLeg(t *testing.T) {
	// The factory takes FEWER units than the hull bought, so the dispatch line below can only have
	// come from what ARRIVED. With one figure, a leg logging what it BOUGHT would be
	// indistinguishable from one logging what the factory actually took.
	const unitsTaken = 37

	pipeline := newDrainPipeline(t, gateMaterialPrimary, 40)
	task := readyConstructionTask(t, pipeline, gateMaterialPrimary)

	hull := newTestHaulerInFleet(t, "GATE-8", gate.FactoryFleetTag)

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(hull)
	deliveryBuyer, factoryBuyer := &countingGateBuyer{}, &countingGateBuyer{}
	feeder := &recordingFeeder{units: unitsTaken}
	logger := &capturingLogger{}

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	handler.SetGateDelivery(&stubGateTopology{supply: "MODERATE"}, deliveryBuyer)
	handler.SetGateFactory(newStubFactoryTopology(), factoryBuyer, feeder)
	pinFactoryRoleByPausing(handler, pipeline)
	if _, err := drainSettled(t, handler, common.WithLogger(context.Background(), logger), newDrainCommand()); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	joined := logger.joined()

	// BEHAVIOUR FIRST. A leg that reported RUNNING while no cargo moved is this plan's most
	// frequent failure, so the assertion is on the driven port, not on a status.
	feeds := feeder.feeds()
	if len(feeds) == 0 {
		t.Fatalf("a FACTORY-tagged hull driven through the whole drain fed NO factory. feedGateLeg has no other production caller, so the factory role ships dark.\nlog:\n%s", joined)
	}
	if deliveryBuyer.calls() != 0 {
		t.Fatalf("the factory hull spent %d time(s) through the DELIVERY buyer; it was routed to the wrong leg", deliveryBuyer.calls())
	}
	if factoryBuyer.calls() != 1 {
		t.Fatalf("the factory leg made %d buy(s), want exactly 1 — one hull-load per leg is the only bound on factory-fleet spend", factoryBuyer.calls())
	}
	// UNITS ACTUALLY MOVED, not a status — a leg that reports RUNNING while no cargo moved is this
	// plan's most frequent failure. Non-vacuity FIRST: the two figures must genuinely differ, or
	// this assertion cannot tell what ARRIVED from what was BOUGHT.
	//
	// The success marker is "fed " and the failure line reads "feeding %d %s into ... failed", which
	// does NOT contain it — the near-miss that nearly cost task 4 its I-2 kill.
	if bought := factoryBuyer.goods()["IRON"]; bought == unitsTaken {
		t.Fatalf("fixture is inert: the leg bought %d IRON and the factory took %d — with one number, a leg logging what it BOUGHT is indistinguishable from one logging what ARRIVED", bought, unitsTaken)
	}
	if want := fmt.Sprintf("fed %d IRON into", unitsTaken); !strings.Contains(joined, want) {
		t.Fatalf("the feed moved no units an operator can see; want a %q dispatch line.\nlog:\n%s", want, joined)
	}

	// THE CLAIM IDENTITY. A hull claimed under any other operation is rejected at the DB and then
	// silently never works — the defect class that ships green.
	shipRepo.mu.Lock()
	claims := append([]drainClaim(nil), shipRepo.claims...)
	shipRepo.mu.Unlock()
	if len(claims) != 1 {
		t.Fatalf("expected the factory hull discovered and claimed exactly once, got %d claim(s)", len(claims))
	}
	if claims[0].symbol != "GATE-8" || claims[0].operation != gate.FactoryFleetTag {
		t.Fatalf("claim %+v: a gate-factory hull must be claimed under %q, or ClaimShip rejects it and the hull silently never works", claims[0], gate.FactoryFleetTag)
	}
}

// ---------------------------------------------------------------------------------------------
// THE DECLINE MUST STAY SCOPED TO THE DELIVERY ROLE
// ---------------------------------------------------------------------------------------------

// THE FLEET-KILLER THIS TASK EXISTS TO PREVENT. wedgedAtFullHold means "the hold is full and
// nothing aboard is a material whose bill is still open" — and a factory hull's hold is full of
// IRON_ORE, which is NEVER a bill material. So the predicate is TRUE for every laden factory hull.
// Widening the shared routing condition to a boolean covering both roles would decline the entire
// factory fleet every tick, forever, and the feeding leg would never run once.
//
// WHAT THIS TEST PINS, EXACTLY: that the hull is still DISPATCHED — claimed once — and nothing
// more. It asserts claimCount and no outcome of the leg.
//
// It used to claim more. The comment here read "the feeding leg recovers exactly this hull by a
// route the predicate cannot see: FeedFactory sells the inputs into the factory's import listing,
// which frees the hold" — which was the very claim sp-2scwt disproved: feedGateLeg BUYS BEFORE IT
// FEEDS and short-circuited at capacity<=0, so a full hull never reached FeedFactory at all. The
// claim is TRUE AGAIN now that sp-2scwt is fixed, but this test still cannot demonstrate it: its
// material is gateMaterialSecondary, which is absent from stubFactoryTopology.recipes, so IsRaw
// calls it raw, PlanFeed yields ZERO steps, and there is nothing for any hold-driven walk to find.
// A dispatched hull that then feeds nothing passes every assertion below.
//
// The recovery is demonstrated instead by
// TestConstructionDrain_RecoversAFactoryHullFullOfFabricationInputs, which is this fixture over a
// material the stub topology actually has a recipe for. Keeping the two separate keeps this one's
// subject — the decline staying scoped to the delivery role — undiluted.
func TestConstructionDrain_StillDispatchesAFactoryHullFullOfFabricationInputs(t *testing.T) {
	pipeline := newDrainPipeline(t, gateMaterialSecondary, 200)
	task := readyConstructionTask(t, pipeline, gateMaterialSecondary)

	inputs, err := shared.NewCargoItem("IRON_ORE", "IRON_ORE", "", 40)
	if err != nil {
		t.Fatalf("NewCargoItem: %v", err)
	}
	hull := newTestHauler(t, "TORWIND-5", []*shared.CargoItem{inputs}) // 40/40: no free hold
	hull.SetDedicatedFleet(gate.FactoryFleetTag)

	// NON-VACUITY, and it is not optional here: this test's whole subject is a hull that
	// wedgedAtFullHold calls WEDGED. wedgedAtFullHold has TWO conditions and the fixture must
	// satisfy BOTH, or the predicate is false for every role and the assertion below passes under
	// the widened decline too, naming zero tests for the mutation probe that guards this file.
	//
	// (1) FULL HOLD. One unit of free space and the predicate returns false on the first branch.
	if cargo := hull.Cargo(); cargo == nil || cargo.Capacity-cargo.Units != 0 {
		t.Fatalf("fixture is inert: the hull has free hold, so wedgedAtFullHold is false for it whatever the role, and a decline widened to the factory role would not be visible here")
	}
	// (2) NOTHING ABOARD IS WANTED. The second branch, and the easier of the two to break silently:
	// make the cargo aboard a ready material — rename a constant, add a bill — and the predicate
	// returns false on `wantsMaterialAboard` while the hold stays full, so this test goes green for
	// a reason that has nothing to do with the role scoping it exists to pin.
	// onHandUnits is the same helper wantsMaterialAboard asks, so this reads the predicate's own
	// second branch rather than a paraphrase of it.
	if onHandUnits(hull, task.Good()) > 0 {
		t.Fatalf("fixture is inert: the hull carries %s, which this tick's ready task still wants, so wedgedAtFullHold is false on its second condition and the hull would be dispatched whatever the decline's role scoping says", task.Good())
	}

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(hull)

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	handler.SetGateDelivery(&stubGateTopology{supply: "HIGH"}, &countingGateBuyer{})
	handler.SetGateFactory(newStubFactoryTopology(), &countingGateBuyer{}, &recordingFeeder{})
	pinFactoryRoleByPausing(handler, pipeline)
	if _, err := drainSettled(t, handler, context.Background(), newDrainCommand()); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	if got := shipRepo.claimCount(); got != 1 {
		t.Fatalf("a FACTORY hull full of fabrication inputs was claimed %d time(s), want 1 — its leg SELLS those inputs into a factory, so declining it makes the whole factory fleet permanently invisible", got)
	}
}

// THE RECOVERY ITSELF, end to end through the drain. The sibling above pins that a full
// factory hull is DISPATCHED; this pins that being dispatched actually gets it EMPTIED, which is
// the whole reason declining it would have been wrong.
//
// The only fixture difference is the material: gateMaterialPrimary IS in stubFactoryTopology's
// recipes (FAB_MATS = {IRON, QUARTZ_SAND}, IRON = {IRON_ORE}), so PlanFeed yields real steps and
// the IRON_ORE aboard matches the depth-2 step IRON_ORE->IRON. Before sp-2scwt the leg parked here
// on the capacity guard and the hull stayed full forever.
func TestConstructionDrain_RecoversAFactoryHullFullOfFabricationInputs(t *testing.T) {
	pipeline := newDrainPipeline(t, gateMaterialPrimary, 40)
	task := readyConstructionTask(t, pipeline, gateMaterialPrimary)

	inputs, err := shared.NewCargoItem("IRON_ORE", "IRON_ORE", "", 40)
	if err != nil {
		t.Fatalf("NewCargoItem: %v", err)
	}
	hull := newTestHauler(t, "TORWIND-6", []*shared.CargoItem{inputs}) // 40/40: no free hold
	hull.SetDedicatedFleet(gate.FactoryFleetTag)

	// NON-VACUITY 1: the hull must genuinely have nowhere to put a purchase, or the leg takes the
	// ordinary buy path and this test says nothing about the wedge.
	if cargo := hull.Cargo(); cargo == nil || cargo.Capacity-cargo.Units != 0 {
		t.Fatalf("fixture is inert: the hull has free hold, so it never reaches the zero-capacity path this test is about")
	}
	// NON-VACUITY 2: and the topology must actually plan a step for what is aboard. This is exactly
	// what the sibling test lacks, and without it a green run would prove only that nothing fed.
	plan := gate.PlanFeed(gateMaterialPrimary, newStubFactoryTopology(), gate.DefaultFeedDepthCap)
	carriesAPlannedInput := false
	for _, step := range plan.Steps {
		if step.Input == "IRON_ORE" {
			carriesAPlannedInput = true
		}
	}
	if !carriesAPlannedInput {
		t.Fatalf("fixture is inert: no step in the %s plan takes IRON_ORE, so the hold path has nothing to find and 'fed nothing' would be correct rather than a regression. steps=%+v", gateMaterialPrimary, plan.Steps)
	}

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(hull)
	factoryBuyer := &countingGateBuyer{}
	feeder := &recordingFeeder{}
	logger := &capturingLogger{}

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	handler.SetGateDelivery(&stubGateTopology{supply: "HIGH"}, &countingGateBuyer{})
	handler.SetGateFactory(newStubFactoryTopology(), factoryBuyer, feeder)
	pinFactoryRoleByPausing(handler, pipeline)
	if _, err := drainSettled(t, handler, common.WithLogger(context.Background(), logger), newDrainCommand()); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	// BEHAVIOUR FIRST: a factory was fed. Dispatch without delivery is the defect, not the fix.
	feeds := feeder.feeds()
	if len(feeds) != 1 {
		t.Fatalf("a full FACTORY hull was driven through the whole drain and fed %d factories; nothing else empties it, so it is out of the fleet until a human intervenes.\nlog:\n%s", len(feeds), logger.joined())
	}
	if feeds[0].waypoint != "IRON-EXPORTER" {
		t.Fatalf("fed %s; the IRON_ORE aboard is consumed by the IRON factory", feeds[0].waypoint)
	}
	if named := strings.Join(feeds[0].inputs, ","); named != "IRON_ORE" {
		t.Fatalf("the leg named %q as the trip's subject while the hull carries only IRON_ORE; ValidateFeedDestination judges that list, so naming anything else refuses the trip that empties it", named)
	}
	// The hull had no room, so nothing was bought to make this happen (RULINGS #4).
	if calls := factoryBuyer.calls(); calls != 0 {
		t.Fatalf("BuyAtTerminalFactory called %d time(s) for a hull with zero free hold; the recovery moves already-owned cargo", calls)
	}
}

// THE SAME RECOVERY, WITH DELIVERY RUNNING — the composition defect the sibling above cannot see.
//
// That sibling calls pinFactoryRoleByPausing, and the pause is the ONLY thing hiding this. Unpaused,
// the reallocator's target is the D/F/F/D baseline, BaselineMix(1) is a single DELIVERY hull, and
// the return-to-baseline direction carries no dwell — so the reallocation re-tags this hull to
// gate-delivery in the same tick, BEFORE hauler discovery.
//
// From there the recovery built for it is unreachable, and every step is a correct decision taken
// alone: wedgedAtFullHold is TRUE for a delivery hull holding IRON_ORE (never a bill material), so
// dispatchableByHold declines it every tick; and feedGateLegFromHold, the only path that empties it,
// is factory-role-only. Task 5 scoped the decline to delivery (widening it is the fleet-killer),
// task 6 restricted reallocation to idle unheld hulls, sp-2scwt scoped the from-hold recovery to the
// factory leg — and the composition of the three moves the hull out of the role that protects it.
//
// The reallocator must therefore keep a hull carrying undeliverable cargo on the factory role, which
// is the only role that can empty it.
func TestConstructionDrain_RecoversAFullFactoryHullWhileDeliveryIsStillRunning(t *testing.T) {
	// A figure the fixture uses NOWHERE else, so the units assertion below can only have come from
	// what the feeder reported ARRIVED.
	const unitsTaken = 33

	pipeline := newDrainPipeline(t, gateMaterialPrimary, 40)
	task := readyConstructionTask(t, pipeline, gateMaterialPrimary)

	inputs, err := shared.NewCargoItem("IRON_ORE", "IRON_ORE", "", 40)
	if err != nil {
		t.Fatalf("NewCargoItem: %v", err)
	}
	hull := newTestHauler(t, "TORWIND-7", []*shared.CargoItem{inputs}) // 40/40: no free hold
	hull.SetDedicatedFleet(gate.FactoryFleetTag)

	// NON-VACUITY 1: zero free hold, or the leg takes the ordinary buy path and this test is about
	// nothing. It is also half of wedgedAtFullHold, which is what declines the hull once re-tagged.
	if cargo := hull.Cargo(); cargo == nil || cargo.Capacity-cargo.Units != 0 {
		t.Fatalf("fixture is inert: the hull has free hold, so it neither reaches the from-hold path nor reads as wedged in the delivery role")
	}
	// NON-VACUITY 2: nothing aboard is a material the bill still wants — the OTHER half of
	// wedgedAtFullHold, and the easier one to break silently by renaming a material constant.
	if onHandUnits(hull, task.Good()) > 0 {
		t.Fatalf("fixture is inert: the hull carries %s, which this tick's ready task still wants, so the delivery role would flush it and the trap this test is about does not exist", task.Good())
	}

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(hull)
	factoryBuyer := &countingGateBuyer{}
	feeder := &recordingFeeder{units: unitsTaken}
	logger := &capturingLogger{}

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	handler.SetGateDelivery(&stubGateTopology{supply: "HIGH"}, &countingGateBuyer{})
	handler.SetGateFactory(newStubFactoryTopology(), factoryBuyer, feeder)
	// pinFactoryRoleByPausing is deliberately NOT called: delivery RUNNING is the whole subject.
	if _, err := drainSettled(t, handler, common.WithLogger(context.Background(), logger), newDrainCommand()); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}
	joined := logger.joined()

	// BEHAVIOUR FIRST: a factory was fed. A hull re-tagged out of the factory role is declined
	// silently from the dispatch pool, so "no error" and "the cargo moved" are entirely different
	// claims here.
	feeds := feeder.feeds()
	if len(feeds) != 1 {
		t.Fatalf("a full FACTORY hull fed %d factories with delivery RUNNING. Re-tagged to gate-delivery it is declined by wedgedAtFullHold every tick, and feedGateLegFromHold — the only thing that empties it — never runs for a delivery hull.\nlog:\n%s", len(feeds), joined)
	}
	if feeds[0].waypoint != "IRON-EXPORTER" {
		t.Fatalf("fed %s; the IRON_ORE aboard is consumed by the IRON factory", feeds[0].waypoint)
	}
	if named := strings.Join(feeds[0].inputs, ","); named != "IRON_ORE" {
		t.Fatalf("the leg named %q as the trip's subject while the hull carries only IRON_ORE; ValidateFeedDestination judges that list", named)
	}
	// UNITS ACTUALLY TRANSFERRED, off the feeder's own report rather than off a status or the
	// absence of an error. The from-hold success line is the only one carrying this phrase — the buy
	// path's reads "fed %d IRON_ORE into", and both failure lines read "aboard ... failed"/"refused".
	if want := fmt.Sprintf("fed the %d IRON_ORE already aboard", unitsTaken); !strings.Contains(joined, want) {
		t.Fatalf("no %q line: the hull may have been dispatched without anything leaving its hold.\nlog:\n%s", want, joined)
	}

	// NON-VACUITY 3, and it is what separates this test from its paused sibling: the reallocator must
	// actually have ruled, with delivery RUNNING. If the fixture ever pauses, every assertion above
	// still passes and this becomes a silent duplicate of
	// TestConstructionDrain_RecoversAFactoryHullFullOfFabricationInputs.
	if !strings.Contains(joined, "Gate roles (delivery running") {
		t.Fatalf("the reallocator never ruled on a RUNNING delivery fleet, so nothing here exercised the return-to-baseline direction that re-tags this hull:\n%s", joined)
	}
	// And the hull must still carry the tag whose leg just ran. A re-tag mid-tick would leave the
	// next tick's claim identity pointing at a role with no recovery.
	if got := hull.DedicatedFleet(); got != gate.FactoryFleetTag {
		t.Fatalf("the hull ended the tick on %q; a hull holding cargo only the factory role can unload must not be returned to delivery", got)
	}
}

// The falsifier for the test above: the DELIVERY-role decline must still fire. Without this, a
// decline that never fires would pass the factory test and silently reintroduce the tick-forever
// wedge phase 2 closed.
func TestConstructionDrain_StillDeclinesAWedgedDELIVERYHullAfterTheRoleSplit(t *testing.T) {
	pipeline := manufacturing.NewConstructionPipeline(constructionSiteWP, 1, 1, 5)
	if err := pipeline.AddMaterial(manufacturing.NewConstructionMaterialTarget(gateMaterialPrimary, 40)); err != nil {
		t.Fatalf("AddMaterial: %v", err)
	}
	if err := pipeline.AddMaterial(manufacturing.NewConstructionMaterialTarget(gateMaterialSecondary, 200)); err != nil {
		t.Fatalf("AddMaterial: %v", err)
	}
	if err := pipeline.Start(); err != nil {
		t.Fatalf("pipeline.Start: %v", err)
	}
	if err := pipeline.RecordMaterialDelivery(gateMaterialPrimary, 40); err != nil {
		t.Fatalf("RecordMaterialDelivery: %v", err)
	}
	task := readyConstructionTask(t, pipeline, gateMaterialSecondary)

	stranded, err := shared.NewCargoItem(gateMaterialPrimary, gateMaterialPrimary, "", 40)
	if err != nil {
		t.Fatalf("NewCargoItem: %v", err)
	}
	hull := newTestHauler(t, "TORWIND-9", []*shared.CargoItem{stranded})
	hull.SetDedicatedFleet(gate.DeliveryFleetTag)

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(hull)
	logger := &capturingLogger{}

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	handler.SetGateDelivery(&stubGateTopology{supply: "HIGH"}, &countingGateBuyer{})
	handler.SetGateFactory(newStubFactoryTopology(), &countingGateBuyer{}, &recordingFeeder{})
	if _, err := drainSettled(t, handler, common.WithLogger(context.Background(), logger), newDrainCommand()); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	if got := shipRepo.claimCount(); got != 0 {
		t.Fatalf("a wedged DELIVERY hull was claimed %d time(s) after the role split; the decline stopped firing and the tick-forever wedge is back", got)
	}
	// ADDED (not in the brief). claimCount == 0 is satisfied by a drain that dispatched NOTHING —
	// a fixture with no ready task, an undiscovered hull, or a hull in the wrong system all pass it
	// while proving nothing about the decline. The decline names the hull it dropped, so requiring
	// that line is what separates "declined" from "never seen".
	if !strings.Contains(logger.joined(), "TORWIND-9") || !strings.Contains(logger.joined(), "FULL hold") {
		t.Fatalf("nothing in the log says this hull was DECLINED, so claimCount == 0 may just mean the drain dispatched nothing at all:\n%s", logger.joined())
	}
	// THE DECLINE MUST RENDER THE HOLD, because the hold is what decides which of the two cases this
	// is: factory feedstock the next pause recovers on its own, versus a closed-bill gate material a
	// terminal factory will not buy back and only a human can move. Derived from the fixture, so a
	// line that dropped the hold fails rather than matching static prose.
	if want := "FULL hold (40 " + gateMaterialPrimary + ")"; !strings.Contains(logger.joined(), want) {
		t.Fatalf("the decline does not render %q, so an operator cannot tell a recoverable wedge from one needing intervention:\n%s", want, logger.joined())
	}
	// And it must no longer state the intervention UNCONDITIONALLY. That advice was wrong for the
	// commonest cargo — a factory hull's feedstock, which the next delivery pause hands back to the
	// factory role and feedGateLegFromHold unloads for free.
	if strings.Contains(logger.joined(), "before it can work again") {
		t.Fatalf("the decline still advises manual intervention unconditionally, for a state the reallocation recovers by itself whenever the cargo is factory feedstock:\n%s", logger.joined())
	}
}

// sp-xcjuy: THE PRECHECK MUST PRICE THE MINIMUM TRANCHE, NOT THE FULL ONE.
//
// fillFromSource resizes a breaching tranche down to the largest affordable one, but the planner
// runs FIRST — so a precheck that prices the full tranche declines the step before the resize can
// ever happen, and the fix is inert. That is the 78-minute gate stall exactly: 60 IRON at 4,044 was
// refused while a 25-unit tranche had ~87k of room.
//
// Found by mutation probe, not by review: reverting the precheck to the full tranche compiled and
// killed no test at all.
func TestFeedGateLeg_TakesAStepWhoseMinimumTrancheFitsEvenWhenTheFullOneDoesNot(t *testing.T) {
	f := newGateFactoryHandler(t)
	// The incident's market: IRON at 4,044 with a 60-unit trade volume.
	f.topo.priceByGood = map[string]int{"IRON": 4044}
	f.topo.volumeByGood = map[string]int{"IRON": 60}
	// Full 60 x 4044 = 242,640 (breaches); minimum 25 x 4044 = 101,100 (fits).
	f.buyer.spendHeadroom = 150_000
	assertIronIsPlannedBeforeQuartz(t, f)

	f.runFeed(t, "GF-1")

	bought := f.buyer.goods()
	if _, boughtIron := bought["IRON"]; !boughtIron {
		t.Fatalf("bought %v — IRON was declined although a %d-unit minimum tranche (%d credits) fits inside %d of headroom. Pricing the FULL tranche here refuses a step the buy would have completed, and the refusal happens first, so the resize never runs",
			bought, mfgServices.MinViableTrancheUnits, mfgServices.MinViableTrancheUnits*4044, f.buyer.spendHeadroom)
	}

	// NON-VACUITY: the FULL tranche must genuinely be unaffordable, or this passes for a precheck
	// that never narrowed anything.
	if full := 60 * 4044; full <= f.buyer.spendHeadroom {
		t.Fatalf("fixture is inert: the full tranche costs %d against %d headroom, so it was affordable all along", full, f.buyer.spendHeadroom)
	}
}

// AND A STEP NO SIZE OF WHICH FITS IS STILL DECLINED, with its own wording (criterion 5).
func TestFeedGateLeg_DeclinesWhenEvenTheMinimumTrancheBreaches(t *testing.T) {
	f := newGateFactoryHandler(t)
	f.topo.priceByGood = map[string]int{"IRON": 4044, "QUARTZ_SAND": 23}
	f.topo.volumeByGood = map[string]int{"IRON": 60}
	// 25 x 4044 = 101,100 — above this headroom, so no size of the IRON step is affordable.
	f.buyer.spendHeadroom = 50_000

	f.runFeed(t, "GF-1")

	if _, boughtIron := f.buyer.goods()["IRON"]; boughtIron {
		t.Fatal("bought IRON although even the minimum tranche breaches the reserve")
	}
	if want := "even the MINIMUM"; !strings.Contains(f.logLines(), want) {
		t.Fatalf("no %q line: 'no size of this step is affordable' must not share wording with an ordinary decline:\n%s", want, f.logLines())
	}
}
