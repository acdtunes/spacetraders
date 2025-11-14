package ship

import (
	"context"
	"log"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// WaypointEnricher enriches graph waypoints with trait data from database
type WaypointEnricher struct {
	waypointRepo system.WaypointRepository
}

// NewWaypointEnricher creates a new waypoint enricher
func NewWaypointEnricher(waypointRepo system.WaypointRepository) *WaypointEnricher {
	return &WaypointEnricher{
		waypointRepo: waypointRepo,
	}
}

// EnrichGraphWaypoints converts graph waypoints to Waypoint objects with trait enrichment
//
// Args:
//
//	graph: Graph structure from system_graphs table (structure-only)
//	systemSymbol: System symbol for loading waypoint traits
//
// Returns:
//
//	Map of waypoint_symbol -> Waypoint objects with correct has_fuel data
func (e *WaypointEnricher) EnrichGraphWaypoints(
	ctx context.Context,
	graph map[string]interface{},
	systemSymbol string,
) (map[string]*shared.Waypoint, error) {
	// 1. Load waypoint traits from database
	waypointList, err := e.waypointRepo.ListBySystem(ctx, systemSymbol)
	var waypointTraits map[string]*shared.Waypoint
	if err != nil {
		log.Printf("Warning: failed to load waypoint traits: %v (continuing with graph-only data)", err)
		waypointTraits = make(map[string]*shared.Waypoint)
	} else {
		waypointTraits = make(map[string]*shared.Waypoint)
		for _, wp := range waypointList {
			waypointTraits[wp.Symbol] = wp
		}
	}

	// 2. Convert graph to waypoints with enrichment
	return convertGraphToWaypoints(graph, waypointTraits), nil
}

// convertGraphToWaypoints converts graph waypoints to Waypoint objects with trait enrichment
//
// This function is extracted from navigate_ship.go (lines 276-340)
//
// Args:
//
//	graph: Graph structure from system_graphs table (structure-only)
//	waypointTraits: Optional lookup dict of Waypoint objects from waypoints table
//	               Maps waypoint_symbol -> Waypoint with full trait data
//
// Returns:
//
//	Dict of waypoint_symbol -> Waypoint objects with correct has_fuel data
func convertGraphToWaypoints(
	graph map[string]interface{},
	waypointTraits map[string]*shared.Waypoint,
) map[string]*shared.Waypoint {
	waypointObjects := make(map[string]*shared.Waypoint)

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
			x, _ := wpMap["x"].(float64)
			y, _ := wpMap["y"].(float64)
			wpType, _ := wpMap["type"].(string)
			systemSymbol, _ := wpMap["systemSymbol"].(string)

			// Try to extract has_fuel from graph (may not exist in structure-only graph)
			hasFuel := false
			if hasFuelVal, ok := wpMap["has_fuel"].(bool); ok {
				hasFuel = hasFuelVal
			} else {
				// Fallback: check if traits contain MARKETPLACE
				if traits, ok := wpMap["traits"].([]string); ok {
					for _, trait := range traits {
						if trait == "MARKETPLACE" || trait == "FUEL_STATION" {
							hasFuel = true
							break
						}
					}
				}
			}

			wp, err := shared.NewWaypoint(symbol, x, y)
			if err != nil {
				log.Printf("Warning: failed to create waypoint %s: %v", symbol, err)
				continue
			}

			wp.Type = wpType
			wp.SystemSymbol = systemSymbol
			wp.HasFuel = hasFuel

			// Extract orbitals if present
			if orbitals, ok := wpMap["orbitals"].([]string); ok {
				wp.Orbitals = orbitals
			}

			waypointObjects[symbol] = wp
		}
	}

	return waypointObjects
}
