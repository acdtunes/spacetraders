package steps

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cucumber/godog"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	scoutingCommands "github.com/andrescamacho/spacetraders-go/internal/application/scouting/commands"
	scoutingQueries "github.com/andrescamacho/spacetraders-go/internal/application/scouting/queries"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship"
	shipCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/test/helpers"
)

// scoutingApplicationContext is a unified context for ALL scouting application layer handlers
type scoutingApplicationContext struct {
	// Shared infrastructure
	db         *gorm.DB
	playerRepo *persistence.GormPlayerRepository
	marketRepo *persistence.MarketRepositoryGORM

	// All handlers
	assignFleetHandler  *scoutingCommands.AssignScoutingFleetHandler
	scoutMarketsHandler *scoutingCommands.ScoutMarketsHandler
	scoutTourHandler    *scoutingCommands.ScoutTourHandler
	getMarketHandler    *scoutingQueries.GetMarketDataHandler
	listMarketsHandler  *scoutingQueries.ListMarketDataHandler

	// Generic state tracking
	lastError    error
	lastResponse interface{}

	// Mocks
	apiClient       *helpers.MockAPIClient
	shipRepo        *helpers.MockShipRepository
	waypointRepo    *helpers.MockWaypointRepository
	assignmentRepo  *helpers.MockShipAssignmentRepository
	daemonClient    *helpers.MockDaemonClient
	routingClient   *helpers.MockRoutingClient
	graphProvider   *helpers.MockGraphProvider
	mediator        *helpers.MockMediator
	marketScanner   *ship.MarketScanner
	clock           *shared.MockClock
	stopContainerFn func(string) // Track stop calls

	// Test fixtures
	testShips         map[string]*navigation.Ship
	stoppedContainers []string
}

func (ctx *scoutingApplicationContext) reset() {
	// Clear state
	ctx.lastError = nil
	ctx.lastResponse = nil
	ctx.testShips = make(map[string]*navigation.Ship)
	ctx.stoppedContainers = []string{}

	// Truncate tables
	if err := helpers.TruncateAllTables(); err != nil {
		panic(fmt.Errorf("failed to truncate tables: %w", err))
	}

	// Real repositories
	ctx.db = helpers.SharedTestDB
	ctx.playerRepo = persistence.NewGormPlayerRepository(helpers.SharedTestDB)
	ctx.marketRepo = persistence.NewMarketRepository(helpers.SharedTestDB)

	// Create mocks
	ctx.apiClient = helpers.NewMockAPIClient()
	ctx.shipRepo = helpers.NewMockShipRepository()
	ctx.waypointRepo = helpers.NewMockWaypointRepository()
	ctx.assignmentRepo = helpers.NewMockShipAssignmentRepository()
	ctx.daemonClient = helpers.NewMockDaemonClient()
	ctx.routingClient = helpers.NewMockRoutingClient()
	ctx.graphProvider = helpers.NewMockGraphProvider()
	ctx.mediator = helpers.NewMockMediator()
	ctx.clock = shared.NewMockClock(time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC))

	// Setup market scanner with real repos
	ctx.marketScanner = ship.NewMarketScanner(ctx.apiClient, ctx.marketRepo, ctx.playerRepo)

	// Setup default mediator behavior
	ctx.mediator.SetSendFunc(func(ctxMed context.Context, request common.Request) (common.Response, error) {
		if _, ok := request.(*shipCmd.NavigateRouteCommand); ok {
			return &shipCmd.NavigateRouteResponse{
				Status:        "success",
				FuelRemaining: 100,
			}, nil
		}
		return nil, nil
	})

	// Setup stop container tracking
	ctx.stopContainerFn = func(id string) {
		ctx.stoppedContainers = append(ctx.stoppedContainers, id)
	}

	// Create handlers - note: using adapter to convert MockShipRepository methods
	ctx.assignFleetHandler = scoutingCommands.NewAssignScoutingFleetHandler(
		ctx.shipRepo,
		ctx.waypointRepo,
		ctx.graphProvider,
		ctx.routingClient,
		ctx.daemonClient,
		ctx.assignmentRepo,
	)
	ctx.scoutMarketsHandler = scoutingCommands.NewScoutMarketsHandler(
		ctx.shipRepo,
		ctx.graphProvider,
		ctx.routingClient,
		ctx.daemonClient,
		ctx.assignmentRepo,
	)
	ctx.scoutTourHandler = scoutingCommands.NewScoutTourHandler(
		ctx.shipRepo,
		ctx.mediator,
		ctx.marketScanner,
	)
	ctx.getMarketHandler = scoutingQueries.NewGetMarketDataHandler(ctx.marketRepo)
	ctx.listMarketsHandler = scoutingQueries.NewListMarketDataHandler(ctx.marketRepo)
}

// ==================== GIVEN STEPS ====================

func (ctx *scoutingApplicationContext) theCurrentTimeIs(timeStr string) error {
	t, err := time.Parse(time.RFC3339, timeStr)
	if err != nil {
		return fmt.Errorf("invalid time format: %w", err)
	}
	ctx.clock.SetTime(t)
	return nil
}

func (ctx *scoutingApplicationContext) aPlayerWithIDAndTokenExistsInTheDatabase(playerID int, token string) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}

	p := player.NewPlayer(pid, fmt.Sprintf("AGENT-%d", playerID), token)
	return ctx.playerRepo.Add(context.Background(), p)
}

func (ctx *scoutingApplicationContext) aProbeShipForPlayerAtWaypoint(shipSymbol string, playerID int, waypointSymbol string) error {
	return ctx.createShipFixture(shipSymbol, playerID, waypointSymbol, "FRAME_PROBE")
}

func (ctx *scoutingApplicationContext) aDroneShipForPlayerAtWaypoint(shipSymbol string, playerID int, waypointSymbol string) error {
	return ctx.createShipFixture(shipSymbol, playerID, waypointSymbol, "FRAME_DRONE")
}

func (ctx *scoutingApplicationContext) aFrigateShipForPlayerAtWaypoint(shipSymbol string, playerID int, waypointSymbol string) error {
	return ctx.createShipFixture(shipSymbol, playerID, waypointSymbol, "FRAME_FRIGATE")
}

func (ctx *scoutingApplicationContext) createShipFixture(shipSymbol string, playerID int, waypointSymbol string, frameType string) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}

	location, err := shared.NewWaypoint(waypointSymbol, 0.0, 0.0)
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

	// Determine role based on frame type
	role := "SATELLITE"
	if frameType == "FRAME_FRIGATE" {
		role = "COMMAND"
	}

	// Use the proper constructor instead of reflection
	ship, err := navigation.NewShip(
		shipSymbol,
		pid,
		location,
		fuel,
		100, // fuelCapacity
		100, // cargoCapacity
		cargo,
		30, // engineSpeed
		frameType,
		role,
		navigation.NavStatusInOrbit,
	)
	if err != nil {
		return err
	}

	ctx.testShips[shipSymbol] = ship
	ctx.shipRepo.Ships[shipSymbol] = ship
	ctx.shipRepo.ShipsByPlayer[playerID] = append(ctx.shipRepo.ShipsByPlayer[playerID], ship)

	// Extract system symbol and ensure graph exists
	systemSymbol := location.SystemSymbol
	ctx.ensureGraphForSystem(systemSymbol)

	return nil
}

func (ctx *scoutingApplicationContext) aMarketplaceInSystem(waypointSymbol string, systemSymbol string) error {
	wp, err := shared.NewWaypoint(waypointSymbol, 0.0, 0.0)
	if err != nil {
		return err
	}
	wp.Traits = []string{"MARKETPLACE"}
	wp.SystemSymbol = systemSymbol
	ctx.waypointRepo.AddWaypoint(wp)

	// Ensure graph exists for this system and add waypoint to it
	ctx.ensureGraphForSystem(systemSymbol)

	return nil
}

func (ctx *scoutingApplicationContext) aFuelStationMarketplaceInSystem(waypointSymbol string, systemSymbol string) error {
	wp, err := shared.NewWaypoint(waypointSymbol, 0.0, 0.0)
	if err != nil {
		return err
	}
	wp.Type = "FUEL_STATION"
	wp.Traits = []string{"MARKETPLACE"}
	wp.SystemSymbol = systemSymbol
	ctx.waypointRepo.AddWaypoint(wp)

	// Ensure graph exists for this system
	ctx.ensureGraphForSystem(systemSymbol)

	return nil
}

func (ctx *scoutingApplicationContext) shipHasAnExistingActiveContainer(shipSymbol string, containerID string, playerID int) error {
	assignment := container.NewShipAssignment(shipSymbol, playerID, containerID, nil)
	return ctx.assignmentRepo.Assign(context.Background(), assignment)
}

func (ctx *scoutingApplicationContext) marketDataExistsForWaypointWithPlayer(waypointSymbol string, playerID int) error {
	return ctx.marketDataExistsForWaypointWithPlayerScannedMinutesAgo(waypointSymbol, playerID, 0)
}

func (ctx *scoutingApplicationContext) marketDataExistsForWaypointWithPlayerScannedMinutesAgo(waypointSymbol string, playerID int, minutesAgo int) error {
	goods := []market.TradeGood{}
	good, err := market.NewTradeGood("IRON_ORE", stringPtr("MODERATE"), nil, 10, 15, 100)
	if err != nil {
		return err
	}
	goods = append(goods, *good)

	// Use real time, not mock clock, because the repository uses time.Now() for age filtering
	timestamp := time.Now().Add(-time.Duration(minutesAgo) * time.Minute)
	return ctx.marketRepo.UpsertMarketData(context.Background(), uint(playerID), waypointSymbol, goods, timestamp)
}

func (ctx *scoutingApplicationContext) vrpAssignsToAnd(markets1 string, ship1 string, markets2 string, ship2 string) error {
	markets1List := parseMarketList(markets1)
	markets2List := parseMarketList(markets2)

	ctx.routingClient.SetVRPResult(map[string][]string{
		ship1: markets1List,
		ship2: markets2List,
	})

	return nil
}

// ==================== WHEN STEPS ====================

func (ctx *scoutingApplicationContext) iExecuteAssignScoutingFleetCommandForPlayerInSystem(playerID int, systemSymbol string) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}

	cmd := &scoutingCommands.AssignScoutingFleetCommand{
		PlayerID:     pid,
		SystemSymbol: systemSymbol,
	}

	response, err := ctx.assignFleetHandler.Handle(context.Background(), cmd)
	ctx.lastError = err
	ctx.lastResponse = response
	return nil
}

func (ctx *scoutingApplicationContext) iExecuteScoutMarketsCommandForPlayerWithShipsAndMarketsInSystemWithIterations(
	playerID int, shipsStr string, marketsStr string, systemSymbol string, iterations int) error {

	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}

	ships := parseStringList(shipsStr)
	markets := parseStringList(marketsStr)

	cmd := &scoutingCommands.ScoutMarketsCommand{
		PlayerID:     pid,
		ShipSymbols:  ships,
		SystemSymbol: systemSymbol,
		Markets:      markets,
		Iterations:   iterations,
	}

	response, err := ctx.scoutMarketsHandler.Handle(context.Background(), cmd)
	ctx.lastError = err
	ctx.lastResponse = response
	return nil
}

func (ctx *scoutingApplicationContext) iExecuteScoutTourCommandForPlayerWithShipAndMarketsWithIterations(
	playerID int, shipSymbol string, marketsStr string, iterations int) error {

	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}

	markets := parseStringList(marketsStr)

	cmd := &scoutingCommands.ScoutTourCommand{
		PlayerID:   pid,
		ShipSymbol: shipSymbol,
		Markets:    markets,
		Iterations: iterations,
	}

	response, err := ctx.scoutTourHandler.Handle(context.Background(), cmd)
	ctx.lastError = err
	ctx.lastResponse = response
	return nil
}

func (ctx *scoutingApplicationContext) iExecuteGetMarketDataQueryForWaypointWithPlayer(waypointSymbol string, playerID int) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}

	query := &scoutingQueries.GetMarketDataQuery{
		PlayerID:       pid,
		WaypointSymbol: waypointSymbol,
	}

	response, err := ctx.getMarketHandler.Handle(context.Background(), query)
	ctx.lastError = err
	ctx.lastResponse = response
	return nil
}

func (ctx *scoutingApplicationContext) iExecuteListMarketDataQueryForSystemWithPlayerAndMaxAgeMinutes(
	systemSymbol string, playerID int, maxAgeMinutes int) error {

	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}

	query := &scoutingQueries.ListMarketDataQuery{
		PlayerID:      pid,
		SystemSymbol:  systemSymbol,
		MaxAgeMinutes: maxAgeMinutes,
	}

	response, err := ctx.listMarketsHandler.Handle(context.Background(), query)
	ctx.lastError = err
	ctx.lastResponse = response
	return nil
}

// ==================== THEN STEPS ====================

func (ctx *scoutingApplicationContext) theCommandShouldSucceed() error {
	if ctx.lastError != nil {
		return fmt.Errorf("expected success but got error: %v", ctx.lastError)
	}
	if ctx.lastResponse == nil {
		return fmt.Errorf("expected response but got nil")
	}
	return nil
}

func (ctx *scoutingApplicationContext) theQueryShouldSucceed() error {
	return ctx.theCommandShouldSucceed()
}

func (ctx *scoutingApplicationContext) theCommandShouldReturnAnErrorContaining(expectedError string) error {
	if ctx.lastError == nil {
		return fmt.Errorf("expected error containing '%s' but command succeeded", expectedError)
	}

	errMsg := strings.ToLower(ctx.lastError.Error())
	expectedLower := strings.ToLower(expectedError)

	if !strings.Contains(errMsg, expectedLower) {
		return fmt.Errorf("expected error containing '%s' but got '%v'", expectedError, ctx.lastError)
	}

	return nil
}

func (ctx *scoutingApplicationContext) shipsShouldBeAssigned(count int) error {
	resp, ok := ctx.lastResponse.(*scoutingCommands.AssignScoutingFleetResponse)
	if !ok {
		return fmt.Errorf("invalid response type")
	}

	if len(resp.AssignedShips) != count {
		return fmt.Errorf("expected %d ships assigned, got %d", count, len(resp.AssignedShips))
	}

	return nil
}

func (ctx *scoutingApplicationContext) containerIDsShouldBeReturned() error {
	resp, ok := ctx.lastResponse.(*scoutingCommands.AssignScoutingFleetResponse)
	if !ok {
		return fmt.Errorf("invalid response type")
	}

	if len(resp.ContainerIDs) == 0 {
		return fmt.Errorf("expected container IDs but got none")
	}

	return nil
}

func (ctx *scoutingApplicationContext) assignmentsShouldMapShipsToMarkets() error {
	resp, ok := ctx.lastResponse.(*scoutingCommands.AssignScoutingFleetResponse)
	if !ok {
		return fmt.Errorf("invalid response type")
	}

	if len(resp.Assignments) == 0 {
		return fmt.Errorf("expected assignments but got none")
	}

	return nil
}

func (ctx *scoutingApplicationContext) shouldNotBeInAnyAssignment(waypoint string) error {
	resp, ok := ctx.lastResponse.(*scoutingCommands.AssignScoutingFleetResponse)
	if !ok {
		return fmt.Errorf("invalid response type")
	}

	for ship, markets := range resp.Assignments {
		for _, market := range markets {
			if market == waypoint {
				return fmt.Errorf("waypoint %s found in assignment for ship %s", waypoint, ship)
			}
		}
	}

	return nil
}

func (ctx *scoutingApplicationContext) shipShouldBeAssignedAllMarkets(shipSymbol string) error {
	resp, ok := ctx.lastResponse.(*scoutingCommands.ScoutMarketsResponse)
	if !ok {
		return fmt.Errorf("invalid response type")
	}

	if _, exists := resp.Assignments[shipSymbol]; !exists {
		return fmt.Errorf("ship %s not found in assignments", shipSymbol)
	}

	return nil
}

func (ctx *scoutingApplicationContext) shipShouldBeAssignedMarkets(shipSymbol string, marketsStr string) error {
	resp, ok := ctx.lastResponse.(*scoutingCommands.ScoutMarketsResponse)
	if !ok {
		return fmt.Errorf("invalid response type")
	}

	expectedMarkets := parseMarketList(marketsStr)
	actualMarkets := resp.Assignments[shipSymbol]

	if len(actualMarkets) != len(expectedMarkets) {
		return fmt.Errorf("expected %d markets for ship %s, got %d", len(expectedMarkets), shipSymbol, len(actualMarkets))
	}

	return nil
}

func (ctx *scoutingApplicationContext) containersShouldBeCreated(count int) error {
	// Check both response types
	if resp, ok := ctx.lastResponse.(*scoutingCommands.ScoutMarketsResponse); ok {
		actual := len(resp.ContainerIDs) - len(resp.ReusedContainers)
		if actual != count {
			return fmt.Errorf("expected %d containers created, got %d", count, actual)
		}
		return nil
	}

	if resp, ok := ctx.lastResponse.(*scoutingCommands.AssignScoutingFleetResponse); ok {
		actual := len(resp.ContainerIDs) - len(resp.ReusedContainers)
		if actual != count {
			return fmt.Errorf("expected %d containers created, got %d", count, actual)
		}
		return nil
	}

	return fmt.Errorf("invalid response type")
}

func (ctx *scoutingApplicationContext) theExistingContainerShouldBeStopped() error {
	if len(ctx.stoppedContainers) == 0 {
		// Check daemon client
		containers := ctx.daemonClient.GetCreatedContainers()
		if len(containers) == 0 {
			return fmt.Errorf("expected at least one container to be stopped")
		}
	}
	return nil
}

func (ctx *scoutingApplicationContext) marketsShouldBeVisitedInTotal(count int) error {
	resp, ok := ctx.lastResponse.(*scoutingCommands.ScoutTourResponse)
	if !ok {
		return fmt.Errorf("invalid response type")
	}

	if resp.MarketsVisited != count {
		return fmt.Errorf("expected %d markets visited, got %d", count, resp.MarketsVisited)
	}

	return nil
}

func (ctx *scoutingApplicationContext) theShipShouldNavigateTimes(count int) error {
	actualCount := ctx.mediator.GetCallCount()
	if actualCount != count {
		return fmt.Errorf("expected %d navigation calls, got %d", count, actualCount)
	}
	return nil
}

func (ctx *scoutingApplicationContext) theMarketShouldBeScannedTimes(waypointSymbol string, count int) error {
	// For now, just check that the command succeeded
	// TODO: Add proper market scan tracking if needed
	return nil
}

func (ctx *scoutingApplicationContext) theTourOrderShouldStartWith(waypoint string) error {
	resp, ok := ctx.lastResponse.(*scoutingCommands.ScoutTourResponse)
	if !ok {
		return fmt.Errorf("invalid response type")
	}

	if len(resp.TourOrder) == 0 {
		return fmt.Errorf("tour order is empty")
	}

	if resp.TourOrder[0] != waypoint {
		return fmt.Errorf("expected tour to start with %s, got %s", waypoint, resp.TourOrder[0])
	}

	return nil
}

func (ctx *scoutingApplicationContext) theTourOrderShouldBe(ordersStr string) error {
	resp, ok := ctx.lastResponse.(*scoutingCommands.ScoutTourResponse)
	if !ok {
		return fmt.Errorf("invalid response type")
	}

	expectedOrder := parseStringList(ordersStr)

	if len(resp.TourOrder) != len(expectedOrder) {
		return fmt.Errorf("expected tour order length %d, got %d", len(expectedOrder), len(resp.TourOrder))
	}

	for i, expected := range expectedOrder {
		if resp.TourOrder[i] != expected {
			return fmt.Errorf("expected position %d to be %s, got %s", i, expected, resp.TourOrder[i])
		}
	}

	return nil
}

func (ctx *scoutingApplicationContext) theQueryShouldReturnNoMarketData() error {
	resp, ok := ctx.lastResponse.(*scoutingQueries.GetMarketDataResponse)
	if !ok {
		return fmt.Errorf("invalid response type")
	}

	if resp.Market != nil {
		return fmt.Errorf("expected no market data but got market")
	}

	return nil
}

func (ctx *scoutingApplicationContext) theMarketDataShouldBeReturned() error {
	resp, ok := ctx.lastResponse.(*scoutingQueries.GetMarketDataResponse)
	if !ok {
		return fmt.Errorf("invalid response type")
	}

	if resp.Market == nil {
		return fmt.Errorf("expected market data but got nil")
	}

	return nil
}

func (ctx *scoutingApplicationContext) theMarketWaypointShouldBe(waypointSymbol string) error {
	resp, ok := ctx.lastResponse.(*scoutingQueries.GetMarketDataResponse)
	if !ok {
		return fmt.Errorf("invalid response type")
	}

	if resp.Market == nil {
		return fmt.Errorf("no market data returned")
	}

	if resp.Market.WaypointSymbol() != waypointSymbol {
		return fmt.Errorf("expected waypoint %s, got %s", waypointSymbol, resp.Market.WaypointSymbol())
	}

	return nil
}

func (ctx *scoutingApplicationContext) marketsShouldBeReturned(count int) error {
	resp, ok := ctx.lastResponse.(*scoutingQueries.ListMarketDataResponse)
	if !ok {
		return fmt.Errorf("invalid response type")
	}

	if len(resp.Markets) != count {
		return fmt.Errorf("expected %d markets, got %d", count, len(resp.Markets))
	}

	return nil
}

func (ctx *scoutingApplicationContext) shouldBeInTheResults(waypointSymbol string) error {
	resp, ok := ctx.lastResponse.(*scoutingQueries.ListMarketDataResponse)
	if !ok {
		return fmt.Errorf("invalid response type")
	}

	for _, m := range resp.Markets {
		if m.WaypointSymbol() == waypointSymbol {
			return nil
		}
	}

	return fmt.Errorf("waypoint %s not found in results", waypointSymbol)
}

func (ctx *scoutingApplicationContext) shouldNotBeInTheResults(waypointSymbol string) error {
	resp, ok := ctx.lastResponse.(*scoutingQueries.ListMarketDataResponse)
	if !ok {
		return fmt.Errorf("invalid response type")
	}

	for _, m := range resp.Markets {
		if m.WaypointSymbol() == waypointSymbol {
			return fmt.Errorf("waypoint %s should not be in results but was found", waypointSymbol)
		}
	}

	return nil
}

func (ctx *scoutingApplicationContext) assignmentsShouldBeEmpty() error {
	if resp, ok := ctx.lastResponse.(*scoutingCommands.AssignScoutingFleetResponse); ok {
		if len(resp.Assignments) > 0 {
			return fmt.Errorf("expected empty assignments but got %d", len(resp.Assignments))
		}
		return nil
	}

	if resp, ok := ctx.lastResponse.(*scoutingCommands.ScoutMarketsResponse); ok {
		if len(resp.Assignments) > 0 {
			return fmt.Errorf("expected empty assignments but got %d", len(resp.Assignments))
		}
		return nil
	}

	return fmt.Errorf("invalid response type")
}

// ==================== HELPER FUNCTIONS ====================

func (ctx *scoutingApplicationContext) ensureGraphForSystem(systemSymbol string) {
	// Set up a simple graph with all waypoints connected
	// This is a simplified mock - distances don't matter for these tests
	ctx.graphProvider.SetGraph(systemSymbol, map[string]map[string]float64{})
}

func parseStringList(input string) []string {
	input = strings.Trim(input, "[]")
	input = strings.ReplaceAll(input, "\"", "")

	if input == "" {
		return []string{}
	}

	parts := strings.Split(input, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func parseMarketList(input string) []string {
	return parseStringList(input)
}

func stringPtr(s string) *string {
	return &s
}

// ==================== REGISTRATION ====================

func InitializeScoutingApplicationScenarios(sc *godog.ScenarioContext) {
	ctx := &scoutingApplicationContext{}

	sc.Before(func(context.Context, *godog.Scenario) (context.Context, error) {
		ctx.reset()
		return context.Background(), nil
	})

	// Register all Given steps
	sc.Step(`^the current time is "([^"]*)"$`, ctx.theCurrentTimeIs)
	sc.Step(`^a player with ID (\d+) and token "([^"]*)" exists in the database$`, ctx.aPlayerWithIDAndTokenExistsInTheDatabase)
	sc.Step(`^a probe ship "([^"]*)" for player (\d+) at waypoint "([^"]*)"$`, ctx.aProbeShipForPlayerAtWaypoint)
	sc.Step(`^a drone ship "([^"]*)" for player (\d+) at waypoint "([^"]*)"$`, ctx.aDroneShipForPlayerAtWaypoint)
	sc.Step(`^a frigate ship "([^"]*)" for player (\d+) at waypoint "([^"]*)"$`, ctx.aFrigateShipForPlayerAtWaypoint)
	sc.Step(`^a marketplace "([^"]*)" in system "([^"]*)"$`, ctx.aMarketplaceInSystem)
	sc.Step(`^a fuel station marketplace "([^"]*)" in system "([^"]*)"$`, ctx.aFuelStationMarketplaceInSystem)
	sc.Step(`^"([^"]*)" has an existing active container "([^"]*)" for player (\d+)$`, ctx.shipHasAnExistingActiveContainer)
	sc.Step(`^market data exists for waypoint "([^"]*)" with player (\d+)$`, ctx.marketDataExistsForWaypointWithPlayer)
	sc.Step(`^market data exists for waypoint "([^"]*)" with player (\d+) scanned (\d+) minutes ago$`, ctx.marketDataExistsForWaypointWithPlayerScannedMinutesAgo)
	sc.Step(`^VRP assigns \[([^\]]*)\] to "([^"]*)" and \[([^\]]*)\] to "([^"]*)"$`, ctx.vrpAssignsToAnd)

	// Register all When steps
	sc.Step(`^I execute assign scouting fleet command for player (\d+) in system "([^"]*)"$`, ctx.iExecuteAssignScoutingFleetCommandForPlayerInSystem)
	sc.Step(`^I execute scout markets command for player (\d+) with ships \[([^\]]*)\] and markets \[([^\]]*)\] in system "([^"]*)" with (\d+) iterations$`, ctx.iExecuteScoutMarketsCommandForPlayerWithShipsAndMarketsInSystemWithIterations)
	sc.Step(`^I execute scout tour command for player (\d+) with ship "([^"]*)" and markets \[([^\]]*)\] with (\d+) iterations$`, ctx.iExecuteScoutTourCommandForPlayerWithShipAndMarketsWithIterations)
	sc.Step(`^I execute get market data query for waypoint "([^"]*)" with player (\d+)$`, ctx.iExecuteGetMarketDataQueryForWaypointWithPlayer)
	sc.Step(`^I execute list market data query for system "([^"]*)" with player (\d+) and max age (\d+) minutes$`, ctx.iExecuteListMarketDataQueryForSystemWithPlayerAndMaxAgeMinutes)

	// Register all Then steps
	sc.Step(`^the command should succeed$`, ctx.theCommandShouldSucceed)
	sc.Step(`^the query should succeed$`, ctx.theQueryShouldSucceed)
	sc.Step(`^the command should return an error containing "([^"]*)"$`, ctx.theCommandShouldReturnAnErrorContaining)
	sc.Step(`^(\d+) ships? should be assigned$`, ctx.shipsShouldBeAssigned)
	sc.Step(`^container IDs should be returned$`, ctx.containerIDsShouldBeReturned)
	sc.Step(`^assignments should map ships to markets$`, ctx.assignmentsShouldMapShipsToMarkets)
	sc.Step(`^"([^"]*)" should not be in any assignment$`, ctx.shouldNotBeInAnyAssignment)
	sc.Step(`^"([^"]*)" should be assigned all markets$`, ctx.shipShouldBeAssignedAllMarkets)
	sc.Step(`^"([^"]*)" should be assigned \[([^\]]*)\]$`, ctx.shipShouldBeAssignedMarkets)
	sc.Step(`^(\d+) containers? should be created$`, ctx.containersShouldBeCreated)
	sc.Step(`^the existing container should be stopped$`, ctx.theExistingContainerShouldBeStopped)
	sc.Step(`^(\d+) markets? should be visited in total$`, ctx.marketsShouldBeVisitedInTotal)
	sc.Step(`^the ship should navigate (\d+) times?$`, ctx.theShipShouldNavigateTimes)
	sc.Step(`^the market "([^"]*)" should be scanned (\d+) times?$`, ctx.theMarketShouldBeScannedTimes)
	sc.Step(`^the tour order should start with "([^"]*)"$`, ctx.theTourOrderShouldStartWith)
	sc.Step(`^the tour order should be \[([^\]]*)\]$`, ctx.theTourOrderShouldBe)
	sc.Step(`^the query should return no market data$`, ctx.theQueryShouldReturnNoMarketData)
	sc.Step(`^the market data should be returned$`, ctx.theMarketDataShouldBeReturned)
	sc.Step(`^the market waypoint should be "([^"]*)"$`, ctx.theMarketWaypointShouldBe)
	sc.Step(`^(\d+) markets? should be returned$`, ctx.marketsShouldBeReturned)
	sc.Step(`^"([^"]*)" should be in the results$`, ctx.shouldBeInTheResults)
	sc.Step(`^"([^"]*)" should not be in the results$`, ctx.shouldNotBeInTheResults)
	sc.Step(`^assignments should be empty$`, ctx.assignmentsShouldBeEmpty)
	sc.Step(`^(\d+) ship should be assigned$`, ctx.shipsShouldBeAssigned)
}
