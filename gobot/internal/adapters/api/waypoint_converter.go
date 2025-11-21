package api

import (
	"log"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// WaypointConverter converts graph data structures to domain Waypoint objects
type WaypointConverter struct{}

// NewWaypointConverter creates a new waypoint converter
func NewWaypointConverter() *WaypointConverter {
	return &WaypointConverter{}
}

// ConvertGraphToWaypoints converts graph waypoints to Waypoint objects with optional trait enrichment
//
// This centralizes the conversion logic that was duplicated across multiple files:
// - waypoint_enricher.go
// - distribution_checker.go
// - ship_selector.go
//
// Args:
//   graph: Graph structure with waypoints data (map[string]interface{})
//   waypointTraits: Optional lookup map of Waypoint objects with full trait data
//                   Maps waypoint_symbol -> Waypoint from database
//
// Returns:
//   Map of waypoint_symbol -> Waypoint objects
func (c *WaypointConverter) ConvertGraphToWaypoints(
	graph map[string]interface{},
	waypointTraits map[string]*shared.Waypoint,
) map[string]*shared.Waypoint {
	waypointObjects := make(map[string]*shared.Waypoint)

	// Extract waypoints from graph structure
	graphWaypoints, ok := graph["waypoints"].(map[string]interface{})
	if !ok {
		return waypointObjects
	}

	for symbol, wpData := range graphWaypoints {
		wpMap, ok := wpData.(map[string]interface{})
		if !ok {
			continue
		}

		// Check if we have trait data from waypoints table
		if traitWp, exists := waypointTraits[symbol]; exists {
			// Use full Waypoint object from waypoints table (has correct has_fuel)
			waypointObjects[symbol] = traitWp
		} else {
			// Fallback: create Waypoint from graph structure-only data
			waypoint := c.convertWaypointFromMap(symbol, wpMap)
			if waypoint != nil {
				waypointObjects[symbol] = waypoint
			}
		}
	}

	return waypointObjects
}

// ConvertWaypointFromMap converts a single waypoint from map[string]interface{} to domain Waypoint
//
// This method handles the extraction of waypoint data from the graph format
func (c *WaypointConverter) ConvertWaypointFromMap(
	symbol string,
	wpMap map[string]interface{},
) *shared.Waypoint {
	return c.convertWaypointFromMap(symbol, wpMap)
}

// convertWaypointFromMap is the internal implementation
func (c *WaypointConverter) convertWaypointFromMap(
	symbol string,
	wpMap map[string]interface{},
) *shared.Waypoint {
	// Extract basic coordinates
	x, _ := wpMap["x"].(float64)
	y, _ := wpMap["y"].(float64)

	// Create waypoint
	wp, err := shared.NewWaypoint(symbol, x, y)
	if err != nil {
		log.Printf("Warning: failed to create waypoint %s: %v", symbol, err)
		return nil
	}

	// Extract optional fields
	if wpType, ok := wpMap["type"].(string); ok {
		wp.Type = wpType
	}

	if systemSymbol, ok := wpMap["systemSymbol"].(string); ok {
		wp.SystemSymbol = systemSymbol
	}

	// Extract has_fuel with multiple fallback strategies
	wp.HasFuel = c.extractHasFuel(wpMap)

	// Extract orbitals if present
	if orbitals, ok := wpMap["orbitals"].([]string); ok {
		wp.Orbitals = orbitals
	}

	return wp
}

// extractHasFuel determines if a waypoint has fuel using multiple strategies
func (c *WaypointConverter) extractHasFuel(wpMap map[string]interface{}) bool {
	// Strategy 1: Check explicit has_fuel field
	if hasFuelVal, ok := wpMap["has_fuel"].(bool); ok {
		return hasFuelVal
	}

	// Strategy 2: Check traits for MARKETPLACE or FUEL_STATION
	if traits, ok := wpMap["traits"].([]string); ok {
		for _, trait := range traits {
			if trait == "MARKETPLACE" || trait == "FUEL_STATION" {
				return true
			}
		}
	}

	// Strategy 3: Check traits as []interface{} (alternative format)
	if traitsInterface, ok := wpMap["traits"].([]interface{}); ok {
		for _, trait := range traitsInterface {
			if traitStr, ok := trait.(string); ok {
				if traitStr == "MARKETPLACE" || traitStr == "FUEL_STATION" {
					return true
				}
			}
		}
	}

	return false
}
