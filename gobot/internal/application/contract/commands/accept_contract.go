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

	// The accept response returns the agent's post-acceptance credits in-band.
	// That authoritative balance replaces the old pre-fetched GetAgent snapshot,
	// which could already be stale (re-anchoring the ledger to a wrong value)
	// and, worse, caused the payment to be dropped from the ledger entirely
	// whenever the fetch failed (sp-sc6u root cause #4).
	contractData, err := h.callAcceptContractAPI(ctx, cmd.ContractID, token)
	if err != nil {
		return nil, err
	}

	if err := h.saveContract(ctx, contract); err != nil {
		return nil, err
	}

	// Record asynchronously (non-blocking). Always record: the acceptance
	// payment is real income even if the API omitted the in-band balance, in
	// which case the ledger reconstructs balance_after from the running chain.
	var authoritativeBalance *int
	if contractData != nil {
		authoritativeBalance = contractData.AgentCredits
	}
	go h.recordContractAcceptance(ctx, contract, authoritativeBalance)

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
	return contract.Accept()
}

func (h *AcceptContractHandler) callAcceptContractAPI(ctx context.Context, contractID string, token string) (*domainPorts.ContractData, error) {
	contractData, err := h.apiClient.AcceptContract(ctx, contractID, token)
	if err != nil {
		return nil, fmt.Errorf("API error: %w", err)
	}
	return contractData, nil
}

func (h *AcceptContractHandler) saveContract(ctx context.Context, contract *contract.Contract) error {
	if err := h.contractRepo.Add(ctx, contract); err != nil {
		return fmt.Errorf("failed to save contract: %w", err)
	}
	return nil
}

// recordContractAcceptance records the contract acceptance payment in the ledger.
// authoritativeBalance, when non-nil, is the agent's post-acceptance credits as
// reported in-band by the accept response; the ledger anchors on it. When nil
// (API omitted it) the zero baseline makes the ledger reconstruct balance_after
// from the running chain.
func (h *AcceptContractHandler) recordContractAcceptance(
	ctx context.Context,
	contract *contract.Contract,
	authoritativeBalance *int,
) {
	logger := logging.LoggerFromContext(ctx)

	payment := contract.Terms().Payment.OnAccepted
	const balanceBefore = 0
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
		PlayerID:             contract.PlayerID().Value(),
		TransactionType:      "CONTRACT_ACCEPTED",
		Amount:               payment, // Positive for income
		BalanceBefore:        balanceBefore,
		BalanceAfter:         balanceAfter,
		AuthoritativeBalance: authoritativeBalance,
		Description:          fmt.Sprintf("Accepted %s contract from %s", contract.Type(), contract.FactionSymbol()),
		Metadata:             metadata,
		RelatedEntityType:    "contract",
		RelatedEntityID:      contract.ContractID(),
		OperationType:        "contract",
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
