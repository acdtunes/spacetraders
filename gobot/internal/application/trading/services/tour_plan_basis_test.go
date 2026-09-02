package services

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// tourPriceTolerancePct, mirrored: these tests bound the executor's guards, and the
// commands package that owns the constant cannot be imported from here.
const guardTolerancePct = 15

func guardCeiling(basis int) int { return basis + basis*guardTolerancePct/100 }
func guardFloor(basis int) int   { return basis - basis*guardTolerancePct/100 }

// snapshotWithRaw runs one aged market through BuildTourSnapshot so the raw and
// discounted quotes come from the real builder rather than a hand-written row.
func snapshotWithRaw(t *testing.T, activity string, age time.Duration) routing.TourGoodSnapshot {
	t.Helper()
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	repo := &snapFakeMarketRepo{
		order: map[string][]string{"X1-S": {"X1-S-A"}},
		markets: map[string]*market.Market{
			"X1-S-A": mustMarket(t, "X1-S-A", now.Add(-age),
				mustGood(t, "G", 1000, 2000, 20, "MODERATE", activity, market.TradeTypeImport)),
		},
	}
	wps := &snapFakeWaypointRepo{byS: map[string][]*shared.Waypoint{"X1-S": {mustWaypoint(t, "X1-S-A", 1, 1)}}}
	rows, _, err := BuildTourSnapshot(context.Background(), repo, wps, []string{"X1-S"}, 1, now,
		trading.DefaultRankerAgeCaps(), trading.DefaultStalenessDiscount())
	if err != nil {
		t.Fatalf("BuildTourSnapshot: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected one snapshot row, got %d", len(rows))
	}
	return rows[0]
}

// The snapshot must carry BOTH quotes: the discounted one the solver ranks on and the
// undiscounted one the money guards will be banded around.
func TestBuildTourSnapshot_KeepsTheUndiscountedQuoteBesideTheDiscountedOne(t *testing.T) {
	row := snapshotWithRaw(t, "STRONG", 6*time.Hour)

	if row.RawAsk != 2000 || row.RawBid != 1000 {
		t.Fatalf("the raw quote must be the market's own, got ask=%d bid=%d", row.RawAsk, row.RawBid)
	}
	if row.Ask <= row.RawAsk || row.Bid >= row.RawBid {
		t.Fatalf("a 6h STRONG row must still be discounted for ranking, got %+v", row)
	}
}

// A discount that charges nothing must leave the two quotes identical, which is what
// makes every downstream guard bit-identical when the model contributes nothing.
func TestBuildTourSnapshot_FreshRowHasRawEqualToDiscounted(t *testing.T) {
	row := snapshotWithRaw(t, "STRONG", 0)

	if row.Ask != row.RawAsk || row.Bid != row.RawBid {
		t.Fatalf("an unaged row must have raw == discounted, got %+v", row)
	}
}

func planWithTrade(waypoint, good string, trade routing.TourTrade) *routing.TourPlan {
	return &routing.TourPlan{Legs: []routing.TourLeg{{
		Waypoint: waypoint, System: "X1-S", Trades: []routing.TourTrade{trade},
	}}}
}

// The buy ceiling is banded over the plan basis, so a marked-UP ask raises it. The
// annotated basis must take that back out, and the resulting ceiling must sit at or
// below the one the discounted basis produced (RULINGS #4).
func TestAnnotateRawPlanBasis_BuyCeilingNeverRisesAboveTheDiscountedOne(t *testing.T) {
	row := snapshotWithRaw(t, "STRONG", 6*time.Hour)
	plan := planWithTrade(row.Waypoint, row.Good,
		routing.TourTrade{Good: row.Good, Units: 20, ExpectedUnitPrice: row.Ask, IsBuy: true})

	AnnotateRawPlanBasis(plan, []routing.TourGoodSnapshot{row})

	trade := plan.Legs[0].Trades[0]
	if trade.RawUnitPrice != row.RawAsk {
		t.Fatalf("a tranche-0 buy must reprice to the raw ask %d, got %d", row.RawAsk, trade.RawUnitPrice)
	}
	if guardCeiling(trade.GuardBasis()) >= guardCeiling(row.Ask) {
		t.Fatalf("the ceiling must fall: raw basis %d ceiling %d vs discounted %d ceiling %d",
			trade.GuardBasis(), guardCeiling(trade.GuardBasis()), row.Ask, guardCeiling(row.Ask))
	}
	if trade.ExpectedUnitPrice != row.Ask {
		t.Fatalf("the ranked projection must be left alone, got %d", trade.ExpectedUnitPrice)
	}
}

// The sell floor is banded under the plan basis, so a marked-DOWN bid lowers it.
func TestAnnotateRawPlanBasis_SellFloorNeverFallsBelowTheDiscountedOne(t *testing.T) {
	row := snapshotWithRaw(t, "STRONG", 6*time.Hour)
	plan := planWithTrade(row.Waypoint, row.Good,
		routing.TourTrade{Good: row.Good, Units: 20, ExpectedUnitPrice: row.Bid})

	AnnotateRawPlanBasis(plan, []routing.TourGoodSnapshot{row})

	trade := plan.Legs[0].Trades[0]
	if trade.RawUnitPrice != row.RawBid {
		t.Fatalf("a tranche-0 sell must reprice to the raw bid %d, got %d", row.RawBid, trade.RawUnitPrice)
	}
	if guardFloor(trade.GuardBasis()) <= guardFloor(row.Bid) {
		t.Fatalf("the floor must rise: raw basis %d floor %d vs discounted %d floor %d",
			trade.GuardBasis(), guardFloor(trade.GuardBasis()), row.Bid, guardFloor(row.Bid))
	}
}

// A deeper tranche is priced quote×factor^k, so the haircut it carries scales with it.
// Rescaling by the raw/discounted ratio must recover the whole charge, not just the
// tranche-0 part of it.
func TestAnnotateRawPlanBasis_RepricesDeepTranchesInProportion(t *testing.T) {
	row := snapshotWithRaw(t, "STRONG", 6*time.Hour)
	deep := row.Ask * 3 / 2
	plan := planWithTrade(row.Waypoint, row.Good,
		routing.TourTrade{Good: row.Good, Units: 20, ExpectedUnitPrice: deep, IsBuy: true})

	AnnotateRawPlanBasis(plan, []routing.TourGoodSnapshot{row})

	got := plan.Legs[0].Trades[0].RawUnitPrice
	want := deep * row.RawAsk / row.Ask
	if got != want {
		t.Fatalf("a deep tranche must reprice by the same ratio: want %d, got %d", want, got)
	}
	if charged := deep - got; charged <= row.Ask-row.RawAsk {
		t.Fatalf("the deep tranche's recovered haircut %d must exceed tranche 0's %d",
			charged, row.Ask-row.RawAsk)
	}
}

// A deposit's price is a synthetic contract-savings value, not a market quote, and the
// executor never bands it. Repricing it against a co-located market row would be
// meaningless, so it is left alone.
func TestAnnotateRawPlanBasis_LeavesDepositsAndUnknownRowsAlone(t *testing.T) {
	row := snapshotWithRaw(t, "STRONG", 6*time.Hour)
	plan := &routing.TourPlan{Legs: []routing.TourLeg{{
		Waypoint: row.Waypoint,
		Trades: []routing.TourTrade{
			{Good: row.Good, Units: 5, ExpectedUnitPrice: 900, IsDeposit: true},
			{Good: "NOT_IN_SNAPSHOT", Units: 5, ExpectedUnitPrice: 900, IsBuy: true},
		},
	}}}

	AnnotateRawPlanBasis(plan, []routing.TourGoodSnapshot{row})

	for _, trade := range plan.Legs[0].Trades {
		if trade.RawUnitPrice != 0 {
			t.Fatalf("%s must keep no raw basis, got %d", trade.Good, trade.RawUnitPrice)
		}
		if trade.GuardBasis() != trade.ExpectedUnitPrice {
			t.Fatalf("%s must fall back to the ranked basis, got %d", trade.Good, trade.GuardBasis())
		}
	}
}

// THE MONOTONICITY PROOF (RULINGS #4). Swept across the whole fitted drift range and a
// price range spanning the live board, the reconstructed basis must move the buy ceiling
// only down and the sell floor only up — so every live price the old bound refused is
// still refused, and a discount charging nothing changes neither bound at all.
func TestUndiscountedTranchePrice_NeverLoosensEitherBound(t *testing.T) {
	for _, quote := range []int{1, 2, 7, 58, 137, 900, 2900, 3600, 12000, 48000} {
		for driftBps := 0; driftBps <= 824; driftBps += 4 {
			adjAsk := quote + quote*driftBps/10000
			adjBid := quote - quote*driftBps/10000
			for _, factorPct := range []int{100, 103, 112, 87, 61} {
				projBuy := adjAsk * factorPct / 100
				projSell := adjBid * factorPct / 100

				rawBuy := undiscountedTranchePrice(projBuy, adjAsk, quote, true)
				if rawBuy != 0 && guardCeiling(rawBuy) > guardCeiling(projBuy) {
					t.Fatalf("buy ceiling loosened: quote=%d bps=%d proj=%d raw=%d (%d > %d)",
						quote, driftBps, projBuy, rawBuy, guardCeiling(rawBuy), guardCeiling(projBuy))
				}
				rawSell := undiscountedTranchePrice(projSell, adjBid, quote, false)
				if rawSell != 0 && guardFloor(rawSell) < guardFloor(projSell) {
					t.Fatalf("sell floor loosened: quote=%d bps=%d proj=%d raw=%d (%d < %d)",
						quote, driftBps, projSell, rawSell, guardFloor(rawSell), guardFloor(projSell))
				}
				if driftBps != 0 {
					continue
				}
				if rawBuy != projBuy || rawSell != projSell {
					t.Fatalf("a zero charge must move nothing: quote=%d buy %d->%d sell %d->%d",
						quote, projBuy, rawBuy, projSell, rawSell)
				}
			}
		}
	}
}

// A quote that is not a haircut — a raw ask above the marked-up one, a raw bid below the
// marked-down one — cannot be evidence for moving a bound outward, so it yields no basis
// and the guard binds on the ranked projection exactly as before.
func TestUndiscountedTranchePrice_RefusesToWidenOnAnInvertedQuote(t *testing.T) {
	if got := undiscountedTranchePrice(1000, 900, 1100, true); got != 0 {
		t.Fatalf("a raw ask above the discounted ask must yield no basis, got %d", got)
	}
	if got := undiscountedTranchePrice(1000, 1100, 900, false); got != 0 {
		t.Fatalf("a raw bid below the discounted bid must yield no basis, got %d", got)
	}
	if got := undiscountedTranchePrice(1000, 0, 900, true); got != 0 {
		t.Fatalf("an unpriceable quote must yield no basis, got %d", got)
	}
}
