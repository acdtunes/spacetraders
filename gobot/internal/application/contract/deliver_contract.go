package contract

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/ports"
)

// DeliverContractCommand - Command to deliver cargo for a contract
type DeliverContractCommand struct {
	ContractID  string
	ShipSymbol  string
	TradeSymbol string
	Units       int
	PlayerID    int
}

// DeliverContractResponse - Response from deliver contract command
type DeliverContractResponse struct {
	Contract       *contract.Contract
	UnitsDelivered int
}

// DeliverContractHandler - Handles deliver contract commands
type DeliverContractHandler struct {
	contractRepo contract.ContractRepository
	apiClient    ports.APIClient
	playerRepo   player.PlayerRepository
}

// NewDeliverContractHandler creates a new deliver contract handler
func NewDeliverContractHandler(
	contractRepo contract.ContractRepository,
	apiClient ports.APIClient,
	playerRepo player.PlayerRepository,
) *DeliverContractHandler {
	return &DeliverContractHandler{
		contractRepo: contractRepo,
		apiClient:    apiClient,
		playerRepo:   playerRepo,
	}
}

// Handle executes the deliver contract command
func (h *DeliverContractHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*DeliverContractCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	// 1. Get player token
	player, err := h.playerRepo.FindByID(ctx, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("player not found: %w", err)
	}

	// 2. Load contract from repository
	contract, err := h.contractRepo.FindByID(ctx, cmd.ContractID, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("contract not found: %w", err)
	}

	// 3. Validate delivery using domain logic (BEFORE calling API)
	if err := contract.DeliverCargo(cmd.TradeSymbol, cmd.Units); err != nil {
		return nil, err
	}

	// 4. Call API to deliver cargo
	deliveryData, err := h.apiClient.DeliverContract(
		ctx,
		cmd.ContractID,
		cmd.ShipSymbol,
		cmd.TradeSymbol,
		cmd.Units,
		player.Token,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to deliver cargo: %w", err)
	}

	// 5. Update contract with actual delivery data from API response
	// The domain entity's DeliverCargo already updated the units,
	// but we need to sync with the API's actual response
	terms := contract.Terms()
	for i := range terms.Deliveries {
		// Find matching delivery in API response
		for _, apiDelivery := range deliveryData.Terms.Deliveries {
			if terms.Deliveries[i].TradeSymbol == apiDelivery.TradeSymbol {
				// Update the units fulfilled from API
				terms.Deliveries[i].UnitsFulfilled = apiDelivery.UnitsFulfilled
			}
		}
	}

	// 6. Save updated contract to repository
	if err := h.contractRepo.Add(ctx, contract); err != nil {
		return nil, fmt.Errorf("failed to save contract: %w", err)
	}

	return &DeliverContractResponse{
		Contract:       contract,
		UnitsDelivered: cmd.Units,
	}, nil
}
