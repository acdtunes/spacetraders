package steps

import (
	"context"
	"fmt"
	"strings"

	"github.com/cucumber/godog"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	appShip "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/test/helpers"
)

type sellCargoContext struct {
	ships       map[string]*navigation.Ship
	playerID    int
	agentSymbol string
	token       string
	response    *appShip.SellCargoResponse
	err         error

	// Real repositories with in-memory database
	db         *gorm.DB
	shipRepo   navigation.ShipRepository
	playerRepo *persistence.GormPlayerRepository
	marketRepo *persistence.MarketRepositoryGORM

	// Mock API client (don't hit real SpaceTraders API)
	apiClient  *helpers.MockAPIClient

	handler    *appShip.SellCargoHandler
}

func (ctx *sellCargoContext) reset() {
	ctx.ships = make(map[string]*navigation.Ship)
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

	// Run migrations for all needed models
	err = db.AutoMigrate(
		&persistence.PlayerModel{},
		&persistence.WaypointModel{},
		&persistence.MarketData{},
		&persistence.TradeGoodData{},
	)
	if err != nil {
		panic(fmt.Errorf("failed to migrate database: %w", err))
	}

	ctx.db = db

	// Create mock API client (don't hit real API)
	ctx.apiClient = helpers.NewMockAPIClient()

	// Configure mock API client to successfully sell cargo
	// Default: 100 credits per unit (configurable in tests if needed)
	ctx.apiClient.SetSellCargoFunc(func(ctx context.Context, shipSymbol, goodSymbol string, units int, token string) (*helpers.SellCargoResult, error) {
		return &helpers.SellCargoResult{
			TotalRevenue: units * 100, // 100 credits per unit
			UnitsSold:    units,
		}, nil
	})

	// Create real repositories
	ctx.playerRepo = persistence.NewGormPlayerRepository(db)
	waypointRepo := persistence.NewGormWaypointRepository(db)
	ctx.shipRepo = api.NewAPIShipRepository(ctx.apiClient, ctx.playerRepo, waypointRepo)
	ctx.marketRepo = persistence.NewMarketRepository(db)

	ctx.handler = appShip.NewSellCargoHandler(ctx.shipRepo, ctx.playerRepo, ctx.apiClient, ctx.marketRepo)
}

// Player setup steps (reused from other tests)

func (ctx *sellCargoContext) aPlayerExistsWithAgentAndToken(agentSymbol, token string) error {
	ctx.agentSymbol = agentSymbol
	ctx.token = token

	// Update shared context
	sharedPlayerExistsWithAgentAndToken(agentSymbol, token)

	return nil
}

func (ctx *sellCargoContext) thePlayerHasPlayerID(playerID int) error {
	ctx.playerID = playerID

	// Update shared context
	sharedPlayerHasPlayerID(playerID)

	// Add player to repository
	p := player.NewPlayer(playerID, ctx.agentSymbol, ctx.token)
	if err := ctx.playerRepo.Save(context.Background(), p); err != nil {
		return fmt.Errorf("failed to save player: %w", err)
	}

	return nil
}

// Given steps

func (ctx *sellCargoContext) aShipForPlayerDockedAtMarketplaceWithCargo(shipSymbol string, playerID int, location string) error {
	// Create waypoint in database (required for APIShipRepository.shipDataToDomain)
	// Extract system from location using domain logic
	systemSymbol := shared.ExtractSystemSymbol(location)

	waypointModel := &persistence.WaypointModel{
		WaypointSymbol: location,
		Type:           "MARKETPLACE",
		SystemSymbol:   systemSymbol,
		X:              0,
		Y:              0,
	}
	if err := ctx.db.Create(waypointModel).Error; err != nil {
		return fmt.Errorf("failed to create waypoint: %w", err)
	}

	waypoint, _ := shared.NewWaypoint(location, 0, 0)
	fuel, _ := shared.NewFuel(100, 100)
	cargo, _ := shared.NewCargo(100, 0, []*shared.CargoItem{})

	ship, err := navigation.NewShip(
		shipSymbol, playerID, waypoint, fuel, 100,
		100, cargo, 30, navigation.NavStatusDocked,
	)
	if err != nil {
		return err
	}

	ctx.ships[shipSymbol] = ship

	// Configure API client to return this ship
	ctx.apiClient.AddShip(ship)

	return nil
}

func (ctx *sellCargoContext) aShipForPlayerInOrbitAtWithCargo(shipSymbol string, playerID int, location string) error {
	// Create waypoint in database (required for APIShipRepository.shipDataToDomain)
	// Extract system from location using domain logic
	systemSymbol := shared.ExtractSystemSymbol(location)

	waypointModel := &persistence.WaypointModel{
		WaypointSymbol: location,
		Type:           "PLANET",
		SystemSymbol:   systemSymbol,
		X:              0,
		Y:              0,
	}
	if err := ctx.db.Create(waypointModel).Error; err != nil {
		return fmt.Errorf("failed to create waypoint: %w", err)
	}

	waypoint, _ := shared.NewWaypoint(location, 0, 0)
	fuel, _ := shared.NewFuel(100, 100)
	cargo, _ := shared.NewCargo(100, 0, []*shared.CargoItem{})

	ship, err := navigation.NewShip(
		shipSymbol, playerID, waypoint, fuel, 100,
		100, cargo, 30, navigation.NavStatusInOrbit,
	)
	if err != nil {
		return err
	}

	ctx.ships[shipSymbol] = ship

	// Configure API client to return this ship
	ctx.apiClient.AddShip(ship)

	return nil
}

func (ctx *sellCargoContext) theShipHasUnitsOfInCargo(units int, goodSymbol string) error {
	ship := ctx.ships["SHIP-1"]
	if ship == nil {
		return fmt.Errorf("ship not found in context")
	}

	// Create cargo with the specified item
	item, err := shared.NewCargoItem(goodSymbol, goodSymbol+" Name", "Description", units)
	if err != nil {
		return err
	}

	cargo, err := shared.NewCargo(ship.CargoCapacity(), units, []*shared.CargoItem{item})
	if err != nil {
		return err
	}

	// Recreate ship with new cargo
	waypoint := ship.CurrentLocation()
	fuel := ship.Fuel()
	navStatus := ship.NavStatus()

	newShip, err := navigation.NewShip(
		ship.ShipSymbol(), ship.PlayerID(), waypoint, fuel, ship.FuelCapacity(),
		ship.CargoCapacity(), cargo, ship.EngineSpeed(), navStatus,
	)
	if err != nil {
		return err
	}

	ctx.ships["SHIP-1"] = newShip

	// Update ship in API client
	ctx.apiClient.AddShip(newShip)

	return nil
}

// When steps

func (ctx *sellCargoContext) iSellUnitsOfFromShip(units int, goodSymbol, shipSymbol string) error {
	return ctx.executeSellCommand(shipSymbol, goodSymbol, units)
}

func (ctx *sellCargoContext) iTryToSellUnitsOfFromShip(units int, goodSymbol, shipSymbol string) error {
	return ctx.executeSellCommand(shipSymbol, goodSymbol, units)
}

func (ctx *sellCargoContext) executeSellCommand(shipSymbol, goodSymbol string, units int) error {
	// Get player ID from shared context (in case it was set by another scenario's steps)
	agentSymbol, token, playerID := globalAppContext.getPlayerInfo()
	if playerID == 0 {
		playerID = ctx.playerID // Fallback to local context
		agentSymbol = ctx.agentSymbol
		token = ctx.token
	}

	// Ensure player is in repository
	if playerID > 0 {
		p := player.NewPlayer(playerID, agentSymbol, token)
		_ = ctx.playerRepo.Save(context.Background(), p) // Ignore error - player may already exist
	}

	// Create command
	cmd := &appShip.SellCargoCommand{
		ShipSymbol: shipSymbol,
		PlayerID:   playerID,
		GoodSymbol: goodSymbol,
		Units:      units,
	}

	// Execute handler
	response, err := ctx.handler.Handle(context.Background(), cmd)

	// Store both response and error
	ctx.err = err
	if err == nil {
		ctx.response = response.(*appShip.SellCargoResponse)
	} else {
		ctx.response = nil
	}

	return nil
}

// Then steps

func (ctx *sellCargoContext) theSaleShouldSucceed() error {
	if ctx.err != nil {
		return fmt.Errorf("expected success but got error: %v", ctx.err)
	}
	if ctx.response == nil {
		return fmt.Errorf("expected response but got nil")
	}
	return nil
}

func (ctx *sellCargoContext) unitsShouldBeSoldFromCargo(expectedUnits int) error {
	if ctx.response == nil {
		return fmt.Errorf("no response received")
	}
	if ctx.response.UnitsSold != expectedUnits {
		return fmt.Errorf("expected %d units sold but got %d", expectedUnits, ctx.response.UnitsSold)
	}
	return nil
}

func (ctx *sellCargoContext) theTotalRevenueShouldBeGreaterThan(minRevenue int) error {
	if ctx.response == nil {
		return fmt.Errorf("no response received")
	}
	if ctx.response.TotalRevenue <= minRevenue {
		return fmt.Errorf("expected total revenue > %d but got %d", minRevenue, ctx.response.TotalRevenue)
	}
	return nil
}

func (ctx *sellCargoContext) theSaleShouldFailWithError(expectedError string) error {
	if ctx.err == nil {
		return fmt.Errorf("expected error '%s' but command succeeded", expectedError)
	}

	// Check if error message contains expected text (use contains for flexible matching)
	errMsg := ctx.err.Error()

	// Simple substring match for flexibility
	if !strings.Contains(errMsg, expectedError) {
		return fmt.Errorf("expected error containing '%s' but got '%v'", expectedError, ctx.err)
	}

	return nil
}

// Register steps

func InitializeSellCargoScenario(ctx *godog.ScenarioContext) {
	sellCtx := &sellCargoContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		sellCtx.reset()
		return ctx, nil
	})

	ctx.Step(`^a player exists with agent "([^"]*)" and token "([^"]*)"$`, sellCtx.aPlayerExistsWithAgentAndToken)
	ctx.Step(`^the player has player_id (\d+)$`, sellCtx.thePlayerHasPlayerID)
	ctx.Step(`^a ship "([^"]*)" for player (\d+) docked at marketplace "([^"]*)" with cargo$`, sellCtx.aShipForPlayerDockedAtMarketplaceWithCargo)
	ctx.Step(`^a ship "([^"]*)" for player (\d+) in orbit at "([^"]*)" with cargo$`, sellCtx.aShipForPlayerInOrbitAtWithCargo)
	ctx.Step(`^the ship contains (\d+) units of "([^"]*)"$`, sellCtx.theShipHasUnitsOfInCargo)
	ctx.Step(`^I execute sell cargo command for (\d+) units of "([^"]*)" from ship "([^"]*)"$`, sellCtx.iSellUnitsOfFromShip)
	ctx.Step(`^I attempt to execute sell cargo command for (\d+) units of "([^"]*)" from ship "([^"]*)"$`, sellCtx.iTryToSellUnitsOfFromShip)
	ctx.Step(`^the sell cargo command should succeed$`, sellCtx.theSaleShouldSucceed)
	ctx.Step(`^(\d+) units should be sold from cargo$`, sellCtx.unitsShouldBeSoldFromCargo)
	ctx.Step(`^the total revenue should be greater than (\d+)$`, sellCtx.theTotalRevenueShouldBeGreaterThan)
	ctx.Step(`^the sell cargo command should fail with error "([^"]*)"$`, sellCtx.theSaleShouldFailWithError)
}
