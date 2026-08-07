package trading

import (
	"sort"
	"testing"
)

// twoGoodsThatRankInterleaved quotes two goods from one exporter into two sinks, priced so the four
// lanes RANK GOLD, FUEL, GOLD, FUEL by volume-capped spread. Any walk that emits in ranked order
// therefore splits each good across two runs, which is what makes the contiguity assertion below
// discriminating rather than an accident of the fixture.
func twoGoodsThatRankInterleaved() []GoodListing {
	listings := []GoodListing{
		{Good: "FUEL", Waypoint: "X1-AA-1", TradeType: "EXPORT", Bid: 50, Ask: 100, Volume: 50},
		{Good: "GOLD", Waypoint: "X1-AA-1", TradeType: "EXPORT", Bid: 50, Ask: 100, Volume: 50},
	}
	// Sink bids, ordered so the two goods' capped spreads alternate: 50k, 55k, 60k, 65k.
	for _, sink := range []struct {
		waypoint  string
		fuel, ask int
		gold      int
	}{
		{"X1-AA-2", 1100, 2100, 1200},
		{"X1-BB-3", 1300, 2300, 1400},
	} {
		listings = append(listings,
			GoodListing{Good: "FUEL", Waypoint: sink.waypoint, TradeType: "IMPORT", Bid: sink.fuel, Ask: sink.ask, Volume: 50},
			GoodListing{Good: "GOLD", Waypoint: sink.waypoint, TradeType: "IMPORT", Bid: sink.gold, Ask: sink.ask, Volume: 50},
		)
	}
	return listings
}

// WalkLanes and EnumerateLanes must answer the same question. EnumerateLanes is now the walk plus a
// ranking, so a census counting one and a selector ranking the other cannot come to disagree about
// which lanes exist.
func TestWalkLanes_VisitsExactlyWhatEnumerateLanesReturns(t *testing.T) {
	listings := twoGoodsThatRankInterleaved()

	var walked []ArbitrageLane
	WalkLanes(listings, func(l ArbitrageLane) { walked = append(walked, l) })

	got, want := laneKeys(walked), laneKeys(EnumerateLanes(listings))
	if len(got) == 0 {
		t.Fatalf("the fixture produced no lanes, so this test asserts nothing")
	}
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("walk visited %d lanes, EnumerateLanes returned %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("walk and EnumerateLanes disagree at %d: %q vs %q", i, got[i], want[i])
		}
	}
}

// THE CONTIGUITY IS LOAD-BEARING, not incidental ordering. A counter scopes its per-good identity
// set to the good in hand and resets it on the boundary; a good visited in two runs resets that set
// mid-good, so a duplicate listing's lane is counted twice and the count OVER-states — the direction
// that authorises a purchase (RULINGS #4).
func TestWalkLanes_VisitsEachGoodsLanesInOneContiguousRun(t *testing.T) {
	listings := twoGoodsThatRankInterleaved()

	// Calibration: the RANKED order of these same lanes splits a good across two runs. Without
	// this the assertion below would pass on a fixture no ordering could ever break.
	var ranked []string
	for _, l := range EnumerateLanes(listings) {
		ranked = append(ranked, l.Good)
	}
	if _, repeated := firstRepeatedRun(ranked); !repeated {
		t.Fatalf("fixture ranks as %v — no ordering splits a good here, so contiguity is vacuous", ranked)
	}

	var walked []string
	WalkLanes(listings, func(l ArbitrageLane) { walked = append(walked, l.Good) })
	if good, repeated := firstRepeatedRun(walked); repeated {
		t.Fatalf("%q was visited in more than one run: %v", good, walked)
	}
}

// firstRepeatedRun names the first good that appears in two separate runs of the sequence.
func firstRepeatedRun(goods []string) (string, bool) {
	seen := map[string]bool{}
	previous := ""
	for _, good := range goods {
		if good == previous {
			continue
		}
		if seen[good] {
			return good, true
		}
		seen[good], previous = true, good
	}
	return "", false
}
