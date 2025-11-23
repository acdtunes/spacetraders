package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	scoutingQuery "github.com/andrescamacho/spacetraders-go/internal/application/scouting/queries"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/strategies"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

// PurchaseCargoCommand requests cargo purchase for a ship at its current docked location.
//
// Business rules enforced:
//   - Ship must be docked at a marketplace
//   - Ship must have sufficient available cargo space
//   - Automatically splits large purchases into multiple API transactions based on market limits
//
// To link transactions to a parent operation, add OperationContext to the context using
// shared.WithOperationContext() before sending this command.
type PurchaseCargoCommand struct {
	ShipSymbol string          // Ship symbol (e.g., "SHIP-1")
	GoodSymbol string          // Trade good symbol (e.g., "IRON_ORE")
	Units      int             // Total units to purchase
	PlayerID   shared.PlayerID // Player ID for authorization
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
// This handler has been refactored to use the Strategy pattern, delegating to
// CargoTransactionHandler with a PurchaseStrategy. This eliminates ~90% code
// duplication with SellCargoHandler.
//
// The handler maintains backward compatibility by preserving the same public API
// (PurchaseCargoCommand and PurchaseCargoResponse).
type PurchaseCargoHandler struct {
	delegate *CargoTransactionHandler
}

// NewPurchaseCargoHandler creates a new purchase cargo handler with required dependencies.
//
// Internally, this creates a CargoTransactionHandler with a PurchaseStrategy,
// eliminating the need for duplicated logic.
func NewPurchaseCargoHandler(
	shipRepo navigation.ShipRepository,
	playerRepo player.PlayerRepository,
	apiClient domainPorts.APIClient,
	marketRepo scoutingQuery.MarketRepository,
	mediator common.Mediator,
) *PurchaseCargoHandler {
	strategy := strategies.NewPurchaseStrategy(apiClient)
	delegate := NewCargoTransactionHandler(strategy, shipRepo, playerRepo, marketRepo, apiClient, mediator)

	return &PurchaseCargoHandler{
		delegate: delegate,
	}
}

// Handle executes the purchase cargo command by delegating to the unified handler.
//
// This method maintains backward compatibility by:
//  1. Converting PurchaseCargoCommand to CargoTransactionCommand
//  2. Delegating to the unified handler
//  3. Converting CargoTransactionResponse back to PurchaseCargoResponse
func (h *PurchaseCargoHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*PurchaseCargoCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	// Convert to unified command
	unifiedCmd := &CargoTransactionCommand{
		ShipSymbol: cmd.ShipSymbol,
		GoodSymbol: cmd.GoodSymbol,
		Units:      cmd.Units,
		PlayerID:   cmd.PlayerID,
	}

	// Delegate to unified handler
	response, err := h.delegate.Handle(ctx, unifiedCmd)
	if err != nil {
		return nil, err
	}

	// Convert back to specific response type for backward compatibility
	unifiedResp := response.(*CargoTransactionResponse)
	return &PurchaseCargoResponse{
		TotalCost:        unifiedResp.TotalAmount,
		UnitsAdded:       unifiedResp.UnitsProcessed,
		TransactionCount: unifiedResp.TransactionCount,
	}, nil
}
