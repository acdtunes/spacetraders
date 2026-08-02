package cargo

import (
	"context"
	"fmt"
	"strings"

	ledgerCommands "github.com/andrescamacho/spacetraders-go/internal/application/ledger/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// persistCargoDelta writes this transaction's cargo change onto the FRESH ship row
// under CAS-retry: on a concurrent-writer version conflict the closure
// re-loads the fresh row and re-applies ONLY this op's own cargo state for
// cmd.GoodSymbol, so a colliding writer's nav/fuel/other-cargo update survives
// instead of being last-write-wins clobbered (the reported cargo desync). This is
// the single field this transaction owns; the closure touches nothing else. A
// transaction with nothing to write (e.g. floor/ceiling-aborted before any tranche)
// is not persisted — no spurious version bump. The persist error is intentionally
// not fatal: the API transaction already committed and the daemon cache reconciles
// from the API on the next sync.
//
// It runs on the FAILURE path too. Returning before this write on a multi-tranche
// sell whose later tranche errored orphans the units its EARLIER tranches really
// sold: the ledger records them, the hold does not, and the cached count stays
// permanently ahead of the server. That drift is what makes a hull ask to sell 71
// units it no longer has, so healing it here removes a standing producer of the
// very rejection the clamp above contains.
func (h *CargoTransactionHandler) persistCargoDelta(ctx context.Context, cmd *CargoTransactionCommand, transactionType string, unitsProcessed, serverGoodUnits int) {
	if unitsProcessed <= 0 && serverGoodUnits < 0 {
		return
	}
	_, _, _ = h.shipRepo.SaveWithRetry(ctx, cmd.ShipSymbol, cmd.PlayerID,
		func(sh *navigation.Ship) (bool, error) {
			// SERVER TRUTH WINS: when a 4219 told us exactly how many units
			// the hull holds, reconcile the cache to that count ABSOLUTELY rather than
			// applying our own delta to a belief the rejection just disproved —
			// deducting the 11 units we sold from a phantom 71 would leave 60 phantom
			// units behind, re-arming the identical failure on the next leg.
			//
			// DOWNWARD ONLY: the server count may shrink a phantom hold but must never
			// inflate one, so a cache already at or below server truth is left untouched.
			// Believing we hold LESS than we do costs an under-sized sell; believing we
			// hold more is what strands hulls, so the asymmetry fails closed (RULINGS #4).
			if serverGoodUnits >= 0 {
				held := 0
				if c := sh.Cargo(); c != nil {
					held = c.GetItemUnits(cmd.GoodSymbol)
				}
				if held <= serverGoodUnits {
					return false, nil
				}
				_ = sh.RemoveCargo(cmd.GoodSymbol, held-serverGoodUnits)
				return true, nil
			}
			if transactionType == "purchase" {
				_ = sh.ReceiveCargo(&shared.CargoItem{Symbol: cmd.GoodSymbol, Units: unitsProcessed})
			} else {
				_ = sh.RemoveCargo(cmd.GoodSymbol, unitsProcessed)
			}
			return true, nil
		})
}

// recordCargoTransaction records the cargo transaction in the ledger
func (h *CargoTransactionHandler) recordCargoTransaction(
	ctx context.Context,
	cmd *CargoTransactionCommand,
	waypointSymbol string,
	response *CargoTransactionResponse,
	balanceBefore int,
	authoritativeBalance *int,
) {
	logger := logging.LoggerFromContext(ctx)

	// Skip recording if amount is zero (transaction validation requires amount != 0)
	if response.TotalAmount == 0 {
		logger.Log("DEBUG", "Skipping ledger entry for zero-amount transaction", map[string]interface{}{
			"ship":  cmd.ShipSymbol,
			"good":  cmd.GoodSymbol,
			"units": response.UnitsProcessed,
		})
		return
	}

	// Determine transaction type and amount sign
	transactionTypeStr := strings.ToUpper(h.strategy.GetTransactionType())
	var ledgerTxType string
	var amount int
	var balanceAfter int

	if transactionTypeStr == "PURCHASE" {
		ledgerTxType = "PURCHASE_CARGO"
		amount = -response.TotalAmount // Negative for expense
		balanceAfter = balanceBefore - response.TotalAmount
	} else if transactionTypeStr == "SELL" {
		ledgerTxType = "SELL_CARGO"
		amount = response.TotalAmount // Positive for income
		balanceAfter = balanceBefore + response.TotalAmount
	} else {
		logger.Log("ERROR", "Unknown transaction type for ledger recording", map[string]interface{}{
			"type": transactionTypeStr,
		})
		return
	}

	// Fetch player to get agent symbol
	playerData, err := h.playerRepo.FindByID(ctx, cmd.PlayerID)
	agentSymbol := "UNKNOWN"
	if err == nil && playerData != nil {
		agentSymbol = playerData.AgentSymbol
	}

	// Build metadata
	metadata := map[string]interface{}{
		"agent":       agentSymbol,
		"ship_symbol": cmd.ShipSymbol,
		"good_symbol": cmd.GoodSymbol,
		"units":       response.UnitsProcessed,
		"waypoint":    waypointSymbol,
	}

	// Tag a factory input buy with the a5j7 selector branch (ELIGIBLE | RESCUE |
	// era-end | disabled) that chose its source, recorded beside good_symbol so the analyst can
	// grade A1 (supply-first compliance) and split legal RESCUE buys from violations straight
	// from the transactions table. Only the input-buy path stamps the branch onto ctx
	// (production_executor.buyGood); every other caller through this shared recorder — trade,
	// tour, arb, contract delivery, refuel, the fabricated-output harvest — leaves it unset, so
	// the key is simply absent on their rows and their metadata is unchanged.
	if branch, ok := shared.SelectorBranchFromContext(ctx); ok {
		metadata["selector_branch"] = branch
	}

	// Create record transaction command. AuthoritativeBalance carries the
	// in-band agent.credits (when present) so the ledger anchors on API truth
	// rather than the zero-baseline reconstruction below.
	recordCmd := &ledgerCommands.RecordTransactionCommand{
		PlayerID:             cmd.PlayerID.Value(),
		TransactionType:      ledgerTxType,
		Amount:               amount,
		BalanceBefore:        balanceBefore,
		BalanceAfter:         balanceAfter,
		AuthoritativeBalance: authoritativeBalance,
		Description:          fmt.Sprintf("%s %d units of %s at %s", transactionTypeStr, response.UnitsProcessed, cmd.GoodSymbol, waypointSymbol),
		Metadata:             metadata,
	}

	// Propagate operation context if present in the context
	if opCtx := shared.OperationContextFromContext(ctx); opCtx != nil && opCtx.IsValid() {
		recordCmd.RelatedEntityType = "container"
		recordCmd.RelatedEntityID = opCtx.ContainerID
		recordCmd.OperationType = opCtx.NormalizedOperationType()
	} else {
		// No operation context - mark as manual transaction
		recordCmd.OperationType = "manual"
	}

	// Record transaction via mediator
	_, err = h.mediator.Send(context.Background(), recordCmd)
	if err != nil {
		// Log error but don't fail the operation
		logger.Log("ERROR", "Failed to record cargo transaction in ledger", map[string]interface{}{
			"error":     err.Error(),
			"ship":      cmd.ShipSymbol,
			"good":      cmd.GoodSymbol,
			"amount":    response.TotalAmount,
			"player_id": cmd.PlayerID.Value(),
		})
	} else {
		logger.Log("DEBUG", "Cargo transaction recorded in ledger", map[string]interface{}{
			"ship":   cmd.ShipSymbol,
			"good":   cmd.GoodSymbol,
			"amount": response.TotalAmount,
			"type":   ledgerTxType,
		})
	}
}
