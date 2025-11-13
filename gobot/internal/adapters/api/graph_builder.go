package api

import (
	"context"
	"fmt"
	"log"
	"math"

	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/ports"
)

// GraphBuilder builds system navigation graphs from API data
type GraphBuilder struct {
	apiClient    ports.APIClient
	playerRepo   player.PlayerRepository
	waypointRepo system.WaypointRepository
}

// NewGraphBuilder creates a new graph builder
func NewGraphBuilder(
	apiClient ports.APIClient,
	playerRepo player.PlayerRepository,
	waypointRepo system.WaypointRepository,
) system.IGraphBuilder {
	return &GraphBuilder{
		apiClient:    apiClient,
		playerRepo:   playerRepo,
		waypointRepo: waypointRepo,
	}
}

// euclideanDistance calculates Euclidean distance between two coordinates
func euclideanDistance(x1, y1, x2, y2 float64) float64 {
	dx := x2 - x1
	dy := y2 - y1
	return math.Sqrt(dx*dx + dy*dy)
}

// BuildSystemGraph fetches all waypoints from API and builds navigation graph with dual-cache strategy
//
// Populates both:
// 1. Return value: Structure-only graph for navigation (infinite TTL)
// 2. Waypoints table: Full trait data for queries (2hr TTL)
func (b *GraphBuilder) BuildSystemGraph(ctx context.Context, systemSymbol string, playerID int) (map[string]interface{}, error) {
	log.Printf("Building graph for system %s...", systemSymbol)

	// Get player token
	player, err := b.playerRepo.FindByID(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get player: %w", err)
	}

	// Fetch all waypoints with pagination
	allWaypoints := []system.WaypointAPIData{}
	page := 1
	limit := 20
	maxPages := 50 // Safety limit

	for page <= maxPages {
		result, err := b.apiClient.ListWaypoints(ctx, systemSymbol, player.Token, page, limit)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch waypoints page %d: %w", page, err)
		}

		if len(result.Data) == 0 {
			break
		}

		allWaypoints = append(allWaypoints, result.Data...)

		log.Printf("  Fetched page %d: %d waypoints", page, len(result.Data))

		// Check if we have more pages
		totalPages := (result.Meta.Total / limit)
		if result.Meta.Total%limit > 0 {
			totalPages++
		}

		if page >= totalPages || len(result.Data) < limit {
			break
		}

		page++
	}

	if len(allWaypoints) == 0 {
		return nil, fmt.Errorf("no waypoints found for system %s", systemSymbol)
	}

	// Build STRUCTURE-ONLY graph for navigation (infinite TTL)
	graph := map[string]interface{}{
		"system":    systemSymbol,
		"waypoints": make(map[string]interface{}),
		"edges":     []interface{}{},
	}

	waypoints := graph["waypoints"].(map[string]interface{})
	edges := []interface{}{}

	// Prepare waypoint objects for trait cache (2hr TTL)
	waypointObjects := []*shared.Waypoint{}

	// Process waypoints with dual-cache strategy
	for _, wp := range allWaypoints {
		// Extract orbitals
		orbitals := []string{}
		for _, orbital := range wp.Orbitals {
			if symbol, ok := orbital["symbol"]; ok {
				orbitals = append(orbitals, symbol)
			}
		}

		// Extract traits
		traits := []string{}
		for _, trait := range wp.Traits {
			if symbol, ok := trait["symbol"]; ok {
				if symbolStr, ok := symbol.(string); ok {
					traits = append(traits, symbolStr)
				}
			}
		}

		// Determine has_fuel
		hasFuel := false
		for _, trait := range traits {
			if trait == "MARKETPLACE" || trait == "FUEL_STATION" {
				hasFuel = true
				break
			}
		}

		// 1. STRUCTURE-ONLY data for navigation graph (no traits, no has_fuel)
		waypoints[wp.Symbol] = map[string]interface{}{
			"type":         wp.Type,
			"x":            wp.X,
			"y":            wp.Y,
			"systemSymbol": systemSymbol,
			"orbitals":     orbitals,
			// NO traits or has_fuel - structure only for routing
		}

		// 2. FULL waypoint object with traits for waypoints table
		waypointObj, err := shared.NewWaypoint(wp.Symbol, wp.X, wp.Y)
		if err != nil {
			log.Printf("Warning: failed to create waypoint %s: %v", wp.Symbol, err)
			continue
		}

		waypointObj.SystemSymbol = systemSymbol
		waypointObj.Type = wp.Type
		waypointObj.Traits = traits
		waypointObj.HasFuel = hasFuel
		waypointObj.Orbitals = orbitals

		waypointObjects = append(waypointObjects, waypointObj)
	}

	// Build edges (bidirectional graph)
	waypointList := make([]string, 0, len(waypoints))
	for symbol := range waypoints {
		waypointList = append(waypointList, symbol)
	}

	for i, wp1Symbol := range waypointList {
		wp1Data := waypoints[wp1Symbol].(map[string]interface{})
		wp1X := wp1Data["x"].(float64)
		wp1Y := wp1Data["y"].(float64)
		wp1Orbitals := wp1Data["orbitals"].([]string)

		// Only create edges with waypoints that come after this one (avoid duplicates)
		for _, wp2Symbol := range waypointList[i+1:] {
			wp2Data := waypoints[wp2Symbol].(map[string]interface{})
			wp2X := wp2Data["x"].(float64)
			wp2Y := wp2Data["y"].(float64)
			wp2Orbitals := wp2Data["orbitals"].([]string)

			// Check if this is an orbital relationship (zero distance)
			isOrbital := false
			for _, orbital := range wp1Orbitals {
				if orbital == wp2Symbol {
					isOrbital = true
					break
				}
			}
			if !isOrbital {
				for _, orbital := range wp2Orbitals {
					if orbital == wp1Symbol {
						isOrbital = true
						break
					}
				}
			}

			var distance float64
			var edgeType string

			if isOrbital {
				distance = 0.0
				edgeType = "orbital"
			} else {
				distance = euclideanDistance(wp1X, wp1Y, wp2X, wp2Y)
				edgeType = "normal"
			}

			distance = math.Round(distance*100) / 100 // Round to 2 decimal places

			// Add bidirectional edges
			edges = append(edges, map[string]interface{}{
				"from":     wp1Symbol,
				"to":       wp2Symbol,
				"distance": distance,
				"type":     edgeType,
			})
			edges = append(edges, map[string]interface{}{
				"from":     wp2Symbol,
				"to":       wp1Symbol,
				"distance": distance,
				"type":     edgeType,
			})
		}
	}

	graph["edges"] = edges

	// Save waypoints with traits to waypoints table (2hr TTL)
	for _, waypointObj := range waypointObjects {
		if err := b.waypointRepo.Save(ctx, waypointObj); err != nil {
			log.Printf("Warning: failed to save waypoint %s: %v", waypointObj.Symbol, err)
			// Continue - caching failure shouldn't break the operation
		}
	}

	fuelStations := 0
	for _, wp := range waypointObjects {
		if wp.HasFuel {
			fuelStations++
		}
	}

	log.Printf("Graph built for %s", systemSymbol)
	log.Printf("  Waypoints: %d", len(waypoints))
	log.Printf("  Edges: %d", len(edges))
	log.Printf("  Synced %d waypoints to waypoints table", len(waypointObjects))
	log.Printf("  Fuel stations: %d", fuelStations)

	return graph, nil
}
