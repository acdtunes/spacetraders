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
	Save(ctx context.Context, waypoint *shared.Waypoint) error
}

// SystemGraphRepository defines operations for system graph persistence
type SystemGraphRepository interface {
	// Get retrieves a graph for a system from cache
	Get(ctx context.Context, systemSymbol string) (map[string]interface{}, error)

	// Save persists a graph for a system
	Save(ctx context.Context, systemSymbol string, graph map[string]interface{}) error
}

// IGraphBuilder defines operations for building system graphs from API
type IGraphBuilder interface {
	// BuildSystemGraph fetches all waypoints from API and builds navigation graph
	// Returns graph dict: {waypoints: {symbol: {...}}, edges: [{from, to, distance, type}]}
	BuildSystemGraph(ctx context.Context, systemSymbol string, playerID int) (map[string]interface{}, error)
}

// ISystemGraphProvider defines operations for system graph management with caching
type ISystemGraphProvider interface {
	// GetGraph retrieves system navigation graph (checks cache first, builds from API if needed)
	// playerID is required for API authentication when building graphs
	GetGraph(ctx context.Context, systemSymbol string, forceRefresh bool, playerID int) (*GraphLoadResult, error)
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
	Graph   map[string]interface{}
	Source  string // "database" or "api"
	Message string
}

type WaypointData struct {
	Symbol  string
	X       float64
	Y       float64
	HasFuel bool
}
