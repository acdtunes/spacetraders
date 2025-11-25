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

// SellCargoCommand requests cargo sale from a ship at its current docked location.
//
// Business rules enforced:
//   - Ship must be docked at a marketplace
//   - Ship must have sufficient cargo of the specified type
//   - Automatically splits large sales into multiple API transactions based on market limits
//
// To link transactions to a parent operation, add OperationContext to the context using
// shared.WithOperationContext() before sending this command.
type SellCargoCommand struct {
	ShipSymbol string          // Ship symbol (e.g., "SHIP-1")
	GoodSymbol string          // Trade good symbol (e.g., "IRON_ORE")
	Units      int             // Total units to sell
	PlayerID   shared.PlayerID // Player ID for authorization
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
// This handler has been refactored to use the Strategy pattern, delegating to
// CargoTransactionHandler with a SellStrategy. This eliminates ~90% code
// duplication with PurchaseCargoHandler.
//
// The handler maintains backward compatibility by preserving the same public API
// (SellCargoCommand and SellCargoResponse).
type SellCargoHandler struct {
	delegate *CargoTransactionHandler
}

// NewSellCargoHandler creates a new sell cargo handler with required dependencies.
//
// Internally, this creates a CargoTransactionHandler with a SellStrategy,
// eliminating the need for duplicated logic.
//
// The marketRefresher is optional - if nil, market data will not be refreshed after sales.
func NewSellCargoHandler(
	shipRepo navigation.ShipRepository,
	playerRepo player.PlayerRepository,
	apiClient domainPorts.APIClient,
	marketRepo scoutingQuery.MarketRepository,
	mediator common.Mediator,
	marketRefresher MarketRefresher,
) *SellCargoHandler {
	strategy := strategies.NewSellStrategy(apiClient)
	delegate := NewCargoTransactionHandler(strategy, shipRepo, playerRepo, marketRepo, apiClient, mediator, marketRefresher)

	return &SellCargoHandler{
		delegate: delegate,
	}
}

// Handle executes the sell cargo command by delegating to the unified handler.
//
// This method maintains backward compatibility by:
//  1. Converting SellCargoCommand to CargoTransactionCommand
//  2. Delegating to the unified handler
//  3. Converting CargoTransactionResponse back to SellCargoResponse
func (h *SellCargoHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*SellCargoCommand)
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
	return &SellCargoResponse{
		TotalRevenue:     unifiedResp.TotalAmount,
		UnitsSold:        unifiedResp.UnitsProcessed,
		TransactionCount: unifiedResp.TransactionCount,
	}, nil
}
