package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

// ExtractResourcesCommand - Command to extract resources from an asteroid
type ExtractResourcesCommand struct {
	ShipSymbol string
	PlayerID   shared.PlayerID
}

// ExtractResourcesResponse - Response from extract resources command
type ExtractResourcesResponse struct {
	YieldSymbol      string
	YieldUnits       int
	CooldownDuration time.Duration
	Cargo            *navigation.CargoData
}

// ExtractResourcesHandler - Handles extract resources commands
type ExtractResourcesHandler struct {
	shipRepo   navigation.ShipRepository
	playerRepo player.PlayerRepository
	apiClient  domainPorts.APIClient
}

// NewExtractResourcesHandler creates a new extract resources handler
func NewExtractResourcesHandler(
	shipRepo navigation.ShipRepository,
	playerRepo player.PlayerRepository,
	apiClient domainPorts.APIClient,
) *ExtractResourcesHandler {
	return &ExtractResourcesHandler{
		shipRepo:   shipRepo,
		playerRepo: playerRepo,
		apiClient:  apiClient,
	}
}

// Handle executes the extract resources command
func (h *ExtractResourcesHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*ExtractResourcesCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	// 1. Get player token from context
	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return nil, err
	}

	// 2. Load ship from repository
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("ship not found: %w", err)
	}

	// 3. Ensure ship is in orbit (required for extraction)
	stateChanged, err := ship.EnsureInOrbit()
	if err != nil {
		return nil, err
	}

	// 4. If state was changed, call repository to orbit via API
	if stateChanged {
		if err := h.shipRepo.Orbit(ctx, ship, cmd.PlayerID); err != nil {
			return nil, fmt.Errorf("failed to orbit ship: %w", err)
		}
	}

	// 5. Call API to extract resources
	result, err := h.apiClient.ExtractResources(ctx, cmd.ShipSymbol, token)
	if err != nil {
		return nil, fmt.Errorf("failed to extract resources: %w", err)
	}

	return &ExtractResourcesResponse{
		YieldSymbol:      result.YieldSymbol,
		YieldUnits:       result.YieldUnits,
		CooldownDuration: time.Duration(result.CooldownSeconds) * time.Second,
		Cargo:            result.Cargo,
	}, nil
}
