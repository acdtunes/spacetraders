package system

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// WaypointRepository defines waypoint persistence operations
type WaypointRepository interface {
	FindBySymbol(ctx context.Context, symbol, systemSymbol string) (*shared.Waypoint, error)
	ListBySystem(ctx context.Context, systemSymbol string) ([]*shared.Waypoint, error)
	ListBySystemWithTrait(ctx context.Context, systemSymbol, trait string) ([]*shared.Waypoint, error)
	Add(ctx context.Context, waypoint *shared.Waypoint) error
}

// SystemGraphRepository defines operations for system graph persistence
type SystemGraphRepository interface {
	// Get retrieves a graph for a system from cache
	Get(ctx context.Context, systemSymbol string) (*NavigationGraph, error)

	// Add persists a graph for a system
	Add(ctx context.Context, systemSymbol string, graph *NavigationGraph) error
}

// IGraphBuilder defines operations for building system graphs from API
type IGraphBuilder interface {
	// BuildSystemGraph fetches all waypoints from API and builds navigation graph
	// Returns strongly-typed NavigationGraph with waypoints and edges
	BuildSystemGraph(ctx context.Context, systemSymbol string, playerID int) (*NavigationGraph, error)
}

// ISystemGraphProvider defines operations for system graph management with caching
type ISystemGraphProvider interface {
	// GetGraph retrieves system navigation graph (checks cache first, builds from API if needed)
	// playerID is required for API authentication when building graphs
	GetGraph(ctx context.Context, systemSymbol string, forceRefresh bool, playerID int) (*GraphLoadResult, error)
}

// IWaypointProvider defines operations for waypoint data retrieval with auto-fetch
type IWaypointProvider interface {
	// GetWaypoint retrieves waypoint data (auto-fetches from API if not cached)
	// Flow: Try database cache → build system graph if needed → retry database
	// playerID is required for API authentication when building the system graph
	GetWaypoint(ctx context.Context, waypointSymbol, systemSymbol string, playerID int) (*shared.Waypoint, error)
}

// IWaypointConverter defines operations for converting graph data to domain Waypoint objects
type IWaypointConverter interface {
	// ConvertGraphToWaypoints converts graph waypoints to Waypoint objects with optional trait enrichment
	// Args:
	//   graph: Graph structure with waypoints data (map[string]interface{})
	//   waypointTraits: Optional lookup map of Waypoint objects with full trait data
	// Returns:
	//   Map of waypoint_symbol -> Waypoint objects
	ConvertGraphToWaypoints(graph map[string]interface{}, waypointTraits map[string]*shared.Waypoint) map[string]*shared.Waypoint

	// ConvertWaypointFromMap converts a single waypoint from map[string]interface{} to domain Waypoint
	ConvertWaypointFromMap(symbol string, wpMap map[string]interface{}) *shared.Waypoint
}

// DTOs for system operations

type WaypointAPIData struct {
	Symbol   string
	Type     string
	X        float64
	Y        float64
	Traits   []map[string]interface{}
	Orbitals []map[string]string
}

type WaypointsListResponse struct {
	Data []WaypointAPIData
	Meta PaginationMeta
}

type PaginationMeta struct {
	Total int
	Page  int
	Limit int
}

type GraphLoadResult struct {
	Graph   *NavigationGraph
	Source  string // "database" or "api"
	Message string
}

type WaypointData struct {
	Symbol  string
	X       float64
	Y       float64
	HasFuel bool
}
