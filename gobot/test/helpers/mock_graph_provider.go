package helpers

import (
	"context"
	"fmt"
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// MockGraphProvider simulates system graph operations for testing
type MockGraphProvider struct {
	mu sync.RWMutex

	graphs map[string]map[string]interface{} // systemSymbol -> graph
}

// NewMockGraphProvider creates a new mock graph provider
func NewMockGraphProvider() *MockGraphProvider {
	return &MockGraphProvider{
		graphs: make(map[string]map[string]interface{}),
	}
}

// SetGraph configures the graph for a system
func (m *MockGraphProvider) SetGraph(systemSymbol string, distanceGraph map[string]map[string]float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Convert distance graph to the expected format
	waypoints := make(map[string]interface{})
	edges := []map[string]interface{}{}

	// Build waypoints map
	for waypointSymbol := range distanceGraph {
		waypoints[waypointSymbol] = map[string]interface{}{
			"symbol":   waypointSymbol,
			"x":        0.0,
			"y":        0.0,
			"has_fuel": true, // Assume all waypoints have fuel for simplicity
		}
	}

	// Build edges list
	for from, destinations := range distanceGraph {
		for to, distance := range destinations {
			edges = append(edges, map[string]interface{}{
				"from":     from,
				"to":       to,
				"distance": distance,
				"type":     "orbital",
			})
		}
	}

	m.graphs[systemSymbol] = map[string]interface{}{
		"waypoints": waypoints,
		"edges":     edges,
	}
}

// GetGraph retrieves a system graph (implements system.ISystemGraphProvider)
func (m *MockGraphProvider) GetGraph(ctx context.Context, systemSymbol string, forceRefresh bool) (*system.GraphLoadResult, error) {
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
	m.graphs = make(map[string]map[string]interface{})
}
