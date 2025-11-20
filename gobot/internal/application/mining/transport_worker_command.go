package mining

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appShip "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	"github.com/andrescamacho/spacetraders-go/internal/application/trading"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// RunTransportWorkerCommand orchestrates passive cargo receiving and selling
// Transport waits at asteroid field as a cargo sink for miners
type RunTransportWorkerCommand struct {
	ShipSymbol        string
	PlayerID          int
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
	shipAssignmentRepo daemon.ShipAssignmentRepository
	graphProvider      system.ISystemGraphProvider
}

// NewRunTransportWorkerHandler creates a new transport worker handler
func NewRunTransportWorkerHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	shipAssignmentRepo daemon.ShipAssignmentRepository,
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
		logger.Log("INFO", fmt.Sprintf("Navigating to asteroid field %s via market %s", cmd.AsteroidField, cmd.MarketSymbol), nil)

		// Step 1: Navigate to market (BURN for speed, will refuel there)
		if transportShip.CurrentLocation().Symbol != cmd.MarketSymbol {
			logger.Log("INFO", fmt.Sprintf("Step 1: BURN to market %s", cmd.MarketSymbol), nil)
			navToMarketCmd := &appShip.NavigateShipCommand{
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
		logger.Log("INFO", "Step 2: Docking and refueling to full", nil)
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
			logger.Log("WARNING", fmt.Sprintf("Failed to refuel: %v", err), nil)
		}

		// Step 3: CRUISE to asteroid (preserves fuel for return trip)
		logger.Log("INFO", fmt.Sprintf("Step 3: CRUISE to asteroid %s", cmd.AsteroidField), nil)
		navToAsteroidCmd := &appShip.NavigateShipCommand{
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

	logger.Log("INFO", fmt.Sprintf("Transport positioned at %s, starting passive cargo receiving", cmd.AsteroidField), nil)

	// 3. Main transport loop - runs indefinitely until context cancelled
	for {
		// Check for context cancellation
		select {
		case <-ctx.Done():
			logger.Log("INFO", "Transport operation cancelled", nil)
			return ctx.Err()
		default:
		}

		// 3a. Signal availability to coordinator
		logger.Log("DEBUG", "Signaling availability to coordinator", nil)

		select {
		case cmd.AvailabilityChan <- cmd.ShipSymbol:
			// Availability signaled
		case <-ctx.Done():
			return ctx.Err()
		}

		// 3b. Wait for cargo transfer notification
		logger.Log("DEBUG", "Waiting for cargo transfer", nil)

		select {
		case <-cmd.CargoReceivedChan:
			// Cargo was transferred to us
			logger.Log("DEBUG", "Received cargo transfer notification", nil)
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
		logger.Log("DEBUG", fmt.Sprintf("Cargo at %.1f%% (%d/%d)",
			cargoUsage*100, transportShip.Cargo().Units, transportShip.Cargo().Capacity), nil)

		if cargoUsage >= 0.95 || transportShip.IsCargoFull() {
			logger.Log("INFO", fmt.Sprintf("Cargo is %.1f%% full, executing selling route", cargoUsage*100), nil)

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

			logger.Log("INFO", fmt.Sprintf("Completed selling cycle %d: %d markets, %d credits",
				result.SellingCycles, tourResult.MarketsVisited, tourResult.TotalRevenue), nil)

			// Note: executeSellRoute already includes return to asteroid via OptimizeFueledTour

			// Reload ship after returning
			transportShip, err = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
			if err != nil {
				return fmt.Errorf("failed to reload transport ship: %w", err)
			}

			logger.Log("INFO", "Returned to asteroid field, resuming passive receiving", nil)
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

	logger.Log("INFO", fmt.Sprintf("Returning to asteroid field %s", cmd.AsteroidField), nil)

	navCmd := &appShip.NavigateShipCommand{
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
