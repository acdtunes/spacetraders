package helpers

import (
	"context"
	"fmt"
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// MockGraphProvider simulates system graph operations for testing
type MockGraphProvider struct {
	mu sync.RWMutex

	graphs map[string]*system.NavigationGraph // systemSymbol -> graph
}

// NewMockGraphProvider creates a new mock graph provider
func NewMockGraphProvider() *MockGraphProvider {
	return &MockGraphProvider{
		graphs: make(map[string]*system.NavigationGraph),
	}
}

// SetGraph configures the graph for a system
func (m *MockGraphProvider) SetGraph(systemSymbol string, distanceGraph map[string]map[string]float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Build NavigationGraph
	graph := system.NewNavigationGraph(systemSymbol)

	// Build waypoints
	for waypointSymbol := range distanceGraph {
		// Create waypoint at 0,0 with fuel for simplicity
		waypoint, _ := shared.NewWaypoint(waypointSymbol, 0.0, 0.0)
		waypoint.SystemSymbol = systemSymbol
		waypoint.HasFuel = true
		graph.AddWaypoint(waypoint)
	}

	// Build edges
	for from, destinations := range distanceGraph {
		for to, distance := range destinations {
			graph.AddEdge(from, to, distance, system.EdgeTypeOrbital)
		}
	}

	m.graphs[systemSymbol] = graph
}

// GetGraph retrieves a system graph (implements system.ISystemGraphProvider)
func (m *MockGraphProvider) GetGraph(ctx context.Context, systemSymbol string, forceRefresh bool, playerID int) (*system.GraphLoadResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	graph, exists := m.graphs[systemSymbol]
	if !exists {
		return nil, fmt.Errorf("no graph configured for system %s", systemSymbol)
	}

	return &system.GraphLoadResult{
		Graph:   graph,
		Source:  "mock",
		Message: "Mock graph loaded",
	}, nil
}

// Reset clears all configured graphs
func (m *MockGraphProvider) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.graphs = make(map[string]*system.NavigationGraph)
}
