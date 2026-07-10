// Package services holds trading application services that are shared across the
// trade coordinators. tour_snapshot assembles the request-carried inputs the
// depth-aware tour planner needs (sp-1ek0): a per-(waypoint, good) market snapshot
// plus waypoint coordinates for real travel-time pricing.
package services

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// BuildTourSnapshot assembles the market snapshot and waypoint coordinates for
// OptimizeTradeTour across the given systems. It reuses the SAME per-system market
// listing path the lane scanner uses (FindAllMarketsInSystem + GetMarketData,
// mirroring collectSystemListings) so tour prices come from the identical cache the
// circuit trades on, and it reads coordinates from the era-scoped waypoints table
// (WaypointRepository.ListBySystem is already era-filtered per sp-vapw, so dead-era
// coords never leak into routing).
//
// Staleness: a market row whose cached snapshot is older than maxAge (relative to
// now) is excluded — the same age-cap discipline as lane ranking. The caller passes
// the trading package's maxListingAge so the 75-minute value is defined once, not
// redeclared here. A zero ObservedAt means "unknown age" and is treated as fresh
// (matching GoodListing semantics).
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
	maxAge time.Duration,
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
			if !observed.IsZero() && now.Sub(observed) > maxAge {
				continue // stale — same age-cap as lane ranking
			}
			for _, g := range mkt.TradeGoods() {
				// sp-9mkf (Bug 3): an EXPORT market's bid is a low sellback price, never a
				// real import sink — the tour solver admits ANY positive-bid market as a sell
				// destination, so it dumped 80 LAB_INSTRUMENTS into the exporter C37 at
				// 2,347/u. Zero the sink-side Bid for EXPORT goods so the solver cannot pick
				// this (waypoint, good) as a sell destination, while keeping the Ask so the
				// market stays a valid BUY source. IMPORT/EXCHANGE (and unknown trade type)
				// keep their bid — the fail-open posture the manufacturing reference filter
				// (sell_market_distributor) uses. This applies the trade_type sink filter at
				// the snapshot boundary, so no trade_type field need thread through the
				// routing proto/solver.
				bid := g.PurchasePrice() // market BUY column = what we receive selling
				if g.TradeType() == market.TradeTypeExport {
					bid = 0
				}
				snapshot = append(snapshot, routing.TourGoodSnapshot{
					Waypoint:    mkt.WaypointSymbol(),
					System:      sys,
					Good:        g.Symbol(),
					Supply:      derefString(g.Supply()),
					Activity:    derefString(g.Activity()),
					Ask:         g.SellPrice(), // market SELL column = what we pay buying
					Bid:         bid,
					TradeVolume: g.TradeVolume(),
					ObservedAt:  observed,
				})
			}
			snapshotMarkets[mkt.WaypointSymbol()] = true
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
