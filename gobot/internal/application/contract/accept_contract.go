package contract

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	infraPorts "github.com/andrescamacho/spacetraders-go/internal/infrastructure/ports"
)

// AcceptContractCommand - Command to accept a contract
type AcceptContractCommand struct {
	ContractID string
	PlayerID   int
}

// AcceptContractResponse - Response from accept contract command
type AcceptContractResponse struct {
	Contract *contract.Contract
}

// AcceptContractHandler - Handles accept contract commands
type AcceptContractHandler struct {
	contractRepo contract.ContractRepository
	playerRepo   player.PlayerRepository
	apiClient    infraPorts.APIClient
}

// NewAcceptContractHandler creates a new accept contract handler
func NewAcceptContractHandler(
	contractRepo contract.ContractRepository,
	playerRepo player.PlayerRepository,
	apiClient infraPorts.APIClient,
) *AcceptContractHandler {
	return &AcceptContractHandler{
		contractRepo: contractRepo,
		playerRepo:   playerRepo,
		apiClient:    apiClient,
	}
}

// Handle executes the accept contract command
func (h *AcceptContractHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*AcceptContractCommand)
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

	// 3. Accept contract using domain method (validates not already accepted)
	if err := contract.Accept(); err != nil {
		return nil, err
	}

	// 4. Call API to accept contract
	_, err = h.apiClient.AcceptContract(ctx, cmd.ContractID, player.Token)
	if err != nil {
		return nil, fmt.Errorf("API error: %w", err)
	}

	// 5. Save updated contract to database
	if err := h.contractRepo.Add(ctx, contract); err != nil {
		return nil, fmt.Errorf("failed to save contract: %w", err)
	}

	return &AcceptContractResponse{
		Contract: contract,
	}, nil
}
