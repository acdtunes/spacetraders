package cargo

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/strategies"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

// volumeClampOutcome reports what a 4604 trade-volume reconcile learned and did. known is
// true only for a TRUSTWORTHY per-transaction limit, which the caller then adopts for every
// REMAINING tranche — what drains the hold instead of stranding its tail.
type volumeClampOutcome struct {
	known  bool
	limit  int
	units  int
	result *strategies.TransactionResult
	err    error
}

// retryClampedToMarketTradeVolume reconciles a tranche the API rejected as over the market's
// per-transaction trade volume and re-sends it clamped to the limit the rejection states,
// exactly once. That rejection is the only authoritative statement about the market's depth
// in the exchange — a cached trade_volume can outlive a live drop by the better part of an
// hour — so the retry is min(planned, limit) and NEVER larger. It refuses to act, leaving
// the API's verdict standing, on an unreadable payload and on a stated limit at or above
// what we asked for: a "limit" that is not limiting is self-contradictory.
//
// It reads the MARKET's depth, not the hull's, so it is side-agnostic — a capped purchase
// tranche is clamped exactly like a sell, unlike the 4219 hold reconcile beside it. The
// sell floor and buy ceiling are deliberately not re-checked here: the guard cleared
// this tranche at this market moments ago and the retry is strictly smaller, so re-reading
// the live price would spend a scan re-answering a question just answered. Every LATER chunk
// re-enters the caller's loop and faces the guard again, as it would have had the cached
// limit been right.
func (h *CargoTransactionHandler) retryClampedToMarketTradeVolume(
	ctx context.Context,
	cmd *CargoTransactionCommand,
	token string,
	planned int,
	rejection error,
) volumeClampOutcome {
	limit, ok := domainPorts.MarketTradeVolumeLimit(rejection, cmd.GoodSymbol)
	if !ok || limit >= planned {
		return volumeClampOutcome{}
	}

	logging.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
		"%s of %d %s on %s rejected: the market takes at most %d units per transaction - clamping to %d and chunking the remainder (cached depth was %d ahead)",
		h.strategy.GetTransactionType(), planned, cmd.GoodSymbol, cmd.ShipSymbol, limit, limit, planned-limit), map[string]interface{}{
		"action": "market_volume_clamp", "ship_symbol": cmd.ShipSymbol, "good": cmd.GoodSymbol,
		"planned_units": planned, "market_trade_volume": limit, "clamped_units": limit,
	})

	result, err := h.strategy.Execute(ctx, cmd.ShipSymbol, cmd.GoodSymbol, limit, token)
	if err == nil && result == nil {
		// A strategy reporting neither outcome is unusable: fail the retry rather than
		// letting the caller account a nil trade.
		err = fmt.Errorf("clamped %s of %d %s returned no result", h.strategy.GetTransactionType(), limit, cmd.GoodSymbol)
	}
	return volumeClampOutcome{known: true, limit: limit, units: limit, result: result, err: err}
}
