package helpers

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// MockGraphBuilder simulates graph building operations for testing
type MockGraphBuilder struct {
	apiClient    *MockAPIClient
	waypointRepo *persistence.GormWaypointRepository
}

// NewMockGraphBuilder creates a new mock graph builder
func NewMockGraphBuilder(apiClient *MockAPIClient, waypointRepo *persistence.GormWaypointRepository) system.IGraphBuilder {
	return &MockGraphBuilder{
		apiClient:    apiClient,
		waypointRepo: waypointRepo,
	}
}

// BuildSystemGraph builds a system graph for testing
func (m *MockGraphBuilder) BuildSystemGraph(ctx context.Context, systemSymbol string, playerID int) (map[string]interface{}, error) {
	// For testing, load waypoints from repository and build graph
	waypoints, err := m.waypointRepo.ListBySystem(ctx, systemSymbol)
	if err != nil {
		// Return empty graph if no waypoints
		return map[string]interface{}{
			"waypoints": map[string]interface{}{},
		}, nil
	}

	// Build graph structure from waypoints
	waypointMap := make(map[string]interface{})
	for _, wp := range waypoints {
		waypointMap[wp.Symbol] = map[string]interface{}{
			"symbol":       wp.Symbol,
			"systemSymbol": wp.SystemSymbol,
			"x":            wp.X,
			"y":            wp.Y,
			"type":         wp.Type,
			"has_fuel":     wp.HasFuel,
		}
	}

	return map[string]interface{}{
		"waypoints": waypointMap,
	}, nil
}
