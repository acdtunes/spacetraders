package steps

import (
	"context"
	"fmt"
	"strings"

	"github.com/cucumber/godog"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	appShip "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/test/helpers"
)

type purchaseCargoContext struct {
	ships       map[string]*navigation.Ship
	playerID    int
	agentSymbol string
	token       string
	response    *appShip.PurchaseCargoResponse
	err         error

	// Real repositories with in-memory database
	db         *gorm.DB
	shipRepo   navigation.ShipRepository
	playerRepo *persistence.GormPlayerRepository
	marketRepo *persistence.MarketRepositoryGORM

	// Mock API client (don't hit real SpaceTraders API)
	apiClient  *helpers.MockAPIClient

	handler    *appShip.PurchaseCargoHandler
}

func (ctx *purchaseCargoContext) reset() {
	ctx.ships = make(map[string]*navigation.Ship)
	ctx.playerID = 0
	ctx.agentSymbol = ""
	ctx.token = ""
	ctx.response = nil
	ctx.err = nil

	// Use shared test database and truncate all tables for test isolation
	if err := helpers.TruncateAllTables(); err != nil {
		panic(fmt.Errorf("failed to truncate tables: %w", err))
	}

	// Run migrations for all needed models

	ctx.db = helpers.SharedTestDB

	// Create mock API client (don't hit real API)
	ctx.apiClient = helpers.NewMockAPIClient()

	// Configure mock API client to return successful purchases
	ctx.apiClient.SetPurchaseCargoFunc(func(ctx context.Context, shipSymbol, goodSymbol string, units int, token string) (*helpers.PurchaseCargoResult, error) {
		// Simple mock: $100 per unit
		return &helpers.PurchaseCargoResult{
			TotalCost:  units * 100,
			UnitsAdded: units,
		}, nil
	})

	// Create real repositories
	ctx.playerRepo = persistence.NewGormPlayerRepository(helpers.SharedTestDB)
	waypointRepo := persistence.NewGormWaypointRepository(helpers.SharedTestDB)
	ctx.shipRepo = api.NewAPIShipRepository(ctx.apiClient, ctx.playerRepo, waypointRepo)
	ctx.marketRepo = persistence.NewMarketRepository(helpers.SharedTestDB)

	ctx.handler = appShip.NewPurchaseCargoHandler(ctx.shipRepo, ctx.playerRepo, ctx.apiClient, ctx.marketRepo)
}

// Player setup steps

func (ctx *purchaseCargoContext) aPlayerExistsWithAgentAndToken(agentSymbol, token string) error {
	ctx.agentSymbol = agentSymbol
	ctx.token = token

	// Update shared context
	sharedPlayerExistsWithAgentAndToken(agentSymbol, token)

	return nil
}

func (ctx *purchaseCargoContext) thePlayerHasPlayerID(playerID int) error {
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

func (ctx *purchaseCargoContext) aShipForPlayerDockedAtMarketplace(shipSymbol string, playerID int, location string) error {
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
		100, cargo, 30, "FRAME_EXPLORER", navigation.NavStatusDocked,
	)
	if err != nil {
		return err
	}

	ctx.ships[shipSymbol] = ship

	// Configure API client to return this ship
	ctx.apiClient.AddShip(ship)

	return nil
}

func (ctx *purchaseCargoContext) aShipForPlayerInOrbitAt(shipSymbol string, playerID int, location string) error {
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
		100, cargo, 30, "FRAME_EXPLORER", navigation.NavStatusInOrbit,
	)
	if err != nil {
		return err
	}

	ctx.ships[shipSymbol] = ship

	// Configure API client to return this ship
	ctx.apiClient.AddShip(ship)

	return nil
}

func (ctx *purchaseCargoContext) theShipHasCargoSpaceAvailable(cargoSpace int) error {
	ship := ctx.ships["SHIP-1"]
	if ship == nil {
		return fmt.Errorf("ship not found in context")
	}

	// Modify ship to have specific cargo space available
	// We need to create a ship with cargo that leaves the desired space available
	cargoCapacity := ship.CargoCapacity()
	usedCargo := cargoCapacity - cargoSpace

	waypoint := ship.CurrentLocation()
	fuel := ship.Fuel()
	navStatus := ship.NavStatus()

	// Create cargo with used space
	var inventory []*shared.CargoItem
	if usedCargo > 0 {
		item, _ := shared.NewCargoItem("EXISTING_ITEM", "Existing Item", "Already in cargo", usedCargo)
		inventory = []*shared.CargoItem{item}
	}
	cargo, _ := shared.NewCargo(cargoCapacity, usedCargo, inventory)

	newShip, err := navigation.NewShip(
		ship.ShipSymbol(), ship.PlayerID(), waypoint, fuel, ship.FuelCapacity(),
		cargoCapacity, cargo, ship.EngineSpeed(), "FRAME_EXPLORER", navStatus,
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

func (ctx *purchaseCargoContext) iPurchaseUnitsOfForShip(units int, goodSymbol, shipSymbol string) error {
	return ctx.tryToPurchase(shipSymbol, goodSymbol, units)
}

func (ctx *purchaseCargoContext) iTryToPurchaseUnitsOfForShip(units int, goodSymbol, shipSymbol string) error {
	return ctx.tryToPurchase(shipSymbol, goodSymbol, units)
}

func (ctx *purchaseCargoContext) tryToPurchase(shipSymbol, goodSymbol string, units int) error {
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
	cmd := &appShip.PurchaseCargoCommand{
		ShipSymbol: shipSymbol,
		GoodSymbol: goodSymbol,
		Units:      units,
		PlayerID:   playerID,
	}

	// Execute handler
	response, err := ctx.handler.Handle(context.Background(), cmd)

	// Store both response and error
	ctx.err = err
	if err == nil {
		ctx.response = response.(*appShip.PurchaseCargoResponse)
	} else {
		ctx.response = nil
	}

	return nil
}

// Then steps

func (ctx *purchaseCargoContext) thePurchaseShouldSucceed() error {
	if ctx.err != nil {
		return fmt.Errorf("expected success but got error: %v", ctx.err)
	}
	if ctx.response == nil {
		return fmt.Errorf("expected response but got nil")
	}
	return nil
}

func (ctx *purchaseCargoContext) unitsShouldBeAddedToCargo(expectedUnits int) error {
	if ctx.response == nil {
		return fmt.Errorf("no response received")
	}
	if ctx.response.UnitsAdded != expectedUnits {
		return fmt.Errorf("expected %d units added but got %d", expectedUnits, ctx.response.UnitsAdded)
	}
	return nil
}

func (ctx *purchaseCargoContext) theTotalCostShouldBeGreaterThan(minCost int) error {
	if ctx.response == nil {
		return fmt.Errorf("no response received")
	}
	if ctx.response.TotalCost <= minCost {
		return fmt.Errorf("expected total cost > %d but got %d", minCost, ctx.response.TotalCost)
	}
	return nil
}

func (ctx *purchaseCargoContext) thePurchaseShouldFailWithError(expectedError string) error {
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

func InitializePurchaseCargoScenario(ctx *godog.ScenarioContext) {
	purchaseCtx := &purchaseCargoContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		purchaseCtx.reset()
		return ctx, nil
	})

	ctx.Step(`^a player exists with agent "([^"]*)" and token "([^"]*)"$`, purchaseCtx.aPlayerExistsWithAgentAndToken)
	ctx.Step(`^the player has player_id (\d+)$`, purchaseCtx.thePlayerHasPlayerID)
	ctx.Step(`^a ship "([^"]*)" for player (\d+) docked at marketplace "([^"]*)"$`, purchaseCtx.aShipForPlayerDockedAtMarketplace)
	ctx.Step(`^a ship "([^"]*)" for player (\d+) in orbit at "([^"]*)"$`, purchaseCtx.aShipForPlayerInOrbitAt)
	ctx.Step(`^the ship has (\d+) cargo space available$`, purchaseCtx.theShipHasCargoSpaceAvailable)
	ctx.Step(`^I execute purchase cargo command for (\d+) units of "([^"]*)" on ship "([^"]*)"$`, purchaseCtx.iPurchaseUnitsOfForShip)
	ctx.Step(`^I attempt to execute purchase cargo command for (\d+) units of "([^"]*)" on ship "([^"]*)"$`, purchaseCtx.iTryToPurchaseUnitsOfForShip)
	ctx.Step(`^the purchase cargo command should succeed$`, purchaseCtx.thePurchaseShouldSucceed)
	ctx.Step(`^(\d+) units should be added to cargo$`, purchaseCtx.unitsShouldBeAddedToCargo)
	ctx.Step(`^the total cost should be greater than (\d+)$`, purchaseCtx.theTotalCostShouldBeGreaterThan)
	ctx.Step(`^the purchase cargo command should fail with error "([^"]*)"$`, purchaseCtx.thePurchaseShouldFailWithError)
}
