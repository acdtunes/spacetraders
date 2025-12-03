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

	// 3. If ship is IN_TRANSIT, wait for it to arrive first
	// This handles ships that were mid-navigation when daemon restarted
	if ship.NavStatus() == navigation.NavStatusInTransit {
		if err := h.waitForShipArrival(ctx, ship, cmd.PlayerID); err != nil {
			return nil, fmt.Errorf("failed to wait for ship arrival: %w", err)
		}
	}

	// 4. Ensure ship is in orbit (required for siphoning)
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

	// 6. Call API to siphon resources
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

// waitForShipArrival waits for a ship in transit to complete its journey.
// This handles ships that were mid-navigation when daemon restarted.
func (h *SiphonResourcesHandler) waitForShipArrival(
	ctx context.Context,
	ship *navigation.Ship,
	playerID shared.PlayerID,
) error {
	logger := common.LoggerFromContext(ctx)

	logger.Log("INFO", "Siphon ship in transit - waiting for arrival", map[string]interface{}{
		"ship_symbol": ship.ShipSymbol(),
		"action":      "wait_transit_arrival",
	})

	// Use DB arrival time (DB is source of truth after daemon startup)
	var waitTime time.Duration
	if ship.ArrivalTime() != nil {
		waitTime = time.Until(*ship.ArrivalTime())
	}

	// Wait if we have positive wait time
	if waitTime > 0 {
		// Add 3 second buffer for API lag
		totalWait := waitTime + 3*time.Second
		logger.Log("INFO", "Waiting for siphon ship to complete transit", map[string]interface{}{
			"ship_symbol":  ship.ShipSymbol(),
			"action":       "wait_transit",
			"wait_seconds": int(totalWait.Seconds()),
		})
		time.Sleep(totalWait)
	}

	// After sleeping for arrival + buffer, trust that ship has arrived
	// Force domain state to IN_ORBIT
	if ship.NavStatus() == navigation.NavStatusInTransit {
		logger.Log("INFO", "Trusting arrival time - marking siphon ship as arrived", map[string]interface{}{
			"ship_symbol": ship.ShipSymbol(),
			"action":      "trust_arrival",
		})
		if err := ship.Arrive(); err != nil {
			return fmt.Errorf("failed to mark ship as arrived: %w", err)
		}

		// Clear arrival time and persist ship state
		ship.ClearArrivalTime()
		if err := h.shipRepo.Save(ctx, ship); err != nil {
			logger.Log("WARNING", "Failed to persist ship state after transit wait", map[string]interface{}{
				"ship_symbol": ship.ShipSymbol(),
				"error":       err.Error(),
			})
		}
	}

	return nil
}
