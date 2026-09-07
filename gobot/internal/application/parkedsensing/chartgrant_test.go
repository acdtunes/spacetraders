package parkedsensing

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// chartgrant_test.go pins the RATE a crew assembles at, not the SIZE it may reach
// (chartcrew_test.go). EVERY FIXTURE HOLDS MORE SPARES THAN ANY BUDGET HERE CAN CLAIM.

func idleGrantBudget() int {
	return PacedBudget(MaxChartGrantsPerSystem, 0, ExpansionHeadroomMultiple)
}

func boundGrantBudget() int {
	return PacedBudget(MaxChartGrantsPerSystem, trading.APISaturationPermilleMax, ExpansionHeadroomMultiple)
}

// darkSystemAtWalk stands one uncrewed dark system `hops` from the only system holding
// placements, with `spares` hulls parked there — the walk being what prices a hull.
func darkSystemAtWalk(h *expandHarness, hops, uncharted, spares int) *fakeUncharted {
	path := []string{"X1-HOME"}
	for i := 1; i < hops; i++ {
		path = append(path, fmt.Sprintf("X1-MID%d", i))
	}
	path = append(path, "X1-DARK")

	h.gates.adjacency = map[string][]string{}
	for i := 0; i+1 < len(path); i++ {
		h.gates.adjacency[path[i]] = append(h.gates.adjacency[path[i]], path[i+1])
		h.gates.adjacency[path[i+1]] = append(h.gates.adjacency[path[i+1]], path[i])
	}

	h.ledger.systems = nil
	for _, system := range path[:len(path)-1] {
		h.ledger.systems = append(h.ledger.systems,
			ExpandSystem{System: system, Verdict: VerdictInScope, CatalogKnown: true})
	}
	h.ledger.systems = append(h.ledger.systems, ExpandSystem{
		System: "X1-DARK", Verdict: VerdictPending, CatalogKnown: true, UnchartedCount: uncharted,
	})

	for i := 0; i < spares; i++ {
		h.ledger.slots = append(h.ledger.slots, QueuedSlot{
			Waypoint: fmt.Sprintf("X1-HOME-S%02d", i), System: "X1-HOME", Kind: SlotKindSpare,
			State: SlotStateParked, AssignedShip: fmt.Sprintf("PROBE-S%02d", i),
		})
	}
	return &fakeUncharted{stops: map[string][]ChartStop{"X1-DARK": ringStops("X1-DARK", uncharted)}}
}

func runGrantAt(t *testing.T, h *expandHarness, uncharted *fakeUncharted, budget int) ExpandReport {
	t.Helper()
	rep, err := h.runWithKnobs(t, uncharted, ExpandKnobs{
		SeedsEnabled: true, MinBudgetRate: 0.05, Whitelist: h.whitelist,
		MaxChartGrants: budget,
	})
	require.NoError(t, err)
	return rep
}

// A FULLY COMMITTED BUDGET IS THE PASS AS IT SHIPPED: one hull per system per tick,
// however much crew the system earned and however many spares stand ready.
func TestChartGrant_AFullyCommittedBudgetGrantsExactlyOneHullPerSystemPerTick(t *testing.T) {
	require.Equal(t, MaxChartGrantsPerSystem, boundGrantBudget(),
		"at the request ceiling the paced budget is the shipped constant")

	h := newExpandHarness()
	uncharted := darkSystemAtWalk(h, 1, 20, 8)
	require.Equal(t, 9, resolveChartHulls(ExpandKnobs{}).budgetFor(20, walkOf(1)),
		"the fixture must earn a crew the budget can bind against")

	rep := runGrantAt(t, h, uncharted, boundGrantBudget())

	require.Equal(t, 1, rep.SeedsClaimed, "one hull per system per tick, as before")
	require.Empty(t, h.ledger.extraSeeds, "the single grant takes the primary slot, as a lone tour always did")
	require.Equal(t, 1, rep.ChartGrantUsed)
	require.Equal(t, 1, rep.ChartGrantLimit)
}

// THE CHANGE ITSELF: an idle budget hands the same system its earned crew at once. A pass
// looping on its own constant returns 1 here.
func TestChartGrant_AnIdleBudgetCrewsASystemInOneTick(t *testing.T) {
	want := idleGrantBudget()
	require.Equal(t, MaxChartGrantsPerSystem*ExpansionHeadroomMultiple, want,
		"the fixture must exercise the whole headroom")

	h := newExpandHarness()
	uncharted := darkSystemAtWalk(h, 1, 20, 8)

	rep := runGrantAt(t, h, uncharted, want)

	require.Equal(t, want, rep.SeedsClaimed, "the pass must spend the budget it was handed")
	require.Len(t, h.ledger.extraSeeds, want-1, "the first grant is the primary slot, the rest are extras")
	require.Equal(t, want, rep.ChartGrantUsed, "used == limit names the grant rate as the binding cap")
	require.Equal(t, want, rep.ChartGrantLimit)
}

// paysItsWalk STILL GATES EVERY HULL OF THE BURST: nine waypoints two hops out earn three
// and no more — 2*9 >= 3*(2*2+1), not 4*(2*2+1) — where the budget would admit six.
func TestChartGrant_PaysItsWalkGatesEveryHullOfTheBurst(t *testing.T) {
	h := newExpandHarness()
	uncharted := darkSystemAtWalk(h, 2, 9, 8)

	entitled := resolveChartHulls(ExpandKnobs{}).budgetFor(9, walkOf(2))
	require.Equal(t, 3, entitled)
	require.True(t, paysItsWalk(9, entitled, 2))
	require.False(t, paysItsWalk(9, entitled+1, 2), "the walk is what stops the fourth hull, not the budget")
	require.True(t, arrivesToWork(9, entitled+1), "and the assembly bound would have allowed it")

	rep := runGrantAt(t, h, uncharted, idleGrantBudget())

	require.Equal(t, entitled, rep.SeedsClaimed, "the crew stops at the break-even, not at the budget")
	require.Equal(t, entitled, rep.ChartGrantUsed)
	require.Less(t, rep.ChartGrantUsed, rep.ChartGrantLimit,
		"used below limit is the honest reading: the entitlement bound this system, not the rate")
}

// arrivesToWork STILL GATES EVERY HULL OF THE BURST, the mutant that matters most: it is
// what binds at real system sizes. Fifty-two waypoints one hop out earn FOURTEEN — 14*13/2
// = 91 < 104, where 15*14/2 = 105 is not — where the walk would pay for thirty-four.
func TestChartGrant_ArrivesToWorkGatesEveryHullOfTheBurst(t *testing.T) {
	h := newExpandHarness()
	uncharted := darkSystemAtWalk(h, 1, 52, 20)

	entitled := resolveChartHulls(ExpandKnobs{}).budgetFor(52, walkOf(1))
	require.Equal(t, 14, entitled)
	require.False(t, arrivesToWork(52, entitled+1), "the assembly bound is what stops the fifteenth hull")
	require.True(t, paysItsWalk(52, entitled+1, 1), "and the walk would have paid for it")
	require.Less(t, entitled+1, maxChartCrew+1, "and the ceiling would have allowed it")

	rep := runGrantAt(t, h, uncharted, 20)

	require.Equal(t, entitled, rep.SeedsClaimed, "the crew stops at the assembly bound, not at the budget")
	require.Equal(t, entitled, rep.ChartGrantUsed)
	require.Less(t, rep.ChartGrantUsed, rep.ChartGrantLimit)
}

// BOTH TESTS, AT EVERY RANK THE BURST GRANTED, over the hulls that actually left: a burst
// pricing only its first hull, or only its last, passes every test above and fails this.
func TestChartGrant_EveryHullOfABurstClearsBothEconomicTestsAtItsOwnRank(t *testing.T) {
	const uncharted, hops = 52, 1

	h := newExpandHarness()
	stops := darkSystemAtWalk(h, hops, uncharted, 20)
	rep := runGrantAt(t, h, stops, 20)
	require.Positive(t, rep.SeedsClaimed, "the burst must actually grant something to be worth checking")

	for rank := 2; rank <= rep.SeedsClaimed; rank++ {
		require.True(t, paysItsWalk(uncharted, rank, hops),
			"hull %d left without its share of the tour paying for its walk", rank)
		require.True(t, arrivesToWork(uncharted, rank),
			"hull %d left for a system whose work would be gone before it arrived", rank)
	}
	require.False(t,
		paysItsWalk(uncharted, rep.SeedsClaimed+1, hops) && arrivesToWork(uncharted, rep.SeedsClaimed+1),
		"the burst stopped short of a rank both tests would have admitted")
}

// A SYSTEM AT ITS ENTITLEMENT TAKES NOTHING: the rate can only close the gap between the
// crew a system holds and the crew it has earned.
func TestChartGrant_AFullCrewDrawsNoHullAtAnyBudget(t *testing.T) {
	h := newExpandHarness()
	uncharted := darkSystemAtWalk(h, 13, 20, 8)
	require.Equal(t, 1, resolveChartHulls(ExpandKnobs{}).budgetFor(20, walkOf(13)),
		"a thirteen-hop walk earns exactly one hull")

	rep := runGrantAt(t, h, uncharted, idleGrantBudget())
	require.Equal(t, 1, rep.SeedsClaimed, "the one hull it earns, and no burst behind it")

	h.ledger.systems[len(h.ledger.systems)-1].SeedShip = "PROBE-S00"
	h.ledger.systems[len(h.ledger.systems)-1].SeedState = SeedStateDispatched
	h.ledger.extraSeeds = nil
	h.ledger.setSeeds = nil

	full := newExpandHarness()
	full.gates.adjacency = h.gates.adjacency
	full.ledger.systems = h.ledger.systems
	full.ledger.slots = []QueuedSlot{{
		Waypoint: "X1-HOME-S99", System: "X1-HOME", Kind: SlotKindSpare,
		State: SlotStateParked, AssignedShip: "PROBE-S99",
	}}
	rep = runGrantAt(t, full, uncharted, idleGrantBudget())
	require.Zero(t, rep.SeedsClaimed, "a full crew draws nothing however idle the request budget")
	require.Zero(t, rep.ChartGrantUsed)
}
