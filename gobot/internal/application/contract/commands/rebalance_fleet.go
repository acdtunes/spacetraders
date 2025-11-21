package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractTypes "github.com/andrescamacho/spacetraders-go/internal/application/contract/types"
	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	appShipCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// Type aliases for convenience
type RebalanceContractFleetCommand = contractTypes.RebalanceContractFleetCommand
type RebalanceContractFleetResponse = contractTypes.RebalanceContractFleetResponse

// RebalanceContractFleetHandler implements fleet rebalancing logic
type RebalanceContractFleetHandler struct {
	mediator            common.Mediator
	shipRepo            navigation.ShipRepository
	shipAssignmentRepo  container.ShipAssignmentRepository
	graphProvider       system.ISystemGraphProvider
	marketRepo          MarketRepository
	converter           system.IWaypointConverter
	distributionChecker *appContract.DistributionChecker
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
	converter system.IWaypointConverter,
) *RebalanceContractFleetHandler {
	return &RebalanceContractFleetHandler{
		mediator:            mediator,
		shipRepo:            shipRepo,
		shipAssignmentRepo:  shipAssignmentRepo,
		graphProvider:       graphProvider,
		marketRepo:          marketRepo,
		converter:           converter,
		distributionChecker: appContract.NewDistributionChecker(graphProvider, converter),
	}
}

// Handle executes the fleet rebalancing command
func (h *RebalanceContractFleetHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*RebalanceContractFleetCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	result := &RebalanceContractFleetResponse{
		ShipsMoved:         0,
		TargetMarkets:      []string{},
		AverageDistance:    0,
		DistanceThreshold:  500.0, // Default threshold
		RebalancingSkipped: false,
		SkipReason:         "",
		Assignments:        make(map[string]string),
	}

	targetMarkets, skip := h.discoverSystemMarkets(ctx, cmd, result)
	if skip {
		return result, nil
	}

	ships, skip := h.getCoordinatorShips(ctx, cmd, result)
	if skip {
		return result, nil
	}

	needsRebalancing, skip := h.checkIfRebalancingNeeded(ctx, cmd, ships, targetMarkets, result)
	if skip {
		return result, nil
	}

	if !needsRebalancing {
		return result, nil
	}

	if err := h.assignShipsToMarkets(ctx, cmd, ships, targetMarkets, result); err != nil {
		return nil, err
	}

	if err := h.executeParallelRepositioning(ctx, cmd, ships, result); err != nil {
		return nil, err
	}

	return result, nil
}

func (h *RebalanceContractFleetHandler) discoverSystemMarkets(
	ctx context.Context,
	cmd *RebalanceContractFleetCommand,
	result *RebalanceContractFleetResponse,
) ([]string, bool) {
	logger := common.LoggerFromContext(ctx)

	logger.Log("INFO", "Discovering all markets in system", nil)
	targetMarkets, err := h.marketRepo.FindAllMarketsInSystem(ctx, cmd.SystemSymbol, cmd.PlayerID.Value())
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("Failed to discover markets: %v", err), nil)
		return nil, true
	}

	result.TargetMarkets = targetMarkets

	if len(targetMarkets) == 0 {
		logger.Log("WARNING", "No markets found in system - skipping rebalancing", nil)
		result.RebalancingSkipped = true
		result.SkipReason = "No markets available in system"
		return nil, true
	}

	logger.Log("INFO", fmt.Sprintf("Found %d markets in system: %v", len(targetMarkets), targetMarkets), nil)
	return targetMarkets, false
}

func (h *RebalanceContractFleetHandler) getCoordinatorShips(
	ctx context.Context,
	cmd *RebalanceContractFleetCommand,
	result *RebalanceContractFleetResponse,
) ([]*navigation.Ship, bool) {
	logger := common.LoggerFromContext(ctx)

	shipSymbols, err := appContract.FindCoordinatorShips(ctx, cmd.CoordinatorID, cmd.PlayerID.Value(), h.shipAssignmentRepo)
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("Failed to find coordinator ships: %v", err), nil)
		return nil, true
	}

	logger.Log("INFO", fmt.Sprintf("Found %d ships in coordinator pool", len(shipSymbols)), nil)

	if len(shipSymbols) == 0 {
		logger.Log("INFO", "No ships in coordinator pool - skipping rebalancing", nil)
		result.RebalancingSkipped = true
		result.SkipReason = "No ships in coordinator pool"
		return nil, true
	}

	ships := make([]*navigation.Ship, 0, len(shipSymbols))
	for _, shipSymbol := range shipSymbols {
		ship, err := h.shipRepo.FindBySymbol(ctx, shipSymbol, cmd.PlayerID)
		if err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to load ship %s: %v", shipSymbol, err), nil)
			continue
		}
		ships = append(ships, ship)
	}

	if len(ships) == 0 {
		logger.Log("WARNING", "No ships could be loaded - skipping rebalancing", nil)
		result.RebalancingSkipped = true
		result.SkipReason = "Failed to load ship data"
		return nil, true
	}

	return ships, false
}

func (h *RebalanceContractFleetHandler) checkIfRebalancingNeeded(
	ctx context.Context,
	cmd *RebalanceContractFleetCommand,
	ships []*navigation.Ship,
	targetMarkets []string,
	result *RebalanceContractFleetResponse,
) (bool, bool) {
	logger := common.LoggerFromContext(ctx)

	logger.Log("INFO", fmt.Sprintf("Checking distribution (threshold: %.1f distance units)", result.DistanceThreshold), nil)
	needsRebalancing, avgDistance, err := h.distributionChecker.IsRebalancingNeeded(
		ctx,
		ships,
		targetMarkets,
		cmd.SystemSymbol,
		cmd.PlayerID.Value(),
		result.DistanceThreshold,
	)
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("Failed to check distribution: %v", err), nil)
		return false, true
	}

	result.AverageDistance = avgDistance
	logger.Log("INFO", fmt.Sprintf("Average distance to nearest target: %.1f units", avgDistance), nil)

	if !needsRebalancing {
		logger.Log("INFO", "Fleet already well-distributed - skipping rebalancing", nil)
		result.RebalancingSkipped = true
		result.SkipReason = fmt.Sprintf("Fleet well-distributed (avg distance %.1f < threshold %.1f)", avgDistance, result.DistanceThreshold)
		return false, true
	}

	return true, false
}

func (h *RebalanceContractFleetHandler) assignShipsToMarkets(
	ctx context.Context,
	cmd *RebalanceContractFleetCommand,
	ships []*navigation.Ship,
	targetMarkets []string,
	result *RebalanceContractFleetResponse,
) error {
	logger := common.LoggerFromContext(ctx)

	logger.Log("INFO", "Assigning ships to markets...", nil)
	assignments, err := h.distributionChecker.AssignShipsToMarkets(ctx, ships, targetMarkets, cmd.SystemSymbol, cmd.PlayerID.Value())
	if err != nil {
		return fmt.Errorf("failed to assign ships to markets: %w", err)
	}

	result.Assignments = assignments
	logger.Log("INFO", fmt.Sprintf("Generated %d ship assignments", len(assignments)), nil)
	return nil
}

func (h *RebalanceContractFleetHandler) executeParallelRepositioning(
	ctx context.Context,
	cmd *RebalanceContractFleetCommand,
	ships []*navigation.Ship,
	result *RebalanceContractFleetResponse,
) error {
	logger := common.LoggerFromContext(ctx)

	logger.Log("INFO", "Starting ship repositioning (parallel)...", nil)

	type navResult struct {
		shipSymbol string
		success    bool
		err        error
	}
	resultsChan := make(chan navResult, len(ships))

	shipsToReposition := 0

	for _, ship := range ships {
		targetMarket, hasAssignment := result.Assignments[ship.ShipSymbol()]
		if !hasAssignment {
			continue
		}

		if ship.CurrentLocation().Symbol == targetMarket {
			logger.Log("INFO", fmt.Sprintf("Ship %s already at target %s - skipping", ship.ShipSymbol(), targetMarket), nil)
			continue
		}

		shipsToReposition++

		go func(shipSymbol, destination string) {
			logger.Log("INFO", fmt.Sprintf("Repositioning %s to %s", shipSymbol, destination), nil)

			navigateCmd := &appShipCmd.NavigateRouteCommand{
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
	return nil
}
