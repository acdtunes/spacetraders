package steps

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/cucumber/godog"
)

type systemContext struct {
	graph              *system.NavigationGraph
	retrievedWaypoint  *shared.Waypoint
	retrievalErr       error
	edgesFromWaypoint  []system.GraphEdge
	fuelStations       []*shared.Waypoint
}

func (sc *systemContext) reset() {
	sc.graph = nil
	sc.retrievedWaypoint = nil
	sc.retrievalErr = nil
	sc.edgesFromWaypoint = nil
	sc.fuelStations = nil
}

// Navigation Graph Steps

func (sc *systemContext) aNavigationGraphForSystem(systemSymbol string) error {
	sc.graph = system.NewNavigationGraph(systemSymbol)
	return nil
}

func (sc *systemContext) theGraphShouldHaveSystemSymbol(expectedSymbol string) error {
	if sc.graph.SystemSymbol != expectedSymbol {
		return fmt.Errorf("expected system symbol %s, got %s", expectedSymbol, sc.graph.SystemSymbol)
	}
	return nil
}

func (sc *systemContext) theGraphShouldHaveWaypoints(expectedCount int) error {
	actualCount := sc.graph.WaypointCount()
	if actualCount != expectedCount {
		return fmt.Errorf("expected %d waypoints, got %d", expectedCount, actualCount)
	}
	return nil
}

func (sc *systemContext) theGraphShouldHaveEdges(expectedCount int) error {
	actualCount := sc.graph.EdgeCount()
	if actualCount != expectedCount {
		return fmt.Errorf("expected %d edges, got %d", expectedCount, actualCount)
	}
	return nil
}

func (sc *systemContext) iAddWaypointAtCoordinates(waypointSymbol string, x, y float64) error {
	waypoint, err := shared.NewWaypoint(waypointSymbol, x, y)
	if err != nil {
		return err
	}
	sc.graph.AddWaypoint(waypoint)
	return nil
}

func (sc *systemContext) iAddWaypointAtCoordinatesWithFuel(waypointSymbol string, x, y float64) error {
	waypoint, err := shared.NewWaypoint(waypointSymbol, x, y)
	if err != nil {
		return err
	}
	waypoint.HasFuel = true
	sc.graph.AddWaypoint(waypoint)
	return nil
}

func (sc *systemContext) iAddWaypointAtCoordinatesWithoutFuel(waypointSymbol string, x, y float64) error {
	waypoint, err := shared.NewWaypoint(waypointSymbol, x, y)
	if err != nil {
		return err
	}
	waypoint.HasFuel = false
	sc.graph.AddWaypoint(waypoint)
	return nil
}

func (sc *systemContext) theGraphShouldContainWaypoint(waypointSymbol string) error {
	if !sc.graph.HasWaypoint(waypointSymbol) {
		return fmt.Errorf("expected graph to contain waypoint %s, but it does not", waypointSymbol)
	}
	return nil
}

func (sc *systemContext) theGraphShouldNotContainWaypoint(waypointSymbol string) error {
	if sc.graph.HasWaypoint(waypointSymbol) {
		return fmt.Errorf("expected graph not to contain waypoint %s, but it does", waypointSymbol)
	}
	return nil
}

func (sc *systemContext) iRetrieveWaypointFromTheGraph(waypointSymbol string) error {
	sc.retrievedWaypoint, sc.retrievalErr = sc.graph.GetWaypoint(waypointSymbol)
	return nil
}

func (sc *systemContext) iAttemptToRetrieveWaypointFromTheGraph(waypointSymbol string) error {
	sc.retrievedWaypoint, sc.retrievalErr = sc.graph.GetWaypoint(waypointSymbol)
	return nil
}

func (sc *systemContext) theRetrievalShouldSucceed() error {
	if sc.retrievalErr != nil {
		return fmt.Errorf("expected retrieval to succeed, but got error: %v", sc.retrievalErr)
	}
	if sc.retrievedWaypoint == nil {
		return fmt.Errorf("expected retrieval to succeed with a waypoint, but got nil")
	}
	return nil
}

func (sc *systemContext) theRetrievalShouldFailWithError(expectedError string) error {
	if sc.retrievalErr == nil {
		return fmt.Errorf("expected retrieval to fail with error, but it succeeded")
	}
	if sc.retrievalErr.Error() != expectedError {
		return fmt.Errorf("expected error %q, got %q", expectedError, sc.retrievalErr.Error())
	}
	return nil
}

func (sc *systemContext) theRetrievedWaypointShouldHaveSymbol(expectedSymbol string) error {
	if sc.retrievedWaypoint == nil {
		return fmt.Errorf("no waypoint was retrieved")
	}
	if sc.retrievedWaypoint.Symbol != expectedSymbol {
		return fmt.Errorf("expected waypoint symbol %s, got %s", expectedSymbol, sc.retrievedWaypoint.Symbol)
	}
	return nil
}

func (sc *systemContext) theRetrievedWaypointShouldHaveCoordinates(x, y float64) error {
	if sc.retrievedWaypoint == nil {
		return fmt.Errorf("no waypoint was retrieved")
	}
	if sc.retrievedWaypoint.X != x || sc.retrievedWaypoint.Y != y {
		return fmt.Errorf("expected coordinates (%.1f, %.1f), got (%.1f, %.1f)",
			x, y, sc.retrievedWaypoint.X, sc.retrievedWaypoint.Y)
	}
	return nil
}

func (sc *systemContext) iAddANormalEdgeBetweenAndWithDistance(from, to string, distance float64) error {
	sc.graph.AddEdge(from, to, distance, system.EdgeTypeNormal)
	return nil
}

func (sc *systemContext) iAddAnOrbitalEdgeBetweenAnd(from, to string) error {
	sc.graph.AddEdge(from, to, 0, system.EdgeTypeOrbital)
	return nil
}

func (sc *systemContext) thereShouldBeAnEdgeFromToWithDistance(from, to string, expectedDistance float64) error {
	edges := sc.graph.GetEdges(from)
	for _, edge := range edges {
		if edge.To == to && edge.Distance == expectedDistance {
			return nil
		}
	}
	return fmt.Errorf("expected edge from %s to %s with distance %.1f, but it was not found", from, to, expectedDistance)
}

func (sc *systemContext) thereShouldBeAnOrbitalEdgeFromTo(from, to string) error {
	edges := sc.graph.GetEdges(from)
	for _, edge := range edges {
		if edge.To == to && edge.Type == system.EdgeTypeOrbital {
			return nil
		}
	}
	return fmt.Errorf("expected orbital edge from %s to %s, but it was not found", from, to)
}

func (sc *systemContext) waypointShouldHaveOutgoingEdges(waypointSymbol string, expectedCount int) error {
	edges := sc.graph.GetEdges(waypointSymbol)
	actualCount := len(edges)
	if actualCount != expectedCount {
		return fmt.Errorf("expected waypoint %s to have %d outgoing edges, got %d", waypointSymbol, expectedCount, actualCount)
	}
	return nil
}

func (sc *systemContext) theGraphShouldHaveFuelStations(expectedCount int) error {
	sc.fuelStations = sc.graph.GetFuelStations()
	actualCount := len(sc.fuelStations)
	if actualCount != expectedCount {
		return fmt.Errorf("expected %d fuel stations, got %d", expectedCount, actualCount)
	}
	return nil
}

func (sc *systemContext) theFuelStationsShouldInclude(waypointSymbol string) error {
	for _, station := range sc.fuelStations {
		if station.Symbol == waypointSymbol {
			return nil
		}
	}
	return fmt.Errorf("expected fuel stations to include %s, but it was not found", waypointSymbol)
}

func InitializeSystemScenario(ctx *godog.ScenarioContext) {
	sc := &systemContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		return ctx, nil
	})

	ctx.After(func(ctx context.Context, sc *godog.Scenario, err error) (context.Context, error) {
		return ctx, nil
	})

	// Navigation Graph steps
	ctx.Step(`^a navigation graph for system "([^"]*)"$`, sc.aNavigationGraphForSystem)
	ctx.Step(`^the graph should have system symbol "([^"]*)"$`, sc.theGraphShouldHaveSystemSymbol)
	ctx.Step(`^the graph should have (\d+) waypoints?$`, sc.theGraphShouldHaveWaypoints)
	ctx.Step(`^the graph should have (\d+) edges?$`, sc.theGraphShouldHaveEdges)
	ctx.Step(`^I add waypoint "([^"]*)" at coordinates \((-?\d+(?:\.\d+)?), (-?\d+(?:\.\d+)?)\)$`, sc.iAddWaypointAtCoordinates)
	ctx.Step(`^I add waypoint "([^"]*)" at coordinates \((-?\d+(?:\.\d+)?), (-?\d+(?:\.\d+)?)\) with fuel$`, sc.iAddWaypointAtCoordinatesWithFuel)
	ctx.Step(`^I add waypoint "([^"]*)" at coordinates \((-?\d+(?:\.\d+)?), (-?\d+(?:\.\d+)?)\) without fuel$`, sc.iAddWaypointAtCoordinatesWithoutFuel)
	ctx.Step(`^the graph should contain waypoint "([^"]*)"$`, sc.theGraphShouldContainWaypoint)
	ctx.Step(`^the graph should not contain waypoint "([^"]*)"$`, sc.theGraphShouldNotContainWaypoint)
	ctx.Step(`^I retrieve waypoint "([^"]*)" from the graph$`, sc.iRetrieveWaypointFromTheGraph)
	ctx.Step(`^I attempt to retrieve waypoint "([^"]*)" from the graph$`, sc.iAttemptToRetrieveWaypointFromTheGraph)
	ctx.Step(`^the retrieval should succeed$`, sc.theRetrievalShouldSucceed)
	ctx.Step(`^the retrieval should fail with error "([^"]*)"$`, sc.theRetrievalShouldFailWithError)
	ctx.Step(`^the retrieved waypoint should have symbol "([^"]*)"$`, sc.theRetrievedWaypointShouldHaveSymbol)
	ctx.Step(`^the retrieved waypoint should have coordinates \((-?\d+(?:\.\d+)?), (-?\d+(?:\.\d+)?)\)$`, sc.theRetrievedWaypointShouldHaveCoordinates)
	ctx.Step(`^I add a normal edge between "([^"]*)" and "([^"]*)" with distance (-?\d+(?:\.\d+)?)$`, sc.iAddANormalEdgeBetweenAndWithDistance)
	ctx.Step(`^I add an orbital edge between "([^"]*)" and "([^"]*)"$`, sc.iAddAnOrbitalEdgeBetweenAnd)
	ctx.Step(`^there should be an edge from "([^"]*)" to "([^"]*)" with distance (-?\d+(?:\.\d+)?)$`, sc.thereShouldBeAnEdgeFromToWithDistance)
	ctx.Step(`^there should be an orbital edge from "([^"]*)" to "([^"]*)"$`, sc.thereShouldBeAnOrbitalEdgeFromTo)
	ctx.Step(`^waypoint "([^"]*)" should have (\d+) outgoing edges?$`, sc.waypointShouldHaveOutgoingEdges)
	ctx.Step(`^the graph should have (\d+) fuel stations?$`, sc.theGraphShouldHaveFuelStations)
	ctx.Step(`^the fuel stations should include "([^"]*)"$`, sc.theFuelStationsShouldInclude)
}
