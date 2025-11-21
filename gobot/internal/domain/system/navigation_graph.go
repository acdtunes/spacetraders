package system

import (
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// NavigationGraph represents a system's navigation graph with waypoints and edges
// This replaces the map[string]interface{} structure used throughout the codebase
type NavigationGraph struct {
	SystemSymbol string                      `json:"system"`
	Waypoints    map[string]*shared.Waypoint `json:"waypoints"`
	Edges        []GraphEdge                 `json:"edges"`
}

// GraphEdge represents a connection between two waypoints
type GraphEdge struct {
	From     string   `json:"from"`
	To       string   `json:"to"`
	Distance float64  `json:"distance"`
	Type     EdgeType `json:"type"`
}

// EdgeType defines the type of connection between waypoints
type EdgeType string

const (
	EdgeTypeOrbital EdgeType = "orbital" // Zero-distance orbital relationship
	EdgeTypeNormal  EdgeType = "normal"  // Standard travel edge
)

// NewNavigationGraph creates a new navigation graph
func NewNavigationGraph(systemSymbol string) *NavigationGraph {
	return &NavigationGraph{
		SystemSymbol: systemSymbol,
		Waypoints:    make(map[string]*shared.Waypoint),
		Edges:        []GraphEdge{},
	}
}

// AddWaypoint adds a waypoint to the graph
func (g *NavigationGraph) AddWaypoint(waypoint *shared.Waypoint) {
	g.Waypoints[waypoint.Symbol] = waypoint
}

// AddEdge adds a bidirectional edge between two waypoints
func (g *NavigationGraph) AddEdge(from, to string, distance float64, edgeType EdgeType) {
	g.Edges = append(g.Edges,
		GraphEdge{From: from, To: to, Distance: distance, Type: edgeType},
		GraphEdge{From: to, To: from, Distance: distance, Type: edgeType},
	)
}

// GetWaypoint retrieves a waypoint by symbol
func (g *NavigationGraph) GetWaypoint(symbol string) (*shared.Waypoint, error) {
	waypoint, exists := g.Waypoints[symbol]
	if !exists {
		return nil, fmt.Errorf("waypoint %s not found in graph", symbol)
	}
	return waypoint, nil
}

// HasWaypoint checks if a waypoint exists in the graph
func (g *NavigationGraph) HasWaypoint(symbol string) bool {
	_, exists := g.Waypoints[symbol]
	return exists
}

// GetEdges returns all edges from a specific waypoint
func (g *NavigationGraph) GetEdges(fromSymbol string) []GraphEdge {
	var edges []GraphEdge
	for _, edge := range g.Edges {
		if edge.From == fromSymbol {
			edges = append(edges, edge)
		}
	}
	return edges
}

// WaypointCount returns the number of waypoints in the graph
func (g *NavigationGraph) WaypointCount() int {
	return len(g.Waypoints)
}

// EdgeCount returns the number of edges in the graph
func (g *NavigationGraph) EdgeCount() int {
	return len(g.Edges)
}

// GetFuelStations returns all waypoints that have fuel available
func (g *NavigationGraph) GetFuelStations() []*shared.Waypoint {
	var fuelStations []*shared.Waypoint
	for _, waypoint := range g.Waypoints {
		if waypoint.HasFuel {
			fuelStations = append(fuelStations, waypoint)
		}
	}
	return fuelStations
}
