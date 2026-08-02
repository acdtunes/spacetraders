package commands

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// Ranker age-cap: lanes priced off market observations older than
// marketDataAgeFloor are excluded from UNDIRECTED ranking — a stale lane can't win
// selection and then execute at prices that already moved — while a captain-
// DIRECTED --dest lane is NOT vetoed by staleness (it is re-verified live at
// execution by staleAskAborts + the per-visit margin re-check); its stale backing
// is only logged so the reliance on live re-verification is visible, never silent.

// hasLogContaining reports whether any captured log entry's MESSAGE TEXT contains
// every one of substrs. The age-cap and cargo-park logs deliberately carry their
// payload in the message (not the metadata map the `container logs` renderer drops),
// so these assertions grep the text exactly as the captain would.
func hasLogContaining(logger *laneLogCapturingLogger, substrs ...string) bool {
	for _, e := range logger.entries {
		all := true
		for _, s := range substrs {
			if !strings.Contains(e.message, s) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

func TestPartitionListingsByAge_KeepsFreshDropsStaleTreatsZeroAsFresh(t *testing.T) {
	now := time.Now()
	caps := trading.DefaultRankerAgeCaps()
	// STRONG activity → 30-min cap: -10m is within it (fresh), -2h is past it (stale).
	fresh := trading.GoodListing{Good: "FRESH", Waypoint: "X1-A-1", Activity: "STRONG", ObservedAt: now.Add(-10 * time.Minute)}
	stale := trading.GoodListing{Good: "STALE", Waypoint: "X1-A-2", Activity: "STRONG", ObservedAt: now.Add(-2 * time.Hour)}
	unknown := trading.GoodListing{Good: "UNKNOWN", Waypoint: "X1-A-3"} // zero ObservedAt

	gotFresh, gotStale := partitionListingsByAge([]trading.GoodListing{fresh, stale, unknown}, now, caps)

	if len(gotStale) != 1 || gotStale[0].Good != "STALE" {
		t.Fatalf("expected only STALE partitioned as stale, got %+v", gotStale)
	}
	// A listing within the cap AND one with a zero (unknown) timestamp are both fresh:
	// unknown age is not evidence of staleness, so callers that never stamp a time rank
	// unchanged.
	if len(gotFresh) != 2 {
		t.Fatalf("expected FRESH + UNKNOWN(zero-time) treated as fresh, got %+v", gotFresh)
	}
	joined := gotFresh[0].Good + "," + gotFresh[1].Good
	if !strings.Contains(joined, "FRESH") || !strings.Contains(joined, "UNKNOWN") {
		t.Fatalf("expected fresh set to contain FRESH and UNKNOWN, got %s", joined)
	}
}

func TestPartitionListingsByAge_ExactlyActivityCapIsFresh(t *testing.T) {
	now := time.Now()
	caps := trading.DefaultRankerAgeCaps()
	// Exactly the activity cap old: now.Sub(ObservedAt) == caps.For(activity), and the
	// gate is a STRICT greater-than, so the boundary listing stays fresh.
	edge := trading.GoodListing{Good: "EDGE", Waypoint: "X1-A-1", Activity: "STRONG", ObservedAt: now.Add(-caps.For("STRONG"))}
	fresh, stale := partitionListingsByAge([]trading.GoodListing{edge}, now, caps)
	if len(stale) != 0 || len(fresh) != 1 {
		t.Fatalf("a listing exactly at its activity cap must be fresh (strict >), got fresh=%d stale=%d", len(fresh), len(stale))
	}
}

// staleAgeCapFixture builds a single home-system WIDGET lane (export A1 -> import
// B1) whose SOURCE market is stale. Undirected ranking must drop the stale source
// (so no lane forms); a directed --dest scan must retain it.
func staleAgeCapFixture() *msMarketRepo {
	return &msMarketRepo{
		waypointsBySystem: map[string][]string{"X1-HOME": {"X1-HOME-A1", "X1-HOME-B1"}},
		goods: map[string]msGood{
			"X1-HOME-A1": {symbol: "WIDGET", bid: 50, ask: 100, volume: 60, tradeType: market.TradeTypeExport},
			"X1-HOME-B1": {symbol: "WIDGET", bid: 2000, ask: 2050, volume: 60, tradeType: market.TradeTypeImport},
		},
		// Source observed 2h ago (> the 75-min cap); dest defaults to fresh.
		observedAt: map[string]time.Time{"X1-HOME-A1": time.Now().Add(-2 * time.Hour)},
	}
}

// The freshness cap is ACTIVITY-CONDITIONED: each listing is dropped
// against its own activity's window, not one flat 75-minute threshold. A WEAK market
// stays eligible for hours (a quote the retired flat cap would have dropped is now
// retained); a STRONG one only ~30 min; an unknown/missing activity falls to the
// RESTRICTED (conservative middle) cap.
func TestPartitionListingsByAge_ActivityConditionedCaps(t *testing.T) {
	now := time.Now()
	caps := trading.DefaultRankerAgeCaps()
	cases := []struct {
		name       string
		activity   string
		ageMinutes int
		wantStale  bool
	}{
		// WEAK (480m/8h): a 400m quote the retired flat 75m cap would have DROPPED is now
		// retained; past 480m it drops.
		{"weak_within_cap_retained", "WEAK", 400, false},
		{"weak_past_cap_dropped", "WEAK", 500, true},
		// STRONG (30m): tightest — a 40m quote drops, 20m survives.
		{"strong_past_cap_dropped", "STRONG", 40, true},
		{"strong_within_cap_retained", "STRONG", 20, false},
		// GROWING (60m).
		{"growing_within_cap_retained", "GROWING", 50, false},
		{"growing_past_cap_dropped", "GROWING", 70, true},
		// RESTRICTED (180m) explicitly, and the unknown/missing-activity fallback that must
		// resolve to the SAME RESTRICTED cap (conservative middle).
		{"restricted_within_cap_retained", "RESTRICTED", 120, false},
		{"restricted_past_cap_dropped", "RESTRICTED", 200, true},
		{"unknown_activity_uses_restricted_within", "", 120, false},
		{"unknown_activity_uses_restricted_past", "", 200, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			listing := trading.GoodListing{
				Good: "G", Waypoint: "X1-A-1", Activity: tc.activity,
				ObservedAt: now.Add(-time.Duration(tc.ageMinutes) * time.Minute),
			}
			fresh, stale := partitionListingsByAge([]trading.GoodListing{listing}, now, caps)
			if tc.wantStale && (len(stale) != 1 || len(fresh) != 0) {
				t.Fatalf("activity=%q age=%dm: want STALE, got fresh=%d stale=%d", tc.activity, tc.ageMinutes, len(fresh), len(stale))
			}
			if !tc.wantStale && (len(fresh) != 1 || len(stale) != 0) {
				t.Fatalf("activity=%q age=%dm: want FRESH, got fresh=%d stale=%d", tc.activity, tc.ageMinutes, len(fresh), len(stale))
			}
		})
	}
}

// End-to-end through the undirected auto-scan (the driving path): a WEAK source aged
// 400m — well past the retired flat 75-min cap that would have dropped it — is RETAINED
// under WEAK's 480-min cap, so the lane still forms. Proves the activity table is wired
// into scanLanes (h.rankerAgeCaps -> partitionListingsByAge), and the handler's unset
// zero-value table still ranks on the fitted armed defaults.
func TestScanLanes_Undirected_RetainsWeakListingPastFlatCap(t *testing.T) {
	marketRepo := &msMarketRepo{
		waypointsBySystem: map[string][]string{"X1-HOME": {"X1-HOME-A1", "X1-HOME-B1"}},
		goods: map[string]msGood{
			"X1-HOME-A1": {symbol: "WIDGET", bid: 50, ask: 100, volume: 60, tradeType: market.TradeTypeExport, activity: "WEAK"},
			"X1-HOME-B1": {symbol: "WIDGET", bid: 2000, ask: 2050, volume: 60, tradeType: market.TradeTypeImport, activity: "WEAK"},
		},
		// Source observed 400m ago: stale under the retired flat 75m, fresh under WEAK 480m.
		observedAt: map[string]time.Time{"X1-HOME-A1": time.Now().Add(-400 * time.Minute)},
	}
	handler := NewRunTradeRouteCoordinatorHandler(&msMediator{}, nil, marketRepo, nil, nil, nil)

	lanes, err := handler.scanLanes(context.Background(), "X1-HOME", 1, 0, "")
	if err != nil {
		t.Fatalf("scanLanes error: %v", err)
	}
	if len(lanes) != 1 || lanes[0].Good != "WIDGET" || lanes[0].SourceWaypoint != "X1-HOME-A1" {
		t.Fatalf("expected the WEAK 400m-old source retained so the WIDGET lane forms, got %+v", lanes)
	}
}

func TestScanLanes_Undirected_ExcludesStaleListingAndLogsIt(t *testing.T) {
	handler := NewRunTradeRouteCoordinatorHandler(&msMediator{}, nil, staleAgeCapFixture(), nil, nil, nil)
	logger := &laneLogCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	lanes, err := handler.scanLanes(ctx, "X1-HOME", 1, 0, "")
	if err != nil {
		t.Fatalf("scanLanes error: %v", err)
	}

	// The only WIDGET lane needs the stale source listing; once it is excluded the
	// pair can't form, so undirected ranking yields nothing rather than a stale lane.
	if len(lanes) != 0 {
		t.Fatalf("expected the stale-sourced lane excluded from undirected ranking, got %+v", lanes)
	}
	if !hasLogContaining(logger, "Excluded", "X1-HOME-A1") {
		t.Fatalf("expected an exclusion log naming the stale source in its text, got %+v", logger.entries)
	}
}

func TestScanLanes_Directed_RetainsStaleListingNotVetoed(t *testing.T) {
	handler := NewRunTradeRouteCoordinatorHandler(&msMediator{}, nil, staleAgeCapFixture(), nil, nil, nil)
	logger := &laneLogCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	// Directed at the dest waypoint: the stale source must NOT silently veto the
	// operator's lane — the buy path re-verifies live — so the lane still ranks.
	lanes, err := handler.scanLanes(ctx, "X1-HOME", 1, 0, "X1-HOME-B1")
	if err != nil {
		t.Fatalf("scanLanes error: %v", err)
	}

	if len(lanes) != 1 || lanes[0].Good != "WIDGET" || lanes[0].DestWaypoint != "X1-HOME-B1" {
		t.Fatalf("expected the directed WIDGET lane retained despite stale data, got %+v", lanes)
	}
	if !hasLogContaining(logger, "Retained", "directed") {
		t.Fatalf("expected a 'retained stale for directed' log, got %+v", logger.entries)
	}
}
