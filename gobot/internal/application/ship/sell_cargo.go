package ship

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	infraPorts "github.com/andrescamacho/spacetraders-go/internal/infrastructure/ports"
)

// SellCargoCommand requests cargo sale from a ship at its current docked location.
//
// Business rules enforced:
//   - Ship must be docked at a marketplace
//   - Ship must have sufficient cargo of the specified type
//   - Automatically splits large sales into multiple API transactions based on market limits
type SellCargoCommand struct {
	ShipSymbol string // Ship symbol (e.g., "SHIP-1")
	GoodSymbol string // Trade good symbol (e.g., "IRON_ORE")
	Units      int    // Total units to sell
	PlayerID   int    // Player ID for authorization
}

// SellCargoResponse contains the results of a cargo sale operation.
//
// TransactionCount indicates how many API calls were made to complete the sale.
// This is typically > 1 when the requested units exceed the market's transaction limit.
type SellCargoResponse struct {
	TotalRevenue     int // Total credits earned across all transactions
	UnitsSold        int // Total units successfully sold
	TransactionCount int // Number of API transactions executed
}

// SellCargoHandler orchestrates cargo sale operations for ships.
//
// This handler implements automatic transaction splitting when the requested units
// exceed the market's per-transaction limit. It fetches market data to determine
// the limit and executes multiple API calls as needed.
//
// Fallback strategy: If market data is unavailable, the handler attempts a single
// transaction with all requested units.
type SellCargoHandler struct {
	shipRepo   navigation.ShipRepository
	playerRepo player.PlayerRepository
	apiClient  infraPorts.APIClient
	marketRepo MarketRepository
}

// NewSellCargoHandler creates a new sell cargo handler with required dependencies.
func NewSellCargoHandler(
	shipRepo navigation.ShipRepository,
	playerRepo player.PlayerRepository,
	apiClient infraPorts.APIClient,
	marketRepo MarketRepository,
) *SellCargoHandler {
	return &SellCargoHandler{
		shipRepo:   shipRepo,
		playerRepo: playerRepo,
		apiClient:  apiClient,
		marketRepo: marketRepo,
	}
}

// Handle executes the sell cargo command with automatic transaction splitting.
//
// The handler performs the following steps:
//  1. Load player and ship data
//  2. Validate business rules (docked, sufficient cargo)
//  3. Determine transaction limit from market data
//  4. Split sale into multiple transactions if needed
//  5. Execute sales sequentially, accumulating results
func (h *SellCargoHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*SellCargoCommand)
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
		return nil, fmt.Errorf("ship must be docked to sell cargo")
	}

	// 4. Validate ship has enough cargo (business rule)
	currentUnits := ship.Cargo().GetItemUnits(cmd.GoodSymbol)
	if currentUnits < cmd.Units {
		return nil, fmt.Errorf("insufficient cargo: need %d, have %d", cmd.Units, currentUnits)
	}

	// 5. Get transaction limit from market (shared utility)
	waypointSymbol := ship.CurrentLocation().Symbol
	transactionLimit := getTransactionLimit(ctx, h.marketRepo, waypointSymbol, cmd.GoodSymbol, cmd.PlayerID, cmd.Units)

	// 6. Execute sales with transaction splitting
	totalRevenue := 0
	unitsSold := 0
	transactionCount := 0
	unitsRemaining := cmd.Units

	for unitsRemaining > 0 {
		unitsToSell := min(unitsRemaining, transactionLimit)

		result, err := h.apiClient.SellCargo(ctx, cmd.ShipSymbol, cmd.GoodSymbol, unitsToSell, player.Token)
		if err != nil {
			return nil, fmt.Errorf("failed to sell cargo after %d successful transactions (%d units sold, %d credits earned): %w", transactionCount, unitsSold, totalRevenue, err)
		}

		totalRevenue += result.TotalRevenue
		unitsSold += result.UnitsSold
		transactionCount++
		unitsRemaining -= unitsToSell
	}

	return &SellCargoResponse{
		TotalRevenue:     totalRevenue,
		UnitsSold:        unitsSold,
		TransactionCount: transactionCount,
	}, nil
}
