package parkedsensing

import (
	"fmt"
	"math"
	"sort"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// chartcrew_test.go pins how many hulls a dark system earns, the roster the
// partition is solved over, and the angular fallback that answers when the fleet
// partitioner cannot. The solved partition itself is chartshare_test.go.

// ringStops lays n uncharted waypoints evenly around the system centre, which is
// the only shape that lets a sector partition be checked for BALANCE as well as
// for disjointness.
func ringStops(system string, n int) []ChartStop {
	stops := make([]ChartStop, 0, n)
	for i := 0; i < n; i++ {
		angle := 2 * math.Pi * float64(i) / float64(n)
		stops = append(stops, ChartStop{
			Waypoint: fmt.Sprintf("%s-W%02d", system, i),
			X:        100 * math.Cos(angle),
			Y:        100 * math.Sin(angle),
		})
	}
	return stops
}

// --- the budget --------------------------------------------------------------

// THE BUDGET IS PROPORTIONAL TO THE WORK, not a pair of bumps that flattens: a
// system twice as dark draws a crew that finishes it in the same time.
func TestChartHulls_ScalesTheBudgetWithTheOutstandingWork(t *testing.T) {
	hulls := resolveChartHulls(ExpandKnobs{})
	for _, tc := range []struct {
		uncharted, want int
	}{
		{0, 1}, {1, 1}, {defaultSecondChartHullAt - 1, 1},
		{defaultSecondChartHullAt, 2}, {defaultThirdChartHullAt - 1, 2},
		{defaultThirdChartHullAt, 3},
		{defaultThirdChartHullAt + defaultChartHullTier - 1, 3},
		{defaultThirdChartHullAt + defaultChartHullTier, 4},
		{defaultThirdChartHullAt + 2*defaultChartHullTier, 5},
	} {
		if got := hulls.budgetFor(tc.uncharted); got != tc.want {
			t.Fatalf("budgetFor(%d) = %d, want %d", tc.uncharted, got, tc.want)
		}
	}
}

// THE DERIVED CAP IS THE ONLY THING BOUNDING THE GROWTH, the old hard-coded three
// being gone. That is what makes the knob's advertised maximum honest.
func TestChartHulls_TheDerivedCapBoundsTheProportionalGrowth(t *testing.T) {
	hulls := resolveChartHulls(ExpandKnobs{})
	if got := hulls.budgetFor(defaultThirdChartHullAt + 4*defaultChartHullTier); got != defaultChartHullCap {
		t.Fatalf("budgetFor(a system four tiers past the third hull) = %d, want the cap %d", got, defaultChartHullCap)
	}
	for _, uncharted := range []int{100, 1_000, 100_000} {
		if got := hulls.budgetFor(uncharted); got != defaultChartHullCap {
			t.Fatalf("budgetFor(%d) = %d, want the cap %d — nothing may exceed it", uncharted, got, defaultChartHullCap)
		}
	}
	// A tuned cap binds the same way, below the derived one.
	tuned := resolveChartHulls(ExpandKnobs{ChartHullCap: 2})
	if got := tuned.budgetFor(1_000); got != 2 {
		t.Fatalf("budgetFor(1000) = %d under a cap of 2, want 2", got)
	}

	// A cap ABOVE the derived one clamps to it: the tune bound cannot reach a value
	// already stored, so a stale one would outlive the derivation that replaced it.
	for _, stale := range []int{defaultChartHullCap + 1, 10, 1_000} {
		if got := resolveChartHulls(ExpandKnobs{ChartHullCap: stale}).cap; got != defaultChartHullCap {
			t.Fatalf("a stored cap of %d resolved to %d, want the derived cap %d", stale, got, defaultChartHullCap)
		}
	}
}

// A NON-POSITIVE KNOB IS THE REVERT VERB, not a disarm — and that includes the
// derived tier, which at zero would divide by it and negative would run backwards.
func TestChartHulls_NonPositiveKnobsRevertToTheDocumentedDefaults(t *testing.T) {
	for _, k := range []ExpandKnobs{
		{},
		{ChartHullCap: 0, SecondChartHullAt: 0, ThirdChartHullAt: 0},
		{ChartHullCap: -1, SecondChartHullAt: -5, ThirdChartHullAt: -99},
	} {
		hulls := resolveChartHulls(k)
		want := chartHulls{
			cap:    defaultChartHullCap,
			second: defaultSecondChartHullAt,
			third:  defaultThirdChartHullAt,
			tier:   defaultThirdChartHullAt - defaultSecondChartHullAt,
		}
		if hulls != want {
			t.Fatalf("resolveChartHulls(%+v) = %+v, want the documented defaults %+v", k, hulls, want)
		}
	}
}

// THE BUDGET IS MONOTONIC IN THE OUTSTANDING COUNT however the knobs are ordered:
// a budget that dipped would stand a hull down over a system that had just got darker.
func TestChartHulls_TheBudgetIsMonotonicUnderOutOfOrderThresholds(t *testing.T) {
	for _, k := range []ExpandKnobs{
		{}, // documented order
		{SecondChartHullAt: 40, ThirdChartHullAt: 20},                   // inverted
		{SecondChartHullAt: 20, ThirdChartHullAt: 20},                   // equal, no step to read
		{ChartHullCap: 5, SecondChartHullAt: 500, ThirdChartHullAt: 1},  // third first, second unreachable
		{ChartHullCap: 4, SecondChartHullAt: 2, ThirdChartHullAt: 3},    // tighter than any real system
		{ChartHullCap: 1, SecondChartHullAt: 100, ThirdChartHullAt: 50}, // inverted under the kill switch
	} {
		hulls := resolveChartHulls(k)
		if hulls.tier <= 0 {
			t.Fatalf("resolveChartHulls(%+v) left tier %d — a non-positive tier divides by zero or counts backwards", k, hulls.tier)
		}
		previous := 0
		for uncharted := 0; uncharted <= 600; uncharted++ {
			got := hulls.budgetFor(uncharted)
			if got < 1 {
				t.Fatalf("budgetFor(%d) = %d under %+v, want at least one hull", uncharted, got, k)
			}
			if got < previous {
				t.Fatalf("budgetFor(%d) = %d after %d under %+v — a darker system earned fewer hulls", uncharted, got, previous, k)
			}
			if got > hulls.cap {
				t.Fatalf("budgetFor(%d) = %d under %+v, above its own cap %d", uncharted, got, k, hulls.cap)
			}
			previous = got
		}
	}
}

// AN UNSWEPT SYSTEM REPORTS ZERO AND STILL EARNS EXACTLY ONE HULL under every
// configuration: the first hull's catalog sweep is what produces a count to scale on.
func TestChartHulls_AnUnsweptSystemEarnsExactlyOneHull(t *testing.T) {
	for _, k := range []ExpandKnobs{
		{},
		{SecondChartHullAt: 1, ThirdChartHullAt: 1},
		{ChartHullCap: 5, SecondChartHullAt: 1, ThirdChartHullAt: 2},
		{SecondChartHullAt: 40, ThirdChartHullAt: 20},
	} {
		if got := resolveChartHulls(k).budgetFor(0); got != 1 {
			t.Fatalf("budgetFor(0) = %d under %+v, want exactly 1", got, k)
		}
	}
}

// THE KILL SWITCH. A cap of one is the whole feature turned off: every system,
// however dark, is worked by a single hull over the whole uncharted list.
func TestChartHulls_ACapOfOneIsTheSingleHullTourAgain(t *testing.T) {
	hulls := resolveChartHulls(ExpandKnobs{ChartHullCap: 1})
	for _, uncharted := range []int{0, 1, 12, 24, 500} {
		if got := hulls.budgetFor(uncharted); got != 1 {
			t.Fatalf("budgetFor(%d) = %d under cap 1, want 1", uncharted, got)
		}
	}

	stops := ringStops("X1-DARK", 30)
	crew := []string{"PROBE-A", "PROBE-B", "PROBE-C"}
	// Even with a crew already standing on the system, a capped budget leaves the
	// partition undivided: the cap bounds what the seeder RAISES, and the tour a
	// lone hull walks is the full list.
	own := partitionOf(stops, crew[:1], "PROBE-A")
	if len(own) != len(stops) {
		t.Fatalf("a single-hull crew owns %d of %d stops, want all of them", len(own), len(stops))
	}
}

// The thresholds set the first two bumps AND, in their spacing, the step every hull
// past the third is earned at — so retuning the pair moves the whole ladder.
func TestChartHulls_KnobsOverrideTheThresholds(t *testing.T) {
	hulls := resolveChartHulls(ExpandKnobs{ChartHullCap: 5, SecondChartHullAt: 4, ThirdChartHullAt: 6})
	if hulls.tier != 2 {
		t.Fatalf("tier = %d, want 2 — the step is the operator's own spacing", hulls.tier)
	}
	for _, tc := range []struct{ uncharted, want int }{
		{3, 1}, {4, 2}, {5, 2}, {6, 3}, {7, 3}, {8, 4}, {10, 5}, {60, 5},
	} {
		if got := hulls.budgetFor(tc.uncharted); got != tc.want {
			t.Fatalf("budgetFor(%d) = %d, want %d under tuned thresholds", tc.uncharted, got, tc.want)
		}
	}
}

// --- the angular fallback ----------------------------------------------------

// The fallback holds THE SAME PROPERTY the solved partition does: shares disjoint
// and together covering every outstanding stop. Two probes charting one waypoint
// is a hull-hour spent on nothing; a waypoint owned by nobody leaves the system's
// count stuck above zero and the system permanently re-seeded.
func TestSectorFallback_IsDisjointAndCoversEveryStop(t *testing.T) {
	stops := ringStops("X1-DARK", 31)
	crew := []string{"PROBE-A", "PROBE-B", "PROBE-C"}

	owner := map[string]string{}
	for _, ship := range crew {
		for _, waypoint := range partitionOf(stops, crew, ship) {
			if held, taken := owner[waypoint]; taken {
				t.Fatalf("%s is owned by both %s and %s — two hulls charting one waypoint", waypoint, held, ship)
			}
			owner[waypoint] = ship
		}
	}
	if len(owner) != len(stops) {
		t.Fatalf("the crew owns %d of %d stops, want every one — an unowned waypoint never gets charted", len(owner), len(stops))
	}
	for _, ship := range crew {
		if len(partitionOf(stops, crew, ship)) == 0 {
			t.Fatalf("%s owns nothing of an evenly spread system, so its hull charts nothing", ship)
		}
	}
}

// A partition keeps the catalog's own visiting ORDER, which is what puts a
// system's shipyard-bearing waypoints in front of its dead rock.
func TestSectorFallback_KeepsTheCatalogOrder(t *testing.T) {
	stops := ringStops("X1-DARK", 12)
	crew := []string{"PROBE-A", "PROBE-B"}

	for _, ship := range crew {
		own := partitionOf(stops, crew, ship)
		position := map[string]int{}
		for i, stop := range stops {
			position[stop.Waypoint] = i
		}
		for i := 1; i < len(own); i++ {
			if position[own[i-1]] > position[own[i]] {
				t.Fatalf("%s owns %v, which reorders the catalog — the tour charts its head, so the value tier must survive partitioning", ship, own)
			}
		}
	}
}

// A hull's share must not depend on WHICH waypoints its crewmates have already
// charted, or a partition boundary walks across the system every tick and two
// hulls chase the same waypoint in turn.
func TestSectorFallback_OwnershipSurvivesTheRestOfTheSetBeingCharted(t *testing.T) {
	stops := ringStops("X1-DARK", 24)
	crew := []string{"PROBE-A", "PROBE-B", "PROBE-C"}
	before := partitionOf(stops, crew, "PROBE-B")

	// Everything PROBE-A and PROBE-C own is charted away.
	mine := map[string]bool{}
	for _, waypoint := range before {
		mine[waypoint] = true
	}
	remaining := make([]ChartStop, 0, len(before))
	for _, stop := range stops {
		if mine[stop.Waypoint] {
			remaining = append(remaining, stop)
		}
	}

	after := partitionOf(remaining, crew, "PROBE-B")
	if len(after) != len(before) {
		t.Fatalf("PROBE-B owned %v and now owns %v — a partition that moves is a partition two hulls fight over", before, after)
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("PROBE-B owned %v and now owns %v", before, after)
		}
	}
}

// A hull that is not on the crew owns nothing: the roster is what grants a share,
// and a stale errand must never be handed the whole system by default.
func TestSectorFallback_AShipOffTheCrewOwnsNothing(t *testing.T) {
	stops := ringStops("X1-DARK", 9)
	if own := partitionOf(stops, []string{"PROBE-A", "PROBE-B"}, "PROBE-GHOST"); len(own) != 0 {
		t.Fatalf("an off-crew hull owns %v, want nothing", own)
	}
}

// --- the roster --------------------------------------------------------------

func TestSeedRoster_CountsThePrimaryAndTheExtrasTogether(t *testing.T) {
	roster := newSeedRoster([]ExpandSystem{{
		System: "X1-DARK", SeedShip: "PROBE-A", SeedState: SeedStateCharting,
		ExtraSeeds: []SeedErrand{
			{Ship: "PROBE-B", State: SeedStateDispatched},
			{Ship: "PROBE-DONE", State: SeedStateDone},
		},
	}})

	if got := roster.size("X1-DARK"); got != 2 {
		t.Fatalf("crew size = %d, want 2 — a DONE errand is over and its hull is a spare again", got)
	}
	want := []string{"PROBE-A", "PROBE-B"}
	got := roster.crew("X1-DARK")
	if len(got) != len(want) {
		t.Fatalf("crew = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("crew = %v, want %v — the partition rank is read off this order, so it must be total and stable", got, want)
		}
	}
	hulls := roster.hulls()
	if !hulls["PROBE-A"] || !hulls["PROBE-B"] {
		t.Fatalf("hulls on errand = %v, want both active seeds — a hull missing here is claimed twice", hulls)
	}
	if hulls["PROBE-DONE"] {
		t.Fatal("a DONE hull reads as on-errand, so the spare it parked as can never be re-tasked")
	}
}

// --- targeting ---------------------------------------------------------------

func TestSeedlessTargets_ADarkSystemStaysATargetUntilItsCrewIsFull(t *testing.T) {
	hulls := resolveChartHulls(ExpandKnobs{})
	rows := []ExpandSystem{{
		System: "X1-BIG", Verdict: VerdictPending, CatalogKnown: true,
		UnchartedCount: defaultThirdChartHullAt,
		SeedShip:       "PROBE-A", SeedState: SeedStateCharting,
		ExtraSeeds: []SeedErrand{{Ship: "PROBE-B", State: SeedStateCharting}},
	}}

	if targets := seedlessTargets(rows, hulls); len(targets) != 1 {
		t.Fatalf("targets = %v, want X1-BIG — two of its three hulls are aboard and the third is still owed", targets)
	}

	rows[0].ExtraSeeds = append(rows[0].ExtraSeeds, SeedErrand{Ship: "PROBE-C", State: SeedStateDispatched})
	if targets := seedlessTargets(rows, hulls); len(targets) != 0 {
		t.Fatalf("targets = %v, want none — a full crew must not draw a fourth hull", targets)
	}
}

func TestSeedlessTargets_ASmallSystemIsFullOnOneHull(t *testing.T) {
	hulls := resolveChartHulls(ExpandKnobs{})
	rows := []ExpandSystem{{
		System: "X1-SMALL", Verdict: VerdictPending, CatalogKnown: true,
		UnchartedCount: defaultSecondChartHullAt - 1,
		SeedShip:       "PROBE-A", SeedState: SeedStateCharting,
	}}
	if targets := seedlessTargets(rows, hulls); len(targets) != 0 {
		t.Fatalf("targets = %v, want none — a system under the second-hull threshold is served", targets)
	}
}

// UNDER THE CAP THE OLD RULE RETURNS EXACTLY: one hull per system, whatever the
// count says.
func TestSeedlessTargets_ACapOfOneRestoresOneHullPerSystem(t *testing.T) {
	hulls := resolveChartHulls(ExpandKnobs{ChartHullCap: 1})
	rows := []ExpandSystem{{
		System: "X1-BIG", Verdict: VerdictPending, CatalogKnown: true, UnchartedCount: 60,
		SeedShip: "PROBE-A", SeedState: SeedStateCharting,
	}}
	if targets := seedlessTargets(rows, hulls); len(targets) != 0 {
		t.Fatalf("targets = %v, want none under a cap of one", targets)
	}
}

// --- the tick ----------------------------------------------------------------

// darkSystemCrew stands a full crew on one dark system, every hull already in
// the system and charting, so one tick gives each of them a step.
func darkSystemCrew(h *expandHarness, stops []ChartStop) *fakeUncharted {
	h.ledger.systems = []ExpandSystem{{
		System: "X1-DARK", Verdict: VerdictPending, UnchartedCount: len(stops),
		SeedShip: "PROBE-A", SeedState: SeedStateCharting,
		ExtraSeeds: []SeedErrand{
			{Ship: "PROBE-B", State: SeedStateCharting},
			{Ship: "PROBE-C", State: SeedStateCharting},
		},
	}}
	for _, ship := range []string{"PROBE-A", "PROBE-B", "PROBE-C"} {
		h.ships.positions[ship] = ShipPos{
			// Parked on the system's gate rather than on any uncharted waypoint, so
			// every hull's step is a NAVIGATE naming the waypoint it has chosen.
			Waypoint: "X1-DARK-GATE", NavStatus: navigation.NavStatusInOrbit, Found: true,
		}
	}
	return &fakeUncharted{stops: map[string][]ChartStop{"X1-DARK": stops}}
}

// THE HEADLINE: three hulls on one dark system fly at three DIFFERENT waypoints.
func TestAdvanceExpansion_ACrewedSystemChartsOnDisjointTours(t *testing.T) {
	h := newExpandHarness()
	stops := ringStops("X1-DARK", 30)
	uncharted := darkSystemCrew(h, stops)

	rep, err := h.run(t, uncharted)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Navigated != 3 {
		t.Fatalf("Navigated = %d, want 3 — every hull of the crew gets its step", rep.Navigated)
	}

	targets := map[string]string{}
	for _, call := range h.seed.calls {
		if call.verb != "navigate" {
			continue
		}
		if owner, taken := targets[call.arg]; taken {
			t.Fatalf("%s and %s both flew at %s — the partitions overlap", owner, call.ship, call.arg)
		}
		targets[call.arg] = call.ship
	}
	if len(targets) != 3 {
		t.Fatalf("the crew flew at %d distinct waypoints, want 3", len(targets))
	}
}

// A hull whose own share is charted through stands down even while its crewmates
// still have work: the tour it owns is the tour it finishes.
func TestAdvanceExpansion_AHullStandsDownWhenItsOwnShareIsCharted(t *testing.T) {
	h := newExpandHarness()
	stops := ringStops("X1-DARK", 30)
	crew := []string{"PROBE-A", "PROBE-B", "PROBE-C"}
	shares := dealtShares("X1-DARK", crew, stops)
	mine := map[string]bool{}
	for _, share := range shares {
		if share.Ship == "PROBE-A" {
			for _, waypoint := range share.Waypoints {
				mine[waypoint] = true
			}
		}
	}
	// Everything PROBE-A owns is already charted; the rest of the system is not.
	remaining := make([]ChartStop, 0, len(stops))
	for _, stop := range stops {
		if !mine[stop.Waypoint] {
			remaining = append(remaining, stop)
		}
	}
	uncharted := darkSystemCrew(h, remaining)
	h.ledger.systems[0].UnchartedCount = len(remaining)
	h.ledger.chartShares = shares
	h.screen.verdicts["X1-DARK"] = VerdictNoWhitelist

	if _, err := h.run(t, uncharted); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var stoodDown bool
	for _, slot := range h.ledger.upsertedSlots {
		if slot.Kind == SlotKindSpare && slot.AssignedShip == "PROBE-A" {
			stoodDown = true
		}
	}
	if !stoodDown {
		t.Fatalf("PROBE-A did not park as a spare; slots written = %v", h.ledger.upsertedSlots)
	}
}

// PARTIAL COVERAGE: a system owed one more hull takes exactly one, and the new
// errand goes onto the roster rather than over the incumbent.
func TestAdvanceExpansion_AnUnderCrewedSystemTakesExactlyOneMoreHull(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-HOME", Verdict: VerdictInScope, CatalogKnown: true},
		{
			System: "X1-DARK", Verdict: VerdictPending, CatalogKnown: true,
			UnchartedCount: defaultSecondChartHullAt,
			SeedShip:       "PROBE-A", SeedState: SeedStateCharting,
		},
	}
	h.gates.adjacency = map[string][]string{"X1-HOME": {"X1-DARK"}, "X1-DARK": {"X1-HOME"}}
	h.ships.positions["PROBE-A"] = ShipPos{
		Waypoint: "X1-DARK-GATE", NavStatus: navigation.NavStatusInOrbit, Found: true,
	}
	// Two parked spares, so the tick could over-crew if it wanted to.
	for _, ship := range []string{"PROBE-S1", "PROBE-S2"} {
		h.ledger.slots = append(h.ledger.slots, QueuedSlot{
			Waypoint: "X1-HOME-" + ship, System: "X1-HOME", Kind: SlotKindSpare,
			State: SlotStateParked, AssignedShip: ship,
		})
	}
	uncharted := &fakeUncharted{stops: map[string][]ChartStop{"X1-DARK": ringStops("X1-DARK", defaultSecondChartHullAt)}}

	rep, err := h.run(t, uncharted)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsClaimed != 1 {
		t.Fatalf("SeedsClaimed = %d, want 1 — a system owed one hull takes one", rep.SeedsClaimed)
	}
	if len(h.ledger.extraSeeds) != 1 {
		t.Fatalf("extra errands stamped = %v, want exactly one", h.ledger.extraSeeds)
	}
	if h.ledger.extraSeeds[0].system != "X1-DARK" || h.ledger.extraSeeds[0].state != SeedStateDispatched {
		t.Fatalf("extra errand = %+v, want a DISPATCHED errand on X1-DARK", h.ledger.extraSeeds[0])
	}
	for _, call := range h.ledger.setSeeds {
		if call.system == "X1-DARK" {
			t.Fatalf("the incumbent errand was rewritten (%+v) — the system row still names PROBE-A", call)
		}
	}
}

// A CAPPED TICK NEVER CREWS. Same fixture, cap of one: the spares stay parked.
func TestAdvanceExpansion_ACapOfOneLeavesAnIncumbentSystemAlone(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-HOME", Verdict: VerdictInScope, CatalogKnown: true},
		{
			System: "X1-DARK", Verdict: VerdictPending, CatalogKnown: true,
			UnchartedCount: 60, SeedShip: "PROBE-A", SeedState: SeedStateCharting,
		},
	}
	h.gates.adjacency = map[string][]string{"X1-HOME": {"X1-DARK"}, "X1-DARK": {"X1-HOME"}}
	h.ships.positions["PROBE-A"] = ShipPos{
		Waypoint: "X1-DARK-GATE", NavStatus: navigation.NavStatusInOrbit, Found: true,
	}
	h.ledger.slots = []QueuedSlot{{
		Waypoint: "X1-HOME-S1", System: "X1-HOME", Kind: SlotKindSpare,
		State: SlotStateParked, AssignedShip: "PROBE-S1",
	}}
	uncharted := &fakeUncharted{stops: map[string][]ChartStop{"X1-DARK": ringStops("X1-DARK", 60)}}

	rep, err := h.runWithKnobs(t, uncharted, ExpandKnobs{
		SeedsEnabled: true, MinBudgetRate: 0.05, Whitelist: h.whitelist, ChartHullCap: 1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsClaimed != 0 || len(h.ledger.extraSeeds) != 0 {
		t.Fatalf("SeedsClaimed = %d and extras = %v under a cap of one, want none of either",
			rep.SeedsClaimed, h.ledger.extraSeeds)
	}
}

// A hull whose share is charted by somebody else mid-errand simply moves on to
// the next stop it owns: the already-charted case stays benign.
func TestAdvanceExpansion_AShareCharteredElsewhereLeavesTheTourWalking(t *testing.T) {
	h := newExpandHarness()
	stops := ringStops("X1-DARK", 30)
	uncharted := darkSystemCrew(h, stops)
	shares := dealtShares("X1-DARK", []string{"PROBE-A", "PROBE-B", "PROBE-C"}, stops)
	h.ledger.chartShares = shares
	var own []string
	for _, share := range shares {
		if share.Ship == "PROBE-A" {
			own = share.Waypoints
		}
	}
	if len(own) < 2 {
		t.Fatalf("fixture gives PROBE-A only %v", own)
	}

	// The head of PROBE-A's own share disappears from the catalog between ticks.
	kept := make([]ChartStop, 0, len(stops))
	for _, stop := range stops {
		if stop.Waypoint != own[0] {
			kept = append(kept, stop)
		}
	}
	uncharted.stops["X1-DARK"] = kept

	if _, err := h.run(t, uncharted); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, call := range h.seed.calls {
		if call.ship == "PROBE-A" && call.verb == "navigate" && call.arg == own[0] {
			t.Fatalf("PROBE-A flew at %s, which is no longer uncharted", own[0])
		}
	}
	if h.partitioner.calls() != 0 {
		t.Fatalf("a stop leaving a stored share re-solved the partition %d time(s)", h.partitioner.calls())
	}
}

// The crew order is read off symbols, so a roster is the same however the ledger
// hands its rows back.
func TestSeedRoster_CrewOrderIsIndependentOfTheRowOrder(t *testing.T) {
	forward := newSeedRoster([]ExpandSystem{{
		System: "X1-DARK", SeedShip: "PROBE-M", SeedState: SeedStateCharting,
		ExtraSeeds: []SeedErrand{
			{Ship: "PROBE-Z", State: SeedStateCharting},
			{Ship: "PROBE-A", State: SeedStateCharting},
		},
	}})
	backward := newSeedRoster([]ExpandSystem{{
		System: "X1-DARK", SeedShip: "PROBE-M", SeedState: SeedStateCharting,
		ExtraSeeds: []SeedErrand{
			{Ship: "PROBE-A", State: SeedStateCharting},
			{Ship: "PROBE-Z", State: SeedStateCharting},
		},
	}})

	left, right := forward.crew("X1-DARK"), backward.crew("X1-DARK")
	if !sort.StringsAreSorted(left) {
		t.Fatalf("crew = %v, want symbol order", left)
	}
	for i := range left {
		if left[i] != right[i] {
			t.Fatalf("crew = %v one way and %v the other — the partition rank would swap between ticks", left, right)
		}
	}
}
