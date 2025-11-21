package contract

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	infraPorts "github.com/andrescamacho/spacetraders-go/internal/infrastructure/ports"
)

// AcceptContractCommand - Command to accept a contract
type AcceptContractCommand struct {
	ContractID string
	PlayerID   shared.PlayerID
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

	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return nil, err
	}

	contract, err := h.loadContract(ctx, cmd.ContractID, cmd.PlayerID.Value())
	if err != nil {
		return nil, err
	}

	if err := h.acceptContractInDomain(contract); err != nil {
		return nil, err
	}

	if err := h.callAcceptContractAPI(ctx, cmd.ContractID, token); err != nil {
		return nil, err
	}

	if err := h.saveContract(ctx, contract); err != nil {
		return nil, err
	}

	return &AcceptContractResponse{
		Contract: contract,
	}, nil
}

func (h *AcceptContractHandler) loadContract(ctx context.Context, contractID string, playerID int) (*contract.Contract, error) {
	contract, err := h.contractRepo.FindByID(ctx, contractID)
	if err != nil {
		return nil, fmt.Errorf("contract not found: %w", err)
	}
	return contract, nil
}

func (h *AcceptContractHandler) acceptContractInDomain(contract *contract.Contract) error {
	if err := contract.Accept(); err != nil {
		return err
	}
	return nil
}

func (h *AcceptContractHandler) callAcceptContractAPI(ctx context.Context, contractID string, token string) error {
	_, err := h.apiClient.AcceptContract(ctx, contractID, token)
	if err != nil {
		return fmt.Errorf("API error: %w", err)
	}
	return nil
}

func (h *AcceptContractHandler) saveContract(ctx context.Context, contract *contract.Contract) error {
	if err := h.contractRepo.Add(ctx, contract); err != nil {
		return fmt.Errorf("failed to save contract: %w", err)
	}
	return nil
}
