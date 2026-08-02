package cargo

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/strategies"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

// sellShortfallOutcome reports what a 4219 cargo-shortfall reconcile learned and did.
//
// known is true only when the rejection yielded a TRUSTWORTHY server count for this
// good; the caller heals its cached count from onHand whenever it is set, including
// when no retry could be attempted. units is the clamped tranche actually re-sent (0
// when none was), and result/err are that retry's outcome.
type sellShortfallOutcome struct {
	known  bool
	onHand int
	units  int
	result *strategies.TransactionResult
	err    error
}

// retrySellClampedToServerCargo reconciles a sell tranche the API rejected as a cargo
// shortfall (4219) and re-sends it clamped to the hull's true depth, exactly once.
//
// The rejection is the only authoritative statement about the hold in the exchange:
// our planned size is precisely what it disproved. So the retry is min(planned,
// onHand) and NEVER anything larger — this must not become a path to asking for units
// we do not hold. Concretely it refuses to act, leaving the API's original verdict
// standing, when:
//   - the transaction is not a sell (a purchase 4219 says nothing about our hold);
//   - the payload is not a readable 4219 naming this good (serverCargoUnits fails closed);
//   - the reported count is at or above what we asked for — a "shortfall" that is not
//     short is self-contradictory, so the count is distrusted entirely rather than
//     used to re-request the same size the server just refused.
//
// A count of zero IS trusted (the hull genuinely holds none of this good): no retry is
// possible, but the caller still heals its cache and then fails the transaction.
//
// The per-tranche sell floor is deliberately not re-checked: it already cleared this
// tranche at this market moments ago, and the retry is strictly smaller, so re-reading
// the live bid would spend an extra market scan to re-answer a question just answered.
func (h *CargoTransactionHandler) retrySellClampedToServerCargo(
	ctx context.Context,
	cmd *CargoTransactionCommand,
	token string,
	planned int,
	rejection error,
) sellShortfallOutcome {
	if h.strategy.GetTransactionType() != "sell" {
		return sellShortfallOutcome{}
	}
	onHand, ok := domainPorts.CargoShortfallUnits(rejection, cmd.GoodSymbol)
	if !ok {
		return sellShortfallOutcome{}
	}
	if onHand >= planned {
		return sellShortfallOutcome{}
	}

	logger := logging.LoggerFromContext(ctx)
	if onHand <= 0 {
		logger.Log("WARNING", fmt.Sprintf(
			"Sell of %d %s on %s rejected: server reports 0 units aboard - cached hold was phantom, resyncing (no retry possible)",
			planned, cmd.GoodSymbol, cmd.ShipSymbol), map[string]interface{}{
			"action": "cargo_shortfall_phantom", "ship_symbol": cmd.ShipSymbol, "good": cmd.GoodSymbol,
			"planned_units": planned, "server_units": onHand,
		})
		return sellShortfallOutcome{known: true, onHand: onHand}
	}

	logger.Log("WARNING", fmt.Sprintf(
		"Sell of %d %s on %s rejected: server reports only %d units aboard - clamping to %d and retrying once (cache was %d ahead)",
		planned, cmd.GoodSymbol, cmd.ShipSymbol, onHand, onHand, planned-onHand), map[string]interface{}{
		"action": "cargo_shortfall_clamp", "ship_symbol": cmd.ShipSymbol, "good": cmd.GoodSymbol,
		"planned_units": planned, "server_units": onHand, "clamped_units": onHand,
	})

	result, err := h.strategy.Execute(ctx, cmd.ShipSymbol, cmd.GoodSymbol, onHand, token)
	if err == nil && result == nil {
		// A strategy that reports neither outcome is unusable; treat it as a failed
		// retry so the caller fails the transaction rather than accounting a nil sale.
		err = fmt.Errorf("clamped sell of %d %s returned no result", onHand, cmd.GoodSymbol)
	}
	return sellShortfallOutcome{known: true, onHand: onHand, units: onHand, result: result, err: err}
}
