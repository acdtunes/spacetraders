package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	infraPorts "github.com/andrescamacho/spacetraders-go/internal/infrastructure/ports"
)

// NegotiateContractCommand - Command to negotiate a new contract
type NegotiateContractCommand struct {
	ShipSymbol string
	PlayerID   shared.PlayerID
}

// NegotiateContractResponse - Response from negotiate contract command
type NegotiateContractResponse struct {
	Contract      *contract.Contract
	WasNegotiated bool // false if existing contract returned (error 4511)
}

// NegotiateContractHandler - Handles negotiate contract commands
type NegotiateContractHandler struct {
	contractRepo contract.ContractRepository
	shipRepo     navigation.ShipRepository
	playerRepo   player.PlayerRepository
	apiClient    infraPorts.APIClient
}

// NewNegotiateContractHandler creates a new negotiate contract handler
func NewNegotiateContractHandler(
	contractRepo contract.ContractRepository,
	shipRepo navigation.ShipRepository,
	playerRepo player.PlayerRepository,
	apiClient infraPorts.APIClient,
) *NegotiateContractHandler {
	return &NegotiateContractHandler{
		contractRepo: contractRepo,
		shipRepo:     shipRepo,
		playerRepo:   playerRepo,
		apiClient:    apiClient,
	}
}

// Handle executes the negotiate contract command
func (h *NegotiateContractHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*NegotiateContractCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return nil, err
	}

	ship, err := h.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, err
	}

	if err := h.ensureShipDocked(ctx, ship, cmd.PlayerID); err != nil {
		return nil, err
	}

	result, err := h.callNegotiateContractAPI(ctx, cmd.ShipSymbol, token)

	if existingContract, wasExisting := h.handleExistingContractError(ctx, result, err, token, cmd.PlayerID); wasExisting {
		return existingContract, nil
	}

	if err != nil {
		return nil, fmt.Errorf("failed to negotiate contract: %w", err)
	}

	if result == nil || result.Contract == nil {
		return nil, fmt.Errorf("API returned nil result or contract")
	}

	newContract := h.convertToDomain(result.Contract, cmd.PlayerID)

	if err := h.saveContract(ctx, newContract); err != nil {
		return nil, err
	}

	return &NegotiateContractResponse{
		Contract:      newContract,
		WasNegotiated: true,
	}, nil
}

func (h *NegotiateContractHandler) loadShip(ctx context.Context, shipSymbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
	ship, err := h.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("ship not found: %w", err)
	}
	return ship, nil
}

func (h *NegotiateContractHandler) ensureShipDocked(ctx context.Context, ship *navigation.Ship, playerID shared.PlayerID) error {
	stateChanged, err := ship.EnsureDocked()
	if err != nil {
		return err
	}

	if stateChanged {
		if err := h.shipRepo.Dock(ctx, ship, playerID); err != nil {
			return fmt.Errorf("failed to dock ship: %w", err)
		}
	}

	return nil
}

func (h *NegotiateContractHandler) callNegotiateContractAPI(ctx context.Context, shipSymbol string, token string) (*infraPorts.ContractNegotiationResult, error) {
	result, err := h.apiClient.NegotiateContract(ctx, shipSymbol, token)
	return result, err
}

func (h *NegotiateContractHandler) handleExistingContractError(
	ctx context.Context,
	result *infraPorts.ContractNegotiationResult,
	err error,
	token string,
	playerID shared.PlayerID,
) (*NegotiateContractResponse, bool) {
	if result != nil && result.ErrorCode == 4511 {
		existingContractData, err := h.apiClient.GetContract(ctx, result.ExistingContractID, token)
		if err != nil {
			return nil, false
		}

		existingContract := h.convertToDomain(existingContractData, playerID)

		if err := h.contractRepo.Add(ctx, existingContract); err != nil {
			return nil, false
		}

		return &NegotiateContractResponse{
			Contract:      existingContract,
			WasNegotiated: false,
		}, true
	}
	return nil, false
}

func (h *NegotiateContractHandler) saveContract(ctx context.Context, contract *contract.Contract) error {
	if err := h.contractRepo.Add(ctx, contract); err != nil {
		return fmt.Errorf("failed to save contract: %w", err)
	}
	return nil
}

// convertToDomain converts API contract data to domain entity
func (h *NegotiateContractHandler) convertToDomain(data *infraPorts.ContractData, playerID shared.PlayerID) *contract.Contract {
	// Convert deliveries
	deliveries := make([]contract.Delivery, len(data.Terms.Deliveries))
	for i, d := range data.Terms.Deliveries {
		deliveries[i] = contract.Delivery{
			TradeSymbol:       d.TradeSymbol,
			DestinationSymbol: d.DestinationSymbol,
			UnitsRequired:     d.UnitsRequired,
			UnitsFulfilled:    d.UnitsFulfilled,
		}
	}

	// Build contract terms
	terms := contract.Terms{
		Payment: contract.Payment{
			OnAccepted:  data.Terms.Payment.OnAccepted,
			OnFulfilled: data.Terms.Payment.OnFulfilled,
		},
		Deliveries:       deliveries,
		DeadlineToAccept: data.Terms.DeadlineToAccept,
		Deadline:         data.Terms.Deadline,
	}

	// Create contract
	contractEntity, _ := contract.NewContract(
		data.ID,
		playerID,
		data.FactionSymbol,
		data.Type,
		terms,
	)

	// Restore state from API data
	if data.Accepted {
		contractEntity.Accept()
	}
	if data.Fulfilled {
		contractEntity.Fulfill()
	}

	return contractEntity
}
