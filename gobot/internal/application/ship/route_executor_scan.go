package ship

import (
	"context"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

func (e *RouteExecutor) scanMarketIfPresent(ctx context.Context, segment *domainNavigation.RouteSegment, ship *domainNavigation.Ship, playerID shared.PlayerID) {
	if e.marketScanner == nil || !segment.ToWaypoint.IsMarketplace() {
		return
	}
	logger := common.LoggerFromContext(ctx)

	// Guard-deferral gate: the caller stamped THIS waypoint because a money-guarded
	// trade live-reads it seconds from now (liveAskForCeiling/liveBidForFloor), so
	// this scan would buy nothing the guard's own read does not already buy. Only
	// the stamped waypoint is skipped — a flight's gate arrivals still scan.
	if deferred, ok := shared.ArrivalScanDeferredFromContext(ctx); ok && deferred == segment.ToWaypoint.Symbol {
		side := shared.ArrivalScanDeferredSideFromContext(ctx)
		metrics.RecordArrivalScanDeferred(playerID.Value(), side)
		logger.Log("INFO", "Arrival market scan deferred to the trade guard", map[string]interface{}{
			"ship_symbol": ship.ShipSymbol(),
			"action":      "scan_deferred_to_guard",
			"waypoint":    segment.ToWaypoint.Symbol,
			"side":        side,
		})
		return
	}

	logger.Log("INFO", "Marketplace detected - scanning market data", map[string]interface{}{
		"ship_symbol": ship.ShipSymbol(),
		"action":      "scan_market",
		"waypoint":    segment.ToWaypoint.Symbol,
	})

	// Recent-scan freshness gate: a trade coordinator stamps a ScanPolicy with
	// MaxScanAge>0, so an arrival at a market scanned within that window reuses
	// the cache instead of re-calling GetMarket. The freshness-scout recovery path
	// stamps NO policy (maxAge 0), so ScanAndSaveMarketFresh always scans and its
	// recovery/decay dataset is untouched.
	var maxScanAge time.Duration
	if policy, ok := shared.ScanPolicyFromContext(ctx); ok {
		maxScanAge = policy.MaxScanAge
	}
	if _, err := e.marketScanner.ScanAndSaveMarketFresh(ctx, uint(playerID.Value()), segment.ToWaypoint.Symbol, maxScanAge); err != nil {
		logger.Log("ERROR", "Market scan failed", map[string]interface{}{
			"ship_symbol": ship.ShipSymbol(),
			"action":      "scan_market",
			"waypoint":    segment.ToWaypoint.Symbol,
			"error":       err.Error(),
		})
	}
}

// scanShipyardIfPresent piggybacks a shipyard-inventory scan on a route arrival. The
// route executor is the ONLY market-scan path a standing multi-market scout tour
// exercises — executeMultiMarketTour delegates its market scan here rather than
// re-scanning in the handler — so the shipyard scan MUST ride this same
// route-arrival hook, or a scout that visits a SHIPYARD-trait waypoint never
// persists a shipyard_inventory row.
//
// The trigger is not marketplace-arrival-only: it also fires when the arrived
// waypoint bears the SHIPYARD trait but carries NO marketplace — a charted-but-
// un-toured shipyard the depth frontier reaches but no MARKET tour ever visits. A
// probe that charts/visits such a system and arrives at its shipyard scans it on
// the way through, decoupling shipyard discovery from the lagging market tour.
//
// No double-scan per visit: the ScoutTourHandler's stationary
// performInitialScan/continuousMarketScanning paths scan a waypoint the executor
// never navigates to, and ReplaceScan is idempotent regardless. The scanner's own
// immutable-SHIPYARD-trait gate is a single cached-waypoint read that no-ops every
// non-shipyard for zero API budget; GetShipyard fires only on a real shipyard.
// Strictly non-fatal — a shipyard failure is logged and the route proceeds,
// mirroring scanMarketIfPresent.
func (e *RouteExecutor) scanShipyardIfPresent(ctx context.Context, segment *domainNavigation.RouteSegment, ship *domainNavigation.Ship, playerID shared.PlayerID) {
	if e.shipyardScanner == nil {
		return
	}
	if !segment.ToWaypoint.IsMarketplace() && !segment.ToWaypoint.HasTrait("SHIPYARD") {
		return
	}
	if err := e.shipyardScanner.ScanAndSaveShipyard(ctx, uint(playerID.Value()), segment.ToWaypoint.Symbol); err != nil {
		common.LoggerFromContext(ctx).Log("ERROR", "Shipyard scan failed (non-fatal to route)", map[string]interface{}{
			"ship_symbol": ship.ShipSymbol(),
			"action":      "scan_shipyard",
			"waypoint":    segment.ToWaypoint.Symbol,
			"error":       err.Error(),
		})
	}
}
