// Package services holds trading application services that are shared across the
// trade coordinators. tour_snapshot assembles the request-carried inputs the
// depth-aware tour planner needs: a per-(waypoint, good) market snapshot plus
// waypoint coordinates for real travel-time pricing.
package services

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// BuildTourSnapshot assembles the market snapshot and waypoint coordinates for
// OptimizeTradeTour across the given systems. It reuses the SAME per-system market
// listing path the lane scanner uses (FindAllMarketsInSystem + GetMarketData,
// mirroring collectSystemListings) so tour prices come from the identical cache the
// circuit trades on, and it reads coordinates from the era-scoped waypoints table
// (WaypointRepository.ListBySystem is already era-filtered, so dead-era coords never
// leak into routing).
//
// Staleness: each GOOD row is excluded when its market's cached snapshot is older than
// that good's ACTIVITY-conditioned freshness cap (sp-t5sh5): caps.For(good.Activity) —
// a WEAK good survives for hours, a STRONG one only ~30 min — the same activity-cap
// discipline as the undirected lane ranker (partitionListingsByAge). The check is
// per-good, not per-market, so a market with mixed activities keeps its WEAK rows while
// dropping its STRONG ones. The caller passes the same config-resolved RankerAgeCaps
// table the lane ranker uses, so the caps are defined once, not redeclared here. A zero
// ObservedAt means "unknown age" and is treated as fresh (matching GoodListing
// semantics).
//
// Failure posture: a market that cannot be read simply contributes no rows (an
// unreadable market is not a lane); a per-system waypoint-repo error degrades that
// system's coordinates to empty (the planner falls back to flat travel with a logged
// warning) rather than aborting the whole snapshot. Only a market-listing error
// (FindAllMarketsInSystem) aborts, because it means the system itself is unreadable.
//
// Coordinates are emitted only for waypoints that actually produced snapshot rows,
// so the coords list aligns with the markets the planner will route over.
func BuildTourSnapshot(
	ctx context.Context,
	marketRepo market.MarketRepository,
	waypointRepo system.WaypointRepository,
	systems []string,
	playerID int,
	now time.Time,
	caps trading.RankerAgeCaps,
) ([]routing.TourGoodSnapshot, []routing.TourWaypoint, error) {
	var snapshot []routing.TourGoodSnapshot
	var waypoints []routing.TourWaypoint
	emittedCoord := map[string]bool{}

	for _, sys := range systems {
		marketWaypoints, err := marketRepo.FindAllMarketsInSystem(ctx, sys, playerID)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to list markets in %s: %w", sys, err)
		}

		snapshotMarkets := map[string]bool{}
		for _, wp := range marketWaypoints {
			mkt, err := marketRepo.GetMarketData(ctx, wp, playerID)
			if err != nil || mkt == nil {
				continue // an unreadable market simply doesn't contribute rows
			}
			observed := mkt.LastUpdated()
			age := now.Sub(observed)
			observedKnown := !observed.IsZero()
			for _, g := range mkt.TradeGoods() {
				activity := derefString(g.Activity())
				if observedKnown && age > caps.For(activity) {
					// Per-good activity-conditioned staleness (sp-t5sh5): drop this good's
					// row against ITS OWN activity's cap, not one flat market-wide
					// threshold, so a market with mixed activities keeps its WEAK rows and
					// drops its STRONG ones. Counted per dropped good ("lane") so a
					// market-rich system silently aging past the cap is visible on
					// tour_lanes_stale_excluded_total instead of just vanishing from the
					// plan. Observation only (RULINGS #4): a no-op when metrics are disabled.
					metrics.RecordTourLanesStaleExcluded(playerID, sys, 1)
					continue
				}
				// An EXPORT market's bid is a low sellback price, never a real import sink —
				// the tour solver admits ANY positive-bid market as a sell destination, which
				// would let it dump goods into an exporter at a giveaway price. Zero the
				// sink-side Bid for EXPORT goods so the solver cannot pick this (waypoint,
				// good) as a sell destination, while keeping the Ask so the market stays a
				// valid BUY source. IMPORT/EXCHANGE (and unknown trade type) keep their bid —
				// the fail-open posture the manufacturing reference filter
				// (sell_market_distributor) uses. This applies the trade_type sink filter at
				// the snapshot boundary, so no trade_type field need thread through the
				// routing proto/solver.
				// sell_price = what we RECEIVE selling; purchase_price = what we PAY
				// buying. These read the other way round until sp-en5h7, correct only
				// against the transposed rows that bug wrote.
				bid, ask := g.SellPrice(), g.PurchasePrice()
				if ask < bid {
					// A market asking less than it bids is impossible data, and this
					// snapshot feeds the tour solver directly — it never passes through
					// RankSpreads, so GoodListing.IsCrossed cannot cover it. Every row
					// written before sp-en5h7 has exactly this shape, and to the solver it
					// looks like free money at a single waypoint. Refuse the good outright
					// (RULINGS #4: fail closed — a dropped good yields no tour leg).
					// Checked on the RAW quote, BEFORE the EXPORT bid is zeroed, or a
					// crossed exporter would slip through as ask<0.
					continue
				}
				if g.TradeType() == market.TradeTypeExport {
					bid = 0
				}
				snapshot = append(snapshot, routing.TourGoodSnapshot{
					Waypoint:    mkt.WaypointSymbol(),
					System:      sys,
					Good:        g.Symbol(),
					Supply:      derefString(g.Supply()),
					Activity:    activity,
					Ask:         ask,
					Bid:         bid,
					TradeVolume: g.TradeVolume(),
					ObservedAt:  observed,
				})
				// Mark the market only once a good row actually survives its activity cap,
				// so a market whose every good aged out contributes no coords either.
				snapshotMarkets[mkt.WaypointSymbol()] = true
			}
		}

		wps, werr := waypointRepo.ListBySystem(ctx, sys)
		if werr != nil {
			continue // coords degrade to empty for this system (planner flat-travel fallback)
		}
		for _, w := range wps {
			if !snapshotMarkets[w.Symbol] || emittedCoord[w.Symbol] {
				continue
			}
			emittedCoord[w.Symbol] = true
			waypoints = append(waypoints, routing.TourWaypoint{
				Symbol: w.Symbol,
				System: w.SystemSymbol,
				X:      int(w.X),
				Y:      int(w.Y),
			})
		}
	}
	return snapshot, waypoints, nil
}

// derefString returns the pointed-to string or "" for a nil pointer — supply and
// activity are optional on a TradeGood.
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
