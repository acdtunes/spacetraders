package contract

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appShip "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// RebalanceContractFleetCommand rebalances idle contract fleet ships across strategic markets
type RebalanceContractFleetCommand struct {
	CoordinatorID string
	PlayerID      int
	SystemSymbol  string
}

// RebalanceContractFleetResponse contains rebalancing execution results
type RebalanceContractFleetResponse struct {
	ShipsMoved          int
	TargetMarkets       []string
	AverageDistance     float64
	DistanceThreshold   float64
	RebalancingSkipped  bool
	SkipReason          string
	Assignments         map[string]string // ship symbol -> market waypoint
}

// RebalanceContractFleetHandler implements fleet rebalancing logic
type RebalanceContractFleetHandler struct {
	mediator            common.Mediator
	shipRepo            navigation.ShipRepository
	shipAssignmentRepo  container.ShipAssignmentRepository
	graphProvider       system.ISystemGraphProvider
	marketRepo          MarketRepository
	distributionChecker *DistributionChecker
}

// MarketRepository defines the interface for market data access needed by rebalancing
type MarketRepository interface {
	FindAllMarketsInSystem(ctx context.Context, systemSymbol string, playerID int) ([]string, error)
}

// NewRebalanceContractFleetHandler creates a new rebalance fleet handler
func NewRebalanceContractFleetHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	shipAssignmentRepo container.ShipAssignmentRepository,
	graphProvider system.ISystemGraphProvider,
	marketRepo MarketRepository,
) *RebalanceContractFleetHandler {
	return &RebalanceContractFleetHandler{
		mediator:            mediator,
		shipRepo:            shipRepo,
		shipAssignmentRepo:  shipAssignmentRepo,
		graphProvider:       graphProvider,
		marketRepo:          marketRepo,
		distributionChecker: NewDistributionChecker(graphProvider),
	}
}

// Handle executes the fleet rebalancing command
func (h *RebalanceContractFleetHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*RebalanceContractFleetCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	logger := common.LoggerFromContext(ctx)

	result := &RebalanceContractFleetResponse{
		ShipsMoved:         0,
		TargetMarkets:      []string{},
		AverageDistance:    0,
		DistanceThreshold:  500.0, // Default threshold
		RebalancingSkipped: false,
		SkipReason:         "",
		Assignments:        make(map[string]string),
	}

	// Step 1: Get all markets in the system
	logger.Log("INFO", "Discovering all markets in system", nil)
	targetMarkets, err := h.marketRepo.FindAllMarketsInSystem(ctx, cmd.SystemSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("failed to discover markets in system: %w", err)
	}

	result.TargetMarkets = targetMarkets

	// Step 2: Check if we have market data
	if len(targetMarkets) == 0 {
		logger.Log("WARNING", "No markets found in system - skipping rebalancing", nil)
		result.RebalancingSkipped = true
		result.SkipReason = "No markets available in system"
		return result, nil
	}

	logger.Log("INFO", fmt.Sprintf("Found %d markets in system: %v", len(targetMarkets), targetMarkets), nil)

	// Step 3: Get idle ships from coordinator pool
	shipSymbols, err := FindCoordinatorShips(ctx, cmd.CoordinatorID, cmd.PlayerID, h.shipAssignmentRepo)
	if err != nil {
		return nil, fmt.Errorf("failed to find coordinator ships: %w", err)
	}

	logger.Log("INFO", fmt.Sprintf("Found %d ships in coordinator pool", len(shipSymbols)), nil)

	if len(shipSymbols) == 0 {
		logger.Log("INFO", "No ships in coordinator pool - skipping rebalancing", nil)
		result.RebalancingSkipped = true
		result.SkipReason = "No ships in coordinator pool"
		return result, nil
	}

	// Load ship objects from symbols
	ships := make([]*navigation.Ship, 0, len(shipSymbols))
	for _, shipSymbol := range shipSymbols {
		ship, err := h.shipRepo.FindBySymbol(ctx, shipSymbol, cmd.PlayerID)
		if err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to load ship %s: %v", shipSymbol, err), nil)
			continue // Skip ships that can't be loaded
		}
		ships = append(ships, ship)
	}

	if len(ships) == 0 {
		logger.Log("WARNING", "No ships could be loaded - skipping rebalancing", nil)
		result.RebalancingSkipped = true
		result.SkipReason = "Failed to load ship data"
		return result, nil
	}

	// Step 4: Check if rebalancing is needed
	logger.Log("INFO", fmt.Sprintf("Checking distribution (threshold: %.1f distance units)", result.DistanceThreshold), nil)
	needsRebalancing, avgDistance, err := h.distributionChecker.IsRebalancingNeeded(
		ctx,
		ships,
		targetMarkets,
		cmd.SystemSymbol,
		cmd.PlayerID,
		result.DistanceThreshold,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to check distribution: %w", err)
	}

	result.AverageDistance = avgDistance
	logger.Log("INFO", fmt.Sprintf("Average distance to nearest target: %.1f units", avgDistance), nil)

	if !needsRebalancing {
		logger.Log("INFO", "Fleet already well-distributed - skipping rebalancing", nil)
		result.RebalancingSkipped = true
		result.SkipReason = fmt.Sprintf("Fleet well-distributed (avg distance %.1f < threshold %.1f)", avgDistance, result.DistanceThreshold)
		return result, nil
	}

	// Step 5: Assign ships to markets
	logger.Log("INFO", "Assigning ships to markets...", nil)
	assignments, err := h.distributionChecker.AssignShipsToMarkets(ctx, ships, targetMarkets, cmd.SystemSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("failed to assign ships to markets: %w", err)
	}

	result.Assignments = assignments
	logger.Log("INFO", fmt.Sprintf("Generated %d ship assignments", len(assignments)), nil)

	// Step 6: Execute repositioning (parallel navigation)
	logger.Log("INFO", "Starting ship repositioning (parallel)...", nil)

	// Create a channel to collect results
	type navResult struct {
		shipSymbol string
		success    bool
		err        error
	}
	resultsChan := make(chan navResult, len(ships))

	// Track number of ships to reposition
	shipsToReposition := 0

	// Launch goroutines for parallel navigation
	for _, ship := range ships {
		targetMarket, hasAssignment := assignments[ship.ShipSymbol()]
		if !hasAssignment {
			continue
		}

		// Skip if ship is already at target market
		if ship.CurrentLocation().Symbol == targetMarket {
			logger.Log("INFO", fmt.Sprintf("Ship %s already at target %s - skipping", ship.ShipSymbol(), targetMarket), nil)
			continue
		}

		shipsToReposition++

		// Launch navigation in goroutine
		go func(shipSymbol, destination string) {
			logger.Log("INFO", fmt.Sprintf("Repositioning %s to %s", shipSymbol, destination), nil)

			navigateCmd := &appShip.NavigateShipCommand{
				ShipSymbol:  shipSymbol,
				Destination: destination,
				PlayerID:    cmd.PlayerID,
			}

			_, err := h.mediator.Send(ctx, navigateCmd)
			resultsChan <- navResult{
				shipSymbol: shipSymbol,
				success:    err == nil,
				err:        err,
			}
		}(ship.ShipSymbol(), targetMarket)
	}

	// Collect results from all goroutines
	for i := 0; i < shipsToReposition; i++ {
		navRes := <-resultsChan
		if navRes.success {
			result.ShipsMoved++
			logger.Log("INFO", fmt.Sprintf("Successfully repositioned %s", navRes.shipSymbol), nil)
		} else {
			logger.Log("ERROR", fmt.Sprintf("Failed to reposition %s: %v", navRes.shipSymbol, navRes.err), nil)
		}
	}

	logger.Log("INFO", fmt.Sprintf("Rebalancing complete: %d/%d ships repositioned", result.ShipsMoved, shipsToReposition), nil)
	return result, nil
}
