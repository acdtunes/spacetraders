package services

import (
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// FleetPoolManager handles mediator access for the coordinator
type FleetPoolManager struct {
	mediator common.Mediator
}

// NewFleetPoolManager creates a new fleet pool manager service
func NewFleetPoolManager(
	mediator common.Mediator,
) *FleetPoolManager {
	return &FleetPoolManager{
		mediator: mediator,
	}
}

// GetMediator returns the mediator for sending commands
func (m *FleetPoolManager) GetMediator() common.Mediator {
	return m.mediator
}
