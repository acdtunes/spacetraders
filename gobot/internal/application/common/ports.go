package common

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ShipRepository defines ship persistence and API operations
// Following hexagonal architecture: repositories abstract both database and API operations
type ShipRepository interface {
	// FindBySymbol retrieves a ship (from API with waypoint reconstruction)
	FindBySymbol(ctx context.Context, symbol string, playerID int) (*navigation.Ship, error)

	// FindAllByPlayer retrieves all ships for a player (from API with waypoint reconstruction)
	FindAllByPlayer(ctx context.Context, playerID int) ([]*navigation.Ship, error)

	// Save persists ship state to database (if caching is needed)
	Save(ctx context.Context, ship *navigation.Ship) error

	// Navigate executes ship navigation (updates via API)
	// Returns navigation result with arrival time from API
	Navigate(ctx context.Context, ship *navigation.Ship, destination *shared.Waypoint, playerID int) (*NavigationResult, error)

	// Dock docks the ship (updates via API)
	Dock(ctx context.Context, ship *navigation.Ship, playerID int) error

	// Orbit puts ship in orbit (updates via API)
	Orbit(ctx context.Context, ship *navigation.Ship, playerID int) error

	// Refuel refuels the ship (updates via API)
	Refuel(ctx context.Context, ship *navigation.Ship, playerID int, units *int) error

	// SetFlightMode sets the ship's flight mode (updates via API)
	SetFlightMode(ctx context.Context, ship *navigation.Ship, playerID int, mode string) error
}

// PlayerRepository defines player persistence operations
type PlayerRepository interface {
	FindByID(ctx context.Context, playerID int) (*Player, error)
	FindByAgentSymbol(ctx context.Context, agentSymbol string) (*Player, error)
	Save(ctx context.Context, player *Player) error
}

// WaypointRepository defines waypoint persistence operations
type WaypointRepository interface {
	FindBySymbol(ctx context.Context, symbol, systemSymbol string) (*shared.Waypoint, error)
	ListBySystem(ctx context.Context, systemSymbol string) ([]*shared.Waypoint, error)
	Save(ctx context.Context, waypoint *shared.Waypoint) error
}

// APIClient defines operations for interacting with SpaceTraders API
type APIClient interface {
	// Ship operations
	GetShip(ctx context.Context, symbol, token string) (*ShipData, error)
	ListShips(ctx context.Context, token string) ([]*ShipData, error)
	NavigateShip(ctx context.Context, symbol, destination, token string) (*NavigationResult, error)
	OrbitShip(ctx context.Context, symbol, token string) error
	DockShip(ctx context.Context, symbol, token string) error
	RefuelShip(ctx context.Context, symbol, token string, units *int) (*RefuelResult, error)
	SetFlightMode(ctx context.Context, symbol, flightMode, token string) error

	// Player operations
	GetAgent(ctx context.Context, token string) (*AgentData, error)

	// Waypoint operations
	ListWaypoints(ctx context.Context, systemSymbol, token string, page, limit int) (*WaypointsListResponse, error)
}

// RoutingClient defines operations for route planning (OR-Tools gRPC service)
type RoutingClient interface {
	PlanRoute(ctx context.Context, request *RouteRequest) (*RouteResponse, error)
	OptimizeTour(ctx context.Context, request *TourRequest) (*TourResponse, error)
	PartitionFleet(ctx context.Context, request *VRPRequest) (*VRPResponse, error)
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

// GraphLoadResult represents the result of loading a system graph
type GraphLoadResult struct {
	Graph   map[string]interface{}
	Source  string // "database" or "api"
	Message string
}

// ISystemGraphProvider defines operations for system graph management with caching
type ISystemGraphProvider interface {
	// GetGraph retrieves system navigation graph (checks cache first, builds from API if needed)
	GetGraph(ctx context.Context, systemSymbol string, forceRefresh bool) (*GraphLoadResult, error)
}

// DTOs for API operations

type Player struct {
	ID          int
	AgentSymbol string
	Token       string
	Credits     int
	StartingFaction string
	Metadata    map[string]interface{}
}

type ShipData struct {
	Symbol          string
	Location        string
	NavStatus       string
	FuelCurrent     int
	FuelCapacity    int
	CargoCapacity   int
	CargoUnits      int
	EngineSpeed     int
	Cargo           *CargoData
}

type CargoData struct {
	Capacity  int
	Units     int
	Inventory []CargoItemData
}

type CargoItemData struct {
	Symbol      string
	Name        string
	Description string
	Units       int
}

type NavigationResult struct {
	Destination      string
	ArrivalTime      int    // Calculated seconds
	ArrivalTimeStr   string // ISO8601 timestamp from API (e.g., "2024-01-01T12:00:00Z")
	FuelConsumed     int
}

type RefuelResult struct {
	FuelAdded int
	CreditsCost int
}

type AgentData struct {
	AccountID       string
	Symbol          string
	Headquarters    string
	Credits         int
	StartingFaction string
}

type WaypointAPIData struct {
	Symbol  string
	Type    string
	X       float64
	Y       float64
	Traits  []map[string]interface{}
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

// Route planning DTOs

type RouteRequest struct {
	SystemSymbol   string
	StartWaypoint  string
	GoalWaypoint   string
	CurrentFuel    int
	FuelCapacity   int
	EngineSpeed    int
	Waypoints      []*WaypointData
}

type WaypointData struct {
	Symbol  string
	X       float64
	Y       float64
	HasFuel bool
}

type RouteResponse struct {
	Steps            []*RouteStepData
	TotalFuelCost    int
	TotalTimeSeconds int
	TotalDistance    float64
}

type RouteStepData struct {
	Action      RouteAction
	Waypoint    string
	FuelCost    int
	TimeSeconds int
	Mode        string // Flight mode: "BURN", "CRUISE", or "DRIFT"
}

type RouteAction int

const (
	RouteActionTravel RouteAction = iota
	RouteActionRefuel
)

type TourRequest struct {
	SystemSymbol  string
	StartWaypoint string
	Waypoints     []string
	FuelCapacity  int
	EngineSpeed   int
	AllWaypoints  []*WaypointData
}

type TourResponse struct {
	VisitOrder     []string
	CombinedRoute  []*RouteStepData
	TotalTimeSeconds int
}

type VRPRequest struct {
	SystemSymbol    string
	ShipSymbols     []string
	MarketWaypoints []string
	ShipConfigs     map[string]*ShipConfigData
	AllWaypoints    []*WaypointData
}

type ShipConfigData struct {
	CurrentLocation string
	FuelCapacity    int
	EngineSpeed     int
}

type VRPResponse struct {
	Assignments map[string]*ShipTourData
}

type ShipTourData struct {
	Waypoints []string
	Route     []*RouteStepData
}
