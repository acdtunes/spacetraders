package routing

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// RoutingClient defines operations for route planning (OR-Tools gRPC service)
type RoutingClient interface {
	PlanRoute(ctx context.Context, request *RouteRequest) (*RouteResponse, error)
	OptimizeTour(ctx context.Context, request *TourRequest) (*TourResponse, error)
	PartitionFleet(ctx context.Context, request *VRPRequest) (*VRPResponse, error)
}

// DTOs for routing operations

type RouteRequest struct {
	SystemSymbol   string
	StartWaypoint  string
	GoalWaypoint   string
	CurrentFuel    int
	FuelCapacity   int
	EngineSpeed    int
	Waypoints      []*system.WaypointData
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
