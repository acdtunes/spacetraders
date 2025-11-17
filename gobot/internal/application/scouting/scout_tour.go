package scouting

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipapp "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	infraports "github.com/andrescamacho/spacetraders-go/internal/infrastructure/ports"
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
	shipRepo   navigation.ShipRepository
	marketRepo MarketRepository
	apiClient  infraports.APIClient
	playerRepo player.PlayerRepository
	mediator   common.Mediator
}

// NewScoutTourHandler creates a new scout tour command handler
func NewScoutTourHandler(
	shipRepo navigation.ShipRepository,
	marketRepo MarketRepository,
	apiClient infraports.APIClient,
	playerRepo player.PlayerRepository,
	mediator common.Mediator,
) *ScoutTourHandler {
	return &ScoutTourHandler{
		shipRepo:   shipRepo,
		marketRepo: marketRepo,
		apiClient:  apiClient,
		playerRepo: playerRepo,
		mediator:   mediator,
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

	// 2. Get player for token
	player, err := h.playerRepo.FindByID(ctx, int(cmd.PlayerID))
	if err != nil {
		return nil, fmt.Errorf("failed to find player: %w", err)
	}

	// 3. Rotate markets slice to start from current position (idempotency)
	tourOrder := rotateTourToStart(cmd.Markets, ship.CurrentLocation().Symbol)

	response := &ScoutTourResponse{
		MarketsVisited: 0,
		TourOrder:      tourOrder,
		Iterations:     0,
	}

	// 4. Execute tour iterations
	for iteration := 0; iteration < cmd.Iterations || cmd.Iterations == -1; iteration++ {
		// For each market: navigate → dock → get market data → persist
		for _, marketWaypoint := range tourOrder {
			// Navigate to waypoint using NavigateShip (handles route planning, refueling, etc.)
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
			logger.Log("INFO", fmt.Sprintf("[ScoutTour] Navigation complete: status=%s, fuel=%d", navResult.Status, navResult.FuelRemaining), nil)

			// Reload ship after navigation
			ship, err = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, int(cmd.PlayerID))
			if err != nil {
				return nil, fmt.Errorf("failed to reload ship after navigation: %w", err)
			}

			logger.Log("INFO", fmt.Sprintf("[ScoutTour] Docking %s at %s", cmd.ShipSymbol, marketWaypoint), nil)
			dockCmd := &shipapp.DockShipCommand{
				ShipSymbol: cmd.ShipSymbol,
				PlayerID:   int(cmd.PlayerID),
			}
			_, err = h.mediator.Send(ctx, dockCmd)
			if err != nil {
				return nil, fmt.Errorf("failed to dock at %s: %w", marketWaypoint, err)
			}

			// Reload ship after docking
			ship, err = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, int(cmd.PlayerID))
			if err != nil {
				return nil, fmt.Errorf("failed to reload ship after docking: %w", err)
			}

			// Get market data from API
			// Extract system symbol from waypoint (e.g., "X1-TEST-A1" -> "X1-TEST")
			systemSymbol := extractSystemSymbol(marketWaypoint)

			logger.Log("INFO", fmt.Sprintf("[ScoutTour] Getting market data for %s at %s", cmd.ShipSymbol, marketWaypoint), nil)
			marketData, err := h.apiClient.GetMarket(ctx, systemSymbol, marketWaypoint, player.Token)
			if err != nil {
				return nil, fmt.Errorf("failed to get market data for %s: %w", marketWaypoint, err)
			}

			// Convert API DTOs to domain TradeGoods
			tradeGoods := make([]market.TradeGood, 0, len(marketData.TradeGoods))
			for _, apiGood := range marketData.TradeGoods {
				good, err := market.NewTradeGood(
					apiGood.Symbol,
					&apiGood.Supply,
					nil, // activity not provided in this API response
					apiGood.SellPrice,
					apiGood.PurchasePrice,
					apiGood.TradeVolume,
				)
				if err != nil {
					return nil, fmt.Errorf("failed to create trade good: %w", err)
				}
				tradeGoods = append(tradeGoods, *good)
			}

			// Persist market data
			err = h.marketRepo.UpsertMarketData(ctx, cmd.PlayerID, marketWaypoint, tradeGoods, time.Now())
			if err != nil {
				return nil, fmt.Errorf("failed to persist market data: %w", err)
			}

			response.MarketsVisited++
		}

		// For multi-market tours (2+ markets), ship returns to start by definition
		// For single-market tours, no navigation needed (stationary scout)

		response.Iterations++

		// For single-market tours with 1 iteration, we're done
		if len(tourOrder) == 1 {
			break
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

// extractSystemSymbol extracts the system symbol from a waypoint symbol
// Example: "X1-TEST-A1" -> "X1-TEST"
func extractSystemSymbol(waypointSymbol string) string {
	// Waypoint format: SYSTEM-SECTOR (e.g., X1-TEST-A1 -> X1-TEST)
	// Simple heuristic: find the last dash and take everything before it
	for i := len(waypointSymbol) - 1; i >= 0; i-- {
		if waypointSymbol[i] == '-' {
			return waypointSymbol[:i]
		}
	}
	return waypointSymbol
}
