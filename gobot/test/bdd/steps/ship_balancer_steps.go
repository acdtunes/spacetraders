package steps

import (
	"context"
	"fmt"
	"math"

	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/cucumber/godog"
)

type shipBalancerContext struct {
	balancer            *domainContract.ShipBalancer
	markets             []*shared.Waypoint
	idleHaulers         []*navigation.Ship
	testShip            *navigation.Ship
	result              *domainContract.BalancingResult
	err                 error
	marketLookup        map[string]*shared.Waypoint
	haulerLookup        map[string]*navigation.Ship
	nearbyHaulersByWp   map[string]int
}

func (sbc *shipBalancerContext) reset() {
	sbc.balancer = domainContract.NewShipBalancer()
	sbc.markets = nil
	sbc.idleHaulers = nil
	sbc.testShip = nil
	sbc.result = nil
	sbc.err = nil
	sbc.marketLookup = make(map[string]*shared.Waypoint)
	sbc.haulerLookup = make(map[string]*navigation.Ship)
	sbc.nearbyHaulersByWp = make(map[string]int)
}

// Setup steps

func (sbc *shipBalancerContext) theFollowingMarketsExist(table *godog.Table) error {
	sbc.markets = make([]*shared.Waypoint, 0)

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
		sbc.markets = append(sbc.markets, waypoint)
		sbc.marketLookup[symbol] = waypoint
	}

	return nil
}

func (sbc *shipBalancerContext) shipIsAtLocation(shipSymbol string, x, y float64) error {
	location, err := shared.NewWaypoint("TEST-LOC", x, y)
	if err != nil {
		return err
	}

	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		return err
	}

	cargo, err := shared.NewCargo(100, 0, []*shared.CargoItem{})
	if err != nil {
		return err
	}

	ship, err := navigation.NewShip(
		shipSymbol,
		shared.MustNewPlayerID(1),
		location,
		fuel,
		100, // fuelCapacity
		100, // cargoCapacity
		cargo,
		30,                            // engineSpeed
		"FRAME_LIGHT_FREIGHTER",       // frameType
		"HAULER",                      // role
		[]*navigation.ShipModule{},    // modules
		navigation.NavStatusInOrbit,   // navStatus
	)
	if err != nil {
		return err
	}

	sbc.testShip = ship
	return nil
}

func (sbc *shipBalancerContext) theFollowingIdleHaulersExist(table *godog.Table) error {
	sbc.idleHaulers = make([]*navigation.Ship, 0)

	for i, row := range table.Rows {
		if i == 0 {
			continue // Skip header
		}

		symbol := row.Cells[0].Value
		var x, y float64
		fmt.Sscanf(row.Cells[1].Value, "%f", &x)
		fmt.Sscanf(row.Cells[2].Value, "%f", &y)

		// Check if there's a market at these exact coordinates
		// If so, use the market symbol so ship counts work
		var locationSymbol string
		for marketSymbol, market := range sbc.marketLookup {
			if market.X == x && market.Y == y {
				locationSymbol = marketSymbol
				break
			}
		}
		if locationSymbol == "" {
			locationSymbol = fmt.Sprintf("LOC-%s", symbol)
		}

		location, err := shared.NewWaypoint(locationSymbol, x, y)
		if err != nil {
			return err
		}

		fuel, err := shared.NewFuel(100, 100)
		if err != nil {
			return err
		}

		cargo, err := shared.NewCargo(100, 0, []*shared.CargoItem{})
		if err != nil {
			return err
		}

		ship, err := navigation.NewShip(
			symbol,
			shared.MustNewPlayerID(1),
			location,
			fuel,
			100, // fuelCapacity
			100, // cargoCapacity
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

		sbc.idleHaulers = append(sbc.idleHaulers, ship)
		sbc.haulerLookup[symbol] = ship
	}

	return nil
}

func (sbc *shipBalancerContext) theFollowingIdleHaulersExistWithinUnitsOf(units float64, marketSymbol string, table *godog.Table) error {
	// First create the haulers
	if err := sbc.theFollowingIdleHaulersExist(table); err != nil {
		return err
	}

	// This step is for documentation purposes - the actual positions in the table
	// determine whether haulers are within the proximity radius
	// The ShipBalancer uses a hardcoded ProximityRadius of 500

	return nil
}

func (sbc *shipBalancerContext) noOtherIdleHaulersExist() error {
	sbc.idleHaulers = []*navigation.Ship{}
	return nil
}

func (sbc *shipBalancerContext) noMarketsExist() error {
	sbc.markets = []*shared.Waypoint{}
	return nil
}

func (sbc *shipBalancerContext) noShipExists() error {
	sbc.testShip = nil
	return nil
}

func (sbc *shipBalancerContext) marketsExist() error {
	// Create a minimal market for error testing scenarios
	if len(sbc.markets) == 0 {
		waypoint, err := shared.NewWaypoint("MARKET-TEST", 0, 0)
		if err != nil {
			return err
		}
		sbc.markets = []*shared.Waypoint{waypoint}
	}
	return nil
}

// Action steps

func (sbc *shipBalancerContext) iCalculateTheOptimalBalancingPosition(shipSymbol string) error {
	if sbc.testShip == nil {
		return fmt.Errorf("test ship not initialized")
	}

	sbc.result, sbc.err = sbc.balancer.SelectOptimalBalancingPosition(
		sbc.testShip,
		sbc.markets,
		sbc.idleHaulers,
	)

	return nil
}

func (sbc *shipBalancerContext) iAttemptToCalculateTheOptimalBalancingPosition(shipSymbol string) error {
	return sbc.iCalculateTheOptimalBalancingPosition(shipSymbol)
}

func (sbc *shipBalancerContext) iAttemptToCalculateTheOptimalBalancingPositionForANilShip() error {
	sbc.result, sbc.err = sbc.balancer.SelectOptimalBalancingPosition(
		nil,
		sbc.markets,
		sbc.idleHaulers,
	)
	return nil
}

func (sbc *shipBalancerContext) iCheckHaulersWithinUnitsOf(units float64, marketSymbol string) error {
	market, exists := sbc.marketLookup[marketSymbol]
	if !exists {
		return fmt.Errorf("market %s not found", marketSymbol)
	}

	count := 0
	for _, hauler := range sbc.idleHaulers {
		distance := hauler.CurrentLocation().DistanceTo(market)
		if distance <= units {
			count++
		}
	}

	sbc.nearbyHaulersByWp[marketSymbol] = count
	return nil
}

// Assertion steps

func (sbc *shipBalancerContext) theSelectedMarketShouldBe(expectedMarket string) error {
	if sbc.err != nil {
		return fmt.Errorf("expected successful operation, got error: %v", sbc.err)
	}
	if sbc.result == nil {
		return fmt.Errorf("result is nil")
	}
	if sbc.result.TargetMarket.Symbol != expectedMarket {
		return fmt.Errorf("expected market %s, got %s", expectedMarket, sbc.result.TargetMarket.Symbol)
	}
	return nil
}

func (sbc *shipBalancerContext) theSelectedMarketShouldNotBe(unexpectedMarket string) error {
	if sbc.err != nil {
		return fmt.Errorf("expected successful operation, got error: %v", sbc.err)
	}
	if sbc.result == nil {
		return fmt.Errorf("result is nil")
	}
	if sbc.result.TargetMarket.Symbol == unexpectedMarket {
		return fmt.Errorf("expected market to not be %s, but it was", unexpectedMarket)
	}
	return nil
}

func (sbc *shipBalancerContext) theBalancingScoreShouldBeApproximately(expectedScore float64) error {
	if sbc.err != nil {
		return fmt.Errorf("expected successful operation, got error: %v", sbc.err)
	}
	if sbc.result == nil {
		return fmt.Errorf("result is nil")
	}

	tolerance := 0.1
	if math.Abs(sbc.result.Score-expectedScore) > tolerance {
		return fmt.Errorf("expected score approximately %.1f, got %.1f", expectedScore, sbc.result.Score)
	}
	return nil
}

func (sbc *shipBalancerContext) thereShouldBeNearbyHaulersAtTheTargetMarket(expectedCount int) error {
	if sbc.err != nil {
		return fmt.Errorf("expected successful operation, got error: %v", sbc.err)
	}
	if sbc.result == nil {
		return fmt.Errorf("result is nil")
	}
	if sbc.result.AssignedShips != expectedCount {
		return fmt.Errorf("expected %d assigned ships, got %d", expectedCount, sbc.result.AssignedShips)
	}
	return nil
}

func (sbc *shipBalancerContext) theDistanceToTargetShouldBe(expectedDistance float64) error {
	if sbc.err != nil {
		return fmt.Errorf("expected successful operation, got error: %v", sbc.err)
	}
	if sbc.result == nil {
		return fmt.Errorf("result is nil")
	}

	tolerance := 0.1
	if math.Abs(sbc.result.Distance-expectedDistance) > tolerance {
		return fmt.Errorf("expected distance %.1f, got %.1f", expectedDistance, sbc.result.Distance)
	}
	return nil
}

func (sbc *shipBalancerContext) theDistanceToTargetShouldBeLessThan(maxDistance float64) error {
	if sbc.err != nil {
		return fmt.Errorf("expected successful operation, got error: %v", sbc.err)
	}
	if sbc.result == nil {
		return fmt.Errorf("result is nil")
	}
	if sbc.result.Distance >= maxDistance {
		return fmt.Errorf("expected distance less than %.1f, got %.1f", maxDistance, sbc.result.Distance)
	}
	return nil
}

func (sbc *shipBalancerContext) theOperationShouldFailWithError(expectedError string) error {
	if sbc.err == nil {
		return fmt.Errorf("expected error '%s', but operation succeeded", expectedError)
	}
	if sbc.err.Error() != expectedError {
		return fmt.Errorf("expected error '%s', got '%s'", expectedError, sbc.err.Error())
	}
	return nil
}

func (sbc *shipBalancerContext) marketShouldHaveNearbyHaulers(marketSymbol string, expectedCount int) error {
	market, exists := sbc.marketLookup[marketSymbol]
	if !exists {
		return fmt.Errorf("market %s not found", marketSymbol)
	}

	count := 0
	for _, hauler := range sbc.idleHaulers {
		distance := hauler.CurrentLocation().DistanceTo(market)
		if distance <= 500 { // ProximityRadius
			count++
		}
	}

	if count != expectedCount {
		return fmt.Errorf("expected %d nearby haulers at %s, got %d", expectedCount, marketSymbol, count)
	}
	return nil
}

func (sbc *shipBalancerContext) thereShouldBeNearbyHaulerAt(expectedCount int, marketSymbol string) error {
	count, exists := sbc.nearbyHaulersByWp[marketSymbol]
	if !exists {
		return fmt.Errorf("no nearby hauler count computed for %s", marketSymbol)
	}

	if count != expectedCount {
		return fmt.Errorf("expected %d nearby haulers at %s, got %d", expectedCount, marketSymbol, count)
	}
	return nil
}

// RegisterShipBalancerSteps registers all ship balancer step definitions
func RegisterShipBalancerSteps(sc *godog.ScenarioContext) {
	ctx := &shipBalancerContext{
		marketLookup:      make(map[string]*shared.Waypoint),
		haulerLookup:      make(map[string]*navigation.Ship),
		nearbyHaulersByWp: make(map[string]int),
	}

	sc.Before(func(_ context.Context, _ *godog.Scenario) (context.Context, error) {
		ctx.reset()
		return context.Background(), nil
	})

	// Setup steps
	sc.Step(`^the following markets exist:$`, ctx.theFollowingMarketsExist)
	sc.Step(`^ship "([^"]*)" is at location \((-?[\d.]+), (-?[\d.]+)\)$`, ctx.shipIsAtLocation)
	sc.Step(`^the following idle haulers exist:$`, ctx.theFollowingIdleHaulersExist)
	sc.Step(`^the following idle haulers exist within ([\d.]+) units of ([^:]+):$`, ctx.theFollowingIdleHaulersExistWithinUnitsOf)
	sc.Step(`^no other idle haulers exist$`, ctx.noOtherIdleHaulersExist)
	sc.Step(`^no markets exist$`, ctx.noMarketsExist)
	sc.Step(`^no ship exists$`, ctx.noShipExists)
	sc.Step(`^markets exist$`, ctx.marketsExist)

	// Action steps
	sc.Step(`^I calculate the optimal balancing position for "([^"]*)"$`, ctx.iCalculateTheOptimalBalancingPosition)
	sc.Step(`^I attempt to calculate the optimal balancing position for "([^"]*)"$`, ctx.iAttemptToCalculateTheOptimalBalancingPosition)
	sc.Step(`^I attempt to calculate the optimal balancing position for a nil ship$`, ctx.iAttemptToCalculateTheOptimalBalancingPositionForANilShip)
	sc.Step(`^I check haulers within ([\d.]+) units of ([^"]+)$`, ctx.iCheckHaulersWithinUnitsOf)

	// Assertion steps
	sc.Step(`^the selected market should be "([^"]*)"$`, ctx.theSelectedMarketShouldBe)
	sc.Step(`^the selected market should not be "([^"]*)"$`, ctx.theSelectedMarketShouldNotBe)
	sc.Step(`^the balancing score should be approximately ([\d.]+)$`, ctx.theBalancingScoreShouldBeApproximately)
	sc.Step(`^there should be (\d+) nearby haulers at the target market$`, ctx.thereShouldBeNearbyHaulersAtTheTargetMarket)
	sc.Step(`^there should be (\d+) assigned ships at the target market$`, ctx.thereShouldBeNearbyHaulersAtTheTargetMarket)
	sc.Step(`^the distance to target should be ([\d.]+)$`, ctx.theDistanceToTargetShouldBe)
	sc.Step(`^the distance to target should be less than ([\d.]+)$`, ctx.theDistanceToTargetShouldBeLessThan)
	sc.Step(`^the operation should fail with error "([^"]*)"$`, ctx.theOperationShouldFailWithError)
	sc.Step(`^([A-Z0-9-]+) should have (\d+) nearby haulers$`, ctx.marketShouldHaveNearbyHaulers)
	sc.Step(`^there should be (\d+) nearby hauler at "([^"]*)"$`, ctx.thereShouldBeNearbyHaulerAt)
}
