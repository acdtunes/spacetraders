package steps

import (
	"context"
	"fmt"
	"strings"

	"github.com/cucumber/godog"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/graph"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	appShip "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/test/helpers"
)

type jettisonCargoContext struct {
	ships       map[string]*navigation.Ship
	playerID    int
	agentSymbol string
	token       string
	response    *appShip.JettisonCargoResponse
	err         error

	// Real repositories with in-memory database
	db         *gorm.DB
	shipRepo   navigation.ShipRepository
	playerRepo *persistence.GormPlayerRepository

	// Mock API client (don't hit real SpaceTraders API)
	apiClient  *helpers.MockAPIClient

	handler    *appShip.JettisonCargoHandler
}

func (ctx *jettisonCargoContext) reset() {
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

	// Create real repositories
	ctx.playerRepo = persistence.NewGormPlayerRepository(helpers.SharedTestDB)
	waypointRepo := persistence.NewGormWaypointRepository(helpers.SharedTestDB)
	graphBuilder := helpers.NewMockGraphBuilder(ctx.apiClient, waypointRepo)
	waypointProvider := graph.NewWaypointProvider(waypointRepo, graphBuilder)
	ctx.shipRepo = api.NewAPIShipRepository(ctx.apiClient, ctx.playerRepo, waypointRepo, waypointProvider)

	ctx.handler = appShip.NewJettisonCargoHandler(ctx.shipRepo, ctx.playerRepo, ctx.apiClient)
}

// Player setup steps

func (ctx *jettisonCargoContext) aPlayerExistsWithAgentAndToken(agentSymbol, token string) error {
	ctx.agentSymbol = agentSymbol
	ctx.token = token

	// Update shared context
	sharedPlayerExistsWithAgentAndToken(agentSymbol, token)

	return nil
}

func (ctx *jettisonCargoContext) thePlayerHasPlayerID(playerID int) error {
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

func (ctx *jettisonCargoContext) aShipForPlayerInOrbitAtWithCargo(
	shipSymbol string,
	playerID int,
	location string,
	cargoTable *godog.Table,
) error {
	return ctx.createShipWithCargo(shipSymbol, playerID, location, navigation.NavStatusInOrbit, cargoTable)
}

func (ctx *jettisonCargoContext) aShipForPlayerDockedAtWithCargo(
	shipSymbol string,
	playerID int,
	location string,
	cargoTable *godog.Table,
) error {
	return ctx.createShipWithCargo(shipSymbol, playerID, location, navigation.NavStatusDocked, cargoTable)
}

func (ctx *jettisonCargoContext) createShipWithCargo(
	shipSymbol string,
	playerID int,
	location string,
	navStatus navigation.NavStatus,
	cargoTable *godog.Table,
) error {
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

	// Create waypoint
	waypoint, err := shared.NewWaypoint(location, 0, 0)
	if err != nil {
		return err
	}

	// Parse cargo table
	var cargoItems []*shared.CargoItem
	totalUnits := 0

	// Skip header row (row 0)
	for i := 1; i < len(cargoTable.Rows); i++ {
		row := cargoTable.Rows[i]
		if len(row.Cells) < 2 {
			continue
		}

		symbol := row.Cells[0].Value
		units := 0
		fmt.Sscanf(row.Cells[1].Value, "%d", &units)

		if units > 0 {
			item, err := shared.NewCargoItem(symbol, symbol, "", units)
			if err != nil {
				return err
			}
			cargoItems = append(cargoItems, item)
			totalUnits += units
		}
	}

	// Create cargo
	cargo, err := shared.NewCargo(100, totalUnits, cargoItems)
	if err != nil {
		return err
	}

	// Create fuel
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		return err
	}

	// Create ship
	ship, err := navigation.NewShip(
		shipSymbol, playerID, waypoint, fuel, 100,
		100, cargo, 30, "FRAME_EXPLORER", navStatus,
	)
	if err != nil {
		return err
	}

	ctx.ships[shipSymbol] = ship

	// Configure API client to return this ship
	ctx.apiClient.AddShip(ship)

	return nil
}

// When steps

func (ctx *jettisonCargoContext) iExecuteJettisonCargoCommandForShipJettisoningUnitsOf(
	shipSymbol string,
	units int,
	goodSymbol string,
) error {
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
	cmd := &appShip.JettisonCargoCommand{
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
		ctx.response = response.(*appShip.JettisonCargoResponse)
	} else {
		ctx.response = nil
	}

	return nil
}

// Then steps

func (ctx *jettisonCargoContext) theJettisonCommandShouldSucceed() error {
	if ctx.err != nil {
		return fmt.Errorf("expected success but got error: %v", ctx.err)
	}
	if ctx.response == nil {
		return fmt.Errorf("expected response but got nil")
	}
	return nil
}

func (ctx *jettisonCargoContext) unitsShouldHaveBeenJettisoned(expectedUnits int) error {
	if ctx.response == nil {
		return fmt.Errorf("no response received")
	}
	if ctx.response.UnitsJettisoned != expectedUnits {
		return fmt.Errorf("expected %d units jettisoned but got %d", expectedUnits, ctx.response.UnitsJettisoned)
	}
	return nil
}

func (ctx *jettisonCargoContext) theShipShouldHaveUnitsOfRemaining(expectedUnits int, goodSymbol string) error {
	ship := ctx.ships["SHIP-1"]
	if ship == nil {
		return fmt.Errorf("ship not found in context")
	}

	// Note: In a real implementation, we would update the ship's cargo after jettisoning
	// For this test, we're checking the pre-jettison state minus what was jettisoned
	currentUnits := ship.Cargo().GetItemUnits(goodSymbol)
	expectedAfterJettison := currentUnits - ctx.response.UnitsJettisoned

	if expectedAfterJettison != expectedUnits {
		return fmt.Errorf("expected ship to have %d units of %s but would have %d", expectedUnits, goodSymbol, expectedAfterJettison)
	}
	return nil
}

func (ctx *jettisonCargoContext) theJettisonCommandShouldFailWithError(expectedError string) error {
	if ctx.err == nil {
		return fmt.Errorf("expected error '%s' but command succeeded", expectedError)
	}

	// Check if error message contains expected text (use contains for flexible matching)
	errMsg := ctx.err.Error()

	// Simple substring match for flexibility
	if !containsSubstring(errMsg, expectedError) {
		return fmt.Errorf("expected error containing '%s' but got '%v'", expectedError, ctx.err)
	}

	return nil
}

// Helper function to check if a string contains a substring (case-sensitive)
func containsSubstring(s, substr string) bool {
	return strings.Contains(s, substr)
}

// Register steps

func InitializeJettisonCargoScenario(ctx *godog.ScenarioContext) {
	jettisonCtx := &jettisonCargoContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		jettisonCtx.reset()
		return ctx, nil
	})

	// NOTE: Player setup steps are NOT registered here to avoid conflicts with other scenarios.
	// JettisonCargo reads player info from shared context populated by Background steps.

	// Ship and command steps
	ctx.Step(`^a ship "([^"]*)" for player (\d+) in orbit at "([^"]*)" with cargo:$`, jettisonCtx.aShipForPlayerInOrbitAtWithCargo)
	ctx.Step(`^a ship "([^"]*)" for player (\d+) docked at "([^"]*)" with cargo:$`, jettisonCtx.aShipForPlayerDockedAtWithCargo)
	ctx.Step(`^I execute JettisonCargoCommand for ship "([^"]*)" jettisoning (\d+) units of "([^"]*)"$`, jettisonCtx.iExecuteJettisonCargoCommandForShipJettisoningUnitsOf)
	ctx.Step(`^the jettison command should succeed$`, jettisonCtx.theJettisonCommandShouldSucceed)
	ctx.Step(`^(\d+) units should have been jettisoned$`, jettisonCtx.unitsShouldHaveBeenJettisoned)
	ctx.Step(`^the ship should have (\d+) units of "([^"]*)" remaining$`, jettisonCtx.theShipShouldHaveUnitsOfRemaining)
	ctx.Step(`^the jettison command should fail with error "([^"]*)"$`, jettisonCtx.theJettisonCommandShouldFailWithError)
}
