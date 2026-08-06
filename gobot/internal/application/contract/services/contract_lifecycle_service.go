package services

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractQueries "github.com/andrescamacho/spacetraders-go/internal/application/contract/queries"
	contractTypes "github.com/andrescamacho/spacetraders-go/internal/application/contract/types"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Type aliases for convenience
type NegotiateContractCommand = contractTypes.NegotiateContractCommand
type NegotiateContractResponse = contractTypes.NegotiateContractResponse
type AcceptContractCommand = contractTypes.AcceptContractCommand
type AcceptContractResponse = contractTypes.AcceptContractResponse
type FulfillContractCommand = contractTypes.FulfillContractCommand

// ServerContractReader reads a contract's AUTHORITATIVE state straight from the game server.
// Declared here, at the consumer, so this package depends on the one method it needs instead of
// the whole APIClient surface; *api.SpaceTradersClient satisfies it.
type ServerContractReader interface {
	GetContract(ctx context.Context, contractID, token string) (*domainPorts.ContractData, error)
}

// ContractLifecycleService handles contract negotiation, acceptance, and fulfillment
type ContractLifecycleService struct {
	mediator     common.Mediator
	contractRepo domainContract.ContractRepository
	// serverContracts is the game server's view of a contract, used by ReconcileWithServer to
	// heal a local row that lagged behind a delivery the server already accepted. Production
	// passes the real API client positionally (see NewRunWorkflowHandler) — this is not an
	// arming seam, it is nil only in unit tests that drive the lifecycle without an API.
	serverContracts ServerContractReader
}

// NewContractLifecycleService creates a new contract lifecycle service
func NewContractLifecycleService(
	mediator common.Mediator,
	contractRepo domainContract.ContractRepository,
	serverContracts ServerContractReader,
) *ContractLifecycleService {
	return &ContractLifecycleService{
		mediator:        mediator,
		contractRepo:    contractRepo,
		serverContracts: serverContracts,
	}
}

// ReconcileWithServer folds the game server's authoritative contract state into the local
// aggregate BEFORE any delivery is planned, and persists what it healed (sp-20eyn).
//
// The local delivery counts are a cache of numbers the server owns, written only after a
// deliver the server already accepted. A worker that dies in between leaves the cache
// permanently behind, and the next worker resumes from that stale view and delivers the same
// load a second time — the 2026-08-05 TORWIND contract that read 0/47 locally while the server
// read 94/47. Reading server truth here is what makes the delivery leg's own
// "units remaining <= 0" guards trustworthy: without it they are evaluated against a number
// that can only be too low, which is the direction that spends.
//
// FAIL-OPEN on a read failure, and deliberately so. This is a NEW guard, not a weakened one:
// when the contracts endpoint cannot be read the workflow proceeds on exactly the local state it
// uses today, so the change is add-only (RULINGS #4). Failing closed instead would park every
// contract in the fleet on any transient contracts-endpoint hiccup — a strictly larger outage
// than the one this fixes. The failure is logged at WARNING so a persistent blind spot surfaces
// rather than silently degrading into the double-delivery this exists to prevent.
func (s *ContractLifecycleService) ReconcileWithServer(
	ctx context.Context,
	c *domainContract.Contract,
	playerID shared.PlayerID,
) (*domainContract.Contract, error) {
	logger := common.LoggerFromContext(ctx)

	if s.serverContracts == nil {
		return c, nil
	}

	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		s.logReconcileBlind(logger, c, "no player token in context", err)
		return c, nil
	}

	data, err := s.serverContracts.GetContract(ctx, c.ContractID(), token)
	if err != nil {
		s.logReconcileBlind(logger, c, "contract read failed", err)
		return c, nil
	}

	if data.Accepted {
		c.MarkAcceptedFromServer()
	}

	if data.Fulfilled {
		c.MarkFulfilledFromServer()
		s.persistReconciled(ctx, logger, c)
		return c, nil
	}

	observed := make(map[string]int, len(data.Terms.Deliveries))
	for _, d := range data.Terms.Deliveries {
		observed[d.TradeSymbol] = d.UnitsFulfilled
		if d.UnitsFulfilled > d.UnitsRequired {
			// The only place an over-delivery is ever visible. It means units were handed
			// over that the contract will never pay for, so it is logged as loudly as the
			// crash it used to hide behind.
			logger.Log("ERROR", fmt.Sprintf(
				"Contract %s OVER-DELIVERED on %s: the server holds %d units against %d required — %d units were delivered twice and will not be paid for (sp-20eyn)",
				c.ContractID(), d.TradeSymbol, d.UnitsFulfilled, d.UnitsRequired, d.UnitsFulfilled-d.UnitsRequired),
				map[string]interface{}{
					"action":          "contract_over_delivered",
					"contract_id":     c.ContractID(),
					"trade_symbol":    d.TradeSymbol,
					"units_fulfilled": d.UnitsFulfilled,
					"units_required":  d.UnitsRequired,
					"units_wasted":    d.UnitsFulfilled - d.UnitsRequired,
				})
		}
	}

	if !c.ReconcileDeliveredFromServer(observed) {
		return c, nil
	}

	logger.Log("INFO", fmt.Sprintf(
		"Contract %s reconciled to server truth before delivery planning; local delivery counts had lagged behind deliveries the server already accepted (sp-20eyn)",
		c.ContractID()), map[string]interface{}{
		"action":      "contract_server_reconcile",
		"contract_id": c.ContractID(),
		"can_fulfill": c.CanFulfill(),
	})
	s.persistReconciled(ctx, logger, c)

	return c, nil
}

// persistReconciled writes the healed contract back so the next worker resumes from server
// truth rather than re-reading the same stale row and re-deriving the same wrong plan. A write
// failure is logged, never fatal: the in-memory aggregate this pass runs on is already correct,
// so the workflow is strictly better off continuing than crashing.
func (s *ContractLifecycleService) persistReconciled(ctx context.Context, logger common.ContainerLogger, c *domainContract.Contract) {
	if err := s.contractRepo.Add(ctx, c); err != nil {
		logger.Log("WARNING", "Failed to persist server-reconciled contract; this pass still runs on server truth", map[string]interface{}{
			"action":      "contract_server_reconcile_persist_failed",
			"contract_id": c.ContractID(),
			"error":       err.Error(),
		})
	}
}

func (s *ContractLifecycleService) logReconcileBlind(logger common.ContainerLogger, c *domainContract.Contract, reason string, err error) {
	logger.Log("WARNING", fmt.Sprintf(
		"Cannot reconcile contract %s against the server (%s); proceeding on local delivery counts, which can only be behind — a re-delivery is possible this pass (sp-20eyn)",
		c.ContractID(), reason), map[string]interface{}{
		"action":      "contract_server_reconcile_blind",
		"contract_id": c.ContractID(),
		"reason":      reason,
		"error":       err.Error(),
	})
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
