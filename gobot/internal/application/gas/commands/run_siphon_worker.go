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

// RunSiphonWorkerCommand orchestrates continuous gas siphoning with storage ship buffering.
// Siphon ship siphons until cargo is full, transfers to storage ship, then resumes siphoning.
// Transport/delivery is handled by manufacturing pool via STORAGE_ACQUIRE_DELIVER tasks.
type RunSiphonWorkerCommand struct {
	ShipSymbol         string
	PlayerID           shared.PlayerID
	GasGiant           string // Waypoint symbol of gas giant
	CoordinatorID      string // Parent coordinator container ID
	StorageOperationID string // Storage operation ID for finding storage ships
}

// RunSiphonWorkerResponse contains siphoning execution results
type RunSiphonWorkerResponse struct {
	SiphonCount           int
	TransferCount         int
	TotalUnitsTransferred int
	Error                 string
}

// RunSiphonWorkerHandler implements the siphon worker workflow
type RunSiphonWorkerHandler struct {
	mediator           common.Mediator
	shipRepo           navigation.ShipRepository
	shipAssignmentRepo container.ShipAssignmentRepository
	storageCoordinator storage.StorageCoordinator
	clock              shared.Clock
}

// NewRunSiphonWorkerHandler creates a new siphon worker handler
func NewRunSiphonWorkerHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	shipAssignmentRepo container.ShipAssignmentRepository,
	storageCoordinator storage.StorageCoordinator,
	clock shared.Clock,
) *RunSiphonWorkerHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RunSiphonWorkerHandler{
		mediator:           mediator,
		shipRepo:           shipRepo,
		shipAssignmentRepo: shipAssignmentRepo,
		storageCoordinator: storageCoordinator,
		clock:              clock,
	}
}

// Handle executes the siphon worker command
func (h *RunSiphonWorkerHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*RunSiphonWorkerCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	result := &RunSiphonWorkerResponse{
		SiphonCount:           0,
		TransferCount:         0,
		TotalUnitsTransferred: 0,
		Error:                 "",
	}

	// Execute continuous siphoning workflow
	if err := h.executeSiphoning(ctx, cmd, result); err != nil {
		result.Error = err.Error()
		return result, err
	}

	return result, nil
}

// executeSiphoning handles the main siphoning workflow with transport-as-sink pattern
func (h *RunSiphonWorkerHandler) executeSiphoning(
	ctx context.Context,
	cmd *RunSiphonWorkerCommand,
	result *RunSiphonWorkerResponse,
) error {
	logger := common.LoggerFromContext(ctx)

	// 1. Load ship and check current location
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return fmt.Errorf("failed to load ship: %w", err)
	}

	// 2. Navigate to gas giant if not there
	if ship.CurrentLocation().Symbol != cmd.GasGiant {
		logger.Log("INFO", "Siphon ship navigating to gas giant", map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol,
			"action":      "navigate_to_gas_giant",
			"destination": cmd.GasGiant,
		})

		navCmd := &appShipCmd.NavigateRouteCommand{
			ShipSymbol:  cmd.ShipSymbol,
			Destination: cmd.GasGiant,
			PlayerID:    cmd.PlayerID,
		}

		_, err := h.mediator.Send(ctx, navCmd)
		if err != nil {
			return fmt.Errorf("failed to navigate to gas giant: %w", err)
		}

		// Reload ship after navigation
		ship, err = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
		if err != nil {
			return fmt.Errorf("failed to reload ship after navigation: %w", err)
		}
	}

	logger.Log("INFO", "Siphon ship continuous siphoning started", map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol,
		"action":      "start_siphoning",
		"gas_giant":   cmd.GasGiant,
	})

	// 3. Main siphoning loop - runs indefinitely until context cancelled
	for {
		// Check for context cancellation
		select {
		case <-ctx.Done():
			logger.Log("INFO", "Siphoning operation cancelled", map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol,
				"action":      "siphoning_cancelled",
				"siphons":     result.SiphonCount,
			})
			return ctx.Err()
		default:
		}

		// 3a. Siphon resources
		siphonCmd := &SiphonResourcesCommand{
			ShipSymbol: cmd.ShipSymbol,
			PlayerID:   cmd.PlayerID,
		}

		siphonResp, err := h.mediator.Send(ctx, siphonCmd)
		if err != nil {
			return fmt.Errorf("failed to siphon resources: %w", err)
		}

		siphon := siphonResp.(*SiphonResourcesResponse)
		result.SiphonCount++

		logger.Log("INFO", "Gas siphoned successfully", map[string]interface{}{
			"ship_symbol":  cmd.ShipSymbol,
			"action":       "siphon_resources",
			"yield_units":  siphon.YieldUnits,
			"yield_symbol": siphon.YieldSymbol,
			"siphon_count": result.SiphonCount,
		})

		// 3b. Wait for cooldown
		if siphon.CooldownDuration > 0 {
			h.clock.Sleep(siphon.CooldownDuration)
		}

		// 3c. Reload ship to get updated cargo
		ship, err = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
		if err != nil {
			return fmt.Errorf("failed to reload ship: %w", err)
		}

		// 3d. Check if cargo is full - transfer to storage ship
		if ship.IsCargoFull() {
			logger.Log("INFO", "Siphon ship cargo full - depositing to storage ship", map[string]interface{}{
				"ship_symbol":    cmd.ShipSymbol,
				"action":         "deposit_to_storage",
				"cargo_units":    ship.Cargo().Units,
				"cargo_capacity": ship.Cargo().Capacity,
			})

			// Deposit ALL cargo to storage ships
			unitsTransferred, err := h.depositToStorageShip(ctx, cmd, ship)
			if err != nil {
				return fmt.Errorf("failed to deposit to storage ship: %w", err)
			}

			result.TransferCount++
			result.TotalUnitsTransferred += unitsTransferred

			logger.Log("INFO", "Cargo deposited to storage ship successfully", map[string]interface{}{
				"ship_symbol":       cmd.ShipSymbol,
				"action":            "deposit_complete",
				"units_transferred": unitsTransferred,
				"transfer_count":    result.TransferCount,
			})
		}
	}
}

// depositToStorageShip finds a storage ship with space and transfers all cargo to it.
// After the API transfer, notifies the StorageCoordinator to wake waiting haulers.
func (h *RunSiphonWorkerHandler) depositToStorageShip(
	ctx context.Context,
	cmd *RunSiphonWorkerCommand,
	ship *navigation.Ship,
) (int, error) {
	logger := common.LoggerFromContext(ctx)

	totalTransferred := 0

	// Transfer ALL cargo items to storage ships
	for _, item := range ship.Cargo().Inventory {
		if item.Units <= 0 {
			continue
		}

		unitsRemaining := item.Units

		// Keep finding storage ships until all cargo is deposited
		for unitsRemaining > 0 {
			// Find a storage ship with available space
			storageShip, found := h.storageCoordinator.FindStorageShipWithSpace(
				cmd.StorageOperationID,
				1, // At least 1 unit of space
			)
			if !found {
				logger.Log("WARNING", "No storage ship with space available - waiting", map[string]interface{}{
					"ship_symbol":     cmd.ShipSymbol,
					"action":          "no_storage_space",
					"good":            item.Symbol,
					"units_remaining": unitsRemaining,
				})
				// Wait a bit and retry
				h.clock.Sleep(5 * 1000 * 1000 * 1000) // 5 seconds in nanoseconds
				continue
			}

			// Calculate how much to transfer
			availableSpace := storageShip.AvailableSpace()
			unitsToTransfer := unitsRemaining
			if unitsToTransfer > availableSpace {
				unitsToTransfer = availableSpace
			}

			logger.Log("INFO", "Depositing cargo to storage ship", map[string]interface{}{
				"ship_symbol":     cmd.ShipSymbol,
				"action":          "deposit_cargo",
				"storage_ship":    storageShip.ShipSymbol(),
				"good":            item.Symbol,
				"units":           unitsToTransfer,
				"available_space": availableSpace,
			})

			// Transfer cargo via API
			transferCmd := &TransferCargoCommand{
				FromShip:   cmd.ShipSymbol,
				ToShip:     storageShip.ShipSymbol(),
				GoodSymbol: item.Symbol,
				Units:      unitsToTransfer,
				PlayerID:   cmd.PlayerID,
			}

			_, err := h.mediator.Send(ctx, transferCmd)
			if err != nil {
				logger.Log("ERROR", "Failed to transfer cargo to storage ship", map[string]interface{}{
					"ship_symbol":  cmd.ShipSymbol,
					"action":       "transfer_error",
					"storage_ship": storageShip.ShipSymbol(),
					"good":         item.Symbol,
					"units":        unitsToTransfer,
					"error":        err.Error(),
				})
				return totalTransferred, fmt.Errorf("failed to transfer %s to storage: %w", item.Symbol, err)
			}

			// Notify coordinator of the deposit - this updates inventory and wakes waiting haulers
			h.storageCoordinator.NotifyCargoDeposited(
				storageShip.ShipSymbol(),
				item.Symbol,
				unitsToTransfer,
			)

			totalTransferred += unitsToTransfer
			unitsRemaining -= unitsToTransfer

			logger.Log("INFO", "Cargo deposited to storage ship successfully", map[string]interface{}{
				"ship_symbol":     cmd.ShipSymbol,
				"action":          "deposit_success",
				"storage_ship":    storageShip.ShipSymbol(),
				"good":            item.Symbol,
				"units":           unitsToTransfer,
				"units_remaining": unitsRemaining,
			})
		}
	}

	return totalTransferred, nil
}
