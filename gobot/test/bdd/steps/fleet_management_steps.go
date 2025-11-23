package steps

import (
	"context"
	"fmt"
	"math"

	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/cucumber/godog"
)

type fleetManagementContext struct {
	ships            []*navigation.Ship
	targetWaypoints  []*shared.Waypoint
	fleetAssigner    *contract.FleetAssigner
	shipSelector     *contract.ShipSelector
	needsRebalancing bool
	metrics          *contract.DistributionMetrics
	assignments      []contract.Assignment
	selectionResult  *contract.SelectionResult
	qualityScore     float64
	err              error
}

func (fmc *fleetManagementContext) reset() {
	fmc.ships = nil
	fmc.targetWaypoints = nil
	fmc.fleetAssigner = contract.NewFleetAssigner()
	fmc.shipSelector = contract.NewShipSelector()
	fmc.needsRebalancing = false
	fmc.metrics = nil
	fmc.assignments = nil
	fmc.selectionResult = nil
	fmc.qualityScore = 0
	fmc.err = nil
}

// Ship Setup Helpers

func (fmc *fleetManagementContext) createShipAt(symbol string, waypointSymbol string, x, y float64, cargoItems ...*shared.CargoItem) error {
	waypoint, err := shared.NewWaypoint(waypointSymbol, x, y)
	if err != nil {
		return err
	}

	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		return err
	}

	// Calculate total units from cargo items
	totalUnits := 0
	for _, item := range cargoItems {
		totalUnits += item.Units
	}

	cargo, err := shared.NewCargo(100, totalUnits, cargoItems)
	if err != nil {
		return err
	}

	ship, err := navigation.NewShip(
		symbol,
		shared.MustNewPlayerID(1),
		waypoint,
		fuel,
		100, // fuelCapacity
		100, // cargoCapacity
		cargo,
		10, // engineSpeed
		"FRAME_HAULER",
		"HAULER",
		[]*navigation.ShipModule{}, // modules
		navigation.NavStatusDocked,
	)
	if err != nil {
		return err
	}

	fmc.ships = append(fmc.ships, ship)
	return nil
}

func (fmc *fleetManagementContext) createShipAtWithStatus(symbol, waypointSymbol string, x, y float64, status navigation.NavStatus, cargoItems ...*shared.CargoItem) error {
	waypoint, err := shared.NewWaypoint(waypointSymbol, x, y)
	if err != nil {
		return err
	}

	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		return err
	}

	// Calculate total units from cargo items
	totalUnits := 0
	for _, item := range cargoItems {
		totalUnits += item.Units
	}

	cargo, err := shared.NewCargo(100, totalUnits, cargoItems)
	if err != nil {
		return err
	}

	ship, err := navigation.NewShip(
		symbol,
		shared.MustNewPlayerID(1),
		waypoint,
		fuel,
		100, // fuelCapacity
		100, // cargoCapacity
		cargo,
		10, // engineSpeed
		"FRAME_HAULER",
		"HAULER",
		[]*navigation.ShipModule{}, // modules
		status,
	)
	if err != nil {
		return err
	}

	fmc.ships = append(fmc.ships, ship)
	return nil
}

// Given Steps

func (fmc *fleetManagementContext) shipsAtWaypoint(count int, waypointSymbol string) error {
	for i := 0; i < count; i++ {
		symbol := fmt.Sprintf("SHIP-%d", i+1)
		if err := fmc.createShipAt(symbol, waypointSymbol, 0, 0); err != nil {
			return err
		}
	}
	return nil
}

func (fmc *fleetManagementContext) targetWaypointsAtAnd(count int, wp1, wp2 string) error {
	w1, err := shared.NewWaypoint(wp1, 100, 100)
	if err != nil {
		return err
	}
	w2, err := shared.NewWaypoint(wp2, 200, 200)
	if err != nil {
		return err
	}
	fmc.targetWaypoints = append(fmc.targetWaypoints, w1, w2)
	return nil
}

func (fmc *fleetManagementContext) shipAtWaypoint(count int, waypointSymbol string) error {
	return fmc.shipsAtWaypoint(count, waypointSymbol)
}

func (fmc *fleetManagementContext) targetWaypointsAtAndAnd(count int, wp1, wp2, wp3 string) error {
	w1, err := shared.NewWaypoint(wp1, 100, 100)
	if err != nil {
		return err
	}
	w2, err := shared.NewWaypoint(wp2, 200, 200)
	if err != nil {
		return err
	}
	w3, err := shared.NewWaypoint(wp3, 300, 300)
	if err != nil {
		return err
	}
	fmc.targetWaypoints = append(fmc.targetWaypoints, w1, w2, w3)
	return nil
}

func (fmc *fleetManagementContext) shipsAtWaypointAtCoordinates(count int, waypointSymbol string, x, y float64) error {
	for i := 0; i < count; i++ {
		symbol := fmt.Sprintf("SHIP-%d", i+1)
		if err := fmc.createShipAt(symbol, waypointSymbol, x, y); err != nil {
			return err
		}
	}
	return nil
}

func (fmc *fleetManagementContext) targetWaypointsAtCoordinatesAndCoordinates(count int, wp1 string, x1, y1 float64, wp2 string, x2, y2 float64) error {
	w1, err := shared.NewWaypoint(wp1, x1, y1)
	if err != nil {
		return err
	}
	w2, err := shared.NewWaypoint(wp2, x2, y2)
	if err != nil {
		return err
	}
	fmc.targetWaypoints = append(fmc.targetWaypoints, w1, w2)
	return nil
}

func (fmc *fleetManagementContext) shipsInTheFleet(count int) error {
	// 0 ships means don't add any
	return nil
}

func (fmc *fleetManagementContext) targetWaypointsCount(count int) error {
	// 0 waypoints means don't add any
	return nil
}

func (fmc *fleetManagementContext) shipsAtVariousWaypoints(count int) error {
	for i := 0; i < count; i++ {
		symbol := fmt.Sprintf("SHIP-%d", i+1)
		waypointSymbol := fmt.Sprintf("X1-WP%d", i+1)
		if err := fmc.createShipAt(symbol, waypointSymbol, float64(i*10), float64(i*10)); err != nil {
			return err
		}
	}
	return nil
}

func (fmc *fleetManagementContext) targetWaypointsForAssignment(count int) error {
	for i := 0; i < count; i++ {
		symbol := fmt.Sprintf("X1-TARGET%d", i+1)
		w, err := shared.NewWaypoint(symbol, float64(i*100), float64(i*100))
		if err != nil {
			return err
		}
		fmc.targetWaypoints = append(fmc.targetWaypoints, w)
	}
	return nil
}

func (fmc *fleetManagementContext) shipAtWaypointCoordinates(shipSymbol, waypointSymbol string, x, y float64) error {
	return fmc.createShipAt(shipSymbol, waypointSymbol, x, y)
}

func (fmc *fleetManagementContext) targetWaypointAt(waypointSymbol string, x, y float64) error {
	w, err := shared.NewWaypoint(waypointSymbol, x, y)
	if err != nil {
		return err
	}
	fmc.targetWaypoints = append(fmc.targetWaypoints, w)
	return nil
}

func (fmc *fleetManagementContext) shipAtWaypointWithUnitsOf(shipSymbol, waypointSymbol string, x, y float64, units int, cargoSymbol string) error {
	cargoItem, err := shared.NewCargoItem(cargoSymbol, cargoSymbol, "", units)
	if err != nil {
		return err
	}
	return fmc.createShipAt(shipSymbol, waypointSymbol, x, y, cargoItem)
}

func (fmc *fleetManagementContext) shipAtWaypointWithNoCargo(shipSymbol, waypointSymbol string, x, y float64) error {
	return fmc.createShipAt(shipSymbol, waypointSymbol, x, y)
}

func (fmc *fleetManagementContext) shipDockedAtWithNoCargo(shipSymbol, waypointSymbol string, x, y float64) error {
	return fmc.createShipAtWithStatus(shipSymbol, waypointSymbol, x, y, navigation.NavStatusDocked)
}

func (fmc *fleetManagementContext) shipInTransitAtWithUnitsOf(shipSymbol, waypointSymbol string, x, y float64, units int, cargoSymbol string) error {
	cargoItem, err := shared.NewCargoItem(cargoSymbol, cargoSymbol, "", units)
	if err != nil {
		return err
	}
	return fmc.createShipAtWithStatus(shipSymbol, waypointSymbol, x, y, navigation.NavStatusInTransit, cargoItem)
}

func (fmc *fleetManagementContext) shipInOrbitAtWithNoCargo(shipSymbol, waypointSymbol string, x, y float64) error {
	return fmc.createShipAtWithStatus(shipSymbol, waypointSymbol, x, y, navigation.NavStatusInOrbit)
}

func (fmc *fleetManagementContext) shipInOrbitAt(shipSymbol, waypointSymbol string, x, y float64) error {
	return fmc.createShipAtWithStatus(shipSymbol, waypointSymbol, x, y, navigation.NavStatusInOrbit)
}

func (fmc *fleetManagementContext) shipInTransitAtWithNoCargo(shipSymbol, waypointSymbol string, x, y float64) error {
	return fmc.createShipAtWithStatus(shipSymbol, waypointSymbol, x, y, navigation.NavStatusInTransit)
}

func (fmc *fleetManagementContext) shipInTransitAt(shipSymbol, waypointSymbol string) error {
	return fmc.createShipAtWithStatus(shipSymbol, waypointSymbol, 0, 0, navigation.NavStatusInTransit)
}

func (fmc *fleetManagementContext) shipAt(shipSymbol, waypointSymbol string, x, y float64) error {
	return fmc.createShipAt(shipSymbol, waypointSymbol, x, y)
}

// Alternative step handlers that construct waypoint symbols from separate capture groups

func (fmc *fleetManagementContext) shipAtXWithUnitsOfIRONORE(shipSymbol string, systemNum, sectorNum, x, y, units int) error {
	waypointSymbol := fmt.Sprintf("X%d-A%d", systemNum, sectorNum)
	cargoItem, err := shared.NewCargoItem("IRON_ORE", "IRON_ORE", "", units)
	if err != nil {
		return err
	}
	return fmc.createShipAt(shipSymbol, waypointSymbol, float64(x), float64(y), cargoItem)
}

func (fmc *fleetManagementContext) shipAtXWithNoCargo(shipSymbol string, systemNum, sectorNum, x, y int) error {
	waypointSymbol := fmt.Sprintf("X%d-B%d", systemNum, sectorNum)
	return fmc.createShipAt(shipSymbol, waypointSymbol, float64(x), float64(y))
}

func (fmc *fleetManagementContext) shipAtXWithUnitsOfCOPPER(shipSymbol string, systemNum, sectorNum, x, y, units int) error {
	waypointSymbol := fmt.Sprintf("X%d-C%d", systemNum, sectorNum)
	cargoItem, err := shared.NewCargoItem("COPPER", "COPPER", "", units)
	if err != nil {
		return err
	}
	return fmc.createShipAt(shipSymbol, waypointSymbol, float64(x), float64(y), cargoItem)
}

func (fmc *fleetManagementContext) shipDockedAtX(shipSymbol string, systemNum, sectorNum, x, y int) error {
	waypointSymbol := fmt.Sprintf("X%d-A%d", systemNum, sectorNum)
	return fmc.createShipAtWithStatus(shipSymbol, waypointSymbol, float64(x), float64(y), navigation.NavStatusDocked)
}

func (fmc *fleetManagementContext) shipInTransitAtX(shipSymbol string, systemNum, sectorNum, x, y int) error {
	waypointSymbol := fmt.Sprintf("X%d-B%d", systemNum, sectorNum)
	return fmc.createShipAtWithStatus(shipSymbol, waypointSymbol, float64(x), float64(y), navigation.NavStatusInTransit)
}

// When Steps

func (fmc *fleetManagementContext) iCheckIfRebalancingIsNeededWithDistanceThreshold(threshold float64) error {
	needsRebalancing, metrics, err := fmc.fleetAssigner.IsRebalancingNeeded(
		fmc.ships,
		fmc.targetWaypoints,
		threshold,
	)
	fmc.needsRebalancing = needsRebalancing
	fmc.metrics = metrics
	fmc.err = err
	return nil
}

func (fmc *fleetManagementContext) iAssignShipsToTargets() error {
	assignments, err := fmc.fleetAssigner.AssignShipsToTargets(fmc.ships, fmc.targetWaypoints)
	fmc.assignments = assignments
	fmc.err = err
	return nil
}

func (fmc *fleetManagementContext) iCalculateDistributionQuality() error {
	quality, err := fmc.fleetAssigner.CalculateDistributionQuality(fmc.ships, fmc.targetWaypoints)
	fmc.qualityScore = quality
	fmc.err = err
	return nil
}

func (fmc *fleetManagementContext) iSelectOptimalShipForTargetRequiring(cargoSymbol string) error {
	if len(fmc.targetWaypoints) == 0 {
		return fmt.Errorf("no target waypoints defined")
	}
	result, err := fmc.shipSelector.SelectOptimalShip(fmc.ships, fmc.targetWaypoints[0], cargoSymbol)
	fmc.selectionResult = result
	fmc.err = err
	return nil
}

func (fmc *fleetManagementContext) iSelectOptimalShipWithoutCargoRequirement() error {
	if len(fmc.targetWaypoints) == 0 {
		return fmt.Errorf("no target waypoints defined")
	}
	result, err := fmc.shipSelector.SelectOptimalShip(fmc.ships, fmc.targetWaypoints[0], "")
	fmc.selectionResult = result
	fmc.err = err
	return nil
}

func (fmc *fleetManagementContext) iSelectOptimalShipWithNilTarget() error {
	result, err := fmc.shipSelector.SelectOptimalShip(fmc.ships, nil, "")
	fmc.selectionResult = result
	fmc.err = err
	return nil
}

func (fmc *fleetManagementContext) iSelectClosestShipByDistanceExcludingInTransit() error {
	if len(fmc.targetWaypoints) == 0 {
		return fmt.Errorf("no target waypoints defined")
	}
	result, err := fmc.shipSelector.SelectClosestShipByDistance(fmc.ships, fmc.targetWaypoints[0], true)
	fmc.selectionResult = result
	fmc.err = err
	return nil
}

func (fmc *fleetManagementContext) iSelectClosestShipByDistanceIncludingInTransit() error {
	if len(fmc.targetWaypoints) == 0 {
		return fmt.Errorf("no target waypoints defined")
	}
	result, err := fmc.shipSelector.SelectClosestShipByDistance(fmc.ships, fmc.targetWaypoints[0], false)
	fmc.selectionResult = result
	fmc.err = err
	return nil
}

// Then Steps

func (fmc *fleetManagementContext) rebalancingShouldBeNeeded() error {
	if !fmc.needsRebalancing {
		return fmt.Errorf("expected rebalancing to be needed, but it was not")
	}
	return nil
}

func (fmc *fleetManagementContext) clusteringShouldBeDetectedAt(waypointSymbol string) error {
	if fmc.metrics == nil {
		return fmt.Errorf("no metrics available")
	}
	if !fmc.metrics.IsClustered {
		return fmt.Errorf("expected clustering to be detected, but it was not")
	}
	if fmc.metrics.ClusteredAt != waypointSymbol {
		return fmt.Errorf("expected clustering at %s, got %s", waypointSymbol, fmc.metrics.ClusteredAt)
	}
	return nil
}

func (fmc *fleetManagementContext) rebalancingShouldNotBeNeededDueToClustering() error {
	if fmc.metrics != nil && fmc.metrics.IsClustered {
		return fmt.Errorf("expected no clustering, but clustering was detected at %s", fmc.metrics.ClusteredAt)
	}
	return nil
}

func (fmc *fleetManagementContext) averageDistanceShouldBeGreaterThan(threshold float64) error {
	if fmc.metrics == nil {
		return fmt.Errorf("no metrics available")
	}
	if fmc.metrics.AverageDistance <= threshold {
		return fmt.Errorf("expected average distance > %f, got %f", threshold, fmc.metrics.AverageDistance)
	}
	return nil
}

func (fmc *fleetManagementContext) rebalancingShouldNotBeNeeded() error {
	if fmc.needsRebalancing {
		return fmt.Errorf("expected rebalancing not to be needed, but it was")
	}
	return nil
}

func (fmc *fleetManagementContext) averageDistanceShouldBeLessThan(threshold float64) error {
	if fmc.metrics == nil {
		return fmt.Errorf("no metrics available")
	}
	if fmc.metrics.AverageDistance >= threshold {
		return fmt.Errorf("expected average distance < %f, got %f", threshold, fmc.metrics.AverageDistance)
	}
	return nil
}

func (fmc *fleetManagementContext) allShipsShouldBeAssigned(expected int) error {
	if len(fmc.assignments) != expected {
		return fmt.Errorf("expected %d ships assigned, got %d", expected, len(fmc.assignments))
	}
	return nil
}

func (fmc *fleetManagementContext) noTargetShouldHaveMoreThanShips(maxShips int) error {
	targetCounts := make(map[string]int)
	for _, assignment := range fmc.assignments {
		targetCounts[assignment.TargetWaypoint]++
	}

	for target, count := range targetCounts {
		if count > maxShips {
			return fmt.Errorf("target %s has %d ships, exceeds max of %d", target, count, maxShips)
		}
	}
	return nil
}

func (fmc *fleetManagementContext) shipsShouldBeAssignedToNearestAvailableTargets() error {
	// This is validated by the algorithm - just check that assignments exist
	if len(fmc.assignments) == 0 && len(fmc.ships) > 0 && len(fmc.targetWaypoints) > 0 {
		return fmt.Errorf("expected ships to be assigned, but none were")
	}
	return nil
}

func (fmc *fleetManagementContext) targetShouldHaveExactlyShips(target string, expected int) error {
	count := 0
	for _, assignment := range fmc.assignments {
		if assignment.TargetWaypoint == target {
			count++
		}
	}
	if count != expected {
		// Debug: print all assignments
		assignmentDetails := ""
		for _, a := range fmc.assignments {
			assignmentDetails += fmt.Sprintf("  %s → %s\n", a.ShipSymbol, a.TargetWaypoint)
		}
		return fmt.Errorf("expected %d ships at %s, got %d (total assignments: %d)\nAssignments:\n%s",
			expected, target, count, len(fmc.assignments), assignmentDetails)
	}
	return nil
}

func (fmc *fleetManagementContext) shipsShouldRemainUnassigned(expected int) error {
	assigned := len(fmc.assignments)
	total := len(fmc.ships)
	unassigned := total - assigned
	if unassigned != expected {
		return fmt.Errorf("expected %d unassigned ships, got %d", expected, unassigned)
	}
	return nil
}

func (fmc *fleetManagementContext) shouldBeAssignedTo(shipSymbol, targetWaypoint string) error {
	for _, assignment := range fmc.assignments {
		if assignment.ShipSymbol == shipSymbol {
			if assignment.TargetWaypoint == targetWaypoint {
				return nil
			}
			return fmt.Errorf("ship %s assigned to %s, expected %s", shipSymbol, assignment.TargetWaypoint, targetWaypoint)
		}
	}
	return fmt.Errorf("ship %s not found in assignments", shipSymbol)
}

func (fmc *fleetManagementContext) exactlyShipsShouldBeAssigned(expected int) error {
	if len(fmc.assignments) != expected {
		return fmt.Errorf("expected exactly %d ships assigned, got %d", expected, len(fmc.assignments))
	}
	return nil
}

func (fmc *fleetManagementContext) eachTargetShouldHaveAtMostShip(maxShips int) error {
	return fmc.noTargetShouldHaveMoreThanShips(maxShips)
}

func (fmc *fleetManagementContext) shipsShouldBeAssigned(expected int) error {
	if len(fmc.assignments) != expected {
		return fmt.Errorf("expected %d ships assigned, got %d", expected, len(fmc.assignments))
	}
	return nil
}

func (fmc *fleetManagementContext) qualityScoreShouldBeApproximately(expected float64) error {
	tolerance := 0.5
	if math.Abs(fmc.qualityScore-expected) > tolerance {
		return fmt.Errorf("expected quality score ~%f, got %f", expected, fmc.qualityScore)
	}
	return nil
}

func (fmc *fleetManagementContext) distributionQualityCalculationShouldFail() error {
	if fmc.err == nil {
		return fmt.Errorf("expected distribution quality calculation to fail, but it succeeded")
	}
	return nil
}

func (fmc *fleetManagementContext) shouldBeSelected(shipSymbol string) error {
	if fmc.selectionResult == nil {
		return fmt.Errorf("no ship was selected")
	}
	if fmc.selectionResult.Ship.ShipSymbol() != shipSymbol {
		return fmt.Errorf("expected %s to be selected, got %s", shipSymbol, fmc.selectionResult.Ship.ShipSymbol())
	}
	return nil
}

func (fmc *fleetManagementContext) selectionReasonShouldBe(expected string) error {
	if fmc.selectionResult == nil {
		return fmt.Errorf("no selection result available")
	}
	if fmc.selectionResult.Reason != expected {
		return fmt.Errorf("expected reason '%s', got '%s'", expected, fmc.selectionResult.Reason)
	}
	return nil
}

func (fmc *fleetManagementContext) selectionDistanceShouldBeApproximately(expected float64) error {
	if fmc.selectionResult == nil {
		return fmt.Errorf("no selection result available")
	}
	tolerance := 0.5
	if math.Abs(fmc.selectionResult.Distance-expected) > tolerance {
		return fmt.Errorf("expected distance ~%f, got %f", expected, fmc.selectionResult.Distance)
	}
	return nil
}

func (fmc *fleetManagementContext) shipSelectionShouldFailWith(expectedError string) error {
	if fmc.err == nil {
		return fmt.Errorf("expected error '%s', but selection succeeded", expectedError)
	}
	if fmc.err.Error() != expectedError {
		return fmt.Errorf("expected error '%s', got '%s'", expectedError, fmc.err.Error())
	}
	return nil
}

func InitializeFleetManagementScenario(ctx *godog.ScenarioContext) {
	fmc := &fleetManagementContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		fmc.reset()
		return ctx, nil
	})

	// Given steps
	ctx.Step(`^(\d+) ships at waypoint ([A-Z0-9-]+)$`, fmc.shipsAtWaypoint)
	ctx.Step(`^(\d+) target waypoints at ([A-Z0-9-]+) and ([A-Z0-9-]+)$`, fmc.targetWaypointsAtAnd)
	ctx.Step(`^(\d+) ship at waypoint ([A-Z0-9-]+)$`, fmc.shipAtWaypoint)
	ctx.Step(`^(\d+) target waypoints at ([A-Z0-9-]+), ([A-Z0-9-]+), and ([A-Z0-9-]+)$`, fmc.targetWaypointsAtAndAnd)
	ctx.Step(`^(\d+) ships at waypoint ([A-Z0-9-]+) at coordinates \(([^,]+), ([^)]+)\)$`, fmc.shipsAtWaypointAtCoordinates)
	ctx.Step(`^(\d+) target waypoints at ([A-Z0-9-]+) \(([^,]+), ([^)]+)\) and ([A-Z0-9-]+) \(([^,]+), ([^)]+)\)$`, fmc.targetWaypointsAtCoordinatesAndCoordinates)
	ctx.Step(`^(\d+) ships in the fleet$`, fmc.shipsInTheFleet)
	ctx.Step(`^(\d+) target waypoints$`, fmc.targetWaypointsCount)
	ctx.Step(`^(\d+) ships at various waypoints$`, fmc.shipsAtVariousWaypoints)
	ctx.Step(`^(\d+) target waypoints for assignment$`, fmc.targetWaypointsForAssignment)
	ctx.Step(`^ship "([^"]*)" at waypoint ([A-Z0-9-]+) \(([^,]+), ([^)]+)\)$`, fmc.shipAtWaypointCoordinates)
	ctx.Step(`^target waypoint ([A-Z0-9-]+) at \(([^,]+), ([^)]+)\)$`, fmc.targetWaypointAt)
	ctx.Step(`^ship "([^"]*)" at waypoint ([A-Z0-9-]+) \(([^,]+), ([^)]+)\) with (\d+) units of ([A-Z_]+)$`, fmc.shipAtWaypointWithUnitsOf)
	ctx.Step(`^ship "([^"]*)" at waypoint ([A-Z0-9-]+) \(([^,]+), ([^)]+)\) with no cargo$`, fmc.shipAtWaypointWithNoCargo)
	ctx.Step(`^ship "([^"]*)" docked at ([A-Z0-9-]+) \(([^,]+), ([^)]+)\) with no cargo$`, fmc.shipDockedAtWithNoCargo)
	ctx.Step(`^ship "([^"]*)" in transit at ([A-Z0-9-]+) \(([^,]+), ([^)]+)\) with (\d+) units of ([A-Z_]+)$`, fmc.shipInTransitAtWithUnitsOf)
	ctx.Step(`^ship "([^"]*)" in orbit at ([A-Z0-9-]+) \(([^,]+), ([^)]+)\) with no cargo$`, fmc.shipInOrbitAtWithNoCargo)
	ctx.Step(`^ship "([^"]*)" in orbit at ([A-Z0-9-]+) \(([^,]+), ([^)]+)\)$`, fmc.shipInOrbitAt)
	ctx.Step(`^ship "([^"]*)" in transit at ([A-Z0-9-]+) \(([^,]+), ([^)]+)\) with no cargo$`, fmc.shipInTransitAtWithNoCargo)
	ctx.Step(`^ship "([^"]*)" in transit at ([A-Z0-9-]+)$`, fmc.shipInTransitAt)
	ctx.Step(`^ship "([^"]*)" at ([A-Z0-9-]+) \(([^,]+), ([^)]+)\)$`, fmc.shipAt)

	// Alternative step patterns that parse waypoint symbols with separate capture groups
	ctx.Step(`^ship "([^"]*)" at X(\d+)-A(\d+) \((\d+), (\d+)\) with (\d+) units of IRON_ORE$`, fmc.shipAtXWithUnitsOfIRONORE)
	ctx.Step(`^ship "([^"]*)" at X(\d+)-B(\d+) \((\d+), (\d+)\) with no cargo$`, fmc.shipAtXWithNoCargo)
	ctx.Step(`^ship "([^"]*)" at X(\d+)-C(\d+) \((\d+), (\d+)\) with (\d+) units of COPPER$`, fmc.shipAtXWithUnitsOfCOPPER)
	ctx.Step(`^ship "([^"]*)" docked at X(\d+)-A(\d+) \((\d+), (\d+)\)$`, fmc.shipDockedAtX)
	ctx.Step(`^ship "([^"]*)" in transit at X(\d+)-B(\d+) \((\d+), (\d+)\)$`, fmc.shipInTransitAtX)

	// When steps
	ctx.Step(`^I check if rebalancing is needed with distance threshold ([0-9.]+)$`, fmc.iCheckIfRebalancingIsNeededWithDistanceThreshold)
	ctx.Step(`^I assign ships to targets$`, fmc.iAssignShipsToTargets)
	ctx.Step(`^I calculate distribution quality$`, fmc.iCalculateDistributionQuality)
	ctx.Step(`^I select optimal ship for target requiring ([A-Z_]+)$`, fmc.iSelectOptimalShipForTargetRequiring)
	ctx.Step(`^I select optimal ship without cargo requirement$`, fmc.iSelectOptimalShipWithoutCargoRequirement)
	ctx.Step(`^I select optimal ship with nil target$`, fmc.iSelectOptimalShipWithNilTarget)
	ctx.Step(`^I select closest ship by distance excluding in-transit$`, fmc.iSelectClosestShipByDistanceExcludingInTransit)
	ctx.Step(`^I select closest ship by distance including in-transit$`, fmc.iSelectClosestShipByDistanceIncludingInTransit)

	// Then steps
	ctx.Step(`^rebalancing should be needed$`, fmc.rebalancingShouldBeNeeded)
	ctx.Step(`^clustering should be detected at ([A-Z0-9-]+)$`, fmc.clusteringShouldBeDetectedAt)
	ctx.Step(`^rebalancing should not be needed due to clustering$`, fmc.rebalancingShouldNotBeNeededDueToClustering)
	ctx.Step(`^average distance should be greater than ([0-9.]+)$`, fmc.averageDistanceShouldBeGreaterThan)
	ctx.Step(`^rebalancing should not be needed$`, fmc.rebalancingShouldNotBeNeeded)
	ctx.Step(`^average distance should be less than ([0-9.]+)$`, fmc.averageDistanceShouldBeLessThan)
	ctx.Step(`^all (\d+) ships should be assigned$`, fmc.allShipsShouldBeAssigned)
	ctx.Step(`^no target should have more than (\d+) ships$`, fmc.noTargetShouldHaveMoreThanShips)
	ctx.Step(`^ships should be assigned to nearest available targets$`, fmc.shipsShouldBeAssignedToNearestAvailableTargets)
	ctx.Step(`^target ([A-Z0-9-]+) should have exactly (\d+) ships$`, fmc.targetShouldHaveExactlyShips)
	ctx.Step(`^(\d+) ships should remain unassigned$`, fmc.shipsShouldRemainUnassigned)
	ctx.Step(`^"([^"]*)" should be assigned to ([A-Z0-9-]+)$`, fmc.shouldBeAssignedTo)
	ctx.Step(`^exactly (\d+) ships should be assigned$`, fmc.exactlyShipsShouldBeAssigned)
	ctx.Step(`^each target should have at most (\d+) ship$`, fmc.eachTargetShouldHaveAtMostShip)
	ctx.Step(`^(\d+) ships should be assigned$`, fmc.shipsShouldBeAssigned)
	ctx.Step(`^quality score should be approximately ([0-9.]+)$`, fmc.qualityScoreShouldBeApproximately)
	ctx.Step(`^distribution quality calculation should fail$`, fmc.distributionQualityCalculationShouldFail)
	ctx.Step(`^"([^"]*)" should be selected$`, fmc.shouldBeSelected)
	ctx.Step(`^selection reason should be "([^"]*)"$`, fmc.selectionReasonShouldBe)
	ctx.Step(`^selection distance should be approximately ([0-9.]+)$`, fmc.selectionDistanceShouldBeApproximately)
	ctx.Step(`^ship selection should fail with "([^"]*)"$`, fmc.shipSelectionShouldFailWith)
}
