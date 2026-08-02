package navigation

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	ledgerCommands "github.com/andrescamacho/spacetraders-go/internal/application/ledger/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// recordJumpFee writes the jump gate fee to the financial ledger.
//
// Without this the fee is the ledger's one credit-DECREASING blind spot: the
// API charges it, the adapter parses it into JumpResult.TotalPrice, and nothing
// consumes the value. That is the single failure direction a fail-closed
// money guard cannot tolerate — an unrecorded spend makes the ledger report MORE
// credits than exist, which is the direction that authorises an unaffordable buy.
// (Staleness alone is safe; it only ever under-reports.)
//
// AuthoritativeBalance is the whole point. The jump response carries the agent's
// post-fee credits in-band (data.agent.credits, mandatory in the API schema), so
// the row RE-ANCHORS the running chain to API truth. A row that only appended
// `amount` would make the chain longer without making it any more trustworthy.
// When the API omits the agent block the pointer is nil and the ledger falls back
// to reconstruction — still recorded, just not re-anchored.
//
// Best-effort by design: a ledger failure is logged, never returned. The hull has
// already moved and the credits have already gone; failing the jump here would
// trade a bookkeeping gap for a lost hull position.
func (h *JumpShipHandler) recordJumpFee(
	ctx context.Context,
	cmd *JumpShipCommand,
	playerID shared.PlayerID,
	agentSymbol string,
	originSystem string,
	jumpResult *ports.JumpResult,
) {
	logger := common.LoggerFromContext(ctx)

	// A zero fee is not a transaction: ledger.Validate rejects amount == 0
	// (same guard refuel_ship applies to a free top-up). Nothing was spent, so
	// there is nothing to record and nothing to re-anchor.
	if jumpResult.TotalPrice == 0 {
		return
	}

	if h.mediator == nil {
		logger.Log("ERROR", "Cannot record jump gate fee: no mediator wired", map[string]interface{}{
			"ship": cmd.ShipSymbol,
			"fee":  jumpResult.TotalPrice,
		})
		return
	}

	if agentSymbol == "" {
		agentSymbol = "UNKNOWN"
	}

	// Zero baseline: when AgentCredits is nil the ledger reconstructs
	// balance_after from the running chain; when set it re-anchors to API truth.
	const balanceBefore = 0
	balanceAfter := balanceBefore - jumpResult.TotalPrice

	recordCmd := &ledgerCommands.RecordTransactionCommand{
		PlayerID:             playerID.Value(),
		TransactionType:      string(ledger.TransactionTypeJump),
		Amount:               -jumpResult.TotalPrice, // Negative: a gate fee is an expense
		BalanceBefore:        balanceBefore,
		BalanceAfter:         balanceAfter,
		AuthoritativeBalance: jumpResult.AgentCredits,
		Description: fmt.Sprintf("Jump gate fee for %s to %s",
			cmd.ShipSymbol, jumpResult.DestinationSystem),
		Metadata: map[string]interface{}{
			"agent":       agentSymbol,
			"ship_symbol": cmd.ShipSymbol,
			// origin_system is what makes the fee ATTRIBUTABLE. The fee is a
			// property of the gate a hull DEPARTS from, not of the pair and not of the
			// distance, so without this field the per-gate table can only be recovered by a
			// window function over each hull's jump sequence — which mis-attributes silently
			// the moment a hull's previous row is not where this jump actually started.
			// Recording it is free: the handler already knows where the hull was.
			"origin_system":        originSystem,
			"destination_system":   jumpResult.DestinationSystem,
			"destination_waypoint": jumpResult.DestinationWaypoint,
		},
	}

	// Propagate operation context if present (attributes the fee to the tour /
	// contract / expansion run that spent it).
	if opCtx := shared.OperationContextFromContext(ctx); opCtx != nil && opCtx.IsValid() {
		recordCmd.RelatedEntityType = "container"
		recordCmd.RelatedEntityID = opCtx.ContainerID
		recordCmd.OperationType = opCtx.NormalizedOperationType()
	} else {
		recordCmd.OperationType = "manual"
	}

	// context.Background(): the spend is already real, so the record must not be
	// lost to a cancelled caller context (mirrors refuel/cargo recording).
	if _, err := h.mediator.Send(context.Background(), recordCmd); err != nil {
		logger.Log("ERROR", "Failed to record jump gate fee in ledger", map[string]interface{}{
			"error":     err.Error(),
			"ship":      cmd.ShipSymbol,
			"fee":       jumpResult.TotalPrice,
			"player_id": playerID.Value(),
		})
	}
}
