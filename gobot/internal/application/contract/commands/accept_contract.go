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
type AcceptContractCommand = contractTypes.AcceptContractCommand
type AcceptContractResponse = contractTypes.AcceptContractResponse

// AcceptContractHandler - Handles accept contract commands
type AcceptContractHandler struct {
	contractRepo contract.ContractRepository
	playerRepo   player.PlayerRepository
	apiClient    domainPorts.APIClient
	mediator     common.Mediator
}

// NewAcceptContractHandler creates a new accept contract handler
func NewAcceptContractHandler(
	contractRepo contract.ContractRepository,
	playerRepo player.PlayerRepository,
	apiClient domainPorts.APIClient,
	mediator common.Mediator,
) *AcceptContractHandler {
	return &AcceptContractHandler{
		contractRepo: contractRepo,
		playerRepo:   playerRepo,
		apiClient:    apiClient,
		mediator:     mediator,
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

	// Fetch balance before accepting
	balanceBefore, err := h.fetchCurrentCredits(ctx, token)
	if err != nil {
		// Log warning but don't fail the operation
		logger := logging.LoggerFromContext(ctx)
		logger.Log("WARN", "Failed to fetch credits before accepting contract, ledger entry will not be recorded", map[string]interface{}{
			"error":       err.Error(),
			"contract_id": cmd.ContractID,
		})
	}

	if err := h.callAcceptContractAPI(ctx, cmd.ContractID, token); err != nil {
		return nil, err
	}

	if err := h.saveContract(ctx, contract); err != nil {
		return nil, err
	}

	// Record transaction asynchronously (non-blocking)
	if balanceBefore > 0 {
		go h.recordContractAcceptance(ctx, contract, balanceBefore)
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

// fetchCurrentCredits fetches the player's current credits from the API
func (h *AcceptContractHandler) fetchCurrentCredits(ctx context.Context, token string) (int, error) {
	agent, err := h.apiClient.GetAgent(ctx, token)
	if err != nil {
		return 0, fmt.Errorf("failed to fetch agent credits: %w", err)
	}
	return agent.Credits, nil
}

// recordContractAcceptance records the contract acceptance payment in the ledger
func (h *AcceptContractHandler) recordContractAcceptance(
	ctx context.Context,
	contract *contract.Contract,
	balanceBefore int,
) {
	logger := logging.LoggerFromContext(ctx)

	payment := contract.Terms().Payment.OnAccepted
	balanceAfter := balanceBefore + payment

	// Fetch player to get agent symbol
	playerData, err := h.playerRepo.FindByID(ctx, contract.PlayerID())
	agentSymbol := "UNKNOWN"
	if err == nil && playerData != nil {
		agentSymbol = playerData.AgentSymbol
	}

	// Build metadata
	metadata := map[string]interface{}{
		"agent":          agentSymbol,
		"contract_id":    contract.ContractID(),
		"faction":        contract.FactionSymbol(),
		"contract_type":  contract.Type(),
	}

	// Create record transaction command
	recordCmd := &ledgerCommands.RecordTransactionCommand{
		PlayerID:          contract.PlayerID().Value(),
		TransactionType:   "CONTRACT_ACCEPTED",
		Amount:            payment, // Positive for income
		BalanceBefore:     balanceBefore,
		BalanceAfter:      balanceAfter,
		Description:       fmt.Sprintf("Accepted %s contract from %s", contract.Type(), contract.FactionSymbol()),
		Metadata:          metadata,
		RelatedEntityType: "contract",
		RelatedEntityID:   contract.ContractID(),
		OperationType:     "contract",
	}

	// Record transaction via mediator
	_, err = h.mediator.Send(context.Background(), recordCmd)
	if err != nil {
		// Log error but don't fail the operation
		logger.Log("ERROR", "Failed to record contract acceptance transaction in ledger", map[string]interface{}{
			"error":       err.Error(),
			"contract_id": contract.ContractID(),
			"payment":     payment,
			"player_id":   contract.PlayerID().Value(),
		})
	} else {
		logger.Log("DEBUG", "Contract acceptance transaction recorded in ledger", map[string]interface{}{
			"contract_id": contract.ContractID(),
			"payment":     payment,
		})
	}
}
