package helpers

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// MockWaypointProvider is a test double for IWaypointProvider interface
// Required for shipyard auto-discovery and navigation planning
type MockWaypointProvider struct {
	waypoints map[string]*shared.Waypoint // waypointSymbol -> waypoint
	getFunc   func(ctx context.Context, waypointSymbol, systemSymbol string, playerID int) (*shared.Waypoint, error)
}

// NewMockWaypointProvider creates a new MockWaypointProvider
func NewMockWaypointProvider() *MockWaypointProvider {
	return &MockWaypointProvider{
		waypoints: make(map[string]*shared.Waypoint),
	}
}

// GetWaypoint implements the IWaypointProvider interface
func (m *MockWaypointProvider) GetWaypoint(ctx context.Context, waypointSymbol, systemSymbol string, playerID int) (*shared.Waypoint, error) {
	// Use custom function if provided
	if m.getFunc != nil {
		return m.getFunc(ctx, waypointSymbol, systemSymbol, playerID)
	}

	// Return stored waypoint if available
	if wp, exists := m.waypoints[waypointSymbol]; exists {
		return wp, nil
	}

	return nil, fmt.Errorf("waypoint not found: %s", waypointSymbol)
}

// SetWaypoint adds a waypoint to the mock's storage
func (m *MockWaypointProvider) SetWaypoint(wp *shared.Waypoint) {
	m.waypoints[wp.Symbol] = wp
}

// SetGetWaypointFunc sets a custom function for GetWaypoint calls
func (m *MockWaypointProvider) SetGetWaypointFunc(fn func(ctx context.Context, waypointSymbol, systemSymbol string, playerID int) (*shared.Waypoint, error)) {
	m.getFunc = fn
}

// ClearWaypoints removes all stored waypoints
func (m *MockWaypointProvider) ClearWaypoints() {
	m.waypoints = make(map[string]*shared.Waypoint)
	m.getFunc = nil
}

// Ensure MockWaypointProvider implements the system.IWaypointProvider interface
var _ system.IWaypointProvider = (*MockWaypointProvider)(nil)
