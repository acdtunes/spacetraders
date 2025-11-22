package services

import (
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainContainer "github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// FleetPoolManager handles mediator access for the coordinator
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

// GetMediator returns the mediator for sending commands
func (m *FleetPoolManager) GetMediator() common.Mediator {
	return m.mediator
}

// NOTE: ValidateShipAvailability, InitializeShipPool, TransferShipBackToCoordinator,
// and ExecuteRebalancingIfNeeded have been removed as they are no longer needed.
// The coordinator now dynamically discovers idle haulers without pre-assignment.
