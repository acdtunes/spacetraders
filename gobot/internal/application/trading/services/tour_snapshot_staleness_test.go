package services

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// stalenessSnapshot builds one market of one good at the given activity and age and runs it
// through the snapshot the solver is handed.
func stalenessSnapshot(t *testing.T, activity string, age time.Duration, discount trading.StalenessDiscount) []struct{ Ask, Bid int } {
	t.Helper()
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	repo := &snapFakeMarketRepo{
		order: map[string][]string{"X1-S": {"X1-S-A"}},
		markets: map[string]*market.Market{
			"X1-S-A": mustMarket(t, "X1-S-A", now.Add(-age),
				mustGood(t, "G", 1000, 2000, 20, "MODERATE", activity, market.TradeTypeImport)),
		},
	}
	wps := &snapFakeWaypointRepo{byS: map[string][]*shared.Waypoint{"X1-S": {mustWaypoint(t, "X1-S-A", 1, 1)}}}

	rows, _, err := BuildTourSnapshot(context.Background(), repo, wps, []string{"X1-S"}, 1, now,
		trading.DefaultRankerAgeCaps(), discount)
	if err != nil {
		t.Fatalf("BuildTourSnapshot: %v", err)
	}
	out := make([]struct{ Ask, Bid int }, len(rows))
	for i, r := range rows {
		out[i] = struct{ Ask, Bid int }{r.Ask, r.Bid}
	}
	return out
}

// The solver has no age model of its own, so the snapshot must hand it prices already moved
// toward the adverse side: what we would PAY marked up, what we would RECEIVE marked down.
// This is what makes a long scan rotation safe rather than merely cheap.
func TestBuildTourSnapshot_ChargesAStaleRowsAgeIntoItsPrices(t *testing.T) {
	fresh := stalenessSnapshot(t, "STRONG", 0, trading.DefaultStalenessDiscount())
	stale := stalenessSnapshot(t, "STRONG", 4*time.Hour, trading.DefaultStalenessDiscount())

	if len(fresh) != 1 || len(stale) != 1 {
		t.Fatalf("expected one row each, got fresh=%d stale=%d", len(fresh), len(stale))
	}
	if fresh[0].Ask != 2000 || fresh[0].Bid != 1000 {
		t.Fatalf("a freshly observed row must reach the solver at face value, got %+v", fresh[0])
	}
	if stale[0].Ask <= fresh[0].Ask {
		t.Fatalf("a 4h-old ask must be marked UP (we would pay more), got %d vs %d", stale[0].Ask, fresh[0].Ask)
	}
	if stale[0].Bid >= fresh[0].Bid {
		t.Fatalf("a 4h-old bid must be marked DOWN (we would receive less), got %d vs %d", stale[0].Bid, fresh[0].Bid)
	}
}

// The charge is activity-conditioned at the same age, and it only ever makes the solver
// more conservative — the adjusted spread can never widen (RULINGS #4).
func TestBuildTourSnapshot_ChargesByActivityAndOnlyEverNarrowsTheSpread(t *testing.T) {
	const age = 4 * time.Hour
	weak := stalenessSnapshot(t, "WEAK", age, trading.DefaultStalenessDiscount())[0]
	strong := stalenessSnapshot(t, "STRONG", age, trading.DefaultStalenessDiscount())[0]

	if strong.Ask <= weak.Ask || strong.Bid >= weak.Bid {
		t.Fatalf("at the same age STRONG must be charged harder than WEAK, got strong=%+v weak=%+v", strong, weak)
	}
	for _, row := range []struct{ Ask, Bid int }{weak, strong} {
		if row.Ask < 2000 || row.Bid > 1000 {
			t.Fatalf("the adjustment must never flatter a quote, got %+v", row)
		}
	}
}

// The kill switch reaches the tour path too: disabled, the solver sees the raw board.
func TestBuildTourSnapshot_DisabledDiscountLeavesThePricesAlone(t *testing.T) {
	row := stalenessSnapshot(t, "STRONG", 8*time.Hour, trading.StalenessDiscount{Disabled: true})[0]
	if row.Ask != 2000 || row.Bid != 1000 {
		t.Fatalf("a disabled discount must pass the quote through untouched, got %+v", row)
	}
}

// PRICED, NOT BANNED — but still backstopped. A row inside the horizon survives at a
// haircut where the retired fitted STRONG cap would have refused it outright; a row past
// the horizon is still dropped.
func TestBuildTourSnapshot_KeepsStaleRowsInsideTheBackstopAndDropsThemPastIt(t *testing.T) {
	inside := stalenessSnapshot(t, "STRONG", 4*time.Hour, trading.DefaultStalenessDiscount())
	if len(inside) != 1 {
		t.Fatalf("a 4h STRONG row inside the backstop must be RANKED at a haircut, not dropped; got %d rows", len(inside))
	}
	past := stalenessSnapshot(t, "STRONG", trading.StalenessDiscountHorizon+time.Hour,
		trading.DefaultStalenessDiscount())
	if len(past) != 0 {
		t.Fatalf("a row past the saturation horizon must be dropped, got %+v", past)
	}
}
