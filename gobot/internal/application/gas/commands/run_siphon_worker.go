package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
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

	// 1. Load ship data ONCE - use for location check AND cooldown check (1 API call)
	shipData, err := h.shipRepo.GetShipData(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return fmt.Errorf("failed to load ship data: %w", err)
	}

	// Track cargo state from initial load
	cargoUnits := shipData.CargoUnits
	cargoCapacity := shipData.CargoCapacity

	// 2. Navigate to gas giant if not there
	if shipData.Location != cmd.GasGiant {
		logger.Log("INFO", "Siphon ship navigating to gas giant", map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol,
			"action":      "navigate_to_gas_giant",
			"destination": cmd.GasGiant,
		})

		// Need full Ship entity for navigation
		ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
		if err != nil {
			return fmt.Errorf("failed to load ship: %w", err)
		}

		navCmd := &shipNav.NavigateRouteCommand{
			ShipSymbol:  cmd.ShipSymbol,
			Destination: cmd.GasGiant,
			PlayerID:    cmd.PlayerID,
		}

		navResp, err := h.mediator.Send(ctx, navCmd)
		if err != nil {
			return fmt.Errorf("failed to navigate to gas giant: %w", err)
		}

		// Use ship from navigation response for updated cargo state
		ship = navResp.(*shipNav.NavigateRouteResponse).Ship
		cargoUnits = ship.Cargo().Units
		cargoCapacity = ship.Cargo().Capacity

		// Skip cooldown re-check after navigation:
		// 1. Navigation time is typically longer than siphon cooldowns
		// 2. Siphon command already handles cooldown errors with retry logic (lines 218-235)
		// This saves 1 API call per worker startup
		shipData.CooldownExpiration = "" // Clear cooldown - navigation time covered it
	}

	logger.Log("INFO", "Siphon ship continuous siphoning started", map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol,
		"action":      "start_siphoning",
		"gas_giant":   cmd.GasGiant,
	})

	// 2b. Check and wait for any existing cooldown from previous session
	// Uses shipData we already have (no extra API call if ship was already at gas giant)
	if err := h.waitForShipCooldownFromData(ctx, cmd, shipData); err != nil {
		return fmt.Errorf("failed to wait for ship cooldown: %w", err)
	}

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

		// OPTIMIZATION: Use local cargo tracking instead of API call every loop
		// The siphon response updates our yield, and we track deposits locally
		// This saves 1 GetShip API call per siphon cycle (every ~60s)

		// 3b. If cargo is full or nearly full, deposit to storage BEFORE siphoning
		// This handles ships that start with cargo from previous runs
		if cargoUnits >= cargoCapacity || cargoCapacity-cargoUnits < 1 {
			logger.Log("INFO", "Siphon ship cargo full - depositing to storage ship before siphoning", map[string]interface{}{
				"ship_symbol":    cmd.ShipSymbol,
				"action":         "deposit_before_siphon",
				"cargo_units":    cargoUnits,
				"cargo_capacity": cargoCapacity,
			})

			// Load fresh ship data for deposit (need cargo inventory details)
			ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
			if err != nil {
				return fmt.Errorf("failed to load ship for deposit: %w", err)
			}

			unitsTransferred, err := h.depositToStorageShip(ctx, cmd, ship)
			if err != nil {
				return fmt.Errorf("failed to deposit to storage ship: %w", err)
			}

			result.TransferCount++
			result.TotalUnitsTransferred += unitsTransferred

			// OPTIMIZATION: Update local cargo tracking after deposit
			cargoUnits = 0 // All cargo was transferred

			logger.Log("INFO", "Cargo deposited before siphoning", map[string]interface{}{
				"ship_symbol":       cmd.ShipSymbol,
				"action":            "deposit_complete",
				"units_transferred": unitsTransferred,
			})
		}

		// 3c. Siphon resources (with cooldown retry)
		siphonCmd := &SiphonResourcesCommand{
			ShipSymbol: cmd.ShipSymbol,
			PlayerID:   cmd.PlayerID,
		}

		siphonResp, err := h.mediator.Send(ctx, siphonCmd)
		if err != nil {
			// Check if this is a cooldown error - if so, wait and retry
			cooldownDuration := parseCooldownFromError(err)
			if cooldownDuration > 0 {
				logger.Log("INFO", "Ship on cooldown, waiting before retry", map[string]interface{}{
					"ship_symbol":      cmd.ShipSymbol,
					"action":           "cooldown_wait",
					"cooldown_seconds": int(cooldownDuration.Seconds()),
				})
				h.clock.Sleep(cooldownDuration)

				// Retry siphon after cooldown
				siphonResp, err = h.mediator.Send(ctx, siphonCmd)
				if err != nil {
					return fmt.Errorf("failed to siphon resources after cooldown: %w", err)
				}
			} else {
				return fmt.Errorf("failed to siphon resources: %w", err)
			}
		}

		siphon := siphonResp.(*SiphonResourcesResponse)
		result.SiphonCount++

		// OPTIMIZATION: Update local cargo tracking with siphon yield
		cargoUnits += siphon.YieldUnits

		logger.Log("INFO", "Gas siphoned successfully", map[string]interface{}{
			"ship_symbol":  cmd.ShipSymbol,
			"action":       "siphon_resources",
			"yield_units":  siphon.YieldUnits,
			"yield_symbol": siphon.YieldSymbol,
			"siphon_count": result.SiphonCount,
			"cargo_units":  cargoUnits,
		})

		// 3d. Wait for cooldown then loop back
		// Cargo full check happens at start of next iteration (step 3b)
		if siphon.CooldownDuration > 0 {
			h.clock.Sleep(siphon.CooldownDuration)
		}
	}
}

// depositToStorageShip finds a storage ship with space and transfers all cargo to it.
// After the API transfer, notifies the StorageCoordinator to wake waiting haulers.
// HYDROCARBON is transferred but NOT tracked (storage ship worker handles cleanup).
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
			// For HYDROCARBON, this triggers the storage ship worker to jettison it
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

// waitForShipCooldownFromData checks if the ship has an active cooldown and waits for it to expire.
// This is called at the start of siphoning to handle cooldowns from previous sessions.
// Takes pre-fetched ShipData to avoid an extra API call.
func (h *RunSiphonWorkerHandler) waitForShipCooldownFromData(
	ctx context.Context,
	cmd *RunSiphonWorkerCommand,
	shipData *navigation.ShipData,
) error {
	logger := common.LoggerFromContext(ctx)

	// If no cooldown, nothing to wait for
	if shipData.CooldownExpiration == "" {
		return nil
	}

	// Parse the cooldown expiration time
	cooldownExpires, err := time.Parse(time.RFC3339, shipData.CooldownExpiration)
	if err != nil {
		// If we can't parse, log and continue (don't fail)
		logger.Log("WARNING", "Could not parse cooldown expiration, proceeding anyway", map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol,
			"action":      "cooldown_parse_failed",
			"expiration":  shipData.CooldownExpiration,
		})
		return nil
	}

	// Calculate remaining time
	now := h.clock.Now()
	remaining := cooldownExpires.Sub(now)

	// If cooldown has expired, nothing to wait for
	if remaining <= 0 {
		return nil
	}

	// Add 1 second buffer to ensure cooldown has fully expired
	waitDuration := remaining + time.Second

	logger.Log("INFO", "Ship on cooldown from previous session, waiting proactively", map[string]interface{}{
		"ship_symbol":      cmd.ShipSymbol,
		"action":           "proactive_cooldown_wait",
		"cooldown_seconds": int(waitDuration.Seconds()),
		"expires_at":       cooldownExpires.Format(time.RFC3339),
	})

	h.clock.Sleep(waitDuration)
	return nil
}

// parseCooldownFromError extracts the remaining cooldown seconds from a cooldown error.
// Returns the cooldown duration if found, or 0 if not a cooldown error.
// Error format: API error (status 409): {"error":{"code":4000,"message":"...","data":{"cooldown":{"remainingSeconds":49,...}}}}
func parseCooldownFromError(err error) time.Duration {
	if err == nil {
		return 0
	}

	errStr := err.Error()

	// Check if this is a cooldown error (code 4000, status 409)
	if !strings.Contains(errStr, "cooldown") {
		return 0
	}

	// Find the JSON part of the error message
	jsonStart := strings.Index(errStr, "{")
	if jsonStart == -1 {
		return 0
	}

	jsonStr := errStr[jsonStart:]

	// Parse the JSON to extract remainingSeconds
	var apiErr struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    struct {
				Cooldown struct {
					RemainingSeconds int    `json:"remainingSeconds"`
					Expiration       string `json:"expiration"`
				} `json:"cooldown"`
			} `json:"data"`
		} `json:"error"`
	}

	if err := json.Unmarshal([]byte(jsonStr), &apiErr); err != nil {
		return 0
	}

	// Verify this is a cooldown error (code 4000)
	if apiErr.Error.Code != 4000 {
		return 0
	}

	remainingSeconds := apiErr.Error.Data.Cooldown.RemainingSeconds
	if remainingSeconds <= 0 {
		return 0
	}

	// Add 1 second buffer to ensure cooldown has fully expired
	return time.Duration(remainingSeconds+1) * time.Second
}
