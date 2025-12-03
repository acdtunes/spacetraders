package cargo

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

// JettisonCargoCommand - Command to jettison cargo from a ship
type JettisonCargoCommand struct {
	ShipSymbol string
	PlayerID   shared.PlayerID
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
	apiClient  domainPorts.APIClient
}

// NewJettisonCargoHandler creates a new jettison cargo handler
func NewJettisonCargoHandler(
	shipRepo navigation.ShipRepository,
	playerRepo player.PlayerRepository,
	apiClient domainPorts.APIClient,
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

	token, err := h.getPlayerToken(ctx)
	if err != nil {
		return nil, err
	}

	ship, err := h.loadShip(ctx, cmd)
	if err != nil {
		return nil, err
	}

	if err := h.validateSufficientCargo(ship, cmd); err != nil {
		return nil, err
	}

	if err := h.ensureShipInOrbitForJettison(ctx, ship, cmd.PlayerID); err != nil {
		return nil, err
	}

	if err := h.jettisonCargoViaAPI(ctx, cmd, token); err != nil {
		return nil, err
	}

	// Update ship cargo and persist to DB
	_ = ship.RemoveCargo(cmd.GoodSymbol, cmd.Units)
	_ = h.shipRepo.Save(ctx, ship)

	return &JettisonCargoResponse{
		UnitsJettisoned: cmd.Units,
	}, nil
}

func (h *JettisonCargoHandler) getPlayerToken(ctx context.Context) (string, error) {
	return common.PlayerTokenFromContext(ctx)
}

func (h *JettisonCargoHandler) loadShip(ctx context.Context, cmd *JettisonCargoCommand) (*navigation.Ship, error) {
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("ship not found: %w", err)
	}
	return ship, nil
}

func (h *JettisonCargoHandler) validateSufficientCargo(ship *navigation.Ship, cmd *JettisonCargoCommand) error {
	currentUnits := ship.Cargo().GetItemUnits(cmd.GoodSymbol)
	if currentUnits < cmd.Units {
		return fmt.Errorf("insufficient cargo: have %d units of %s, need %d", currentUnits, cmd.GoodSymbol, cmd.Units)
	}
	return nil
}

func (h *JettisonCargoHandler) ensureShipInOrbitForJettison(ctx context.Context, ship *navigation.Ship, playerID shared.PlayerID) error {
	stateChanged, err := ship.EnsureInOrbit()
	if err != nil {
		return err
	}

	if stateChanged {
		if err := h.shipRepo.Orbit(ctx, ship, playerID); err != nil {
			return fmt.Errorf("failed to orbit ship: %w", err)
		}
	}
	return nil
}

func (h *JettisonCargoHandler) jettisonCargoViaAPI(ctx context.Context, cmd *JettisonCargoCommand, token string) error {
	if err := h.apiClient.JettisonCargo(ctx, cmd.ShipSymbol, cmd.GoodSymbol, cmd.Units, token); err != nil {
		return fmt.Errorf("failed to jettison cargo: %w", err)
	}
	return nil
}
