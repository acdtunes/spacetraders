package system

import (
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// NavigationGraph represents a system's navigation graph with waypoints and edges
// This replaces the map[string]interface{} structure used throughout the codebase
type NavigationGraph struct {
	SystemSymbol string
	Waypoints    map[string]*shared.Waypoint
	Edges        []GraphEdge
}

// GraphEdge represents a connection between two waypoints
type GraphEdge struct {
	From     string
	To       string
	Distance float64
	Type     EdgeType
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

// ToLegacyFormat converts the graph to the legacy map[string]interface{} format
// This is needed for backward compatibility with existing code that expects this format
// TODO: Remove this once all code is updated to use NavigationGraph directly
func (g *NavigationGraph) ToLegacyFormat() map[string]interface{} {
	waypoints := make(map[string]interface{})
	for symbol, wp := range g.Waypoints {
		waypoints[symbol] = map[string]interface{}{
			"type":         wp.Type,
			"x":            wp.X,
			"y":            wp.Y,
			"systemSymbol": wp.SystemSymbol,
			"orbitals":     wp.Orbitals,
			"has_fuel":     wp.HasFuel,
		}
	}

	edges := make([]interface{}, len(g.Edges))
	for i, edge := range g.Edges {
		edges[i] = map[string]interface{}{
			"from":     edge.From,
			"to":       edge.To,
			"distance": edge.Distance,
			"type":     string(edge.Type),
		}
	}

	return map[string]interface{}{
		"system":    g.SystemSymbol,
		"waypoints": waypoints,
		"edges":     edges,
	}
}

// FromLegacyFormat creates a NavigationGraph from the legacy map[string]interface{} format
// This enables gradual migration from the old format to the new typed structure
func FromLegacyFormat(data map[string]interface{}, converter IWaypointConverter) (*NavigationGraph, error) {
	systemSymbol, ok := data["system"].(string)
	if !ok {
		return nil, fmt.Errorf("invalid graph format: missing or invalid system symbol")
	}

	graph := NewNavigationGraph(systemSymbol)

	// Convert waypoints using the converter
	if converter != nil {
		waypointObjects := converter.ConvertGraphToWaypoints(data, nil)
		for _, wp := range waypointObjects {
			graph.AddWaypoint(wp)
		}
	}

	// Convert edges
	if edgesRaw, ok := data["edges"].([]interface{}); ok {
		for _, edgeRaw := range edgesRaw {
			if edgeMap, ok := edgeRaw.(map[string]interface{}); ok {
				from, _ := edgeMap["from"].(string)
				to, _ := edgeMap["to"].(string)
				distance, _ := edgeMap["distance"].(float64)
				edgeTypeStr, _ := edgeMap["type"].(string)

				// Note: AddEdge adds bidirectional edges, so only add one direction
				// Skip if we've already added the reverse edge
				alreadyAdded := false
				for _, existing := range graph.Edges {
					if existing.From == to && existing.To == from {
						alreadyAdded = true
						break
					}
				}

				if !alreadyAdded {
					edgeType := EdgeType(edgeTypeStr)
					graph.AddEdge(from, to, distance, edgeType)
				}
			}
		}
	}

	return graph, nil
}
