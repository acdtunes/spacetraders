package steps

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipyardCommands "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/commands"
	shipyardQueries "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
	"github.com/andrescamacho/spacetraders-go/test/helpers"
)

// shipyardApplicationContext is a unified context for ALL shipyard application layer handlers
type shipyardApplicationContext struct {
	// Real repositories
	repos *helpers.TestRepositories

	// All handlers under test
	getListingsHandler   *shipyardQueries.GetShipyardListingsHandler
	purchaseShipHandler  *shipyardCommands.PurchaseShipHandler
	batchPurchaseHandler *shipyardCommands.BatchPurchaseShipsHandler

	// Mocks (only external services)
	apiClient *helpers.MockAPIClient
	mediator  *helpers.MockMediator
	clock     *shared.MockClock

	// State tracking for assertions
	lastError              error
	lastShipyard           *shipyard.Shipyard
	lastPurchaseResp       *shipyardCommands.PurchaseShipResponse
	lastBatchResp          *shipyardCommands.BatchPurchaseShipsResponse
	lastShip               *navigation.Ship
	autoDiscoveredShipyard string

	// Track created ships for batch purchase assertions
	createdShips []*navigation.Ship
}

func (ctx *shipyardApplicationContext) reset() {
	ctx.lastError = nil
	ctx.lastShipyard = nil
	ctx.lastPurchaseResp = nil
	ctx.lastBatchResp = nil
	ctx.lastShip = nil
	ctx.autoDiscoveredShipyard = ""
	ctx.createdShips = []*navigation.Ship{}

	// Truncate all tables for test isolation
	if err := helpers.TruncateAllTables(); err != nil {
		panic(fmt.Errorf("failed to truncate tables: %w", err))
	}

	// Create mocks (only external services)
	ctx.apiClient = helpers.NewMockAPIClient()
	ctx.mediator = helpers.NewMockMediator()
	ctx.clock = shared.NewMockClock(time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC))

	// Create real repositories
	ctx.repos = helpers.NewTestRepositories(ctx.apiClient, ctx.clock)

	// Create handlers
	ctx.getListingsHandler = shipyardQueries.NewGetShipyardListingsHandler(ctx.apiClient, ctx.repos.PlayerRepo)
	ctx.purchaseShipHandler = shipyardCommands.NewPurchaseShipHandler(
		ctx.repos.ShipRepo,
		ctx.repos.PlayerRepo,
		ctx.repos.WaypointRepo,
		ctx.repos.GraphService,
		ctx.apiClient,
		ctx.mediator,
	)
	ctx.batchPurchaseHandler = shipyardCommands.NewBatchPurchaseShipsHandler(
		ctx.repos.PlayerRepo,
		ctx.mediator,
		ctx.apiClient,
	)

	// Register handlers with mock mediator
	ctx.mediator.SetShipyardListingsHandler(ctx.getListingsHandler)
	ctx.mediator.SetPurchaseShipHandler(ctx.purchaseShipHandler)

	// Setup default mock behaviors
	ctx.setupDefaultMockBehaviors()
}

// setupDefaultMockBehaviors configures the mock API and mediator with default behaviors
func (ctx *shipyardApplicationContext) setupDefaultMockBehaviors() {
	// Setup purchase ship counter for generating unique ship symbols
	purchaseCount := 0

	// Default PurchaseShip function - generates new ships with unique symbols
	ctx.apiClient.SetPurchaseShipFunc(func(ctxAPI context.Context, shipType, waypointSymbol, token string) (*domainPorts.ShipPurchaseResult, error) {
		// Get agent data to determine current credits
		agentData, err := ctx.apiClient.GetAgent(ctxAPI, token)
		if err != nil {
			return nil, fmt.Errorf("failed to get agent: %w", err)
		}

		agentSymbol := agentData.Symbol
		currentCredits := agentData.Credits

		purchaseCount++
		shipSymbol := fmt.Sprintf("%s-SHIP-%d", agentSymbol, purchaseCount)

		// Find the ship listing to get the price
		shipyardData, err := ctx.apiClient.GetShipyard(ctxAPI, shared.ExtractSystemSymbol(waypointSymbol), waypointSymbol, token)
		if err != nil {
			return nil, err
		}

		var price int
		for _, listing := range shipyardData.Ships {
			if listing.Type == shipType {
				price = listing.PurchasePrice
				break
			}
		}

		// Calculate new credits after purchase
		newCredits := currentCredits - price

		// Update the mock API client with new credits so future GetAgent() calls see the updated balance
		player, err := ctx.repos.PlayerRepo.FindByAgentSymbol(ctxAPI, agentSymbol)
		if err == nil {
			player.Credits = newCredits
			ctx.repos.PlayerRepo.Add(ctxAPI, player)
			ctx.apiClient.UpdatePlayer(player)
		}

		return helpers.CreateTestShipPurchaseResult(agentSymbol, shipSymbol, shipType, waypointSymbol, price, newCredits), nil
	})

	// Default GetAgent function
	ctx.apiClient.SetError(false, "")
}

// ============================================================================
// Given Steps (Test Setup)
// ============================================================================

func (ctx *shipyardApplicationContext) aPlayerExistsWithCredits(agentSymbol string, credits int) error {
	// Create player ID from a hash of agent symbol for consistency
	playerID := 1
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}

	p := player.NewPlayer(pid, agentSymbol, "token-"+agentSymbol)
	p.Credits = credits

	// Add to mock API client for authorization
	ctx.apiClient.AddPlayer(p)

	// Save to database
	return ctx.repos.PlayerRepo.Add(context.Background(), p)
}

func (ctx *shipyardApplicationContext) thePlayerHasCredits(agentSymbol string, credits int) error {
	// Find and update existing player
	p, err := ctx.repos.PlayerRepo.FindByAgentSymbol(context.Background(), agentSymbol)
	if err != nil {
		return fmt.Errorf("player %s not found: %w", agentSymbol, err)
	}

	p.Credits = credits
	// Re-add to update (GORM repository uses Add for upsert)
	if err := ctx.repos.PlayerRepo.Add(context.Background(), p); err != nil {
		return err
	}

	// Also update the MockAPIClient so GetAgent() returns correct credits
	ctx.apiClient.UpdatePlayer(p)

	return nil
}

func (ctx *shipyardApplicationContext) aShipExistsForPlayerAtWaypoint(shipSymbol, agentSymbol, waypointSymbol string) error {
	// Find player
	p, err := ctx.repos.PlayerRepo.FindByAgentSymbol(context.Background(), agentSymbol)
	if err != nil {
		return fmt.Errorf("player %s not found: %w", agentSymbol, err)
	}

	playerID := p.ID

	// Create waypoint
	systemSymbol := shared.ExtractSystemSymbol(waypointSymbol)
	waypoint, err := ctx.repos.WaypointRepo.FindBySymbol(context.Background(), waypointSymbol, systemSymbol)
	if err != nil {
		// Waypoint doesn't exist, create it
		waypoint, err = shared.NewWaypoint(waypointSymbol, 0, 0)
		if err != nil {
			return err
		}
		if err := ctx.repos.WaypointRepo.Add(context.Background(), waypoint); err != nil {
			return err
		}
	}

	// Create ship
	fuel, _ := shared.NewFuel(100, 100)
	cargo, _ := shared.NewCargo(40, 0, []*shared.CargoItem{})

	ship, err := navigation.NewShip(
		shipSymbol,
		playerID,
		waypoint,
		fuel,
		100,
		40,
		cargo,
		30,
		"FRAME_PROBE",
		"COMMAND",
		navigation.NavStatusDocked,
	)
	if err != nil {
		return err
	}

	// Add to mock API client for GetShip calls (API repository fetches from mock)
	ctx.apiClient.AddShip(ship)

	return nil
}

func (ctx *shipyardApplicationContext) theShipIsDocked(shipSymbol string) error {
	// Ships are fetched from API mock, so we need to update the mock
	// Find ship in mock and update its status
	ship, ok := ctx.apiClient.GetShipFromMock(shipSymbol)
	if !ok {
		return fmt.Errorf("ship %s not found in mock", shipSymbol)
	}

	ship.EnsureDocked()
	ctx.apiClient.AddShip(ship)
	return nil
}

func (ctx *shipyardApplicationContext) theShipIsInOrbit(shipSymbol string) error {
	// Ships are fetched from API mock, so we need to update the mock
	ship, ok := ctx.apiClient.GetShipFromMock(shipSymbol)
	if !ok {
		return fmt.Errorf("ship %s not found in mock", shipSymbol)
	}

	ship.EnsureInOrbit()
	ctx.apiClient.AddShip(ship)
	return nil
}

func (ctx *shipyardApplicationContext) aWaypointExistsWithShipyardAtCoordinates(waypointSymbol string, x, y int) error {
	waypoint, err := helpers.CreateTestWaypointWithShipyard(waypointSymbol, x, y)
	if err != nil {
		return err
	}

	// Add to mock API client
	ctx.apiClient.AddWaypoint(waypoint)

	// Save to database
	return ctx.repos.WaypointRepo.Add(context.Background(), waypoint)
}

func (ctx *shipyardApplicationContext) aWaypointExistsAtCoordinates(waypointSymbol string, x, y int) error {
	waypoint, err := helpers.CreateTestWaypoint(waypointSymbol, x, y)
	if err != nil {
		return err
	}

	// Add to mock API client
	ctx.apiClient.AddWaypoint(waypoint)

	// Save to database
	return ctx.repos.WaypointRepo.Add(context.Background(), waypoint)
}

func (ctx *shipyardApplicationContext) theShipIsAtWaypoint(shipSymbol, waypointSymbol string) error {
	// Ships are in the API mock, so we need to update the mock
	ship, ok := ctx.apiClient.GetShipFromMock(shipSymbol)
	if !ok {
		return fmt.Errorf("ship %s not found in mock", shipSymbol)
	}

	// Get waypoint
	systemSymbol := shared.ExtractSystemSymbol(waypointSymbol)
	waypoint, err := ctx.repos.WaypointRepo.FindBySymbol(context.Background(), waypointSymbol, systemSymbol)
	if err != nil {
		return fmt.Errorf("waypoint %s not found: %w", waypointSymbol, err)
	}

	// Recreate ship at new location
	newShip, err := navigation.NewShip(
		ship.ShipSymbol(),
		ship.PlayerID(),
		waypoint,
		ship.Fuel(),
		ship.FuelCapacity(),
		ship.CargoCapacity(),
		ship.Cargo(),
		ship.EngineSpeed(),
		ship.FrameSymbol(),
		ship.Role(),
		ship.NavStatus(),
	)
	if err != nil {
		return err
	}

	// Update mock API client
	ctx.apiClient.AddShip(newShip)

	return nil
}

func (ctx *shipyardApplicationContext) theShipyardHasTheFollowingShips(waypointSymbol string, table *godog.Table) error {
	if len(table.Rows) < 2 {
		return fmt.Errorf("table must have header and at least one row")
	}

	// Parse table
	var listings []domainPorts.ShipListingData
	for i, row := range table.Rows[1:] {
		if len(row.Cells) < 2 {
			return fmt.Errorf("row %d: expected at least 2 columns", i+1)
		}

		shipType := row.Cells[0].Value
		price, err := strconv.Atoi(row.Cells[1].Value)
		if err != nil {
			return fmt.Errorf("row %d: invalid price: %w", i+1, err)
		}

		listing := helpers.CreateTestShipListing(shipType, price)
		listings = append(listings, listing)
	}

	// Create shipyard data
	shipyardData := helpers.CreateTestShipyardData(waypointSymbol, listings...)

	// Set in mock API client
	ctx.apiClient.SetShipyardData(waypointSymbol, shipyardData)

	return nil
}

func (ctx *shipyardApplicationContext) theShipyardHasNoShipsForSale(waypointSymbol string) error {
	shipyardData := helpers.CreateTestShipyardData(waypointSymbol)
	ctx.apiClient.SetShipyardData(waypointSymbol, shipyardData)
	return nil
}

func (ctx *shipyardApplicationContext) theAPIWillReturnAnErrorWhenGettingShipyard(waypointSymbol string) error {
	ctx.apiClient.SetGetShipyardFunc(func(ctxAPI context.Context, systemSymbol, waypointSym, token string) (*domainPorts.ShipyardData, error) {
		if waypointSym == waypointSymbol {
			return nil, fmt.Errorf("API error getting shipyard")
		}
		return ctx.apiClient.GetShipyard(ctxAPI, systemSymbol, waypointSym, token)
	})
	return nil
}

func (ctx *shipyardApplicationContext) navigationWillSucceedFromTo(fromWaypoint, toWaypoint string) error {
	// Default mediator behavior already handles this
	// Just ensure ship will be updated to the destination
	return nil
}

func (ctx *shipyardApplicationContext) navigationWillFailFromTo(fromWaypoint, toWaypoint string) error {
	ctx.mediator.SetSendFunc(func(ctxMed context.Context, request common.Request) (common.Response, error) {
		return nil, fmt.Errorf("navigation failed")
	})
	return nil
}

func (ctx *shipyardApplicationContext) waypointIsTheNearestShipyardTo(shipyardWaypoint, fromWaypoint string) error {
	// The shipyard is already set up with the SHIPYARD trait in the database
	// The handler will find it via the waypoint repository
	return nil
}

func (ctx *shipyardApplicationContext) thereAreNoShipyardsInSystem(systemSymbol string) error {
	// Don't add any waypoints with SHIPYARD trait for this system
	// The existing waypoints don't have SHIPYARD trait
	return nil
}

func (ctx *shipyardApplicationContext) theAPIWillReturnAnErrorWhenPurchasingAShip() error {
	ctx.apiClient.SetPurchaseShipFunc(func(ctxAPI context.Context, shipType, waypointSymbol, token string) (*domainPorts.ShipPurchaseResult, error) {
		return nil, fmt.Errorf("API error purchasing ship")
	})
	return nil
}

// ============================================================================
// When Steps (Execute Commands/Queries)
// ============================================================================

func (ctx *shipyardApplicationContext) iQueryShipyardListingsFor(waypointSymbol, agentSymbol string) error {
	// Find player
	p, err := ctx.repos.PlayerRepo.FindByAgentSymbol(context.Background(), agentSymbol)
	if err != nil {
		// Player not found - store error and return without executing handler
		ctx.lastError = fmt.Errorf("player not found: %w", err)
		ctx.lastShipyard = nil
		return nil
	}

	// Create context with token
	cmdCtx := common.WithPlayerToken(context.Background(), p.Token)

	// Create query
	query := &shipyardQueries.GetShipyardListingsQuery{
		SystemSymbol:   shared.ExtractSystemSymbol(waypointSymbol),
		WaypointSymbol: waypointSymbol,
		PlayerID:       p.ID,
	}

	// Execute handler
	response, err := ctx.getListingsHandler.Handle(cmdCtx, query)

	// Store results
	ctx.lastError = err
	if err == nil {
		resp := response.(*shipyardQueries.GetShipyardListingsResponse)
		ctx.lastShipyard = &resp.Shipyard
	}

	return nil
}

func (ctx *shipyardApplicationContext) iPurchaseAShipUsingAt(shipType, purchasingShipSymbol, waypointSymbol, agentSymbol string) error {
	return ctx.executePurchaseCommand(shipType, purchasingShipSymbol, waypointSymbol, agentSymbol, false)
}

func (ctx *shipyardApplicationContext) iPurchaseAShipUsingWithoutSpecifyingShipyard(shipType, purchasingShipSymbol, agentSymbol string) error {
	return ctx.executePurchaseCommand(shipType, purchasingShipSymbol, "", agentSymbol, true)
}

func (ctx *shipyardApplicationContext) executePurchaseCommand(shipType, purchasingShipSymbol, waypointSymbol, agentSymbol string, autoDiscover bool) error {
	// Find player
	p, err := ctx.repos.PlayerRepo.FindByAgentSymbol(context.Background(), agentSymbol)
	if err != nil {
		return fmt.Errorf("player %s not found: %w", agentSymbol, err)
	}

	playerID := p.ID
	token := p.Token

	// Create context with token
	cmdCtx := common.WithPlayerToken(context.Background(), token)

	// Create command
	cmd := &shipyardCommands.PurchaseShipCommand{
		PurchasingShipSymbol: purchasingShipSymbol,
		ShipType:             shipType,
		PlayerID:             playerID,
		ShipyardWaypoint:     waypointSymbol,
	}

	// Execute handler
	response, err := ctx.purchaseShipHandler.Handle(cmdCtx, cmd)

	// Store results
	ctx.lastError = err
	if err == nil {
		ctx.lastPurchaseResp = response.(*shipyardCommands.PurchaseShipResponse)
		ctx.lastShip = ctx.lastPurchaseResp.Ship

		// Register the purchased ship in the mock API client so it can be retrieved later
		ctx.apiClient.AddShip(ctx.lastShip)

		if autoDiscover && waypointSymbol == "" {
			ctx.autoDiscoveredShipyard = ctx.lastShip.CurrentLocation().Symbol
		}
	}

	return nil
}

func (ctx *shipyardApplicationContext) iBatchPurchaseShipsUsingAt(quantity int, shipType, purchasingShipSymbol, waypointSymbol, agentSymbol string) error {
	// Pass large maxBudget to indicate no budget constraint
	return ctx.executeBatchPurchaseCommand(quantity, shipType, 1000000000, purchasingShipSymbol, waypointSymbol, agentSymbol)
}

func (ctx *shipyardApplicationContext) iBatchPurchaseShipsWithMaxBudgetUsingAt(quantity int, shipType string, maxBudget int, purchasingShipSymbol, waypointSymbol, agentSymbol string) error {
	return ctx.executeBatchPurchaseCommand(quantity, shipType, maxBudget, purchasingShipSymbol, waypointSymbol, agentSymbol)
}

func (ctx *shipyardApplicationContext) executeBatchPurchaseCommand(quantity int, shipType string, maxBudget int, purchasingShipSymbol, waypointSymbol, agentSymbol string) error {
	// Find player
	p, err := ctx.repos.PlayerRepo.FindByAgentSymbol(context.Background(), agentSymbol)
	if err != nil {
		return fmt.Errorf("player %s not found: %w", agentSymbol, err)
	}

	playerID := p.ID
	token := p.Token

	// Create context with token
	cmdCtx := common.WithPlayerToken(context.Background(), token)

	// Create command
	cmd := &shipyardCommands.BatchPurchaseShipsCommand{
		PurchasingShipSymbol: purchasingShipSymbol,
		ShipType:             shipType,
		Quantity:             quantity,
		MaxBudget:            maxBudget,
		PlayerID:             playerID,
		ShipyardWaypoint:     waypointSymbol,
	}

	// Execute handler
	response, err := ctx.batchPurchaseHandler.Handle(cmdCtx, cmd)

	// Store results
	ctx.lastError = err
	if err == nil {
		ctx.lastBatchResp = response.(*shipyardCommands.BatchPurchaseShipsResponse)
		// Store created ships for assertions
		ctx.createdShips = ctx.lastBatchResp.PurchasedShips

		// Register all purchased ships in the mock API client
		for _, ship := range ctx.createdShips {
			ctx.apiClient.AddShip(ship)
		}
	} else {
		// Even on error, create empty response for assertions
		ctx.lastBatchResp = &shipyardCommands.BatchPurchaseShipsResponse{
			PurchasedShips:      []*navigation.Ship{},
			TotalCost:           0,
			ShipsPurchasedCount: 0,
		}
	}

	return nil
}

// ============================================================================
// Then Steps (Assertions)
// ============================================================================

func (ctx *shipyardApplicationContext) theQueryShouldSucceed() error {
	if ctx.lastError != nil {
		return fmt.Errorf("expected query to succeed, but got error: %v", ctx.lastError)
	}
	return nil
}

func (ctx *shipyardApplicationContext) theQueryShouldFail() error {
	if ctx.lastError == nil {
		return fmt.Errorf("expected query to fail, but it succeeded")
	}
	return nil
}

func (ctx *shipyardApplicationContext) theQueryShouldFailWithError(expectedError string) error {
	if ctx.lastError == nil {
		return fmt.Errorf("expected query to fail, but it succeeded")
	}
	if !strings.Contains(strings.ToLower(ctx.lastError.Error()), strings.ToLower(expectedError)) {
		return fmt.Errorf("expected error containing '%s', got: %v", expectedError, ctx.lastError)
	}
	return nil
}

func (ctx *shipyardApplicationContext) theShipyardShouldHaveShipTypesAvailable(expectedCount int) error {
	if ctx.lastShipyard == nil {
		return fmt.Errorf("no shipyard data available")
	}
	if len(ctx.lastShipyard.ShipTypes) != expectedCount {
		return fmt.Errorf("expected %d ship types, got %d", expectedCount, len(ctx.lastShipyard.ShipTypes))
	}
	return nil
}

func (ctx *shipyardApplicationContext) theShipyardShouldHaveAListingForPricedAt(shipType string, price int) error {
	if ctx.lastShipyard == nil {
		return fmt.Errorf("no shipyard data available")
	}

	listing, found := ctx.lastShipyard.FindListingByType(shipType)
	if !found {
		return fmt.Errorf("ship type %s not found in shipyard", shipType)
	}

	if listing.PurchasePrice != price {
		return fmt.Errorf("expected price %d for %s, got %d", price, shipType, listing.PurchasePrice)
	}

	return nil
}

func (ctx *shipyardApplicationContext) thePurchaseShouldSucceed() error {
	if ctx.lastError != nil {
		return fmt.Errorf("expected purchase to succeed, but got error: %v", ctx.lastError)
	}
	if ctx.lastPurchaseResp == nil {
		return fmt.Errorf("expected purchase response, but got nil")
	}
	return nil
}

func (ctx *shipyardApplicationContext) thePurchaseShouldFail() error {
	if ctx.lastError == nil {
		return fmt.Errorf("expected purchase to fail, but it succeeded")
	}
	return nil
}

func (ctx *shipyardApplicationContext) thePurchaseShouldFailWithError(expectedError string) error {
	if ctx.lastError == nil {
		return fmt.Errorf("expected purchase to fail, but it succeeded")
	}
	if !strings.Contains(strings.ToLower(ctx.lastError.Error()), strings.ToLower(expectedError)) {
		return fmt.Errorf("expected error containing '%s', got: %v", expectedError, ctx.lastError)
	}
	return nil
}

func (ctx *shipyardApplicationContext) thePlayerShouldHaveCreditsRemaining(agentSymbol string, expectedCredits int) error {
	// Find player in database to get token
	p, err := ctx.repos.PlayerRepo.FindByAgentSymbol(context.Background(), agentSymbol)
	if err != nil {
		return fmt.Errorf("player %s not found: %w", agentSymbol, err)
	}

	// Get agent data from API client (where credits are actually stored)
	agentData, err := ctx.apiClient.GetAgent(context.Background(), p.Token)
	if err != nil {
		return fmt.Errorf("failed to get agent data for %s: %w", agentSymbol, err)
	}

	if agentData.Credits != expectedCredits {
		return fmt.Errorf("expected player %s to have %d credits, got %d", agentSymbol, expectedCredits, agentData.Credits)
	}

	return nil
}

func (ctx *shipyardApplicationContext) aNewShipShouldBeCreatedForPlayer(agentSymbol string) error {
	if ctx.lastShip == nil {
		return fmt.Errorf("no ship was created")
	}

	// Verify ship was persisted to database
	ship, err := ctx.repos.ShipRepo.FindBySymbol(context.Background(), ctx.lastShip.ShipSymbol(), ctx.lastShip.PlayerID())
	if err != nil {
		return fmt.Errorf("ship not found in database: %w", err)
	}

	if ship == nil {
		return fmt.Errorf("ship not persisted to database")
	}

	return nil
}

func (ctx *shipyardApplicationContext) theNewShipShouldBeAtWaypoint(waypointSymbol string) error {
	if ctx.lastShip == nil {
		return fmt.Errorf("no ship was created")
	}

	if ctx.lastShip.CurrentLocation().Symbol != waypointSymbol {
		return fmt.Errorf("expected ship to be at %s, got %s", waypointSymbol, ctx.lastShip.CurrentLocation().Symbol)
	}

	return nil
}

func (ctx *shipyardApplicationContext) theNewShipShouldBeDocked() error {
	if ctx.lastShip == nil {
		return fmt.Errorf("no ship was created")
	}

	if ctx.lastShip.NavStatus() != navigation.NavStatusDocked {
		return fmt.Errorf("expected ship to be docked, got %s", ctx.lastShip.NavStatus())
	}

	return nil
}

func (ctx *shipyardApplicationContext) theMediatorShouldHaveBeenCalledToNavigateFromTo(fromWaypoint, toWaypoint string) error {
	callLog := ctx.mediator.GetCallLog()

	// Check if any call matches navigation to destination
	for _, call := range callLog {
		if strings.Contains(call, "NavigateRoute") && strings.Contains(call, toWaypoint) {
			return nil
		}
	}

	return fmt.Errorf("mediator was not called to navigate from %s to %s. Call log: %v", fromWaypoint, toWaypoint, callLog)
}

func (ctx *shipyardApplicationContext) theMediatorShouldHaveBeenCalledToDockTheShip() error {
	callLog := ctx.mediator.GetCallLog()

	for _, call := range callLog {
		if strings.Contains(call, "DockShip") {
			return nil
		}
	}

	return fmt.Errorf("mediator was not called to dock ship. Call log: %v", callLog)
}

func (ctx *shipyardApplicationContext) theShipyardShouldHaveBeenAutoDiscovered(expectedShipyard string) error {
	if ctx.autoDiscoveredShipyard == "" {
		return fmt.Errorf("no shipyard was auto-discovered")
	}

	if ctx.autoDiscoveredShipyard != expectedShipyard {
		return fmt.Errorf("expected auto-discovered shipyard %s, got %s", expectedShipyard, ctx.autoDiscoveredShipyard)
	}

	return nil
}

func (ctx *shipyardApplicationContext) theBatchPurchaseShouldSucceed() error {
	if ctx.lastError != nil {
		return fmt.Errorf("expected batch purchase to succeed, but got error: %v", ctx.lastError)
	}
	if ctx.lastBatchResp == nil {
		return fmt.Errorf("expected batch purchase response, but got nil")
	}
	return nil
}

func (ctx *shipyardApplicationContext) theBatchPurchaseShouldSucceedWithPartialResults() error {
	// Batch purchase can succeed even with partial results
	if ctx.lastBatchResp == nil {
		return fmt.Errorf("expected batch purchase response, but got nil")
	}
	return nil
}

func (ctx *shipyardApplicationContext) theBatchPurchaseShouldFail() error {
	if ctx.lastError == nil {
		return fmt.Errorf("expected batch purchase to fail, but it succeeded")
	}
	return nil
}

func (ctx *shipyardApplicationContext) theBatchPurchaseShouldFailWithError(expectedError string) error {
	if ctx.lastError == nil {
		return fmt.Errorf("expected batch purchase to fail, but it succeeded")
	}
	if !strings.Contains(strings.ToLower(ctx.lastError.Error()), strings.ToLower(expectedError)) {
		return fmt.Errorf("expected error containing '%s', got: %v", expectedError, ctx.lastError)
	}
	return nil
}

func (ctx *shipyardApplicationContext) shipsShouldHaveBeenPurchased(expectedCount int) error {
	if ctx.lastBatchResp == nil {
		return fmt.Errorf("no batch purchase response available")
	}

	actualCount := len(ctx.lastBatchResp.PurchasedShips)
	if actualCount != expectedCount {
		return fmt.Errorf("expected %d ships to be purchased, got %d", expectedCount, actualCount)
	}

	return nil
}

func (ctx *shipyardApplicationContext) allPurchasedShipsShouldBeAtWaypoint(waypointSymbol string) error {
	if ctx.lastBatchResp == nil {
		return fmt.Errorf("no batch purchase response available")
	}

	for i, ship := range ctx.lastBatchResp.PurchasedShips {
		if ship.CurrentLocation().Symbol != waypointSymbol {
			return fmt.Errorf("ship %d (%s) is at %s, expected %s", i, ship.ShipSymbol(), ship.CurrentLocation().Symbol, waypointSymbol)
		}
	}

	return nil
}

// ==================== REGISTRATION ====================

func InitializeShipyardApplicationScenarios(sc *godog.ScenarioContext) {
	ctx := &shipyardApplicationContext{}

	sc.Before(func(context.Context, *godog.Scenario) (context.Context, error) {
		ctx.reset()
		return context.Background(), nil
	})

	// Register Given steps
	sc.Step(`^a player "([^"]*)" exists with (\d+) credits$`, ctx.aPlayerExistsWithCredits)
	sc.Step(`^the player "([^"]*)" has (\d+) credits$`, ctx.thePlayerHasCredits)
	sc.Step(`^a ship "([^"]*)" exists for player "([^"]*)" at waypoint "([^"]*)"$`, ctx.aShipExistsForPlayerAtWaypoint)
	sc.Step(`^the ship "([^"]*)" is docked$`, ctx.theShipIsDocked)
	sc.Step(`^the ship "([^"]*)" is in orbit$`, ctx.theShipIsInOrbit)
	sc.Step(`^a waypoint "([^"]*)" exists with a shipyard at coordinates \((\d+), (\d+)\)$`, ctx.aWaypointExistsWithShipyardAtCoordinates)
	sc.Step(`^a waypoint "([^"]*)" exists at coordinates \((\d+), (\d+)\)$`, ctx.aWaypointExistsAtCoordinates)
	sc.Step(`^the ship "([^"]*)" is at waypoint "([^"]*)"$`, ctx.theShipIsAtWaypoint)
	sc.Step(`^the shipyard at "([^"]*)" has the following ships:$`, ctx.theShipyardHasTheFollowingShips)
	sc.Step(`^the shipyard at "([^"]*)" has no ships for sale$`, ctx.theShipyardHasNoShipsForSale)
	sc.Step(`^the API will return an error when getting shipyard "([^"]*)"$`, ctx.theAPIWillReturnAnErrorWhenGettingShipyard)
	sc.Step(`^navigation will succeed from "([^"]*)" to "([^"]*)"$`, ctx.navigationWillSucceedFromTo)
	sc.Step(`^navigation will fail from "([^"]*)" to "([^"]*)"$`, ctx.navigationWillFailFromTo)
	sc.Step(`^waypoint "([^"]*)" is the nearest shipyard to "([^"]*)"$`, ctx.waypointIsTheNearestShipyardTo)
	sc.Step(`^there are no shipyards in system "([^"]*)"$`, ctx.thereAreNoShipyardsInSystem)
	sc.Step(`^the API will return an error when purchasing a ship$`, ctx.theAPIWillReturnAnErrorWhenPurchasingAShip)

	// Register When steps
	sc.Step(`^I query shipyard listings for "([^"]*)" as "([^"]*)"$`, ctx.iQueryShipyardListingsFor)
	sc.Step(`^I purchase a "([^"]*)" ship using "([^"]*)" at "([^"]*)" as "([^"]*)"$`, ctx.iPurchaseAShipUsingAt)
	sc.Step(`^I purchase a "([^"]*)" ship using "([^"]*)" without specifying shipyard as "([^"]*)"$`, ctx.iPurchaseAShipUsingWithoutSpecifyingShipyard)
	sc.Step(`^I batch purchase (\d+) "([^"]*)" ships using "([^"]*)" at "([^"]*)" as "([^"]*)"$`, ctx.iBatchPurchaseShipsUsingAt)
	sc.Step(`^I batch purchase (\d+) "([^"]*)" ships with max budget (\d+) using "([^"]*)" at "([^"]*)" as "([^"]*)"$`, ctx.iBatchPurchaseShipsWithMaxBudgetUsingAt)

	// Register Then steps
	sc.Step(`^the query should succeed$`, ctx.theQueryShouldSucceed)
	sc.Step(`^the query should fail$`, ctx.theQueryShouldFail)
	sc.Step(`^the query should fail with error "([^"]*)"$`, ctx.theQueryShouldFailWithError)
	sc.Step(`^the shipyard should have (\d+) ship types available$`, ctx.theShipyardShouldHaveShipTypesAvailable)
	sc.Step(`^the shipyard should have a listing for "([^"]*)" priced at (\d+)$`, ctx.theShipyardShouldHaveAListingForPricedAt)
	sc.Step(`^the purchase should succeed$`, ctx.thePurchaseShouldSucceed)
	sc.Step(`^the purchase should fail$`, ctx.thePurchaseShouldFail)
	sc.Step(`^the purchase should fail with error "([^"]*)"$`, ctx.thePurchaseShouldFailWithError)
	sc.Step(`^the player "([^"]*)" should have (\d+) credits remaining$`, ctx.thePlayerShouldHaveCreditsRemaining)
	sc.Step(`^a new ship should be created for player "([^"]*)"$`, ctx.aNewShipShouldBeCreatedForPlayer)
	sc.Step(`^the new ship should be at waypoint "([^"]*)"$`, ctx.theNewShipShouldBeAtWaypoint)
	sc.Step(`^the new ship should be docked$`, ctx.theNewShipShouldBeDocked)
	sc.Step(`^the mediator should have been called to navigate from "([^"]*)" to "([^"]*)"$`, ctx.theMediatorShouldHaveBeenCalledToNavigateFromTo)
	sc.Step(`^the mediator should have been called to dock the ship$`, ctx.theMediatorShouldHaveBeenCalledToDockTheShip)
	sc.Step(`^the shipyard "([^"]*)" should have been auto-discovered$`, ctx.theShipyardShouldHaveBeenAutoDiscovered)
	sc.Step(`^the batch purchase should succeed$`, ctx.theBatchPurchaseShouldSucceed)
	sc.Step(`^the batch purchase should succeed with partial results$`, ctx.theBatchPurchaseShouldSucceedWithPartialResults)
	sc.Step(`^the batch purchase should fail$`, ctx.theBatchPurchaseShouldFail)
	sc.Step(`^the batch purchase should fail with error "([^"]*)"$`, ctx.theBatchPurchaseShouldFailWithError)
	sc.Step(`^(\d+) ships should have been purchased$`, ctx.shipsShouldHaveBeenPurchased)
	sc.Step(`^all purchased ships should be at waypoint "([^"]*)"$`, ctx.allPurchasedShipsShouldBeAtWaypoint)
}
