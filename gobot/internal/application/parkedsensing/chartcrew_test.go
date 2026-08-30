package parkedsensing

import (
	"context"
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

// walkOf is a measured walk of this many hops.
func walkOf(hops int) chartWalk { return chartWalk{hops: hops, measured: true} }

// THE SAME SYSTEM EARNS A DIFFERENT CREW ON A DIFFERENT MAP, which is the whole
// point: the walk is what the marginal hull is priced against, and it is an order of
// magnitude shorter when the fleet sits beside the dark than when it is chasing the
// last unlit corners. A cap fitted to the second case throttles the first — which is
// the case charting is cheapest and most valuable in (sp-glyoe).
//
// Twenty outstanding waypoints, and only the walk changes.
func TestChartHulls_TheCrewFollowsTheMeasuredWalk(t *testing.T) {
	hulls := resolveChartHulls(ExpandKnobs{})
	for _, tc := range []struct{ hops, want int }{
		{1, 9},  // beside the dark: the assembly bound is what stops it, not the walk
		{2, 8},  // 20/8 = 2.5, exactly the 2-hop break-even
		{3, 5},  // 20/5 = 4.0 >= 3.5
		{5, 3},  // 20/3 = 6.7 >= 5.5
		{9, 2},  // 20/2 = 10 >= 9.5
		{13, 1}, // 20/2 = 10 < 13.5: the second hull cannot pay
		{40, 1},
	} {
		if got := hulls.budgetFor(20, walkOf(tc.hops)); got != tc.want {
			t.Fatalf("a 20-waypoint system %d hops out earned %d hulls, want %d", tc.hops, got, tc.want)
		}
	}
}

// THE BREAK-EVEN IS THE RULE AND IT IS EXACT — the last rank whose share clears its
// own walk, checked at the boundary from both sides so an off-by-one cannot hide.
func TestChartHulls_TheMarginalHullIsAddedExactlyAtItsBreakEven(t *testing.T) {
	hulls := resolveChartHulls(ExpandKnobs{})
	for hops := 1; hops <= 12; hops++ {
		for rank := 2; rank <= 6; rank++ {
			// U/rank >= hops + 0.5 first holds at U = ceil(rank*(2*hops+1)/2).
			breakEven := (rank*(2*hops+1) + 1) / 2
			if got := hulls.budgetFor(breakEven, walkOf(hops)); got < rank {
				t.Fatalf("at %d outstanding and %d hops, rank %d earns its place but the budget was %d", breakEven, hops, rank, got)
			}
			if got := hulls.budgetFor(breakEven-1, walkOf(hops)); got >= rank {
				t.Fatalf("at %d outstanding and %d hops, rank %d does NOT pay its walk but the budget was %d", breakEven-1, hops, rank, got)
			}
		}
	}
}

// THE ASSEMBLY BOUND IS WHAT A SHORT WALK RUNS INTO, and it is what keeps removing
// the fitted constant from meaning an unbounded crew. Hulls are granted one per system
// per tick and each takes one step per tick, so the ranks already aboard have taken
// rank*(rank-1)/2 of the system's 2*outstanding steps by the time the next one is
// granted; past that it arrives to a finished system. At a one-hop walk it is the
// binding constraint from fourteen outstanding waypoints upward.
func TestChartHulls_TheAssemblyBoundStopsAShortWalkFromBuyingAnUnboundedCrew(t *testing.T) {
	hulls := resolveChartHulls(ExpandKnobs{})
	for _, tc := range []struct {
		uncharted, want int
		assembled       bool
	}{
		{1, 1, false}, {5, 3, false}, {14, 7, true}, {20, 9, true}, {57, maxChartCrew, true},
	} {
		got := hulls.budgetFor(tc.uncharted, walkOf(1))
		if got != tc.want {
			t.Fatalf("budgetFor(%d) at a one-hop walk = %d, want %d", tc.uncharted, got, tc.want)
		}
		if paysWalk := paysItsWalk(tc.uncharted, got+1, 1); paysWalk != tc.assembled {
			t.Fatalf("at %d outstanding the rank after %d %s pay its walk — the wrong bound is binding",
				tc.uncharted, got, map[bool]string{true: "does not", false: "does"}[tc.assembled])
		}
	}
	// And it is the bound maxChartCrew is derived from: the largest chartable system
	// this map holds is 57 waypoints, and the next rank is where the ladder ends.
	if arrivesToWork(57, maxChartCrew+1) {
		t.Fatalf("rank %d still arrives to work on a 57-waypoint system — maxChartCrew is below its own derivation", maxChartCrew+1)
	}
	if !arrivesToWork(57, maxChartCrew) {
		t.Fatalf("rank %d does not arrive to work on a 57-waypoint system — maxChartCrew is above its own derivation", maxChartCrew)
	}
}

// AN UNMEASURABLE WALK EARNS EXACTLY ONE HULL. Nothing we hold can reach the system,
// so there is no walk to price a second against — and one hull is still correct,
// because requestSeeds may yet buy one into reach.
func TestChartHulls_AnUnmeasurableWalkEarnsExactlyOneHull(t *testing.T) {
	for _, k := range []ExpandKnobs{{}, {ChartHullCap: maxChartCrew}, {SecondChartHullAt: 1, ThirdChartHullAt: 1}} {
		for _, uncharted := range []int{0, 1, 20, 57, 100_000} {
			if got := resolveChartHulls(k).budgetFor(uncharted, chartWalk{}); got != 1 {
				t.Fatalf("budgetFor(%d) with no measured walk = %d under %+v, want exactly 1", uncharted, got, k)
			}
		}
	}
}

// AN UNSWEPT SYSTEM REPORTS ZERO AND STILL EARNS EXACTLY ONE HULL under every
// configuration and every walk: the first hull's catalog sweep is what produces a
// count to scale on, and no rank past the first can pay a walk out of nothing.
func TestChartHulls_AnUnsweptSystemEarnsExactlyOneHull(t *testing.T) {
	for _, k := range []ExpandKnobs{
		{},
		{SecondChartHullAt: 1, ThirdChartHullAt: 1},
		{ChartHullCap: maxChartCrew, SecondChartHullAt: 1, ThirdChartHullAt: 2},
		{SecondChartHullAt: 40, ThirdChartHullAt: 20},
	} {
		for _, walk := range []chartWalk{{}, walkOf(0), walkOf(1), walkOf(13)} {
			if got := resolveChartHulls(k).budgetFor(0, walk); got != 1 {
				t.Fatalf("budgetFor(0) = %d under %+v at %+v, want exactly 1", got, k, walk)
			}
		}
	}
}

// A NON-POSITIVE KNOB IS THE REVERT VERB, not a disarm — and that includes the
// derived tier, which at zero would leave every rank past the third the same floor.
func TestChartHulls_NonPositiveKnobsRevertToTheDocumentedDefaults(t *testing.T) {
	for _, k := range []ExpandKnobs{
		{},
		{ChartHullCap: 0, SecondChartHullAt: 0, ThirdChartHullAt: 0},
		{ChartHullCap: -1, SecondChartHullAt: -5, ThirdChartHullAt: -99},
	} {
		hulls := resolveChartHulls(k)
		want := chartHulls{
			cap:    maxChartCrew,
			second: defaultSecondChartHullAt,
			third:  defaultThirdChartHullAt,
			tier:   defaultThirdChartHullAt - defaultSecondChartHullAt,
		}
		if hulls != want {
			t.Fatalf("resolveChartHulls(%+v) = %+v, want the documented defaults %+v", k, hulls, want)
		}
	}
}

// chart_hull_cap IS A HARD OVERRIDE over whatever the walk earns, in both directions:
// it only ever removes hulls, and a value above the ceiling the sizing can reach
// clamps to it so the tune bound's advertised range stays true through the paths that
// never see the bound — a stored value, and the boot config.
func TestChartHulls_TheCapIsAHardOverrideOverTheMeasuredAnswer(t *testing.T) {
	// The walk earns nine on this system; every cap under that binds, and the numbers
	// it binds AT are the whole advertised range, so none of them is inert.
	for capped := 1; capped <= maxChartCrew; capped++ {
		want := capped
		if want > 9 {
			want = 9
		}
		if got := resolveChartHulls(ExpandKnobs{ChartHullCap: capped}).budgetFor(20, walkOf(1)); got != want {
			t.Fatalf("a cap of %d over a 9-hull walk gave %d, want %d", capped, got, want)
		}
	}
	for _, stale := range []int{maxChartCrew + 1, 100, 1_000} {
		if got := resolveChartHulls(ExpandKnobs{ChartHullCap: stale}).cap; got != maxChartCrew {
			t.Fatalf("a stored cap of %d resolved to %d, want the derived ceiling %d", stale, got, maxChartCrew)
		}
	}
	// NOTHING exceeds the ceiling, whatever the walk and the work say.
	hulls := resolveChartHulls(ExpandKnobs{})
	for _, uncharted := range []int{100, 1_000, 100_000} {
		if got := hulls.budgetFor(uncharted, walkOf(0)); got != maxChartCrew {
			t.Fatalf("budgetFor(%d) = %d, want the ceiling %d — nothing may exceed it", uncharted, got, maxChartCrew)
		}
	}
}

// THE KILL SWITCH. A cap of one is the whole feature turned off: every system,
// however dark and however near, is worked by a single hull over the whole list.
func TestChartHulls_ACapOfOneIsTheSingleHullTourAgain(t *testing.T) {
	hulls := resolveChartHulls(ExpandKnobs{ChartHullCap: 1})
	for _, uncharted := range []int{0, 1, 12, 24, 500} {
		for _, walk := range []chartWalk{{}, walkOf(0), walkOf(1), walkOf(9)} {
			if got := hulls.budgetFor(uncharted, walk); got != 1 {
				t.Fatalf("budgetFor(%d) at %+v = %d under cap 1, want 1", uncharted, walk, got)
			}
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

// THE THRESHOLDS ARE A BRAKE, NOT THE LADDER. They floor the outstanding work each
// rank needs on top of what the walk earns, so they can only ever take hulls away —
// and their spacing carries every rank past the third, so retuning the pair moves the
// whole brake rather than leaving a hidden constant to disagree with them.
func TestChartHulls_TheThresholdsFloorWhatTheWalkEarns(t *testing.T) {
	hulls := resolveChartHulls(ExpandKnobs{SecondChartHullAt: 10, ThirdChartHullAt: 30})
	if hulls.tier != 20 {
		t.Fatalf("tier = %d, want 20 — the step is the operator's own spacing", hulls.tier)
	}
	// A one-hop walk would earn 4 at nine outstanding and 9 at twenty; the brake holds
	// them to 1 and 2 until the system is big enough for the operator's own ladder.
	for _, tc := range []struct{ uncharted, want int }{{9, 1}, {10, 2}, {29, 2}, {30, 3}, {49, 3}, {50, 4}} {
		if got := hulls.budgetFor(tc.uncharted, walkOf(1)); got != tc.want {
			t.Fatalf("budgetFor(%d) at a 1-hop walk = %d under a 10/30 brake, want %d", tc.uncharted, got, tc.want)
		}
	}
	// It never ADDS a hull the walk did not earn: at thirteen hops the second cannot
	// pay however low the floor is set.
	loose := resolveChartHulls(ExpandKnobs{SecondChartHullAt: 1, ThirdChartHullAt: 2})
	if got := loose.budgetFor(20, walkOf(13)); got != 1 {
		t.Fatalf("budgetFor(20) at 13 hops under a floor of 1 = %d, want 1 — a floor may not buy a hull the walk refuses", got)
	}
}

// THE DOCUMENTED DEFAULTS IMPOSE NOTHING, which is what makes them defaults and not
// a second, quieter cap: the brake at its shipped values sits at or below the walk's
// own answer at every rank the sizing can reach, so the measurement decides. Checked
// from ONE HOP, the shortest walk the gate walker can report — it excludes the origin
// from its own search, so a hull is never zero hops from the system it must reach.
func TestChartHulls_TheDefaultThresholdsNeverBindBelowTheWalk(t *testing.T) {
	shipped := resolveChartHulls(ExpandKnobs{})
	unbraked := resolveChartHulls(ExpandKnobs{SecondChartHullAt: 1, ThirdChartHullAt: 2})
	for hops := 1; hops <= 20; hops++ {
		for uncharted := 0; uncharted <= 200; uncharted++ {
			want := unbraked.budgetFor(uncharted, walkOf(hops))
			if got := shipped.budgetFor(uncharted, walkOf(hops)); got != want {
				t.Fatalf("budgetFor(%d) at %d hops = %d shipped but %d unbraked — the default threshold bound", uncharted, hops, got, want)
			}
		}
	}
}

// THE BUDGET IS MONOTONIC IN THE OUTSTANDING COUNT and ANTITONIC IN THE WALK, however
// the knobs are ordered. A budget that dipped would stand a hull down over a system
// that had just got darker; one that rose with the walk would crew the far systems.
func TestChartHulls_TheBudgetIsMonotonicUnderOutOfOrderThresholds(t *testing.T) {
	for _, k := range []ExpandKnobs{
		{}, // documented order
		{SecondChartHullAt: 40, ThirdChartHullAt: 20},                    // inverted
		{SecondChartHullAt: 20, ThirdChartHullAt: 20},                    // equal, no step to read
		{ChartHullCap: 15, SecondChartHullAt: 500, ThirdChartHullAt: 1},  // third first, second unreachable
		{ChartHullCap: 4, SecondChartHullAt: 2, ThirdChartHullAt: 3},     // tighter than any real system
		{ChartHullCap: 1, SecondChartHullAt: 100, ThirdChartHullAt: 50},  // inverted under the kill switch
		{ChartHullCap: 9, SecondChartHullAt: 300, ThirdChartHullAt: 300}, // brake above every real system
	} {
		hulls := resolveChartHulls(k)
		if hulls.tier <= 0 {
			t.Fatalf("resolveChartHulls(%+v) left tier %d — a non-positive tier flattens every rank past the third", k, hulls.tier)
		}
		for hops := 0; hops <= 20; hops++ {
			previous := 0
			for uncharted := 0; uncharted <= 600; uncharted++ {
				got := hulls.budgetFor(uncharted, walkOf(hops))
				if got < 1 {
					t.Fatalf("budgetFor(%d) at %d hops = %d under %+v, want at least one hull", uncharted, hops, got, k)
				}
				if got < previous {
					t.Fatalf("budgetFor(%d) at %d hops = %d after %d under %+v — a darker system earned fewer hulls", uncharted, hops, got, previous, k)
				}
				if got > hulls.cap {
					t.Fatalf("budgetFor(%d) at %d hops = %d under %+v, above its own cap %d", uncharted, hops, got, k, hulls.cap)
				}
				if nearer := hulls.budgetFor(uncharted, walkOf(hops-1)); hops > 0 && got > nearer {
					t.Fatalf("budgetFor(%d) earned %d at %d hops but only %d one hop nearer, under %+v", uncharted, got, hops, nearer, k)
				}
				previous = got
			}
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

// gateChain lays a gate path of `hops` edges from X1-HOME to each named system, and
// returns a walker over it holding X1-HOME as the fleet's only footprint. The graph
// is real and the walker is the tick's own, so what these tests exercise is the
// measurement rather than a number handed to it.
func gateChain(hops int, systems ...string) (*chartWalks, *fakeGates) {
	adjacency := map[string][]string{}
	for i, system := range systems {
		from := "X1-HOME"
		for hop := 1; hop < hops; hop++ {
			via := fmt.Sprintf("X1-VIA%d-%d", i, hop)
			adjacency[from] = append(adjacency[from], via)
			from = via
		}
		adjacency[from] = append(adjacency[from], system)
	}
	gates := &fakeGates{adjacency: adjacency}
	return newChartWalks(newGateReach(gates, nil, SeedFlightUnbounded), []string{"X1-HOME"}), gates
}

// THE SAME LEDGER, TWO MAPS. One dark system with twenty outstanding waypoints and a
// crew of three: beside the fleet it is still owed six more hulls, and out at the
// frontier its three are already two too many. Nothing about the system changed —
// only how far a hull has to fly to reach it, which is the input a fitted constant
// could not carry across an era (sp-glyoe).
func TestSeedlessTargets_TheSameSystemDrawsADifferentCrewOnADifferentMap(t *testing.T) {
	hulls := resolveChartHulls(ExpandKnobs{})
	rows := func() []ExpandSystem {
		return []ExpandSystem{{
			System: "X1-DARK", Verdict: VerdictPending, CatalogKnown: true, UnchartedCount: 20,
			SeedShip: "PROBE-A", SeedState: SeedStateCharting,
			ExtraSeeds: []SeedErrand{
				{Ship: "PROBE-B", State: SeedStateCharting},
				{Ship: "PROBE-C", State: SeedStateDispatched},
			},
		}}
	}

	near, _ := gateChain(1, "X1-DARK")
	targets, err := seedlessTargets(context.Background(), rows(), hulls, near)
	if err != nil {
		t.Fatalf("seedlessTargets over the near map: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets = %v over a one-hop map, want X1-DARK — its share still clears a one-hop walk nine hulls deep", targets)
	}

	far, _ := gateChain(13, "X1-DARK")
	targets, err = seedlessTargets(context.Background(), rows(), hulls, far)
	if err != nil {
		t.Fatalf("seedlessTargets over the far map: %v", err)
	}
	if len(targets) != 0 {
		t.Fatalf("targets = %v over a thirteen-hop map, want none — no second hull's share pays that walk", targets)
	}
}

// A DARK SYSTEM STAYS A TARGET UNTIL ITS CREW IS FULL, and full is what the walk says
// it is: five hops earns three hulls on a twenty-waypoint system, so the third is
// owed and a fourth is not.
func TestSeedlessTargets_ADarkSystemStaysATargetUntilItsCrewIsFull(t *testing.T) {
	hulls := resolveChartHulls(ExpandKnobs{})
	rows := []ExpandSystem{{
		System: "X1-BIG", Verdict: VerdictPending, CatalogKnown: true, UnchartedCount: 20,
		SeedShip: "PROBE-A", SeedState: SeedStateCharting,
		ExtraSeeds: []SeedErrand{{Ship: "PROBE-B", State: SeedStateCharting}},
	}}
	walks, _ := gateChain(5, "X1-BIG")

	targets, err := seedlessTargets(context.Background(), rows, hulls, walks)
	if err != nil {
		t.Fatalf("seedlessTargets: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets = %v, want X1-BIG — two of its three hulls are aboard and the third is still owed", targets)
	}

	rows[0].ExtraSeeds = append(rows[0].ExtraSeeds, SeedErrand{Ship: "PROBE-C", State: SeedStateDispatched})
	targets, err = seedlessTargets(context.Background(), rows, hulls, walks)
	if err != nil {
		t.Fatalf("seedlessTargets: %v", err)
	}
	if len(targets) != 0 {
		t.Fatalf("targets = %v, want none — a full crew must not draw a fourth hull", targets)
	}
}

// A SYSTEM NOTHING WE HOLD CAN REACH IS FULL ON ONE HULL. There is no walk to price a
// second against, and inventing one is how a probe is committed to a flight the
// router cannot resolve (sp-c0oyu).
func TestSeedlessTargets_AnUnreachableSystemIsFullOnOneHull(t *testing.T) {
	hulls := resolveChartHulls(ExpandKnobs{})
	rows := []ExpandSystem{{
		System: "X1-ISLAND", Verdict: VerdictPending, CatalogKnown: true, UnchartedCount: 57,
		SeedShip: "PROBE-A", SeedState: SeedStateCharting,
	}}
	walks, _ := gateChain(1, "X1-ELSEWHERE")

	targets, err := seedlessTargets(context.Background(), rows, hulls, walks)
	if err != nil {
		t.Fatalf("seedlessTargets: %v", err)
	}
	if len(targets) != 0 {
		t.Fatalf("targets = %v, want none — nothing we hold can reach X1-ISLAND, so nothing prices a second hull", targets)
	}
}

// THE WALK IS PAID FOR ONLY WHERE IT CAN CHANGE AN ANSWER. A system nobody is
// charting earns its first hull under every walk, so measuring one there would spend
// a gate traversal per uncrewed target on a tick that already has an action budget.
func TestSeedlessTargets_AnUncrewedSystemCostsNoGateWalk(t *testing.T) {
	hulls := resolveChartHulls(ExpandKnobs{})
	rows := []ExpandSystem{
		{System: "X1-A", Verdict: VerdictPending, CatalogKnown: true, UnchartedCount: 40},
		{System: "X1-B", Verdict: VerdictPending, CatalogKnown: false},
		{System: "X1-C", Verdict: VerdictInScope, CatalogKnown: true, UnchartedCount: 0},
	}
	walks, gates := gateChain(1, "X1-A", "X1-B", "X1-C")

	targets, err := seedlessTargets(context.Background(), rows, hulls, walks)
	if err != nil {
		t.Fatalf("seedlessTargets: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("targets = %v, want the two systems with work", targets)
	}
	if gates.calls != 0 {
		t.Fatalf("sizing an uncrewed list read the gate store %d times, want 0", gates.calls)
	}
}

// UNDER THE CAP THE OLD RULE RETURNS EXACTLY: one hull per system, whatever the
// count and however near the system.
func TestSeedlessTargets_ACapOfOneRestoresOneHullPerSystem(t *testing.T) {
	hulls := resolveChartHulls(ExpandKnobs{ChartHullCap: 1})
	rows := []ExpandSystem{{
		System: "X1-BIG", Verdict: VerdictPending, CatalogKnown: true, UnchartedCount: 60,
		SeedShip: "PROBE-A", SeedState: SeedStateCharting,
	}}
	walks, _ := gateChain(1, "X1-BIG")

	targets, err := seedlessTargets(context.Background(), rows, hulls, walks)
	if err != nil {
		t.Fatalf("seedlessTargets: %v", err)
	}
	if len(targets) != 0 {
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

// PARTIAL COVERAGE: a system owed more hulls takes exactly ONE PER TICK, and the new
// errand goes onto the roster rather than over the incumbent.
//
// The one-per-tick grant is not incidental — it is the rate the assembly bound in
// chartcrew.go is derived from. This system is one gate hop out with twenty
// outstanding waypoints, so the measured walk earns it a crew of nine and it holds
// one; two spares are parked and in reach, and it still takes a single hull.
func TestAdvanceExpansion_AnUnderCrewedSystemTakesExactlyOneMoreHull(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-HOME", Verdict: VerdictInScope, CatalogKnown: true},
		{
			System: "X1-DARK", Verdict: VerdictPending, CatalogKnown: true,
			UnchartedCount: 20,
			SeedShip:       "PROBE-A", SeedState: SeedStateCharting,
		},
	}
	h.gates.adjacency = map[string][]string{"X1-HOME": {"X1-DARK"}, "X1-DARK": {"X1-HOME"}}
	h.ships.positions["PROBE-A"] = ShipPos{
		Waypoint: "X1-DARK-GATE", NavStatus: navigation.NavStatusInOrbit, Found: true,
	}
	// Two parked spares, so the tick could over-crew if it wanted to.
	for _, waypoint := range []string{"X1-HOME-S1", "X1-HOME-S2"} {
		h.ledger.slots = append(h.ledger.slots, QueuedSlot{
			Waypoint: waypoint, System: "X1-HOME", Kind: SlotKindSpare,
			State: SlotStateParked, AssignedShip: "PROBE-" + waypoint[len(waypoint)-2:],
		})
	}
	uncharted := &fakeUncharted{stops: map[string][]ChartStop{"X1-DARK": ringStops("X1-DARK", 20)}}

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
