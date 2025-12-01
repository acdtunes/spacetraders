package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/gas/ports"
	"github.com/andrescamacho/spacetraders-go/internal/application/gas/queries"
	appShipCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// RunGasTransportWorkerCommand orchestrates gas delivery to factories
// Transport waits at gas giant as a cargo sink for siphon ships, then delivers to factories
type RunGasTransportWorkerCommand struct {
	ShipSymbol    string
	PlayerID      shared.PlayerID
	GasGiant      string // Waypoint to wait at and return to
	CoordinatorID string // Parent coordinator container ID
	Coordinator   ports.TransportCoordinator
}

// RunGasTransportWorkerResponse contains gas transport execution results
type RunGasTransportWorkerResponse struct {
	DeliveryCycles      int
	FactoriesSupplied   int
	TotalUnitsDelivered int
	Error               string
}

// RunGasTransportWorkerHandler implements the gas transport worker workflow
type RunGasTransportWorkerHandler struct {
	mediator           common.Mediator
	shipRepo           navigation.ShipRepository
	shipAssignmentRepo container.ShipAssignmentRepository
	apiClient          domainPorts.APIClient
}

// NewRunGasTransportWorkerHandler creates a new gas transport worker handler
func NewRunGasTransportWorkerHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	shipAssignmentRepo container.ShipAssignmentRepository,
	apiClient domainPorts.APIClient,
) *RunGasTransportWorkerHandler {
	return &RunGasTransportWorkerHandler{
		mediator:           mediator,
		shipRepo:           shipRepo,
		shipAssignmentRepo: shipAssignmentRepo,
		apiClient:          apiClient,
	}
}

// Handle executes the gas transport worker command
func (h *RunGasTransportWorkerHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*RunGasTransportWorkerCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	result := &RunGasTransportWorkerResponse{
		DeliveryCycles:      0,
		FactoriesSupplied:   0,
		TotalUnitsDelivered: 0,
		Error:               "",
	}

	// Execute gas transport workflow
	if err := h.executeGasTransport(ctx, cmd, result); err != nil {
		result.Error = err.Error()
		return result, err
	}

	return result, nil
}

// executeGasTransport handles the main gas transport workflow with factory delivery
func (h *RunGasTransportWorkerHandler) executeGasTransport(
	ctx context.Context,
	cmd *RunGasTransportWorkerCommand,
	result *RunGasTransportWorkerResponse,
) error {
	logger := common.LoggerFromContext(ctx)

	// 1. Load transport ship
	transportShip, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return fmt.Errorf("failed to load transport ship: %w", err)
	}

	// 2. Navigate to gas giant if not there
	if transportShip.CurrentLocation().Symbol != cmd.GasGiant {
		logger.Log("INFO", "Gas transport navigating to gas giant", map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol,
			"action":      "navigate_to_gas_giant",
			"gas_giant":   cmd.GasGiant,
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
		transportShip, err = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
		if err != nil {
			return fmt.Errorf("failed to reload transport ship: %w", err)
		}
	}

	logger.Log("INFO", "Gas transport positioned and ready for cargo receiving", map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol,
		"action":      "start_receiving",
		"location":    cmd.GasGiant,
		"mode":        "passive",
	})

	// 3. Main transport loop - runs indefinitely until context cancelled
	for {
		// Check for context cancellation
		select {
		case <-ctx.Done():
			logger.Log("INFO", "Gas transport operation cancelled by context", map[string]interface{}{
				"ship_symbol":     cmd.ShipSymbol,
				"action":          "operation_cancelled",
				"delivery_cycles": result.DeliveryCycles,
				"units_delivered": result.TotalUnitsDelivered,
			})
			return ctx.Err()
		default:
		}

		// 3a. Signal availability and wait for cargo (blocking operation)
		if err := cmd.Coordinator.SignalAvailability(ctx, cmd.ShipSymbol); err != nil {
			if err == context.Canceled || err == context.DeadlineExceeded {
				return err
			}
			return fmt.Errorf("failed to signal availability: %w", err)
		}

		// Cargo was transferred to us
		logger.Log("INFO", "Gas transport received cargo from siphon ship", map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol,
			"action":      "cargo_received",
			"location":    cmd.GasGiant,
		})

		// 3c. Reload ship to check cargo level
		transportShip, err = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
		if err != nil {
			return fmt.Errorf("failed to reload transport ship: %w", err)
		}

		// 3d. Check if cargo is at least 95% full (or truly full)
		cargoUsage := float64(transportShip.Cargo().Units) / float64(transportShip.Cargo().Capacity)

		if cargoUsage >= 0.95 || transportShip.IsCargoFull() {
			logger.Log("INFO", "Gas transport cargo threshold reached, executing factory delivery", map[string]interface{}{
				"ship_symbol":    cmd.ShipSymbol,
				"action":         "execute_factory_delivery",
				"cargo_usage":    fmt.Sprintf("%.1f%%", cargoUsage*100),
				"cargo_units":    transportShip.Cargo().Units,
				"cargo_capacity": transportShip.Cargo().Capacity,
			})

			// 3e. Execute factory delivery for each gas type in cargo
			unitsDelivered, factoriesVisited, err := h.executeFactoryDelivery(ctx, cmd, transportShip)
			if err != nil {
				logger.Log("WARNING", "Factory delivery failed", map[string]interface{}{
					"ship_symbol": cmd.ShipSymbol,
					"action":      "factory_delivery_error",
					"error":       err.Error(),
				})
				// Continue despite error - try to return to gas giant
			}

			result.DeliveryCycles++
			result.TotalUnitsDelivered += unitsDelivered
			result.FactoriesSupplied += factoriesVisited

			logger.Log("INFO", "Gas delivery cycle completed", map[string]interface{}{
				"ship_symbol":       cmd.ShipSymbol,
				"action":            "delivery_cycle_complete",
				"cycle_number":      result.DeliveryCycles,
				"units_delivered":   unitsDelivered,
				"factories_visited": factoriesVisited,
				"total_delivered":   result.TotalUnitsDelivered,
			})

			// 3f. Return to gas giant
			if err := h.returnToGasGiant(ctx, cmd); err != nil {
				return fmt.Errorf("failed to return to gas giant: %w", err)
			}

			// Reload ship after returning
			transportShip, err = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
			if err != nil {
				return fmt.Errorf("failed to reload transport ship: %w", err)
			}

			logger.Log("INFO", "Gas transport returned to gas giant", map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol,
				"action":      "resume_receiving",
				"location":    cmd.GasGiant,
			})
		}

		// Continue loop - will signal availability again
	}
}

// executeFactoryDelivery delivers gas cargo to factories with LOW supply
func (h *RunGasTransportWorkerHandler) executeFactoryDelivery(
	ctx context.Context,
	cmd *RunGasTransportWorkerCommand,
	ship *navigation.Ship,
) (int, int, error) {
	logger := common.LoggerFromContext(ctx)
	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return 0, 0, err
	}

	totalUnitsDelivered := 0
	factoriesVisited := 0
	systemSymbol := ship.CurrentLocation().SystemSymbol

	// Process each gas type in cargo
	for _, cargoItem := range ship.Cargo().Inventory {
		if cargoItem.Units == 0 {
			continue
		}

		// Find factory that needs this gas type
		findFactoryQuery := &queries.FindFactoryForGasQuery{
			GasSymbol:    cargoItem.Symbol,
			SystemSymbol: systemSymbol,
			PlayerID:     cmd.PlayerID.Value(),
			ShipLocation: ship.CurrentLocation(),
		}

		factoryResp, err := h.mediator.Send(ctx, findFactoryQuery)
		if err != nil {
			logger.Log("WARNING", "Failed to find factory for gas", map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol,
				"action":      "find_factory_error",
				"gas_symbol":  cargoItem.Symbol,
				"error":       err.Error(),
			})
			continue
		}

		factory := factoryResp.(*queries.FindFactoryForGasResponse)
		if !factory.Found {
			logger.Log("WARNING", "No factory found that needs this gas", map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol,
				"action":      "no_factory_found",
				"gas_symbol":  cargoItem.Symbol,
			})
			continue
		}

		logger.Log("INFO", "Factory found for gas delivery", map[string]interface{}{
			"ship_symbol":  cmd.ShipSymbol,
			"action":       "factory_found",
			"gas_symbol":   cargoItem.Symbol,
			"factory":      factory.FactoryWaypoint,
			"supply_level": factory.SupplyLevel,
			"distance":     factory.Distance,
		})

		// Navigate to factory
		if ship.CurrentLocation().Symbol != factory.FactoryWaypoint {
			navCmd := &appShipCmd.NavigateRouteCommand{
				ShipSymbol:  cmd.ShipSymbol,
				Destination: factory.FactoryWaypoint,
				PlayerID:    cmd.PlayerID,
			}
			_, err := h.mediator.Send(ctx, navCmd)
			if err != nil {
				logger.Log("WARNING", "Failed to navigate to factory", map[string]interface{}{
					"ship_symbol": cmd.ShipSymbol,
					"action":      "navigate_factory_error",
					"factory":     factory.FactoryWaypoint,
					"error":       err.Error(),
				})
				continue
			}
		}

		// Dock at factory
		dockCmd := &shipTypes.DockShipCommand{
			Ship:     ship,
			PlayerID: cmd.PlayerID,
		}
		_, err = h.mediator.Send(ctx, dockCmd)
		if err != nil {
			logger.Log("WARNING", "Failed to dock at factory", map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol,
				"action":      "dock_factory_error",
				"factory":     factory.FactoryWaypoint,
				"error":       err.Error(),
			})
			continue
		}

		// Sell (deliver) the gas to the factory
		// Using sell API which "delivers" goods to markets/factories
		_, err = h.apiClient.SellCargo(ctx, cmd.ShipSymbol, cargoItem.Symbol, cargoItem.Units, token)
		if err != nil {
			logger.Log("WARNING", "Failed to deliver gas to factory", map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol,
				"action":      "deliver_gas_error",
				"factory":     factory.FactoryWaypoint,
				"gas_symbol":  cargoItem.Symbol,
				"units":       cargoItem.Units,
				"error":       err.Error(),
			})
			continue
		}

		logger.Log("INFO", "Gas delivered to factory successfully", map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol,
			"action":      "gas_delivered",
			"factory":     factory.FactoryWaypoint,
			"gas_symbol":  cargoItem.Symbol,
			"units":       cargoItem.Units,
		})

		totalUnitsDelivered += cargoItem.Units
		factoriesVisited++

		// Refuel if possible (factory may have fuel)
		refuelCmd := &shipTypes.RefuelShipCommand{
			Ship:     ship,
			PlayerID: cmd.PlayerID,
			Units:    nil, // Full refuel
		}
		_, _ = h.mediator.Send(ctx, refuelCmd) // Ignore refuel errors - factory may not have fuel

		// Reload ship to get updated cargo
		ship, err = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
		if err != nil {
			return totalUnitsDelivered, factoriesVisited, fmt.Errorf("failed to reload ship: %w", err)
		}
	}

	return totalUnitsDelivered, factoriesVisited, nil
}

// returnToGasGiant navigates the transport back to the gas giant
func (h *RunGasTransportWorkerHandler) returnToGasGiant(
	ctx context.Context,
	cmd *RunGasTransportWorkerCommand,
) error {
	logger := common.LoggerFromContext(ctx)

	// Load current ship state
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return fmt.Errorf("failed to load ship: %w", err)
	}

	if ship.CurrentLocation().Symbol == cmd.GasGiant {
		return nil
	}

	logger.Log("INFO", "Gas transport returning to gas giant", map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol,
		"action":      "return_to_gas_giant",
		"destination": cmd.GasGiant,
	})

	navCmd := &appShipCmd.NavigateRouteCommand{
		ShipSymbol:  cmd.ShipSymbol,
		Destination: cmd.GasGiant,
		PlayerID:    cmd.PlayerID,
	}

	_, err = h.mediator.Send(ctx, navCmd)
	if err != nil {
		return fmt.Errorf("failed to return to gas giant: %w", err)
	}

	return nil
}
