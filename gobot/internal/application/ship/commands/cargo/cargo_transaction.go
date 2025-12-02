package cargo

import (
	"context"
	"fmt"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	ledgerCommands "github.com/andrescamacho/spacetraders-go/internal/application/ledger/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	scoutingQuery "github.com/andrescamacho/spacetraders-go/internal/application/scouting/queries"
	shipPkg "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/strategies"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// MarketRefresher defines the interface for refreshing market data after transactions.
// This interface allows the CargoTransactionHandler to refresh prices without
// creating import cycles with scouting/commands.
type MarketRefresher interface {
	ScanAndSaveMarket(ctx context.Context, playerID uint, waypointSymbol string) error
}

// CargoTransactionCommand represents a unified command for cargo transactions (purchase or sell).
//
// This unified command replaces separate PurchaseCargoCommand and SellCargoCommand,
// reducing duplication and enabling extensibility through the Strategy pattern.
//
// The specific transaction type (purchase vs sell) is determined by the strategy
// injected into the handler.
//
// Business rules enforced:
//   - Ship must be docked at a marketplace
//   - Transaction-specific preconditions are validated by the strategy
//   - Automatically splits large transactions based on market limits
//
// To link transactions to a parent operation, add OperationContext to the context using
// shared.WithOperationContext() before sending this command.
type CargoTransactionCommand struct {
	ShipSymbol string          // Ship symbol (e.g., "SHIP-1")
	GoodSymbol string          // Trade good symbol (e.g., "IRON_ORE")
	Units      int             // Total units to transaction
	PlayerID   shared.PlayerID // Player ID for authorization
}

// CargoTransactionResponse contains the unified results of a cargo transaction.
//
// TransactionCount indicates how many API calls were made to complete the operation.
// This is typically > 1 when the requested units exceed the market's transaction limit.
type CargoTransactionResponse struct {
	TotalAmount      int // Total credits (cost for purchase, revenue for sell)
	UnitsProcessed   int // Total units (added for purchase, sold for sell)
	TransactionCount int // Number of API transactions executed
}

// CargoTransactionHandler orchestrates cargo transaction operations using the Strategy pattern.
//
// This handler unifies the logic previously duplicated between PurchaseCargoHandler
// and SellCargoHandler (~90% code duplication eliminated).
//
// Key responsibilities:
//   - Validate ship is docked
//   - Load player token
//   - Delegate validation to strategy (cargo space vs cargo availability)
//   - Fetch transaction limit from market
//   - Execute transactions in batches via strategy
//   - Accumulate results
//   - Record financial transactions in ledger
//
// The handler is Open/Closed:
//   - Open for extension: New transaction types (trade, donate) can be added by implementing CargoTransactionStrategy
//   - Closed for modification: Handler logic doesn't change when adding new transaction types
type CargoTransactionHandler struct {
	strategy        strategies.CargoTransactionStrategy
	shipRepo        navigation.ShipRepository
	playerRepo      player.PlayerRepository
	marketRepo      scoutingQuery.MarketRepository
	apiClient       domainPorts.APIClient
	mediator        common.Mediator
	marketRefresher MarketRefresher // Optional: refreshes market data after transactions
}

// NewCargoTransactionHandler creates a new cargo transaction handler with the given strategy.
//
// Different transaction types are created by injecting different strategies:
//   - NewCargoTransactionHandler(NewPurchaseStrategy(...)) - for purchases
//   - NewCargoTransactionHandler(NewSellStrategy(...)) - for sales
//   - NewCargoTransactionHandler(NewTradeStrategy(...)) - future: for trades
//
// The marketRefresher is optional - if nil, market data will not be refreshed after transactions.
func NewCargoTransactionHandler(
	strategy strategies.CargoTransactionStrategy,
	shipRepo navigation.ShipRepository,
	playerRepo player.PlayerRepository,
	marketRepo scoutingQuery.MarketRepository,
	apiClient domainPorts.APIClient,
	mediator common.Mediator,
	marketRefresher MarketRefresher,
) *CargoTransactionHandler {
	return &CargoTransactionHandler{
		strategy:        strategy,
		shipRepo:        shipRepo,
		playerRepo:      playerRepo,
		marketRepo:      marketRepo,
		apiClient:       apiClient,
		mediator:        mediator,
		marketRefresher: marketRefresher,
	}
}

// Handle executes the cargo transaction command with automatic transaction splitting.
//
// The method follows a consistent flow:
//  1. Retrieve player token from context
//  2. Load ship from repository
//  3. Validate ship is docked
//  4. Delegate precondition validation to strategy
//  5. Fetch current player balance (for ledger)
//  6. Determine transaction limit from market
//  7. Execute transactions in batches
//  8. Record transaction in ledger (async)
//  9. Return accumulated results
func (h *CargoTransactionHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*CargoTransactionCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	token, err := h.getPlayerToken(ctx)
	if err != nil {
		return nil, err
	}

	ship, err := h.loadShip(ctx, cmd)
	if err != nil {
		return nil, err
	}

	if err := h.validateShipDocked(ship); err != nil {
		return nil, err
	}

	if err := h.strategy.ValidatePreconditions(ship, cmd.GoodSymbol, cmd.Units); err != nil {
		return nil, err
	}

	transactionLimit := h.getTransactionLimit(ctx, ship, cmd)
	waypointSymbol := ship.CurrentLocation().Symbol

	response, err := h.executeTransactions(ctx, cmd, token, transactionLimit, waypointSymbol)
	if err != nil {
		return nil, err
	}

	// Note: Ledger recording now happens inside executeTransactions after each batch
	// This ensures partial purchases are recorded even if later batches fail

	return response, nil
}

// getPlayerToken retrieves the player token from the context.
func (h *CargoTransactionHandler) getPlayerToken(ctx context.Context) (string, error) {
	return common.PlayerTokenFromContext(ctx)
}

// loadShip loads the ship from the repository by symbol and player ID.
func (h *CargoTransactionHandler) loadShip(ctx context.Context, cmd *CargoTransactionCommand) (*navigation.Ship, error) {
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("ship not found: %w", err)
	}
	return ship, nil
}

// validateShipDocked ensures the ship is docked before allowing cargo transactions.
func (h *CargoTransactionHandler) validateShipDocked(ship *navigation.Ship) error {
	if !ship.IsDocked() {
		return fmt.Errorf("ship must be docked to perform cargo transactions")
	}
	return nil
}

// getTransactionLimit retrieves the market's transaction limit for the good.
//
// This limit determines how many units can be transacted in a single API call.
// The handler automatically splits transactions that exceed this limit.
func (h *CargoTransactionHandler) getTransactionLimit(ctx context.Context, ship *navigation.Ship, cmd *CargoTransactionCommand) int {
	waypointSymbol := ship.CurrentLocation().Symbol
	return shipPkg.GetTransactionLimit(ctx, h.marketRepo, waypointSymbol, cmd.GoodSymbol, cmd.PlayerID.Value(), cmd.Units)
}

// executeTransactions performs the cargo transaction in batches, respecting market limits.
//
// The method:
//  1. Splits the total units into batches based on transaction limit
//  2. Executes each batch via the strategy
//  3. Records ledger entry immediately after each successful batch
//  4. Accumulates results (total amount, units processed, transaction count)
//  5. Returns error on first failure with partial success information (partial success is already recorded)
//
// OPTIMIZATION: waypointSymbol is passed from caller to avoid duplicate ship API load.
// Balance tracking is skipped to avoid GetAgent API call - transactions still recorded.
func (h *CargoTransactionHandler) executeTransactions(ctx context.Context, cmd *CargoTransactionCommand, token string, transactionLimit int, waypointSymbol string) (*CargoTransactionResponse, error) {
	totalAmount := 0
	unitsProcessed := 0
	transactionCount := 0
	unitsRemaining := cmd.Units

	transactionType := h.strategy.GetTransactionType()

	// OPTIMIZATION: Skip balance fetch (saves 1 API call)
	// Ledger entries will have balance=0 but transaction amounts are still tracked
	runningBalance := 0

	for unitsRemaining > 0 {
		unitsToProcess := utils.Min(unitsRemaining, transactionLimit)

		result, err := h.strategy.Execute(ctx, cmd.ShipSymbol, cmd.GoodSymbol, unitsToProcess, token)
		if err != nil {
			// Return error but partial success is already recorded in ledger
			return nil, fmt.Errorf("partial failure: failed to %s cargo after %d successful transactions (%d units processed, %d credits): %w",
				transactionType, transactionCount, unitsProcessed, totalAmount, err)
		}

		totalAmount += result.TotalAmount
		unitsProcessed += result.UnitsProcessed
		transactionCount++
		unitsRemaining -= unitsToProcess

		// Record ledger entry immediately after each successful batch
		batchResponse := &CargoTransactionResponse{
			TotalAmount:      result.TotalAmount,
			UnitsProcessed:   result.UnitsProcessed,
			TransactionCount: 1,
		}
		h.recordCargoTransaction(ctx, cmd, waypointSymbol, batchResponse, runningBalance)

		// Update running balance for next batch (approximate, without initial balance)
		if transactionType == "purchase" {
			runningBalance -= result.TotalAmount
		} else {
			runningBalance += result.TotalAmount
		}
	}

	// Refresh market data once after all batches complete (not per-batch)
	// This reduces API calls from 2N to N+1 for N batches
	h.refreshMarketData(ctx, cmd.PlayerID, waypointSymbol)

	return &CargoTransactionResponse{
		TotalAmount:      totalAmount,
		UnitsProcessed:   unitsProcessed,
		TransactionCount: transactionCount,
	}, nil
}

// fetchCurrentCredits fetches the player's current credits from the API
func (h *CargoTransactionHandler) fetchCurrentCredits(ctx context.Context) (int, error) {
	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return 0, fmt.Errorf("player token not found in context: %w", err)
	}

	agent, err := h.apiClient.GetAgent(ctx, token)
	if err != nil {
		return 0, fmt.Errorf("failed to fetch agent credits: %w", err)
	}

	return agent.Credits, nil
}

// recordCargoTransaction records the cargo transaction in the ledger
func (h *CargoTransactionHandler) recordCargoTransaction(
	ctx context.Context,
	cmd *CargoTransactionCommand,
	waypointSymbol string,
	response *CargoTransactionResponse,
	balanceBefore int,
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

	// Create record transaction command
	recordCmd := &ledgerCommands.RecordTransactionCommand{
		PlayerID:        cmd.PlayerID.Value(),
		TransactionType: ledgerTxType,
		Amount:          amount,
		BalanceBefore:   balanceBefore,
		BalanceAfter:    balanceAfter,
		Description:     fmt.Sprintf("%s %d units of %s at %s", transactionTypeStr, response.UnitsProcessed, cmd.GoodSymbol, waypointSymbol),
		Metadata:        metadata,
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

// refreshMarketData triggers a market data refresh after a successful transaction.
// This ensures that price data remains up-to-date after buy/sell operations.
// The refresh is non-blocking - errors are logged but don't fail the transaction.
//
// OPTIMIZATION: Skip refresh when called from manufacturing operations (context flag).
// Manufacturing scans markets separately and doesn't need immediate refresh after each sell.
func (h *CargoTransactionHandler) refreshMarketData(ctx context.Context, playerID shared.PlayerID, waypointSymbol string) {
	// Skip if no market refresher is configured
	if h.marketRefresher == nil {
		return
	}

	// OPTIMIZATION: Skip market refresh for manufacturing operations
	// They scan markets independently and don't need post-transaction refresh
	if shared.SkipMarketRefreshFromContext(ctx) {
		return
	}

	logger := logging.LoggerFromContext(ctx)

	err := h.marketRefresher.ScanAndSaveMarket(ctx, uint(playerID.Value()), waypointSymbol)
	if err != nil {
		// Log error but don't fail the transaction - market refresh is non-critical
		logger.Log("WARN", "Failed to refresh market data after transaction", map[string]interface{}{
			"waypoint": waypointSymbol,
			"error":    err.Error(),
		})
	} else {
		logger.Log("DEBUG", "Market data refreshed after transaction", map[string]interface{}{
			"waypoint": waypointSymbol,
		})
	}
}
