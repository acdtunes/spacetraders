package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appShip "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	domainShared "github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// RunTourSellingCommand executes optimized cargo selling tour
type RunTourSellingCommand struct {
	ShipSymbol     string
	PlayerID       domainShared.PlayerID
	ReturnWaypoint string // Optional waypoint to return to after selling
}

// RunTourSellingResponse contains tour execution results
type RunTourSellingResponse struct {
	MarketsVisited int
	TotalRevenue   int
	ItemsSold      []SoldItem
}

// SoldItem represents a single item sold during the tour
type SoldItem struct {
	Symbol  string
	Units   int
	Revenue int
	Market  string
}

// RunTourSellingHandler implements the tour selling workflow
type RunTourSellingHandler struct {
	mediator      common.Mediator
	shipRepo      navigation.ShipRepository
	marketRepo    market.MarketRepository
	routingClient routing.RoutingClient
	graphProvider system.ISystemGraphProvider
}

// NewRunTourSellingHandler creates a new tour selling handler
func NewRunTourSellingHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	marketRepo market.MarketRepository,
	routingClient routing.RoutingClient,
	graphProvider system.ISystemGraphProvider,
) *RunTourSellingHandler {
	return &RunTourSellingHandler{
		mediator:      mediator,
		shipRepo:      shipRepo,
		marketRepo:    marketRepo,
		routingClient: routingClient,
		graphProvider: graphProvider,
	}
}

// Handle executes the tour selling command
func (h *RunTourSellingHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*RunTourSellingCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	// Load ship
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("failed to load ship: %w", err)
	}

	// Execute selling route
	marketsVisited, revenue, itemsSold, err := h.executeSellRoute(ctx, cmd, ship)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sell route: %w", err)
	}

	return &RunTourSellingResponse{
		MarketsVisited: marketsVisited,
		TotalRevenue:   revenue,
		ItemsSold:      itemsSold,
	}, nil
}

// executeSellRoute finds best markets and sells cargo using globally optimized fueled tour
func (h *RunTourSellingHandler) executeSellRoute(
	ctx context.Context,
	cmd *RunTourSellingCommand,
	ship *navigation.Ship,
) (int, int, []SoldItem, error) {
	logger := common.LoggerFromContext(ctx)

	// Find best markets for cargo (returns map of market -> goods to sell there)
	marketGoods, err := h.findBestMarketsForCargo(ctx, ship, cmd.PlayerID.Value())
	if err != nil {
		return 0, 0, nil, fmt.Errorf("failed to find markets: %w", err)
	}

	if len(marketGoods) == 0 {
		logger.Log("WARNING", "No markets found for cargo", nil)
		return 0, 0, nil, nil
	}

	// Extract market list
	markets := make([]string, 0, len(marketGoods))
	for market := range marketGoods {
		markets = append(markets, market)
	}

	// Get system graph for waypoint data
	systemSymbol := ship.CurrentLocation().SystemSymbol
	graphResult, err := h.graphProvider.GetGraph(ctx, systemSymbol, false, cmd.PlayerID.Value())
	if err != nil {
		return 0, 0, nil, fmt.Errorf("failed to get system graph: %w", err)
	}

	// Extract waypoint data from graph
	waypointData, err := extractWaypointData(graphResult.Graph)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("failed to extract waypoint data: %w", err)
	}

	// Reload ship to get current fuel
	transportShip, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("failed to reload ship: %w", err)
	}

	// Determine return waypoint
	returnWaypoint := cmd.ReturnWaypoint
	if returnWaypoint == "" {
		returnWaypoint = transportShip.CurrentLocation().Symbol
	}

	// Call OptimizeFueledTour for globally optimized route with return
	tourRequest := &routing.FueledTourRequest{
		SystemSymbol:    systemSymbol,
		StartWaypoint:   transportShip.CurrentLocation().Symbol,
		TargetWaypoints: markets,
		ReturnWaypoint:  returnWaypoint,
		CurrentFuel:     transportShip.Fuel().Current,
		FuelCapacity:    transportShip.FuelCapacity(),
		EngineSpeed:     transportShip.EngineSpeed(),
		AllWaypoints:    waypointData,
	}

	tourResponse, err := h.routingClient.OptimizeFueledTour(ctx, tourRequest)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Fueled tour optimization failed: %v", err), nil)
		return 0, 0, nil, fmt.Errorf("failed to optimize fueled tour: %w", err)
	}

	logger.Log("INFO", fmt.Sprintf("Fueled tour planned: %d markets, %d legs, %d refuel stops, total time %ds",
		len(tourResponse.VisitOrder), len(tourResponse.Legs), tourResponse.RefuelStops, tourResponse.TotalTimeSeconds), nil)

	marketsVisited := 0
	totalRevenue := 0
	var itemsSold []SoldItem

	// Execute each leg of the optimized tour
	for _, leg := range tourResponse.Legs {
		// Handle refuel before departing if needed
		if leg.RefuelBefore && leg.RefuelAmount > 0 {
			logger.Log("INFO", fmt.Sprintf("Refueling %d units before leg to %s", leg.RefuelAmount, leg.ToWaypoint), nil)

			// Make sure we're docked to refuel - pass ship so it's updated in place
			dockCmd := &shipTypes.DockShipCommand{
				Ship:     transportShip,
				PlayerID: cmd.PlayerID,
			}
			h.mediator.Send(ctx, dockCmd)

			refuelAmount := leg.RefuelAmount
			refuelCmd := &shipTypes.RefuelShipCommand{
				Ship:     transportShip,
				PlayerID: cmd.PlayerID,
				Units:    &refuelAmount,
			}
			_, err := h.mediator.Send(ctx, refuelCmd)
			if err != nil {
				logger.Log("WARNING", fmt.Sprintf("Failed to refuel: %v", err), nil)
			}
		}

		// Navigate to destination using the flight mode from routing service
		logger.Log("INFO", fmt.Sprintf("Navigating %s -> %s (%s mode, %d fuel)",
			leg.FromWaypoint, leg.ToWaypoint, leg.FlightMode, leg.FuelCost), nil)

		// Orbit before navigating - pass ship so it's updated in place
		orbitCmd := &shipTypes.OrbitShipCommand{
			Ship:     transportShip,
			PlayerID: cmd.PlayerID,
		}
		h.mediator.Send(ctx, orbitCmd)

		// Use the flight mode that was calculated by routing service - pass ship
		navCmd := &shipTypes.NavigateDirectCommand{
			Ship:        transportShip,
			Destination: leg.ToWaypoint,
			PlayerID:    cmd.PlayerID,
			FlightMode:  leg.FlightMode,
		}

		navResp, err := h.mediator.Send(ctx, navCmd)
		if err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to navigate to %s: %v", leg.ToWaypoint, err), nil)
			continue
		}

		// Wait for navigation to complete before proceeding
		if navResult, ok := navResp.(*shipTypes.NavigateDirectResponse); ok {
			if navResult.Status == "navigating" && navResult.ArrivalTime > 0 {
				logger.Log("INFO", fmt.Sprintf("Waiting %d seconds for ship to arrive at %s", navResult.ArrivalTime, leg.ToWaypoint), nil)
				select {
				case <-time.After(time.Duration(navResult.ArrivalTime) * time.Second):
					// Navigation complete
				case <-ctx.Done():
					return marketsVisited, totalRevenue, itemsSold, ctx.Err()
				}
			}
		}

		// Check if this is a market we need to sell at (not the return leg)
		goodsToSell := marketGoods[leg.ToWaypoint]
		if len(goodsToSell) > 0 {
			// Dock at market before selling - pass ship so it's updated in place
			dockCmd := &shipTypes.DockShipCommand{
				Ship:     transportShip,
				PlayerID: cmd.PlayerID,
			}
			_, err = h.mediator.Send(ctx, dockCmd)
			if err != nil {
				logger.Log("WARNING", fmt.Sprintf("Failed to dock at market %s: %v", leg.ToWaypoint, err), nil)
				continue
			}

			// OPTIMIZATION: Ship is updated in place by DockShipHandler (no reload needed)

			// Create set of goods to sell at this market
			goodsSet := make(map[string]bool)
			for _, good := range goodsToSell {
				goodsSet[good] = true
			}

			// Sell only the goods designated for this market
			for _, item := range transportShip.Cargo().Inventory {
				if !goodsSet[item.Symbol] {
					continue
				}

				sellCmd := &appShip.SellCargoCommand{
					ShipSymbol: cmd.ShipSymbol,
					PlayerID:   cmd.PlayerID,
					GoodSymbol: item.Symbol,
					Units:      item.Units,
				}

				sellResp, err := h.mediator.Send(ctx, sellCmd)
				if err != nil {
					logger.Log("WARNING", fmt.Sprintf("Failed to sell %s at %s: %v", item.Symbol, leg.ToWaypoint, err), nil)
					continue
				}

				sellResult := sellResp.(*appShip.SellCargoResponse)
				totalRevenue += sellResult.TotalRevenue

				itemsSold = append(itemsSold, SoldItem{
					Symbol:  item.Symbol,
					Units:   item.Units,
					Revenue: sellResult.TotalRevenue,
					Market:  leg.ToWaypoint,
				})

				logger.Log("INFO", fmt.Sprintf("Sold %d units of %s for %d credits",
					item.Units, item.Symbol, sellResult.TotalRevenue), nil)
			}

			marketsVisited++
		}
	}

	return marketsVisited, totalRevenue, itemsSold, nil
}

// findBestMarketsForCargo finds the best markets for the ship's cargo
// Returns a map of market waypoint -> list of goods to sell there
func (h *RunTourSellingHandler) findBestMarketsForCargo(
	ctx context.Context,
	ship *navigation.Ship,
	playerID int,
) (map[string][]string, error) {
	// Map market -> goods to sell there
	marketGoods := make(map[string][]string)
	systemSymbol := ship.CurrentLocation().SystemSymbol

	// Find best market for each cargo item (highest purchase price)
	for _, item := range ship.Cargo().Inventory {
		result, err := h.marketRepo.FindBestMarketBuying(ctx, item.Symbol, systemSymbol, playerID)
		if err != nil || result == nil {
			continue
		}

		marketGoods[result.WaypointSymbol] = append(marketGoods[result.WaypointSymbol], item.Symbol)
	}

	return marketGoods, nil
}

// FindNearestFuelStation finds the nearest waypoint with fuel to the target waypoint
func FindNearestFuelStation(graph *system.NavigationGraph, targetWaypoint string) string {
	// Extract waypoint data and convert to domain value objects
	waypointData, err := extractWaypointData(graph)
	if err != nil || len(waypointData) == 0 {
		return ""
	}

	// Convert to domain Waypoint value objects
	waypoints := make(map[string]*domainShared.Waypoint)
	for _, wp := range waypointData {
		waypoint, err := domainShared.NewWaypoint(wp.Symbol, wp.X, wp.Y)
		if err != nil {
			continue
		}
		waypoint.HasFuel = wp.HasFuel
		waypoints[wp.Symbol] = waypoint
	}

	// Get target waypoint
	targetWP, ok := waypoints[targetWaypoint]
	if !ok {
		return ""
	}

	// Find nearest fuel station using domain DistanceTo method
	var nearestSymbol string
	nearestDistance := float64(1e9)

	for symbol, wp := range waypoints {
		if !wp.HasFuel {
			continue
		}

		distance := targetWP.DistanceTo(wp)
		if distance < nearestDistance {
			nearestDistance = distance
			nearestSymbol = symbol
		}
	}

	return nearestSymbol
}

// extractWaypointData converts graph format to routing waypoint data
func extractWaypointData(graph *system.NavigationGraph) ([]*system.WaypointData, error) {
	waypointData := make([]*system.WaypointData, 0, len(graph.Waypoints))

	for symbol, waypoint := range graph.Waypoints {
		waypointData = append(waypointData, &system.WaypointData{
			Symbol:  symbol,
			X:       waypoint.X,
			Y:       waypoint.Y,
			HasFuel: waypoint.HasFuel,
		})
	}

	return waypointData, nil
}
