package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractTypes "github.com/andrescamacho/spacetraders-go/internal/application/contract/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

// isContractDeliveryTermsMetError reports whether err is (or wraps, via %w) a
// SpaceTraders API 4509 "Contract delivery terms ... have been met" response —
// the authoritative server signal that a good's delivery is already fully
// satisfied. Detection mirrors the established IsInsufficientCreditsError idiom
// (services/insufficient_credits.go): a substring match on the wire-format
// "code":4509 that survives every %w-wrapping layer between the API client and
// here (the *APIError body preserves it verbatim), since the client's error type
// is not exported for errors.As classification.
func isContractDeliveryTermsMetError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), `"code":4509`)
}

// Type aliases for convenience
type DeliverContractCommand = contractTypes.DeliverContractCommand
type DeliverContractResponse = contractTypes.DeliverContractResponse

// DeliverContractHandler - Handles deliver contract commands
type DeliverContractHandler struct {
	contractRepo contract.ContractRepository
	apiClient    domainPorts.APIClient
	playerRepo   player.PlayerRepository
}

// NewDeliverContractHandler creates a new deliver contract handler
func NewDeliverContractHandler(
	contractRepo contract.ContractRepository,
	apiClient domainPorts.APIClient,
	playerRepo player.PlayerRepository,
) *DeliverContractHandler {
	return &DeliverContractHandler{
		contractRepo: contractRepo,
		apiClient:    apiClient,
		playerRepo:   playerRepo,
	}
}

// Handle executes the deliver contract command
func (h *DeliverContractHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*DeliverContractCommand)
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

	if err := h.validateDeliveryInDomain(contract, cmd.TradeSymbol, cmd.Units); err != nil {
		return nil, err
	}

	deliveryData, err := h.callDeliverCargoAPI(ctx, cmd, token)
	if err != nil {
		// AUTHORITATIVE 4509 RECONCILIATION: a "delivery terms have been met" response is server
		// truth that this good is already fully delivered while the local row lagged behind. Do NOT
		// propagate it as a crash — that wedges the whole contract income stream in a loop (worker
		// crash -> coordinator re-selects the same cargo-laden hull -> deliver -> 4509 -> forever).
		// Reconcile the local delivery to fulfilled and return terminal success instead.
		if isContractDeliveryTermsMetError(err) {
			return h.reconcileDeliveryTermsMet(ctx, contract, cmd)
		}
		return nil, err
	}

	h.syncDeliveryDataFromAPI(contract, deliveryData)

	if err := h.saveContract(ctx, contract); err != nil {
		return nil, err
	}

	return &DeliverContractResponse{
		Contract:       contract,
		UnitsDelivered: cmd.Units,
	}, nil
}

// reconcileDeliveryTermsMet heals a server-met/local-unmet divergence. It marks ONLY the good the
// server named terms-met to UnitsRequired — never a good the server has not confirmed — PERSISTS
// that so the reconciliation survives a restart (RULINGS #2) and the contract can never resume the
// loop, and returns a terminal success. The worker then sees UnitsFulfilled >= UnitsRequired,
// returns, and the workflow fulfils the contract so it leaves the active set; the now-orphaned
// over-fetch cargo is unrelated to any active contract, so the existing FilterUnrelatedCargo and
// auto-liquidation path sells or dumps it — no crash, no retry, no fleet-unassign.
func (h *DeliverContractHandler) reconcileDeliveryTermsMet(ctx context.Context, contract *contract.Contract, cmd *DeliverContractCommand) (common.Response, error) {
	if err := contract.MarkDeliveryTermsMet(cmd.TradeSymbol); err != nil {
		return nil, fmt.Errorf("reconcile terms-met for %s: %w", cmd.TradeSymbol, err)
	}
	if err := h.saveContract(ctx, contract); err != nil {
		return nil, err
	}

	common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
		"Contract %s delivery terms for %s already met server-side (4509); reconciled local fulfilment to the server count and returning terminal success — ending the retry loop and releasing the hull (sp-1pf0r)",
		cmd.ContractID, cmd.TradeSymbol), map[string]interface{}{
		"action":       "contract_terms_met_reconcile",
		"contract_id":  cmd.ContractID,
		"ship_symbol":  cmd.ShipSymbol,
		"trade_symbol": cmd.TradeSymbol,
	})

	// UnitsDelivered reports the units this call reconciled as delivered (the
	// server-side count now recorded locally), not a physical hand-off — nothing
	// left the hull. It is a display/telemetry field with no money or ledger
	// consumer for contracts.
	fulfilled := 0
	for _, d := range contract.Terms().Deliveries {
		if d.TradeSymbol == cmd.TradeSymbol {
			fulfilled = d.UnitsFulfilled
			break
		}
	}
	return &DeliverContractResponse{
		Contract:       contract,
		UnitsDelivered: fulfilled,
	}, nil
}

func (h *DeliverContractHandler) loadContract(ctx context.Context, contractID string, playerID int) (*contract.Contract, error) {
	contract, err := h.contractRepo.FindByID(ctx, contractID)
	if err != nil {
		return nil, fmt.Errorf("contract not found: %w", err)
	}

	// Validate player exists
	_, err = h.playerRepo.FindByID(ctx, contract.PlayerID())
	if err != nil {
		return nil, fmt.Errorf("player not found: %w", err)
	}

	return contract, nil
}

func (h *DeliverContractHandler) validateDeliveryInDomain(contract *contract.Contract, tradeSymbol string, units int) error {
	return contract.DeliverCargo(tradeSymbol, units)
}

func (h *DeliverContractHandler) callDeliverCargoAPI(ctx context.Context, cmd *DeliverContractCommand, token string) (*domainPorts.ContractData, error) {
	deliveryData, err := h.apiClient.DeliverContract(
		ctx,
		cmd.ContractID,
		cmd.ShipSymbol,
		cmd.TradeSymbol,
		cmd.Units,
		token,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to deliver cargo: %w", err)
	}
	return deliveryData, nil
}

func (h *DeliverContractHandler) syncDeliveryDataFromAPI(contract *contract.Contract, deliveryData *domainPorts.ContractData) {
	terms := contract.Terms()
	for i := range terms.Deliveries {
		for _, apiDelivery := range deliveryData.Terms.Deliveries {
			if terms.Deliveries[i].TradeSymbol == apiDelivery.TradeSymbol {
				terms.Deliveries[i].UnitsFulfilled = apiDelivery.UnitsFulfilled
			}
		}
	}
}

func (h *DeliverContractHandler) saveContract(ctx context.Context, contract *contract.Contract) error {
	if err := h.contractRepo.Add(ctx, contract); err != nil {
		return fmt.Errorf("failed to save contract: %w", err)
	}
	return nil
}
