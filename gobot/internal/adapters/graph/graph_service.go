package graph

import (
	"context"
	"fmt"
	"log"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// GraphService provides unified access to system graphs and waypoints
// This service replaces both SystemGraphProvider and WaypointProvider
// with a single, cohesive interface for graph-related operations
type GraphService struct {
	graphRepo    system.SystemGraphRepository
	waypointRepo system.WaypointRepository
	graphBuilder system.IGraphBuilder
}

// NewGraphService creates a new graph service
func NewGraphService(
	graphRepo system.SystemGraphRepository,
	waypointRepo system.WaypointRepository,
	graphBuilder system.IGraphBuilder,
) *GraphService {
	return &GraphService{
		graphRepo:    graphRepo,
		waypointRepo: waypointRepo,
		graphBuilder: graphBuilder,
	}
}

// GetGraph retrieves system navigation graph (implements ISystemGraphProvider)
//
// Flow:
// 1. Check graph cache (unless forceRefresh is true)
// 2. If not cached, build from API and cache
// 3. Return graph with metadata about source
func (s *GraphService) GetGraph(ctx context.Context, systemSymbol string, forceRefresh bool, playerID int) (*system.GraphLoadResult, error) {
	// Try loading from database cache first (unless force refresh)
	if !forceRefresh {
		graph, err := s.graphRepo.Get(ctx, systemSymbol)
		if err != nil {
			log.Printf("Error loading graph from database: %v", err)
		} else if graph != nil {
			log.Printf("Graph cache hit for %s", systemSymbol)
			return &system.GraphLoadResult{
				Graph:   graph,
				Source:  "database",
				Message: fmt.Sprintf("Loaded graph for %s from database cache", systemSymbol),
			}, nil
		}
	}

	// Build from API and cache it
	log.Printf("Building navigation graph for %s from API", systemSymbol)
	graph, err := s.graphBuilder.BuildSystemGraph(ctx, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to build graph for %s: %w", systemSymbol, err)
	}

	// Save to database cache
	if err := s.graphRepo.Add(ctx, systemSymbol, graph); err != nil {
		log.Printf("Warning: failed to cache graph for %s: %v", systemSymbol, err)
		// Don't fail - caching failure shouldn't break the operation
	} else {
		log.Printf("Graph for %s cached in database", systemSymbol)
	}

	return &system.GraphLoadResult{
		Graph:   graph,
		Source:  "api",
		Message: fmt.Sprintf("Built graph for %s from API", systemSymbol),
	}, nil
}

// GetWaypoint retrieves waypoint data (implements IWaypointProvider)
//
// Flow:
// 1. Try loading waypoint from waypoint cache
// 2. If not found, trigger graph build (which populates waypoint cache)
// 3. Retry loading waypoint from cache
// 4. Return waypoint or error
func (s *GraphService) GetWaypoint(ctx context.Context, waypointSymbol, systemSymbol string, playerID int) (*shared.Waypoint, error) {
	// Try loading from database cache first
	waypoint, err := s.waypointRepo.FindBySymbol(ctx, waypointSymbol, systemSymbol)
	if err == nil && waypoint != nil {
		log.Printf("Waypoint cache hit for %s", waypointSymbol)
		return waypoint, nil
	}

	// Cache miss - trigger system graph build to fetch all waypoints
	log.Printf("Waypoint cache miss for %s - building system graph for %s", waypointSymbol, systemSymbol)
	_, err = s.graphBuilder.BuildSystemGraph(ctx, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to build system graph for %s: %w", systemSymbol, err)
	}

	// Retry loading waypoint from database (should exist after graph build)
	waypoint, err = s.waypointRepo.FindBySymbol(ctx, waypointSymbol, systemSymbol)
	if err != nil {
		return nil, fmt.Errorf("waypoint %s not found even after building system graph: %w", waypointSymbol, err)
	}

	if waypoint == nil {
		return nil, fmt.Errorf("waypoint %s not found in system %s", waypointSymbol, systemSymbol)
	}

	log.Printf("Waypoint %s loaded after system graph build", waypointSymbol)
	return waypoint, nil
}

// RefreshSystemGraph forces a rebuild of the system graph from the API
// This is useful when waypoint data needs to be updated (e.g., markets change)
func (s *GraphService) RefreshSystemGraph(ctx context.Context, systemSymbol string, playerID int) error {
	log.Printf("Force refreshing graph for %s", systemSymbol)
	_, err := s.GetGraph(ctx, systemSymbol, true, playerID)
	return err
}
