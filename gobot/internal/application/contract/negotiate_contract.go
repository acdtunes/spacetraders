package contract

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	infraPorts "github.com/andrescamacho/spacetraders-go/internal/infrastructure/ports"
)

// NegotiateContractCommand - Command to negotiate a new contract
type NegotiateContractCommand struct {
	ShipSymbol string
	PlayerID   int
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

	// 1. Get player to retrieve token
	player, err := h.playerRepo.FindByID(ctx, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("player not found: %w", err)
	}

	// 2. Load ship from repository
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("ship not found: %w", err)
	}

	// 3. Ensure ship is docked (idempotent)
	stateChanged, err := ship.EnsureDocked()
	if err != nil {
		return nil, err
	}

	// 4. If state was changed, call repository to dock via API
	if stateChanged {
		if err := h.shipRepo.Dock(ctx, ship, cmd.PlayerID); err != nil {
			return nil, fmt.Errorf("failed to dock ship: %w", err)
		}
	}

	// 5. Call API to negotiate contract
	result, err := h.apiClient.NegotiateContract(ctx, cmd.ShipSymbol, player.Token)

	// 6. Handle error 4511 - agent already has active contract
	// Note: API client now parses JSON before checking status, so result.ErrorCode is populated even on errors
	if result != nil && result.ErrorCode == 4511 {
		// Fetch existing contract from API
		existingContractData, err := h.apiClient.GetContract(ctx, result.ExistingContractID, player.Token)
		if err != nil {
			return nil, fmt.Errorf("failed to get existing contract: %w", err)
		}

		// Convert to domain entity
		existingContract := h.convertToDomain(existingContractData, cmd.PlayerID)

		// Save to database
		if err := h.contractRepo.Save(ctx, existingContract); err != nil {
			return nil, fmt.Errorf("failed to save existing contract: %w", err)
		}

		return &NegotiateContractResponse{
			Contract:      existingContract,
			WasNegotiated: false,
		}, nil
	}

	// 7. Convert new contract to domain entity
	newContract := h.convertToDomain(result.Contract, cmd.PlayerID)

	// 8. Save contract to database
	if err := h.contractRepo.Save(ctx, newContract); err != nil {
		return nil, fmt.Errorf("failed to save contract: %w", err)
	}

	return &NegotiateContractResponse{
		Contract:      newContract,
		WasNegotiated: true,
	}, nil
}

// convertToDomain converts API contract data to domain entity
func (h *NegotiateContractHandler) convertToDomain(data *infraPorts.ContractData, playerID int) *contract.Contract {
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
	terms := contract.ContractTerms{
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
