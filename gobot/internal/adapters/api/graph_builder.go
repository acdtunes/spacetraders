package api

import (
	"context"
	"fmt"
	"log"
	"math"
	"slices"

	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// maxWaypointPages caps waypoint pagination so a server that never reports the
// collection exhausted cannot spin the fetch loop forever.
const maxWaypointPages = 50

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

	playerIDValue, err := shared.NewPlayerID(playerID)
	if err != nil {
		return nil, fmt.Errorf("invalid player ID: %w", err)
	}
	player, err := b.playerRepo.FindByID(ctx, playerIDValue)
	if err != nil {
		return nil, fmt.Errorf("failed to get player: %w", err)
	}

	allWaypoints, err := b.fetchAllWaypoints(ctx, systemSymbol, player.Token)
	if err != nil {
		return nil, err
	}

	if len(allWaypoints) == 0 {
		return nil, fmt.Errorf("no waypoints found for system %s", systemSymbol)
	}

	graph := system.NewNavigationGraph(systemSymbol)

	waypointObjects := []*shared.Waypoint{}

	for _, wp := range allWaypoints {
		waypointObj, err := waypointFromAPIData(wp, systemSymbol)
		if err != nil {
			log.Printf("Warning: failed to create waypoint %s: %v", wp.Symbol, err)
			continue
		}

		graph.AddWaypoint(waypointObj)

		waypointObjects = append(waypointObjects, waypointObj)
	}

	connectEveryWaypointPair(graph)
	b.cacheWaypoints(ctx, waypointObjects)

	fuelStations := len(graph.GetFuelStations())

	log.Printf("Graph built for %s", systemSymbol)
	log.Printf("  Waypoints: %d", graph.WaypointCount())
	log.Printf("  Edges: %d", graph.EdgeCount())
	log.Printf("  Synced %d waypoints to waypoints table", len(waypointObjects))
	log.Printf("  Fuel stations: %d", fuelStations)

	return graph, nil
}

func (b *GraphBuilder) fetchAllWaypoints(ctx context.Context, systemSymbol, token string) ([]system.WaypointAPIData, error) {
	allWaypoints := []system.WaypointAPIData{}
	limit := apiPageLimitMax

	for page := 1; page <= maxWaypointPages; page++ {
		result, err := b.apiClient.ListWaypoints(ctx, systemSymbol, token, page, limit)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch waypoints page %d: %w", page, err)
		}

		if len(result.Data) == 0 {
			break
		}

		allWaypoints = append(allWaypoints, result.Data...)

		log.Printf("  Fetched page %d: %d waypoints", page, len(result.Data))

		totalPages := (result.Meta.Total / limit)
		if result.Meta.Total%limit > 0 {
			totalPages++
		}

		if page >= totalPages || len(result.Data) < limit {
			break
		}
	}

	return allWaypoints, nil
}

func waypointFromAPIData(wp system.WaypointAPIData, systemSymbol string) (*shared.Waypoint, error) {
	waypointObj, err := shared.NewWaypoint(wp.Symbol, wp.X, wp.Y)
	if err != nil {
		return nil, err
	}

	traits := traitSymbols(wp.Traits)

	waypointObj.SystemSymbol = systemSymbol
	waypointObj.Type = wp.Type
	waypointObj.Traits = traits
	waypointObj.HasFuel = shared.WaypointGrantsFuel(wp.Type, traits)
	waypointObj.Orbitals = orbitalSymbols(wp.Orbitals)

	return waypointObj, nil
}

func orbitalSymbols(orbitals []map[string]string) []string {
	symbols := []string{}
	for _, orbital := range orbitals {
		if symbol, ok := orbital["symbol"]; ok {
			symbols = append(symbols, symbol)
		}
	}
	return symbols
}

func traitSymbols(traits []map[string]interface{}) []string {
	symbols := []string{}
	for _, trait := range traits {
		if symbol, ok := trait["symbol"]; ok {
			if symbolStr, ok := symbol.(string); ok {
				symbols = append(symbols, symbolStr)
			}
		}
	}
	return symbols
}

func connectEveryWaypointPair(graph *system.NavigationGraph) {
	waypointList := make([]string, 0, len(graph.Waypoints))
	for symbol := range graph.Waypoints {
		waypointList = append(waypointList, symbol)
	}

	for i, wp1Symbol := range waypointList {
		wp1 := graph.Waypoints[wp1Symbol]

		// Only create edges with waypoints that come after this one (avoid duplicates)
		for _, wp2Symbol := range waypointList[i+1:] {
			distance, edgeType := edgeBetween(wp1, graph.Waypoints[wp2Symbol])
			graph.AddEdge(wp1Symbol, wp2Symbol, distance, edgeType)
		}
	}
}

func edgeBetween(wp1, wp2 *shared.Waypoint) (float64, system.EdgeType) {
	if inSameOrbit(wp1, wp2) {
		return 0.0, system.EdgeTypeOrbital
	}
	return math.Round(wp1.DistanceTo(wp2)*100) / 100, system.EdgeTypeNormal
}

func inSameOrbit(wp1, wp2 *shared.Waypoint) bool {
	return slices.Contains(wp1.Orbitals, wp2.Symbol) || slices.Contains(wp2.Orbitals, wp1.Symbol)
}

func (b *GraphBuilder) cacheWaypoints(ctx context.Context, waypointObjects []*shared.Waypoint) {
	for _, waypointObj := range waypointObjects {
		if err := b.waypointRepo.Add(ctx, waypointObj); err != nil {
			log.Printf("Warning: failed to save waypoint %s: %v", waypointObj.Symbol, err)
			// Continue - caching failure shouldn't break the operation
		}
	}
}
