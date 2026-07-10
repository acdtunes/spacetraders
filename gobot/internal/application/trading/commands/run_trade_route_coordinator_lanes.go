package commands

// run_trade_route_coordinator_lanes.go — lane discovery: cross-market listing collection, jump-gate neighbor scan, and live good re-observation (sp-wads move-only split).

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipQuery "github.com/andrescamacho/spacetraders-go/internal/application/ship/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// scanLanes builds cross-market listings for the system from cache, plus (sp-wlev)
// every system one jump-gate hop away, and ranks them all in a single pass so
// gate-crossing lanes can surface alongside home-system ones. Aggregating BEFORE
// ranking (rather than ranking each system separately) is what lets a good that
// only exports in one system and only imports in another form a lane at all —
// trading.RankSpreads pairs listings purely by good and waypoint, indifferent to
// which system either side is in. Cross-system candidates are then penalized via
// rankLanesWithGatePenalty to reflect the jump+cooldown time cost a raw per-unit
// spread can't see.
//
// Neighbor discovery is fail-open: a system with no jump gate, or a discovery
// query that errors, simply contributes no extra listings — never an aborted
// scan. One hop only (no recursive multi-hop chase): out of scope for sp-wlev.
//
// Multi-daemon lane-dedupe semantics (sp-q1ca, confirmed): scanLanes has NO
// awareness of other concurrently-running trade-route daemons or their active
// circuits — there is no registry of in-flight lanes and no query of what any
// other hull is doing. Two daemons started at the same instant against the same
// system WILL rank identically and both select the same top lane; there is no
// explicit dedupe. Divergence observed in practice (e.g. one hull landing on a
// different lane than another started moments later) is an EMERGENT side effect
// of the shared market cache, not deliberate coordination: after a hull finishes
// a buy/sell batch, the handler issues one extra live GET of that waypoint's
// market and overwrites the cached rows (see cargo_transaction.go's
// refreshMarketData -> MarketScanner.ScanAndSaveMarket -> UpsertMarketData),
// synchronously before the command returns. h.collectSystemListings reads that
// same cache with a plain uncached query, so a daemon that scans shortly AFTER
// another hull has already traded into a lane's destination sees that lane's
// decayed bid and naturally re-ranks it lower — no locking, no CAS, last writer
// wins. Two daemons racing to scan at the SAME moment (before either has traded)
// can still legitimately pick the same lane; only a bid-floor "margin died" stop
// on one of them or a later rescan resolves the collision, not the ranker itself.
func (h *RunTradeRouteCoordinatorHandler) scanLanes(
	ctx context.Context,
	systemSymbol string,
	playerID int,
	shipCapacity int,
	targetDest string,
) ([]trading.ArbitrageLane, error) {
	listings, err := h.collectSystemListings(ctx, systemSymbol, playerID)
	if err != nil {
		return nil, err
	}

	for _, neighbor := range h.neighborSystems(ctx, systemSymbol, playerID) {
		neighborListings, err := h.collectSystemListings(ctx, neighbor, playerID)
		if err != nil {
			continue // fail-open: an unreadable neighbor system just yields fewer lanes
		}
		listings = append(listings, neighborListings...)
	}

	// Ranker age-cap (sp-xwa1): a lane priced from a market observation older than
	// maxListingAge can already have moved, so ranking it chases a spread that no
	// longer exists. An UNDIRECTED auto-scan drops stale rows before ranking so a
	// stale lane can't win selection and execute at moved prices. A DIRECTED --dest
	// scan keeps every row — the operator's lane is re-verified LIVE at execution
	// (staleAskAborts + the per-visit margin re-check), so staleness must never
	// SILENTLY veto it — but logs the retained stale rows so the reliance on live
	// re-verification is visible. Either way the exclusion/retention is put in the
	// MESSAGE TEXT (staleListingSummary), which `container logs` keeps even though it
	// drops the metadata map (sp-149h/sp-iqyq renderer defect).
	logger := common.LoggerFromContext(ctx)
	fresh, stale := partitionListingsByAge(listings, h.clock.Now(), maxListingAge)
	if len(stale) > 0 {
		if targetDest == "" {
			logger.Log("INFO", fmt.Sprintf(
				"Excluded %d stale market listing(s) older than %s from undirected lane ranking: %s",
				len(stale), maxListingAge, staleListingSummary(stale)),
				map[string]interface{}{
					"action":          "stale_listings_excluded",
					"count":           len(stale),
					"max_age_minutes": int(maxListingAge.Minutes()),
				})
			listings = fresh
		} else {
			logger.Log("INFO", fmt.Sprintf(
				"Retained %d stale market listing(s) for directed --dest %q (re-verified live at execution, not vetoed): %s",
				len(stale), targetDest, staleListingSummary(stale)),
				map[string]interface{}{
					"action":          "stale_listings_retained_directed",
					"count":           len(stale),
					"target_dest":     targetDest,
					"max_age_minutes": int(maxListingAge.Minutes()),
				})
			// listings unchanged: the directed path ranks all rows; live re-verify guards it.
		}
	}

	// Hold-vs-absorption weighting (sp-pnx0) and the cross-system jump-gate
	// penalty are folded into ONE scoring pass inside rankLanesWithGatePenalty,
	// not chained as two sequential re-rankings: both are "recompute-from-
	// scratch" rankers that derive their score purely from each lane's own
	// fields, ignoring input order, so composing them as funcB(funcA(lanes))
	// would let funcB silently discard funcA's reordering. Start from the
	// plain trading.RankSpreads order (not RankSpreadsForHold) since hold-fit
	// weighting is applied here via shipCapacity instead. targetDest (sp-xwa1)
	// waives the cross-system penalty for the operator-directed lane only —
	// see rankLanesWithGatePenalty's doc.
	//
	// The absorption consult (sp-78ai L4) runs as its own step BEFORE
	// rankLanesWithGatePenalty, not folded into it: rankLanesWithGatePenalty is a
	// pure reorder (every input lane comes back out, just resequenced), while the
	// consult REMOVES lanes outright — mixing the two would make the reorder step
	// silently lossy. READ-ONLY (trade-analyst Q1: "circuits write nothing").
	ranked := trading.RankSpreads(listings)
	consult := h.readAbsorption(ctx, playerID)
	ranked = h.filterShadowedLanes(ctx, ranked, consult, shipCapacity)
	return rankLanesWithGatePenalty(ranked, shipCapacity, targetDest), nil
}

// collectSystemListings reads every cached market in one system into flat
// GoodListing rows, the shared building block scanLanes aggregates across the
// home system and its jump-gate neighbors before ranking.
func (h *RunTradeRouteCoordinatorHandler) collectSystemListings(
	ctx context.Context,
	systemSymbol string,
	playerID int,
) ([]trading.GoodListing, error) {
	waypoints, err := h.marketRepo.FindAllMarketsInSystem(ctx, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to list markets in %s: %w", systemSymbol, err)
	}

	var listings []trading.GoodListing
	for _, wp := range waypoints {
		mkt, err := h.marketRepo.GetMarketData(ctx, wp, playerID)
		if err != nil || mkt == nil {
			continue // an unreadable market simply doesn't contribute lanes
		}
		for _, g := range mkt.TradeGoods() {
			listings = append(listings, trading.GoodListing{
				Good:      g.Symbol(),
				Waypoint:  mkt.WaypointSymbol(),
				TradeType: string(g.TradeType()),
				Bid:       g.PurchasePrice(), // market BUY column = what we receive selling TO it
				Ask:       g.SellPrice(),     // market SELL column = what we pay buying FROM it
				Supply:    derefString(g.Supply()),
				Activity:  derefString(g.Activity()),
				Volume:    g.TradeVolume(),
				// Stamp each row with the market snapshot's freshness so the ranker can
				// reject stale-priced lanes (sp-xwa1). One timestamp covers all of a
				// waypoint's goods — a market scan observes the whole board at once.
				ObservedAt: mkt.LastUpdated(),
			})
		}
	}
	return listings, nil
}

// neighborSystems returns the systems one jump directly away from systemSymbol's
// own jump gate, via the already-registered GetJumpGateConnectionsQuery. Any
// failure (no gate in the system, an API error, no player context) fails open to
// an empty neighbor set rather than surfacing an error — a multi-system trade
// route degrades to a home-system-only one, it never aborts the scan.
func (h *RunTradeRouteCoordinatorHandler) neighborSystems(ctx context.Context, systemSymbol string, playerID int) []string {
	resp, err := h.mediator.Send(ctx, &shipQuery.GetJumpGateConnectionsQuery{
		SystemSymbol: systemSymbol,
		PlayerID:     &playerID,
	})
	if err != nil {
		return nil
	}
	conn, ok := resp.(*shipQuery.GetJumpGateConnectionsResponse)
	if !ok || conn == nil {
		return nil
	}
	return conn.ConnectedSystems
}

// observeGood re-reads a single good's live cached row at a waypoint so the loop
// can watch the destination bid decay as the importer fills.
func (h *RunTradeRouteCoordinatorHandler) observeGood(
	ctx context.Context,
	waypoint, good string,
	playerID int,
) (*market.TradeGood, error) {
	mkt, err := h.marketRepo.GetMarketData(ctx, waypoint, playerID)
	if err != nil {
		return nil, err
	}
	if mkt == nil {
		return nil, fmt.Errorf("no cached market at %s", waypoint)
	}
	g := mkt.FindGood(good)
	if g == nil {
		return nil, fmt.Errorf("%s no longer trades %s", waypoint, good)
	}
	return g, nil
}
