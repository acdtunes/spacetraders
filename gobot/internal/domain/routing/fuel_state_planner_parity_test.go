package routing

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// The goldens are recorded from the OR-Tools service's fuel-state Dijkstra, the
// engine that defined this route contract, over the real X1-MG48 system graph
// plus two hand-built graphs that isolate the starved and multi-refuel edges.
// testdata/generate_route_goldens.py rewrites them.
//
// tie_class says how strictly a case must match. Both engines break an equal-cost
// tie on generation order, and generation order follows the order waypoints are
// handed over in, so a case whose waypoints arrive sorted (the Go planner's own
// scan order) must match step for step. A case whose waypoints arrive shuffled is
// free to return a DIFFERENT equally-optimal path: the two engines then walk the
// graph in different orders, and only total time and total fuel are contractual.
const routeGoldensPath = "testdata/route_goldens.json"

type routeGoldens struct {
	Graphs map[string][]goldenWaypoint `json:"graphs"`
	Cases  []goldenCase                `json:"cases"`
}

type goldenWaypoint struct {
	Symbol  string  `json:"symbol"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	HasFuel bool    `json:"has_fuel"`
}

type goldenCase struct {
	Name     string         `json:"name"`
	Graph    string         `json:"graph"`
	TieClass string         `json:"tie_class"`
	Request  goldenRequest  `json:"request"`
	Response goldenResponse `json:"response"`
}

// waypoints are the graph as the case hands them to the planner. A case carries
// its own list only when that order is not the sorted one.
func (g routeGoldens) waypoints(c goldenCase) []goldenWaypoint {
	if len(c.Request.Waypoints) > 0 {
		return c.Request.Waypoints
	}
	return g.Graphs[c.Graph]
}

type goldenRequest struct {
	SystemSymbol  string           `json:"system_symbol"`
	StartWaypoint string           `json:"start_waypoint"`
	GoalWaypoint  string           `json:"goal_waypoint"`
	CurrentFuel   int              `json:"current_fuel"`
	FuelCapacity  int              `json:"fuel_capacity"`
	EngineSpeed   int              `json:"engine_speed"`
	FuelEfficient bool             `json:"fuel_efficient"`
	PreferCruise  bool             `json:"prefer_cruise"`
	Waypoints     []goldenWaypoint `json:"waypoints"`
}

type goldenResponse struct {
	Success          bool         `json:"success"`
	ErrorMessage     string       `json:"error_message"`
	Steps            []goldenStep `json:"steps"`
	TotalFuelCost    int          `json:"total_fuel_cost"`
	TotalTimeSeconds int          `json:"total_time_seconds"`
	TotalDistance    float64      `json:"total_distance"`
}

type goldenStep struct {
	Action       string  `json:"action"`
	Waypoint     string  `json:"waypoint"`
	FuelCost     int     `json:"fuel_cost"`
	TimeSeconds  int     `json:"time_seconds"`
	Distance     float64 `json:"distance"`
	Mode         string  `json:"mode"`
	RefuelAmount int     `json:"refuel_amount"`
}

func (r goldenRequest) toRouteRequest(rows []goldenWaypoint) *RouteRequest {
	waypoints := make([]*system.WaypointData, len(rows))
	for i, wp := range rows {
		waypoints[i] = &system.WaypointData{Symbol: wp.Symbol, X: wp.X, Y: wp.Y, HasFuel: wp.HasFuel}
	}
	return &RouteRequest{
		SystemSymbol:  r.SystemSymbol,
		StartWaypoint: r.StartWaypoint,
		GoalWaypoint:  r.GoalWaypoint,
		CurrentFuel:   r.CurrentFuel,
		FuelCapacity:  r.FuelCapacity,
		EngineSpeed:   r.EngineSpeed,
		FuelEfficient: r.FuelEfficient,
		PreferCruise:  r.PreferCruise,
		Waypoints:     waypoints,
	}
}

// sameDistance compares route totals. Per-leg distances are compared exactly;
// only their SUM is compared with a tolerance, because the reference engine adds
// them with compensated summation and the planner adds them in order, which puts
// a long multi-leg route's total a single ulp apart. No consumer reads the total
// distance, and the tolerance is far tighter than any real divergence.
func sameDistance(got, want float64) bool {
	const relativeTolerance = 1e-12
	diff := math.Abs(got - want)
	return diff <= relativeTolerance*math.Max(1, math.Abs(want))
}

func loadRouteGoldens(t *testing.T) routeGoldens {
	t.Helper()
	raw, err := os.ReadFile(routeGoldensPath)
	if err != nil {
		t.Fatalf("read goldens: %v", err)
	}
	var goldens routeGoldens
	if err := json.Unmarshal(raw, &goldens); err != nil {
		t.Fatalf("parse goldens: %v", err)
	}
	if len(goldens.Cases) == 0 {
		t.Fatal("goldens carry no cases")
	}
	return goldens
}

func TestFuelStatePlanner_MatchesRecordedRoutingServiceRoutes(t *testing.T) {
	goldens := loadRouteGoldens(t)
	exact, costOnly := 0, 0

	for _, testCase := range goldens.Cases {
		t.Run(testCase.Name, func(t *testing.T) {
			plan, err := planFuelStateRoute(context.Background(), testCase.Request.toRouteRequest(goldens.waypoints(testCase)))

			if !testCase.Response.Success {
				if err == nil {
					t.Fatalf("want no-route error, got a %d-step plan", len(plan.Steps))
				}
				return
			}
			if err != nil {
				t.Fatalf("plan route: %v", err)
			}
			if plan.TotalTimeSeconds != testCase.Response.TotalTimeSeconds {
				t.Errorf("total time = %d, want %d", plan.TotalTimeSeconds, testCase.Response.TotalTimeSeconds)
			}
			if plan.TotalFuelCost != testCase.Response.TotalFuelCost {
				t.Errorf("total fuel = %d, want %d", plan.TotalFuelCost, testCase.Response.TotalFuelCost)
			}
			if testCase.TieClass == "cost_only" {
				costOnly++
				return
			}
			exact++

			if !sameDistance(plan.TotalDistance, testCase.Response.TotalDistance) {
				t.Errorf("total distance = %v, want %v", plan.TotalDistance, testCase.Response.TotalDistance)
			}
			if len(plan.Steps) != len(testCase.Response.Steps) {
				t.Fatalf("step count = %d, want %d", len(plan.Steps), len(testCase.Response.Steps))
			}
			for i, want := range testCase.Response.Steps {
				got := plan.Steps[i]
				action := "TRAVEL"
				if got.Action == RouteActionRefuel {
					action = "REFUEL"
				}
				mode := got.Mode
				if mode == "" {
					mode = defaultFlightMode
				}
				if action != want.Action || got.Waypoint != want.Waypoint ||
					got.FuelCost != want.FuelCost || got.TimeSeconds != want.TimeSeconds ||
					got.Distance != want.Distance || mode != want.Mode ||
					got.RefuelAmount != want.RefuelAmount {
					t.Errorf("step %d = {%s %s fuel=%d time=%d dist=%v mode=%s refuel=%d}, want {%s %s fuel=%d time=%d dist=%v mode=%s refuel=%d}",
						i, action, got.Waypoint, got.FuelCost, got.TimeSeconds, got.Distance, mode, got.RefuelAmount,
						want.Action, want.Waypoint, want.FuelCost, want.TimeSeconds, want.Distance, want.Mode, want.RefuelAmount)
				}
			}
		})
	}

	if exact == 0 || costOnly == 0 {
		t.Fatalf("goldens must cover both tie classes: %d exact, %d cost-only", exact, costOnly)
	}
}

// The projection onto the port's DTO carries the totals through untouched and
// stamps CRUISE on the refuel steps the search leaves unmarked.
func TestFuelStatePlanner_ProjectsRecordedRouteOntoPortResponse(t *testing.T) {
	goldens := loadRouteGoldens(t)
	planner := NewFuelStatePlanner()

	for _, testCase := range goldens.Cases {
		if !testCase.Response.Success || testCase.TieClass == "cost_only" {
			continue
		}
		t.Run(testCase.Name, func(t *testing.T) {
			resp, err := planner.PlanRoute(context.Background(), testCase.Request.toRouteRequest(goldens.waypoints(testCase)))
			if err != nil {
				t.Fatalf("plan route: %v", err)
			}
			if resp.TotalTimeSeconds != testCase.Response.TotalTimeSeconds ||
				resp.TotalFuelCost != testCase.Response.TotalFuelCost ||
				!sameDistance(resp.TotalDistance, testCase.Response.TotalDistance) {
				t.Fatalf("totals = (%d, %d, %v), want (%d, %d, %v)",
					resp.TotalTimeSeconds, resp.TotalFuelCost, resp.TotalDistance,
					testCase.Response.TotalTimeSeconds, testCase.Response.TotalFuelCost, testCase.Response.TotalDistance)
			}
			if len(resp.Steps) != len(testCase.Response.Steps) {
				t.Fatalf("step count = %d, want %d", len(resp.Steps), len(testCase.Response.Steps))
			}
			for i, want := range testCase.Response.Steps {
				got := resp.Steps[i]
				if got.Waypoint != want.Waypoint || got.FuelCost != want.FuelCost ||
					got.TimeSeconds != want.TimeSeconds || got.Mode != want.Mode {
					t.Errorf("step %d = {%s fuel=%d time=%d mode=%s}, want {%s fuel=%d time=%d mode=%s}",
						i, got.Waypoint, got.FuelCost, got.TimeSeconds, got.Mode,
						want.Waypoint, want.FuelCost, want.TimeSeconds, want.Mode)
				}
			}
		})
	}
}

func BenchmarkFuelStatePlanner_SystemGraph(b *testing.B) {
	raw, err := os.ReadFile(routeGoldensPath)
	if err != nil {
		b.Fatalf("read goldens: %v", err)
	}
	var goldens routeGoldens
	if err := json.Unmarshal(raw, &goldens); err != nil {
		b.Fatalf("parse goldens: %v", err)
	}
	planner := NewFuelStatePlanner()
	ctx := context.Background()

	for _, testCase := range goldens.Cases {
		if !testCase.Response.Success {
			continue
		}
		req := testCase.Request.toRouteRequest(goldens.waypoints(testCase))
		b.Run(testCase.Name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := planner.PlanRoute(ctx, req); err != nil {
					b.Fatalf("plan route: %v", err)
				}
			}
		})
	}
}
