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
	PlayerID   shared.PlayerID    // Player ID for authorization
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
	marketRepo scoutingQuery.MarketRepository
}

// NewPurchaseCargoHandler creates a new purchase cargo handler with required dependencies.
func NewPurchaseCargoHandler(
	shipRepo navigation.ShipRepository,
	playerRepo player.PlayerRepository,
	apiClient infraPorts.APIClient,
	marketRepo scoutingQuery.MarketRepository,
) *PurchaseCargoHandler {
	return &PurchaseCargoHandler{
		shipRepo:   shipRepo,
		playerRepo: playerRepo,
		apiClient:  apiClient,
		marketRepo: marketRepo,
	}
}

// Handle executes the purchase cargo command with automatic transaction splitting.
func (h *PurchaseCargoHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*PurchaseCargoCommand)
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

	if err := h.validateShipDockedForPurchase(ship); err != nil {
		return nil, err
	}

	if err := h.validateSufficientCargoSpace(ship, cmd.Units); err != nil {
		return nil, err
	}

	transactionLimit := h.getTransactionLimit(ctx, ship, cmd)

	return h.executePurchaseTransactions(ctx, cmd, token, transactionLimit)
}

func (h *PurchaseCargoHandler) getPlayerToken(ctx context.Context) (string, error) {
	return common.PlayerTokenFromContext(ctx)
}

func (h *PurchaseCargoHandler) loadShip(ctx context.Context, cmd *PurchaseCargoCommand) (*navigation.Ship, error) {
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("ship not found: %w", err)
	}
	return ship, nil
}

func (h *PurchaseCargoHandler) validateShipDockedForPurchase(ship *navigation.Ship) error {
	if !ship.IsDocked() {
		return fmt.Errorf("ship must be docked to purchase cargo")
	}
	return nil
}

func (h *PurchaseCargoHandler) validateSufficientCargoSpace(ship *navigation.Ship, unitsNeeded int) error {
	availableSpace := ship.AvailableCargoSpace()
	if availableSpace < unitsNeeded {
		return fmt.Errorf("insufficient cargo space: need %d, have %d", unitsNeeded, availableSpace)
	}
	return nil
}

func (h *PurchaseCargoHandler) getTransactionLimit(ctx context.Context, ship *navigation.Ship, cmd *PurchaseCargoCommand) int {
	waypointSymbol := ship.CurrentLocation().Symbol
	return shipPkg.GetTransactionLimit(ctx, h.marketRepo, waypointSymbol, cmd.GoodSymbol, cmd.PlayerID.Value(), cmd.Units)
}

func (h *PurchaseCargoHandler) executePurchaseTransactions(ctx context.Context, cmd *PurchaseCargoCommand, token string, transactionLimit int) (*PurchaseCargoResponse, error) {
	totalCost := 0
	unitsAdded := 0
	transactionCount := 0
	unitsRemaining := cmd.Units

	for unitsRemaining > 0 {
		unitsToBuy := utils.Min(unitsRemaining, transactionLimit)

		result, err := h.apiClient.PurchaseCargo(ctx, cmd.ShipSymbol, cmd.GoodSymbol, unitsToBuy, token)
		if err != nil {
			return nil, fmt.Errorf("partial failure: failed to purchase cargo after %d successful transactions (%d units added, %d credits spent): %w", transactionCount, unitsAdded, totalCost, err)
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
