package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship"
	shipCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ScoutTourCommand - Command to execute a market scouting tour with a single ship
type ScoutTourCommand struct {
	PlayerID   shared.PlayerID
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
	marketScanner *ship.MarketScanner
}

// NewScoutTourHandler creates a new scout tour command handler
func NewScoutTourHandler(
	shipRepo navigation.ShipRepository,
	mediator common.Mediator,
	marketScanner *ship.MarketScanner,
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

	ship, tourOrder, response, err := h.loadShipAndPrepareTour(ctx, cmd)
	if err != nil {
		return nil, err
	}

	if len(tourOrder) == 1 {
		err = h.executeStationaryScout(ctx, cmd, ship, tourOrder[0], response)
	} else {
		err = h.executeMultiMarketTour(ctx, cmd, tourOrder, response)
	}

	return response, err
}

// loadShipAndPrepareTour loads ship data, rotates tour to start at current location, and initializes response
func (h *ScoutTourHandler) loadShipAndPrepareTour(
	ctx context.Context,
	cmd *ScoutTourCommand,
) (*navigation.Ship, []string, *ScoutTourResponse, error) {
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to find ship: %w", err)
	}

	tourOrder := rotateTourToStart(cmd.Markets, ship.CurrentLocation().Symbol)

	response := &ScoutTourResponse{
		MarketsVisited: 0,
		TourOrder:      tourOrder,
		Iterations:     0,
	}

	return ship, tourOrder, response, nil
}

// executeStationaryScout executes a continuous scanning operation at a single market
func (h *ScoutTourHandler) executeStationaryScout(
	ctx context.Context,
	cmd *ScoutTourCommand,
	ship *navigation.Ship,
	marketWaypoint string,
	response *ScoutTourResponse,
) error {
	if err := h.navigateToMarketIfNeeded(ctx, ship, marketWaypoint, cmd.PlayerID, cmd.ShipSymbol); err != nil {
		return err
	}

	if ship.CurrentLocation().Symbol == marketWaypoint {
		if err := h.performInitialScan(ctx, uint(cmd.PlayerID.Value()), marketWaypoint, cmd.ShipSymbol); err == nil {
			response.MarketsVisited++
		}
	} else {
		response.MarketsVisited++
	}

	response.Iterations++

	return h.continuousMarketScanning(ctx, cmd, marketWaypoint, response)
}

// navigateToMarketIfNeeded navigates ship to market if not already there
func (h *ScoutTourHandler) navigateToMarketIfNeeded(
	ctx context.Context,
	ship *navigation.Ship,
	marketWaypoint string,
	playerID shared.PlayerID,
	shipSymbol string,
) error {
	if ship.CurrentLocation().Symbol == marketWaypoint {
		return nil
	}

	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", "Ship navigating to stationary scout position", map[string]interface{}{
		"ship_symbol": shipSymbol,
		"action":      "navigate",
		"destination": marketWaypoint,
		"tour_type":   "stationary_scout",
	})

	navCmd := &shipCmd.NavigateRouteCommand{
		ShipSymbol:  shipSymbol,
		Destination: marketWaypoint,
		PlayerID:    playerID,
	}

	navResp, err := h.mediator.Send(ctx, navCmd)
	if err != nil {
		return fmt.Errorf("failed to navigate to %s: %w", marketWaypoint, err)
	}

	navResult := navResp.(*shipCmd.NavigateRouteResponse)
	logger.Log("INFO", "Ship navigation complete - market scanned", map[string]interface{}{
		"ship_symbol":    shipSymbol,
		"action":         "navigation_complete",
		"status":         navResult.Status,
		"fuel":           navResult.FuelRemaining,
		"market_scanned": true,
	})

	return nil
}

// performInitialScan performs the first market scan when ship is already at market
func (h *ScoutTourHandler) performInitialScan(
	ctx context.Context,
	playerID uint,
	marketWaypoint string,
	shipSymbol string,
) error {
	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", "Ship performing initial market scan", map[string]interface{}{
		"ship_symbol": shipSymbol,
		"action":      "scan_market",
		"waypoint":    marketWaypoint,
		"reason":      "already_present",
	})

	if err := h.marketScanner.ScanAndSaveMarket(ctx, playerID, marketWaypoint); err != nil {
		logger.Log("ERROR", "Initial market scan failed", map[string]interface{}{
			"ship_symbol": shipSymbol,
			"action":      "scan_market",
			"waypoint":    marketWaypoint,
			"error":       err.Error(),
		})
		return err
	}

	return nil
}

// continuousMarketScanning runs a loop that scans the market every 60 seconds
func (h *ScoutTourHandler) continuousMarketScanning(
	ctx context.Context,
	cmd *ScoutTourCommand,
	marketWaypoint string,
	response *ScoutTourResponse,
) error {
	logger := common.LoggerFromContext(ctx)

	for iteration := 1; iteration < cmd.Iterations || cmd.Iterations == -1; iteration++ {
		logger.Log("INFO", "Waiting before next market scan", map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol,
			"action":      "wait_scan",
			"duration":    "5m",
		})

		select {
		case <-time.After(5 * time.Minute):
			// Continue to next scan
		case <-ctx.Done():
			logger.Log("INFO", "Scout tour cancelled by context", map[string]interface{}{
				"ship_symbol":          cmd.ShipSymbol,
				"action":               "tour_cancelled",
				"iterations_completed": response.Iterations,
			})
			return nil
		}

		logger.Log("INFO", "Scanning market at waypoint", map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol,
			"action":      "scan_market",
			"waypoint":    marketWaypoint,
			"iteration":   iteration + 1,
		})

		if err := h.marketScanner.ScanAndSaveMarket(ctx, uint(cmd.PlayerID.Value()), marketWaypoint); err != nil {
			logger.Log("ERROR", "Market scan failed", map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol,
				"action":      "scan_market",
				"waypoint":    marketWaypoint,
				"iteration":   iteration + 1,
				"error":       err.Error(),
			})
		} else {
			response.MarketsVisited++
		}

		response.Iterations++
	}

	return nil
}

// executeMultiMarketTour executes a tour visiting multiple markets in sequence
func (h *ScoutTourHandler) executeMultiMarketTour(
	ctx context.Context,
	cmd *ScoutTourCommand,
	tourOrder []string,
	response *ScoutTourResponse,
) error {
	for iteration := 0; iteration < cmd.Iterations || cmd.Iterations == -1; iteration++ {
		for _, marketWaypoint := range tourOrder {
			navResult, err := h.navigateToMarket(ctx, cmd, marketWaypoint, iteration)
			if err != nil {
				return err
			}

			logger := common.LoggerFromContext(ctx)
			logger.Log("INFO", "Ship navigation complete - scanning market", map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol,
				"action":      "navigation_complete",
				"status":      navResult.Status,
				"fuel":        navResult.FuelRemaining,
			})

			// Scan the market after arriving
			if err := h.marketScanner.ScanAndSaveMarket(ctx, uint(cmd.PlayerID.Value()), marketWaypoint); err != nil {
				logger.Log("ERROR", "Market scan failed", map[string]interface{}{
					"ship_symbol": cmd.ShipSymbol,
					"action":      "scan_market",
					"waypoint":    marketWaypoint,
					"iteration":   iteration + 1,
					"error":       err.Error(),
				})
				// Continue to next market even if scan fails
			} else {
				response.MarketsVisited++
			}
		}

		response.Iterations++
	}

	return nil
}

// navigateToMarket navigates ship to specified market waypoint
func (h *ScoutTourHandler) navigateToMarket(
	ctx context.Context,
	cmd *ScoutTourCommand,
	marketWaypoint string,
	iteration int,
) (*shipCmd.NavigateRouteResponse, error) {
	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", "Ship navigating to market on tour", map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol,
		"action":      "navigate",
		"destination": marketWaypoint,
		"tour_type":   "multi_market",
		"iteration":   iteration + 1,
	})

	navCmd := &shipCmd.NavigateRouteCommand{
		ShipSymbol:  cmd.ShipSymbol,
		Destination: marketWaypoint,
		PlayerID:    cmd.PlayerID,
	}

	navResp, err := h.mediator.Send(ctx, navCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate to %s: %w", marketWaypoint, err)
	}

	return navResp.(*shipCmd.NavigateRouteResponse), nil
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
