package services

import "github.com/andrescamacho/spacetraders-go/internal/domain/routing"

// AnnotateRawPlanBasis fills every market trade's RawUnitPrice: the plan's own projected
// tranche price with BuildTourSnapshot's staleness haircut divided back out.
//
// The solver never sees a raw quote. It ladders the DISCOUNTED quote through its impact
// curve, so a tranche's projected price is quote×factor^k for a factor that does not
// depend on the quote — which makes the haircut recoverable by the ratio raw/discounted
// alone, with no need to know k, the curve, or the age and activity that priced it.
//
// It rounds AWAY from the trade on both sides — down for a buy, up for a sell — so the
// reconstruction can only ever tighten a guard past the exact figure, never short of it.
// Deposits are skipped (a synthetic bid is not a market quote), as is any trade whose
// (waypoint, good) row is not in the snapshot or whose discounted quote is non-positive;
// those keep RawUnitPrice zero and bind on ExpectedUnitPrice exactly as before.
//
// A discount that charged nothing gives raw == discounted, so every RawUnitPrice equals
// the ExpectedUnitPrice it came from and no guard moves at all.
func AnnotateRawPlanBasis(plan *routing.TourPlan, snapshot []routing.TourGoodSnapshot) {
	if plan == nil || len(snapshot) == 0 {
		return
	}
	rows := make(map[tourQuoteKey]routing.TourGoodSnapshot, len(snapshot))
	for _, row := range snapshot {
		rows[tourQuoteKey{row.Waypoint, row.Good}] = row
	}
	for li := range plan.Legs {
		leg := &plan.Legs[li]
		for ti := range leg.Trades {
			trade := &leg.Trades[ti]
			if trade.IsDeposit || trade.ExpectedUnitPrice <= 0 {
				continue
			}
			row, ok := rows[tourQuoteKey{leg.Waypoint, trade.Good}]
			if !ok {
				continue
			}
			discounted, raw := row.Bid, row.RawBid
			if trade.IsBuy {
				discounted, raw = row.Ask, row.RawAsk
			}
			trade.RawUnitPrice = undiscountedTranchePrice(trade.ExpectedUnitPrice, discounted, raw, trade.IsBuy)
		}
	}
}

// tourQuoteKey indexes the plan-time snapshot the way a leg addresses it.
type tourQuoteKey struct {
	waypoint string
	good     string
}

// undiscountedTranchePrice rescales one projected tranche price from the discounted quote
// it was laddered off to the raw quote beside it. It answers 0 — "unknown, bind on the
// projection" — for any input that cannot support the ratio, including a raw quote that
// somehow exceeds a marked-up ask or falls under a marked-down bid, which is not a
// haircut and must never be used to move a guard outward.
func undiscountedTranchePrice(projected, discounted, raw int, isBuy bool) int {
	if projected <= 0 || discounted <= 0 || raw <= 0 {
		return 0
	}
	if isBuy && raw > discounted {
		return 0
	}
	if !isBuy && raw < discounted {
		return 0
	}
	scaled := projected * raw
	if isBuy {
		return scaled / discounted
	}
	return (scaled + discounted - 1) / discounted
}
