package services

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractTypes "github.com/andrescamacho/spacetraders-go/internal/application/contract/types"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ContractMarketService handles contract negotiation and market analysis
type ContractMarketService struct {
	mediator     common.Mediator
	contractRepo domainContract.ContractRepository
}

// NewContractMarketService creates a new contract market service
func NewContractMarketService(
	mediator common.Mediator,
	contractRepo domainContract.ContractRepository,
) *ContractMarketService {
	return &ContractMarketService{
		mediator:     mediator,
		contractRepo: contractRepo,
	}
}

// EnsureAccepted accepts the contract if it isn't already, returning the
// (possibly refreshed) contract. The sourcing defer gate (sp-1z2h) calls this
// BEFORE parking a projected-negative contract: acceptance is what keeps a
// deferred contract discoverable by FindActiveContracts on the next pass and
// protects its accept-by deadline — deferral without acceptance would rot into
// a skip (RULINGS #1). Idempotent for an already-accepted contract.
func (s *ContractMarketService) EnsureAccepted(
	ctx context.Context,
	contract *domainContract.Contract,
	playerID shared.PlayerID,
) (*domainContract.Contract, error) {
	if contract.Accepted() {
		return contract, nil
	}

	acceptCmd := &contractTypes.AcceptContractCommand{
		ContractID: contract.ContractID(),
		PlayerID:   playerID,
	}

	acceptResp, err := s.mediator.Send(ctx, acceptCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to accept contract: %w", err)
	}

	acceptResult := acceptResp.(*contractTypes.AcceptContractResponse)
	return acceptResult.Contract, nil
}

// FulfillContract fulfills a completed contract to claim rewards
func (s *ContractMarketService) FulfillContract(
	ctx context.Context,
	contract *domainContract.Contract,
	playerID shared.PlayerID,
) error {
	fulfillCmd := &FulfillContractCommand{
		ContractID: contract.ContractID(),
		PlayerID:   playerID,
	}

	_, err := s.mediator.Send(ctx, fulfillCmd)
	if err != nil {
		return fmt.Errorf("failed to fulfill contract: %w", err)
	}

	return nil
}

// NegotiateContract negotiates a new contract or resumes an existing one
func (s *ContractMarketService) NegotiateContract(
	ctx context.Context,
	shipSymbol string,
	playerID int,
) (*domainContract.Contract, error) {
	// Check for existing active contracts first
	activeContracts, err := s.contractRepo.FindActiveContracts(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to check active contracts: %w", err)
	}

	logger := common.LoggerFromContext(ctx)

	if len(activeContracts) > 0 {
		// Resume existing contract
		logger.Log("INFO", fmt.Sprintf("Resuming existing active contract: %s", activeContracts[0].ContractID()), nil)
		return activeContracts[0], nil
	}

	// Negotiate new contract
	negotiateCmd := &contractTypes.NegotiateContractCommand{
		ShipSymbol: shipSymbol,
		PlayerID:   shared.MustNewPlayerID(playerID),
	}

	negotiateResp, err := s.mediator.Send(ctx, negotiateCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to negotiate: %w", err)
	}

	negotiateResult := negotiateResp.(*contractTypes.NegotiateContractResponse)
	logger.Log("INFO", fmt.Sprintf("Negotiated contract: %s", negotiateResult.Contract.ContractID()), nil)

	return negotiateResult.Contract, nil
}
