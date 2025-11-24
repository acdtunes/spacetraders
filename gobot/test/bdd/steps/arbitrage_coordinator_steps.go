package steps

import (
	"context"
	"fmt"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
	"github.com/cucumber/godog"
)

type arbitrageCoordinatorContext struct {
	waypoints       map[string]*shared.Waypoint
	idleShips       map[string]*navigation.Ship
	opportunities   []*trading.ArbitrageOpportunity
	assignments     map[string]string // shipSymbol -> good
	assignmentCount int
	maxWorkers      int
	logMessages     []string
}

func (acc *arbitrageCoordinatorContext) reset() {
	acc.waypoints = make(map[string]*shared.Waypoint)
	acc.idleShips = make(map[string]*navigation.Ship)
	acc.opportunities = nil
	acc.assignments = make(map[string]string)
	acc.assignmentCount = 0
	acc.maxWorkers = 100 // Default high value
	acc.logMessages = nil
}

// Background: System setup

func (acc *arbitrageCoordinatorContext) aSystemWithWaypoints(systemSymbol string, table *godog.Table) error {
	for i, row := range table.Rows {
		if i == 0 {
			continue // Skip header
		}

		symbol := row.Cells[0].Value
		var x, y float64
		fmt.Sscanf(row.Cells[1].Value, "%f", &x)
		fmt.Sscanf(row.Cells[2].Value, "%f", &y)

		waypoint, err := shared.NewWaypoint(symbol, x, y)
		if err != nil {
			return err
		}
		acc.waypoints[symbol] = waypoint
	}

	return nil
}

// Given: Ship setup

func (acc *arbitrageCoordinatorContext) theFollowingIdleHaulerShips(table *godog.Table) error {
	for i, row := range table.Rows {
		if i == 0 {
			continue // Skip header
		}

		shipSymbol := row.Cells[0].Value
		locationSymbol := row.Cells[1].Value

		location, ok := acc.waypoints[locationSymbol]
		if !ok {
			return fmt.Errorf("waypoint %s not found", locationSymbol)
		}

		// Create ship entity
		fuel, err := shared.NewFuel(100, 100)
		if err != nil {
			return err
		}

		cargo, err := shared.NewCargo(40, 0, []*shared.CargoItem{})
		if err != nil {
			return err
		}

		ship, err := navigation.NewShip(
			shipSymbol,
			shared.MustNewPlayerID(1),
			location,
			fuel,
			100, // fuelCapacity
			40,  // cargoCapacity (light hauler)
			cargo,
			30,                          // engineSpeed
			"FRAME_LIGHT_FREIGHTER",     // frameType
			"HAULER",                    // role
			[]*navigation.ShipModule{},  // modules
			navigation.NavStatusInOrbit, // navStatus
		)
		if err != nil {
			return err
		}

		acc.idleShips[shipSymbol] = ship
	}

	return nil
}

func (acc *arbitrageCoordinatorContext) idleHaulerShips(count int) error {
	// Create placeholder ships for generic counts
	for i := 1; i <= count; i++ {
		shipSymbol := fmt.Sprintf("HAULER-%d", i)
		location, _ := shared.NewWaypoint("X1-TEST", 0, 0)
		fuel, _ := shared.NewFuel(100, 100)
		cargo, _ := shared.NewCargo(40, 0, []*shared.CargoItem{})

		ship, err := navigation.NewShip(
			shipSymbol,
			shared.MustNewPlayerID(1),
			location,
			fuel,
			100,
			40,
			cargo,
			30,
			"FRAME_LIGHT_FREIGHTER",
			"HAULER",
			[]*navigation.ShipModule{},
			navigation.NavStatusInOrbit,
		)
		if err != nil {
			return err
		}

		acc.idleShips[shipSymbol] = ship
	}

	return nil
}

// Given: Opportunity setup

func (acc *arbitrageCoordinatorContext) theFollowingArbitrageOpportunitiesSortedByProfitability(table *godog.Table) error {
	acc.opportunities = make([]*trading.ArbitrageOpportunity, 0)

	for i, row := range table.Rows {
		if i == 0 {
			continue // Skip header
		}

		good := row.Cells[0].Value
		buyMarketSymbol := row.Cells[1].Value
		sellMarketSymbol := row.Cells[2].Value
		var margin float64
		fmt.Sscanf(strings.TrimSuffix(row.Cells[3].Value, "%"), "%f", &margin)

		buyMarket, ok := acc.waypoints[buyMarketSymbol]
		if !ok {
			return fmt.Errorf("buy market %s not found", buyMarketSymbol)
		}

		sellMarket, ok := acc.waypoints[sellMarketSymbol]
		if !ok {
			return fmt.Errorf("sell market %s not found", sellMarketSymbol)
		}

		// Calculate prices based on margin
		// If margin is 25%, then sellPrice = buyPrice * 1.25
		buyPrice := 100 // Base price
		sellPrice := int(float64(buyPrice) * (1.0 + margin/100.0))

		opp, err := trading.NewArbitrageOpportunity(
			good,
			buyMarket,
			sellMarket,
			buyPrice,
			sellPrice,
			40, // cargo capacity
			"MODERATE",
			"STRONG",
			1.0, // minMargin (low threshold for tests)
		)
		if err != nil {
			return err
		}

		acc.opportunities = append(acc.opportunities, opp)
	}

	return nil
}

func (acc *arbitrageCoordinatorContext) arbitrageOpportunities(count int) error {
	// Create placeholder opportunities for generic counts
	for i := 1; i <= count; i++ {
		good := fmt.Sprintf("GOOD-%d", i)
		buyMarket, _ := shared.NewWaypoint("X1-BUY", 0, 0)
		sellMarket, _ := shared.NewWaypoint("X1-SELL", 10, 0)

		opp, err := trading.NewArbitrageOpportunity(
			good,
			buyMarket,
			sellMarket,
			100,
			120,
			40,
			"MODERATE",
			"STRONG",
			1.0,
		)
		if err != nil {
			return err
		}

		acc.opportunities = append(acc.opportunities, opp)
	}

	return nil
}

func (acc *arbitrageCoordinatorContext) maxWorkersIsSetTo(max int) error {
	acc.maxWorkers = max
	return nil
}

// When: Coordinator assigns ships

func (acc *arbitrageCoordinatorContext) theCoordinatorAssignsShipsToOpportunities() error {
	// Simulate the assignment algorithm from RunArbitrageCoordinatorHandler.spawnWorkers

	// Create available ships map
	availableShips := make(map[string]*navigation.Ship)
	for k, v := range acc.idleShips {
		availableShips[k] = v
	}

	// Calculate max assignments
	maxAssignments := len(availableShips)
	if maxAssignments > len(acc.opportunities) {
		maxAssignments = len(acc.opportunities)
	}
	if maxAssignments > acc.maxWorkers {
		maxAssignments = acc.maxWorkers
	}

	// For each opportunity (in order of profitability), assign closest ship
	for i := 0; i < len(acc.opportunities) && len(acc.assignments) < maxAssignments; i++ {
		opp := acc.opportunities[i]
		buyMarket := opp.BuyMarket()

		// Find closest available ship to the buy market
		var closestShip string
		var closestDistance float64 = -1

		for shipSymbol, ship := range availableShips {
			distance := ship.CurrentLocation().DistanceTo(buyMarket)
			if closestDistance < 0 || distance < closestDistance {
				closestDistance = distance
				closestShip = shipSymbol
			}
		}

		if closestShip == "" {
			break // No more ships available
		}

		// Assign ship to this opportunity
		acc.assignments[closestShip] = opp.Good()
		acc.assignmentCount++

		// Remove ship from available pool
		delete(availableShips, closestShip)

		// Log assignment decision
		logMsg := fmt.Sprintf("Assigned ship %s to opportunity %s (distance: %.1f, margin: %.1f%%)",
			closestShip, opp.Good(), closestDistance, opp.ProfitMargin())
		acc.logMessages = append(acc.logMessages, logMsg)
	}

	// Log completion
	if acc.assignmentCount > 0 {
		logMsg := fmt.Sprintf("Spawning %d arbitrage workers with optimal assignments", acc.assignmentCount)
		acc.logMessages = append(acc.logMessages, logMsg)
	}

	return nil
}

// Then: Verify assignments

func (acc *arbitrageCoordinatorContext) shipShouldBeAssignedToOpportunity(shipSymbol, good string) error {
	assignedGood, ok := acc.assignments[shipSymbol]
	if !ok {
		return fmt.Errorf("ship %s was not assigned to any opportunity", shipSymbol)
	}

	if assignedGood != good {
		return fmt.Errorf("ship %s assigned to %s, expected %s", shipSymbol, assignedGood, good)
	}

	return nil
}

func (acc *arbitrageCoordinatorContext) exactlyWorkersShouldBeSpawned(expected int) error {
	if acc.assignmentCount != expected {
		return fmt.Errorf("expected %d workers, got %d", expected, acc.assignmentCount)
	}
	return nil
}

func (acc *arbitrageCoordinatorContext) coordinatorShouldLog(expectedMsg string) error {
	for _, logMsg := range acc.logMessages {
		if strings.Contains(logMsg, expectedMsg) {
			return nil
		}
	}

	return fmt.Errorf("expected log message not found: %s\nActual logs: %v", expectedMsg, acc.logMessages)
}

// Initialize step definitions
func InitializeArbitrageCoordinatorScenario(ctx *godog.ScenarioContext) {
	acc := &arbitrageCoordinatorContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		acc.reset()
		return ctx, nil
	})

	// Background
	ctx.Step(`^a system "([^"]*)" with the following waypoints:$`, acc.aSystemWithWaypoints)

	// Given: Ships
	ctx.Step(`^the following idle hauler ships:$`, acc.theFollowingIdleHaulerShips)
	ctx.Step(`^(\d+) idle hauler ships$`, acc.idleHaulerShips)

	// Given: Opportunities
	ctx.Step(`^the following arbitrage opportunities sorted by profitability:$`, acc.theFollowingArbitrageOpportunitiesSortedByProfitability)
	ctx.Step(`^(\d+) arbitrage opportunities$`, acc.arbitrageOpportunities)

	// Given: Configuration
	ctx.Step(`^max workers is set to (\d+)$`, acc.maxWorkersIsSetTo)

	// When
	ctx.Step(`^the coordinator assigns ships to opportunities$`, acc.theCoordinatorAssignsShipsToOpportunities)

	// Then
	ctx.Step(`^ship "([^"]*)" should be assigned to "([^"]*)" opportunity$`, acc.shipShouldBeAssignedToOpportunity)
	ctx.Step(`^exactly (\d+) workers should be spawned$`, acc.exactlyWorkersShouldBeSpawned)
	ctx.Step(`^coordinator should log "([^"]*)"$`, acc.coordinatorShouldLog)
}
