package parkedsensing

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/yardscan"
)

// yardqueue_test.go pins the yard-aware ordering (sp-7qhum).
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

// TestDrainCandidates_AHeavyYardOutranksAProbeOnlyYardInItsOwnSystem pins the
// demand weighting.
//
// One unpriced heavy counter is worth more than every probe yard on the map put
// together — the incident behind sp-mb0er had the fleet buying heavies at up to
// 2,288,156 against a visible cheapest of 1,918,293, chosen from four prices out of
// eighty-five. So a yard selling only probes and shuttles must not take the one
// promotion its system gets.
//
// THE FIXTURE IS BUILT TO DEFEAT THE TWO CHEAP WAYS OF PASSING IT. The probe yard
// is named X1-TWO-A1 and the heavy X1-TWO-Z9, so RankPresence's SYMBOL tiebreak
// favours the probe yard — dropping the Heavy clause therefore flips this test
// rather than leaving it green on alphabetical luck. And the probe yard is written
// FIRST in the ledger, so a keying that made all yards tie would hand it the
// system's offset 0 on stable order alone.
func TestDrainCandidates_AHeavyYardOutranksAProbeOnlyYardInItsOwnSystem(t *testing.T) {
	rivals, rivalSys := rivalSystems(8, 1, 9_000)
	slots := append([]QueuedSlot{
		{Waypoint: "X1-TWO-A1", System: "X1-TWO", Kind: SlotKindMarket, State: SlotStateWanted},
		{Waypoint: "X1-TWO-Z9", System: "X1-TWO", Kind: SlotKindMarket, State: SlotStateWanted},
		{Waypoint: "X1-TWO-M1", System: "X1-TWO", Kind: SlotKindMarket, State: SlotStateWanted},
	}, rivals...)
	systems := append([]ScreenedSystem{{System: "X1-TWO", DepthCredits: 100}}, rivalSys...)

	ports, _ := yardQueuePorts(slots, systems, &fakeYardDemand{yards: []fakeYard{
		darkYard("X1-TWO-A1", "X1-TWO", false), // probes only
		darkYard("X1-TWO-Z9", "X1-TWO", true),  // heavy freighters
	}})
	got, _, err := drainCandidates(context.Background(), ports, testPlayerID)
	if err != nil {
		t.Fatalf("drainCandidates returned error: %v", err)
	}

	// Nine systems rank a placement at coverage 0 and the head is six, so the
	// coverage-1 placement X1-TWO gets is out of reach. The system's ONE promotion
	// has to go to the heavy counter.
	if !inHead(got, "X1-TWO-Z9") {
		t.Fatalf("the heavy counter did not take its system's promotion: Z9 at %d, head=%v",
			positionOf(got, "X1-TWO-Z9"), head(got))
	}
	if inHead(got, "X1-TWO-A1") {
		t.Fatalf("a probe-only yard took the promotion a heavy counter needed. A1 at %d, Z9 at %d, head=%v",
			positionOf(got, "X1-TWO-A1"), positionOf(got, "X1-TWO-Z9"), head(got))
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

// TestDrainCandidates_CoverageStillOutranksTheYardTerm is the coverage-first
// guarantee, and it is the test that fails if this is ever "improved" by ranking
// yards ABOVE coverage.
//
// Coverage-first exists because depth alone put 67% of parked probes in three
// systems, and a shipyard is not worth re-concentrating the fleet for. It is also
// the correct division of labour with sp-fox5u: a system that already holds a hull
// can have its yard priced by relocating that hull in-system, which buys nothing,
// so the queue that SPENDS should be aimed at the systems that hold none. Measured
// live, that is where the problem is anyway — 56 of the 78 unfilled heavy yards
// sit in systems with no probe at all.
//
// Here the dark heavy counter sits in a system that already has a probe parked in
// it. It must therefore wait behind eight uncovered systems, promoted within its
// own system and nowhere else.
func TestDrainCandidates_CoverageStillOutranksTheYardTerm(t *testing.T) {
	rivals, rivalSys := rivalSystems(8, 1, 100)
	slots := append([]QueuedSlot{
		// The hull that makes X1-COVERED covered.
		{Waypoint: "X1-COVERED-P1", System: "X1-COVERED", Kind: SlotKindMarket, State: SlotStateParked, AssignedShip: "PROBE-1"},
		{Waypoint: "X1-COVERED-M1", System: "X1-COVERED", Kind: SlotKindMarket, State: SlotStateWanted},
		{Waypoint: "X1-COVERED-Y1", System: "X1-COVERED", Kind: SlotKindMarket, State: SlotStateWanted},
	}, rivals...)
	systems := append([]ScreenedSystem{{System: "X1-COVERED", DepthCredits: 90_000}}, rivalSys...)

	ports, _ := yardQueuePorts(slots, systems, &fakeYardDemand{
		yards: []fakeYard{darkYard("X1-COVERED-Y1", "X1-COVERED", true)},
	})
	got, _, err := drainCandidates(context.Background(), ports, testPlayerID)
	if err != nil {
		t.Fatalf("drainCandidates returned error: %v", err)
	}

	if inHead(got, "X1-COVERED-Y1") {
		t.Fatalf("a dark yard in an ALREADY-COVERED system jumped ahead of eight systems holding no hull "+
			"at all. The yard term ranks below coverage: it reorders within a system, it does not promote "+
			"one past another. head=%v", head(got))
	}
	// It is still promoted WITHIN its own system — the term fired, it just did not
	// outrank coverage. Without this the test would also pass with the feature
	// deleted.
	if positionOf(got, "X1-COVERED-Y1") > positionOf(got, "X1-COVERED-M1") {
		t.Fatalf("the yard was not promoted inside its own system: Y1 at %d, M1 at %d",
			positionOf(got, "X1-COVERED-Y1"), positionOf(got, "X1-COVERED-M1"))
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

// TestDrainCandidates_AnUnreachableDarkYardIsNotPromoted keeps feasibility ahead of
// priority.
//
// An ordering fix that does not first partition by feasibility PROMOTES THE
// IMPOSSIBLE — sp-1r08q's own finding, and the reason reachableFills runs before
// the ranking rather than after it. An unreachable system's coverage is exactly
// zero because no hull has ever arrived, so it was already being promoted for the
// wrong reason; making it a dark yard as well must not promote it twice.
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
		t.Fatalf("funded a heavy counter in a system no hull can walk to. A dark yard that cannot be "+
			"reached is still unreachable. got=%v", systemsOf(got))
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
