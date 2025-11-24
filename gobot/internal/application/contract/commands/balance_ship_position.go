package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	contractTypes "github.com/andrescamacho/spacetraders-go/internal/application/contract/types"
	appShipCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	domainContainer "github.com/andrescamacho/spacetraders-go/internal/domain/container"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// Type aliases for convenience
type BalanceShipPositionCommand = contractTypes.BalanceShipPositionCommand
type BalanceShipPositionResponse = contractTypes.BalanceShipPositionResponse

// ContainerRepository defines persistence operations for containers
type ContainerRepository interface {
	Add(ctx context.Context, containerEntity *domainContainer.Container, commandType string) error
	Remove(ctx context.Context, containerID string, playerID int) error
}

// BalanceShipPositionHandler implements ship position balancing logic.
//
// This handler repositions a ship to optimize the overall fleet distribution
// across markets using a Distance + Coverage Score algorithm.
type BalanceShipPositionHandler struct {
	mediator              common.Mediator
	shipRepo              navigation.ShipRepository
	shipAssignmentRepo    container.ShipAssignmentRepository
	containerRepo         ContainerRepository
	graphProvider         system.ISystemGraphProvider
	marketRepo            MarketRepository
	balancer              *domainContract.ShipBalancer
}

// NewBalanceShipPositionHandler creates a new balance ship position handler
func NewBalanceShipPositionHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	shipAssignmentRepo container.ShipAssignmentRepository,
	containerRepo ContainerRepository,
	graphProvider system.ISystemGraphProvider,
	marketRepo MarketRepository,
) *BalanceShipPositionHandler {
	return &BalanceShipPositionHandler{
		mediator:           mediator,
		shipRepo:           shipRepo,
		shipAssignmentRepo: shipAssignmentRepo,
		containerRepo:      containerRepo,
		graphProvider:      graphProvider,
		marketRepo:         marketRepo,
		balancer:           domainContract.NewShipBalancer(),
	}
}

// Handle executes the ship position balancing command
func (h *BalanceShipPositionHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*BalanceShipPositionCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", "Balancing ship position", map[string]interface{}{
		"action":      "balance_ship_position",
		"ship_symbol": cmd.ShipSymbol,
	})

	// 1. Create temporary container record to satisfy foreign key constraint
	balancingContainerID := fmt.Sprintf("ship-balancing-%s", cmd.ShipSymbol)
	metadata := map[string]interface{}{
		"ship_symbol":    cmd.ShipSymbol,
		"coordinator_id": cmd.CoordinatorID,
	}

	// Use coordinator ID as parent if provided (spawned by coordinator)
	// Otherwise nil for manual/standalone balancing operations
	var parentContainerID *string
	if cmd.CoordinatorID != "" {
		parentContainerID = &cmd.CoordinatorID
	}

	balancingContainer := domainContainer.NewContainer(
		balancingContainerID,
		domainContainer.ContainerTypeBalancing,
		cmd.PlayerID.Value(),
		1, // maxIterations: balancing is single-shot
		parentContainerID,
		metadata,
		shared.NewRealClock(),
	)

	// Create container record in database
	if err := h.containerRepo.Add(ctx, balancingContainer, "balance_ship_position"); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to create balancing container: %v", err), nil)
		// Continue anyway - balancing is best-effort
	}

	// Add operation context to ctx for transaction tracking
	opContext := shared.NewOperationContext(balancingContainerID, "balance_ship_position")
	ctx = shared.WithOperationContext(ctx, opContext)

	// Ensure container is cleaned up on exit (success or failure)
	defer func() {
		_ = h.containerRepo.Remove(ctx, balancingContainerID, cmd.PlayerID.Value())
	}()

	// 2. Create temporary assignment to prevent this ship from being selected elsewhere
	assignment := container.NewShipAssignment(cmd.ShipSymbol, cmd.PlayerID.Value(), balancingContainerID, shared.NewRealClock())
	if err := h.shipAssignmentRepo.Assign(ctx, assignment); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to create balancing assignment: %v", err), nil)
		// Continue anyway - balancing is best-effort
	}
	// Ensure assignment is released on exit (success or failure)
	defer func() {
		_ = h.shipAssignmentRepo.Release(ctx, cmd.ShipSymbol, cmd.PlayerID.Value(), "balancing_complete")
	}()

	// 2. Fetch ship
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("failed to load ship %s: %w", cmd.ShipSymbol, err)
	}

	// 2. Discover all markets in ship's system
	systemSymbol := ship.CurrentLocation().SystemSymbol
	marketSymbols, err := h.marketRepo.FindAllMarketsInSystem(ctx, systemSymbol, cmd.PlayerID.Value())
	if err != nil {
		return nil, fmt.Errorf("failed to discover markets: %w", err)
	}

	if len(marketSymbols) == 0 {
		logger.Log("WARNING", "No markets found in system - skipping balancing", nil)
		return &BalanceShipPositionResponse{Navigated: false}, nil
	}

	// 3. Fetch market waypoint objects from graph
	marketWaypoints, err := h.fetchMarketWaypoints(ctx, marketSymbols, systemSymbol, cmd.PlayerID.Value())
	if err != nil {
		return nil, fmt.Errorf("failed to fetch market waypoints: %w", err)
	}

	// 4. Fetch all idle light hauler ships
	// Ships with active assignments (including other ships being balanced) are automatically excluded
	idleHaulers, _, err := appContract.FindIdleLightHaulers(ctx, cmd.PlayerID, h.shipRepo, h.shipAssignmentRepo)
	if err != nil {
		return nil, fmt.Errorf("failed to find idle haulers: %w", err)
	}

	logger.Log("INFO", "Calculating optimal balancing position", map[string]interface{}{
		"markets":      len(marketWaypoints),
		"idle_haulers": len(idleHaulers),
	})

	// 5. Use domain service to select optimal market
	result, err := h.balancer.SelectOptimalBalancingPosition(ship, marketWaypoints, idleHaulers)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate balancing position: %w", err)
	}

	logger.Log("INFO", "Optimal market selected", map[string]interface{}{
		"target_market":   result.TargetMarket.Symbol,
		"assigned_ships":  result.AssignedShips,
		"distance":        result.Distance,
		"score":           result.Score,
	})

	// 6. Navigate ship to target market
	navigated, err := h.navigateToMarket(ctx, cmd.ShipSymbol, result.TargetMarket.Symbol, cmd.PlayerID)
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("Navigation failed: %v", err), nil)
		return nil, fmt.Errorf("failed to navigate to market: %w", err)
	}

	logger.Log("INFO", "Ship position balanced", map[string]interface{}{
		"ship_symbol":   cmd.ShipSymbol,
		"target_market": result.TargetMarket.Symbol,
		"navigated":     navigated,
	})

	return &BalanceShipPositionResponse{
		TargetMarket:  result.TargetMarket.Symbol,
		AssignedShips: result.AssignedShips,
		Distance:      result.Distance,
		Score:         result.Score,
		Navigated:     navigated,
	}, nil
}

// fetchMarketWaypoints fetches waypoint objects for market symbols from the graph provider
func (h *BalanceShipPositionHandler) fetchMarketWaypoints(
	ctx context.Context,
	marketSymbols []string,
	systemSymbol string,
	playerID int,
) ([]*shared.Waypoint, error) {
	// Get system graph
	graphResult, err := h.graphProvider.GetGraph(ctx, systemSymbol, false, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get system graph: %w", err)
	}

	// Extract waypoint objects
	var waypoints []*shared.Waypoint
	for _, symbol := range marketSymbols {
		waypoint, ok := graphResult.Graph.Waypoints[symbol]
		if !ok {
			continue // Skip markets not found in graph
		}
		waypoints = append(waypoints, waypoint)
	}

	return waypoints, nil
}

// navigateToMarket navigates the ship to the target market
func (h *BalanceShipPositionHandler) navigateToMarket(
	ctx context.Context,
	shipSymbol string,
	targetWaypoint string,
	playerID shared.PlayerID,
) (bool, error) {
	navigateCmd := &appShipCmd.NavigateRouteCommand{
		ShipSymbol:  shipSymbol,
		Destination: targetWaypoint,
		PlayerID:    playerID,
	}

	_, err := h.mediator.Send(ctx, navigateCmd)
	if err != nil {
		return false, err
	}

	return true, nil
}
