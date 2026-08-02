package parkedsensing

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/yardscan"
)

// yardqueue_test.go pins the yard-aware ordering and the ABSOLUTE
// precedence it was later overruled into (sp-0j5hi).
//
// TWO TESTS HERE ASSERT THE OPPOSITE OF WHAT THEY USED TO, and that is the change
// rather than a break. TestDrainCandidates_CoverageStillOutranksTheYardTerm became
// TestDrainCandidates_ADarkYardAtHighCoverageStillBeatsEveryMarketAtZero, and the
// heavy-versus-probe test now asserts that a probe-only yard is ordered SECOND
// rather than excluded — "ALL yards of a system" means the demand weighting orders
// the tier instead of gating entry to it.
//
// WHAT THE FIXTURES HAVE TO DO, because a fixture that does not do it produces a
// test that passes with the feature deleted. The drain works maxDrainAttempts = 6
// placements a tick out of 8,930 outstanding ones, so an ordering term is only
// load-bearing when demand EXCEEDS the head it competes for. Every fixture below
// therefore saturates the queue with ordinary non-yard placements that would
// otherwise fill the head on their own, and asserts on membership of the first
// maxDrainAttempts — never merely on relative order in a list where everything
// fits. saturatedQueue is where that saturation is built and asserted.

// fakeYard is one shipyard as the read budget knows it: what it sells, whether we
// hold a price, and whether it stocks a class the fleet's acquisition path buys.
type fakeYard struct {
	waypoint string
	system   string
	heavy    bool
	facts    yardscan.Facts
}

// darkYard is the state this whole bead is about: a CONFIRMED seller of a hull we
// buy whose price the API will not disclose because nobody of ours is standing
// there.
func darkYard(waypoint, system string, heavy bool) fakeYard {
	return fakeYard{
		waypoint: waypoint,
		system:   system,
		heavy:    heavy,
		facts:    yardscan.Facts{SellsWanted: true},
	}
}

// fakeYardDemand stands in for YardScanBudget.PresenceRequests, and it is a
// FAITHFUL stand-in rather than a hand-ordered list on purpose.
//
// It applies the real yardscan.WantsPresence and the real yardscan.RankPresence,
// so the demand weighting these tests assert on is the shipped one rather than an
// ordering the fixture quietly supplied itself. That is what lets a mutation to
// RankPresence's heavy clause fail a test in THIS package: if the fake pre-sorted
// its own output, the weighting would be untested here and the mutation would
// survive.
type fakeYardDemand struct {
	yards []fakeYard

	calls  int
	limits []int
}

func (f *fakeYardDemand) PresenceRequests(_ context.Context, _ int, limit int) []yardscan.PresenceRequest {
	f.calls++
	f.limits = append(f.limits, limit)
	if limit <= 0 {
		return nil
	}
	requests := make([]yardscan.PresenceRequest, 0, len(f.yards))
	for _, y := range f.yards {
		if !yardscan.WantsPresence(y.facts) {
			continue
		}
		requests = append(requests, yardscan.PresenceRequest{
			Waypoint: y.waypoint,
			System:   y.system,
			Heavy:    y.heavy,
			Weight:   yardscan.Weight(y.facts, 8),
		})
	}
	ranked := yardscan.RankPresence(requests)
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	return ranked
}

// AdmitPresence exists so the fake also satisfies YardPresenceDemand, which is
// what the coordinator hands the drain. It must never be reached from here: the
// drain orders a queue and moves no hull, so consuming the reposition allowance
// would starve the pass that does.
func (f *fakeYardDemand) AdmitPresence() bool {
	panic("the buy queue must not consume the presence allowance: it orders placements, it does not reposition hulls")
}

// rivalSystems builds n IN_SCOPE systems of `each` ordinary market placements,
// none of them a shipyard, all DEEPER than the system under test.
//
// This is the saturation. Every one of these systems ranks its first placement at
// coverage 0 — no hull stands anywhere in this fixture — so they fill the head of
// the queue on depth alone, which is exactly the live condition: 8,934 WANTED rows
// against 705 parked hulls, ordered by three terms none of which knows what a
// shipyard is.
func rivalSystems(n, each int, depth int64) ([]QueuedSlot, []ScreenedSystem) {
	var slots []QueuedSlot
	var systems []ScreenedSystem
	for i := 0; i < n; i++ {
		system := fmt.Sprintf("X1-RIVAL%d", i)
		for j := 0; j < each; j++ {
			slots = append(slots, QueuedSlot{
				Waypoint: fmt.Sprintf("%s-M%d", system, j),
				System:   system,
				Kind:     SlotKindMarket,
				State:    SlotStateWanted,
			})
		}
		systems = append(systems, ScreenedSystem{System: system, DepthCredits: depth})
	}
	return slots, systems
}

// yardQueuePorts wires a drain with a ledger and a demand reader and nothing
// else. Gates is left nil deliberately: reachableFills then fails OPEN and filters
// nothing, so these tests measure the ordering rather than the reachability
// partition. TestDrainCandidates_AnUnreachableDarkYardIsNotPromoted wires it.
func yardQueuePorts(slots []QueuedSlot, systems []ScreenedSystem, demand YardDemandReader) (BuyPorts, *fakeBuyLedger) {
	led := &fakeBuyLedger{slots: slots, systems: systems}
	return BuyPorts{Ledger: led, YardDemand: demand}, led
}

// head returns the waypoints of the first maxDrainAttempts candidates — the only
// part of the queue a tick can reach, and therefore the only part an ordering
// claim may be made about.
func head(candidates []QueuedSlot) []string {
	out := make([]string, 0, maxDrainAttempts)
	for i, slot := range candidates {
		if i >= maxDrainAttempts {
			break
		}
		out = append(out, slot.Waypoint)
	}
	return out
}

func inHead(candidates []QueuedSlot, waypoint string) bool {
	for _, w := range head(candidates) {
		if w == waypoint {
			return true
		}
	}
	return false
}

func positionOf(candidates []QueuedSlot, waypoint string) int {
	for i, slot := range candidates {
		if slot.Waypoint == waypoint {
			return i
		}
	}
	return -1
}

// saturatedQueue is the fixture the main claim rests on.
//
// X1-DARK holds fourteen outstanding placements and the twelfth of them stands on
// a heavy-freighter counter we cannot price. Eight rival systems hold two ordinary
// placements each. Nothing is parked anywhere, so every system's first placement
// competes at coverage 0.
//
// WITHOUT the yard terms this is the live defect in miniature: the yard takes its
// system's twelfth FIFO offset and competes at coverage 11, while nine placements
// sit at coverage 0 and nine more at coverage 1 — so it lands around the twentieth
// place of a queue whose head is six. The assertion helper below refuses to run
// unless that is actually true of the fixture.
func saturatedQueue() ([]QueuedSlot, []ScreenedSystem, *fakeYardDemand) {
	rivals, rivalSys := rivalSystems(8, 2, 9_000)

	dark := make([]QueuedSlot, 0, 14)
	for i := 0; i < 11; i++ {
		dark = append(dark, QueuedSlot{
			Waypoint: fmt.Sprintf("X1-DARK-M%02d", i),
			System:   "X1-DARK",
			Kind:     SlotKindMarket,
			State:    SlotStateWanted,
		})
	}
	dark = append(dark, QueuedSlot{
		Waypoint: "X1-DARK-Y1", System: "X1-DARK", Kind: SlotKindMarket, State: SlotStateWanted,
	})
	for i := 11; i < 13; i++ {
		dark = append(dark, QueuedSlot{
			Waypoint: fmt.Sprintf("X1-DARK-M%02d", i),
			System:   "X1-DARK",
			Kind:     SlotKindMarket,
			State:    SlotStateWanted,
		})
	}

	slots := append(dark, rivals...)
	// X1-DARK is the POOREST system in the fixture, so the depth tiebreak is
	// actively working against the yard. A promotion that only happens in a rich
	// system would not be the fix: 56 of the 78 unfilled heavy yards sit in systems
	// holding no probe at all, which are not the fleet's deep ones.
	systems := append([]ScreenedSystem{{System: "X1-DARK", DepthCredits: 100}}, rivalSys...)

	return slots, systems, &fakeYardDemand{yards: []fakeYard{darkYard("X1-DARK-Y1", "X1-DARK", true)}}
}

// assertFixtureSaturates fails if the fixture would let the yard reach the head on
// its own. It is the guard against the failure mode this bead's acceptance calls
// out by name — a bound is never exercised unless demand exceeds it, so a fixture
// where everything fits is a test that passes with the feature deleted.
func assertFixtureSaturates(t *testing.T, slots []QueuedSlot, systems []ScreenedSystem, waypoint string) {
	t.Helper()
	ports, _ := yardQueuePorts(slots, systems, nil) // nil demand: the yard-blind ordering
	blind, _, err := drainCandidates(context.Background(), ports, testPlayerID)
	if err != nil {
		t.Fatalf("drainCandidates returned error building the blind baseline: %v", err)
	}
	if len(blind) <= maxDrainAttempts {
		t.Fatalf("FIXTURE IS NOT SATURATED: %d candidates against a head of %d, so every placement fits and "+
			"no ordering term can be load-bearing", len(blind), maxDrainAttempts)
	}
	if inHead(blind, waypoint) {
		t.Fatalf("FIXTURE IS NOT SATURATED: %s already reaches the head at position %d with the yard term "+
			"switched off, so this test would pass with the feature deleted. blind head=%v",
			waypoint, positionOf(blind, waypoint), head(blind))
	}
}

// TestDrainCandidates_ADarkHeavyYardReachesTheHeadOfASaturatedQueue is the bead.
//
// 78 of the 85 known heavy-freighter counters sat in 8,934 WANTED rows against 705
// parked hulls with nothing marking any of them, so a queue working correctly by
// its own lights — coverage, then system depth, then arrival — reached them
// approximately never. That is why 116 manned yards and 117 priced yards matched
// one for one: a yard got priced only where a hull already happened to stand.
//
// It kills BOTH ordering terms at once, which is deliberate: each is necessary and
// neither is sufficient. Without the per-system offset the yard competes at
// coverage 11 and never meets the head. Without the equal-coverage tiebreak it
// competes at coverage 0 and loses to eight deeper systems, and the head is six.
func TestDrainCandidates_ADarkHeavyYardReachesTheHeadOfASaturatedQueue(t *testing.T) {
	slots, systems, demand := saturatedQueue()
	assertFixtureSaturates(t, slots, systems, "X1-DARK-Y1")

	ports, _ := yardQueuePorts(slots, systems, demand)
	got, _, err := drainCandidates(context.Background(), ports, testPlayerID)
	if err != nil {
		t.Fatalf("drainCandidates returned error: %v", err)
	}

	if !inHead(got, "X1-DARK-Y1") {
		t.Fatalf("the heavy counter we cannot price sits at position %d of %d and never reaches the six "+
			"placements this tick can work. head=%v", positionOf(got, "X1-DARK-Y1"), len(got), head(got))
	}
	if demand.calls != 1 {
		t.Fatalf("asked the shipyard budget %d times in one drain, want exactly 1", demand.calls)
	}
}

// TestDrainCandidates_ADarkYardTakesItsSystemsFirstPlacement isolates the per-system
// offset term.
//
// The yard's system is the DEEPEST here, so the equal-coverage tiebreak is already
// on the yard's side and cannot be what carries it — only the offset can. This is
// the term that matters most on the live fleet: 56 of the 78 unfilled heavy yards
// sit in 47 systems holding no probe at all, and those systems carry 775
// outstanding placements between them, so a heavy yard's ledger position put it
// around the sixteenth row of its own system.
func TestDrainCandidates_ADarkYardTakesItsSystemsFirstPlacement(t *testing.T) {
	rivals, rivalSys := rivalSystems(8, 2, 900)
	var slots []QueuedSlot
	for i := 0; i < 11; i++ {
		slots = append(slots, QueuedSlot{
			Waypoint: fmt.Sprintf("X1-DEEP-M%02d", i), System: "X1-DEEP",
			Kind: SlotKindMarket, State: SlotStateWanted,
		})
	}
	slots = append(slots, QueuedSlot{
		Waypoint: "X1-DEEP-Y1", System: "X1-DEEP", Kind: SlotKindMarket, State: SlotStateWanted,
	})
	slots = append(slots, rivals...)
	systems := append([]ScreenedSystem{{System: "X1-DEEP", DepthCredits: 90_000}}, rivalSys...)

	assertFixtureSaturates(t, slots, systems, "X1-DEEP-Y1")

	ports, _ := yardQueuePorts(slots, systems, &fakeYardDemand{
		yards: []fakeYard{darkYard("X1-DEEP-Y1", "X1-DEEP", true)},
	})
	got, _, err := drainCandidates(context.Background(), ports, testPlayerID)
	if err != nil {
		t.Fatalf("drainCandidates returned error: %v", err)
	}

	if got[0].Waypoint != "X1-DEEP-Y1" {
		t.Fatalf("the dark yard did not take its system's first placement: queue head is %q, yard at %d. head=%v",
			got[0].Waypoint, positionOf(got, "X1-DEEP-Y1"), head(got))
	}
	// Its system-mates must still be behind it in FIFO order. The offset term
	// REORDERS one placement, it does not shuffle the rest: FIFO per system is what
	// stops a placement being overtaken by a newer one, tick after tick.
	first, second := positionOf(got, "X1-DEEP-M00"), positionOf(got, "X1-DEEP-M01")
	if first < 0 || second < 0 || first > second {
		t.Fatalf("promoting the yard disturbed its system's FIFO order: M00 at %d, M01 at %d", first, second)
	}
}

// TestDrainCandidates_ADarkYardOutranksADeeperSystemAtEqualCoverage isolates the
// equal-coverage tiebreak.
//
// The yard is already its system's FIRST placement here, so the offset term does
// nothing and only the tiebreak can carry it. This is where every system's first
// placement meets: with nothing parked anywhere they all rank at coverage 0, and
// the only thing separating a dark heavy counter from an ordinary market was which
// system happened to be richer.
func TestDrainCandidates_ADarkYardOutranksADeeperSystemAtEqualCoverage(t *testing.T) {
	rivals, rivalSys := rivalSystems(9, 1, 90_000)
	slots := append([]QueuedSlot{
		{Waypoint: "X1-POOR-Y1", System: "X1-POOR", Kind: SlotKindMarket, State: SlotStateWanted},
	}, rivals...)
	systems := append([]ScreenedSystem{{System: "X1-POOR", DepthCredits: 1}}, rivalSys...)

	assertFixtureSaturates(t, slots, systems, "X1-POOR-Y1")

	ports, _ := yardQueuePorts(slots, systems, &fakeYardDemand{
		yards: []fakeYard{darkYard("X1-POOR-Y1", "X1-POOR", true)},
	})
	got, _, err := drainCandidates(context.Background(), ports, testPlayerID)
	if err != nil {
		t.Fatalf("drainCandidates returned error: %v", err)
	}

	if got[0].Waypoint != "X1-POOR-Y1" {
		t.Fatalf("at equal coverage a 90,000-deep ordinary market still outranked a heavy counter we cannot "+
			"price: head is %q, yard at %d. head=%v", got[0].Waypoint, positionOf(got, "X1-POOR-Y1"), head(got))
	}
}

// TestDrainCandidates_AHeavyYardOutranksAProbeOnlyYardInsideTheTier pins the demand
// weighting, which under absolute precedence orders the yard tier rather than
// gating entry to it.
//
// One unpriced heavy counter is worth more than every probe yard on the map put
// together — the incident behind sp-mb0er had the fleet buying heavies at up to
// 2,288,156 against a visible cheapest of 1,918,293, chosen from four prices out of
// eighty-five. A probe-only yard is still a yard and still gets manned (that is the
// "ALL yards" half of the directive), but it goes SECOND.
//
// THE TWO YARDS ARE IN DIFFERENT SYSTEMS, BOTH UNCOVERED, so they meet at coverage
// 0 and the RankPresence key is the only term that can separate them. Putting them
// in one system would let yardFirstOffsets decide it instead and leave the sort's
// key untested.
//
// THE FIXTURE DEFEATS THE THREE CHEAP WAYS OF PASSING. The probe yard is named
// X1-AAA-A1 and the heavy X1-ZZZ-Z9, so RankPresence's SYMBOL tiebreak favours the
// probe yard; the probe yard's system is 900x DEEPER, so the depth tiebreak favours
// it too; and it is written FIRST in the ledger, so stable order favours it. Only
// the Heavy clause can put Z9 ahead.
func TestDrainCandidates_AHeavyYardOutranksAProbeOnlyYardInsideTheTier(t *testing.T) {
	rivals, rivalSys := rivalSystems(8, 1, 9_000)
	slots := append([]QueuedSlot{
		{Waypoint: "X1-AAA-A1", System: "X1-AAA", Kind: SlotKindMarket, State: SlotStateWanted},
		{Waypoint: "X1-ZZZ-Z9", System: "X1-ZZZ", Kind: SlotKindMarket, State: SlotStateWanted},
	}, rivals...)
	systems := append([]ScreenedSystem{
		{System: "X1-AAA", DepthCredits: 900_000},
		{System: "X1-ZZZ", DepthCredits: 1_000},
	}, rivalSys...)

	ports, _ := yardQueuePorts(slots, systems, &fakeYardDemand{yards: []fakeYard{
		darkYard("X1-AAA-A1", "X1-AAA", false), // probes only
		darkYard("X1-ZZZ-Z9", "X1-ZZZ", true),  // heavy freighters
	}})
	got, _, err := drainCandidates(context.Background(), ports, testPlayerID)
	if err != nil {
		t.Fatalf("drainCandidates returned error: %v", err)
	}

	heavy, probe := positionOf(got, "X1-ZZZ-Z9"), positionOf(got, "X1-AAA-A1")
	if heavy != 0 {
		t.Fatalf("the heavy counter is at %d, want 0: inside the yard tier RankPresence puts heavies "+
			"unconditionally first, whatever the symbol, the depth or the ledger order say. head=%v",
			heavy, head(got))
	}
	// Both are still manned — the weighting orders the tier, it does not gate it.
	if probe != 1 {
		t.Fatalf("the probe-only yard is at %d, want 1: it ranks BEHIND the heavy counter and AHEAD of "+
			"every ordinary market. All yards get manned. head=%v", probe, head(got))
	}
}

// manyYardSystem builds one system holding `yards` dark heavy counters plus one
// ordinary market, named so that RankPresence's symbol tiebreak ranks all of them
// AHEAD of anything named later — which is what makes the concentration test below
// able to fail.
func manyYardSystem(system string, yards int) ([]QueuedSlot, []fakeYard) {
	slots := make([]QueuedSlot, 0, yards+1)
	facts := make([]fakeYard, 0, yards)
	for i := 0; i < yards; i++ {
		waypoint := fmt.Sprintf("%s-Y%d", system, i)
		slots = append(slots, QueuedSlot{
			Waypoint: waypoint, System: system, Kind: SlotKindMarket, State: SlotStateWanted,
		})
		facts = append(facts, darkYard(waypoint, system, true))
	}
	slots = append(slots, QueuedSlot{
		Waypoint: system + "-M1", System: system, Kind: SlotKindMarket, State: SlotStateWanted,
	})
	return slots, facts
}

// TestDrainCandidates_EveryYardOfAMultiYardSystemIsPromoted is the "ALL yards of a
// system!" half of the directive, and it is the assertion a one-yard-per-system
// fixture is structurally unable to make.
//
// FOUR yards, because three is the minimum that distinguishes the rules and four
// leaves margin: 677 systems on the live map hold two or more shipyards and the
// largest holds eight. Under sp-7qhum's within-system promotion the system's yards
// take its coverage run 0,1,2,3 and only the FIRST of them ever meets the
// coverage-0 group, so three of the four sit behind every rival market — which is
// exactly what this fixture would show with the tier partition removed.
func TestDrainCandidates_EveryYardOfAMultiYardSystemIsPromoted(t *testing.T) {
	// Ten rival systems each ranking a market at coverage 0, against a head of six,
	// and all of them deeper than the yard system.
	rivals, rivalSys := rivalSystems(10, 2, 90_000)
	yardSlots, yardFacts := manyYardSystem("X1-MANY", 4)
	slots := append(yardSlots, rivals...)
	systems := append([]ScreenedSystem{{System: "X1-MANY", DepthCredits: 100}}, rivalSys...)

	// Every yard past the first must be out of reach with the term switched off, or
	// the claim below is not load-bearing for it.
	for i := 1; i < 4; i++ {
		assertFixtureSaturates(t, slots, systems, fmt.Sprintf("X1-MANY-Y%d", i))
	}

	ports, _ := yardQueuePorts(slots, systems, &fakeYardDemand{yards: yardFacts})
	got, _, err := drainCandidates(context.Background(), ports, testPlayerID)
	if err != nil {
		t.Fatalf("drainCandidates returned error: %v", err)
	}

	for i := 0; i < 4; i++ {
		waypoint := fmt.Sprintf("X1-MANY-Y%d", i)
		if at := positionOf(got, waypoint); at != i {
			t.Fatalf("%s sits at %d, want %d. ALL of a system's dark yards outrank every ordinary market — "+
				"there is no per-system cap and no first-yard-only rule. head=%v", waypoint, at, i, head(got))
		}
	}
	// The system's ordinary market is NOT promoted with them: the tier is yards, not
	// systems that contain yards.
	if inHead(got, "X1-MANY-M1") {
		t.Fatalf("the yard system's ordinary market reached the head at %d — a system with yards had its "+
			"whole backlog promoted. head=%v", positionOf(got, "X1-MANY-M1"), head(got))
	}
}

// TestDrainCandidates_TheYardTierDoesNotConcentrateOnOneSystem is the guarantee
// that makes absolute precedence safe to ship.
//
// Coverage-first was introduced because depth alone put 67% of parked probes in
// three systems, and the tier partition removes coverage's authority OVER the yard
// tier. What replaces it is coverage's authority INSIDE the tier: a system's dark
// yards take its coverage indices 0,1,2,… so its second yard ranks behind every
// other system's first, and no system takes more than one place in a coverage band.
//
// THE FIXTURE IS BUILT SO THAT DROPPING COVERAGE FROM INSIDE THE TIER FAILS IT.
// X1-AAA's four yards are named to sort first under RankPresence's symbol tiebreak
// and every yard here is heavy with equal weight, so a yard tier ordered on the
// presence key alone would hand X1-AAA the first four places outright.
func TestDrainCandidates_TheYardTierDoesNotConcentrateOnOneSystem(t *testing.T) {
	// Eight rival systems ranking an ordinary market at coverage 0. They are not the
	// subject of the test, but without them a yard DEMOTED out of the tier would
	// still land immediately behind the coverage-0 yards and the position assertion
	// below could not tell a promoted yard from a demoted one.
	rivals, rivalSys := rivalSystems(8, 1, 9_000)
	yardSlots, yardFacts := manyYardSystem("X1-AAA", 4)
	slots := append([]QueuedSlot(nil), yardSlots...)
	systems := []ScreenedSystem{{System: "X1-AAA", DepthCredits: 90_000}}
	for _, system := range []string{"X1-BBB", "X1-CCC", "X1-DDD", "X1-EEE"} {
		waypoint := system + "-Y0"
		slots = append(slots, QueuedSlot{
			Waypoint: waypoint, System: system, Kind: SlotKindMarket, State: SlotStateWanted,
		})
		systems = append(systems, ScreenedSystem{System: system, DepthCredits: 100})
		yardFacts = append(yardFacts, darkYard(waypoint, system, true))
	}
	slots = append(slots, rivals...)
	systems = append(systems, rivalSys...)

	ports, _ := yardQueuePorts(slots, systems, &fakeYardDemand{yards: yardFacts})
	got, _, err := drainCandidates(context.Background(), ports, testPlayerID)
	if err != nil {
		t.Fatalf("drainCandidates returned error: %v", err)
	}

	// Eight dark yards compete for six places, so the tier itself is saturated.
	// Five systems rank a yard at coverage 0; that band is the first five places.
	const coverageZeroBand = 5
	var fromAAA []string
	for i := 0; i < coverageZeroBand && i < len(got); i++ {
		if got[i].System == "X1-AAA" {
			fromAAA = append(fromAAA, got[i].Waypoint)
		}
	}
	if len(fromAAA) != 1 {
		t.Fatalf("X1-AAA took %v of the five coverage-0 places, want exactly one. Coverage still orders "+
			"WITHIN the yard tier: a system's second yard ranks behind every other system's first, which "+
			"is what stops absolute precedence re-concentrating the fleet. order=%v", fromAAA, head(got))
	}
	// Its remaining yards still outrank all eight rival markets, so the coverage-1
	// band of the YARD tier opens immediately after the coverage-0 one. This is the
	// assertion that fails if the tier is capped at one yard per system: a demoted
	// X1-AAA-Y1 would sit behind the eight coverage-0 markets instead.
	if at := positionOf(got, "X1-AAA-Y1"); at != coverageZeroBand {
		t.Fatalf("X1-AAA-Y1 sits at %d, want %d: every yard outranks every market, so the yard tier's "+
			"coverage-1 band opens immediately after its coverage-0 one and no market comes between "+
			"them. head=%v", at, coverageZeroBand, head(got))
	}
}

// TestDrainCandidates_APricedYardIsNotPromoted is the retraction property.
//
// Presence buys exactly one thing — the listings array — so a yard we already hold
// a price for has already given it to us. This is why the fact is PULLED every tick
// and never recorded on the row: a stored yard flag would latch, and the queue
// would keep promoting a counter that stopped needing anything the moment a hull
// arrived and priced it.
func TestDrainCandidates_APricedYardIsNotPromoted(t *testing.T) {
	slots, systems, _ := saturatedQueue()
	priced := &fakeYardDemand{yards: []fakeYard{{
		waypoint: "X1-DARK-Y1", system: "X1-DARK", heavy: true,
		facts: yardscan.Facts{SellsWanted: true, Priced: true},
	}}}

	ports, _ := yardQueuePorts(slots, systems, priced)
	got, _, err := drainCandidates(context.Background(), ports, testPlayerID)
	if err != nil {
		t.Fatalf("drainCandidates returned error: %v", err)
	}
	if inHead(got, "X1-DARK-Y1") {
		t.Fatalf("promoted a yard whose price we already hold — presence there would buy nothing. head=%v", head(got))
	}
}

// TestDrainCandidates_AnUnknownYardIsNotPromoted guards the other half of
// WantsPresence.
//
// An unopened catalogue MIGHT hold a heavy, and the read budget ranks it highly for
// exactly that reason — but what it needs is a catalogue READ, which costs one
// request and no hull, and the free presence-less sweep already does it. Promoting
// it here would spend the queue's scarce head on yards that turn out to sell
// nothing we buy.
func TestDrainCandidates_AnUnknownYardIsNotPromoted(t *testing.T) {
	slots, systems, _ := saturatedQueue()
	unknown := &fakeYardDemand{yards: []fakeYard{{
		waypoint: "X1-DARK-Y1", system: "X1-DARK", heavy: true,
		facts: yardscan.Facts{Unknown: true},
	}}}

	ports, _ := yardQueuePorts(slots, systems, unknown)
	got, _, err := drainCandidates(context.Background(), ports, testPlayerID)
	if err != nil {
		t.Fatalf("drainCandidates returned error: %v", err)
	}
	if inHead(got, "X1-DARK-Y1") {
		t.Fatalf("promoted a yard whose catalogue has never been read. head=%v", head(got))
	}
}

// TestDrainCandidates_ADarkYardAtHighCoverageStillBeatsEveryMarketAtZero IS THE
// FALSIFIER for sp-0j5hi, and it is the exact case sp-7qhum's ordering gets wrong.
//
// That version left coverage-first as the top-level key and promoted a yard only
// within its OWN system's run of coverage values, which reads as a safe change and
// is why it shipped. What it means in the live ledger is that a yard in a system
// already holding four probes competes at coverage 4 and is beaten by every one of
// ~8,900 coverage-0 market rows in every other system. A tiebreak inside coverage
// cannot reach a set that never ties: 90 minutes with buying restored bought 56
// probes, 5 of which landed on a yard, and the heavy-yard priced count stayed at 4
// of 86.
//
// The Admiral's directive of 2026-07-31 overruled the constraint that produced it:
// "yards should take absolute precedence over other markets! They should be the
// first ones to be manned." So X1-COVERED-Y1 here sits in the most heavily covered
// system in the fixture, behind four parked hulls, and it must still take the head
// of the queue ahead of nine systems holding nothing at all.
//
// This test previously asserted the opposite (TestDrainCandidates_
// CoverageStillOutranksTheYardTerm). It is inverted deliberately, not broken.
func TestDrainCandidates_ADarkYardAtHighCoverageStillBeatsEveryMarketAtZero(t *testing.T) {
	// Nine uncovered rival systems, each ranking a market at coverage 0, against a
	// head of six. Their depth is the highest in the fixture, so every ordinary
	// tiebreak in the sort is working against the yard.
	rivals, rivalSys := rivalSystems(9, 2, 90_000)
	covered := []QueuedSlot{
		{Waypoint: "X1-COVERED-M1", System: "X1-COVERED", Kind: SlotKindMarket, State: SlotStateWanted},
		{Waypoint: "X1-COVERED-Y1", System: "X1-COVERED", Kind: SlotKindMarket, State: SlotStateWanted},
	}
	// Four hulls already parked, so the yard competes at coverage 4 — the state the
	// shipped ordering is structurally unable to promote out of.
	for i := 0; i < 4; i++ {
		covered = append(covered, QueuedSlot{
			Waypoint: fmt.Sprintf("X1-COVERED-P%d", i), System: "X1-COVERED",
			Kind: SlotKindMarket, State: SlotStateParked, AssignedShip: fmt.Sprintf("PROBE-%d", i),
		})
	}
	slots := append(covered, rivals...)
	systems := append([]ScreenedSystem{{System: "X1-COVERED", DepthCredits: 100}}, rivalSys...)

	assertFixtureSaturates(t, slots, systems, "X1-COVERED-Y1")

	ports, _ := yardQueuePorts(slots, systems, &fakeYardDemand{
		yards: []fakeYard{darkYard("X1-COVERED-Y1", "X1-COVERED", true)},
	})
	got, _, err := drainCandidates(context.Background(), ports, testPlayerID)
	if err != nil {
		t.Fatalf("drainCandidates returned error: %v", err)
	}

	if got[0].Waypoint != "X1-COVERED-Y1" {
		t.Fatalf("a dark heavy counter at coverage 4 lost to a market at coverage 0: head is %q, yard at "+
			"%d of %d. Yards take ABSOLUTE precedence — coverage orders WITHIN the tier, it does not "+
			"outrank it. head=%v", got[0].Waypoint, positionOf(got, "X1-COVERED-Y1"), len(got), head(got))
	}
	// And the market beside it in the same covered system is NOT dragged along: the
	// promotion is of the yard, not of its system.
	if inHead(got, "X1-COVERED-M1") {
		t.Fatalf("promoting the yard dragged its system's ordinary market into the head as well: M1 at %d. "+
			"head=%v", positionOf(got, "X1-COVERED-M1"), head(got))
	}
}

// TestDrainCandidates_TheMarketTierIsStillOrderedCoverageFirst is the other side of
// the partition, and it is what stops "absolute precedence" being read as "throw
// the ordering away".
//
// Coverage-first exists because depth alone put 67% of parked probes in three
// systems. Nothing about the yard tier changes that for the markets: below the
// partition the queue must order exactly as it always did, coverage ascending with
// depth as the tiebreak. The fixture holds no yard at all in the covered system, so
// only the market tier is under test.
func TestDrainCandidates_TheMarketTierIsStillOrderedCoverageFirst(t *testing.T) {
	rivals, rivalSys := rivalSystems(3, 1, 100)
	slots := append([]QueuedSlot{
		{Waypoint: "X1-RICH-P1", System: "X1-RICH", Kind: SlotKindMarket, State: SlotStateParked, AssignedShip: "PROBE-1"},
		{Waypoint: "X1-RICH-M1", System: "X1-RICH", Kind: SlotKindMarket, State: SlotStateWanted},
		{Waypoint: "X1-LONE-Y1", System: "X1-LONE", Kind: SlotKindMarket, State: SlotStateWanted},
	}, rivals...)
	systems := append([]ScreenedSystem{
		{System: "X1-RICH", DepthCredits: 900_000},
		{System: "X1-LONE", DepthCredits: 1},
	}, rivalSys...)

	ports, _ := yardQueuePorts(slots, systems, &fakeYardDemand{
		yards: []fakeYard{darkYard("X1-LONE-Y1", "X1-LONE", true)},
	})
	got, _, err := drainCandidates(context.Background(), ports, testPlayerID)
	if err != nil {
		t.Fatalf("drainCandidates returned error: %v", err)
	}

	// The yard takes the head, then the market tier: three uncovered rivals at
	// coverage 0 before the 900,000-deep covered system's market at coverage 1.
	if got[0].Waypoint != "X1-LONE-Y1" {
		t.Fatalf("the yard did not take the head: got %q. head=%v", got[0].Waypoint, head(got))
	}
	if rich := positionOf(got, "X1-RICH-M1"); rich != 4 {
		t.Fatalf("X1-RICH-M1 sits at %d, want 4: below the yard tier the market ordering is unchanged — "+
			"three uncovered rivals at coverage 0, then the covered system at coverage 1, however deep it "+
			"is. order=%v", rich, head(got))
	}
}

// TestDrainCandidates_APromotedYardDoesNotBringItsSystemAlong is the other half of
// the same guarantee, and the one that would break if the yard's system — rather
// than the yard — were what got promoted.
//
// X1-DARK holds fourteen outstanding placements against the rivals' two each. It
// gets ONE place in the coverage-0 group, exactly as it did before, and that place
// is the yard. Its other thirteen placements are still behind every rival's first.
func TestDrainCandidates_APromotedYardDoesNotBringItsSystemAlong(t *testing.T) {
	slots, systems, demand := saturatedQueue()
	ports, _ := yardQueuePorts(slots, systems, demand)
	got, _, err := drainCandidates(context.Background(), ports, testPlayerID)
	if err != nil {
		t.Fatalf("drainCandidates returned error: %v", err)
	}

	// Nine systems, so the coverage-0 group is the first nine places.
	const coverageZeroGroup = 9
	var fromDark []string
	for i := 0; i < coverageZeroGroup && i < len(got); i++ {
		if got[i].System == "X1-DARK" {
			fromDark = append(fromDark, got[i].Waypoint)
		}
	}
	if len(fromDark) != 1 || fromDark[0] != "X1-DARK-Y1" {
		t.Fatalf("X1-DARK took %v of the nine coverage-0 places, want exactly [X1-DARK-Y1]. A system with a "+
			"dark yard gets its yard promoted, not its whole backlog. order=%v", fromDark, got[:coverageZeroGroup])
	}
}

// TestDrainCandidates_AnUnwiredYardDemandOrdersExactlyAsBefore is the fail-open
// direction.
//
// This term can only reorder placements the queue had already decided it wanted, so
// a blind read must mean "do not prioritise" and never "do not buy" — the same
// asymmetry reachableFills documents against the money guards beside it. The
// expected order is written out rather than compared against a second run, so this
// pins the pre-existing ordering rather than restating the implementation.
func TestDrainCandidates_AnUnwiredYardDemandOrdersExactlyAsBefore(t *testing.T) {
	slots := []QueuedSlot{
		{Waypoint: "X1-AA-M1", System: "X1-AA", Kind: SlotKindMarket, State: SlotStateWanted},
		{Waypoint: "X1-AA-Y1", System: "X1-AA", Kind: SlotKindMarket, State: SlotStateWanted},
		{Waypoint: "X1-BB-M1", System: "X1-BB", Kind: SlotKindMarket, State: SlotStateWanted},
	}
	systems := []ScreenedSystem{{System: "X1-AA", DepthCredits: 100}, {System: "X1-BB", DepthCredits: 9_000}}

	ports, _ := yardQueuePorts(slots, systems, nil)
	got, yards, err := drainCandidates(context.Background(), ports, testPlayerID)
	if err != nil {
		t.Fatalf("drainCandidates returned error: %v", err)
	}
	// Coverage 0: X1-BB-M1 (deeper) then X1-AA-M1. Coverage 1: X1-AA-Y1.
	want := []string{"X1-BB-M1", "X1-AA-M1", "X1-AA-Y1"}
	for i, w := range want {
		if got[i].Waypoint != w {
			t.Fatalf("an unwired yard demand changed the ordering: got %v, want %v", head(got), want)
		}
	}
	if yards.queued != 0 || yards.atHead != 0 {
		t.Fatalf("an unwired yard demand reported yard activity: queued=%d atHead=%d", yards.queued, yards.atHead)
	}
}

// TestDrainCandidates_AnUnreachableDarkYardIsNotPromoted keeps FEASIBILITY ahead of
// PRIORITY, and absolute precedence raises the stakes on it rather than lowering
// them.
//
// An ordering fix that does not first partition by feasibility PROMOTES THE
// IMPOSSIBLE — sp-1r08q's own finding, and the reason reachableFills runs before
// the ranking rather than after it. An unreachable system's coverage is exactly
// zero because no hull has ever arrived, so it was already being promoted for the
// wrong reason. Under sp-7qhum a dark yard there merely won a tiebreak; under
// sp-0j5hi it would take POSITION ZERO of every tick for as long as the system
// stayed unreachable, spending ~59,584 credits on a hull that sits in BOUGHT
// forever. Moving the partition ahead of reachableFills is the mutation this test
// exists to kill.
func TestDrainCandidates_AnUnreachableDarkYardIsNotPromoted(t *testing.T) {
	ports, led, _ := reachPorts()
	led.slots = append(led.slots, QueuedSlot{
		Waypoint: "X1-FAR-Y1", System: "X1-FAR", Kind: SlotKindMarket, State: SlotStateWanted,
	})
	ports.YardDemand = &fakeYardDemand{yards: []fakeYard{darkYard("X1-FAR-Y1", "X1-FAR", true)}}

	got, yards, err := drainCandidates(context.Background(), ports, testPlayerID)
	if err != nil {
		t.Fatalf("drainCandidates returned error: %v", err)
	}
	if positionOf(got, "X1-FAR-Y1") >= 0 {
		t.Fatalf("funded a heavy counter in a system no hull can walk to, at position %d. A dark yard "+
			"that cannot be reached is still unreachable, and absolute precedence would park it at the "+
			"head of every tick. got=%v", positionOf(got, "X1-FAR-Y1"), systemsOf(got))
	}
	// The REACHABLE ordinary market is what the tick works instead. Without this the
	// test would also pass if the fixture had produced nothing at all.
	if len(got) == 0 || got[0].Waypoint != "X1-NEXT-M1" {
		t.Fatalf("the reachable placement did not take the head after the unreachable yard was dropped: "+
			"got=%v", systemsOf(got))
	}
	if yards.queued != 0 {
		t.Fatalf("counted an unreachable yard as queued (%d) — the report would show the ordering "+
			"working on placements it had already dropped", yards.queued)
	}
}

// TestDrainCandidates_ReportsWhatTheOrderingDid is the observability half.
//
// A coordinator losing every one of these decisions must not look identical to one
// with nothing to decide. queued alone cannot tell them apart: forty dark yards in
// the queue and none of them reachable this tick reads exactly like none at all,
// which is the state the fleet was measured in.
func TestDrainCandidates_ReportsWhatTheOrderingDid(t *testing.T) {
	// Ten systems, each holding one ordinary market and one dark yard behind it.
	// Ten yards want the head; six places exist.
	var slots []QueuedSlot
	var systems []ScreenedSystem
	var yardFacts []fakeYard
	for i := 0; i < 10; i++ {
		system := fmt.Sprintf("X1-Y%02d", i)
		slots = append(slots,
			QueuedSlot{Waypoint: system + "-M1", System: system, Kind: SlotKindMarket, State: SlotStateWanted},
			QueuedSlot{Waypoint: system + "-Y1", System: system, Kind: SlotKindMarket, State: SlotStateWanted},
		)
		systems = append(systems, ScreenedSystem{System: system, DepthCredits: int64(1000 - i)})
		yardFacts = append(yardFacts, darkYard(system+"-Y1", system, true))
	}

	ports, _ := yardQueuePorts(slots, systems, &fakeYardDemand{yards: yardFacts})
	got, yards, err := drainCandidates(context.Background(), ports, testPlayerID)
	if err != nil {
		t.Fatalf("drainCandidates returned error: %v", err)
	}
	if yards.queued != 10 {
		t.Fatalf("queued=%d, want 10 — every dark yard with an outstanding placement is a row the "+
			"ordering was consulted on", yards.queued)
	}
	if yards.atHead != maxDrainAttempts {
		t.Fatalf("atHead=%d, want %d: ten dark yards compete for six places and all six should be theirs. head=%v",
			yards.atHead, maxDrainAttempts, head(got))
	}
}

// TestDrain_ReportsYardsFilledWhenADarkYardIsFunded is the outcome counter.
//
// The three ordering numbers say the queue was ordered; this one says a placement
// on a dark counter actually got a hull. It is filled here through the REUSE path,
// which spends nothing — the same path that fires while the expansion switch is
// off — so the assertion does not depend on a purchase being permitted.
func TestDrain_ReportsYardsFilledWhenADarkYardIsFunded(t *testing.T) {
	led := &fakeBuyLedger{
		slots: []QueuedSlot{
			{Waypoint: "X1-AA-Y1", System: "X1-AA", Kind: SlotKindMarket, State: SlotStateWanted},
			{Waypoint: "X1-AA-S1", System: "X1-AA", Kind: SlotKindSpare, State: SlotStateParked, AssignedShip: "PROBE-SPARE"},
		},
		systems: []ScreenedSystem{{System: "X1-AA", DepthCredits: 900}},
	}
	ports := BuyPorts{
		Treasury:   &fakeTreasury{credits: 10_000_000},
		CargoSpend: &fakeCargoSpend{},
		Purchaser:  &fakePurchaser{price: 1_000},
		Ledger:     led,
		Yards:      &fakeYards{yards: map[string][]string{"X1-AA": {"X1-AA-Y1"}}},
		Ships:      &fakeShipReader{docked: map[string]string{"X1-AA-Y1": "BUYER"}},
		Fleet:      &fakeFleet{},
		YardDemand: &fakeYardDemand{yards: []fakeYard{darkYard("X1-AA-Y1", "X1-AA", true)}},
	}

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID,
		BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if rep.Reused != 1 {
		t.Fatalf("the fixture did not fill the placement at all (Reused=%d), so YardsFilled proves nothing: %+v", rep.Reused, rep)
	}
	if rep.YardsFilled != 1 {
		t.Fatalf("YardsFilled=%d, want 1: a placement standing on a heavy counter we cannot price was "+
			"funded this tick and the report does not say so: %+v", rep.YardsFilled, rep)
	}
	if rep.YardsQueued != 1 || rep.YardsAtHead != 1 {
		t.Fatalf("YardsQueued=%d YardsAtHead=%d, want 1 and 1: %+v", rep.YardsQueued, rep.YardsAtHead, rep)
	}
}

// TestDrain_ReportsTheOrderingWhileSpendingIsPaused pins the state the fleet is
// ACTUALLY in: expansion_enabled is off, so no probe is bought, and the ordering
// numbers must still be published or an operator cannot tell a queue that is
// ordered-and-waiting from one that never saw a shipyard.
func TestDrain_ReportsTheOrderingWhileSpendingIsPaused(t *testing.T) {
	slots, systems, demand := saturatedQueue()
	ports, _ := yardQueuePorts(slots, systems, demand)
	ports.Treasury = &fakeTreasury{credits: 10_000_000}
	ports.CargoSpend = &fakeCargoSpend{}
	ports.Yards = &fakeYards{}
	ports.Ships = &fakeShipReader{}
	ports.Fleet = &fakeFleet{}

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID,
		BuyKnobs{SpendEnabled: false, ProbeCap: 100}, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if !rep.SpendingPaused {
		t.Fatalf("fixture did not exercise the paused path: %+v", rep)
	}
	if rep.YardsQueued != 1 || rep.YardsAtHead != 1 {
		t.Fatalf("a paused tick reported YardsQueued=%d YardsAtHead=%d, want 1 and 1. The queue is still "+
			"ordered while the switch is off, and that is the number that says so: %+v",
			rep.YardsQueued, rep.YardsAtHead, rep)
	}
	if rep.YardsFilled != 0 {
		t.Fatalf("YardsFilled=%d with spending paused and no spare to reuse: %+v", rep.YardsFilled, rep)
	}
}

// ---------------------------------------------------------------------------
// A YARD NEVER TAKES A MANNED MARKET'S HULL (Admiral constraint).
//
// The question that produced these two tests: "will they be retasked? so we
// will unman some already manned markets from days ago?" The answer is no, and
// these are what keep it no.
//
// "Absolute precedence" decides WHICH UNFILLED PLACEMENT GETS THE NEXT HULL. It
// is not authority to strip a market that already has one. Three independent
// structural facts hold that line, and each has an assertion below:
//
//  1. The drain's candidate read is SlotsByState(WANTED, QUEUED) — a PARKED row
//     is never a candidate at all.
//  2. The only transition in the whole drain that LEAVES PARKED is the spare
//     release in reuseSpareHull, and its loop guard requires Kind == SPARE. A
//     PARKED MARKET row cannot be transitioned by this queue by any path.
//  3. The foothold path DOES take a hull off a working market, but it fires
//     only for a target of Kind == SPARE (foothold.go), and SPARE placements are
//     routed to `seeds` before the sort ever runs. The ordering this bead
//     changes cannot reach it.
// ---------------------------------------------------------------------------

// TestDrainCandidates_AParkedMarketIsNeverACandidate is fact (1), and it is the
// test that fails if someone widens the drain's state list.
//
// The fixture is deliberately hostile: every PARKED market sits in the SAME
// system as the dark yard that wants a hull, so a state list widened to "see"
// them would put them directly in the yard's path.
func TestDrainCandidates_AParkedMarketIsNeverACandidate(t *testing.T) {
	slots := []QueuedSlot{
		{Waypoint: "X1-OLD-Y1", System: "X1-OLD", Kind: SlotKindMarket, State: SlotStateWanted},
	}
	// Three markets manned days ago, in the yard's own system.
	for i := 0; i < 3; i++ {
		slots = append(slots, QueuedSlot{
			Waypoint: fmt.Sprintf("X1-OLD-M%d", i), System: "X1-OLD", Kind: SlotKindMarket,
			State: SlotStateParked, AssignedShip: fmt.Sprintf("PROBE-OLD-%d", i),
		})
	}
	systems := []ScreenedSystem{{System: "X1-OLD", DepthCredits: 900}}

	ports, _ := yardQueuePorts(slots, systems, &fakeYardDemand{
		yards: []fakeYard{darkYard("X1-OLD-Y1", "X1-OLD", true)},
	})
	got, _, err := drainCandidates(context.Background(), ports, testPlayerID)
	if err != nil {
		t.Fatalf("drainCandidates returned error: %v", err)
	}

	// The yard IS worked — without this the assertion below would also pass on an
	// empty candidate list.
	if len(got) != 1 || got[0].Waypoint != "X1-OLD-Y1" {
		t.Fatalf("expected exactly the one unfilled yard as a candidate, got %v", head(got))
	}
	for _, c := range got {
		if c.State == SlotStateParked {
			t.Fatalf("a PARKED placement reached the candidate list: %s (%s). The drain reads WANTED and "+
				"QUEUED only, and promoting yards must never widen that — a manned market is not a "+
				"placement to be re-decided", c.Waypoint, c.State)
		}
	}
}

// TestDrain_APromotedYardDoesNotUnmanAnAlreadyMannedMarket is facts (2) and (3),
// end to end through the real DrainBuyQueue.
//
// THE FOOTHOLD PATH IS WIRED HERE, NOT NIL, and that is the whole point of the
// fixture. footholdBroker.fill returns false immediately when Gates or
// MannedHulls is nil, so a fixture that left them out would assert that a
// disabled path took no hull — which is true of any code and proves nothing. Both
// ports are supplied, the surplus pool is loaded with the parked markets, and the
// path still must not touch them, because the yard placement is Kind MARKET and
// the broker only serves Kind SPARE.
//
// A spare IS parked in the system, so the drain has a legitimate hull to take.
// That makes the test say something sharper than "nothing happened": the yard is
// filled, and the hull it is filled with is the SPARE's, not any market's.
func TestDrain_APromotedYardDoesNotUnmanAnAlreadyMannedMarket(t *testing.T) {
	slots := []QueuedSlot{
		{Waypoint: "X1-OLD-Y1", System: "X1-OLD", Kind: SlotKindMarket, State: SlotStateWanted},
	}
	// THE MANNED MARKETS COME BEFORE THE SPARE IN LEDGER ORDER, and that is the
	// whole reason this fixture bites. reuseSpareHull walks the system's rows in
	// ledger order and returns on the FIRST match, so with the spare written first
	// a relaxed Kind guard would still find the spare and the markets would survive
	// by luck. Written this way, a guard that stops distinguishing SPARE from
	// MARKET takes X1-OLD-M0 immediately.
	manned := map[string]bool{}
	for i := 0; i < 3; i++ {
		hull := fmt.Sprintf("PROBE-OLD-%d", i)
		slots = append(slots, QueuedSlot{
			Waypoint: fmt.Sprintf("X1-OLD-M%d", i), System: "X1-OLD", Kind: SlotKindMarket,
			State: SlotStateParked, AssignedShip: hull,
		})
		manned[hull] = true
	}
	slots = append(slots, QueuedSlot{
		Waypoint: "X1-OLD-S1", System: "X1-OLD", Kind: SlotKindSpare, State: SlotStateParked, AssignedShip: "PROBE-SPARE",
	})

	led := &fakeBuyLedger{slots: slots, systems: []ScreenedSystem{{System: "X1-OLD", DepthCredits: 900}}}
	ports := BuyPorts{
		Treasury:   &fakeTreasury{credits: 10_000_000},
		CargoSpend: &fakeCargoSpend{},
		Purchaser:  &fakePurchaser{price: 1_000},
		Ledger:     led,
		Yards:      &fakeYards{yards: map[string][]string{"X1-OLD": {"X1-OLD-Y1"}}},
		Ships:      &fakeShipReader{docked: map[string]string{"X1-OLD-Y1": "BUYER"}},
		Fleet:      &fakeFleet{},
		YardDemand: &fakeYardDemand{yards: []fakeYard{darkYard("X1-OLD-Y1", "X1-OLD", true)}},
		// The hull-releasing path, LIVE.
		Gates:       &fakeGates{adjacency: map[string][]string{"X1-OLD": {}}},
		MannedHulls: &fakeManned{hulls: manned},
	}

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID,
		BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}

	// The tick DID work — otherwise every assertion below is vacuous.
	if rep.Reused != 1 || rep.YardsFilled != 1 {
		t.Fatalf("the fixture did not fill the yard (Reused=%d YardsFilled=%d), so the untouched-markets "+
			"claim proves nothing: %+v", rep.Reused, rep.YardsFilled, rep)
	}
	// An end-to-end sanity check, NOT the coverage for the foothold guard. It is
	// weak here by construction: the yard is filled by the reuse path and
	// `continue`s before footholds.fill is ever called, so this stays green even if
	// the broker's Kind guard is deleted. The guard's real killing test is
	// TestFoothold_FillsOnlySparePlacementsNotMarketWants in foothold_test.go —
	// verified by mutating the guard, which fails that test and not this one. Do
	// not read this line as protection.
	if rep.Footholds != 0 {
		t.Fatalf("Footholds=%d: this drain took a hull off a working market to fill a yard: %+v",
			rep.Footholds, rep)
	}

	// Every manned market is still manned, by the same hull.
	for i := 0; i < 3; i++ {
		waypoint := fmt.Sprintf("X1-OLD-M%d", i)
		var found *QueuedSlot
		for j := range led.slots {
			if led.slots[j].Waypoint == waypoint && led.slots[j].Kind == SlotKindMarket {
				found = &led.slots[j]
			}
		}
		if found == nil {
			t.Fatalf("%s vanished from the ledger entirely", waypoint)
		}
		if found.State != SlotStateParked || found.AssignedShip != fmt.Sprintf("PROBE-OLD-%d", i) {
			t.Fatalf("%s was unmanned to feed a yard: state=%q hull=%q, want PARKED with PROBE-OLD-%d. "+
				"Yards get every NEW hull first; they do not get other placements' hulls taken from them",
				waypoint, found.State, found.AssignedShip, i)
		}
	}
	// And no transition was even ATTEMPTED against one of them. The state check
	// above could be satisfied by a release that was later undone; this cannot.
	for _, tr := range led.transitions {
		for i := 0; i < 3; i++ {
			if tr.waypoint == fmt.Sprintf("X1-OLD-M%d", i) {
				t.Fatalf("the drain attempted %s: %s -> %s on a manned market. No path in this queue may "+
					"transition a PARKED MARKET row", tr.waypoint, tr.from, tr.to)
			}
		}
	}
	// The SPARE is what was spent, and it went back to WANTED with no hull — the
	// one PARKED -> WANTED transition the drain owns, and it is Kind SPARE only.
	var spare *QueuedSlot
	for j := range led.slots {
		if led.slots[j].Kind == SlotKindSpare {
			spare = &led.slots[j]
		}
	}
	if spare == nil || spare.State != SlotStateWanted || spare.AssignedShip != "" {
		t.Fatalf("the spare was not the hull that fed the yard: %+v", spare)
	}
}

// TestReadYardDemand_AsksForEveryShipyardOnTheMap pins the limit against the thing
// it is sized for. A limit below the charted shipyard count would silently leave
// yards out of the lookup, and a yard missing from it is a yard the queue stays
// blind to — the defect itself, reintroduced quietly.
func TestReadYardDemand_AsksForEveryShipyardOnTheMap(t *testing.T) {
	const chartedShipyards = 2257 // measured live, player 5
	if yardDemandLimit <= chartedShipyards {
		t.Fatalf("yardDemandLimit is %d against %d charted shipyards: the lookup would drop yards it is "+
			"meant to rank", yardDemandLimit, chartedShipyards)
	}

	demand := &fakeYardDemand{yards: []fakeYard{darkYard("X1-AA-Y1", "X1-AA", true)}}
	readYardDemand(context.Background(), BuyPorts{YardDemand: demand}, testPlayerID)
	if len(demand.limits) != 1 || demand.limits[0] != yardDemandLimit {
		t.Fatalf("asked for limits %v, want exactly [%d]", demand.limits, yardDemandLimit)
	}
}

// TestYardFirstOffsets_LeavesFIFOAloneWhenNothingIsAYard pins the degenerate path
// against the ordering it replaced. The offsets it produces with an empty demand
// must be the ledger-FIFO index this queue has always used, per system.
func TestYardFirstOffsets_LeavesFIFOAloneWhenNothingIsAYard(t *testing.T) {
	fills := []QueuedSlot{
		{Waypoint: "X1-AA-M1", System: "X1-AA"},
		{Waypoint: "X1-BB-M1", System: "X1-BB"},
		{Waypoint: "X1-AA-M2", System: "X1-AA"},
		{Waypoint: "X1-AA-M3", System: "X1-AA"},
		{Waypoint: "X1-BB-M2", System: "X1-BB"},
	}
	want := []int{0, 0, 1, 2, 1}

	for _, tc := range []struct {
		name  string
		yards yardOrder
	}{
		{"nil map", yardOrder{}},
		{"empty map", yardOrder{rank: map[string]int{}}},
		{"no fill is a yard", yardOrder{rank: map[string]int{"X1-ZZ-Y1": 0}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := yardFirstOffsets(fills, tc.yards)
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("offsets %v, want %v — the yard-blind path must reproduce ledger FIFO exactly", got, want)
				}
			}
		})
	}
}
