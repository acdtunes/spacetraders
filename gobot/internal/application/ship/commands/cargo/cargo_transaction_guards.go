package cargo

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/domain/marketscan"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// scanDedupGuardBuyCeiling labels the buy-ceiling guard for the saved-calls counter.
const scanDedupGuardBuyCeiling = "buy_ceiling"

// sellFloorTripped re-reads the LIVE bid before a sell tranche; a crushed bid holds
// the remainder aboard. Armed by MinBidPerUnit>0; fails CLOSED on an unreadable bid.
func (h *CargoTransactionHandler) sellFloorTripped(ctx context.Context, cmd *CargoTransactionCommand, transactionType, waypointSymbol string, unitsRemaining int, reuse *guardReuse) (bool, int) {
	if transactionType != "sell" || cmd.MinBidPerUnit <= 0 {
		return false, 0
	}
	if h.dispatchOnReusedRead(ctx, cmd, waypointSymbol, cmd.MinBidPerUnit, false, reuse) {
		return false, 0
	}
	liveBid, ok := h.liveBidForFloor(ctx, waypointSymbol, cmd.GoodSymbol, cmd.PlayerID, reuse)
	if ok && liveBid >= cmd.MinBidPerUnit {
		return false, 0
	}
	logging.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
		"Sell floor tripped for %s at %s: live bid %d < floor %d/unit (readable=%t) - aborting remaining %d units, held aboard",
		cmd.GoodSymbol, waypointSymbol, liveBid, cmd.MinBidPerUnit, ok, unitsRemaining), map[string]interface{}{
		"action": "sell_floor_abort", "ship_symbol": cmd.ShipSymbol, "good": cmd.GoodSymbol,
		"waypoint": waypointSymbol, "live_bid": liveBid, "floor": cmd.MinBidPerUnit,
		"bid_readable": ok, "units_held": unitsRemaining,
	})
	return true, liveBid
}

// buyCeilingTripped mirrors sellFloorTripped on the ask: a laddered ask leaves the
// remainder unbought. Armed by MaxAskPerUnit>0; fails CLOSED.
func (h *CargoTransactionHandler) buyCeilingTripped(ctx context.Context, cmd *CargoTransactionCommand, transactionType, waypointSymbol string, unitsRemaining int, reuse *guardReuse) (bool, int) {
	if transactionType != "purchase" || cmd.MaxAskPerUnit <= 0 {
		return false, 0
	}
	if h.dispatchOnReusedRead(ctx, cmd, waypointSymbol, cmd.MaxAskPerUnit, true, reuse) {
		return false, 0
	}
	liveAsk, ok := h.liveAskForCeiling(ctx, waypointSymbol, cmd.GoodSymbol, cmd.ShipSymbol, cmd.PlayerID, cmd.ScanDedupBeforeTravel, cmd.ScanDedupAfterArrival, reuse)
	if ok && liveAsk <= cmd.MaxAskPerUnit {
		return false, 0
	}
	logging.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
		"Buy ceiling tripped for %s at %s: live ask %d > ceiling %d/unit (readable=%t) - aborting remaining %d units, left unbought",
		cmd.GoodSymbol, waypointSymbol, liveAsk, cmd.MaxAskPerUnit, ok, unitsRemaining), map[string]interface{}{
		"action": "buy_ceiling_abort", "ship_symbol": cmd.ShipSymbol, "good": cmd.GoodSymbol,
		"waypoint": waypointSymbol, "live_ask": liveAsk, "ceiling": cmd.MaxAskPerUnit,
		"ask_readable": ok, "units_unbought": unitsRemaining,
	})
	return true, liveAsk
}

// liveBidForFloor reads the current per-unit bid for good at waypoint for the
// per-tranche sell floor. When a market refresher is wired it live-
// refreshes first and fails CLOSED (ok=false) if the refresh errors — a tranche
// whose live bid cannot be verified must not be sold. With no refresher wired it
// reads the cached bid (fail-open on the missing port, matching the arb buy
// guard's optional-port contract). ok=false on any inability to read a bid, so
// the caller holds the remainder rather than dump it blind.
func (h *CargoTransactionHandler) liveBidForFloor(ctx context.Context, waypoint, good string, playerID shared.PlayerID, reuse *guardReuse) (int, bool) {
	if h.marketRefresher != nil {
		// LIVE, not budgeted: this guard fails CLOSED, so a bid served from the
		// market-scan budget's cache would either strand the tranche or price it
		// off a stale bid — the exact thing the floor exists to prevent.
		if err := h.marketRefresher.ScanAndSaveMarket(shared.WithLiveScanRequired(ctx), uint(playerID.Value()), waypoint); err != nil {
			return 0, false
		}
		reuse.verified(h.clock.Now())
	}
	mkt, err := h.marketRepo.GetMarketData(ctx, waypoint, playerID.Value())
	if err != nil || mkt == nil {
		return 0, false
	}
	g := mkt.FindGood(good)
	if g == nil {
		return 0, false
	}
	return g.SellPrice(), true // market SELL price = the BID the hull receives selling
}

// liveAskForCeiling reads the current per-unit ask for good at waypoint for the
// per-tranche buy ceiling — the exact mirror of liveBidForFloor. When a
// market refresher is wired it live-refreshes first and fails CLOSED (ok=false) if
// the refresh errors — a tranche whose live ask cannot be verified must not be
// bought. With no refresher wired it reads the cached ask (fail-open on the missing
// port, matching liveBidForFloor). ok=false on any inability to read an ask, so the
// caller holds the remainder (does not buy) rather than purchase blind above the ceiling.
//
// dedupBeforeTravel/dedupAfterArrival are the scan-dedup bracket (zero
// unless the caller captured one for this purchase): when reuseScanDedup
// proves it safe, this skips the scan below and reads the row the same
// visit's arrival already wrote. The ceiling verdict never changes.
func (h *CargoTransactionHandler) liveAskForCeiling(ctx context.Context, waypoint, good, shipSymbol string, playerID shared.PlayerID, dedupBeforeTravel, dedupAfterArrival time.Time, reuse *guardReuse) (int, bool) {
	if h.marketRefresher != nil {
		if !h.reuseScanDedup(ctx, dedupBeforeTravel, dedupAfterArrival, waypoint, shipSymbol, playerID) {
			// LIVE, not budgeted — the exact mirror of liveBidForFloor's exemption.
			if err := h.marketRefresher.ScanAndSaveMarket(shared.WithLiveScanRequired(ctx), uint(playerID.Value()), waypoint); err != nil {
				return 0, false
			}
		}
		// A deduped arrival scan verifies this good's price as squarely as a live
		// one, so both stamp the read the next tranche may reuse.
		reuse.verified(h.clock.Now())
	}
	mkt, err := h.marketRepo.GetMarketData(ctx, waypoint, playerID.Value())
	if err != nil || mkt == nil {
		return 0, false
	}
	g := mkt.FindGood(good)
	if g == nil {
		return 0, false
	}
	return g.PurchasePrice(), true // market PURCHASE price = the ASK the hull pays to buy
}

// reuseScanDedup is the buy-ceiling guard's eligibility+reuse check, the
// cargo-side mirror of the trade-route circuit's own. Any doubt reports
// false, the caller's cue to scan live exactly as before (fail closed).
func (h *CargoTransactionHandler) reuseScanDedup(ctx context.Context, beforeTravel, afterArrival time.Time, waypoint, shipSymbol string, playerID shared.PlayerID) bool {
	if beforeTravel.IsZero() || afterArrival.IsZero() {
		return false
	}
	row, err := h.marketRepo.GetMarketData(ctx, waypoint, playerID.Value())
	if err != nil || row == nil {
		return false
	}
	if !marketscan.DedupEligible(h.clock.Now(), beforeTravel, afterArrival, row.LastUpdated(), marketscan.DedupMaxElapsedSinceArrival) {
		return false
	}
	metrics.RecordScanDedupSaved(playerID.Value(), shipSymbol, scanDedupGuardBuyCeiling)
	logging.LoggerFromContext(ctx).Log("DEBUG", fmt.Sprintf(
		"Scan-dedup: reusing this visit's own arrival scan of %s for the buy-ceiling guard instead of a second live GetMarket call",
		waypoint), map[string]interface{}{
		"action": "scan_dedup_reused", "waypoint": waypoint, "ship_symbol": shipSymbol, "guard": scanDedupGuardBuyCeiling,
	})
	return true
}
