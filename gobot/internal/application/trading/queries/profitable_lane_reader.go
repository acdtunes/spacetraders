// Package queries holds read-only trading read-models. ProfitableLaneReader (sp-4ewi) counts the
// profitable, feasible arbitrage lanes visible in a player's trading grounds — the "unserved trade
// demand" signal the fleet autosizer's heavy-demand provider sizes to.
//
// It is a READ-ONLY twin of the trade-route coordinator's lane scan: it reads the SAME persisted
// market cache and ranks with the SAME pure domain primitives (trading.RankSpreads +
// ArbitrageLane.ClearsFloor), but it never touches the coordinator's state or flies anything. This
// is deliberately a parallel reader rather than an extraction of the coordinator's scanLanes: the
// ranking is already a pure exported domain function (trading.RankSpreads), so the seam needs no
// trading-package refactor and cannot perturb any running circuit (RULINGS #4 — a read that
// influences a spend must not have a write side effect).
package queries

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// laneMarketReader is the narrow slice of market.MarketRepository the lane count consumes: the
// per-system waypoint list and each waypoint's cached market. Declared here so the reader depends on
// behaviour, not the whole repository (the *MarketRepositoryGORM satisfies it structurally).
type laneMarketReader interface {
	FindAllMarketsInSystem(ctx context.Context, systemSymbol string, playerID int) ([]string, error)
	GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error)
}

// ProfitableLaneReader counts the profitable, feasible arbitrage lanes across a set of systems from
// the persisted market cache.
type ProfitableLaneReader struct {
	markets laneMarketReader
}

// NewProfitableLaneReader wires the reader over its market read source.
func NewProfitableLaneReader(markets laneMarketReader) *ProfitableLaneReader {
	return &ProfitableLaneReader{markets: markets}
}

// CountProfitableLanes returns how many distinct profitable, feasible arbitrage lanes the given
// systems currently rank — the count of ranked lanes whose per-unit spread clears the bid-floor
// discipline (trading.ArbitrageLane.ClearsFloor / MinBidMargin), the exact tradeability predicate
// the trade-route executor enforces per visit. Lanes are ranked PER SYSTEM (within-system pairs
// only) and deduped by (good, source, dest), a deliberately conservative scope on the money path:
// it never counts an unreachable cross-system pairing, so it can only UNDER-count demand (fail
// toward not-buying), never over-count it (RULINGS #4).
//
// readable is FALSE only on a genuine market read FAILURE (a system's market-list read errors) — the
// heavy provider then fails closed (no buy against an unreadable surface). An empty cache or a system
// with no floor-clearing lane is a readable ZERO (genuinely no unserved demand), not a failure. An
// individual unreadable waypoint market is skipped (fail-open at the finest grain, mirroring the
// coordinator's collectSystemListings — a single stale/missing market is normal, not a signal loss).
func (r *ProfitableLaneReader) CountProfitableLanes(ctx context.Context, playerID int, systems []string) (int, bool, error) {
	seen := map[string]struct{}{}
	for _, system := range dedupeStrings(systems) {
		listings, err := r.collectSystemListings(ctx, system, playerID)
		if err != nil {
			// A genuine market-list read failure fails the WHOLE count closed — never a silent
			// under-count feeding a spend decision (mirrors the coordinator's scanLanes, which
			// returns the error rather than ranking a partial surface).
			return 0, false, err
		}
		for _, lane := range trading.RankSpreads(listings) {
			if !lane.ClearsFloor() {
				continue
			}
			seen[lane.Good+"|"+lane.SourceWaypoint+"|"+lane.DestWaypoint] = struct{}{}
		}
	}
	return len(seen), true, nil
}

// collectSystemListings reads every cached market in one system into flat trading.GoodListing rows —
// the read-only mirror of the trade-route coordinator's own collectSystemListings, in
// market-perspective column semantics (Bid = the market's PurchasePrice column, Ask = its SellPrice
// column). A system's market-list read error propagates (fail-closed); an individual unreadable
// waypoint market simply contributes no listings.
func (r *ProfitableLaneReader) collectSystemListings(ctx context.Context, systemSymbol string, playerID int) ([]trading.GoodListing, error) {
	waypoints, err := r.markets.FindAllMarketsInSystem(ctx, systemSymbol, playerID)
	if err != nil {
		return nil, err
	}
	var listings []trading.GoodListing
	for _, wp := range waypoints {
		mkt, err := r.markets.GetMarketData(ctx, wp, playerID)
		if err != nil || mkt == nil {
			continue // an unreadable/missing market simply doesn't contribute lanes
		}
		for _, g := range mkt.TradeGoods() {
			listings = append(listings, trading.GoodListing{
				Good:       g.Symbol(),
				Waypoint:   mkt.WaypointSymbol(),
				TradeType:  string(g.TradeType()),
				Bid:        g.PurchasePrice(), // market BUY column = received selling TO it
				Ask:        g.SellPrice(),     // market SELL column = paid buying FROM it
				Supply:     derefString(g.Supply()),
				Activity:   derefString(g.Activity()),
				Volume:     g.TradeVolume(),
				ObservedAt: mkt.LastUpdated(),
			})
		}
	}
	return listings, nil
}

// dedupeStrings returns the distinct, non-empty entries of s, preserving first-seen order — so a
// system a player has several hulls in is scanned once.
func dedupeStrings(s []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(s))
	for _, v := range s {
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// derefString returns the pointed-to string, or "" for a nil pointer (the market Supply/Activity
// fields are optional).
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
