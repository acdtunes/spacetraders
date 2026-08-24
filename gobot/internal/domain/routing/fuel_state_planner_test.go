package routing

import (
	"context"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

func wp(symbol string, x, y float64, hasFuel bool) *system.WaypointData {
	return &system.WaypointData{Symbol: symbol, X: x, Y: y, HasFuel: hasFuel}
}

// tieGraph offers two mirror-image refuelling detours of identical cost, so which
// one a route takes is decided purely by the order candidates are generated in.
func tieGraph() []*system.WaypointData {
	return []*system.WaypointData{
		wp("TIE-A", 0, 0, true),
		wp("TIE-G", 200, 0, false),
		wp("TIE-N", 100, 100, true),
		wp("TIE-S", 100, -100, true),
	}
}

func tieRequest(waypoints []*system.WaypointData) *RouteRequest {
	return &RouteRequest{
		SystemSymbol:  "TIE",
		StartWaypoint: "TIE-A",
		GoalWaypoint:  "TIE-G",
		CurrentFuel:   150,
		FuelCapacity:  150,
		EngineSpeed:   30,
		Waypoints:     waypoints,
	}
}

func planOrFail(t *testing.T, req *RouteRequest) *RouteResponse {
	t.Helper()
	resp, err := NewFuelStatePlanner().PlanRoute(context.Background(), req)
	if err != nil {
		t.Fatalf("plan route: %v", err)
	}
	return resp
}

func routeSummary(resp *RouteResponse) string {
	parts := make([]string, 0, len(resp.Steps))
	for _, step := range resp.Steps {
		verb := "TRAVEL"
		if step.Action == RouteActionRefuel {
			verb = "REFUEL"
		}
		parts = append(parts, verb+" "+step.Waypoint+" "+step.Mode)
	}
	return strings.Join(parts, " | ")
}

// The waypoint list arrives from a map iteration, so the same graph reaches the
// planner in a different order on every call. An equal-cost tie must still
// resolve the same way, or two hulls planning the same leg disagree about the
// route and the ETA a selection ranked on is not the route that gets flown.
func TestFuelStatePlanner_TieBreaksIdenticallyWhateverTheWaypointOrder(t *testing.T) {
	sorted := tieGraph()
	reversed := []*system.WaypointData{sorted[3], sorted[2], sorted[1], sorted[0]}
	shuffled := []*system.WaypointData{sorted[2], sorted[0], sorted[3], sorted[1]}

	want := routeSummary(planOrFail(t, tieRequest(sorted)))
	if !strings.Contains(want, "TIE-N") || strings.Contains(want, "TIE-S") {
		t.Fatalf("tie should resolve to the lexicographically first detour, got %s", want)
	}
	for name, order := range map[string][]*system.WaypointData{
		"reversed": reversed,
		"shuffled": shuffled,
	} {
		if got := routeSummary(planOrFail(t, tieRequest(order))); got != want {
			t.Errorf("%s order routed %s, want %s", name, got, want)
		}
	}
}

func TestFuelStatePlanner_RepeatedPlansAreIdentical(t *testing.T) {
	req := tieRequest(tieGraph())
	first := planOrFail(t, req)
	for i := 0; i < 20; i++ {
		if got := routeSummary(planOrFail(t, req)); got != routeSummary(first) {
			t.Fatalf("run %d routed %s, want %s", i, got, routeSummary(first))
		}
	}
}

// A tank below the origin threshold is topped up before departure even when it
// already holds enough fuel to reach the goal, so a route never starts by
// spending the margin it may need later.
func TestFuelStatePlanner_TopsUpAtOriginBelowThreshold(t *testing.T) {
	graph := []*system.WaypointData{
		wp("REF-A", 0, 0, true),
		wp("REF-B", 60, 0, false),
	}
	req := &RouteRequest{
		StartWaypoint: "REF-A", GoalWaypoint: "REF-B",
		CurrentFuel: 100, FuelCapacity: 200, EngineSpeed: 30,
		Waypoints: graph,
	}

	resp := planOrFail(t, req)
	if resp.Steps[0].Action != RouteActionRefuel {
		t.Fatalf("want a refuel before departure, got %s", routeSummary(resp))
	}

	// The same hull one unit over the threshold departs on the tank it has.
	req.CurrentFuel = 180
	if resp := planOrFail(t, req); resp.Steps[0].Action != RouteActionTravel {
		t.Fatalf("want departure without refuelling, got %s", routeSummary(resp))
	}
}

// An origin with no fuel to sell cannot top up however empty the tank is.
func TestFuelStatePlanner_CannotTopUpWhereNoFuelIsSold(t *testing.T) {
	resp := planOrFail(t, &RouteRequest{
		StartWaypoint: "DRY-A", GoalWaypoint: "DRY-B",
		CurrentFuel: 100, FuelCapacity: 200, EngineSpeed: 30,
		Waypoints: []*system.WaypointData{
			wp("DRY-A", 0, 0, false),
			wp("DRY-B", 60, 0, false),
		},
	})
	if resp.Steps[0].Action != RouteActionTravel {
		t.Fatalf("want a straight departure, got %s", routeSummary(resp))
	}
}

// The hop that lands on the goal may spend the safety margin; an identical hop
// to somewhere else may not, and falls back to the cheaper mode.
func TestFuelStatePlanner_OnlyTheGoalHopSpendsTheSafetyMargin(t *testing.T) {
	graph := []*system.WaypointData{
		wp("MAR-A", 0, 0, false),
		wp("MAR-B", 50, 0, false),
		wp("MAR-C", 500, 0, false),
	}
	// 50 units of fuel covers a 50-unit cruise exactly, with nothing left over.
	goalHop := planOrFail(t, &RouteRequest{
		StartWaypoint: "MAR-A", GoalWaypoint: "MAR-B",
		CurrentFuel: 50, FuelCapacity: 200, EngineSpeed: 30,
		Waypoints: graph,
	})
	if goalHop.Steps[0].Mode != "CRUISE" || goalHop.Steps[0].FuelCost != 50 {
		t.Fatalf("want a CRUISE hop costing 50, got %s", routeSummary(goalHop))
	}

	// Reaching MAR-C means passing MAR-B without landing on it, so the same
	// 50-unit hop no longer fits and only DRIFT remains.
	throughHop := planOrFail(t, &RouteRequest{
		StartWaypoint: "MAR-A", GoalWaypoint: "MAR-C",
		CurrentFuel: 50, FuelCapacity: 200, EngineSpeed: 30,
		Waypoints: graph,
	})
	if throughHop.Steps[0].Mode != "DRIFT" {
		t.Fatalf("want a DRIFT hop, got %s", routeSummary(throughHop))
	}
}

// DRIFT is the last resort: it appears only once no fuelled mode fits, and the
// penalty that keeps it there is what FuelEfficient drops.
func TestFuelStatePlanner_DriftIsTheLastResort(t *testing.T) {
	graph := []*system.WaypointData{
		wp("DRI-A", 0, 0, false),
		wp("DRI-B", 150, 0, false),
	}
	fuelled := planOrFail(t, &RouteRequest{
		StartWaypoint: "DRI-A", GoalWaypoint: "DRI-B",
		CurrentFuel: 400, FuelCapacity: 400, EngineSpeed: 30,
		Waypoints: graph,
	})
	if fuelled.Steps[0].Mode != "BURN" {
		t.Fatalf("a full tank should burn, got %s", routeSummary(fuelled))
	}

	starved := &RouteRequest{
		StartWaypoint: "DRI-A", GoalWaypoint: "DRI-B",
		CurrentFuel: 5, FuelCapacity: 400, EngineSpeed: 30,
		Waypoints: graph,
	}
	penalised := planOrFail(t, starved)
	if penalised.Steps[0].Mode != "DRIFT" {
		t.Fatalf("a starved tank should drift, got %s", routeSummary(penalised))
	}

	starved.FuelEfficient = true
	unpenalised := planOrFail(t, starved)
	if unpenalised.Steps[0].Mode != "DRIFT" {
		t.Fatalf("want DRIFT, got %s", routeSummary(unpenalised))
	}
	if unpenalised.TotalTimeSeconds >= penalised.TotalTimeSeconds {
		t.Errorf("fuel-efficient drift = %ds, want less than the penalised %ds",
			unpenalised.TotalTimeSeconds, penalised.TotalTimeSeconds)
	}
	if unpenalised.TotalTimeSeconds+driftTimePenalty != penalised.TotalTimeSeconds {
		t.Errorf("the two should differ by exactly the penalty: %d vs %d",
			unpenalised.TotalTimeSeconds, penalised.TotalTimeSeconds)
	}
}

// PreferCruise withdraws BURN from every hop, which costs time and saves fuel.
func TestFuelStatePlanner_PreferCruiseWithdrawsBurn(t *testing.T) {
	graph := []*system.WaypointData{
		wp("PRC-A", 0, 0, false),
		wp("PRC-B", 300, 0, false),
	}
	req := &RouteRequest{
		StartWaypoint: "PRC-A", GoalWaypoint: "PRC-B",
		CurrentFuel: 700, FuelCapacity: 700, EngineSpeed: 30,
		Waypoints: graph,
	}
	burned := planOrFail(t, req)
	req.PreferCruise = true
	cruised := planOrFail(t, req)

	if burned.Steps[0].Mode != "BURN" || cruised.Steps[0].Mode != "CRUISE" {
		t.Fatalf("modes = %s / %s, want BURN / CRUISE", burned.Steps[0].Mode, cruised.Steps[0].Mode)
	}
	if cruised.TotalTimeSeconds <= burned.TotalTimeSeconds {
		t.Errorf("cruising should be slower: %ds vs %ds", cruised.TotalTimeSeconds, burned.TotalTimeSeconds)
	}
	if cruised.TotalFuelCost >= burned.TotalFuelCost {
		t.Errorf("cruising should be cheaper: %d vs %d", cruised.TotalFuelCost, burned.TotalFuelCost)
	}
}

func TestFuelStatePlanner_UnroutableCasesError(t *testing.T) {
	reachable := []*system.WaypointData{
		wp("UNR-A", 0, 0, false),
		wp("UNR-B", 500, 0, false),
	}
	cases := map[string]*RouteRequest{
		// An empty tank at a waypoint that sells no fuel cannot move at all.
		"stranded on empty": {
			StartWaypoint: "UNR-A", GoalWaypoint: "UNR-B",
			CurrentFuel: 0, FuelCapacity: 400, EngineSpeed: 30, Waypoints: reachable,
		},
		"goal not in graph": {
			StartWaypoint: "UNR-A", GoalWaypoint: "UNR-Z",
			CurrentFuel: 400, FuelCapacity: 400, EngineSpeed: 30, Waypoints: reachable,
		},
		"origin not in graph": {
			StartWaypoint: "UNR-Z", GoalWaypoint: "UNR-B",
			CurrentFuel: 400, FuelCapacity: 400, EngineSpeed: 30, Waypoints: reachable,
		},
		"empty graph": {
			StartWaypoint: "UNR-A", GoalWaypoint: "UNR-B",
			CurrentFuel: 400, FuelCapacity: 400, EngineSpeed: 30,
		},
	}
	planner := NewFuelStatePlanner()
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			if resp, err := planner.PlanRoute(context.Background(), req); err == nil {
				t.Fatalf("want an error, got %s", routeSummary(resp))
			}
		})
	}

	if _, err := planner.PlanRoute(context.Background(), nil); err == nil {
		t.Error("want an error for a nil request")
	}
}

func TestFuelStatePlanner_StartAtGoalIsAnEmptyRoute(t *testing.T) {
	resp := planOrFail(t, &RouteRequest{
		StartWaypoint: "SAM-A", GoalWaypoint: "SAM-A",
		CurrentFuel: 10, FuelCapacity: 400, EngineSpeed: 30,
		Waypoints: []*system.WaypointData{wp("SAM-A", 0, 0, true), wp("SAM-B", 40, 0, true)},
	})
	if len(resp.Steps) != 0 || resp.TotalTimeSeconds != 0 || resp.TotalFuelCost != 0 || resp.TotalDistance != 0 {
		t.Fatalf("want an empty zero-cost route, got %s at %ds", routeSummary(resp), resp.TotalTimeSeconds)
	}
}

// A hull with no tank flies straight to the goal whatever stands between, and
// waypoints sharing coordinates are one second apart rather than free.
func TestFuelStatePlanner_TanklessHullFliesDirect(t *testing.T) {
	graph := []*system.WaypointData{
		wp("PRB-A", 0, 0, false),
		wp("PRB-B", 300, 0, true),
		wp("PRB-C", 900, 0, false),
		wp("PRB-D", 0, 0, false),
	}
	direct := planOrFail(t, &RouteRequest{
		StartWaypoint: "PRB-A", GoalWaypoint: "PRB-C",
		CurrentFuel: 0, FuelCapacity: 0, EngineSpeed: 9,
		Waypoints: graph,
	})
	if len(direct.Steps) != 1 || direct.Steps[0].Waypoint != "PRB-C" ||
		direct.Steps[0].Mode != "CRUISE" || direct.Steps[0].FuelCost != 0 {
		t.Fatalf("want one free CRUISE hop to the goal, got %s", routeSummary(direct))
	}

	orbital := planOrFail(t, &RouteRequest{
		StartWaypoint: "PRB-A", GoalWaypoint: "PRB-D",
		CurrentFuel: 0, FuelCapacity: 0, EngineSpeed: 9,
		Waypoints: graph,
	})
	if orbital.TotalTimeSeconds != orbitalHopSeconds || orbital.TotalDistance != 0 {
		t.Fatalf("want a %ds hop of no distance, got %ds over %v",
			orbitalHopSeconds, orbital.TotalTimeSeconds, orbital.TotalDistance)
	}
}

// A hop between waypoints sharing coordinates is free of fuel and reachable on an
// empty tank, so a stranded hull can still hop to the market it orbits.
func TestFuelStatePlanner_OrbitalHopCostsNoFuel(t *testing.T) {
	resp := planOrFail(t, &RouteRequest{
		StartWaypoint: "ORB-A", GoalWaypoint: "ORB-B",
		CurrentFuel: 0, FuelCapacity: 400, EngineSpeed: 30,
		Waypoints: []*system.WaypointData{
			wp("ORB-A", 12, -8, false),
			wp("ORB-B", 12, -8, true),
		},
	})
	if len(resp.Steps) != 1 || resp.Steps[0].FuelCost != 0 ||
		resp.TotalTimeSeconds != orbitalHopSeconds || resp.Steps[0].Mode != "CRUISE" {
		t.Fatalf("want one free CRUISE hop, got %s at %ds", routeSummary(resp), resp.TotalTimeSeconds)
	}
}

// The search answers a cancelled context rather than running the graph out, so a
// caller's budget still bounds the call.
func TestFuelStatePlanner_HonoursCancelledContext(t *testing.T) {
	goldens := loadRouteGoldens(t)
	var req *RouteRequest
	for _, testCase := range goldens.Cases {
		if testCase.Name == "mg48_full_tank_freighter" {
			req = testCase.Request.toRouteRequest(goldens.waypoints(testCase))
		}
	}
	if req == nil {
		t.Fatal("goldens no longer carry mg48_full_tank_freighter")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewFuelStatePlanner().PlanRoute(ctx, req); err == nil {
		t.Fatal("want the cancellation surfaced as an error")
	}
}

// Two waypoints under one symbol is malformed input, not a crash: the last one
// wins and the search runs on a graph of unique waypoints.
func TestFuelStatePlanner_DuplicateSymbolsCollapse(t *testing.T) {
	resp := planOrFail(t, &RouteRequest{
		StartWaypoint: "DUP-A", GoalWaypoint: "DUP-B",
		CurrentFuel: 400, FuelCapacity: 400, EngineSpeed: 30,
		Waypoints: []*system.WaypointData{
			wp("DUP-A", 0, 0, false),
			nil,
			wp("DUP-B", 9999, 0, false),
			wp("DUP-B", 60, 0, false),
		},
	})
	if len(resp.Steps) != 1 || resp.Steps[0].FuelCost != 120 {
		t.Fatalf("want a single 120-fuel burn to the nearer duplicate, got %s", routeSummary(resp))
	}
}
