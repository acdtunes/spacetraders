package helpers

import (
	"context"
	"fmt"
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// MockShipAssignmentRepository is a test double for ShipAssignmentRepository interface
type MockShipAssignmentRepository struct {
	mu          sync.RWMutex
	assignments map[string]*container.ShipAssignment // shipSymbol -> assignment
}

// NewMockShipAssignmentRepository creates a new mock ship assignment repository
func NewMockShipAssignmentRepository() *MockShipAssignmentRepository {
	return &MockShipAssignmentRepository{
		assignments: make(map[string]*container.ShipAssignment),
	}
}

// Assign creates or updates a ship assignment
func (m *MockShipAssignmentRepository) Assign(ctx context.Context, assignment *container.ShipAssignment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.assignments[assignment.ShipSymbol()] = assignment
	return nil
}

// FindByShip retrieves the active assignment for a ship
func (m *MockShipAssignmentRepository) FindByShip(ctx context.Context, shipSymbol string, playerID int) (*container.ShipAssignment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	assignment, ok := m.assignments[shipSymbol]
	if !ok {
		return nil, nil
	}

	return assignment, nil
}

// FindByShipSymbol retrieves the assignment for a ship by symbol (alias for FindByShip)
func (m *MockShipAssignmentRepository) FindByShipSymbol(ctx context.Context, shipSymbol string, playerID int) (*container.ShipAssignment, error) {
	return m.FindByShip(ctx, shipSymbol, playerID)
}

// FindByContainer retrieves all ship assignments for a container
func (m *MockShipAssignmentRepository) FindByContainer(ctx context.Context, containerID string, playerID int) ([]*container.ShipAssignment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []*container.ShipAssignment
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

// Transfer transfers a ship assignment from one container to another
func (m *MockShipAssignmentRepository) Transfer(ctx context.Context, shipSymbol string, fromContainerID string, toContainerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	assignment, ok := m.assignments[shipSymbol]
	if !ok {
		return fmt.Errorf("ship assignment not found: %s", shipSymbol)
	}

	if assignment.ContainerID() != fromContainerID {
		return fmt.Errorf("ship %s is not assigned to container %s", shipSymbol, fromContainerID)
	}

	// Create a new assignment with the new container ID
	newAssignment := container.NewShipAssignment(shipSymbol, assignment.PlayerID(), toContainerID, nil)
	m.assignments[shipSymbol] = newAssignment
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
	m.assignments = make(map[string]*container.ShipAssignment)
	return count, nil
}
