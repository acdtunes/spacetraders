package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractTypes "github.com/andrescamacho/spacetraders-go/internal/application/contract/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

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
	if err := contract.DeliverCargo(tradeSymbol, units); err != nil {
		return err
	}
	return nil
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
