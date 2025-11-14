package contract

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	infraPorts "github.com/andrescamacho/spacetraders-go/internal/infrastructure/ports"
)

// FulfillContractCommand - Command to fulfill a contract
type FulfillContractCommand struct {
	ContractID string
	PlayerID   int
}

// FulfillContractResponse - Response from fulfill contract command
type FulfillContractResponse struct {
	Contract *contract.Contract
}

// FulfillContractHandler - Handles fulfill contract commands
type FulfillContractHandler struct {
	contractRepo contract.ContractRepository
	playerRepo   player.PlayerRepository
	apiClient    infraPorts.APIClient
}

// NewFulfillContractHandler creates a new fulfill contract handler
func NewFulfillContractHandler(
	contractRepo contract.ContractRepository,
	playerRepo player.PlayerRepository,
	apiClient infraPorts.APIClient,
) *FulfillContractHandler {
	return &FulfillContractHandler{
		contractRepo: contractRepo,
		playerRepo:   playerRepo,
		apiClient:    apiClient,
	}
}

// Handle executes the fulfill contract command
func (h *FulfillContractHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*FulfillContractCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	// 1. Get player token
	player, err := h.playerRepo.FindByID(ctx, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("player not found: %w", err)
	}

	// 2. Load contract from database
	contract, err := h.contractRepo.FindByID(ctx, cmd.ContractID, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("contract not found: %w", err)
	}

	// 3. Fulfill contract using domain method (validates deliveries are complete)
	if err := contract.Fulfill(); err != nil {
		return nil, err
	}

	// 4. Call API to fulfill contract
	_, err = h.apiClient.FulfillContract(ctx, cmd.ContractID, player.Token)
	if err != nil {
		return nil, fmt.Errorf("API error: %w", err)
	}

	// 5. Save updated contract to database
	if err := h.contractRepo.Save(ctx, contract); err != nil {
		return nil, fmt.Errorf("failed to save contract: %w", err)
	}

	return &FulfillContractResponse{
		Contract: contract,
	}, nil
}
