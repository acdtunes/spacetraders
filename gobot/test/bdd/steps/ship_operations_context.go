package steps

import (
	"context"
	"fmt"
	"strings"

	"github.com/cucumber/godog"

	appShip "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/test/helpers"
)

// shipOperationsContext is a shared context for all ship operation commands
// (dock, orbit, set flight mode, etc.) to avoid step definition collisions
type shipOperationsContext struct {
	ships             map[string]*navigation.Ship
	playerID          int
	agentSymbol       string
	token             string
	dockResponse      *appShip.DockShipResponse
	dockErr           error
	orbitResponse     *appShip.OrbitShipResponse
	orbitErr          error
	setFlightModeResp *appShip.SetFlightModeResponse
	setFlightModeErr  error
	err               error

	// Handlers and repositories for testing
	shipRepo             *helpers.MockShipRepository
	dockHandler          *appShip.DockShipHandler
	orbitHandler         *appShip.OrbitShipHandler
	setFlightModeHandler *appShip.SetFlightModeHandler
}

func (ctx *shipOperationsContext) reset() {
	ctx.ships = make(map[string]*navigation.Ship)
	ctx.playerID = 0
	ctx.agentSymbol = ""
	ctx.token = ""
	ctx.dockResponse = nil
	ctx.dockErr = nil
	ctx.orbitResponse = nil
	ctx.orbitErr = nil
	ctx.setFlightModeResp = nil
	ctx.setFlightModeErr = nil
	ctx.err = nil

	// Initialize mock repository and handlers
	ctx.shipRepo = helpers.NewMockShipRepository()
	ctx.dockHandler = appShip.NewDockShipHandler(ctx.shipRepo)
	ctx.orbitHandler = appShip.NewOrbitShipHandler(ctx.shipRepo)
	ctx.setFlightModeHandler = appShip.NewSetFlightModeHandler(ctx.shipRepo)
}

// Player setup steps

func (ctx *shipOperationsContext) aPlayerExistsWithAgentAndToken(agentSymbol, token string) error {
	ctx.agentSymbol = agentSymbol
	ctx.token = token
	// Also update global context for cross-context communication
	globalAppContext.setPlayerInfo(agentSymbol, token, ctx.playerID)
	return nil
}

func (ctx *shipOperationsContext) thePlayerHasPlayerID(playerID int) error {
	ctx.playerID = playerID
	// Also update global context
	globalAppContext.setPlayerInfo(ctx.agentSymbol, ctx.token, playerID)
	return nil
}

// Given steps

func (ctx *shipOperationsContext) aShipForPlayerAtWithStatus(shipSymbol string, playerID int, location, status string) error {
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
	// Add to mock repository so handler can find it
	ctx.shipRepo.AddShip(ship)
	// Also add to global context for cross-context communication
	globalAppContext.addShip(shipSymbol, ship)
	globalAppContext.addWaypoint(location, waypoint)
	return nil
}

func (ctx *shipOperationsContext) aShipForPlayerInTransitTo(shipSymbol string, playerID int, destination string) error {
	waypoint, _ := shared.NewWaypoint("X1-START", 0, 0)
	destWaypoint, _ := shared.NewWaypoint(destination, 100, 0)
	fuel, _ := shared.NewFuel(100, 100)
	cargo, _ := shared.NewCargo(40, 0, []*shared.CargoItem{})

	ship, err := navigation.NewShip(
		shipSymbol, playerID, waypoint, fuel, 100,
		40, cargo, 30, navigation.NavStatusInOrbit,
	)
	if err != nil {
		return err
	}

	// Transition to in-transit
	if err := ship.StartTransit(destWaypoint); err != nil {
		return err
	}

	ctx.ships[shipSymbol] = ship
	// Add to mock repository so handler can find it
	ctx.shipRepo.AddShip(ship)
	// Also add to global context
	globalAppContext.addShip(shipSymbol, ship)
	globalAppContext.addWaypoint(destination, destWaypoint)
	return nil
}

// When steps - Dock Ship

func (ctx *shipOperationsContext) iExecuteDockShipCommandForShipAndPlayer(shipSymbol string, playerID int) error {
	// Create command
	cmd := &appShip.DockShipCommand{
		ShipSymbol: shipSymbol,
		PlayerID:   playerID,
	}

	// Execute handler
	response, err := ctx.dockHandler.Handle(context.Background(), cmd)

	// Store both response and error
	ctx.dockErr = err
	if response != nil {
		ctx.dockResponse = response.(*appShip.DockShipResponse)
	}

	// Update local context with the modified ship if command succeeded
	if err == nil {
		updatedShip, findErr := ctx.shipRepo.FindBySymbol(context.Background(), shipSymbol, playerID)
		if findErr == nil {
			ctx.ships[shipSymbol] = updatedShip
			globalAppContext.updateShip(shipSymbol, updatedShip)
		}
	}

	return nil
}

// When steps - Orbit Ship

func (ctx *shipOperationsContext) iExecuteOrbitShipCommandForShipAndPlayer(shipSymbol string, playerID int) error {
	// Create command
	cmd := &appShip.OrbitShipCommand{
		ShipSymbol: shipSymbol,
		PlayerID:   playerID,
	}

	// Execute handler
	response, err := ctx.orbitHandler.Handle(context.Background(), cmd)

	// Store both response and error
	ctx.orbitErr = err
	if response != nil {
		ctx.orbitResponse = response.(*appShip.OrbitShipResponse)
	}

	// Update local context with the modified ship if command succeeded
	if err == nil {
		updatedShip, findErr := ctx.shipRepo.FindBySymbol(context.Background(), shipSymbol, playerID)
		if findErr == nil {
			ctx.ships[shipSymbol] = updatedShip
			globalAppContext.updateShip(shipSymbol, updatedShip)
		}
	}

	return nil
}

// Then steps - Dock Ship

func (ctx *shipOperationsContext) theDockCommandShouldSucceedWithStatus(expectedStatus string) error {
	// Check that command succeeded (no Go error returned)
	if ctx.dockErr != nil {
		return fmt.Errorf("expected success but got error: %v", ctx.dockErr)
	}

	// Check response was returned
	if ctx.dockResponse == nil {
		return fmt.Errorf("no response received")
	}

	// Check status matches expected
	if ctx.dockResponse.Status != expectedStatus {
		return fmt.Errorf("expected status %s but got %s", expectedStatus, ctx.dockResponse.Status)
	}

	return nil
}

func (ctx *shipOperationsContext) theShipShouldStillBeDocked() error {
	ship := ctx.ships["SHIP-1"]
	if ship == nil {
		return fmt.Errorf("ship not found in context")
	}
	if !ship.IsDocked() {
		return fmt.Errorf("expected ship to be docked but status is %s", ship.NavStatus())
	}
	return nil
}

func (ctx *shipOperationsContext) theShipShouldBeDockedAt(location string) error {
	ship := ctx.ships["SHIP-1"]
	if ship == nil {
		return fmt.Errorf("ship not found in context")
	}
	if !ship.IsDocked() {
		return fmt.Errorf("expected ship to be docked but status is %s", ship.NavStatus())
	}
	if ship.CurrentLocation().Symbol != location {
		return fmt.Errorf("expected ship at %s but it's at %s", location, ship.CurrentLocation().Symbol)
	}
	return nil
}

func (ctx *shipOperationsContext) theDockCommandShouldFailWithError(expectedError string) error {
	// Check that command failed (Go error was returned)
	if ctx.dockErr == nil {
		return fmt.Errorf("expected error but command succeeded with status: %s", ctx.dockResponse.Status)
	}

	// Check error message contains expected text
	if !strings.Contains(ctx.dockErr.Error(), expectedError) {
		return fmt.Errorf("expected error containing '%s' but got '%s'", expectedError, ctx.dockErr.Error())
	}

	return nil
}

// Then steps - Orbit Ship

func (ctx *shipOperationsContext) theOrbitCommandShouldSucceedWithStatus(expectedStatus string) error {
	// Check that command succeeded (no Go error returned)
	if ctx.orbitErr != nil {
		return fmt.Errorf("expected success but got error: %v", ctx.orbitErr)
	}

	// Check response was returned
	if ctx.orbitResponse == nil {
		return fmt.Errorf("no response received")
	}

	// Check status matches expected
	if ctx.orbitResponse.Status != expectedStatus {
		return fmt.Errorf("expected status %s but got %s", expectedStatus, ctx.orbitResponse.Status)
	}

	return nil
}

func (ctx *shipOperationsContext) theShipShouldStillBeInOrbit() error {
	ship := ctx.ships["SHIP-1"]
	if ship == nil {
		return fmt.Errorf("ship not found in context")
	}
	if !ship.IsInOrbit() {
		return fmt.Errorf("expected ship to be in orbit but status is %s", ship.NavStatus())
	}
	return nil
}

func (ctx *shipOperationsContext) theShipShouldBeInOrbitAt(location string) error {
	ship := ctx.ships["SHIP-1"]
	if ship == nil {
		return fmt.Errorf("ship not found in context")
	}
	if !ship.IsInOrbit() {
		return fmt.Errorf("expected ship to be in orbit but status is %s", ship.NavStatus())
	}
	if ship.CurrentLocation().Symbol != location {
		return fmt.Errorf("expected ship at %s but it's at %s", location, ship.CurrentLocation().Symbol)
	}
	return nil
}

func (ctx *shipOperationsContext) theOrbitCommandShouldFailWithError(expectedError string) error {
	// Check that command failed (Go error was returned)
	if ctx.orbitErr == nil {
		return fmt.Errorf("expected error but command succeeded with status: %s", ctx.orbitResponse.Status)
	}

	// Check error message contains expected text
	if !strings.Contains(ctx.orbitErr.Error(), expectedError) {
		return fmt.Errorf("expected error containing '%s' but got '%s'", expectedError, ctx.orbitErr.Error())
	}

	return nil
}

// SetFlightMode command steps

func (ctx *shipOperationsContext) iExecuteSetFlightModeCommandForShipAndPlayerWithMode(shipSymbol string, playerID int, mode string) error {
	// Parse flight mode from string using the domain function
	flightMode, parseErr := shared.ParseFlightMode(mode)

	// If parse fails, create an invalid FlightMode value (999)
	// This allows the handler to validate and return the appropriate error
	if parseErr != nil {
		flightMode = shared.FlightMode(999) // Invalid enum value
	}

	// Create command
	cmd := &appShip.SetFlightModeCommand{
		ShipSymbol: shipSymbol,
		PlayerID:   playerID,
		Mode:       flightMode,
	}

	// Execute handler and capture both response and error
	resp, err := ctx.setFlightModeHandler.Handle(context.Background(), cmd)

	// Store the error (might be nil on success)
	ctx.setFlightModeErr = err

	// Type assert response if not nil
	if resp != nil {
		ctx.setFlightModeResp = resp.(*appShip.SetFlightModeResponse)
	}

	return nil
}

func (ctx *shipOperationsContext) theCommandShouldSucceedWithStatus(expectedStatus string) error {
	// Check that no error was returned (proper Go error handling)
	if ctx.setFlightModeErr != nil {
		return fmt.Errorf("expected success but got error: %v", ctx.setFlightModeErr)
	}

	// Verify response exists
	if ctx.setFlightModeResp == nil {
		return fmt.Errorf("no response received")
	}

	// Note: expectedStatus is ignored since response no longer has Status field
	// Success is determined solely by err == nil
	return nil
}

func (ctx *shipOperationsContext) theCurrentFlightModeShouldBe(expectedMode string) error {
	// Verify response exists
	if ctx.setFlightModeResp == nil {
		return fmt.Errorf("no response received")
	}

	// Check the flight mode in the response
	actualMode := ctx.setFlightModeResp.CurrentMode.Name()
	if actualMode != expectedMode {
		return fmt.Errorf("expected flight mode %s but got %s", expectedMode, actualMode)
	}
	return nil
}

func (ctx *shipOperationsContext) theCommandShouldFailWithError(expectedError string) error {
	// Check that an error was returned (proper Go error handling)
	if ctx.setFlightModeErr == nil {
		return fmt.Errorf("expected error but command succeeded")
	}

	// Check error message matches expected text exactly
	actualError := ctx.setFlightModeErr.Error()
	if actualError != expectedError {
		return fmt.Errorf("expected error '%s' but got '%s'", expectedError, actualError)
	}

	return nil
}

// Register steps

func InitializeShipOperationsScenario(ctx *godog.ScenarioContext) {
	shipOpsCtx := &shipOperationsContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		shipOpsCtx.reset()
		// Reset global context at the start of each scenario
		globalAppContext.reset()
		return ctx, nil
	})

	// Shared player setup steps
	ctx.Step(`^a player exists with agent "([^"]*)" and token "([^"]*)"$`, shipOpsCtx.aPlayerExistsWithAgentAndToken)
	ctx.Step(`^the player has player_id (\d+)$`, shipOpsCtx.thePlayerHasPlayerID)

	// Shared ship setup steps
	ctx.Step(`^a ship "([^"]*)" for player (\d+) at "([^"]*)" with status "([^"]*)"$`, shipOpsCtx.aShipForPlayerAtWithStatus)
	ctx.Step(`^a ship "([^"]*)" for player (\d+) in transit to "([^"]*)"$`, shipOpsCtx.aShipForPlayerInTransitTo)

	// Dock ship command steps
	ctx.Step(`^I execute DockShipCommand for ship "([^"]*)" and player (\d+)$`, shipOpsCtx.iExecuteDockShipCommandForShipAndPlayer)
	ctx.Step(`^the dock command should succeed with status "([^"]*)"$`, shipOpsCtx.theDockCommandShouldSucceedWithStatus)
	ctx.Step(`^the ship should still be docked$`, shipOpsCtx.theShipShouldStillBeDocked)
	ctx.Step(`^the ship should be docked at "([^"]*)"$`, shipOpsCtx.theShipShouldBeDockedAt)
	ctx.Step(`^the dock command should fail with error "([^"]*)"$`, shipOpsCtx.theDockCommandShouldFailWithError)

	// Orbit ship command steps
	ctx.Step(`^I execute OrbitShipCommand for ship "([^"]*)" and player (\d+)$`, shipOpsCtx.iExecuteOrbitShipCommandForShipAndPlayer)
	ctx.Step(`^the orbit command should succeed with status "([^"]*)"$`, shipOpsCtx.theOrbitCommandShouldSucceedWithStatus)
	ctx.Step(`^the ship should still be in orbit$`, shipOpsCtx.theShipShouldStillBeInOrbit)
	ctx.Step(`^the ship should be in orbit at "([^"]*)"$`, shipOpsCtx.theShipShouldBeInOrbitAt)
	ctx.Step(`^the orbit command should fail with error "([^"]*)"$`, shipOpsCtx.theOrbitCommandShouldFailWithError)

	// SetFlightMode command steps
	ctx.Step(`^I execute SetFlightModeCommand for ship "([^"]*)" and player (\d+) with mode "([^"]*)"$`, shipOpsCtx.iExecuteSetFlightModeCommandForShipAndPlayerWithMode)
	ctx.Step(`^the command should succeed with status "([^"]*)"$`, shipOpsCtx.theCommandShouldSucceedWithStatus)
	ctx.Step(`^the current flight mode should be "([^"]*)"$`, shipOpsCtx.theCurrentFlightModeShouldBe)
	ctx.Step(`^the command should fail with error "([^"]*)"$`, shipOpsCtx.theCommandShouldFailWithError)
}
