package trading

import "testing"

// laneKeys renders the (good, source, dest) identity of each lane, which is what a census counts.
func laneKeys(lanes []ArbitrageLane) []string {
	out := make([]string, 0, len(lanes))
	for _, l := range lanes {
		out = append(out, l.Good+" "+l.SourceWaypoint+"->"+l.DestWaypoint)
	}
	return out
}

// oneSourceTwoSinks is one good with a single cheap exporter and two distinct import sinks: two
// lanes exist, and they differ in depth so a selector has a reason to prefer one.
func oneSourceTwoSinks() []GoodListing {
	return []GoodListing{
		{Good: "FUEL", Waypoint: "X1-AA-1", TradeType: "EXPORT", Bid: 50, Ask: 100, Volume: 50},
		{Good: "FUEL", Waypoint: "X1-AA-2", TradeType: "IMPORT", Bid: 2000, Ask: 3000, Volume: 50},
		{Good: "FUEL", Waypoint: "X1-BB-3", TradeType: "IMPORT", Bid: 1500, Ask: 2200, Volume: 40},
	}
}

// THE DISTINCTION THIS PACKAGE MUST KEEP: EnumerateLanes answers "how much profitable work
// exists", RankSpreads answers "which lane should this hull fly". Counting with the selector
// collapses a good's whole lane set to its best member.
func TestEnumerateLanes_ReturnsEveryPairWhereRankSpreadsKeepsOne(t *testing.T) {
	listings := oneSourceTwoSinks()

	census := EnumerateLanes(listings)
	selected := RankSpreads(listings)

	if got := laneKeys(census); len(got) != 2 {
		t.Fatalf("census = %v, want both AA-1→AA-2 and AA-1→BB-3", got)
	}
	if len(selected) != 1 {
		t.Fatalf("selection = %v, want the single best lane for FUEL — the census above is only "+
			"meaningful against a selector that keeps one", laneKeys(selected))
	}
}

// The census carries the same lane economics as the selector, so a caller can apply the bid-floor
// (ClearsFloor) or any other per-lane test to it. A census of unpriced pairs would need a second
// pricing pass and the two could disagree.
func TestEnumerateLanes_PricesEachLaneLikeTheSelector(t *testing.T) {
	listings := oneSourceTwoSinks()

	best := RankSpreads(listings)[0]

	for _, l := range EnumerateLanes(listings) {
		if l.SourceWaypoint != best.SourceWaypoint || l.DestWaypoint != best.DestWaypoint {
			continue
		}
		if l != best {
			t.Fatalf("census lane %+v differs from the selected lane %+v for the same pair", l, best)
		}
		return
	}
	t.Fatalf("the census omitted the lane the selector chose: %v", laneKeys(EnumerateLanes(listings)))
}

// Every tradeability rule the selector applies binds the census too — same helper, one statement
// of the rules. A crossed quote is impossible data, an exporter is never a sink, and a market is
// not a lane to itself.
func TestEnumerateLanes_AppliesTheSameTradeabilityRules(t *testing.T) {
	cases := map[string][]GoodListing{
		"crossed source quote": {
			{Good: "FUEL", Waypoint: "X1-AA-1", TradeType: "EXPORT", Bid: 900, Ask: 100, Volume: 50},
			{Good: "FUEL", Waypoint: "X1-AA-2", TradeType: "IMPORT", Bid: 2000, Ask: 3000, Volume: 50},
		},
		"export sink": {
			{Good: "FUEL", Waypoint: "X1-AA-1", TradeType: "EXPORT", Bid: 50, Ask: 100, Volume: 50},
			{Good: "FUEL", Waypoint: "X1-AA-2", TradeType: "EXPORT", Bid: 2000, Ask: 3000, Volume: 50},
		},
		"one market only": {
			{Good: "FUEL", Waypoint: "X1-AA-1", TradeType: "IMPORT", Bid: 50, Ask: 100, Volume: 50},
		},
	}

	for name, listings := range cases {
		t.Run(name, func(t *testing.T) {
			if got := EnumerateLanes(listings); len(got) != 0 {
				t.Fatalf("census = %v, want no lane", laneKeys(got))
			}
		})
	}
}

// ClearsFloorAfterGates is the census's trip-value guard: gross minus one fee per gate crossed,
// held to MinBidMargin over every unit the trip moved. Every lane below clears the floor BEFORE the
// crossing is priced in, so what each case exercises is the deduction and never the floor alone.
//
// "Exactly repaying the crossing" is the case that separates this from a break-even test: that trip
// ends the day square with its gate and with nothing above the floor, which is not work.
func TestClearsFloorAfterGates(t *testing.T) {
	const fee int64 = 5_900

	cases := []struct {
		name string
		lane ArbitrageLane
		hops int
		want bool
	}{
		{"deep lane at home pays no gate", ArbitrageLane{SpreadPerUnit: 1000, VolumeCap: 50}, 0, true},
		{"zero absorption earns nothing at any spread", ArbitrageLane{SpreadPerUnit: 100_000, VolumeCap: 0}, 0, false},
		{"a thin trip loses to one crossing", ArbitrageLane{SpreadPerUnit: 1000, VolumeCap: 1}, 1, false},
		{"the same trip at home is work", ArbitrageLane{SpreadPerUnit: 1000, VolumeCap: 1}, 0, true},
		{"exactly repaying the crossing is not work", ArbitrageLane{SpreadPerUnit: 5900, VolumeCap: 1}, 1, false},
		{"a floor-clearing trip with no room for its gate", ArbitrageLane{SpreadPerUnit: 1000, VolumeCap: 10}, 1, false},
		{"one gate this lane can carry", ArbitrageLane{SpreadPerUnit: 1600, VolumeCap: 10}, 1, true},
		{"two gates it cannot", ArbitrageLane{SpreadPerUnit: 1600, VolumeCap: 10}, 2, false},
		{"negative hops fail closed", ArbitrageLane{SpreadPerUnit: 100_000, VolumeCap: 100}, -1, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !tc.lane.ClearsFloor() {
				t.Fatalf("fixture %+v does not clear the per-unit floor, so the trip-cost test is not what it exercises", tc.lane)
			}
			if got := tc.lane.ClearsFloorAfterGates(tc.hops, fee); got != tc.want {
				t.Fatalf("ClearsFloorAfterGates(%d, %d) on %+v = %v, want %v", tc.hops, fee, tc.lane, got, tc.want)
			}
		})
	}

	rich := ArbitrageLane{SpreadPerUnit: 100_000, VolumeCap: 100}
	if rich.ClearsFloorAfterGates(2, -1) {
		t.Fatal("a negative fee must fail closed, not credit the trip for crossing")
	}
}

// A census built from a map must not reorder between runs, or a caller that truncates or logs the
// head of it reads a different answer each tick.
func TestEnumerateLanes_IsDeterministicAcrossRuns(t *testing.T) {
	listings := []GoodListing{
		{Good: "FUEL", Waypoint: "X1-AA-1", TradeType: "EXPORT", Bid: 50, Ask: 100, Volume: 50},
		{Good: "FUEL", Waypoint: "X1-AA-2", TradeType: "EXPORT", Bid: 50, Ask: 100, Volume: 50},
		{Good: "FUEL", Waypoint: "X1-BB-1", TradeType: "IMPORT", Bid: 2000, Ask: 3000, Volume: 50},
		{Good: "GOLD", Waypoint: "X1-AA-1", TradeType: "EXPORT", Bid: 50, Ask: 100, Volume: 50},
		{Good: "GOLD", Waypoint: "X1-BB-1", TradeType: "IMPORT", Bid: 2000, Ask: 3000, Volume: 50},
	}

	first := laneKeys(EnumerateLanes(listings))
	if len(first) != 3 {
		t.Fatalf("census = %v, want two FUEL lanes and one GOLD lane", first)
	}
	for i := 0; i < 20; i++ {
		next := laneKeys(EnumerateLanes(listings))
		for j := range first {
			if next[j] != first[j] {
				t.Fatalf("run %d ordered the census %v, first run had %v", i, next, first)
			}
		}
	}
}
