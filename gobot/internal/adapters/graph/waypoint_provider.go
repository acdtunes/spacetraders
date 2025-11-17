package graph

import (
	"context"
	"fmt"
	"log"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// WaypointProvider provides waypoint data with auto-fetch and database caching
//
// Checks database cache first, triggers system graph build if waypoint not found.
// Building the system graph fetches ALL waypoints in the system and caches them.
type WaypointProvider struct {
	waypointRepo system.WaypointRepository
	graphBuilder system.IGraphBuilder
}

// NewWaypointProvider creates a new waypoint provider
func NewWaypointProvider(
	waypointRepo system.WaypointRepository,
	graphBuilder system.IGraphBuilder,
) *WaypointProvider {
	return &WaypointProvider{
		waypointRepo: waypointRepo,
		graphBuilder: graphBuilder,
	}
}

// GetWaypoint retrieves waypoint data (auto-fetches from API if not cached)
//
// Flow:
// 1. Try loading waypoint from database cache
// 2. If not found, build system graph from API (fetches all waypoints in system)
// 3. Retry loading waypoint from database (should exist after graph build)
// 4. Return waypoint or error if still not found
//
// Note: playerID is required for API authentication when building the system graph
func (p *WaypointProvider) GetWaypoint(ctx context.Context, waypointSymbol, systemSymbol string, playerID int) (*shared.Waypoint, error) {
	// Try loading from database cache first
	waypoint, err := p.loadFromDatabase(ctx, waypointSymbol, systemSymbol)
	if err == nil && waypoint != nil {
		log.Printf("Cache hit for waypoint %s", waypointSymbol)
		return waypoint, nil
	}

	// Cache miss - trigger system graph build to fetch all waypoints
	log.Printf("Cache miss for waypoint %s - building system graph for %s", waypointSymbol, systemSymbol)
	if err := p.buildSystemGraph(ctx, systemSymbol, playerID); err != nil {
		return nil, fmt.Errorf("failed to build system graph for %s: %w", systemSymbol, err)
	}

	// Retry loading waypoint from database (should exist after graph build)
	waypoint, err = p.loadFromDatabase(ctx, waypointSymbol, systemSymbol)
	if err != nil {
		return nil, fmt.Errorf("waypoint %s not found even after building system graph: %w", waypointSymbol, err)
	}

	if waypoint == nil {
		return nil, fmt.Errorf("waypoint %s not found in system %s", waypointSymbol, systemSymbol)
	}

	log.Printf("Waypoint %s loaded after system graph build", waypointSymbol)
	return waypoint, nil
}

// loadFromDatabase loads waypoint from database cache
func (p *WaypointProvider) loadFromDatabase(ctx context.Context, waypointSymbol, systemSymbol string) (*shared.Waypoint, error) {
	waypoint, err := p.waypointRepo.FindBySymbol(ctx, waypointSymbol, systemSymbol)
	if err != nil {
		// Not found is expected - return nil without error
		return nil, err
	}

	return waypoint, nil
}

// buildSystemGraph builds system graph from API which caches all waypoints
func (p *WaypointProvider) buildSystemGraph(ctx context.Context, systemSymbol string, playerID int) error {
	log.Printf("Building navigation graph for %s from API to cache waypoints", systemSymbol)

	// Build the graph using this player's API client
	// This will fetch all waypoints in the system and cache them in the database
	_, err := p.graphBuilder.BuildSystemGraph(ctx, systemSymbol, playerID)
	if err != nil {
		return fmt.Errorf("failed to build graph for %s: %w", systemSymbol, err)
	}

	log.Printf("Graph for %s built successfully - waypoints cached in database", systemSymbol)
	return nil
}
