package services

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	contractTypes "github.com/andrescamacho/spacetraders-go/internal/application/contract/types"
	domainContainer "github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Type aliases for convenience
type RebalanceContractFleetCommand = contractTypes.RebalanceContractFleetCommand
type RebalanceContractFleetResponse = contractTypes.RebalanceContractFleetResponse

// FleetPoolManager handles ship pool initialization, validation, and rebalancing
type FleetPoolManager struct {
	mediator           common.Mediator
	shipRepo           navigation.ShipRepository
	shipAssignmentRepo domainContainer.ShipAssignmentRepository
}

// NewFleetPoolManager creates a new fleet pool manager service
func NewFleetPoolManager(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	shipAssignmentRepo domainContainer.ShipAssignmentRepository,
) *FleetPoolManager {
	return &FleetPoolManager{
		mediator:           mediator,
		shipRepo:           shipRepo,
		shipAssignmentRepo: shipAssignmentRepo,
	}
}

// ValidateShipAvailability validates that ships are not already assigned
func (m *FleetPoolManager) ValidateShipAvailability(
	ctx context.Context,
	shipSymbols []string,
	containerID string,
	playerID int,
) error {
	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", "Validating ship availability...", nil)

	for _, shipSymbol := range shipSymbols {
		assignment, err := m.shipAssignmentRepo.FindByShip(ctx, shipSymbol, playerID)
		if err != nil {
			return fmt.Errorf("failed to check assignment for %s: %w", shipSymbol, err)
		}

		if assignment != nil && assignment.Status() == "active" {
			return fmt.Errorf("ship %s is already assigned to container %s - cannot create overlapping coordinator",
				shipSymbol, assignment.ContainerID())
		}
	}

	return nil
}

// InitializeShipPool creates pool assignments for all ships
func (m *FleetPoolManager) InitializeShipPool(
	ctx context.Context,
	shipSymbols []string,
	containerID string,
	playerID int,
) error {
	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", fmt.Sprintf("Initializing ship pool with %d ships", len(shipSymbols)), nil)

	if err := appContract.CreatePoolAssignments(
		ctx,
		shipSymbols,
		containerID,
		playerID,
		m.shipAssignmentRepo,
	); err != nil {
		return fmt.Errorf("failed to create pool assignments: %w", err)
	}

	return nil
}

// TransferShipBackToCoordinator transfers ship from worker back to coordinator
func (m *FleetPoolManager) TransferShipBackToCoordinator(
	ctx context.Context,
	shipSymbol string,
	workerContainerID string,
	coordinatorContainerID string,
	playerID int,
) {
	logger := common.LoggerFromContext(ctx)

	if err := m.shipAssignmentRepo.Transfer(ctx, shipSymbol, workerContainerID, coordinatorContainerID); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to transfer ship %s back to coordinator: %v", shipSymbol, err), nil)
		// Fallback: try inserting new assignment if transfer fails
		assignment := domainContainer.NewShipAssignment(shipSymbol, playerID, coordinatorContainerID, nil)
		_ = m.shipAssignmentRepo.Assign(ctx, assignment)
	}
}

// ExecuteRebalancingIfNeeded triggers fleet rebalancing via mediator
func (m *FleetPoolManager) ExecuteRebalancingIfNeeded(
	ctx context.Context,
	coordinatorContainerID string,
	playerID shared.PlayerID,
	availableShips []string,
) {
	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", "Rebalance interval reached, checking fleet distribution...", nil)

	systemSymbol := m.extractSystemSymbolFromShips(ctx, availableShips, playerID.Value())
	if systemSymbol == "" {
		logger.Log("WARNING", "Could not determine system symbol for rebalancing", nil)
		return
	}

	rebalanceCmd := &RebalanceContractFleetCommand{
		CoordinatorID: coordinatorContainerID,
		PlayerID:      playerID,
		SystemSymbol:  systemSymbol,
	}

	rebalanceResp, err := m.mediator.Send(ctx, rebalanceCmd)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Rebalancing failed: %v", err), nil)
		return
	}

	result := rebalanceResp.(*RebalanceContractFleetResponse)
	if result.RebalancingSkipped {
		logger.Log("INFO", fmt.Sprintf("Rebalancing skipped: %s", result.SkipReason), nil)
	} else {
		logger.Log("INFO", fmt.Sprintf("Rebalancing complete: %d ships repositioned", result.ShipsMoved), nil)
	}
}

// extractSystemSymbolFromShips extracts system symbol from first ship in list
func (m *FleetPoolManager) extractSystemSymbolFromShips(
	ctx context.Context,
	availableShips []string,
	playerID int,
) string {
	if len(availableShips) == 0 {
		return ""
	}

	firstShip, err := m.shipRepo.FindBySymbol(ctx, availableShips[0], shared.MustNewPlayerID(playerID))
	if err != nil {
		return ""
	}

	currentLocation := firstShip.CurrentLocation().Symbol
	return shared.ExtractSystemSymbol(currentLocation)
}
