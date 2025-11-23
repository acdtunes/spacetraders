package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractTypes "github.com/andrescamacho/spacetraders-go/internal/application/contract/types"
	ledgerCommands "github.com/andrescamacho/spacetraders-go/internal/application/ledger/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

// Type aliases for convenience
type FulfillContractCommand = contractTypes.FulfillContractCommand
type FulfillContractResponse = contractTypes.FulfillContractResponse

// FulfillContractHandler - Handles fulfill contract commands
type FulfillContractHandler struct {
	contractRepo contract.ContractRepository
	playerRepo   player.PlayerRepository
	apiClient    domainPorts.APIClient
	mediator     common.Mediator
}

// NewFulfillContractHandler creates a new fulfill contract handler
func NewFulfillContractHandler(
	contractRepo contract.ContractRepository,
	playerRepo player.PlayerRepository,
	apiClient domainPorts.APIClient,
	mediator common.Mediator,
) *FulfillContractHandler {
	return &FulfillContractHandler{
		contractRepo: contractRepo,
		playerRepo:   playerRepo,
		apiClient:    apiClient,
		mediator:     mediator,
	}
}

// Handle executes the fulfill contract command
func (h *FulfillContractHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*FulfillContractCommand)
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

	if err := h.fulfillContractInDomain(contract); err != nil {
		return nil, err
	}

	// Fetch balance before fulfilling
	balanceBefore, err := h.fetchCurrentCredits(ctx, token)
	if err != nil {
		// Log warning but don't fail the operation
		logger := logging.LoggerFromContext(ctx)
		logger.Log("WARN", "Failed to fetch credits before fulfilling contract, ledger entry will not be recorded", map[string]interface{}{
			"error":       err.Error(),
			"contract_id": cmd.ContractID,
		})
	}

	if err := h.callFulfillContractAPI(ctx, cmd.ContractID, token); err != nil {
		return nil, err
	}

	if err := h.saveContract(ctx, contract); err != nil {
		return nil, err
	}

	// Record transaction asynchronously (non-blocking)
	if balanceBefore > 0 {
		go h.recordContractFulfillment(ctx, contract, balanceBefore)
	}

	return &FulfillContractResponse{
		Contract: contract,
	}, nil
}

func (h *FulfillContractHandler) loadContract(ctx context.Context, contractID string, playerID int) (*contract.Contract, error) {
	contract, err := h.contractRepo.FindByID(ctx, contractID)
	if err != nil {
		return nil, fmt.Errorf("contract not found: %w", err)
	}

	// Validate contract belongs to the requesting player
	if contract.PlayerID().Value() != playerID {
		return nil, fmt.Errorf("contract not found")
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

// fetchCurrentCredits fetches the player's current credits from the API
func (h *FulfillContractHandler) fetchCurrentCredits(ctx context.Context, token string) (int, error) {
	agent, err := h.apiClient.GetAgent(ctx, token)
	if err != nil {
		return 0, fmt.Errorf("failed to fetch agent credits: %w", err)
	}
	return agent.Credits, nil
}

// recordContractFulfillment records the contract fulfillment payment in the ledger
func (h *FulfillContractHandler) recordContractFulfillment(
	ctx context.Context,
	contract *contract.Contract,
	balanceBefore int,
) {
	logger := logging.LoggerFromContext(ctx)

	payment := contract.Terms().Payment.OnFulfilled
	balanceAfter := balanceBefore + payment

	// Fetch player to get agent symbol
	playerData, err := h.playerRepo.FindByID(ctx, contract.PlayerID())
	agentSymbol := "UNKNOWN"
	if err == nil && playerData != nil {
		agentSymbol = playerData.AgentSymbol
	}

	// Build metadata
	metadata := map[string]interface{}{
		"agent":         agentSymbol,
		"contract_id":   contract.ContractID(),
		"faction":       contract.FactionSymbol(),
		"contract_type": contract.Type(),
	}

	// Create record transaction command
	recordCmd := &ledgerCommands.RecordTransactionCommand{
		PlayerID:          contract.PlayerID().Value(),
		TransactionType:   "CONTRACT_FULFILLED",
		Amount:            payment, // Positive for income
		BalanceBefore:     balanceBefore,
		BalanceAfter:      balanceAfter,
		Description:       fmt.Sprintf("Fulfilled %s contract from %s", contract.Type(), contract.FactionSymbol()),
		Metadata:          metadata,
		RelatedEntityType: "contract",
		RelatedEntityID:   contract.ContractID(),
	}

	// Record transaction via mediator
	_, err = h.mediator.Send(context.Background(), recordCmd)
	if err != nil {
		// Log error but don't fail the operation
		logger.Log("ERROR", "Failed to record contract fulfillment transaction in ledger", map[string]interface{}{
			"error":       err.Error(),
			"contract_id": contract.ContractID(),
			"payment":     payment,
			"player_id":   contract.PlayerID().Value(),
		})
	} else {
		logger.Log("DEBUG", "Contract fulfillment transaction recorded in ledger", map[string]interface{}{
			"contract_id": contract.ContractID(),
			"payment":     payment,
		})
	}
}
