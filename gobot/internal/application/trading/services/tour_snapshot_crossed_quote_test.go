package services

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// These tests pin BuildTourSnapshot's crossed-quote guard: a good quoting an ask BELOW its bid
// is refused outright rather than emitted as a tour leg.
//
// The guard is the fail-closed protection for every market_data row written before sp-en5h7.
// Those rows hold their two prices transposed, so reading one with the CORRECTED mapping yields
// exactly this shape — and an inverted row does not look broken to the solver, it looks like a
// spectacular arbitrage (cheap ask, rich bid) at a single waypoint, which it would chase with
// real money.
//
// This snapshot feeds routing.OptimizeTradeTour directly and never passes through
// trading.RankSpreads, so trading.GoodListing.IsCrossed cannot cover it: the guard inside
// BuildTourSnapshot is a SECOND, independent implementation of the same predicate and needs its
// own tests. Every fixture below is deliberately FRESH (observed == now) so a dropped row is the
// guard's doing and never the activity staleness cap's.

// crossedGood builds a good in the pre-sp-en5h7 legacy shape: ask BELOW bid. mustGood maps its
// `ask` argument to purchase_price and its `bid` argument to sell_price, so passing the larger
// value as the bid reproduces a transposed row exactly as the corrected readers see it.
func crossedGood(t *testing.T, sym string, tt market.TradeType) market.TradeGood {
	t.Helper()
	return mustGood(t, sym, 3000, 1000, 40, "MODERATE", "STRONG", tt) // bid 3000 > ask 1000
}

// A crossed good is dropped while a healthy good in the SAME market is kept — the guard is
// per-good, not per-market, so one bad row cannot take a whole market's tradeable goods with it.
func TestBuildTourSnapshot_DropsCrossedQuote_KeepsHealthyGoodInSameMarket(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)

	repo := &snapFakeMarketRepo{
		order: map[string][]string{"X1-CQ": {"X1-CQ-M1"}},
		markets: map[string]*market.Market{
			"X1-CQ-M1": mustMarket(t, "X1-CQ-M1", now,
				crossedGood(t, "LEGACY_ROW", market.TradeTypeImport),
				mustGood(t, "HEALTHY", 900, 1000, 40, "MODERATE", "STRONG", market.TradeTypeImport)),
		},
	}
	wps := &snapFakeWaypointRepo{byS: map[string][]*shared.Waypoint{
		"X1-CQ": {mustWaypoint(t, "X1-CQ-M1", 1, 1)},
	}}

	snapshot, _, err := BuildTourSnapshot(context.Background(), repo, wps,
		[]string{"X1-CQ"}, 1, now, trading.DefaultRankerAgeCaps())
	if err != nil {
		t.Fatalf("BuildTourSnapshot: %v", err)
	}

	if len(snapshot) != 1 {
		t.Fatalf("expected only the HEALTHY good (the crossed LEGACY_ROW refused), got %d rows: %+v",
			len(snapshot), snapshot)
	}
	if snapshot[0].Good != "HEALTHY" {
		t.Fatalf("the crossed LEGACY_ROW must be the dropped row, got %+v", snapshot[0])
	}
	// The survivor's own prices must still map through uncorrupted.
	if snapshot[0].Ask != 1000 || snapshot[0].Bid != 900 {
		t.Fatalf("HEALTHY mapping wrong: want ask 1000 / bid 900, got %+v", snapshot[0])
	}
}

// A crossed EXPORT good is refused, and it is refused on the RAW quote — BEFORE the EXPORT
// sink-bid zeroing. This pins the ORDER of the two rules, not just their presence: zeroing first
// would rewrite the bid to 0, making `ask < bid` false for every crossed exporter, and the whole
// class would slip through as a valid buy source at a transposed ask.
//
// The market's only good is crossed, so it must also contribute no waypoint coordinates — the
// coords list has to stay aligned with the markets the planner will actually route over. The
// waypoint IS served by the fake repo, so an empty coords result is the guard's doing and not an
// absent fixture.
func TestBuildTourSnapshot_DropsCrossedExport_BeforeZeroingItsBid(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)

	repo := &snapFakeMarketRepo{
		order: map[string][]string{"X1-CQX": {"X1-CQX-E1"}},
		markets: map[string]*market.Market{
			"X1-CQX-E1": mustMarket(t, "X1-CQX-E1", now,
				crossedGood(t, "LEGACY_EXPORT", market.TradeTypeExport)),
		},
	}
	wps := &snapFakeWaypointRepo{byS: map[string][]*shared.Waypoint{
		"X1-CQX": {mustWaypoint(t, "X1-CQX-E1", 2, 2)},
	}}

	snapshot, waypoints, err := BuildTourSnapshot(context.Background(), repo, wps,
		[]string{"X1-CQX"}, 1, now, trading.DefaultRankerAgeCaps())
	if err != nil {
		t.Fatalf("BuildTourSnapshot: %v", err)
	}

	if len(snapshot) != 0 {
		t.Fatalf("a crossed EXPORT good must be refused on its raw quote, before the bid is zeroed; got %+v",
			snapshot)
	}
	if len(waypoints) != 0 {
		t.Fatalf("a market whose only good was refused must contribute no coords, got %+v", waypoints)
	}
}

// A zero-rake quote (ask == bid) is NOT crossed and is kept. The predicate is `ask < bid`, not
// `ask <= bid`: an EXCHANGE charging exactly what it pays is unusual but not impossible data, and
// it yields no positive spread anyway, so refusing it would discard a legitimate market. This
// pins the boundary so the guard cannot be quietly widened.
func TestBuildTourSnapshot_KeepsZeroRakeQuote_AskEqualsBid(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)

	repo := &snapFakeMarketRepo{
		order: map[string][]string{"X1-CQZ": {"X1-CQZ-X1"}},
		markets: map[string]*market.Market{
			"X1-CQZ-X1": mustMarket(t, "X1-CQZ-X1", now,
				mustGood(t, "ZERO_RAKE", 1500, 1500, 40, "ABUNDANT", "STRONG", market.TradeTypeExchange)),
		},
	}
	wps := &snapFakeWaypointRepo{byS: map[string][]*shared.Waypoint{
		"X1-CQZ": {mustWaypoint(t, "X1-CQZ-X1", 3, 3)},
	}}

	snapshot, _, err := BuildTourSnapshot(context.Background(), repo, wps,
		[]string{"X1-CQZ"}, 1, now, trading.DefaultRankerAgeCaps())
	if err != nil {
		t.Fatalf("BuildTourSnapshot: %v", err)
	}

	if len(snapshot) != 1 {
		t.Fatalf("a zero-rake ask == bid quote is not crossed and must be kept, got %d rows: %+v",
			len(snapshot), snapshot)
	}
	if snapshot[0].Ask != 1500 || snapshot[0].Bid != 1500 {
		t.Fatalf("zero-rake mapping wrong: want ask 1500 / bid 1500, got %+v", snapshot[0])
	}
}
