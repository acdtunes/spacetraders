package mvt

import (
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

func listing(wp, good, tradeType string, bid, ask, vol int, age time.Duration, now time.Time) trading.GoodListing {
	return trading.GoodListing{Good: good, Waypoint: wp, TradeType: tradeType, Bid: bid, Ask: ask,
		Supply: "MODERATE", Activity: "STRONG", Volume: vol, ObservedAt: now.Add(-age)}
}

func caps() trading.RankerAgeCaps {
	h := time.Hour
	return trading.RankerAgeCaps{Weak: h, Restricted: h, Growing: h, Strong: h}
}

func TestSystemYield_PerGoodSpreadTimesUnoccupiedDepth(t *testing.T) {
	now := time.Now()
	lanes := []LaneDepth{
		{Listing: listing("X1-A-1", "IRON", "EXPORT", 90, 100, 60, time.Minute, now), BuyPlanned: 10, BuyResidual: 5},
		{Listing: listing("X1-A-2", "IRON", "IMPORT", 150, 160, 40, time.Minute, now), SellPlanned: 0},
		{Listing: listing("X1-A-3", "GOLD", "EXCHANGE", 500, 520, 30, time.Minute, now)},
	}
	credits, units, entry := SystemYield(lanes, caps(), now)
	// IRON: spread 150-100=50; buy depth 60-10-5=45; sell depth 40 → depth 40 → 2000 credits.
	// GOLD: only one waypoint → no lane.
	if credits != 2000 || units != 40 || entry != "X1-A-1" {
		t.Fatalf("got credits=%v units=%d entry=%q, want 2000/40/X1-A-1", credits, units, entry)
	}
}

func TestSystemYield_StaleAndNonPositiveSpreadExcluded(t *testing.T) {
	now := time.Now()
	lanes := []LaneDepth{
		{Listing: listing("X1-A-1", "IRON", "EXPORT", 90, 100, 60, 3*time.Hour, now)}, // stale source
		{Listing: listing("X1-A-2", "IRON", "IMPORT", 150, 160, 40, time.Minute, now)},
		{Listing: listing("X1-A-4", "COPPER", "EXPORT", 90, 100, 60, time.Minute, now)},
		{Listing: listing("X1-A-5", "COPPER", "IMPORT", 100, 110, 40, time.Minute, now)}, // spread 0
	}
	credits, units, _ := SystemYield(lanes, caps(), now)
	if credits != 0 || units != 0 {
		t.Fatalf("got credits=%v units=%d, want 0/0", credits, units)
	}
}

// A good trading at several waypoints must be matched side against side, not summed over
// every source×sink pair: the pairwise sum counts each side's depth once per partner and
// hands Rank depth no hull can lift (review round 1, sp-t5xe6).
func TestSystemYield_MultipleMarketsPerGoodAreMatchedNotMultiplied(t *testing.T) {
	now := time.Now()
	lanes := []LaneDepth{
		{Listing: listing("X1-A-1", "IRON", "EXPORT", 90, 100, 40, time.Minute, now)},
		{Listing: listing("X1-A-2", "IRON", "EXPORT", 90, 100, 40, time.Minute, now)},
		{Listing: listing("X1-A-3", "IRON", "IMPORT", 150, 160, 40, time.Minute, now)},
		{Listing: listing("X1-A-4", "IRON", "IMPORT", 150, 160, 40, time.Minute, now)},
	}
	// Two sources of 40 against two sinks of 40 at spread 50: 80 units, 4000 credits.
	// The pairwise sum would report 4 pairs × 40 = 160 units and 8000 credits.
	credits, units, entry := SystemYield(lanes, caps(), now)
	if credits != 4000 || units != 80 || entry != "X1-A-1" {
		t.Fatalf("got credits=%v units=%d entry=%q, want 4000/80/X1-A-1", credits, units, entry)
	}
}

func TestSystemYield_GoodDepthBoundedByThinnerAggregateSide(t *testing.T) {
	now := time.Now()
	lanes := []LaneDepth{
		{Listing: listing("X1-A-1", "IRON", "EXPORT", 90, 100, 40, time.Minute, now)},
		{Listing: listing("X1-A-2", "IRON", "EXPORT", 90, 100, 40, time.Minute, now)},
		{Listing: listing("X1-A-3", "IRON", "IMPORT", 150, 160, 30, time.Minute, now)},
	}
	// 80 units of source against 30 units of sink → 30 units, 1500 credits.
	credits, units, _ := SystemYield(lanes, caps(), now)
	if credits != 1500 || units != 30 {
		t.Fatalf("got credits=%v units=%d, want 1500/30", credits, units)
	}
}

func TestSystemYield_RichestSpreadIsMatchedFirst(t *testing.T) {
	now := time.Now()
	lanes := []LaneDepth{
		{Listing: listing("X1-A-1", "IRON", "EXPORT", 80, 100, 10, time.Minute, now)},
		{Listing: listing("X1-A-2", "IRON", "EXPORT", 80, 90, 10, time.Minute, now)}, // cheaper ask
		{Listing: listing("X1-A-9", "IRON", "IMPORT", 150, 160, 10, time.Minute, now)},
	}
	// The single sink of 10 goes to the 90-ask source: spread 60 → 600 credits, 10 units.
	credits, units, entry := SystemYield(lanes, caps(), now)
	if credits != 600 || units != 10 || entry != "X1-A-2" {
		t.Fatalf("got credits=%v units=%d entry=%q, want 600/10/X1-A-2", credits, units, entry)
	}
}

// A crossed quote (Ask < Bid) is an sp-en5h7 transposed market_data row — bad data, never a
// bargain — and inflates every spread it sources. The sibling ranker refuses those rows
// (trading.tradeableByGood) and so must this one.
func TestSystemYield_CrossedRowsExcluded(t *testing.T) {
	now := time.Now()
	lanes := []LaneDepth{
		{Listing: listing("X1-A-1", "IRON", "EXPORT", 200, 100, 40, time.Minute, now)}, // crossed source
		{Listing: listing("X1-A-2", "IRON", "IMPORT", 150, 160, 40, time.Minute, now)},
		{Listing: listing("X1-A-4", "COPPER", "EXPORT", 90, 100, 40, time.Minute, now)},
		{Listing: listing("X1-A-5", "COPPER", "IMPORT", 200, 100, 40, time.Minute, now)}, // crossed sink
	}
	credits, units, entry := SystemYield(lanes, caps(), now)
	if credits != 0 || units != 0 || entry != "" {
		t.Fatalf("got credits=%v units=%d entry=%q, want 0/0/\"\"", credits, units, entry)
	}
}

func TestRank_OneHullOneSystem_ReturnsCurrentOrNothing(t *testing.T) {
	hull := Hull{Symbol: "H1", System: "X1-A", CargoCapacity: 100}
	got := Rank(hull, []Candidate{{System: "X1-A", YieldCredits: 5000, DepthUnits: 50}}, Costs{})
	if len(got) != 1 || got[0].System != "X1-A" || got[0].TravelPerUnit != 0 {
		t.Fatalf("got %+v", got)
	}
	if _, ok := BestAlternative(got, "X1-A"); ok {
		t.Fatal("no alternative must exist with a single system")
	}
	if got := Rank(hull, nil, Costs{}); len(got) != 0 {
		t.Fatalf("empty candidates must rank to nothing, got %+v", got)
	}
}

func TestRank_ScoreIsPerUnitOfNextLoadMinusTravel(t *testing.T) {
	hull := Hull{Symbol: "H1", System: "X1-A", CargoCapacity: 100, CreditsPerSec: 2}
	costs := Costs{TollSecondsPerHop: 100, GateFeeFromCurrent: 300}
	cands := []Candidate{
		{System: "X1-A", YieldCredits: 1000, DepthUnits: 100},         // 10/unit, travel 0
		{System: "X1-B", Hops: 2, YieldCredits: 6000, DepthUnits: 50}, // w=120; load=50 → 60/unit; travel (2*100*2 + 2*300)/100 = 10
	}
	got := Rank(hull, cands, costs)
	if got[0].System != "X1-B" || got[0].ExpectedPerUnit != 60 || got[0].TravelPerUnit != 10 || got[0].Score != 50 {
		t.Fatalf("got %+v", got[0])
	}
	if got[1].System != "X1-A" || got[1].Score != 10 {
		t.Fatalf("got %+v", got[1])
	}
}

func TestRank_FleetRateUsedWhenHullHasNone(t *testing.T) {
	hull := Hull{Symbol: "H1", System: "X1-A", CargoCapacity: 100, CreditsPerSec: 0}
	costs := Costs{TollSecondsPerHop: 100, FleetCreditsPerSec: 5}
	got := Rank(hull, []Candidate{{System: "X1-B", Hops: 1, YieldCredits: 10000, DepthUnits: 100}}, costs)
	if got[0].TravelPerUnit != 5 { // 1*100*5/100
		t.Fatalf("travel per unit = %v, want 5", got[0].TravelPerUnit)
	}
}

func TestRank_InTransitClaimIsPenaltyNotExclusion(t *testing.T) {
	hull := Hull{Symbol: "H1", System: "X1-A", CargoCapacity: 100}
	costs := Costs{FleetDrawPerVisit: 1000}
	empty := Candidate{System: "X1-B", Hops: 1, YieldCredits: 5000, DepthUnits: 100}
	occupied := Candidate{System: "X1-C", Hops: 1, YieldCredits: 5000, DepthUnits: 100, InTransit: 1}
	got := Rank(hull, []Candidate{occupied, empty}, costs)
	if got[0].System != "X1-B" || got[1].System != "X1-C" {
		t.Fatalf("empty must beat equally rich occupied by one draw: %+v", got)
	}
	if got[1].ExpectedPerUnit != 40 { // (5000-1000)/100 per unit, load 100
		t.Fatalf("occupied per-unit = %v, want 40", got[1].ExpectedPerUnit)
	}
	heavy := Candidate{System: "X1-D", Hops: 1, YieldCredits: 500, DepthUnits: 100, InTransit: 3}
	got = Rank(hull, []Candidate{heavy}, costs)
	if len(got) != 1 || got[0].ExpectedPerUnit != 0 {
		t.Fatalf("over-penalised system floors at 0, stays listed: %+v", got)
	}
}

func TestRank_NHullsOneSystem_OrderingUnchangedByEqualPenalty(t *testing.T) {
	hull := Hull{Symbol: "H1", System: "X1-A", CargoCapacity: 100}
	for _, n := range []int{1, 2, 10, 400} {
		got := Rank(hull, []Candidate{{System: "X1-A", YieldCredits: 1e6, DepthUnits: 1000, InTransit: n}}, Costs{FleetDrawPerVisit: 100})
		if len(got) != 1 || got[0].System != "X1-A" {
			t.Fatalf("n=%d: %+v", n, got)
		}
	}
}

func TestRank_TiesBreakOnHopsThenSystem(t *testing.T) {
	hull := Hull{Symbol: "H1", System: "X1-A", CargoCapacity: 100}
	got := Rank(hull, []Candidate{
		{System: "X1-C", Hops: 2, YieldCredits: 1000, DepthUnits: 100},
		{System: "X1-B", Hops: 2, YieldCredits: 1000, DepthUnits: 100},
	}, Costs{})
	if got[0].System != "X1-B" {
		t.Fatalf("got %+v", got)
	}
}
