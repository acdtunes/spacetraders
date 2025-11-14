package ship

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	infraPorts "github.com/andrescamacho/spacetraders-go/internal/infrastructure/ports"
)

// JettisonCargoCommand - Command to jettison cargo from a ship
type JettisonCargoCommand struct {
	ShipSymbol string
	PlayerID   int
	GoodSymbol string
	Units      int
}

// JettisonCargoResponse - Response from jettison cargo command
type JettisonCargoResponse struct {
	UnitsJettisoned int
}

// JettisonCargoHandler - Handles jettison cargo commands
type JettisonCargoHandler struct {
	shipRepo   navigation.ShipRepository
	playerRepo player.PlayerRepository
	apiClient  infraPorts.APIClient
}

// NewJettisonCargoHandler creates a new jettison cargo handler
func NewJettisonCargoHandler(
	shipRepo navigation.ShipRepository,
	playerRepo player.PlayerRepository,
	apiClient infraPorts.APIClient,
) *JettisonCargoHandler {
	return &JettisonCargoHandler{
		shipRepo:   shipRepo,
		playerRepo: playerRepo,
		apiClient:  apiClient,
	}
}

// Handle executes the jettison cargo command
func (h *JettisonCargoHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*JettisonCargoCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	// 1. Load player to get token
	player, err := h.playerRepo.FindByID(ctx, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("player not found: %w", err)
	}

	// 2. Load ship from repository
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("ship not found: %w", err)
	}

	// 3. Validate ship has enough cargo
	currentUnits := ship.Cargo().GetItemUnits(cmd.GoodSymbol)
	if currentUnits < cmd.Units {
		return nil, fmt.Errorf("insufficient cargo: have %d units of %s, need %d", currentUnits, cmd.GoodSymbol, cmd.Units)
	}

	// 4. Ensure ship is in orbit (auto-orbit if needed)
	stateChanged, err := ship.EnsureInOrbit()
	if err != nil {
		return nil, err
	}

	// 5. If state was changed, call repository to orbit via API
	if stateChanged {
		if err := h.shipRepo.Orbit(ctx, ship, cmd.PlayerID); err != nil {
			return nil, fmt.Errorf("failed to orbit ship: %w", err)
		}
	}

	// 6. Call API to jettison cargo
	if err := h.apiClient.JettisonCargo(ctx, cmd.ShipSymbol, cmd.GoodSymbol, cmd.Units, player.Token); err != nil {
		return nil, fmt.Errorf("failed to jettison cargo: %w", err)
	}

	return &JettisonCargoResponse{
		UnitsJettisoned: cmd.Units,
	}, nil
}
