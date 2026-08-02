package cargo

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sellFloorTripped re-reads the LIVE bid before a sell tranche; a crushed bid holds
// the remainder aboard. Armed by MinBidPerUnit>0; fails CLOSED on an unreadable bid.
func (h *CargoTransactionHandler) sellFloorTripped(ctx context.Context, cmd *CargoTransactionCommand, transactionType, waypointSymbol string, unitsRemaining int) (bool, int) {
	if transactionType != "sell" || cmd.MinBidPerUnit <= 0 {
		return false, 0
	}
	liveBid, ok := h.liveBidForFloor(ctx, waypointSymbol, cmd.GoodSymbol, cmd.PlayerID)
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
func (h *CargoTransactionHandler) buyCeilingTripped(ctx context.Context, cmd *CargoTransactionCommand, transactionType, waypointSymbol string, unitsRemaining int) (bool, int) {
	if transactionType != "purchase" || cmd.MaxAskPerUnit <= 0 {
		return false, 0
	}
	liveAsk, ok := h.liveAskForCeiling(ctx, waypointSymbol, cmd.GoodSymbol, cmd.PlayerID)
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
func (h *CargoTransactionHandler) liveBidForFloor(ctx context.Context, waypoint, good string, playerID shared.PlayerID) (int, bool) {
	if h.marketRefresher != nil {
		// LIVE, not budgeted: this guard fails CLOSED, so a bid served from the
		// market-scan budget's cache would either strand the tranche or price it
		// off a stale bid — the exact thing the floor exists to prevent.
		if err := h.marketRefresher.ScanAndSaveMarket(shared.WithLiveScanRequired(ctx), uint(playerID.Value()), waypoint); err != nil {
			return 0, false
		}
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
func (h *CargoTransactionHandler) liveAskForCeiling(ctx context.Context, waypoint, good string, playerID shared.PlayerID) (int, bool) {
	if h.marketRefresher != nil {
		// LIVE, not budgeted — the exact mirror of liveBidForFloor's exemption.
		if err := h.marketRefresher.ScanAndSaveMarket(shared.WithLiveScanRequired(ctx), uint(playerID.Value()), waypoint); err != nil {
			return 0, false
		}
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
