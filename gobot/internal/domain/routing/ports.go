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
