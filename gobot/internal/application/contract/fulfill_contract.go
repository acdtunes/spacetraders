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

	player, err := h.getPlayerToken(ctx, cmd.PlayerID)
	if err != nil {
		return nil, err
	}

	contract, err := h.loadContract(ctx, cmd.ContractID, cmd.PlayerID)
	if err != nil {
		return nil, err
	}

	if err := h.fulfillContractInDomain(contract); err != nil {
		return nil, err
	}

	if err := h.callFulfillContractAPI(ctx, cmd.ContractID, player.Token); err != nil {
		return nil, err
	}

	if err := h.saveContract(ctx, contract); err != nil {
		return nil, err
	}

	return &FulfillContractResponse{
		Contract: contract,
	}, nil
}

func (h *FulfillContractHandler) getPlayerToken(ctx context.Context, playerID int) (*player.Player, error) {
	player, err := h.playerRepo.FindByID(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("player not found: %w", err)
	}
	return player, nil
}

func (h *FulfillContractHandler) loadContract(ctx context.Context, contractID string, playerID int) (*contract.Contract, error) {
	contract, err := h.contractRepo.FindByID(ctx, contractID, playerID)
	if err != nil {
		return nil, fmt.Errorf("contract not found: %w", err)
	}
	return contract, nil
}

func (h *FulfillContractHandler) fulfillContractInDomain(contract *contract.Contract) error {
	if err := contract.Fulfill(); err != nil {
		return err
	}
	return nil
}

func (h *FulfillContractHandler) callFulfillContractAPI(ctx context.Context, contractID string, token string) error {
	_, err := h.apiClient.FulfillContract(ctx, contractID, token)
	if err != nil {
		return fmt.Errorf("API error: %w", err)
	}
	return nil
}

func (h *FulfillContractHandler) saveContract(ctx context.Context, contract *contract.Contract) error {
	if err := h.contractRepo.Add(ctx, contract); err != nil {
		return fmt.Errorf("failed to save contract: %w", err)
	}
	return nil
}
