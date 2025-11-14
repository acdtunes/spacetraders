package ship

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	infraPorts "github.com/andrescamacho/spacetraders-go/internal/infrastructure/ports"
)

// PurchaseCargoCommand requests cargo purchase for a ship at its current docked location.
//
// Business rules enforced:
//   - Ship must be docked at a marketplace
//   - Ship must have sufficient available cargo space
//   - Automatically splits large purchases into multiple API transactions based on market limits
type PurchaseCargoCommand struct {
	ShipSymbol string // Ship symbol (e.g., "SHIP-1")
	GoodSymbol string // Trade good symbol (e.g., "IRON_ORE")
	Units      int    // Total units to purchase
	PlayerID   int    // Player ID for authorization
}

// PurchaseCargoResponse contains the results of a cargo purchase operation.
//
// TransactionCount indicates how many API calls were made to complete the purchase.
// This is typically > 1 when the requested units exceed the market's transaction limit.
type PurchaseCargoResponse struct {
	TotalCost        int // Total credits spent across all transactions
	UnitsAdded       int // Total units successfully added to cargo
	TransactionCount int // Number of API transactions executed
}

// MarketRepository defines operations for market data access
type MarketRepository interface {
	GetMarketData(ctx context.Context, playerID uint, waypointSymbol string) (*market.Market, error)
}

// PurchaseCargoHandler orchestrates cargo purchase operations for ships.
//
// This handler implements automatic transaction splitting when the requested units
// exceed the market's per-transaction limit. It fetches market data to determine
// the limit and executes multiple API calls as needed.
//
// Fallback strategy: If market data is unavailable, the handler attempts a single
// transaction with all requested units.
type PurchaseCargoHandler struct {
	shipRepo   navigation.ShipRepository
	playerRepo player.PlayerRepository
	apiClient  infraPorts.APIClient
	marketRepo MarketRepository
}

// NewPurchaseCargoHandler creates a new purchase cargo handler with required dependencies.
func NewPurchaseCargoHandler(
	shipRepo navigation.ShipRepository,
	playerRepo player.PlayerRepository,
	apiClient infraPorts.APIClient,
	marketRepo MarketRepository,
) *PurchaseCargoHandler {
	return &PurchaseCargoHandler{
		shipRepo:   shipRepo,
		playerRepo: playerRepo,
		apiClient:  apiClient,
		marketRepo: marketRepo,
	}
}

// Handle executes the purchase cargo command with automatic transaction splitting.
//
// The handler performs the following steps:
//  1. Load player and ship data
//  2. Validate business rules (docked, sufficient cargo space)
//  3. Determine transaction limit from market data
//  4. Split purchase into multiple transactions if needed
//  5. Execute purchases sequentially, accumulating results
func (h *PurchaseCargoHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*PurchaseCargoCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	// 1. Load player to get token
	player, err := h.playerRepo.FindByID(ctx, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("player not found: %w", err)
	}

	// 2. Load ship from repository
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("ship not found: %w", err)
	}

	// 3. Validate ship is docked (business rule)
	if !ship.IsDocked() {
		return nil, fmt.Errorf("ship must be docked to purchase cargo")
	}

	// 4. Validate cargo space (business rule)
	availableSpace := ship.AvailableCargoSpace()
	if availableSpace < cmd.Units {
		return nil, fmt.Errorf("insufficient cargo space: need %d, have %d", cmd.Units, availableSpace)
	}

	// 5. Get transaction limit from market (shared utility)
	waypointSymbol := ship.CurrentLocation().Symbol
	transactionLimit := getTransactionLimit(ctx, h.marketRepo, waypointSymbol, cmd.GoodSymbol, cmd.PlayerID, cmd.Units)

	// 6. Execute purchases with transaction splitting
	totalCost := 0
	unitsAdded := 0
	transactionCount := 0
	unitsRemaining := cmd.Units

	for unitsRemaining > 0 {
		unitsToBuy := min(unitsRemaining, transactionLimit)

		result, err := h.apiClient.PurchaseCargo(ctx, cmd.ShipSymbol, cmd.GoodSymbol, unitsToBuy, player.Token)
		if err != nil {
			return nil, fmt.Errorf("failed to purchase cargo after %d successful transactions (%d units added, %d credits spent): %w", transactionCount, unitsAdded, totalCost, err)
		}

		totalCost += result.TotalCost
		unitsAdded += result.UnitsAdded
		transactionCount++
		unitsRemaining -= unitsToBuy
	}

	return &PurchaseCargoResponse{
		TotalCost:        totalCost,
		UnitsAdded:       unitsAdded,
		TransactionCount: transactionCount,
	}, nil
}
