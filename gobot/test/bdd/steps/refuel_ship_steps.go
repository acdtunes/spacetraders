package steps

import (
	"context"
	"fmt"
	"strings"

	"github.com/cucumber/godog"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	appShip "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/test/helpers"
)

type refuelShipContext struct {
	ships       map[string]*navigation.Ship
	playerID    int
	agentSymbol string
	token       string
	response    *appShip.RefuelShipResponse
	err         error
	waypoints   map[string]*shared.Waypoint
	handler     *appShip.RefuelShipHandler
	apiClient   *helpers.MockAPIClient
	shipRepo    navigation.ShipRepository
	playerRepo  *persistence.GormPlayerRepository
	waypointRepo *persistence.GormWaypointRepository
	db          *gorm.DB // In-memory SQLite database
}

func (ctx *refuelShipContext) reset() {
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

	// Create repositories
	ctx.apiClient = helpers.NewMockAPIClient()
	ctx.playerRepo = persistence.NewGormPlayerRepository(db)
	ctx.waypointRepo = persistence.NewGormWaypointRepository(db)
	ctx.shipRepo = api.NewAPIShipRepository(ctx.apiClient, ctx.playerRepo, ctx.waypointRepo)
	ctx.handler = appShip.NewRefuelShipHandler(ctx.shipRepo)
}

// Player setup steps (reuse from dock_ship)

func (ctx *refuelShipContext) aPlayerExistsWithAgentAndToken(agentSymbol, token string) error {
	ctx.agentSymbol = agentSymbol
	ctx.token = token
	return nil
}

func (ctx *refuelShipContext) thePlayerHasPlayerID(playerID int) error {
	ctx.playerID = playerID
	return nil
}

// ensurePlayerExists ensures a player with the given ID exists in the repository
func (ctx *refuelShipContext) ensurePlayerExists(playerID int) error {
	// Check if player already exists
	_, err := ctx.playerRepo.FindByID(context.Background(), playerID)
	if err == nil {
		return nil // Player already exists
	}

	// Create and save player
	agentSymbol := fmt.Sprintf("AGENT-%d", playerID)
	token := fmt.Sprintf("token-%d", playerID)
	if ctx.agentSymbol != "" {
		agentSymbol = ctx.agentSymbol
	}
	if ctx.token != "" {
		token = ctx.token
	}

	p := player.NewPlayer(playerID, agentSymbol, token)
	return ctx.playerRepo.Save(context.Background(), p)
}

// ensureWaypointExists ensures a waypoint exists in the repository
func (ctx *refuelShipContext) ensureWaypointExists(waypoint *shared.Waypoint) error {
	// Extract system symbol from waypoint symbol
	systemSymbol := shared.ExtractSystemSymbol(waypoint.Symbol)
	waypoint.SystemSymbol = systemSymbol

	// Check if waypoint already exists
	_, err := ctx.waypointRepo.FindBySymbol(context.Background(), waypoint.Symbol, systemSymbol)
	if err == nil {
		return nil // Waypoint already exists
	}

	// Save waypoint to repository
	return ctx.waypointRepo.Save(context.Background(), waypoint)
}

// Given steps

func (ctx *refuelShipContext) aShipForPlayerAtFuelStationWithStatusAndFuel(
	shipSymbol string,
	playerID int,
	location string,
	status string,
	currentFuel, fuelCapacity int,
) error {
	// Ensure player exists in repository
	if err := ctx.ensurePlayerExists(playerID); err != nil {
		return err
	}

	// Create waypoint with fuel station
	waypoint, err := shared.NewWaypoint(location, 0, 0)
	if err != nil {
		return err
	}
	waypoint.HasFuel = true // Mark as fuel station
	ctx.waypoints[location] = waypoint

	// Ensure waypoint exists in repository
	if err := ctx.ensureWaypointExists(waypoint); err != nil {
		return err
	}

	fuel, err := shared.NewFuel(currentFuel, fuelCapacity)
	if err != nil {
		return err
	}

	cargo, err := shared.NewCargo(40, 0, []*shared.CargoItem{})
	if err != nil {
		return err
	}

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
		shipSymbol, playerID, waypoint, fuel, fuelCapacity,
		40, cargo, 30, navStatus,
	)
	if err != nil {
		return err
	}

	ctx.ships[shipSymbol] = ship
	ctx.apiClient.AddShip(ship)

	// Ensure player exists in database (from shared context)
	agentSymbol, token, _ := globalAppContext.getPlayerInfo()
	if agentSymbol != "" {
		p := player.NewPlayer(playerID, agentSymbol, token)
		_ = ctx.playerRepo.Save(context.Background(), p)
	}

	return nil
}

func (ctx *refuelShipContext) aShipForPlayerAtWaypointWithoutFuelWithStatusAndFuel(
	shipSymbol string,
	playerID int,
	location string,
	status string,
	currentFuel, fuelCapacity int,
) error {
	// Ensure player exists in repository
	if err := ctx.ensurePlayerExists(playerID); err != nil {
		return err
	}

	// Create waypoint WITHOUT fuel station
	waypoint, err := shared.NewWaypoint(location, 0, 0)
	if err != nil {
		return err
	}
	waypoint.HasFuel = false // No fuel station
	ctx.waypoints[location] = waypoint

	// Ensure waypoint exists in repository
	if err := ctx.ensureWaypointExists(waypoint); err != nil {
		return err
	}

	fuel, err := shared.NewFuel(currentFuel, fuelCapacity)
	if err != nil {
		return err
	}

	cargo, err := shared.NewCargo(40, 0, []*shared.CargoItem{})
	if err != nil {
		return err
	}

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
		shipSymbol, playerID, waypoint, fuel, fuelCapacity,
		40, cargo, 30, navStatus,
	)
	if err != nil {
		return err
	}

	ctx.ships[shipSymbol] = ship
	ctx.apiClient.AddShip(ship)

	// Ensure player exists in database (from shared context)
	agentSymbol, token, _ := globalAppContext.getPlayerInfo()
	if agentSymbol != "" {
		p := player.NewPlayer(playerID, agentSymbol, token)
		_ = ctx.playerRepo.Save(context.Background(), p)
	}

	return nil
}

// When steps

func (ctx *refuelShipContext) iExecuteRefuelShipCommandForShipAndPlayerWithNilUnits(
	shipSymbol string,
	playerID int,
) error {
	// Call with nil units (full refuel)
	return ctx.executeRefuelCommand(shipSymbol, playerID, nil)
}

func (ctx *refuelShipContext) iExecuteRefuelShipCommandForShipAndPlayerWithUnits(
	shipSymbol string,
	playerID int,
	units int,
) error {
	// Call with specific units
	return ctx.executeRefuelCommand(shipSymbol, playerID, &units)
}

func (ctx *refuelShipContext) executeRefuelCommand(shipSymbol string, playerID int, units *int) error {
	// Create command
	cmd := &appShip.RefuelShipCommand{
		ShipSymbol: shipSymbol,
		PlayerID:   playerID,
		Units:      units,
	}

	// Call the actual handler
	response, err := ctx.handler.Handle(context.Background(), cmd)

	// Store both response and error
	ctx.err = err
	if err == nil {
		ctx.response = response.(*appShip.RefuelShipResponse)

		// Fetch updated ship from repository to verify state
		updatedShip, fetchErr := ctx.shipRepo.FindBySymbol(context.Background(), shipSymbol, playerID)
		if fetchErr == nil && updatedShip != nil {
			ctx.ships[shipSymbol] = updatedShip
		}
	} else {
		ctx.response = nil
	}

	return nil
}

// Then steps

func (ctx *refuelShipContext) theRefuelCommandShouldSucceed() error {
	if ctx.err != nil {
		return fmt.Errorf("expected success but got error: %v", ctx.err)
	}
	if ctx.response == nil {
		return fmt.Errorf("expected response but got nil")
	}
	return nil
}

func (ctx *refuelShipContext) theShipShouldHaveFuel(currentFuel, fuelCapacity int) error {
	if ctx.response == nil {
		return fmt.Errorf("no response received")
	}
	if ctx.response.CurrentFuel != currentFuel {
		return fmt.Errorf("expected current fuel %d but got %d", currentFuel, ctx.response.CurrentFuel)
	}

	// Also verify ship entity state
	ship := ctx.ships["SHIP-1"]
	if ship == nil {
		return fmt.Errorf("ship not found in context")
	}
	if ship.Fuel().Current != currentFuel {
		return fmt.Errorf("ship entity has fuel %d but expected %d", ship.Fuel().Current, currentFuel)
	}
	if ship.Fuel().Capacity != fuelCapacity {
		return fmt.Errorf("ship entity has capacity %d but expected %d", ship.Fuel().Capacity, fuelCapacity)
	}
	return nil
}

func (ctx *refuelShipContext) unitsOfFuelShouldHaveBeenAdded(expectedAdded int) error {
	if ctx.response == nil {
		return fmt.Errorf("no response received")
	}
	if ctx.response.FuelAdded != expectedAdded {
		return fmt.Errorf("expected %d units added but got %d", expectedAdded, ctx.response.FuelAdded)
	}
	return nil
}

func (ctx *refuelShipContext) theShipShouldBeDocked() error {
	ship := ctx.ships["SHIP-1"]
	if ship == nil {
		return fmt.Errorf("ship not found in context")
	}
	if !ship.IsDocked() {
		return fmt.Errorf("expected ship to be docked but status is %s", ship.NavStatus())
	}
	return nil
}

func (ctx *refuelShipContext) theRefuelCommandShouldFailWithError(expectedError string) error {
	if ctx.err == nil {
		return fmt.Errorf("expected error but command succeeded")
	}
	// Check if the error message contains the expected substring
	if !strings.Contains(ctx.err.Error(), expectedError) {
		return fmt.Errorf("expected error containing '%s' but got '%s'", expectedError, ctx.err.Error())
	}
	return nil
}

// Register steps

func InitializeRefuelShipScenario(ctx *godog.ScenarioContext) {
	refuelCtx := &refuelShipContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		refuelCtx.reset()

		// Get player from shared context (set by Background steps)
		agentSymbol, token, playerID := globalAppContext.getPlayerInfo()
		if playerID > 0 {
			// Save player to database for this scenario
			p := player.NewPlayer(playerID, agentSymbol, token)
			_ = refuelCtx.playerRepo.Save(context.Background(), p)
		}

		return ctx, nil
	})

	// Note: Player setup steps are shared with other scenarios and already registered
	// We only register refuel-specific steps here
	ctx.Step(`^a ship "([^"]*)" for player (\d+) at fuel station "([^"]*)" with status "([^"]*)" and fuel (\d+)/(\d+)$`, refuelCtx.aShipForPlayerAtFuelStationWithStatusAndFuel)
	ctx.Step(`^a ship "([^"]*)" for player (\d+) at waypoint "([^"]*)" without fuel with status "([^"]*)" and fuel (\d+)/(\d+)$`, refuelCtx.aShipForPlayerAtWaypointWithoutFuelWithStatusAndFuel)
	ctx.Step(`^I execute RefuelShipCommand for ship "([^"]*)" and player (\d+) with nil units$`, refuelCtx.iExecuteRefuelShipCommandForShipAndPlayerWithNilUnits)
	ctx.Step(`^I execute RefuelShipCommand for ship "([^"]*)" and player (\d+) with (\d+) units$`, refuelCtx.iExecuteRefuelShipCommandForShipAndPlayerWithUnits)
	ctx.Step(`^the refuel command should succeed$`, refuelCtx.theRefuelCommandShouldSucceed)
	ctx.Step(`^the ship should have fuel (\d+)/(\d+)$`, refuelCtx.theShipShouldHaveFuel)
	ctx.Step(`^(\d+) units of fuel should have been added$`, refuelCtx.unitsOfFuelShouldHaveBeenAdded)
	ctx.Step(`^the refuel command should fail with error "([^"]*)"$`, refuelCtx.theRefuelCommandShouldFailWithError)
}
