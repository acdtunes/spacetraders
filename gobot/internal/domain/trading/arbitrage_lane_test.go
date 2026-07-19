package trading

import "testing"

// spreadFixture is a hand-computed, four-good system used to pin RankSpreads'
// arithmetic and ordering. The numbers are chosen so that:
//
//   - FIREARMS has the deepest volume-capped spread and must rank first even
//     though GADGETS has a HIGHER per-unit spread (proves volume-capping, not raw
//     per-unit spread, drives the ranking).
//   - MICROPROCESSORS appears in only one market → no pair → omitted.
//   - WATER has no positive-spread pair → omitted.
//
// Column semantics (market-doctrine): Bid = BUY column = what we RECEIVE selling
// TO the market; Ask = SELL column = what we PAY buying FROM the market. A lane
// buys at a source's Ask (an exporter) and sells at a dest's Bid (an importer).
func spreadFixture() []GoodListing {
	return []GoodListing{
		// FIREARMS: buy at E41 (export, Ask 300), sell at J56 (import, Bid 900).
		// spread/unit = 900 − 300 = 600; cap = min(60,20) = 20; capped = 12000.
		{Good: "FIREARMS", Waypoint: "X1-SYS-E41", TradeType: "EXPORT", Bid: 250, Ask: 300, Supply: "MODERATE", Activity: "STRONG", Volume: 60},
		{Good: "FIREARMS", Waypoint: "X1-SYS-J56", TradeType: "IMPORT", Bid: 900, Ask: 950, Supply: "SCARCE", Activity: "GROWING", Volume: 20},

		// GADGETS: buy at A1 (export, Ask 100), sell at B2 (import, Bid 1100).
		// spread/unit = 1000 (HIGHER than FIREARMS) but cap = min(2,40) = 2, so
		// capped = 2000 — must rank BELOW FIREARMS.
		{Good: "GADGETS", Waypoint: "X1-SYS-A1", TradeType: "EXPORT", Bid: 80, Ask: 100, Supply: "HIGH", Activity: "WEAK", Volume: 2},
		{Good: "GADGETS", Waypoint: "X1-SYS-B2", TradeType: "IMPORT", Bid: 1100, Ask: 1150, Supply: "SCARCE", Activity: "GROWING", Volume: 40},

		// MICROPROCESSORS: single market → no source/dest pair → omitted.
		{Good: "MICROPROCESSORS", Waypoint: "X1-SYS-C3", TradeType: "EXPORT", Bid: 950, Ask: 1000, Supply: "MODERATE", Activity: "STRONG", Volume: 10},

		// WATER: two markets but every cross bid ≤ ask → no positive spread → omitted.
		{Good: "WATER", Waypoint: "X1-SYS-D4", TradeType: "EXCHANGE", Bid: 50, Ask: 60, Supply: "ABUNDANT", Activity: "STRONG", Volume: 100},
		{Good: "WATER", Waypoint: "X1-SYS-E5", TradeType: "EXCHANGE", Bid: 55, Ask: 65, Supply: "ABUNDANT", Activity: "STRONG", Volume: 100},
	}
}

func TestRankSpreads_MatchesHandComputedRankingAndVolumeCap(t *testing.T) {
	lanes := RankSpreads(spreadFixture())

	if len(lanes) != 2 {
		t.Fatalf("expected 2 profitable lanes (FIREARMS, GADGETS); MICROPROCESSORS and WATER must be omitted, got %d: %+v", len(lanes), lanes)
	}

	// FIREARMS ranks first on volume-capped spread despite GADGETS' higher
	// per-unit spread — this is the whole point of capping by min volume.
	top := lanes[0]
	if top.Good != "FIREARMS" {
		t.Fatalf("expected FIREARMS to rank first (capped 12000 > GADGETS 2000), got %q", top.Good)
	}
	if top.SourceWaypoint != "X1-SYS-E41" || top.DestWaypoint != "X1-SYS-J56" {
		t.Fatalf("FIREARMS lane must buy at exporter E41 and sell at importer J56, got source=%q dest=%q", top.SourceWaypoint, top.DestWaypoint)
	}
	if top.SourceAsk != 300 || top.DestBid != 900 {
		t.Fatalf("FIREARMS must pay source Ask 300 and receive dest Bid 900, got ask=%d bid=%d", top.SourceAsk, top.DestBid)
	}
	if top.SpreadPerUnit != 600 {
		t.Fatalf("FIREARMS spread/unit must be destBid(900)−sourceAsk(300)=600, got %d", top.SpreadPerUnit)
	}
	if top.VolumeCap != 20 {
		t.Fatalf("FIREARMS volume cap must be min(60,20)=20, got %d", top.VolumeCap)
	}
	if top.CappedSpread != 12000 {
		t.Fatalf("FIREARMS capped spread must be 600*20=12000, got %d", top.CappedSpread)
	}

	second := lanes[1]
	if second.Good != "GADGETS" {
		t.Fatalf("expected GADGETS to rank second, got %q", second.Good)
	}
	if second.SpreadPerUnit != 1000 {
		t.Fatalf("GADGETS spread/unit must be 1100−100=1000, got %d", second.SpreadPerUnit)
	}
	if second.VolumeCap != 2 {
		t.Fatalf("GADGETS volume cap must be min(2,40)=2, got %d", second.VolumeCap)
	}
	if second.CappedSpread != 2000 {
		t.Fatalf("GADGETS capped spread must be 1000*2=2000, got %d", second.CappedSpread)
	}
}

// A lane's per-unit spread clears the bid-floor discipline exactly when it is at
// or above MinBidMargin — the same inclusive boundary MarginAlive enforces per
// visit (basis+MinBidMargin is alive, one credit below is dead). Selection and
// execution must agree on this one predicate.
func TestArbitrageLane_ClearsFloor(t *testing.T) {
	atFloor := ArbitrageLane{SourceAsk: 500, DestBid: 500 + MinBidMargin, SpreadPerUnit: MinBidMargin}
	if !atFloor.ClearsFloor() {
		t.Fatalf("spread/u == MinBidMargin (%d) must clear the floor", MinBidMargin)
	}

	below := ArbitrageLane{SourceAsk: 500, DestBid: 500 + MinBidMargin - 1, SpreadPerUnit: MinBidMargin - 1}
	if below.ClearsFloor() {
		t.Fatalf("spread/u one credit below MinBidMargin (%d) must not clear the floor", MinBidMargin-1)
	}

	// ClearsFloor must agree with the executor's own gate for the same numbers.
	if atFloor.ClearsFloor() != MarginAlive(atFloor.DestBid, atFloor.SourceAsk) {
		t.Fatal("ClearsFloor must equal MarginAlive(DestBid, SourceAsk) — selection and execution share one predicate")
	}
}

// FirstDisciplinedLane must walk RankSpreads-ordered lanes and return the DEEPEST
// volume-capped lane that clears the floor — skipping a top-ranked lane whose
// per-unit spread is sub-floor. This is the exact scenario: the scan ranks
// FIREARMS #1 by capped spread (600/u × 20 = 12000) but its per-unit spread (600)
// is below the 1000 floor, while the lower-ranked GADGETS (1000/u × 2 = 2000)
// clears it. The executor must select GADGETS, not the sub-floor FIREARMS.
func TestFirstDisciplinedLane_SkipsTopCappedSubFloorLane(t *testing.T) {
	lanes := RankSpreads(spreadFixture())
	if len(lanes) < 2 || lanes[0].Good != "FIREARMS" || lanes[1].Good != "GADGETS" {
		t.Fatalf("fixture precondition: expected ranked [FIREARMS, GADGETS], got %+v", lanes)
	}
	if lanes[0].ClearsFloor() {
		t.Fatalf("precondition: top lane FIREARMS (spread/u %d) must be sub-floor", lanes[0].SpreadPerUnit)
	}

	lane, ok := FirstDisciplinedLane(lanes)
	if !ok {
		t.Fatal("expected a disciplined lane (GADGETS clears the 1000 floor)")
	}
	if lane.Good != "GADGETS" {
		t.Fatalf("expected GADGETS (spread/u 1000 >= floor); executor must skip the deeper sub-floor FIREARMS, got %q", lane.Good)
	}
}

// When no ranked lane clears the floor, FirstDisciplinedLane must report ok=false
// so the caller can exit with a 'no disciplined lane' message rather than pick a
// lane the executor would refuse (a silent zero-visit run).
func TestFirstDisciplinedLane_NoneClearFloor(t *testing.T) {
	// One profitable-but-sub-floor lane (780/u < 1000).
	lanes := []ArbitrageLane{
		{Good: "FOOD", SourceWaypoint: "X1-SYS-A", DestWaypoint: "X1-SYS-B", SourceAsk: 220, DestBid: 1000, SpreadPerUnit: 780, VolumeCap: 60, CappedSpread: 46800},
	}
	if _, ok := FirstDisciplinedLane(lanes); ok {
		t.Fatal("no lane clears the floor; FirstDisciplinedLane must report ok=false")
	}

	// Empty input is also 'none'.
	if _, ok := FirstDisciplinedLane(nil); ok {
		t.Fatal("no lanes at all must report ok=false")
	}
}

// holdWeightFixture pairs one THIN lane (deep per-unit spread, shallow volume cap
// — a light ship's ideal lane) with one DEEP lane (modest per-unit spread, deep
// volume cap — what a heavy hull actually needs to avoid crushing the market).
// Unweighted RankSpreads ranks purely by CappedSpread and picks THINGOOD every
// time, regardless of which hull will fly it — sending a heavy hull onto a
// shallow lane it would crush.
//
//	THINGOOD: source Ask 1000, dest Bid 9000 -> spread/u 8000; volumes 20/500 -> cap 20;  capped = 160000.
//	DEEPGOOD: source Ask  500, dest Bid 1500 -> spread/u 1000; volumes 150/150 -> cap 150; capped = 150000.
func holdWeightFixture() []GoodListing {
	return []GoodListing{
		{Good: "THINGOOD", Waypoint: "X1-SYS-T1", TradeType: "EXPORT", Bid: 950, Ask: 1000, Volume: 20},
		{Good: "THINGOOD", Waypoint: "X1-SYS-T2", TradeType: "IMPORT", Bid: 9000, Ask: 9050, Volume: 500},
		{Good: "DEEPGOOD", Waypoint: "X1-SYS-D1", TradeType: "EXPORT", Bid: 450, Ask: 500, Volume: 150},
		{Good: "DEEPGOOD", Waypoint: "X1-SYS-D2", TradeType: "IMPORT", Bid: 1500, Ask: 1550, Volume: 150},
	}
}

// TestRankSpreads_UnweightedPicksThinLaneRegardlessOfHull pins the PRE-EXISTING
// (unweighted) behavior on the fixture: THINGOOD's deeper CappedSpread
// (160000 > 150000) wins regardless of hull size. RankSpreads itself is
// untouched by hold-fit weighting, so this must keep passing.
func TestRankSpreads_UnweightedPicksThinLaneRegardlessOfHull(t *testing.T) {
	lanes := RankSpreads(holdWeightFixture())
	if len(lanes) != 2 || lanes[0].Good != "THINGOOD" {
		t.Fatalf("expected unweighted RankSpreads to rank THINGOOD first (capped 160000 > DEEPGOOD 150000), got %+v", lanes)
	}
}

// TestRankSpreadsForHold_HeavyHullPrefersDeepLaneOverThinOne proves a 225-cargo
// heavy hull must rank DEEPGOOD above THINGOOD, because THINGOOD's volume cap
// (20) is a small fraction of the hold (225) while DEEPGOOD's (150) covers most
// of it.
//
//	holdFitWeight(20,225)  = 20/225  = 0.0889 -> THINGOOD weighted = 160000*0.0889 = 14222
//	holdFitWeight(150,225) = 150/225 = 0.6667 -> DEEPGOOD weighted = 150000*0.6667 = 100000
//
// DEEPGOOD (100000) > THINGOOD (14222): the ranking flips.
func TestRankSpreadsForHold_HeavyHullPrefersDeepLaneOverThinOne(t *testing.T) {
	lanes := RankSpreadsForHold(holdWeightFixture(), 225)
	if len(lanes) != 2 {
		t.Fatalf("expected both lanes still present (weighting reorders, never filters), got %d: %+v", len(lanes), lanes)
	}
	if lanes[0].Good != "DEEPGOOD" {
		t.Fatalf("expected a 225-hold heavy to rank DEEPGOOD first (thin THINGOOD would crush the vol-20 lane), got %q first: %+v", lanes[0].Good, lanes)
	}
	if lanes[1].Good != "THINGOOD" {
		t.Fatalf("expected THINGOOD to still be present, just ranked second, got %+v", lanes)
	}

	// Weighting must never mutate the lane's real, unpenalized economics - the
	// same "ranking-only adjustment" contract rankLanesByCircuitRate upholds.
	for _, l := range lanes {
		if l.Good == "THINGOOD" && (l.SpreadPerUnit != 8000 || l.CappedSpread != 160000) {
			t.Fatalf("THINGOOD's real economics must be untouched, got spread/u=%d capped=%d", l.SpreadPerUnit, l.CappedSpread)
		}
		if l.Good == "DEEPGOOD" && (l.SpreadPerUnit != 1000 || l.CappedSpread != 150000) {
			t.Fatalf("DEEPGOOD's real economics must be untouched, got spread/u=%d capped=%d", l.SpreadPerUnit, l.CappedSpread)
		}
	}
}

// TestRankSpreadsForHold_LightHullStillPrefersThinLane proves the formula does
// not simply invert the ranking: a light ship whose hold (20) matches THINGOOD's
// volume cap exactly saturates holdFitWeight to 1.0 for BOTH lanes (DEEPGOOD's
// 150-cap also exceeds a 20-hold), so the ORIGINAL CappedSpread order decides -
// THINGOOD (160000) still wins. A hold-mismatched heavy is redirected; a hull
// that genuinely fits the thin lane is not punished for it.
func TestRankSpreadsForHold_LightHullStillPrefersThinLane(t *testing.T) {
	lanes := RankSpreadsForHold(holdWeightFixture(), 20)
	if len(lanes) != 2 || lanes[0].Good != "THINGOOD" {
		t.Fatalf("expected a 20-hold light ship to still prefer THINGOOD (both lanes saturate holdFitWeight to 1.0, real CappedSpread order holds), got %+v", lanes)
	}
}

// TestRankSpreadsForHold_NonPositiveCapacityFallsBackToUnweighted proves an
// unknown/zero/negative hull capacity (e.g. a caller with no ship context)
// degrades to plain RankSpreads ordering rather than dividing by zero or
// producing a meaningless weight.
func TestRankSpreadsForHold_NonPositiveCapacityFallsBackToUnweighted(t *testing.T) {
	for _, capacity := range []int{0, -1} {
		weighted := RankSpreadsForHold(holdWeightFixture(), capacity)
		unweighted := RankSpreads(holdWeightFixture())
		if len(weighted) != len(unweighted) || weighted[0].Good != unweighted[0].Good || weighted[1].Good != unweighted[1].Good {
			t.Fatalf("capacity %d: expected fallback to unweighted RankSpreads order, got %+v want order like %+v", capacity, weighted, unweighted)
		}
	}
}

// TestRankSpreads_InvertedColumnGuard is the inverted-margin-trap sentinel. With
// the CORRECT column semantics, FIREARMS' spread/unit is destBid(900) −
// sourceAsk(300) = 600. An implementation that reads the columns backwards —
// treating the SELL column (Ask) as revenue and the BUY column (Bid) as cost —
// would instead compute destAsk(950) − sourceBid(250) = 700 (~2x-family error)
// and could even reverse the buy/sell direction. This test fails loudly for any
// such inversion.
func TestRankSpreads_InvertedColumnGuard(t *testing.T) {
	lanes := RankSpreads([]GoodListing{
		{Good: "FIREARMS", Waypoint: "X1-SYS-E41", TradeType: "EXPORT", Bid: 250, Ask: 300, Volume: 60},
		{Good: "FIREARMS", Waypoint: "X1-SYS-J56", TradeType: "IMPORT", Bid: 900, Ask: 950, Volume: 20},
	})

	if len(lanes) != 1 {
		t.Fatalf("expected exactly 1 FIREARMS lane, got %d", len(lanes))
	}
	lane := lanes[0]

	const invertedSpread = 700 // destAsk(950) − sourceBid(250): the wrong reading
	if lane.SpreadPerUnit == invertedSpread {
		t.Fatalf("spread/unit is %d — columns are inverted (using Ask as revenue / Bid as cost); must be destBid−sourceAsk", lane.SpreadPerUnit)
	}
	if lane.SpreadPerUnit != 600 {
		t.Fatalf("spread/unit must be destBid(900)−sourceAsk(300)=600, got %d", lane.SpreadPerUnit)
	}
	// Direction must be buy-at-exporter (E41) → sell-at-importer (J56), never reversed.
	if lane.SourceWaypoint != "X1-SYS-E41" || lane.DestWaypoint != "X1-SYS-J56" {
		t.Fatalf("inverted direction: must buy at exporter E41 and sell at importer J56, got source=%q dest=%q", lane.SourceWaypoint, lane.DestWaypoint)
	}
}
