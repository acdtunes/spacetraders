package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// RecordChartReward writes a charting reward to the financial ledger, RE-ANCHORING the running
// chain to the post-reward agent credits the chart response carries in-band.
//
// Best-effort: the credits have already landed, so a ledger failure is logged, never returned.
func RecordChartReward(
	ctx context.Context,
	mediator common.Mediator,
	playerID shared.PlayerID,
	shipSymbol string,
	result *ports.ChartResult,
) {
	// A zero reward is not a transaction: ledger.Validate rejects amount == 0. Nothing was
	// paid, so there is nothing to record and nothing to re-anchor.
	if result == nil || result.Reward == 0 {
		return
	}

	logger := common.LoggerFromContext(ctx)
	if mediator == nil {
		logger.Log("ERROR", "Cannot record charting reward: no mediator wired", map[string]interface{}{
			"ship":   shipSymbol,
			"reward": result.Reward,
		})
		return
	}

	// Zero baseline: with AgentCredits nil the ledger reconstructs balance_after from the
	// running chain; with it set the row re-anchors to API truth.
	const balanceBefore = 0

	command := &RecordTransactionCommand{
		PlayerID:             playerID.Value(),
		TransactionType:      string(ledger.TransactionTypeChart),
		Amount:               result.Reward, // Positive: a charting reward is income
		BalanceBefore:        balanceBefore,
		BalanceAfter:         balanceBefore + result.Reward,
		AuthoritativeBalance: result.AgentCredits,
		Description: fmt.Sprintf("Charting reward for %s at %s",
			shipSymbol, result.WaypointSymbol),
		Metadata: map[string]interface{}{
			"ship_symbol": shipSymbol,
			// The reward is priced per WAYPOINT on trait rarity, so the waypoint is what
			// makes a row attributable; nothing else in the row identifies what was charted.
			"waypoint_symbol": result.WaypointSymbol,
		},
		OperationType: "manual",
	}

	// Attribute the reward to the charting run that earned it when one is on the context.
	if opCtx := shared.OperationContextFromContext(ctx); opCtx != nil && opCtx.IsValid() {
		command.RelatedEntityType = "container"
		command.RelatedEntityID = opCtx.ContainerID
		command.OperationType = opCtx.NormalizedOperationType()
	}

	// context.Background(): the reward is already real, so the record must not be lost to a
	// cancelled caller context (mirrors the jump-fee and refuel recording paths).
	if _, err := mediator.Send(context.Background(), command); err != nil {
		logger.Log("ERROR", "Failed to record charting reward in ledger", map[string]interface{}{
			"error":     err.Error(),
			"ship":      shipSymbol,
			"reward":    result.Reward,
			"player_id": playerID.Value(),
		})
	}
}
