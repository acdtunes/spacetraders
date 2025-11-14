package helpers

import (
	"context"
	"fmt"
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// MockWaypointRepository is a test double for WaypointRepository interface
type MockWaypointRepository struct {
	mu        sync.RWMutex
	waypoints map[string]*shared.Waypoint // symbol -> waypoint
}

// NewMockWaypointRepository creates a new mock waypoint repository
func NewMockWaypointRepository() *MockWaypointRepository {
	return &MockWaypointRepository{
		waypoints: make(map[string]*shared.Waypoint),
	}
}

// AddWaypoint adds a waypoint to the mock repository
func (m *MockWaypointRepository) AddWaypoint(waypoint *shared.Waypoint) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.waypoints[waypoint.Symbol] = waypoint
}

// FindBySymbol retrieves a waypoint by symbol
func (m *MockWaypointRepository) FindBySymbol(ctx context.Context, symbol, systemSymbol string) (*shared.Waypoint, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	waypoint, ok := m.waypoints[symbol]
	if !ok {
		return nil, fmt.Errorf("waypoint not found: %s", symbol)
	}

	return waypoint, nil
}

// ListBySystem retrieves all waypoints in a system
func (m *MockWaypointRepository) ListBySystem(ctx context.Context, systemSymbol string) ([]*shared.Waypoint, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var waypoints []*shared.Waypoint
	for _, waypoint := range m.waypoints {
		if waypoint.SystemSymbol == systemSymbol {
			waypoints = append(waypoints, waypoint)
		}
	}

	return waypoints, nil
}

// Save persists waypoint state
func (m *MockWaypointRepository) Save(ctx context.Context, waypoint *shared.Waypoint) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.waypoints[waypoint.Symbol] = waypoint
	return nil
}

// ListBySystemWithTrait filters waypoints by trait
func (m *MockWaypointRepository) ListBySystemWithTrait(ctx context.Context, systemSymbol, trait string) ([]*shared.Waypoint, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var waypoints []*shared.Waypoint
	for _, waypoint := range m.waypoints {
		if waypoint.SystemSymbol == systemSymbol {
			for _, wpTrait := range waypoint.Traits {
				if wpTrait == trait {
					waypoints = append(waypoints, waypoint)
					break
				}
			}
		}
	}

	return waypoints, nil
}

// ListBySystemExcludingTrait filters waypoints excluding a specific trait
func (m *MockWaypointRepository) ListBySystemExcludingTrait(ctx context.Context, systemSymbol, trait string) ([]*shared.Waypoint, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var waypoints []*shared.Waypoint
	for _, waypoint := range m.waypoints {
		if waypoint.SystemSymbol == systemSymbol {
			hasTraitToExclude := false
			for _, wpTrait := range waypoint.Traits {
				if wpTrait == trait {
					hasTraitToExclude = true
					break
				}
			}
			if !hasTraitToExclude {
				waypoints = append(waypoints, waypoint)
			}
		}
	}

	return waypoints, nil
}
