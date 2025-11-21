package services

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractTypes "github.com/andrescamacho/spacetraders-go/internal/application/contract/types"
	contractQueries "github.com/andrescamacho/spacetraders-go/internal/application/contract/queries"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Type aliases for convenience
type NegotiateContractCommand = contractTypes.NegotiateContractCommand
type NegotiateContractResponse = contractTypes.NegotiateContractResponse
type AcceptContractCommand = contractTypes.AcceptContractCommand
type AcceptContractResponse = contractTypes.AcceptContractResponse
type FulfillContractCommand = contractTypes.FulfillContractCommand

// ContractLifecycleService handles contract negotiation, acceptance, and fulfillment
type ContractLifecycleService struct {
	mediator     common.Mediator
	contractRepo domainContract.ContractRepository
}

// NewContractLifecycleService creates a new contract lifecycle service
func NewContractLifecycleService(
	mediator common.Mediator,
	contractRepo domainContract.ContractRepository,
) *ContractLifecycleService {
	return &ContractLifecycleService{
		mediator:     mediator,
		contractRepo: contractRepo,
	}
}

// FindOrNegotiateContract checks for existing active contracts or negotiates a new one
func (s *ContractLifecycleService) FindOrNegotiateContract(
	ctx context.Context,
	shipSymbol string,
	playerID shared.PlayerID,
) (*domainContract.Contract, bool, error) {
	logger := common.LoggerFromContext(ctx)

	activeContracts, err := s.contractRepo.FindActiveContracts(ctx, playerID.Value())
	if err != nil {
		return nil, false, fmt.Errorf("failed to check active contracts: %w", err)
	}

	if len(activeContracts) > 0 {
		contract := activeContracts[0]
		logger.Log("INFO", "Resuming existing active contract", map[string]interface{}{
			"ship_symbol": shipSymbol,
			"action":      "resume_contract",
			"contract_id": contract.ContractID(),
		})
		return contract, false, nil
	}

	logger.Log("INFO", "Contract negotiation initiated", map[string]interface{}{
		"ship_symbol": shipSymbol,
		"action":      "negotiate_contract",
	})

	negotiateCmd := &NegotiateContractCommand{
		ShipSymbol: shipSymbol,
		PlayerID:   playerID,
	}

	negotiateResp, err := s.mediator.Send(ctx, negotiateCmd)
	if err != nil {
		return nil, false, fmt.Errorf("failed to negotiate contract: %w", err)
	}

	negotiateResult := negotiateResp.(*NegotiateContractResponse)

	if negotiateResult.WasNegotiated {
		logger.Log("INFO", "Contract negotiation successful", map[string]interface{}{
			"ship_symbol": shipSymbol,
			"action":      "contract_negotiated",
			"contract_id": negotiateResult.Contract.ContractID(),
		})
	}

	return negotiateResult.Contract, negotiateResult.WasNegotiated, nil
}

// EvaluateContractProfitability evaluates whether a contract is profitable
func (s *ContractLifecycleService) EvaluateContractProfitability(
	ctx context.Context,
	shipSymbol string,
	playerID shared.PlayerID,
	contract *domainContract.Contract,
) (common.Response, error) {
	logger := common.LoggerFromContext(ctx)

	logger.Log("INFO", "Contract profitability evaluation initiated", map[string]interface{}{
		"ship_symbol": shipSymbol,
		"action":      "evaluate_profitability",
		"contract_id": contract.ContractID(),
	})

	profitabilityQuery := &contractQueries.EvaluateContractProfitabilityQuery{
		Contract:        contract,
		ShipSymbol:      shipSymbol,
		PlayerID:        playerID,
		FuelCostPerTrip: 0,
	}

	profitabilityResp, err := s.mediator.Send(ctx, profitabilityQuery)
	if err != nil {
		logger.Log("WARNING", "Contract profitability evaluation failed", map[string]interface{}{
			"ship_symbol": shipSymbol,
			"action":      "evaluate_profitability",
			"contract_id": contract.ContractID(),
			"error":       err.Error(),
		})
		return nil, err
	}

	profitResult := profitabilityResp.(*contractQueries.ProfitabilityResult)
	if !profitResult.IsProfitable {
		logger.Log("WARNING", "Contract unprofitable but accepting anyway", map[string]interface{}{
			"ship_symbol": shipSymbol,
			"action":      "accept_unprofitable",
			"contract_id": contract.ContractID(),
			"reason":      profitResult.Reason,
		})
	} else {
		logger.Log("INFO", "Contract profitability confirmed", map[string]interface{}{
			"ship_symbol": shipSymbol,
			"action":      "profitability_check",
			"contract_id": contract.ContractID(),
		})
	}

	return profitabilityResp, nil
}

// AcceptContractIfNeeded accepts a contract if not already accepted
func (s *ContractLifecycleService) AcceptContractIfNeeded(
	ctx context.Context,
	contract *domainContract.Contract,
	playerID shared.PlayerID,
) (*domainContract.Contract, bool, error) {
	if contract.Accepted() {
		return contract, false, nil
	}

	acceptCmd := &AcceptContractCommand{
		ContractID: contract.ContractID(),
		PlayerID:   playerID,
	}

	acceptResp, err := s.mediator.Send(ctx, acceptCmd)
	if err != nil {
		return nil, false, fmt.Errorf("failed to accept contract: %w", err)
	}

	acceptResult := acceptResp.(*AcceptContractResponse)
	return acceptResult.Contract, true, nil
}

// FulfillContract fulfills a contract
func (s *ContractLifecycleService) FulfillContract(
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

// CalculateTotalProfit calculates the total profit from a contract
func (s *ContractLifecycleService) CalculateTotalProfit(
	contract *domainContract.Contract,
) int {
	return contract.Terms().Payment.OnAccepted + contract.Terms().Payment.OnFulfilled
}
