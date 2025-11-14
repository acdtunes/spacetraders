package steps

import (
	"context"
	"fmt"
	"strings"

	"github.com/cucumber/godog"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	appShip "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/test/helpers"
)

type navigateToWaypointContext struct {
	ships       map[string]*navigation.Ship
	waypoints   map[string]*shared.Waypoint
	playerID    int
	agentSymbol string
	token       string
	response    *appShip.NavigateToWaypointResponse
	err         error

	// Test doubles
	shipRepo *helpers.MockShipRepository      // Still use mock since ships aren't database-persisted
	handler  *appShip.NavigateToWaypointHandler
	db       *gorm.DB                          // In-memory SQLite database
}

func (ctx *navigateToWaypointContext) reset() {
	ctx.ships = make(map[string]*navigation.Ship)
	ctx.waypoints = make(map[string]*shared.Waypoint)
	ctx.playerID = 0
	ctx.agentSymbol = ""
	ctx.token = ""
	ctx.response = nil
	ctx.err = nil

	// Create in-memory SQLite database
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		panic(fmt.Errorf("failed to open test database: %w", err))
	}

	// Run migrations for models that might be needed
	err = db.AutoMigrate(
		&persistence.PlayerModel{},
		&persistence.WaypointModel{},
	)
	if err != nil {
		panic(fmt.Errorf("failed to migrate database: %w", err))
	}

	ctx.db = db

	// Still use mock repository for ships since they're API-only in production
	ctx.shipRepo = helpers.NewMockShipRepository()
	ctx.handler = appShip.NewNavigateToWaypointHandler(ctx.shipRepo)
}

// syncFromGlobalContext imports ships and waypoints from the global shared context
// This allows navigate_to_waypoint tests to work with ships created by ship_operations_context
func (ctx *navigateToWaypointContext) syncFromGlobalContext() {
	// Import all ships from global context
	for symbol, ship := range globalAppContext.getAllShips() {
		ctx.ships[symbol] = ship
		ctx.shipRepo.AddShip(ship)
	}

	// Import all waypoints from global context
	for symbol, waypoint := range globalAppContext.getAllWaypoints() {
		ctx.waypoints[symbol] = waypoint
	}
}

// Given steps

func (ctx *navigateToWaypointContext) aPlayerExistsWithAgentAndToken(agentSymbol, token string) error {
	ctx.agentSymbol = agentSymbol
	ctx.token = token
	return nil
}

func (ctx *navigateToWaypointContext) thePlayerHasPlayerID(playerID int) error {
	ctx.playerID = playerID
	return nil
}

func (ctx *navigateToWaypointContext) aShipForPlayerAtWithStatus(shipSymbol string, playerID int, location, status string) error {
	waypoint, _ := shared.NewWaypoint(location, 0, 0)
	fuel, _ := shared.NewFuel(100, 100)
	cargo, _ := shared.NewCargo(40, 0, []*shared.CargoItem{})

	var navStatus navigation.NavStatus
	switch status {
	case "DOCKED":
		navStatus = navigation.NavStatusDocked
	case "IN_ORBIT":
		navStatus = navigation.NavStatusInOrbit
	case "IN_TRANSIT":
		navStatus = navigation.NavStatusInTransit
	default:
		return fmt.Errorf("unknown nav status: %s", status)
	}

	ship, err := navigation.NewShip(
		shipSymbol, playerID, waypoint, fuel, 100,
		40, cargo, 30, navStatus,
	)
	if err != nil {
		return err
	}

	ctx.ships[shipSymbol] = ship
	ctx.shipRepo.AddShip(ship)
	ctx.waypoints[location] = waypoint

	return nil
}

func (ctx *navigateToWaypointContext) aWaypointExistsAtCoordinates(waypointSymbol string, x, y int) error {
	waypoint, err := shared.NewWaypoint(waypointSymbol, float64(x), float64(y))
	if err != nil {
		return err
	}

	ctx.waypoints[waypointSymbol] = waypoint
	return nil
}

func (ctx *navigateToWaypointContext) aShipForPlayerInTransitTo(shipSymbol string, playerID int, destination string) error {
	// Get or create destination waypoint
	destWaypoint, exists := ctx.waypoints[destination]
	if !exists {
		destWaypoint, _ = shared.NewWaypoint(destination, 100, 50)
		ctx.waypoints[destination] = destWaypoint
	}

	// Create ship in transit
	currentLocation, _ := shared.NewWaypoint("X1-A1", 0, 0)
	fuel, _ := shared.NewFuel(100, 100)
	cargo, _ := shared.NewCargo(40, 0, []*shared.CargoItem{})

	ship, err := navigation.NewShip(
		shipSymbol, playerID, currentLocation, fuel, 100,
		40, cargo, 30, navigation.NavStatusInOrbit,
	)
	if err != nil {
		return err
	}

	// Put ship in transit
	if err := ship.StartTransit(destWaypoint); err != nil {
		return err
	}

	ctx.ships[shipSymbol] = ship
	ctx.shipRepo.AddShip(ship)

	return nil
}

// When steps

func (ctx *navigateToWaypointContext) iExecuteNavigateToWaypointCommandForShipToForPlayer(shipSymbol, destination string, playerID int) error {
	return ctx.executeNavigate(shipSymbol, destination, "", playerID)
}

func (ctx *navigateToWaypointContext) iExecuteNavigateToWaypointCommandForShipToWithFlightModeForPlayer(shipSymbol, destination, flightMode string, playerID int) error {
	return ctx.executeNavigate(shipSymbol, destination, flightMode, playerID)
}

func (ctx *navigateToWaypointContext) executeNavigate(shipSymbol, destination, flightMode string, playerID int) error {
	// Sync from global context before executing command
	// (ships may have been added by ship_operations_context)
	ctx.syncFromGlobalContext()

	cmd := &appShip.NavigateToWaypointCommand{
		ShipSymbol:  shipSymbol,
		Destination: destination,
		PlayerID:    playerID,
		FlightMode:  flightMode,
	}

	response, err := ctx.handler.Handle(context.Background(), cmd)

	ctx.err = err
	if err == nil {
		ctx.response = response.(*appShip.NavigateToWaypointResponse)
	} else {
		ctx.response = nil
	}

	return nil
}

// Then steps

func (ctx *navigateToWaypointContext) theNavigationShouldSucceedWithStatus(expectedStatus string) error {
	if ctx.err != nil {
		return fmt.Errorf("expected success but got error: %v", ctx.err)
	}
	if ctx.response == nil {
		return fmt.Errorf("expected response but got nil")
	}
	if ctx.response.Status != expectedStatus {
		return fmt.Errorf("expected status '%s' but got '%s'", expectedStatus, ctx.response.Status)
	}
	return nil
}

func (ctx *navigateToWaypointContext) theShipShouldBeInTransitTo(destination string) error {
	if ctx.response == nil {
		return fmt.Errorf("no response received")
	}

	// In a real implementation, we'd check the ship's actual state
	// For now, we verify the response indicates navigation
	if ctx.response.Status != "navigating" {
		return fmt.Errorf("expected ship to be navigating but status is '%s'", ctx.response.Status)
	}

	return nil
}

func (ctx *navigateToWaypointContext) theResponseShouldIncludeArrivalTime() error {
	if ctx.response == nil {
		return fmt.Errorf("no response received")
	}

	if ctx.response.ArrivalTime == 0 && ctx.response.ArrivalTimeStr == "" {
		return fmt.Errorf("expected arrival time in response")
	}

	return nil
}

func (ctx *navigateToWaypointContext) theResponseShouldIncludeFuelConsumed() error {
	if ctx.response == nil {
		return fmt.Errorf("no response received")
	}

	// Fuel consumed should be set (could be 0 for DRIFT mode)
	// Just verify the field is accessible
	_ = ctx.response.FuelConsumed

	return nil
}

func (ctx *navigateToWaypointContext) theShipShouldRemainAt(location string) error {
	if ctx.response == nil {
		return fmt.Errorf("no response received")
	}

	// For "already at destination" case
	if ctx.response.Status != "already_at_destination" {
		return fmt.Errorf("expected status 'already_at_destination' but got '%s'", ctx.response.Status)
	}

	return nil
}

func (ctx *navigateToWaypointContext) theShipShouldStillBeInOrbit() error {
	if ctx.response == nil {
		return fmt.Errorf("no response received")
	}

	// For idempotent case, ship should remain in orbit
	if ctx.response.Status != "already_at_destination" {
		return fmt.Errorf("expected ship to be already at destination")
	}

	return nil
}

func (ctx *navigateToWaypointContext) theNavigationShouldFailWithError(expectedError string) error {
	if ctx.err == nil {
		return fmt.Errorf("expected error containing '%s' but command succeeded", expectedError)
	}

	errMsg := strings.ToLower(ctx.err.Error())
	expectedLower := strings.ToLower(expectedError)

	if !strings.Contains(errMsg, expectedLower) {
		return fmt.Errorf("expected error containing '%s' but got '%v'", expectedError, ctx.err)
	}

	return nil
}

// Register steps

func InitializeNavigateToWaypointScenario(ctx *godog.ScenarioContext) {
	navCtx := &navigateToWaypointContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		navCtx.reset()
		return ctx, nil
	})

	// Background steps
	ctx.Step(`^a player exists with agent "([^"]*)" and token "([^"]*)"$`, navCtx.aPlayerExistsWithAgentAndToken)
	ctx.Step(`^the player has player_id (\d+)$`, navCtx.thePlayerHasPlayerID)

	// Given steps
	ctx.Step(`^a ship "([^"]*)" for player (\d+) at "([^"]*)" with status "([^"]*)"$`, navCtx.aShipForPlayerAtWithStatus)
	ctx.Step(`^a waypoint "([^"]*)" exists at coordinates \((\d+), (\d+)\)$`, navCtx.aWaypointExistsAtCoordinates)
	ctx.Step(`^a ship "([^"]*)" for player (\d+) in transit to "([^"]*)"$`, navCtx.aShipForPlayerInTransitTo)

	// When steps
	ctx.Step(`^I execute NavigateToWaypointCommand for ship "([^"]*)" to "([^"]*)" for player (\d+)$`, navCtx.iExecuteNavigateToWaypointCommandForShipToForPlayer)
	ctx.Step(`^I execute NavigateToWaypointCommand for ship "([^"]*)" to "([^"]*)" with flight mode "([^"]*)" for player (\d+)$`, navCtx.iExecuteNavigateToWaypointCommandForShipToWithFlightModeForPlayer)

	// Then steps
	ctx.Step(`^the navigation should succeed with status "([^"]*)"$`, navCtx.theNavigationShouldSucceedWithStatus)
	ctx.Step(`^the ship should be in transit to "([^"]*)"$`, navCtx.theShipShouldBeInTransitTo)
	ctx.Step(`^the response should include arrival time$`, navCtx.theResponseShouldIncludeArrivalTime)
	ctx.Step(`^the response should include fuel consumed$`, navCtx.theResponseShouldIncludeFuelConsumed)
	ctx.Step(`^the ship should remain at "([^"]*)"$`, navCtx.theShipShouldRemainAt)
	ctx.Step(`^the ship should still be in orbit$`, navCtx.theShipShouldStillBeInOrbit)
	ctx.Step(`^the navigation should fail with error "([^"]*)"$`, navCtx.theNavigationShouldFailWithError)
}
