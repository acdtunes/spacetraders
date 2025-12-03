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

// SiphonResourcesCommand - Command to siphon gas from a gas giant
type SiphonResourcesCommand struct {
	ShipSymbol string
	PlayerID   shared.PlayerID
}

// SiphonResourcesResponse - Response from siphon resources command
type SiphonResourcesResponse struct {
	YieldSymbol      string
	YieldUnits       int
	CooldownDuration time.Duration
	Cargo            *navigation.CargoData
}

// SiphonResourcesHandler - Handles siphon resources commands
type SiphonResourcesHandler struct {
	shipRepo   navigation.ShipRepository
	playerRepo player.PlayerRepository
	apiClient  domainPorts.APIClient
}

// NewSiphonResourcesHandler creates a new siphon resources handler
func NewSiphonResourcesHandler(
	shipRepo navigation.ShipRepository,
	playerRepo player.PlayerRepository,
	apiClient domainPorts.APIClient,
) *SiphonResourcesHandler {
	return &SiphonResourcesHandler{
		shipRepo:   shipRepo,
		playerRepo: playerRepo,
		apiClient:  apiClient,
	}
}

// Handle executes the siphon resources command
func (h *SiphonResourcesHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*SiphonResourcesCommand)
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

	// 3. Ensure ship is in orbit (required for siphoning)
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

	// 5. Call API to siphon resources
	result, err := h.apiClient.SiphonResources(ctx, cmd.ShipSymbol, token)
	if err != nil {
		return nil, fmt.Errorf("failed to siphon resources: %w", err)
	}

	// 6. Persist updated cargo to database
	if result.Cargo != nil {
		// Convert CargoData to domain Cargo
		inventory := make([]*shared.CargoItem, len(result.Cargo.Inventory))
		for i := range result.Cargo.Inventory {
			inventory[i] = &result.Cargo.Inventory[i]
		}
		newCargo, err := shared.NewCargo(result.Cargo.Capacity, result.Cargo.Units, inventory)
		if err != nil {
			return nil, fmt.Errorf("failed to create cargo from API response: %w", err)
		}
		ship.SetCargo(newCargo)
		if err := h.shipRepo.Save(ctx, ship); err != nil {
			return nil, fmt.Errorf("failed to persist cargo after siphon: %w", err)
		}
	}

	return &SiphonResourcesResponse{
		YieldSymbol:      result.YieldSymbol,
		YieldUnits:       result.YieldUnits,
		CooldownDuration: time.Duration(result.CooldownSeconds) * time.Second,
		Cargo:            result.Cargo,
	}, nil
}
