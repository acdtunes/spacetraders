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

	// The fulfill response returns the agent's post-fulfillment credits in-band.
	// That authoritative balance replaces the old pre-fetched GetAgent snapshot,
	// which could already be stale (re-anchoring the ledger to a wrong value)
	// and, worse, caused the payment to be dropped from the ledger entirely
	// whenever the fetch failed (sp-sc6u root cause #4).
	contractData, err := h.callFulfillContractAPI(ctx, cmd.ContractID, token)
	if err != nil {
		return nil, err
	}

	if err := h.saveContract(ctx, contract); err != nil {
		return nil, err
	}

	// Record asynchronously (non-blocking). Always record: the fulfillment
	// payment is real income even if the API omitted the in-band balance, in
	// which case the ledger reconstructs balance_after from the running chain.
	var authoritativeBalance *int
	if contractData != nil {
		authoritativeBalance = contractData.AgentCredits
	}
	go h.recordContractFulfillment(ctx, contract, authoritativeBalance)

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
	return contract.Fulfill()
}

func (h *FulfillContractHandler) callFulfillContractAPI(ctx context.Context, contractID string, token string) (*domainPorts.ContractData, error) {
	contractData, err := h.apiClient.FulfillContract(ctx, contractID, token)
	if err != nil {
		return nil, fmt.Errorf("API error: %w", err)
	}
	return contractData, nil
}

func (h *FulfillContractHandler) saveContract(ctx context.Context, contract *contract.Contract) error {
	if err := h.contractRepo.Add(ctx, contract); err != nil {
		return fmt.Errorf("failed to save contract: %w", err)
	}
	return nil
}

// recordContractFulfillment records the contract fulfillment payment in the ledger.
// authoritativeBalance, when non-nil, is the agent's post-fulfillment credits as
// reported in-band by the fulfill response; the ledger anchors on it. When nil
// (API omitted it) the zero baseline makes the ledger reconstruct balance_after
// from the running chain.
func (h *FulfillContractHandler) recordContractFulfillment(
	ctx context.Context,
	contract *contract.Contract,
	authoritativeBalance *int,
) {
	logger := logging.LoggerFromContext(ctx)

	payment := contract.Terms().Payment.OnFulfilled
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
		TransactionType:      "CONTRACT_FULFILLED",
		Amount:               payment, // Positive for income
		BalanceBefore:        balanceBefore,
		BalanceAfter:         balanceAfter,
		AuthoritativeBalance: authoritativeBalance,
		Description:          fmt.Sprintf("Fulfilled %s contract from %s", contract.Type(), contract.FactionSymbol()),
		Metadata:             metadata,
		RelatedEntityType:    "contract",
		RelatedEntityID:      contract.ContractID(),
		OperationType:        "contract",
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
