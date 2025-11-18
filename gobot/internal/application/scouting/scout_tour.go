package scouting

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipapp "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// ScoutTourCommand - Command to execute a market scouting tour with a single ship
type ScoutTourCommand struct {
	PlayerID   uint
	ShipSymbol string
	Markets    []string // Waypoint symbols to scout
	Iterations int      // Number of complete tours (-1 for infinite)
}

// ScoutTourResponse - Response from scout tour execution
type ScoutTourResponse struct {
	MarketsVisited int
	TourOrder      []string // Order in which markets were visited
	Iterations     int
}

// ScoutTourHandler - Handles scout tour commands
type ScoutTourHandler struct {
	shipRepo      navigation.ShipRepository
	mediator      common.Mediator
	marketScanner *shipapp.MarketScanner
}

// NewScoutTourHandler creates a new scout tour command handler
func NewScoutTourHandler(
	shipRepo navigation.ShipRepository,
	mediator common.Mediator,
	marketScanner *shipapp.MarketScanner,
) *ScoutTourHandler {
	return &ScoutTourHandler{
		shipRepo:      shipRepo,
		mediator:      mediator,
		marketScanner: marketScanner,
	}
}

// Handle executes the scout tour command
func (h *ScoutTourHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*ScoutTourCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	// Extract logger from context
	logger := common.LoggerFromContext(ctx)

	// 1. Load ship to get current position
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, int(cmd.PlayerID))
	if err != nil {
		return nil, fmt.Errorf("failed to find ship: %w", err)
	}

	// 2. Rotate markets slice to start from current position (idempotency)
	tourOrder := rotateTourToStart(cmd.Markets, ship.CurrentLocation().Symbol)

	response := &ScoutTourResponse{
		MarketsVisited: 0,
		TourOrder:      tourOrder,
		Iterations:     0,
	}

	// 4. Execute tour based on type
	if len(tourOrder) == 1 {
		// Single-market tour: Stationary scout with continuous scanning
		marketWaypoint := tourOrder[0]

		// Navigate to market once (if not already there)
		// Market scan happens automatically via NavigateShipCommand
		if ship.CurrentLocation().Symbol != marketWaypoint {
			logger.Log("INFO", fmt.Sprintf("[ScoutTour] Navigating %s to %s (stationary scout)", cmd.ShipSymbol, marketWaypoint), nil)
			navCmd := &shipapp.NavigateShipCommand{
				ShipSymbol:  cmd.ShipSymbol,
				Destination: marketWaypoint,
				PlayerID:    int(cmd.PlayerID),
			}
			navResp, err := h.mediator.Send(ctx, navCmd)
			if err != nil {
				return nil, fmt.Errorf("failed to navigate to %s: %w", marketWaypoint, err)
			}

			navResult := navResp.(*shipapp.NavigateShipResponse)
			logger.Log("INFO", fmt.Sprintf("[ScoutTour] Navigation complete: status=%s, fuel=%d (market scanned automatically)", navResult.Status, navResult.FuelRemaining), nil)
			response.MarketsVisited++
		} else {
			// Already at market, perform initial scan
			logger.Log("INFO", fmt.Sprintf("[ScoutTour] Ship already at %s, performing initial market scan", marketWaypoint), nil)
			if err := h.marketScanner.ScanAndSaveMarket(ctx, cmd.PlayerID, marketWaypoint); err != nil {
				logger.Log("ERROR", fmt.Sprintf("[ScoutTour] Initial market scan failed: %v", err), nil)
				// Non-fatal - continue with tour
			} else {
				response.MarketsVisited++
			}
		}

		response.Iterations++

		// Continue scanning every 60 seconds for remaining iterations
		for iteration := 1; iteration < cmd.Iterations || cmd.Iterations == -1; iteration++ {
			logger.Log("INFO", "[ScoutTour] Waiting 60 seconds before next scan...", nil)

			// Context-aware sleep - respects context cancellation for graceful shutdown
			select {
			case <-time.After(60 * time.Second):
				// Continue to next scan
			case <-ctx.Done():
				logger.Log("INFO", fmt.Sprintf("[ScoutTour] Context cancelled, stopping after %d iterations", response.Iterations), nil)
				return response, nil
			}

			logger.Log("INFO", fmt.Sprintf("[ScoutTour] Scanning market at %s (iteration %d)", marketWaypoint, iteration+1), nil)
			if err := h.marketScanner.ScanAndSaveMarket(ctx, cmd.PlayerID, marketWaypoint); err != nil {
				logger.Log("ERROR", fmt.Sprintf("[ScoutTour] Market scan failed: %v", err), nil)
				// Non-fatal - continue scanning
			} else {
				response.MarketsVisited++
			}

			response.Iterations++
		}
	} else {
		// Multi-market tour: Navigate to each market (scan happens automatically)
		for iteration := 0; iteration < cmd.Iterations || cmd.Iterations == -1; iteration++ {
			for _, marketWaypoint := range tourOrder {
				// Navigate to waypoint using NavigateShip
				// Market scan happens automatically via MarketScanner in RouteExecutor
				logger.Log("INFO", fmt.Sprintf("[ScoutTour] Navigating %s to %s", cmd.ShipSymbol, marketWaypoint), nil)
				navCmd := &shipapp.NavigateShipCommand{
					ShipSymbol:  cmd.ShipSymbol,
					Destination: marketWaypoint,
					PlayerID:    int(cmd.PlayerID),
				}
				navResp, err := h.mediator.Send(ctx, navCmd)
				if err != nil {
					return nil, fmt.Errorf("failed to navigate to %s: %w", marketWaypoint, err)
				}

				navResult := navResp.(*shipapp.NavigateShipResponse)
				logger.Log("INFO", fmt.Sprintf("[ScoutTour] Navigation complete: status=%s, fuel=%d (market scanned automatically)", navResult.Status, navResult.FuelRemaining), nil)

				response.MarketsVisited++
			}

			response.Iterations++
		}
	}

	return response, nil
}

// rotateTourToStart rotates the tour slice so it starts from the ship's current position
// This provides idempotency: if the command is re-run, it continues from where the ship is
func rotateTourToStart(markets []string, currentPosition string) []string {
	// Find index of current position in markets
	startIndex := -1
	for i, waypoint := range markets {
		if waypoint == currentPosition {
			startIndex = i
			break
		}
	}

	// If current position not in tour, return original order
	if startIndex == -1 {
		return markets
	}

	// Rotate slice to start from current position
	rotated := make([]string, len(markets))
	for i := 0; i < len(markets); i++ {
		rotated[i] = markets[(startIndex+i)%len(markets)]
	}

	return rotated
}

