package commands

import (
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// stalenessLane builds one in-system lane priced from two quotes of the given age.
func stalenessLane(good string, ask, bid, volume int, activity string, age time.Duration, now time.Time) trading.ArbitrageLane {
	observed := now.Add(-age)
	return trading.ArbitrageLane{
		Good: good, SourceWaypoint: "X1-S-" + good + "A", DestWaypoint: "X1-S-" + good + "B",
		SourceAsk: ask, DestBid: bid, SpreadPerUnit: bid - ask,
		VolumeCap: volume, CappedSpread: (bid - ask) * volume,
		SourceActivity: activity, SourceObservedAt: observed,
		DestActivity: activity, DestObservedAt: observed,
	}
}

// THE PAYOFF. A slightly richer lane priced from four-hour-old STRONG quotes loses to a
// slightly thinner one priced from fresh quotes, because the older lane is charged for the
// drift its prices have most likely already taken. Both orderings are asserted — the
// undiscounted one AND the discounted one — so a regression to face-value ranking fails
// here rather than silently trading on quotes that moved hours ago.
func TestRankLanesByCircuitRate_PrefersTheFresherLaneOverASlightlyRicherStaleOne(t *testing.T) {
	now := time.Now()
	stale := stalenessLane("STALE", 10000, 12200, 100, "STRONG", 4*time.Hour, now)
	fresh := stalenessLane("FRESH", 10000, 12000, 100, "STRONG", 0, now)
	lanes := []trading.ArbitrageLane{stale, fresh}

	plain := rankLanesByCircuitRate(lanes, 100, "", laneImpactModel{}, 0)
	if plain[0].Good != "STALE" {
		t.Fatalf("fixture is inert: undiscounted, the richer STALE lane must rank first; got %s", plain[0].Good)
	}

	model := laneImpactModel{staleness: trading.DefaultStalenessDiscount(), rankedAt: now}
	ranked := rankLanesByCircuitRate(lanes, 100, "", model, 0)
	if ranked[0].Good != "FRESH" {
		t.Fatalf("a 4h-old STRONG lane must lose its 200-credit edge to the drift it is charged; got %s first", ranked[0].Good)
	}
	if len(ranked) != 2 {
		t.Fatalf("the discount must reorder, never remove: got %d lanes", len(ranked))
	}
}

// A haircut is not a veto. However deep the charge, every lane handed in comes back out —
// removing one is the backstop's job, applied as its own step before ranking.
func TestRankLanesByCircuitRate_TheDiscountNeverDropsALane(t *testing.T) {
	now := time.Now()
	lanes := []trading.ArbitrageLane{
		stalenessLane("THIN", 100000, 100010, 100, "STRONG", 12*time.Hour, now),
		stalenessLane("FAT", 100, 5000, 100, "WEAK", 0, now),
	}
	model := laneImpactModel{staleness: trading.DefaultStalenessDiscount(), rankedAt: now}

	ranked := rankLanesByCircuitRate(lanes, 100, "", model, 0)
	if len(ranked) != 2 {
		t.Fatalf("expected both lanes retained, got %d", len(ranked))
	}
	if got := laneCircuitValue(ranked[1], 100, model); got < 0 {
		t.Fatalf("a lane the haircut exceeds must score at 0, never below: got %v", got)
	}
}

// A model with no ranking clock is INERT, so every caller that supplies none — the whole
// existing test surface — scores exactly the snapshot spread.
func TestLaneImpactModel_WithoutARankingClockChargesNoStaleness(t *testing.T) {
	now := time.Now()
	lane := stalenessLane("G", 1000, 3000, 50, "STRONG", 8*time.Hour, now)
	model := laneImpactModel{staleness: trading.DefaultStalenessDiscount()}

	if got := model.rankingSpreadPerUnit(lane, 50); got != float64(lane.SpreadPerUnit) {
		t.Fatalf("an unclocked model must score the snapshot spread %d, got %v", lane.SpreadPerUnit, got)
	}
}

// The ranker's own view is discounted, but the lane's REAL economics are not: a downstream
// reader of SourceAsk/DestBid/SpreadPerUnit must see the quoted numbers, or a ranking
// adjustment would silently become a sizing and abort-tolerance change.
func TestRankLanesByCircuitRate_LeavesTheLanesRealEconomicsUntouched(t *testing.T) {
	now := time.Now()
	lane := stalenessLane("G", 1000, 3000, 50, "STRONG", 6*time.Hour, now)
	model := laneImpactModel{staleness: trading.DefaultStalenessDiscount(), rankedAt: now}

	ranked := rankLanesByCircuitRate([]trading.ArbitrageLane{lane}, 50, "", model, 0)
	got := ranked[0]
	if got.SourceAsk != 1000 || got.DestBid != 3000 || got.SpreadPerUnit != 2000 || got.CappedSpread != 100000 {
		t.Fatalf("the discount must not rewrite the lane's quoted economics, got %+v", got)
	}
	if scored := model.rankingSpreadPerUnit(lane, 50); scored >= float64(lane.SpreadPerUnit) {
		t.Fatalf("the SCORE must still be discounted below the snapshot spread, got %v", scored)
	}
}

// RankSpreads carries each end's age and activity onto the lane. Without it the ranker has
// nothing to charge and the discount is silently inert on the real path.
func TestRankSpreads_CarriesEachEndsAgeAndActivityOntoTheLane(t *testing.T) {
	now := time.Now()
	src := trading.GoodListing{Good: "G", Waypoint: "X1-A", TradeType: "EXPORT", Bid: 90, Ask: 100,
		Volume: 10, Activity: "WEAK", ObservedAt: now.Add(-30 * time.Minute)}
	dst := trading.GoodListing{Good: "G", Waypoint: "X1-B", TradeType: "IMPORT", Bid: 3000, Ask: 3100,
		Volume: 10, Activity: "STRONG", ObservedAt: now.Add(-90 * time.Minute)}

	lanes := trading.RankSpreads([]trading.GoodListing{src, dst})
	if len(lanes) != 1 {
		t.Fatalf("expected one lane, got %+v", lanes)
	}
	got := lanes[0]
	if got.SourceActivity != "WEAK" || !got.SourceObservedAt.Equal(src.ObservedAt) {
		t.Fatalf("source age/activity lost: %+v", got)
	}
	if got.DestActivity != "STRONG" || !got.DestObservedAt.Equal(dst.ObservedAt) {
		t.Fatalf("dest age/activity lost: %+v", got)
	}
}
