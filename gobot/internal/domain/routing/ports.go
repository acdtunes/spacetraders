package routing

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// RoutingClient defines operations for route planning (OR-Tools gRPC service)
type RoutingClient interface {
	PlanRoute(ctx context.Context, request *RouteRequest) (*RouteResponse, error)
	OptimizeTour(ctx context.Context, request *TourRequest) (*TourResponse, error)
	OptimizeFueledTour(ctx context.Context, request *FueledTourRequest) (*FueledTourResponse, error)
	PartitionFleet(ctx context.Context, request *VRPRequest) (*VRPResponse, error)
	// OptimizeTradeTour plans a depth-aware multi-hop trade tour over the fitted
	// market model (sp-1ek0). snapshot carries per-(waypoint,good) prices; waypoints
	// carries coordinates so the planner prices travel time for real (empty → flat
	// defaults); deposits offers haul-to-storage sinks (sp-dchv Lane C, empty → pure
	// arb); absorption carries outstanding cross-container depth per (waypoint,good,side)
	// the solver nets out of available tranche capacity (sp-78ai L3, empty → full depth).
	// Returns a TourPlan whose Feasible=false carries a structured reason so the executor
	// can fail open to single-lane trading.
	OptimizeTradeTour(ctx context.Context, snapshot []TourGoodSnapshot, waypoints []TourWaypoint, ship TourShipState, cons TourConstraints, deposits []TourDepositCandidate, absorption []TourMarketAbsorption, stockSources []TourStockSource) (*TourPlan, error)
}

// DTOs for routing operations

type RouteRequest struct {
	SystemSymbol  string
	StartWaypoint string
	GoalWaypoint  string
	CurrentFuel   int
	FuelCapacity  int
	EngineSpeed   int
	Waypoints     []*system.WaypointData
	FuelEfficient bool // When true, removes DRIFT penalty for fuel-efficient routes
	PreferCruise  bool // When true, prefer CRUISE over BURN for fuel efficiency
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
	AllWaypoints  []*system.WaypointData
}

type TourResponse struct {
	VisitOrder       []string
	CombinedRoute    []*RouteStepData
	TotalTimeSeconds int
}

// FueledTourRequest for globally fuel-optimized tour
type FueledTourRequest struct {
	SystemSymbol    string
	StartWaypoint   string
	TargetWaypoints []string
	ReturnWaypoint  string // Optional: set to return after tour
	CurrentFuel     int
	FuelCapacity    int
	EngineSpeed     int
	AllWaypoints    []*system.WaypointData
}

// FueledTourResponse with legs containing flight modes and refuel flags
type FueledTourResponse struct {
	VisitOrder       []string
	Legs             []*TourLegData
	TotalTimeSeconds int
	TotalFuelCost    int
	TotalDistance    float64
	RefuelStops      int
}

// TourLegData represents a single leg of a fueled tour
type TourLegData struct {
	FromWaypoint      string
	ToWaypoint        string
	FlightMode        string // "BURN", "CRUISE", or "DRIFT"
	FuelCost          int
	TimeSeconds       int
	Distance          float64
	RefuelBefore      bool
	RefuelAmount      int
	IntermediateStops []*IntermediateStopData
}

// IntermediateStopData for multi-hop legs with refuel stops
type IntermediateStopData struct {
	Waypoint     string
	FlightMode   string
	FuelCost     int
	TimeSeconds  int
	RefuelAmount int
}

type VRPRequest struct {
	SystemSymbol    string
	ShipSymbols     []string
	MarketWaypoints []string
	ShipConfigs     map[string]*ShipConfigData
	AllWaypoints    []*system.WaypointData
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
