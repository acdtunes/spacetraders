package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	scoutingQuery "github.com/andrescamacho/spacetraders-go/internal/application/scouting/queries"
	shipPkg "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	infraPorts "github.com/andrescamacho/spacetraders-go/internal/infrastructure/ports"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
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
	PlayerID   shared.PlayerID    // Player ID for authorization
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
	marketRepo scoutingQuery.MarketRepository
}

// NewSellCargoHandler creates a new sell cargo handler with required dependencies.
func NewSellCargoHandler(
	shipRepo navigation.ShipRepository,
	playerRepo player.PlayerRepository,
	apiClient infraPorts.APIClient,
	marketRepo scoutingQuery.MarketRepository,
) *SellCargoHandler {
	return &SellCargoHandler{
		shipRepo:   shipRepo,
		playerRepo: playerRepo,
		apiClient:  apiClient,
		marketRepo: marketRepo,
	}
}

// Handle executes the sell cargo command with automatic transaction splitting.
func (h *SellCargoHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*SellCargoCommand)
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

	if err := h.validateShipDockedForSale(ship); err != nil {
		return nil, err
	}

	if err := h.validateSufficientCargoForSale(ship, cmd); err != nil {
		return nil, err
	}

	transactionLimit := h.getTransactionLimit(ctx, ship, cmd)

	return h.executeSaleTransactions(ctx, cmd, token, transactionLimit)
}

func (h *SellCargoHandler) getPlayerToken(ctx context.Context) (string, error) {
	return common.PlayerTokenFromContext(ctx)
}

func (h *SellCargoHandler) loadShip(ctx context.Context, cmd *SellCargoCommand) (*navigation.Ship, error) {
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("ship not found: %w", err)
	}
	return ship, nil
}

func (h *SellCargoHandler) validateShipDockedForSale(ship *navigation.Ship) error {
	if !ship.IsDocked() {
		return fmt.Errorf("ship must be docked to sell cargo")
	}
	return nil
}

func (h *SellCargoHandler) validateSufficientCargoForSale(ship *navigation.Ship, cmd *SellCargoCommand) error {
	currentUnits := ship.Cargo().GetItemUnits(cmd.GoodSymbol)
	if currentUnits < cmd.Units {
		return fmt.Errorf("insufficient cargo: need %d, have %d", cmd.Units, currentUnits)
	}
	return nil
}

func (h *SellCargoHandler) getTransactionLimit(ctx context.Context, ship *navigation.Ship, cmd *SellCargoCommand) int {
	waypointSymbol := ship.CurrentLocation().Symbol
	return shipPkg.GetTransactionLimit(ctx, h.marketRepo, waypointSymbol, cmd.GoodSymbol, cmd.PlayerID.Value(), cmd.Units)
}

func (h *SellCargoHandler) executeSaleTransactions(ctx context.Context, cmd *SellCargoCommand, token string, transactionLimit int) (*SellCargoResponse, error) {
	totalRevenue := 0
	unitsSold := 0
	transactionCount := 0
	unitsRemaining := cmd.Units

	for unitsRemaining > 0 {
		unitsToSell := utils.Min(unitsRemaining, transactionLimit)

		result, err := h.apiClient.SellCargo(ctx, cmd.ShipSymbol, cmd.GoodSymbol, unitsToSell, token)
		if err != nil {
			return nil, fmt.Errorf("partial failure: failed to sell cargo after %d successful transactions (%d units sold, %d credits earned): %w", transactionCount, unitsSold, totalRevenue, err)
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
