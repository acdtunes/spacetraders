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

// Ranker backstop: lanes priced off observations past the age at which the fitted drift
// curve saturates are excluded from UNDIRECTED ranking, while a captain-DIRECTED --dest
// lane is NOT vetoed by staleness (it is re-verified live at execution); its stale
// backing is only logged, never silently dropped.

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
	// Measured against the activity's own backstop, so the case survives a retune.
	fresh := trading.GoodListing{Good: "FRESH", Waypoint: "X1-A-1", Activity: "STRONG", ObservedAt: now.Add(-10 * time.Minute)}
	stale := trading.GoodListing{Good: "STALE", Waypoint: "X1-A-2", Activity: "STRONG", ObservedAt: now.Add(-caps.For("STRONG") - time.Minute)}
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
		// Source observed past the backstop; dest defaults to fresh.
		observedAt: map[string]time.Time{"X1-HOME-A1": time.Now().Add(-trading.DefaultRankerAgeCapStrong - time.Hour)},
	}
}

// The backstop is per-activity CONFIGURABLE even though the fit gives every class the
// same saturation horizon: a captain who tightens one class must have that class alone
// drop earlier, and every other class keep its own window.
func TestPartitionListingsByAge_HonoursAPerActivityRetune(t *testing.T) {
	now := time.Now()
	caps := trading.DefaultRankerAgeCaps()
	caps.Strong = 30 * time.Minute
	cases := []struct {
		name       string
		activity   string
		ageMinutes int
		wantStale  bool
	}{
		{"retuned_strong_drops_early", "STRONG", 40, true},
		{"retuned_strong_within_kept", "STRONG", 20, false},
		{"weak_keeps_its_own_window", "WEAK", 400, false},
		{"weak_past_its_own_window", "WEAK", int(caps.For("WEAK").Minutes()) + 1, true},
		{"unknown_activity_uses_restricted", "", int(caps.For("RESTRICTED").Minutes()) + 1, true},
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

// End-to-end through the undirected auto-scan: a WEAK source aged 400m is retained
// inside the backstop, so the lane still forms. Proves the table is wired into scanLanes
// and that the handler's unset zero-value table still ranks on the armed defaults.
func TestScanLanes_Undirected_RetainsWeakListingPastFlatCap(t *testing.T) {
	marketRepo := &msMarketRepo{
		waypointsBySystem: map[string][]string{"X1-HOME": {"X1-HOME-A1", "X1-HOME-B1"}},
		goods: map[string]msGood{
			"X1-HOME-A1": {symbol: "WIDGET", bid: 50, ask: 100, volume: 60, tradeType: market.TradeTypeExport, activity: "WEAK"},
			"X1-HOME-B1": {symbol: "WIDGET", bid: 2000, ask: 2050, volume: 60, tradeType: market.TradeTypeImport, activity: "WEAK"},
		},
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
