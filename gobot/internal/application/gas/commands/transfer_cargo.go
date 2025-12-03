package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// TransferCargoCommand - Command to transfer cargo between ships at same waypoint
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
	shipRepo  navigation.ShipRepository
	apiClient domainPorts.APIClient
}

// NewTransferCargoHandler creates a new transfer cargo handler
func NewTransferCargoHandler(
	shipRepo navigation.ShipRepository,
	apiClient domainPorts.APIClient,
) *TransferCargoHandler {
	return &TransferCargoHandler{
		shipRepo:  shipRepo,
		apiClient: apiClient,
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

	// 2. Load source ship to verify cargo
	fromShip, err := h.shipRepo.FindBySymbol(ctx, cmd.FromShip, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("source ship not found: %w", err)
	}

	// 3. Load destination ship to verify space
	toShip, err := h.shipRepo.FindBySymbol(ctx, cmd.ToShip, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("destination ship not found: %w", err)
	}

	// 4. Verify ships are at same location
	if fromShip.CurrentLocation().Symbol != toShip.CurrentLocation().Symbol {
		return nil, fmt.Errorf("ships must be at the same location for transfer: %s at %s, %s at %s",
			cmd.FromShip, fromShip.CurrentLocation().Symbol,
			cmd.ToShip, toShip.CurrentLocation().Symbol)
	}

	// 5. Call API to transfer cargo
	result, err := h.apiClient.TransferCargo(ctx, cmd.FromShip, cmd.ToShip, cmd.GoodSymbol, cmd.Units, token)
	if err != nil {
		return nil, fmt.Errorf("failed to transfer cargo: %w", err)
	}

	// 6. Update ships' cargo state using domain methods
	_ = fromShip.RemoveCargo(cmd.GoodSymbol, result.UnitsTransferred)
	_ = h.shipRepo.Save(ctx, fromShip)

	_ = toShip.ReceiveCargo(&shared.CargoItem{Symbol: cmd.GoodSymbol, Units: result.UnitsTransferred})
	_ = h.shipRepo.Save(ctx, toShip)

	return &TransferCargoResponse{
		UnitsTransferred: result.UnitsTransferred,
		RemainingCargo:   result.RemainingCargo,
	}, nil
}
