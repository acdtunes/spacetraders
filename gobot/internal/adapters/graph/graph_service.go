package graph

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// GraphService provides unified access to system graphs and waypoints
// This service replaces both SystemGraphProvider and WaypointProvider
// with a single, cohesive interface for graph-related operations
//
// Caching Strategy (Two-Tier):
// - Tier 1: In-memory cache (waypointCache) - infinite TTL during daemon lifetime
// - Tier 2: Database cache (waypoints table) - 1-day TTL for persistence across restarts
// - Concurrent build protection via per-system locks (buildLocks)
type GraphService struct {
	graphRepo     system.SystemGraphRepository
	waypointRepo  system.WaypointRepository
	graphBuilder  system.IGraphBuilder
	waypointCache sync.Map // key: "system:waypoint" -> *shared.Waypoint (infinite TTL)
	buildLocks    sync.Map // key: systemSymbol -> *sync.Mutex (prevents concurrent builds)
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
// 3. Populate in-memory waypoint cache for fast lookups
// 4. Return graph with metadata about source
func (s *GraphService) GetGraph(ctx context.Context, systemSymbol string, forceRefresh bool, playerID int) (*system.GraphLoadResult, error) {
	// Try loading from database cache first (unless force refresh)
	if !forceRefresh {
		graph, err := s.graphRepo.Get(ctx, systemSymbol)
		if err != nil {
			log.Printf("Error loading graph from database: %v", err)
		} else if graph != nil {
			// Populate in-memory waypoint cache from loaded graph
			s.populateWaypointCache(systemSymbol, graph)
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

	// Populate in-memory waypoint cache from built graph
	s.populateWaypointCache(systemSymbol, graph)

	return &system.GraphLoadResult{
		Graph:   graph,
		Source:  "api",
		Message: fmt.Sprintf("Built graph for %s from API", systemSymbol),
	}, nil
}

// populateWaypointCache stores all waypoints from a graph into the in-memory cache
func (s *GraphService) populateWaypointCache(systemSymbol string, graph *system.NavigationGraph) {
	for symbol, wp := range graph.Waypoints {
		key := systemSymbol + ":" + symbol
		s.waypointCache.Store(key, wp)
	}
}

// GetWaypoint retrieves waypoint data with two-tier caching (implements IWaypointProvider)
//
// Two-Tier Caching Strategy:
// 1. TIER 1: Check in-memory cache (infinite TTL, zero latency)
// 2. TIER 2: Check database cache (1-day TTL, persists across restarts)
// 3. TIER 3: Cache miss - fetch from API via graph build (with per-system lock)
//
// Concurrent Protection:
// - Per-system mutex prevents duplicate graph builds
// - Double-check pattern after acquiring lock
func (s *GraphService) GetWaypoint(ctx context.Context, waypointSymbol, systemSymbol string, playerID int) (*shared.Waypoint, error) {
	cacheKey := systemSymbol + ":" + waypointSymbol

	// TIER 1: Check in-memory cache first (infinite TTL, zero latency)
	if cached, ok := s.waypointCache.Load(cacheKey); ok {
		return cached.(*shared.Waypoint), nil
	}

	// TIER 2: Try loading from database (1-day TTL)
	waypoint, err := s.waypointRepo.FindBySymbol(ctx, waypointSymbol, systemSymbol)
	if err == nil && waypoint != nil {
		s.waypointCache.Store(cacheKey, waypoint) // Populate memory cache
		return waypoint, nil
	}

	// TIER 3: Cache miss - need to build graph from API
	// Use per-system lock to prevent concurrent builds
	lock, _ := s.buildLocks.LoadOrStore(systemSymbol, &sync.Mutex{})
	mutex := lock.(*sync.Mutex)
	mutex.Lock()
	defer mutex.Unlock()

	// Double-check MEMORY cache after acquiring lock - another goroutine may have built it
	if cached, ok := s.waypointCache.Load(cacheKey); ok {
		return cached.(*shared.Waypoint), nil
	}

	// Double-check DATABASE cache after acquiring lock - another goroutine may have built and saved
	// This is critical: if graph was built, DB has fresh waypoints even if our memory cache missed
	waypoint, err = s.waypointRepo.FindBySymbol(ctx, waypointSymbol, systemSymbol)
	if err == nil && waypoint != nil {
		s.waypointCache.Store(cacheKey, waypoint)
		return waypoint, nil
	}

	// Also try loading the full graph from database - it might have been built by another goroutine
	// and saved to the graph repository, which would populate ALL waypoints
	graph, err := s.graphRepo.Get(ctx, systemSymbol)
	if err == nil && graph != nil {
		s.populateWaypointCache(systemSymbol, graph)
		if wp, ok := graph.Waypoints[waypointSymbol]; ok {
			return wp, nil
		}
	}

	log.Printf("Waypoint cache miss for %s - building system graph for %s", waypointSymbol, systemSymbol)
	graph, err = s.graphBuilder.BuildSystemGraph(ctx, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to build system graph for %s: %w", systemSymbol, err)
	}

	// Save graph to database cache (like GetGraph does) so other lookups find it
	if saveErr := s.graphRepo.Add(ctx, systemSymbol, graph); saveErr != nil {
		log.Printf("Warning: failed to cache graph for %s: %v", systemSymbol, saveErr)
	}

	// Populate memory cache with ALL waypoints from the built graph
	s.populateWaypointCache(systemSymbol, graph)

	// Return the requested waypoint
	if wp, ok := graph.Waypoints[waypointSymbol]; ok {
		return wp, nil
	}

	return nil, fmt.Errorf("waypoint %s not found in system %s", waypointSymbol, systemSymbol)
}

// RefreshSystemGraph forces a rebuild of the system graph from the API
// This is useful when waypoint data needs to be updated (e.g., markets change)
func (s *GraphService) RefreshSystemGraph(ctx context.Context, systemSymbol string, playerID int) error {
	log.Printf("Force refreshing graph for %s", systemSymbol)
	_, err := s.GetGraph(ctx, systemSymbol, true, playerID)
	return err
}
