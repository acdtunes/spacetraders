package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appShipCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// RunStorageShipWorkerCommand manages a storage ship's lifecycle at a gas giant.
// It navigates the ship to the gas giant, registers with the storage coordinator,
// and keeps running until shutdown.
type RunStorageShipWorkerCommand struct {
	ShipSymbol         string
	PlayerID           shared.PlayerID
	GasGiant           string // Waypoint symbol of gas giant
	CoordinatorID      string // Parent coordinator container ID
	StorageOperationID string // Storage operation ID
}

// RunStorageShipWorkerResponse contains storage ship worker results
type RunStorageShipWorkerResponse struct {
	ShipSymbol string
	Location   string
	Error      string
}

// RunStorageShipWorkerHandler implements the storage ship worker workflow
type RunStorageShipWorkerHandler struct {
	mediator           common.Mediator
	shipRepo           navigation.ShipRepository
	shipAssignmentRepo container.ShipAssignmentRepository
	storageCoordinator storage.StorageCoordinator
}

// NewRunStorageShipWorkerHandler creates a new storage ship worker handler
func NewRunStorageShipWorkerHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	shipAssignmentRepo container.ShipAssignmentRepository,
	storageCoordinator storage.StorageCoordinator,
) *RunStorageShipWorkerHandler {
	return &RunStorageShipWorkerHandler{
		mediator:           mediator,
		shipRepo:           shipRepo,
		shipAssignmentRepo: shipAssignmentRepo,
		storageCoordinator: storageCoordinator,
	}
}

// Handle executes the storage ship worker command
func (h *RunStorageShipWorkerHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*RunStorageShipWorkerCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	logger := common.LoggerFromContext(ctx)

	result := &RunStorageShipWorkerResponse{
		ShipSymbol: cmd.ShipSymbol,
	}

	// Step 1: Load ship
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		result.Error = fmt.Sprintf("failed to load ship: %v", err)
		return result, fmt.Errorf("failed to load ship: %w", err)
	}

	// Step 2: Navigate to gas giant if not already there
	if ship.CurrentLocation().Symbol != cmd.GasGiant {
		logger.Log("INFO", "Storage ship navigating to gas giant", map[string]interface{}{
			"action":      "navigate_to_gas_giant",
			"ship_symbol": cmd.ShipSymbol,
			"from":        ship.CurrentLocation().Symbol,
			"to":          cmd.GasGiant,
		})

		navCmd := &appShipCmd.NavigateRouteCommand{
			ShipSymbol:  cmd.ShipSymbol,
			Destination: cmd.GasGiant,
			PlayerID:    cmd.PlayerID,
		}

		navResp, err := h.mediator.Send(ctx, navCmd)
		if err != nil {
			logger.Log("WARNING", "Failed to navigate storage ship", map[string]interface{}{
				"action":      "navigate_failed",
				"ship_symbol": cmd.ShipSymbol,
				"error":       err.Error(),
			})
			// Don't return error - we'll retry on next iteration or register where we are
		} else {
			// Use ship from navigation response (already up-to-date)
			ship = navResp.(*appShipCmd.NavigateRouteResponse).Ship
		}
	}

	result.Location = ship.CurrentLocation().Symbol

	// Step 3: Register with storage coordinator
	initialCargo := make(map[string]int)
	for _, item := range ship.Cargo().Inventory {
		initialCargo[item.Symbol] = item.Units
	}

	storageShip, err := storage.NewStorageShip(
		cmd.ShipSymbol,
		ship.CurrentLocation().Symbol, // Current location (might not be gas giant yet)
		cmd.StorageOperationID,
		ship.Cargo().Capacity,
		initialCargo,
	)
	if err != nil {
		result.Error = fmt.Sprintf("failed to create storage ship entity: %v", err)
		return result, fmt.Errorf("failed to create storage ship entity: %w", err)
	}

	if err := h.storageCoordinator.RegisterStorageShip(storageShip); err != nil {
		logger.Log("WARNING", "Storage ship may already be registered", map[string]interface{}{
			"action":      "register_storage_ship",
			"ship_symbol": cmd.ShipSymbol,
			"error":       err.Error(),
		})
		// Continue - ship might already be registered from recovery
	}

	logger.Log("INFO", "Storage ship registered and ready", map[string]interface{}{
		"action":         "storage_ship_ready",
		"ship_symbol":    cmd.ShipSymbol,
		"location":       ship.CurrentLocation().Symbol,
		"cargo_capacity": ship.Cargo().Capacity,
		"current_cargo":  ship.Cargo().Units,
	})

	// Step 4: Subscribe to cargo deposit notifications
	// This eliminates the need for periodic API polling to check for HYDROCARBON
	depositNotifications, unsubscribe := h.storageCoordinator.SubscribeToDeposits(cmd.ShipSymbol)
	defer unsubscribe()

	// Check if we already have HYDROCARBON from initial cargo (recovery scenario)
	if initialCargo["HYDROCARBON"] > 0 {
		h.jettisonHydrocarbonUnits(ctx, cmd, logger, initialCargo["HYDROCARBON"])
	}

	// Step 5: Wait for shutdown or HYDROCARBON deposits
	for {
		select {
		case <-ctx.Done():
			// Cleanup: Unregister from coordinator
			h.storageCoordinator.UnregisterStorageShip(cmd.ShipSymbol)

			logger.Log("INFO", "Storage ship worker shutdown", map[string]interface{}{
				"action":      "shutdown",
				"ship_symbol": cmd.ShipSymbol,
			})

			return result, ctx.Err()

		case notification := <-depositNotifications:
			// Only jettison HYDROCARBON - it's a worthless byproduct that wastes cargo space
			if notification.GoodSymbol == "HYDROCARBON" && notification.Units > 0 {
				h.jettisonHydrocarbonUnits(ctx, cmd, logger, notification.Units)
			}
		}
	}
}

// jettisonHydrocarbonUnits jettisons a known amount of HYDROCARBON from the storage ship.
// HYDROCARBON is a worthless byproduct from gas siphoning that wastes cargo space.
// This is called reactively when HYDROCARBON is deposited (no polling/API call needed).
func (h *RunStorageShipWorkerHandler) jettisonHydrocarbonUnits(
	ctx context.Context,
	cmd *RunStorageShipWorkerCommand,
	logger common.ContainerLogger,
	units int,
) {
	logger.Log("INFO", "Jettisoning HYDROCARBON byproduct from storage ship", map[string]interface{}{
		"action":      "jettison_hydrocarbon",
		"ship_symbol": cmd.ShipSymbol,
		"units":       units,
	})

	jettisonCmd := &appShipCmd.JettisonCargoCommand{
		ShipSymbol: cmd.ShipSymbol,
		GoodSymbol: "HYDROCARBON",
		Units:      units,
		PlayerID:   cmd.PlayerID,
	}

	_, err := h.mediator.Send(ctx, jettisonCmd)
	if err != nil {
		logger.Log("WARNING", "Failed to jettison HYDROCARBON from storage ship", map[string]interface{}{
			"action":      "jettison_error",
			"ship_symbol": cmd.ShipSymbol,
			"units":       units,
			"error":       err.Error(),
		})
	} else {
		// Notify the coordinator that cargo was jettisoned so it updates AvailableSpace()
		h.storageCoordinator.NotifyCargoJettisoned(cmd.ShipSymbol, "HYDROCARBON", units)

		logger.Log("INFO", "Jettisoned HYDROCARBON from storage ship", map[string]interface{}{
			"action":      "jettison_success",
			"ship_symbol": cmd.ShipSymbol,
			"units":       units,
		})
	}
}
