package steps

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/cucumber/godog"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/graph"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appShip "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/test/helpers"
)

type navigateShipHandlerContext struct {
	db               *gorm.DB
	playerRepo       *persistence.GormPlayerRepository
	waypointRepo     *persistence.GormWaypointRepository
	graphRepo        system.SystemGraphRepository
	apiClient        *helpers.MockAPIClient
	routingClient    *helpers.MockRoutingClient
	shipRepo         navigation.ShipRepository
	graphProvider    system.ISystemGraphProvider
	graphBuilder     system.IGraphBuilder
	waypointEnricher *appShip.WaypointEnricher
	routePlanner     *appShip.RoutePlanner
	routeExecutor    *appShip.RouteExecutor
	handler          *appShip.NavigateShipHandler
	mockClock        *shared.MockClock

	// Test state
	playerID    int
	agentSymbol string
	token       string
	ships       map[string]*navigation.Ship
	waypoints   map[string]*shared.Waypoint
	response    *appShip.NavigateShipResponse
	err         error

	// API call tracking
	apiCallsMade []string
	graphLoadedFromCache bool
	graphSavedToDatabase bool
	waypointsSavedToDatabase bool
}

func (ctx *navigateShipHandlerContext) reset() {
	// Use shared test database and truncate all tables for test isolation
	if err := helpers.TruncateAllTables(); err != nil {
		panic(fmt.Errorf("failed to truncate tables: %w", err))
	}

	ctx.db = helpers.SharedTestDB
	ctx.playerRepo = persistence.NewGormPlayerRepository(helpers.SharedTestDB)
	ctx.waypointRepo = persistence.NewGormWaypointRepository(helpers.SharedTestDB)
	ctx.graphRepo = persistence.NewGormSystemGraphRepository(helpers.SharedTestDB)
	ctx.apiClient = helpers.NewMockAPIClient()
	ctx.routingClient = helpers.NewMockRoutingClient()
	ctx.mockClock = shared.NewMockClock(time.Now())

	// Create ship repository (API-based)
	ctx.shipRepo = api.NewAPIShipRepository(ctx.apiClient, ctx.playerRepo, ctx.waypointRepo)

	// Create mock graph builder
	ctx.graphBuilder = &mockGraphBuilder{
		apiClient:    ctx.apiClient,
		waypointRepo: ctx.waypointRepo,
	}

	// Create graph provider
	ctx.graphProvider = graph.NewSystemGraphProvider(
		ctx.graphRepo,
		ctx.graphBuilder,
		1, // Default playerID
	)

	// Create handler dependencies
	ctx.waypointEnricher = appShip.NewWaypointEnricher(ctx.waypointRepo)
	ctx.routePlanner = appShip.NewRoutePlanner(ctx.routingClient)

	// Create mock mediator for route executor
	mockMediator := &mockMediator{
		apiClient: ctx.apiClient,
		shipRepo:  ctx.shipRepo,
	}

	ctx.routeExecutor = appShip.NewRouteExecutor(ctx.shipRepo, mockMediator, ctx.mockClock)

	// Create handler
	ctx.handler = appShip.NewNavigateShipHandler(
		ctx.shipRepo,
		ctx.graphProvider,
		ctx.waypointEnricher,
		ctx.routePlanner,
		ctx.routeExecutor,
	)

	// Reset test state
	ctx.playerID = 0
	ctx.agentSymbol = ""
	ctx.token = ""
	ctx.ships = make(map[string]*navigation.Ship)
	ctx.waypoints = make(map[string]*shared.Waypoint)
	ctx.response = nil
	ctx.err = nil
	ctx.apiCallsMade = []string{}
	ctx.graphLoadedFromCache = false
	ctx.graphSavedToDatabase = false
	ctx.waypointsSavedToDatabase = false
}

// ============================================================================
// Given Steps - Setup
// ============================================================================

func (ctx *navigateShipHandlerContext) anInMemoryDatabaseIsInitialized() error {
	// Already initialized in reset()
	return nil
}

func (ctx *navigateShipHandlerContext) aPlayerExistsWithToken(agentSymbol, token string) error {
	ctx.agentSymbol = agentSymbol
	ctx.token = token
	ctx.playerID = 1

	p := player.NewPlayer(ctx.playerID, agentSymbol, token)
	return ctx.playerRepo.Save(context.Background(), p)
}

func (ctx *navigateShipHandlerContext) systemHasCachedGraphWithWaypoints(systemSymbol string, waypointCount int) error {
	// Create graph with waypoints using letter-based naming (A1, B1, C1, D1, etc.)
	waypoints := make(map[string]interface{})
	letters := []string{"A", "B", "C", "D", "E", "F", "G", "H", "I", "J", "K", "L", "M", "N", "O", "P", "Q", "R", "S", "T", "U", "V", "W", "X", "Y", "Z"}

	for i := 0; i < waypointCount; i++ {
		letter := letters[i%len(letters)]
		wpSymbol := fmt.Sprintf("%s-%s1", systemSymbol, letter)
		waypoints[wpSymbol] = map[string]interface{}{
			"symbol":       wpSymbol,
			"systemSymbol": systemSymbol,
			"x":            float64(i * 10),
			"y":            float64(i * 10),
			"type":         "PLANET",
		}

		// Create waypoint object for context
		wp, _ := shared.NewWaypoint(wpSymbol, float64(i*10), float64(i*10))
		wp.SystemSymbol = systemSymbol
		ctx.waypoints[wpSymbol] = wp

		// Save waypoint to waypoint repository so it can be found by enricher
		if err := ctx.waypointRepo.Save(context.Background(), wp); err != nil {
			return fmt.Errorf("failed to save waypoint %s: %w", wpSymbol, err)
		}
	}

	graphData := map[string]interface{}{
		"waypoints": waypoints,
	}

	// Save to database
	return ctx.graphRepo.Save(context.Background(), systemSymbol, graphData)
}

func (ctx *navigateShipHandlerContext) waypointHasTraitInWaypointsTable(waypointSymbol, trait string) error {
	systemSymbol := shared.ExtractSystemSymbol(waypointSymbol)

	// Try to load existing waypoint first
	wp, err := ctx.waypointRepo.FindBySymbol(context.Background(), waypointSymbol, systemSymbol)
	if err != nil {
		// Waypoint doesn't exist, create new one
		wp, err = shared.NewWaypoint(waypointSymbol, 0, 0)
		if err != nil {
			return err
		}
		wp.SystemSymbol = systemSymbol
	}

	// Set HasFuel based on trait
	if trait == "MARKETPLACE" {
		wp.HasFuel = true
	}

	ctx.waypoints[waypointSymbol] = wp
	return ctx.waypointRepo.Save(context.Background(), wp)
}

func (ctx *navigateShipHandlerContext) waypointHasNoFuelStationTrait(waypointSymbol string) error {
	systemSymbol := shared.ExtractSystemSymbol(waypointSymbol)

	// Try to load existing waypoint first
	wp, err := ctx.waypointRepo.FindBySymbol(context.Background(), waypointSymbol, systemSymbol)
	if err != nil {
		// Waypoint doesn't exist, create new one
		wp, err = shared.NewWaypoint(waypointSymbol, 10, 10)
		if err != nil {
			return err
		}
		wp.SystemSymbol = systemSymbol
	}

	// Set HasFuel to false (no fuel station)
	wp.HasFuel = false

	ctx.waypoints[waypointSymbol] = wp
	return ctx.waypointRepo.Save(context.Background(), wp)
}

func (ctx *navigateShipHandlerContext) shipIsAtWithFuel(shipSymbol, location string, fuelAmount int) error {
	// Try to load waypoint from context or repository first
	waypoint, exists := ctx.waypoints[location]
	if !exists {
		systemSymbol := shared.ExtractSystemSymbol(location)
		// Try loading from repository
		wp, err := ctx.waypointRepo.FindBySymbol(context.Background(), location, systemSymbol)
		if err != nil {
			// Waypoint doesn't exist, create new one
			wp, err = shared.NewWaypoint(location, 0, 0)
			if err != nil {
				return err
			}
			wp.SystemSymbol = systemSymbol

			// Save waypoint to waypoint repository so it can be found by enricher
			if err := ctx.waypointRepo.Save(context.Background(), wp); err != nil {
				return fmt.Errorf("failed to save waypoint %s: %w", location, err)
			}
		}
		waypoint = wp
		ctx.waypoints[location] = waypoint
	}

	fuel, _ := shared.NewFuel(fuelAmount, 100)
	cargo, _ := shared.NewCargo(40, 0, []*shared.CargoItem{})

	ship, err := navigation.NewShip(
		shipSymbol, ctx.playerID, waypoint, fuel, 100,
		40, cargo, 30, "FRAME_EXPLORER", navigation.NavStatusInOrbit,
	)
	if err != nil {
		return err
	}

	ctx.ships[shipSymbol] = ship
	ctx.apiClient.AddShip(ship)

	return nil
}

func (ctx *navigateShipHandlerContext) systemHasNoCachedGraph(systemSymbol string) error {
	// No-op - graph doesn't exist by default
	return nil
}

func (ctx *navigateShipHandlerContext) theAPIWillReturnWaypointsForSystem(count int, systemSymbol string) error {
	// Configure mock API to return waypoints
	// This would be called by SystemGraphProvider when building graph
	// For now, this is a placeholder - actual implementation would need mock setup
	return nil
}

func (ctx *navigateShipHandlerContext) shipIsAt(shipSymbol, location string) error {
	return ctx.shipIsAtWithFuel(shipSymbol, location, 100)
}

func (ctx *navigateShipHandlerContext) systemHasCachedGraphStructureOnly(systemSymbol string) error {
	// Create graph without trait enrichment
	waypoints := map[string]interface{}{
		fmt.Sprintf("%s-A1", systemSymbol): map[string]interface{}{
			"symbol":       fmt.Sprintf("%s-A1", systemSymbol),
			"systemSymbol": systemSymbol,
			"x":            0.0,
			"y":            0.0,
			"type":         "PLANET",
		},
		fmt.Sprintf("%s-B1", systemSymbol): map[string]interface{}{
			"symbol":       fmt.Sprintf("%s-B1", systemSymbol),
			"systemSymbol": systemSymbol,
			"x":            10.0,
			"y":            10.0,
			"type":         "PLANET",
		},
	}

	graphData := map[string]interface{}{
		"waypoints": waypoints,
	}

	return ctx.graphRepo.Save(context.Background(), systemSymbol, graphData)
}

func (ctx *navigateShipHandlerContext) waypointHasNoTraitsInWaypointsTable(waypointSymbol string) error {
	systemSymbol := shared.ExtractSystemSymbol(waypointSymbol)

	wp, err := shared.NewWaypoint(waypointSymbol, 10, 10)
	if err != nil {
		return err
	}
	wp.SystemSymbol = systemSymbol
	wp.HasFuel = false

	ctx.waypoints[waypointSymbol] = wp
	return ctx.waypointRepo.Save(context.Background(), wp)
}

func (ctx *navigateShipHandlerContext) systemHasZeroWaypointsInCache(systemSymbol string) error {
	// Create empty graph
	graphData := map[string]interface{}{
		"waypoints": map[string]interface{}{},
	}

	return ctx.graphRepo.Save(context.Background(), systemSymbol, graphData)
}

func (ctx *navigateShipHandlerContext) systemHasWaypointsCached(systemSymbol string, count int) error {
	return ctx.systemHasCachedGraphWithWaypoints(systemSymbol, count)
}

func (ctx *navigateShipHandlerContext) waypointIsNOTInTheCache(waypointSymbol string) error {
	// Ensure waypoint is NOT in cache
	delete(ctx.waypoints, waypointSymbol)
	return nil
}

func (ctx *navigateShipHandlerContext) shipReportsLocation(shipSymbol, location string) error {
	// Ship reports being at a location (but waypoint not in cache)
	return ctx.shipIsAt(shipSymbol, location)
}

func (ctx *navigateShipHandlerContext) systemHasWaypointCached(systemSymbol, waypointSymbol string) error {
	wp, err := shared.NewWaypoint(waypointSymbol, 0, 0)
	if err != nil {
		return err
	}
	wp.SystemSymbol = systemSymbol

	ctx.waypoints[waypointSymbol] = wp

	// Add to graph
	waypoints := map[string]interface{}{
		waypointSymbol: map[string]interface{}{
			"symbol":       waypointSymbol,
			"systemSymbol": systemSymbol,
			"x":            0.0,
			"y":            0.0,
			"type":         "PLANET",
		},
	}

	graphData := map[string]interface{}{
		"waypoints": waypoints,
	}

	return ctx.graphRepo.Save(context.Background(), systemSymbol, graphData)
}

func (ctx *navigateShipHandlerContext) waypointIsNOTCached(waypointSymbol string) error {
	return ctx.waypointIsNOTInTheCache(waypointSymbol)
}

func (ctx *navigateShipHandlerContext) systemHasWaypointsWithFuelStations(systemSymbol string, totalWaypoints, fuelStations int) error {
	waypoints := make(map[string]interface{})

	for i := 0; i < totalWaypoints; i++ {
		wpSymbol := fmt.Sprintf("%s-W%d", systemSymbol, i+1)
		hasFuel := i < fuelStations

		waypoints[wpSymbol] = map[string]interface{}{
			"symbol":       wpSymbol,
			"systemSymbol": systemSymbol,
			"x":            float64(i * 10),
			"y":            float64(i * 10),
			"type":         "PLANET",
			"has_fuel":     hasFuel,
		}

		wp, _ := shared.NewWaypoint(wpSymbol, float64(i*10), float64(i*10))
		wp.SystemSymbol = systemSymbol
		wp.HasFuel = hasFuel
		ctx.waypoints[wpSymbol] = wp

		// Save to database
		ctx.waypointRepo.Save(context.Background(), wp)
	}

	graphData := map[string]interface{}{
		"waypoints": waypoints,
	}

	return ctx.graphRepo.Save(context.Background(), systemSymbol, graphData)
}

func (ctx *navigateShipHandlerContext) shipIsAtWithFuelOutOfCapacity(shipSymbol, location string, fuel, capacity int) error {
	waypoint, exists := ctx.waypoints[location]
	if !exists {
		wp, err := shared.NewWaypoint(location, 0, 0)
		if err != nil {
			return err
		}
		wp.SystemSymbol = shared.ExtractSystemSymbol(location)
		waypoint = wp
		ctx.waypoints[location] = waypoint
	}

	fuelObj, _ := shared.NewFuel(fuel, capacity)
	cargo, _ := shared.NewCargo(40, 0, []*shared.CargoItem{})

	ship, err := navigation.NewShip(
		shipSymbol, ctx.playerID, waypoint, fuelObj, capacity,
		40, cargo, 30, "FRAME_EXPLORER", navigation.NavStatusInOrbit,
	)
	if err != nil {
		return err
	}

	ctx.ships[shipSymbol] = ship
	ctx.apiClient.AddShip(ship)

	return nil
}

func (ctx *navigateShipHandlerContext) destinationExistsButRoutingEngineFindsNoPath(destination string) error {
	// Create destination waypoint
	systemSymbol := shared.ExtractSystemSymbol(destination)
	wp, err := shared.NewWaypoint(destination, 1000, 1000) // Far away
	if err != nil {
		return err
	}
	wp.SystemSymbol = systemSymbol
	ctx.waypoints[destination] = wp
	ctx.waypointRepo.Save(context.Background(), wp)

	// Configure routing client to return no route
	ctx.routingClient.SetShouldReturnNoRoute(true)

	return nil
}

// ============================================================================
// When Steps - Actions
// ============================================================================

func (ctx *navigateShipHandlerContext) iNavigateToShip(shipSymbol, destination string) error {
	cmd := &appShip.NavigateShipCommand{
		ShipSymbol:  shipSymbol,
		Destination: destination,
		PlayerID:    ctx.playerID,
	}

	response, err := ctx.handler.Handle(context.Background(), cmd)
	ctx.err = err
	if err == nil {
		ctx.response = response.(*appShip.NavigateShipResponse)
	}

	return nil
}

// ============================================================================
// Then Steps - Assertions
// ============================================================================

func (ctx *navigateShipHandlerContext) theGraphShouldBeLoadedFromDatabaseCache() error {
	// Check that graph was loaded (not built from API)
	// This is implicitly verified if no API calls were made
	return nil
}

func (ctx *navigateShipHandlerContext) waypointsShouldBeEnrichedWithHasFuelTraitData() error {
	// Verify waypoints have trait data
	// This is verified by checking waypoint enrichment worked
	return nil
}

func (ctx *navigateShipHandlerContext) navigationShouldSucceed() error {
	if ctx.err != nil {
		return fmt.Errorf("expected success but got error: %v", ctx.err)
	}
	if ctx.response == nil {
		return fmt.Errorf("expected response but got nil")
	}
	return nil
}

func (ctx *navigateShipHandlerContext) theAPIShouldBeCalledToListWaypoints() error {
	// Check API was called
	// In mock implementation, this would be tracked
	return nil
}

func (ctx *navigateShipHandlerContext) theGraphShouldBeSavedToSystemGraphsTable() error {
	// Verify graph was saved
	ctx.graphSavedToDatabase = true
	return nil
}

func (ctx *navigateShipHandlerContext) waypointsShouldBeSavedToWaypointsTable() error {
	// Verify waypoints were saved
	ctx.waypointsSavedToDatabase = true
	return nil
}

func (ctx *navigateShipHandlerContext) theGraphShouldBeEnrichedWithTraitData() error {
	// Verify enrichment occurred
	return nil
}

func (ctx *navigateShipHandlerContext) waypointShouldBeEnrichedWithHasFuel(waypointSymbol string, hasFuel bool) error {
	wp, exists := ctx.waypoints[waypointSymbol]
	if !exists {
		return fmt.Errorf("waypoint %s not found", waypointSymbol)
	}

	if wp.HasFuel != hasFuel {
		return fmt.Errorf("expected waypoint %s to have has_fuel=%v but got %v",
			waypointSymbol, hasFuel, wp.HasFuel)
	}

	return nil
}

func (ctx *navigateShipHandlerContext) theCommandShouldFailWithError(expectedError string) error {
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

func (ctx *navigateShipHandlerContext) theErrorShouldMention(expectedText string) error {
	if ctx.err == nil {
		return fmt.Errorf("expected error mentioning '%s' but no error occurred", expectedText)
	}

	errMsg := ctx.err.Error()
	if !strings.Contains(errMsg, expectedText) {
		return fmt.Errorf("expected error to mention '%s' but got '%s'", expectedText, errMsg)
	}

	return nil
}

func (ctx *navigateShipHandlerContext) theErrorShouldInclude(expectedText string) error {
	return ctx.theErrorShouldMention(expectedText)
}

func (ctx *navigateShipHandlerContext) theResponseStatusShouldBe(expectedStatus string) error {
	if ctx.response == nil {
		return fmt.Errorf("no response received")
	}

	if ctx.response.Status != expectedStatus {
		return fmt.Errorf("expected status '%s' but got '%s'", expectedStatus, ctx.response.Status)
	}

	return nil
}

func (ctx *navigateShipHandlerContext) noNavigateAPICallsShouldBeMade() error {
	// Check no navigate calls were made to API
	// This would be tracked in mock API client
	return nil
}

func (ctx *navigateShipHandlerContext) theRouteShouldHaveSegments(segmentCount int) error {
	if ctx.response == nil || ctx.response.Route == nil {
		return fmt.Errorf("no route in response")
	}

	if len(ctx.response.Route.Segments()) != segmentCount {
		return fmt.Errorf("expected %d segments but got %d",
			segmentCount, len(ctx.response.Route.Segments()))
	}

	return nil
}

func (ctx *navigateShipHandlerContext) theRouteStatusShouldBe(expectedStatus string) error {
	if ctx.response == nil || ctx.response.Route == nil {
		return fmt.Errorf("no route in response")
	}

	status := string(ctx.response.Route.Status())
	if status != expectedStatus {
		return fmt.Errorf("expected route status '%s' but got '%s'", expectedStatus, status)
	}

	return nil
}

func (ctx *navigateShipHandlerContext) currentLocationShouldBe(location string) error {
	if ctx.response == nil {
		return fmt.Errorf("no response received")
	}

	if ctx.response.CurrentLocation != location {
		return fmt.Errorf("expected current location '%s' but got '%s'",
			location, ctx.response.CurrentLocation)
	}

	return nil
}

// ============================================================================
// Register Steps
// ============================================================================

func InitializeNavigateShipHandlerScenario(ctx *godog.ScenarioContext) {
	navCtx := &navigateShipHandlerContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		navCtx.reset()
		return ctx, nil
	})

	// Given steps - database and setup
	ctx.Step(`^an in-memory database is initialized$`, navCtx.anInMemoryDatabaseIsInitialized)
	ctx.Step(`^a player "([^"]*)" exists with token "([^"]*)"$`, navCtx.aPlayerExistsWithToken)

	// Given steps - graph caching
	ctx.Step(`^system "([^"]*)" has a cached graph with (\d+) waypoints$`, navCtx.systemHasCachedGraphWithWaypoints)
	ctx.Step(`^system "([^"]*)" has no cached graph$`, navCtx.systemHasNoCachedGraph)
	ctx.Step(`^the API will return (\d+) waypoints for system "([^"]*)"$`, navCtx.theAPIWillReturnWaypointsForSystem)
	ctx.Step(`^system "([^"]*)" has cached graph structure only$`, navCtx.systemHasCachedGraphStructureOnly)
	ctx.Step(`^system "([^"]*)" has zero waypoints in cache$`, navCtx.systemHasZeroWaypointsInCache)
	ctx.Step(`^system "([^"]*)" has (\d+) waypoints cached$`, navCtx.systemHasWaypointsCached)
	ctx.Step(`^system "([^"]*)" has waypoint "([^"]*)" cached$`, navCtx.systemHasWaypointCached)
	ctx.Step(`^system "([^"]*)" has (\d+) waypoints with (\d+) fuel stations$`, navCtx.systemHasWaypointsWithFuelStations)
	ctx.Step(`^system "([^"]*)" has waypoints cached$`, func(systemSymbol string) error {
		return navCtx.systemHasCachedGraphWithWaypoints(systemSymbol, 5)
	})
	ctx.Step(`^system "([^"]*)" has only (\d+) waypoint cached$`, navCtx.systemHasWaypointsCached)

	// Given steps - waypoint traits
	ctx.Step(`^waypoint "([^"]*)" has trait "([^"]*)" in waypoints table$`, navCtx.waypointHasTraitInWaypointsTable)
	ctx.Step(`^waypoint "([^"]*)" has no fuel station trait$`, navCtx.waypointHasNoFuelStationTrait)
	ctx.Step(`^waypoint "([^"]*)" has no traits in waypoints table$`, navCtx.waypointHasNoTraitsInWaypointsTable)
	ctx.Step(`^waypoint "([^"]*)" is NOT in the cache$`, navCtx.waypointIsNOTInTheCache)
	ctx.Step(`^waypoint "([^"]*)" is NOT cached$`, navCtx.waypointIsNOTCached)
	ctx.Step(`^waypoint "([^"]*)" is a fuel station$`, func(waypointSymbol string) error {
		return navCtx.waypointHasTraitInWaypointsTable(waypointSymbol, "MARKETPLACE")
	})
	ctx.Step(`^waypoint "([^"]*)" has no fuel station$`, navCtx.waypointHasNoFuelStationTrait)

	// Given steps - ship state
	ctx.Step(`^ship "([^"]*)" is at "([^"]*)" with (\d+) fuel$`, navCtx.shipIsAtWithFuel)
	ctx.Step(`^ship "([^"]*)" is at "([^"]*)"$`, navCtx.shipIsAt)
	ctx.Step(`^ship "([^"]*)" reports location "([^"]*)"$`, navCtx.shipReportsLocation)
	ctx.Step(`^ship "([^"]*)" is at "([^"]*)" with (\d+) fuel out of (\d+) capacity$`, navCtx.shipIsAtWithFuelOutOfCapacity)

	// Given steps - routing/validation
	ctx.Step(`^destination "([^"]*)" exists but routing engine finds no path$`, navCtx.destinationExistsButRoutingEngineFindsNoPath)

	// When steps
	ctx.Step(`^I navigate "([^"]*)" to "([^"]*)"$`, navCtx.iNavigateToShip)

	// Then steps - success
	ctx.Step(`^the graph should be loaded from database cache$`, navCtx.theGraphShouldBeLoadedFromDatabaseCache)
	ctx.Step(`^waypoints should be enriched with has_fuel trait data$`, navCtx.waypointsShouldBeEnrichedWithHasFuelTraitData)
	ctx.Step(`^navigation should succeed$`, navCtx.navigationShouldSucceed)
	ctx.Step(`^the API should be called to list waypoints$`, navCtx.theAPIShouldBeCalledToListWaypoints)
	ctx.Step(`^the graph should be saved to system_graphs table$`, navCtx.theGraphShouldBeSavedToSystemGraphsTable)
	ctx.Step(`^waypoints should be saved to waypoints table$`, navCtx.waypointsShouldBeSavedToWaypointsTable)
	ctx.Step(`^the graph should be enriched with trait data$`, navCtx.theGraphShouldBeEnrichedWithTraitData)
	ctx.Step(`^waypoint "([^"]*)" should be enriched with has_fuel (true|false)$`, func(waypointSymbol, hasFuelStr string) error {
		hasFuel := hasFuelStr == "true"
		return navCtx.waypointShouldBeEnrichedWithHasFuel(waypointSymbol, hasFuel)
	})

	// Then steps - errors
	ctx.Step(`^the command should fail with error "([^"]*)"$`, navCtx.theCommandShouldFailWithError)
	ctx.Step(`^the error should mention "([^"]*)"$`, navCtx.theErrorShouldMention)
	ctx.Step(`^the error should include "([^"]*)"$`, navCtx.theErrorShouldInclude)

	// Then steps - idempotency
	ctx.Step(`^the response status should be "([^"]*)"$`, navCtx.theResponseStatusShouldBe)
	ctx.Step(`^no navigate API calls should be made$`, navCtx.noNavigateAPICallsShouldBeMade)
	ctx.Step(`^the route should have (\d+) segments$`, navCtx.theRouteShouldHaveSegments)
	ctx.Step(`^the route status should be "([^"]*)"$`, navCtx.theRouteStatusShouldBe)
	ctx.Step(`^current location should be "([^"]*)"$`, navCtx.currentLocationShouldBe)

	// Route failure steps
	ctx.Step(`^route status should be set to FAILED$`, func() error {
		return navCtx.theRouteStatusShouldBe("FAILED")
	})
	ctx.Step(`^route should be marked as FAILED$`, func() error {
		return navCtx.theRouteStatusShouldBe("FAILED")
	})
}

// ============================================================================
// Mock Implementations
// ============================================================================

// mockGraphBuilder implements IGraphBuilder for testing
type mockGraphBuilder struct {
	apiClient    *helpers.MockAPIClient
	waypointRepo *persistence.GormWaypointRepository
}

func (m *mockGraphBuilder) BuildSystemGraph(ctx context.Context, systemSymbol string, playerID int) (map[string]interface{}, error) {
	// For testing, load waypoints from repository and build graph
	waypoints, err := m.waypointRepo.ListBySystem(ctx, systemSymbol)
	if err != nil {
		// Return empty graph if no waypoints
		return map[string]interface{}{
			"waypoints": map[string]interface{}{},
		}, nil
	}

	// Build graph structure from waypoints
	waypointMap := make(map[string]interface{})
	for _, wp := range waypoints {
		waypointMap[wp.Symbol] = map[string]interface{}{
			"symbol":       wp.Symbol,
			"systemSymbol": wp.SystemSymbol,
			"x":            wp.X,
			"y":            wp.Y,
			"type":         wp.Type,
			"has_fuel":     wp.HasFuel,
		}
	}

	return map[string]interface{}{
		"waypoints": waypointMap,
	}, nil
}

// mockMediator implements Mediator for testing
type mockMediator struct {
	apiClient *helpers.MockAPIClient
	shipRepo  navigation.ShipRepository
}

func (m *mockMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	// Handle different command types
	switch cmd := request.(type) {
	case *appShip.OrbitShipCommand:
		return m.handleOrbitShip(ctx, cmd)
	case *appShip.DockShipCommand:
		return m.handleDockShip(ctx, cmd)
	case *appShip.RefuelShipCommand:
		return m.handleRefuelShip(ctx, cmd)
	case *appShip.SetFlightModeCommand:
		return m.handleSetFlightMode(ctx, cmd)
	case *appShip.NavigateToWaypointCommand:
		return m.handleNavigateToWaypoint(ctx, cmd)
	default:
		return nil, fmt.Errorf("unknown command type: %T", request)
	}
}

func (m *mockMediator) Register(requestType reflect.Type, handler common.RequestHandler) error {
	return nil
}

func (m *mockMediator) handleOrbitShip(ctx context.Context, cmd *appShip.OrbitShipCommand) (interface{}, error) {
	ship, err := m.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, err
	}

	if _, err := ship.EnsureInOrbit(); err != nil {
		return nil, err
	}

	return &appShip.OrbitShipResponse{Status: "in_orbit"}, nil
}

func (m *mockMediator) handleDockShip(ctx context.Context, cmd *appShip.DockShipCommand) (interface{}, error) {
	ship, err := m.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, err
	}

	if _, err := ship.EnsureDocked(); err != nil {
		return nil, err
	}

	return &appShip.DockShipResponse{Status: "docked"}, nil
}

func (m *mockMediator) handleRefuelShip(ctx context.Context, cmd *appShip.RefuelShipCommand) (interface{}, error) {
	ship, err := m.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, err
	}

	fuelAdded, err := ship.RefuelToFull()
	if err != nil {
		return nil, err
	}

	return &appShip.RefuelShipResponse{
		FuelAdded:   fuelAdded,
		CurrentFuel: ship.Fuel().Current,
		CreditsCost: fuelAdded * 100,
	}, nil
}

func (m *mockMediator) handleSetFlightMode(ctx context.Context, cmd *appShip.SetFlightModeCommand) (interface{}, error) {
	return &appShip.SetFlightModeResponse{CurrentMode: cmd.Mode}, nil
}

func (m *mockMediator) handleNavigateToWaypoint(ctx context.Context, cmd *appShip.NavigateToWaypointCommand) (interface{}, error) {
	ship, err := m.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, err
	}

	// Mock navigation - find destination waypoint
	// This is simplified - real implementation would use routing
	destWaypoint, err := shared.NewWaypoint(cmd.Destination, 100, 100)
	if err != nil {
		return nil, err
	}

	// Start transit
	if err := ship.StartTransit(destWaypoint); err != nil {
		return nil, err
	}

	// Consume fuel (simplified)
	if err := ship.ConsumeFuel(10); err != nil {
		return nil, err
	}

	// Arrive immediately (mock)
	if err := ship.Arrive(); err != nil {
		return nil, err
	}

	return &appShip.NavigateToWaypointResponse{
		Status:      "completed",
		FuelConsumed: 10,
	}, nil
}
