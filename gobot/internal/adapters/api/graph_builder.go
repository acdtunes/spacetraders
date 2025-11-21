package api

import (
	"context"
	"fmt"
	"log"
	"math"

	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

// GraphBuilder builds system navigation graphs from API data
type GraphBuilder struct {
	apiClient    domainPorts.APIClient
	playerRepo   player.PlayerRepository
	waypointRepo system.WaypointRepository
}

// NewGraphBuilder creates a new graph builder
func NewGraphBuilder(
	apiClient domainPorts.APIClient,
	playerRepo player.PlayerRepository,
	waypointRepo system.WaypointRepository,
) system.IGraphBuilder {
	return &GraphBuilder{
		apiClient:    apiClient,
		playerRepo:   playerRepo,
		waypointRepo: waypointRepo,
	}
}

// BuildSystemGraph fetches all waypoints from API and builds navigation graph with dual-cache strategy
//
// Populates both:
// 1. Return value: NavigationGraph for navigation (infinite TTL)
// 2. Waypoints table: Full trait data for queries (2hr TTL)
func (b *GraphBuilder) BuildSystemGraph(ctx context.Context, systemSymbol string, playerID int) (*system.NavigationGraph, error) {
	log.Printf("Building graph for system %s...", systemSymbol)

	// Get player token
	playerIDValue, err := shared.NewPlayerID(playerID)
	if err != nil {
		return nil, fmt.Errorf("invalid player ID: %w", err)
	}
	player, err := b.playerRepo.FindByID(ctx, playerIDValue)
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

	// Build NavigationGraph for navigation (infinite TTL)
	graph := system.NewNavigationGraph(systemSymbol)

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

		// Create full waypoint object with traits
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

		// Add to navigation graph
		graph.AddWaypoint(waypointObj)

		// Add to waypoint cache list
		waypointObjects = append(waypointObjects, waypointObj)
	}

	// Build edges (bidirectional graph)
	waypointList := make([]string, 0, len(graph.Waypoints))
	for symbol := range graph.Waypoints {
		waypointList = append(waypointList, symbol)
	}

	for i, wp1Symbol := range waypointList {
		wp1 := graph.Waypoints[wp1Symbol]

		// Only create edges with waypoints that come after this one (avoid duplicates)
		for _, wp2Symbol := range waypointList[i+1:] {
			wp2 := graph.Waypoints[wp2Symbol]

			// Check if this is an orbital relationship (zero distance)
			isOrbital := false
			for _, orbital := range wp1.Orbitals {
				if orbital == wp2Symbol {
					isOrbital = true
					break
				}
			}
			if !isOrbital {
				for _, orbital := range wp2.Orbitals {
					if orbital == wp1Symbol {
						isOrbital = true
						break
					}
				}
			}

			var distance float64
			var edgeType system.EdgeType

			if isOrbital {
				distance = 0.0
				edgeType = system.EdgeTypeOrbital
			} else {
				// Use domain Waypoint.DistanceTo() method
				distance = wp1.DistanceTo(wp2)
				edgeType = system.EdgeTypeNormal
			}

			distance = math.Round(distance*100) / 100 // Round to 2 decimal places

			// Add bidirectional edges using NavigationGraph method
			graph.AddEdge(wp1Symbol, wp2Symbol, distance, edgeType)
		}
	}

	// Save waypoints with traits to waypoints table (2hr TTL)
	for _, waypointObj := range waypointObjects {
		if err := b.waypointRepo.Add(ctx, waypointObj); err != nil {
			log.Printf("Warning: failed to save waypoint %s: %v", waypointObj.Symbol, err)
			// Continue - caching failure shouldn't break the operation
		}
	}

	fuelStations := len(graph.GetFuelStations())

	log.Printf("Graph built for %s", systemSymbol)
	log.Printf("  Waypoints: %d", graph.WaypointCount())
	log.Printf("  Edges: %d", graph.EdgeCount())
	log.Printf("  Synced %d waypoints to waypoints table", len(waypointObjects))
	log.Printf("  Fuel stations: %d", fuelStations)

	return graph, nil
}
