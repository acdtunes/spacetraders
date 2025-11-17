package helpers

import (
	"context"
	"fmt"
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
)

// MockShipAssignmentRepository is a test double for ShipAssignmentRepository interface
type MockShipAssignmentRepository struct {
	mu          sync.RWMutex
	assignments map[string]*daemon.ShipAssignment // shipSymbol -> assignment
}

// NewMockShipAssignmentRepository creates a new mock ship assignment repository
func NewMockShipAssignmentRepository() *MockShipAssignmentRepository {
	return &MockShipAssignmentRepository{
		assignments: make(map[string]*daemon.ShipAssignment),
	}
}

// Insert creates a new ship assignment record
func (m *MockShipAssignmentRepository) Insert(ctx context.Context, assignment *daemon.ShipAssignment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.assignments[assignment.ShipSymbol()] = assignment
	return nil
}

// FindByShip retrieves the active assignment for a ship
func (m *MockShipAssignmentRepository) FindByShip(ctx context.Context, shipSymbol string, playerID int) (*daemon.ShipAssignment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	assignment, ok := m.assignments[shipSymbol]
	if !ok {
		return nil, nil
	}

	return assignment, nil
}

// FindByContainer retrieves all ship assignments for a container
func (m *MockShipAssignmentRepository) FindByContainer(ctx context.Context, containerID string, playerID int) ([]*daemon.ShipAssignment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []*daemon.ShipAssignment
	for _, assignment := range m.assignments {
		if assignment.ContainerID() == containerID {
			result = append(result, assignment)
		}
	}

	return result, nil
}

// Release marks a ship assignment as released
func (m *MockShipAssignmentRepository) Release(ctx context.Context, shipSymbol string, playerID int, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.assignments[shipSymbol]; !ok {
		return fmt.Errorf("ship assignment not found: %s", shipSymbol)
	}

	delete(m.assignments, shipSymbol)
	return nil
}

// ReleaseByContainer releases all ship assignments for a container
func (m *MockShipAssignmentRepository) ReleaseByContainer(ctx context.Context, containerID string, playerID int, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for shipSymbol, assignment := range m.assignments {
		if assignment.ContainerID() == containerID {
			delete(m.assignments, shipSymbol)
		}
	}

	return nil
}

// ReleaseAllActive releases all active ship assignments (used for daemon startup cleanup)
func (m *MockShipAssignmentRepository) ReleaseAllActive(ctx context.Context, reason string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	count := len(m.assignments)
	m.assignments = make(map[string]*daemon.ShipAssignment)
	return count, nil
}
