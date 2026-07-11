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
// which system either side is in. Candidates are then RATE-ranked via
// rankLanesByCircuitRate (sp-1wp8) — per-circuit value over estimated circuit
// hours, a gate-crossing circuit paying its jump+cooldown surcharge in the
// denominator — the time cost a raw per-unit spread can't see.
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

	// Hold-vs-absorption weighting (sp-pnx0) and the cross-system circuit-time
	// surcharge are folded into ONE scoring pass inside rankLanesByCircuitRate,
	// not chained as two sequential re-rankings: both are "recompute-from-
	// scratch" rankers that derive their score purely from each lane's own
	// fields, ignoring input order, so composing them as funcB(funcA(lanes))
	// would let funcB silently discard funcA's reordering. Start from the
	// plain trading.RankSpreads order (not RankSpreadsForHold) since hold-fit
	// weighting is applied here via shipCapacity instead. targetDest (sp-xwa1)
	// ranks the operator-directed lane at the in-system baseline — see
	// rankLanesByCircuitRate's doc.
	//
	// The absorption consult (sp-78ai L4) runs as its own step BEFORE
	// rankLanesByCircuitRate, not folded into it: rankLanesByCircuitRate is a
	// pure reorder (every input lane comes back out, just resequenced), while the
	// consult REMOVES lanes outright — mixing the two would make the reorder step
	// silently lossy. READ-ONLY (trade-analyst Q1: "circuits write nothing").
	ranked := trading.RankSpreads(listings)
	consult := h.readAbsorption(ctx, playerID)
	ranked = h.filterShadowedLanes(ctx, ranked, consult, shipCapacity, playerID)
	return rankLanesByCircuitRate(ranked, shipCapacity, targetDest), nil
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

// repositionNeighbors resolves originSystem's directly-gated neighbors for the sp-zhii
// reposition candidate scan, DURABLE-FIRST (sp-1ki5). The persisted era-scoped gate_edges
// adjacency (via the wired gate graph) is origin-INDEPENDENT: it answers even when the origin's
// own jump gate is uncharted or has no ship present — the case where the live GetJumpGate API
// refuses with 4001 and neighborSystems fails open to nil, which is exactly how discovery
// returned ZERO candidates from X1-DP51 while its direct neighbor X1-GQ92 sat 1-min-fresh (canary
// st-wisp-3i8ls). The live jump-gate scan is a fallback only: used when no durable graph is wired
// (most tests) or the durable read itself errors. Each returned edge carries its build state so
// an under-construction gate is rejected with a named reason, never silently pre-flighted into a
// hop-time crash. reason is a non-empty origin-level diagnostic ONLY when the result is empty
// (no-durable-adjacency / gate-inaccessible / no-neighbors), so an empty discovery names WHY.
func (h *RunTradeRouteCoordinatorHandler) repositionNeighbors(ctx context.Context, originSystem string, playerID int) ([]repositionNeighborEdge, string) {
	if h.gateGraph != nil {
		edges, err := h.gateGraph.Connections(ctx, originSystem, playerID)
		if err == nil {
			if len(edges) == 0 {
				return nil, "no-durable-adjacency" // origin has no gated neighbor in the open era
			}
			out := make([]repositionNeighborEdge, 0, len(edges))
			for _, e := range edges {
				out = append(out, repositionNeighborEdge{system: e.ConnectedSystem, underConstruction: e.UnderConstruction})
			}
			return out, ""
		}
		// The durable read failed (a genuine cache miss+stale that fell through to the live gate
		// fetch, itself refused for an uncharted origin gate). Fall back to the live scan — it
		// usually hits the same refusal, but keeps one code path for a charted origin whose cache
		// merely expired, and unifies the nil-graph test harness below.
		if live := h.neighborSystems(ctx, originSystem, playerID); len(live) > 0 {
			return liveNeighborEdges(live), ""
		}
		return nil, "gate-inaccessible: " + sanitizeReasonToken(err.Error())
	}
	if live := h.neighborSystems(ctx, originSystem, playerID); len(live) > 0 {
		return liveNeighborEdges(live), ""
	}
	return nil, "no-neighbors"
}

// liveNeighborEdges adapts the live jump-gate scan's bare system list into neighbor edges. The
// live connections carry no build state, so each is treated as built here; the pre-buy gate
// graph's own under-construction guard still protects the actual jump, and any unbuilt neighbor
// that reaches the pre-flight is caught downstream, not silently flown into.
func liveNeighborEdges(systems []string) []repositionNeighborEdge {
	out := make([]repositionNeighborEdge, 0, len(systems))
	for _, s := range systems {
		out = append(out, repositionNeighborEdge{system: s})
	}
	return out
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
