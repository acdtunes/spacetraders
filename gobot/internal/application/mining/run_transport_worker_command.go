package mining

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appShip "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	"github.com/andrescamacho/spacetraders-go/internal/application/trading"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// RunTransportWorkerCommand orchestrates passive cargo receiving and selling
// Transport waits at asteroid field as a cargo sink for miners
type RunTransportWorkerCommand struct {
	ShipSymbol        string
	PlayerID          shared.PlayerID
	AsteroidField     string           // Waypoint to wait at and return to
	MarketSymbol      string           // Nearest market with fuel for refueling
	CoordinatorID     string           // Parent coordinator container ID
	AvailabilityChan  chan<- string    // Signal transport is available at asteroid
	CargoReceivedChan <-chan struct{}  // Receive signal that cargo was transferred
}

// RunTransportWorkerResponse contains transport execution results
type RunTransportWorkerResponse struct {
	SellingCycles     int
	TotalMarketsVisited int
	TotalRevenue      int
	Error             string
}

// RunTransportWorkerHandler implements the transport worker workflow
type RunTransportWorkerHandler struct {
	mediator           common.Mediator
	shipRepo           navigation.ShipRepository
	shipAssignmentRepo container.ShipAssignmentRepository
	graphProvider      system.ISystemGraphProvider
}

// NewRunTransportWorkerHandler creates a new transport worker handler
func NewRunTransportWorkerHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	shipAssignmentRepo container.ShipAssignmentRepository,
	graphProvider system.ISystemGraphProvider,
) *RunTransportWorkerHandler {
	return &RunTransportWorkerHandler{
		mediator:           mediator,
		shipRepo:           shipRepo,
		shipAssignmentRepo: shipAssignmentRepo,
		graphProvider:      graphProvider,
	}
}

// Handle executes the transport worker command
func (h *RunTransportWorkerHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*RunTransportWorkerCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	result := &RunTransportWorkerResponse{
		SellingCycles:       0,
		TotalMarketsVisited: 0,
		TotalRevenue:        0,
		Error:               "",
	}

	// Execute transport workflow
	if err := h.executeTransport(ctx, cmd, result); err != nil {
		result.Error = err.Error()
		return result, err
	}

	return result, nil
}

// executeTransport handles the main transport workflow with passive cargo receiving
func (h *RunTransportWorkerHandler) executeTransport(
	ctx context.Context,
	cmd *RunTransportWorkerCommand,
	result *RunTransportWorkerResponse,
) error {
	logger := common.LoggerFromContext(ctx)

	// 1. Load transport ship
	transportShip, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return fmt.Errorf("failed to load transport ship: %w", err)
	}

	// 2. Navigate to asteroid field if not there
	// Strategy: Go to market first (BURN), refuel, then CRUISE to asteroid
	// This matches the dry-run route planning logic exactly
	if transportShip.CurrentLocation().Symbol != cmd.AsteroidField {
		logger.Log("INFO", "Transport navigating to asteroid via market", map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol,
			"action":      "navigate_to_asteroid",
			"asteroid":    cmd.AsteroidField,
			"via_market":  cmd.MarketSymbol,
		})

		// Step 1: Navigate to market (BURN for speed, will refuel there)
		if transportShip.CurrentLocation().Symbol != cmd.MarketSymbol {
			logger.Log("INFO", "Transport navigating to market with BURN mode", map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol,
				"action":      "navigate_to_market",
				"destination": cmd.MarketSymbol,
				"flight_mode": "BURN",
				"step":        1,
			})
			navToMarketCmd := &appShip.NavigateRouteCommand{
				ShipSymbol:   cmd.ShipSymbol,
				Destination:  cmd.MarketSymbol,
				PlayerID:     cmd.PlayerID,
				PreferCruise: false, // BURN to market (can refuel there)
			}
			_, err := h.mediator.Send(ctx, navToMarketCmd)
			if err != nil {
				return fmt.Errorf("failed to navigate to market: %w", err)
			}
		}

		// Step 2: Dock and refuel to full
		logger.Log("INFO", "Transport docking and refueling", map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol,
			"action":      "dock_and_refuel",
			"location":    cmd.MarketSymbol,
			"step":        2,
		})
		dockCmd := &appShip.DockShipCommand{
			ShipSymbol: cmd.ShipSymbol,
			PlayerID:   cmd.PlayerID,
		}
		h.mediator.Send(ctx, dockCmd)

		refuelCmd := &appShip.RefuelShipCommand{
			ShipSymbol: cmd.ShipSymbol,
			PlayerID:   cmd.PlayerID,
			Units:      nil, // nil = full refuel
		}
		_, err = h.mediator.Send(ctx, refuelCmd)
		if err != nil {
			logger.Log("WARNING", "Transport refuel failed", map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol,
				"action":      "refuel",
				"location":    cmd.MarketSymbol,
				"error":       err.Error(),
			})
		}

		// Step 3: CRUISE to asteroid (preserves fuel for return trip)
		logger.Log("INFO", "Transport navigating to asteroid with CRUISE mode", map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol,
			"action":      "navigate_to_asteroid",
			"destination": cmd.AsteroidField,
			"flight_mode": "CRUISE",
			"step":        3,
		})
		navToAsteroidCmd := &appShip.NavigateRouteCommand{
			ShipSymbol:   cmd.ShipSymbol,
			Destination:  cmd.AsteroidField,
			PlayerID:     cmd.PlayerID,
			PreferCruise: true, // CRUISE to asteroid (no fuel there)
		}
		_, err = h.mediator.Send(ctx, navToAsteroidCmd)
		if err != nil {
			return fmt.Errorf("failed to navigate to asteroid: %w", err)
		}

		// Reload ship after navigation
		transportShip, err = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
		if err != nil {
			return fmt.Errorf("failed to reload transport ship: %w", err)
		}
	}

	logger.Log("INFO", "Transport positioned and ready for cargo receiving", map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol,
		"action":      "start_receiving",
		"location":    cmd.AsteroidField,
		"mode":        "passive",
	})

	// 3. Main transport loop - runs indefinitely until context cancelled
	for {
		// Check for context cancellation
		select {
		case <-ctx.Done():
			logger.Log("INFO", "Transport operation cancelled by context", map[string]interface{}{
				"ship_symbol":     cmd.ShipSymbol,
				"action":          "operation_cancelled",
				"selling_cycles":  result.SellingCycles,
				"total_revenue":   result.TotalRevenue,
			})
			return ctx.Err()
		default:
		}

		// 3a. Signal availability to coordinator
		select {
		case cmd.AvailabilityChan <- cmd.ShipSymbol:
			// Availability signaled
		case <-ctx.Done():
			return ctx.Err()
		}

		// 3b. Wait for cargo transfer notification
		select {
		case <-cmd.CargoReceivedChan:
			// Cargo was transferred to us
			logger.Log("INFO", "Transport received cargo from miner", map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol,
				"action":      "cargo_received",
				"location":    cmd.AsteroidField,
			})
		case <-ctx.Done():
			return ctx.Err()
		}

		// 3c. Reload ship to check cargo level
		transportShip, err = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
		if err != nil {
			return fmt.Errorf("failed to reload transport ship: %w", err)
		}

		// 3d. Check if cargo is at least 95% full (or truly full)
		cargoUsage := float64(transportShip.Cargo().Units) / float64(transportShip.Cargo().Capacity)

		if cargoUsage >= 0.95 || transportShip.IsCargoFull() {
			logger.Log("INFO", "Transport cargo threshold reached, executing selling route", map[string]interface{}{
				"ship_symbol":    cmd.ShipSymbol,
				"action":         "execute_selling_route",
				"cargo_usage":    fmt.Sprintf("%.1f%%", cargoUsage*100),
				"cargo_units":    transportShip.Cargo().Units,
				"cargo_capacity": transportShip.Cargo().Capacity,
			})

			// 3e. Execute selling route via TourSellingCommand
			tourCmd := &trading.RunTourSellingCommand{
				ShipSymbol:     cmd.ShipSymbol,
				PlayerID:       cmd.PlayerID,
				ReturnWaypoint: cmd.AsteroidField,
			}

			tourResp, err := h.mediator.Send(ctx, tourCmd)
			if err != nil {
				return fmt.Errorf("failed to execute sell route: %w", err)
			}

			tourResult := tourResp.(*trading.RunTourSellingResponse)
			result.SellingCycles++
			result.TotalMarketsVisited += tourResult.MarketsVisited
			result.TotalRevenue += tourResult.TotalRevenue

			logger.Log("INFO", "Transport selling cycle completed", map[string]interface{}{
				"ship_symbol":    cmd.ShipSymbol,
				"action":         "selling_cycle_complete",
				"cycle_number":   result.SellingCycles,
				"markets_visited": tourResult.MarketsVisited,
				"cycle_revenue":  tourResult.TotalRevenue,
				"total_revenue":  result.TotalRevenue,
			})

			// Note: executeSellRoute already includes return to asteroid via OptimizeFueledTour

			// Reload ship after returning
			transportShip, err = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
			if err != nil {
				return fmt.Errorf("failed to reload transport ship: %w", err)
			}

			logger.Log("INFO", "Transport returned to asteroid field", map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol,
				"action":      "resume_receiving",
				"location":    cmd.AsteroidField,
			})
		}

		// Continue loop - will signal availability again
	}
}

// returnToAsteroid navigates the transport back to the asteroid field
func (h *RunTransportWorkerHandler) returnToAsteroid(
	ctx context.Context,
	cmd *RunTransportWorkerCommand,
) error {
	logger := common.LoggerFromContext(ctx)

	// Load current ship state
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return fmt.Errorf("failed to load ship: %w", err)
	}

	if ship.CurrentLocation().Symbol == cmd.AsteroidField {
		return nil
	}

	logger.Log("INFO", "Transport returning to asteroid field", map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol,
		"action":      "return_to_asteroid",
		"destination": cmd.AsteroidField,
	})

	navCmd := &appShip.NavigateRouteCommand{
		ShipSymbol:   cmd.ShipSymbol,
		Destination:  cmd.AsteroidField,
		PlayerID:     cmd.PlayerID,
		PreferCruise: true, // CRUISE to asteroid (no fuel there for return)
	}

	_, err = h.mediator.Send(ctx, navCmd)
	if err != nil {
		return fmt.Errorf("failed to return to asteroid: %w", err)
	}

	return nil
}
