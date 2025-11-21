package mining

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	infraPorts "github.com/andrescamacho/spacetraders-go/internal/infrastructure/ports"
)

// TransferCargoCommand - Command to transfer cargo between ships
type TransferCargoCommand struct {
	FromShip   string
	ToShip     string
	GoodSymbol string
	Units      int
	PlayerID   shared.PlayerID
}

// TransferCargoResponse - Response from transfer cargo command
type TransferCargoResponse struct {
	UnitsTransferred int
	RemainingCargo   *navigation.CargoData
}

// TransferCargoHandler - Handles transfer cargo commands
type TransferCargoHandler struct {
	shipRepo   navigation.ShipRepository
	playerRepo player.PlayerRepository
	apiClient  infraPorts.APIClient
}

// NewTransferCargoHandler creates a new transfer cargo handler
func NewTransferCargoHandler(
	shipRepo navigation.ShipRepository,
	playerRepo player.PlayerRepository,
	apiClient infraPorts.APIClient,
) *TransferCargoHandler {
	return &TransferCargoHandler{
		shipRepo:   shipRepo,
		playerRepo: playerRepo,
		apiClient:  apiClient,
	}
}

// Handle executes the transfer cargo command
func (h *TransferCargoHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*TransferCargoCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	// 1. Get player token from context
	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return nil, err
	}

	// 2. Load both ships from repository
	fromShip, err := h.shipRepo.FindBySymbol(ctx, cmd.FromShip, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("source ship not found: %w", err)
	}

	toShip, err := h.shipRepo.FindBySymbol(ctx, cmd.ToShip, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("destination ship not found: %w", err)
	}

	// 3. Validate both ships at same waypoint
	if fromShip.CurrentLocation().Symbol != toShip.CurrentLocation().Symbol {
		return nil, fmt.Errorf("ships must be at same waypoint: %s is at %s, %s is at %s",
			cmd.FromShip, fromShip.CurrentLocation().Symbol,
			cmd.ToShip, toShip.CurrentLocation().Symbol)
	}

	// 4. Validate source ship has enough cargo
	currentUnits := fromShip.Cargo().GetItemUnits(cmd.GoodSymbol)
	if currentUnits < cmd.Units {
		return nil, fmt.Errorf("insufficient cargo: have %d units of %s, need %d",
			currentUnits, cmd.GoodSymbol, cmd.Units)
	}

	// 5. Validate destination ship has enough space
	availableSpace := toShip.AvailableCargoSpace()
	if availableSpace < cmd.Units {
		return nil, fmt.Errorf("insufficient cargo space: %s has %d units available, need %d",
			cmd.ToShip, availableSpace, cmd.Units)
	}

	// 6. Ensure both ships are docked (required for transfer)
	if _, err := fromShip.EnsureDocked(); err != nil {
		return nil, fmt.Errorf("failed to ensure source ship docked: %w", err)
	}
	if err := h.shipRepo.Dock(ctx, fromShip, cmd.PlayerID); err != nil {
		return nil, fmt.Errorf("failed to dock source ship: %w", err)
	}

	if _, err := toShip.EnsureDocked(); err != nil {
		return nil, fmt.Errorf("failed to ensure destination ship docked: %w", err)
	}
	if err := h.shipRepo.Dock(ctx, toShip, cmd.PlayerID); err != nil {
		return nil, fmt.Errorf("failed to dock destination ship: %w", err)
	}

	// 7. Call API to transfer cargo
	result, err := h.apiClient.TransferCargo(ctx, cmd.FromShip, cmd.ToShip, cmd.GoodSymbol, cmd.Units, token)
	if err != nil {
		return nil, fmt.Errorf("failed to transfer cargo: %w", err)
	}

	return &TransferCargoResponse{
		UnitsTransferred: result.UnitsTransferred,
		RemainingCargo:   result.RemainingCargo,
	}, nil
}
