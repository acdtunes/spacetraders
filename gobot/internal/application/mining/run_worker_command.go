package mining

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appShip "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// TransferComplete signals that a miner has completed transferring cargo to a transport
type TransferComplete struct {
	MinerSymbol     string
	TransportSymbol string
}

// RunWorkerCommand orchestrates continuous mining with transport-as-sink pattern
// Miner mines until cargo is full, requests transport, transfers cargo, then resumes mining
type RunWorkerCommand struct {
	ShipSymbol           string
	PlayerID             shared.PlayerID
	AsteroidField        string                  // Waypoint symbol of asteroid
	TopNOres             int                     // Deprecated: no longer used, threshold hardcoded to 50
	CoordinatorID        string                  // Parent coordinator container ID
	TransportRequestChan chan<- string           // Send miner symbol to request transport
	TransportAssignChan  <-chan string           // Receive assigned transport symbol
	TransferCompleteChan chan<- TransferComplete // Signal transfer completion to coordinator
}

// RunWorkerResponse contains mining execution results
type RunWorkerResponse struct {
	ExtractionCount int
	TransferCount   int
	TotalUnitsTransferred int
	Error           string
}

// RunWorkerHandler implements the mining worker workflow
type RunWorkerHandler struct {
	mediator           common.Mediator
	shipRepo           navigation.ShipRepository
	shipAssignmentRepo container.ShipAssignmentRepository
	clock              shared.Clock
}

// NewRunWorkerHandler creates a new mining worker handler
func NewRunWorkerHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	shipAssignmentRepo container.ShipAssignmentRepository,
	clock shared.Clock,
) *RunWorkerHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RunWorkerHandler{
		mediator:           mediator,
		shipRepo:           shipRepo,
		shipAssignmentRepo: shipAssignmentRepo,
		clock:              clock,
	}
}

// Handle executes the mining worker command
func (h *RunWorkerHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*RunWorkerCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	result := &RunWorkerResponse{
		ExtractionCount:       0,
		TransferCount:         0,
		TotalUnitsTransferred: 0,
		Error:                 "",
	}

	// Execute continuous mining workflow
	if err := h.executeMining(ctx, cmd, result); err != nil {
		result.Error = err.Error()
		return result, err
	}

	return result, nil
}

// executeMining handles the main mining workflow with transport-as-sink pattern
func (h *RunWorkerHandler) executeMining(
	ctx context.Context,
	cmd *RunWorkerCommand,
	result *RunWorkerResponse,
) error {
	logger := common.LoggerFromContext(ctx)

	// 1. Load ship and check current location
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return fmt.Errorf("failed to load ship: %w", err)
	}

	// 2. Navigate to asteroid field if not there
	if ship.CurrentLocation().Symbol != cmd.AsteroidField {
		logger.Log("INFO", "Miner navigating to asteroid field", map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol,
			"action":      "navigate_to_asteroid",
			"destination": cmd.AsteroidField,
		})

		navCmd := &appShip.NavigateRouteCommand{
			ShipSymbol:  cmd.ShipSymbol,
			Destination: cmd.AsteroidField,
			PlayerID:    cmd.PlayerID,
		}

		_, err := h.mediator.Send(ctx, navCmd)
		if err != nil {
			return fmt.Errorf("failed to navigate to asteroid: %w", err)
		}

		// Reload ship after navigation
		ship, err = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
		if err != nil {
			return fmt.Errorf("failed to reload ship after navigation: %w", err)
		}
	}

	logger.Log("INFO", "Miner continuous mining started", map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol,
		"action":      "start_mining",
		"asteroid":    cmd.AsteroidField,
	})

	// 3. Main mining loop - runs indefinitely until context cancelled
	for {
		// Check for context cancellation
		select {
		case <-ctx.Done():
			logger.Log("INFO", "Mining operation cancelled", map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol,
				"action":      "mining_cancelled",
				"extractions": result.ExtractionCount,
			})
			return ctx.Err()
		default:
		}

		// 3a. Extract resources
		extractCmd := &ExtractResourcesCommand{
			ShipSymbol: cmd.ShipSymbol,
			PlayerID:   cmd.PlayerID,
		}

		extractResp, err := h.mediator.Send(ctx, extractCmd)
		if err != nil {
			return fmt.Errorf("failed to extract resources: %w", err)
		}

		extraction := extractResp.(*ExtractResourcesResponse)
		result.ExtractionCount++

		logger.Log("INFO", "Resources extracted successfully", map[string]interface{}{
			"ship_symbol":      cmd.ShipSymbol,
			"action":           "extract_resources",
			"yield_units":      extraction.YieldUnits,
			"yield_symbol":     extraction.YieldSymbol,
			"extraction_count": result.ExtractionCount,
		})

		// 3b. Wait for cooldown
		if extraction.CooldownDuration > 0 {
			h.clock.Sleep(extraction.CooldownDuration)
		}

		// 3c. Reload ship to get updated cargo
		ship, err = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
		if err != nil {
			return fmt.Errorf("failed to reload ship: %w", err)
		}

		// Check if cargo is full - only then do we jettison and potentially transfer
		if ship.IsCargoFull() {
			logger.Log("INFO", "Miner cargo full - evaluating for jettison", map[string]interface{}{
				"ship_symbol":   cmd.ShipSymbol,
				"action":        "cargo_full",
				"cargo_units":   ship.Cargo().Units,
				"cargo_capacity": ship.Cargo().Capacity,
			})

			// 3g. Evaluate cargo value and jettison low-value items only when full
			cargoItems := make([]CargoItemValue, len(ship.Cargo().Inventory))
			for i, item := range ship.Cargo().Inventory {
				cargoItems[i] = CargoItemValue{
					Symbol: item.Symbol,
					Units:  item.Units,
					Price:  0, // Will be looked up
				}
			}

			evalQuery := &EvaluateCargoValueQuery{
				CargoItems:   cargoItems,
				MinPriceThreshold: 50, // Jettison ores < 50 credits/unit,
				SystemSymbol: ship.CurrentLocation().SystemSymbol,
				PlayerID:     cmd.PlayerID.Value(),
			}

			evalResp, err := h.mediator.Send(ctx, evalQuery)
			if err != nil {
				// Log warning but continue - we'll keep all cargo if we can't evaluate
				logger.Log("WARNING", fmt.Sprintf("Failed to evaluate cargo value: %v", err), nil)
			} else {
				evalResult := evalResp.(*EvaluateCargoValueResponse)

				// Jettison low-value items to make space
				for _, item := range evalResult.JettisonItems {
					jettisonCmd := &appShip.JettisonCargoCommand{
						ShipSymbol: cmd.ShipSymbol,
						PlayerID:   cmd.PlayerID,
						GoodSymbol: item.Symbol,
						Units:      item.Units,
					}

					_, err := h.mediator.Send(ctx, jettisonCmd)
					if err != nil {
						logger.Log("WARNING", "Cargo jettison failed", map[string]interface{}{
							"ship_symbol":  cmd.ShipSymbol,
							"action":       "jettison",
							"cargo_symbol": item.Symbol,
							"error":        err.Error(),
						})
					} else {
						logger.Log("INFO", "Low-value cargo jettisoned", map[string]interface{}{
							"ship_symbol":  cmd.ShipSymbol,
							"action":       "jettison",
							"cargo_symbol": item.Symbol,
							"units":        item.Units,
						})
					}
				}
			}

			// 3h. Reload ship and check if still full (with only valuable cargo)
			ship, err = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
			if err != nil {
				return fmt.Errorf("failed to reload ship after jettison: %w", err)
			}

			// If still full after jettisoning, transfer to transport
			if ship.IsCargoFull() {
				logger.Log("INFO", "Miner still full with valuable cargo - requesting transport", map[string]interface{}{
					"ship_symbol":    cmd.ShipSymbol,
					"action":         "request_transport",
					"cargo_units":    ship.Cargo().Units,
					"cargo_capacity": ship.Cargo().Capacity,
				})

				// 3i. Request transport and transfer what fits
				unitsTransferred, err := h.requestAndTransferToTransport(ctx, cmd, ship)
				if err != nil {
					return fmt.Errorf("failed to transfer to transport: %w", err)
				}

				result.TransferCount++
				result.TotalUnitsTransferred += unitsTransferred

				logger.Log("INFO", "Cargo transferred to transport successfully", map[string]interface{}{
					"ship_symbol":      cmd.ShipSymbol,
					"action":           "transfer_complete",
					"units_transferred": unitsTransferred,
					"transfer_count":   result.TransferCount,
				})
			}

			// Continue mining - miner may still have some cargo but has space now
			continue
		}
	}
}

// requestAndTransferToTransport requests a transport and transfers all cargo to it
func (h *RunWorkerHandler) requestAndTransferToTransport(
	ctx context.Context,
	cmd *RunWorkerCommand,
	ship *navigation.Ship,
) (int, error) {
	logger := common.LoggerFromContext(ctx)

	// Send request for transport
	logger.Log("INFO", "Miner requesting transport", map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol,
		"action":      "request_transport",
	})

	select {
	case cmd.TransportRequestChan <- cmd.ShipSymbol:
		// Request sent
	case <-ctx.Done():
		return 0, ctx.Err()
	}

	// Wait for transport assignment
	var transportSymbol string
	select {
	case transportSymbol = <-cmd.TransportAssignChan:
		logger.Log("INFO", "Transport assigned to miner", map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol,
			"action":      "transport_assigned",
			"transport":   transportSymbol,
		})
	case <-ctx.Done():
		return 0, ctx.Err()
	}

	// Load transport ship to check available space
	transportShip, err := h.shipRepo.FindBySymbol(ctx, transportSymbol, cmd.PlayerID)
	if err != nil {
		return 0, fmt.Errorf("failed to load transport ship: %w", err)
	}

	// Transfer cargo to transport, respecting available space
	totalTransferred := 0
	for _, item := range ship.Cargo().Inventory {
		// Check how much space is available
		availableSpace := transportShip.AvailableCargoSpace()
		if availableSpace == 0 {
			logger.Log("INFO", "Transport cargo full - stopping transfer", map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol,
				"action":      "transfer_stopped",
				"transport":   transportSymbol,
			})
			break
		}

		// Transfer only what fits
		unitsToTransfer := item.Units
		if unitsToTransfer > availableSpace {
			unitsToTransfer = availableSpace
			logger.Log("INFO", "Transferring partial cargo to transport", map[string]interface{}{
				"ship_symbol":     cmd.ShipSymbol,
				"action":          "partial_transfer",
				"cargo_symbol":    item.Symbol,
				"units_partial":   unitsToTransfer,
				"units_total":     item.Units,
				"available_space": availableSpace,
			})
		}

		transferCmd := &TransferCargoCommand{
			FromShip:   cmd.ShipSymbol,
			ToShip:     transportSymbol,
			GoodSymbol: item.Symbol,
			Units:      unitsToTransfer,
			PlayerID:   cmd.PlayerID,
		}

		_, err := h.mediator.Send(ctx, transferCmd)
		if err != nil {
			logger.Log("WARNING", "Cargo transfer failed", map[string]interface{}{
				"ship_symbol":  cmd.ShipSymbol,
				"action":       "transfer",
				"cargo_symbol": item.Symbol,
				"transport":    transportSymbol,
				"error":        err.Error(),
			})
			continue
		}

		totalTransferred += unitsToTransfer
		logger.Log("INFO", "Cargo transferred to transport", map[string]interface{}{
			"ship_symbol":  cmd.ShipSymbol,
			"action":       "transfer",
			"cargo_symbol": item.Symbol,
			"units":        unitsToTransfer,
			"transport":    transportSymbol,
		})

		// Reload transport to get updated cargo space
		transportShip, err = h.shipRepo.FindBySymbol(ctx, transportSymbol, cmd.PlayerID)
		if err != nil {
			logger.Log("WARNING", "Failed to reload transport ship", map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol,
				"action":      "reload_transport",
				"transport":   transportSymbol,
				"error":       err.Error(),
			})
			break
		}
	}

	// Signal transfer completion to coordinator
	select {
	case cmd.TransferCompleteChan <- TransferComplete{
		MinerSymbol:     cmd.ShipSymbol,
		TransportSymbol: transportSymbol,
	}:
		// Transfer completion signal sent
	case <-ctx.Done():
		return totalTransferred, ctx.Err()
	}

	return totalTransferred, nil
}
