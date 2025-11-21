package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	scoutingQuery "github.com/andrescamacho/spacetraders-go/internal/application/scouting/queries"
	shipPkg "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/strategies"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

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
//
// The handler is Open/Closed:
//   - Open for extension: New transaction types (trade, donate) can be added by implementing CargoTransactionStrategy
//   - Closed for modification: Handler logic doesn't change when adding new transaction types
type CargoTransactionHandler struct {
	strategy   strategies.CargoTransactionStrategy
	shipRepo   navigation.ShipRepository
	playerRepo player.PlayerRepository
	marketRepo scoutingQuery.MarketRepository
}

// NewCargoTransactionHandler creates a new cargo transaction handler with the given strategy.
//
// Different transaction types are created by injecting different strategies:
//   - NewCargoTransactionHandler(NewPurchaseStrategy(...)) - for purchases
//   - NewCargoTransactionHandler(NewSellStrategy(...)) - for sales
//   - NewCargoTransactionHandler(NewTradeStrategy(...)) - future: for trades
func NewCargoTransactionHandler(
	strategy strategies.CargoTransactionStrategy,
	shipRepo navigation.ShipRepository,
	playerRepo player.PlayerRepository,
	marketRepo scoutingQuery.MarketRepository,
) *CargoTransactionHandler {
	return &CargoTransactionHandler{
		strategy:   strategy,
		shipRepo:   shipRepo,
		playerRepo: playerRepo,
		marketRepo: marketRepo,
	}
}

// Handle executes the cargo transaction command with automatic transaction splitting.
//
// The method follows a consistent flow:
//  1. Retrieve player token from context
//  2. Load ship from repository
//  3. Validate ship is docked
//  4. Delegate precondition validation to strategy
//  5. Determine transaction limit from market
//  6. Execute transactions in batches
//  7. Return accumulated results
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

	return h.executeTransactions(ctx, cmd, token, transactionLimit)
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
//  3. Accumulates results (total amount, units processed, transaction count)
//  4. Returns error on first failure with partial success information
func (h *CargoTransactionHandler) executeTransactions(ctx context.Context, cmd *CargoTransactionCommand, token string, transactionLimit int) (*CargoTransactionResponse, error) {
	totalAmount := 0
	unitsProcessed := 0
	transactionCount := 0
	unitsRemaining := cmd.Units

	transactionType := h.strategy.GetTransactionType()

	for unitsRemaining > 0 {
		unitsToProcess := utils.Min(unitsRemaining, transactionLimit)

		result, err := h.strategy.Execute(ctx, cmd.ShipSymbol, cmd.GoodSymbol, unitsToProcess, token)
		if err != nil {
			return nil, fmt.Errorf("partial failure: failed to %s cargo after %d successful transactions (%d units processed, %d credits): %w",
				transactionType, transactionCount, unitsProcessed, totalAmount, err)
		}

		totalAmount += result.TotalAmount
		unitsProcessed += result.UnitsProcessed
		transactionCount++
		unitsRemaining -= unitsToProcess
	}

	return &CargoTransactionResponse{
		TotalAmount:      totalAmount,
		UnitsProcessed:   unitsProcessed,
		TransactionCount: transactionCount,
	}, nil
}
