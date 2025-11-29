package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/gas/ports"
	appShipCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// RunSiphonWorkerCommand orchestrates continuous gas siphoning with transport-as-sink pattern
// Siphon ship siphons until cargo is full, requests transport, transfers cargo, then resumes siphoning
type RunSiphonWorkerCommand struct {
	ShipSymbol    string
	PlayerID      shared.PlayerID
	GasGiant      string // Waypoint symbol of gas giant
	CoordinatorID string // Parent coordinator container ID
	Coordinator   ports.TransportCoordinator
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
	clock              shared.Clock
}

// NewRunSiphonWorkerHandler creates a new siphon worker handler
func NewRunSiphonWorkerHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	shipAssignmentRepo container.ShipAssignmentRepository,
	clock shared.Clock,
) *RunSiphonWorkerHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RunSiphonWorkerHandler{
		mediator:           mediator,
		shipRepo:           shipRepo,
		shipAssignmentRepo: shipAssignmentRepo,
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

		// 3d. Check if cargo is full - transfer to transport (no jettison for gas)
		if ship.IsCargoFull() {
			logger.Log("INFO", "Siphon ship cargo full - requesting transport", map[string]interface{}{
				"ship_symbol":    cmd.ShipSymbol,
				"action":         "request_transport",
				"cargo_units":    ship.Cargo().Units,
				"cargo_capacity": ship.Cargo().Capacity,
			})

			// Request transport and transfer ALL cargo (all gases are valuable)
			unitsTransferred, err := h.requestAndTransferToTransport(ctx, cmd, ship)
			if err != nil {
				return fmt.Errorf("failed to transfer to transport: %w", err)
			}

			result.TransferCount++
			result.TotalUnitsTransferred += unitsTransferred

			logger.Log("INFO", "Cargo transferred to transport successfully", map[string]interface{}{
				"ship_symbol":       cmd.ShipSymbol,
				"action":            "transfer_complete",
				"units_transferred": unitsTransferred,
				"transfer_count":    result.TransferCount,
			})
		}
	}
}

// requestAndTransferToTransport requests a transport and transfers all cargo to it
func (h *RunSiphonWorkerHandler) requestAndTransferToTransport(
	ctx context.Context,
	cmd *RunSiphonWorkerCommand,
	ship *navigation.Ship,
) (int, error) {
	logger := common.LoggerFromContext(ctx)

	// Request transport via coordinator
	logger.Log("INFO", "Siphon ship requesting transport", map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol,
		"action":      "request_transport",
	})

	transportSymbol, err := cmd.Coordinator.RequestTransport(ctx, cmd.ShipSymbol)
	if err != nil {
		return 0, fmt.Errorf("failed to request transport: %w", err)
	}

	logger.Log("INFO", "Transport assigned to siphon ship", map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol,
		"action":      "transport_assigned",
		"transport":   transportSymbol,
	})

	// Load transport ship to check available space
	transportShip, err := h.shipRepo.FindBySymbol(ctx, transportSymbol, cmd.PlayerID)
	if err != nil {
		return 0, fmt.Errorf("failed to load transport ship: %w", err)
	}

	// Transfer ALL cargo to transport (all gases are valuable for manufacturing)
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
	if err := cmd.Coordinator.NotifyTransferComplete(ctx, cmd.ShipSymbol, transportSymbol); err != nil {
		return totalTransferred, fmt.Errorf("failed to notify transfer complete: %w", err)
	}

	return totalTransferred, nil
}
