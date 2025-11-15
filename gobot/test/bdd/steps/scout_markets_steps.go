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
	"github.com/andrescamacho/spacetraders-go/internal/application/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/test/helpers"
)

type scoutMarketsContext struct {
	// Database and repositories
	db                 *gorm.DB
	apiClient          *helpers.MockAPIClient
	shipRepo           navigation.ShipRepository
	playerRepo         *persistence.GormPlayerRepository
	waypointRepo       *persistence.GormWaypointRepository
	mockPlayerRepo     *helpers.MockPlayerRepository
	mockRoutingClient  *helpers.MockRoutingClient
	mockDaemonClient   *helpers.MockDaemonClient
	mockGraphProvider  *helpers.MockGraphProvider

	// Test state
	player      *player.Player
	ships       map[string]*navigation.Ship
	waypoints   map[string]*shared.Waypoint
	handler     *scouting.ScoutMarketsHandler
	command     *scouting.ScoutMarketsCommand
	response    *scouting.ScoutMarketsResponse
	err         error
	systemGraph map[string]map[string]float64
}

func (c *scoutMarketsContext) reset() error {
	// Create in-memory SQLite database
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("failed to open test database: %w", err)
	}

	// Auto-migrate the models
	err = db.AutoMigrate(
		&persistence.PlayerModel{},
		&persistence.WaypointModel{},
	)
	if err != nil {
		return fmt.Errorf("failed to migrate database: %w", err)
	}

	c.db = db
	c.apiClient = helpers.NewMockAPIClient()
	c.playerRepo = persistence.NewGormPlayerRepository(db)
	c.waypointRepo = persistence.NewGormWaypointRepository(db)
	c.shipRepo = api.NewAPIShipRepository(c.apiClient, c.playerRepo, c.waypointRepo)
	c.mockPlayerRepo = helpers.NewMockPlayerRepository()
	c.mockRoutingClient = helpers.NewMockRoutingClient()
	c.mockDaemonClient = helpers.NewMockDaemonClient()
	c.mockGraphProvider = helpers.NewMockGraphProvider()

	c.ships = make(map[string]*navigation.Ship)
	c.waypoints = make(map[string]*shared.Waypoint)
	c.systemGraph = make(map[string]map[string]float64)
	c.response = nil
	c.err = nil

	return nil
}

// Background steps - Scout Markets specific

func (c *scoutMarketsContext) aScoutMarketsTestDatabase() error {
	// Already set up in reset()
	return nil
}

func (c *scoutMarketsContext) aScoutMarketsPlayerWithIDAndAgent(playerID int, agentSymbol string) error {
	c.player = player.NewPlayer(playerID, agentSymbol, "")
	c.mockPlayerRepo.AddPlayer(c.player)

	// Persist to database
	playerModel := &persistence.PlayerModel{
		PlayerID:    playerID,
		AgentSymbol: agentSymbol,
		Token:       "test-token",
	}
	if err := c.db.Create(playerModel).Error; err != nil {
		return fmt.Errorf("failed to create player in database: %w", err)
	}

	return nil
}

func (c *scoutMarketsContext) theScoutMarketsPlayerHasToken(token string) error {
	c.player = player.NewPlayer(c.player.ID, c.player.AgentSymbol, token)
	c.mockPlayerRepo.AddPlayer(c.player)
	return nil
}

// ensureWaypointExists ensures a waypoint exists in the repository
func (c *scoutMarketsContext) ensureWaypointExists(waypoint *shared.Waypoint) error {
	// Extract system symbol from waypoint symbol
	systemSymbol := shared.ExtractSystemSymbol(waypoint.Symbol)
	waypoint.SystemSymbol = systemSymbol

	// Check if waypoint already exists
	_, err := c.waypointRepo.FindBySymbol(context.Background(), waypoint.Symbol, systemSymbol)
	if err == nil {
		return nil // Waypoint already exists
	}

	// Save waypoint to repository
	return c.waypointRepo.Save(context.Background(), waypoint)
}

// Given steps

func (c *scoutMarketsContext) iHaveAShipAtWaypointInSystem(shipSymbol, waypointSymbol, systemSymbol string) error {
	// Ensure player is initialized
	if c.player == nil {
		return fmt.Errorf("player must be initialized before creating ships")
	}

	// Create waypoint if not exists
	if _, exists := c.waypoints[waypointSymbol]; !exists {
		waypoint, err := shared.NewWaypoint(waypointSymbol, 0, 0)
		if err != nil {
			return fmt.Errorf("failed to create waypoint: %w", err)
		}
		c.waypoints[waypointSymbol] = waypoint

		// Ensure waypoint exists in repository
		if err := c.ensureWaypointExists(waypoint); err != nil {
			return fmt.Errorf("failed to persist waypoint: %w", err)
		}
	}

	// Create fuel
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		return fmt.Errorf("failed to create fuel: %w", err)
	}

	// Create cargo
	cargo, err := shared.NewCargo(100, 0, []*shared.CargoItem{})
	if err != nil {
		return fmt.Errorf("failed to create cargo: %w", err)
	}

	// Create ship
	ship, err := navigation.NewShip(
		shipSymbol,
		c.player.ID,
		c.waypoints[waypointSymbol],
		fuel,
		100,   // fuel capacity
		100,   // cargo capacity
		cargo,
		50,    // engine speed
		"FRAME_EXPLORER",
		navigation.NavStatusInOrbit, // initial status
	)
	if err != nil {
		return fmt.Errorf("failed to create ship: %w", err)
	}

	c.ships[shipSymbol] = ship
	c.apiClient.AddShip(ship)

	return nil
}

func (c *scoutMarketsContext) theSystemHasMarketsAt(systemSymbol, marketsCsv string) error {
	markets := strings.Split(marketsCsv, ",")

	// Create waypoints for each market
	for _, market := range markets {
		market = strings.TrimSpace(market)
		if _, exists := c.waypoints[market]; !exists {
			// Simple grid layout for testing
			x := float64(len(c.waypoints) * 100)
			y := float64(len(c.waypoints) * 100)
			waypoint, err := shared.NewWaypoint(market, x, y)
			if err != nil {
				return fmt.Errorf("failed to create waypoint %s: %w", market, err)
			}
			c.waypoints[market] = waypoint
		}
	}

	// Build a simple distance graph for testing
	waypointList := make([]string, 0, len(c.waypoints))
	for wp := range c.waypoints {
		waypointList = append(waypointList, wp)
	}

	for _, wp1 := range waypointList {
		if _, exists := c.systemGraph[wp1]; !exists {
			c.systemGraph[wp1] = make(map[string]float64)
		}
		for _, wp2 := range waypointList {
			if wp1 != wp2 {
				// Simple Euclidean distance
				dist := c.waypoints[wp1].DistanceTo(c.waypoints[wp2])
				c.systemGraph[wp1][wp2] = dist
			}
		}
	}

	// Configure mock graph provider
	c.mockGraphProvider.SetGraph(systemSymbol, c.systemGraph)

	return nil
}

func (c *scoutMarketsContext) shipHasAnActiveContainer(shipSymbol, containerID string) error {
	c.mockDaemonClient.AddContainer(helpers.Container{
		ID:       containerID,
		PlayerID: uint(c.player.ID),
		Status:   "RUNNING",
		Type:     "scout-tour",
	})
	return nil
}

func (c *scoutMarketsContext) theRoutingClientWillPartitionMarketsUsingVRP() error {
	// Mock VRP will be configured automatically when called
	// The mock routing client will return balanced assignments
	return nil
}

// When steps

func (c *scoutMarketsContext) iExecuteScoutMarketsCommandWithShipsAndMarketsInSystem(shipsCsv, marketsCsv, systemSymbol string) error {
	ships := strings.Split(shipsCsv, ",")
	for i := range ships {
		ships[i] = strings.TrimSpace(ships[i])
	}

	markets := strings.Split(marketsCsv, ",")
	for i := range markets {
		markets[i] = strings.TrimSpace(markets[i])
	}

	c.command = &scouting.ScoutMarketsCommand{
		PlayerID:     uint(c.player.ID),
		ShipSymbols:  ships,
		SystemSymbol: systemSymbol,
		Markets:      markets,
		Iterations:   -1, // Infinite by default
	}

	// Create handler
	c.handler = scouting.NewScoutMarketsHandler(
		c.shipRepo,
		c.mockGraphProvider,
		c.mockRoutingClient,
		c.mockDaemonClient,
	)

	// Execute command
	ctx := context.Background()
	resp, err := c.handler.Handle(ctx, c.command)
	c.err = err
	if err == nil && resp != nil {
		c.response, _ = resp.(*scouting.ScoutMarketsResponse)
	}

	return nil
}

// Then steps

func (c *scoutMarketsContext) theScoutMarketsCommandShouldSucceed() error {
	if c.err != nil {
		return fmt.Errorf("scout markets command should succeed but got error: %w", c.err)
	}
	if c.response == nil {
		return fmt.Errorf("scout markets response should not be nil")
	}
	return nil
}

func (c *scoutMarketsContext) scoutContainersShouldBeCreated(expectedCount int) error {
	createdContainers := c.mockDaemonClient.GetCreatedContainers()
	if len(createdContainers) != expectedCount {
		return fmt.Errorf("expected %d scout containers to be created, got %d", expectedCount, len(createdContainers))
	}
	return nil
}

func (c *scoutMarketsContext) newScoutContainersShouldBeCreated(expectedCount int) error {
	createdContainers := c.mockDaemonClient.GetCreatedContainers()
	if len(createdContainers) != expectedCount {
		return fmt.Errorf("expected %d new scout containers, got %d", expectedCount, len(createdContainers))
	}
	return nil
}

func (c *scoutMarketsContext) scoutContainersShouldBeReused(expectedCount int) error {
	reusedContainers := c.response.ReusedContainers
	if len(reusedContainers) != expectedCount {
		return fmt.Errorf("expected %d scout containers to be reused, got %d", expectedCount, len(reusedContainers))
	}
	return nil
}

func (c *scoutMarketsContext) shipShouldBeAssignedMarkets(shipSymbol string, expectedCount int) error {
	markets, exists := c.response.Assignments[shipSymbol]
	if !exists {
		return fmt.Errorf("ship %s should have market assignments", shipSymbol)
	}
	if len(markets) != expectedCount {
		return fmt.Errorf("ship %s should be assigned %d markets, got %d", shipSymbol, expectedCount, len(markets))
	}
	return nil
}

func (c *scoutMarketsContext) marketsShouldBeDistributedAcrossShips() error {
	// Verify that multiple ships have assignments
	if len(c.response.Assignments) <= 1 {
		return fmt.Errorf("should have assignments for multiple ships, got %d", len(c.response.Assignments))
	}

	// Verify at least 1 ship has non-empty assignments
	shipsWithMarkets := 0
	for ship, markets := range c.response.Assignments {
		if len(markets) > 0 {
			shipsWithMarkets++
			fmt.Printf("Ship %s assigned %d markets\n", ship, len(markets))
		}
	}

	if shipsWithMarkets < 1 {
		return fmt.Errorf("at least one ship should have market assignments, got %d", shipsWithMarkets)
	}
	return nil
}

func (c *scoutMarketsContext) theReusedContainerIs(expectedContainerID string) error {
	found := false
	for _, containerID := range c.response.ReusedContainers {
		if containerID == expectedContainerID {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("reused containers should include %s, got %v", expectedContainerID, c.response.ReusedContainers)
	}
	return nil
}

func (c *scoutMarketsContext) shipShouldHaveANewScoutContainer(shipSymbol string) error {
	// Check that ship is in assignments
	_, exists := c.response.Assignments[shipSymbol]
	if !exists {
		return fmt.Errorf("ship %s should be in assignments", shipSymbol)
	}

	// Check that at least one container was created
	createdContainers := c.mockDaemonClient.GetCreatedContainers()
	if len(createdContainers) == 0 {
		return fmt.Errorf("at least one scout container should be created")
	}

	// Verify container ID contains ship symbol
	foundContainer := false
	for _, containerID := range createdContainers {
		if strings.Contains(strings.ToLower(containerID), strings.ToLower(shipSymbol)) {
			foundContainer = true
			break
		}
	}
	if !foundContainer {
		return fmt.Errorf("should find scout container for ship %s in %v", shipSymbol, createdContainers)
	}

	return nil
}

func (c *scoutMarketsContext) allRequestedShipsHaveContainers() error {
	// All ships in command should be in response assignments
	for _, shipSymbol := range c.command.ShipSymbols {
		_, exists := c.response.Assignments[shipSymbol]
		if !exists {
			return fmt.Errorf("ship %s should have container assignment", shipSymbol)
		}
	}
	return nil
}

// Register steps
func InitializeScoutMarketsScenario(ctx *godog.ScenarioContext) {
	c := &scoutMarketsContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		return ctx, c.reset()
	})

	// Background - Scout Markets specific
	ctx.Step(`^a scout markets test database$`, c.aScoutMarketsTestDatabase)
	ctx.Step(`^a scout markets player with ID (\d+) and agent "([^"]*)"$`, c.aScoutMarketsPlayerWithIDAndAgent)
	ctx.Step(`^the scout markets player has token "([^"]*)"$`, c.theScoutMarketsPlayerHasToken)

	// Given
	ctx.Step(`^I have a ship "([^"]*)" at waypoint "([^"]*)" in system "([^"]*)"$`, c.iHaveAShipAtWaypointInSystem)
	ctx.Step(`^the system "([^"]*)" has markets at "([^"]*)"$`, c.theSystemHasMarketsAt)
	ctx.Step(`^ship "([^"]*)" has an active container "([^"]*)"$`, c.shipHasAnActiveContainer)
	ctx.Step(`^the routing client will partition markets using VRP$`, c.theRoutingClientWillPartitionMarketsUsingVRP)

	// When
	ctx.Step(`^I execute ScoutMarketsCommand with ships "([^"]*)" and markets "([^"]*)" in system "([^"]*)"$`, c.iExecuteScoutMarketsCommandWithShipsAndMarketsInSystem)

	// Then - Scout-markets-specific steps
	ctx.Step(`^the scout markets command should succeed$`, c.theScoutMarketsCommandShouldSucceed)
	ctx.Step(`^(\d+) scout containers? should be created$`, c.scoutContainersShouldBeCreated)
	ctx.Step(`^(\d+) new scout containers? should be created$`, c.newScoutContainersShouldBeCreated)
	ctx.Step(`^(\d+) scout containers? should be reused$`, c.scoutContainersShouldBeReused)
	ctx.Step(`^ship "([^"]*)" should be assigned (\d+) markets$`, c.shipShouldBeAssignedMarkets)
	ctx.Step(`^markets should be distributed across ships$`, c.marketsShouldBeDistributedAcrossShips)
	ctx.Step(`^the reused container is "([^"]*)"$`, c.theReusedContainerIs)
	ctx.Step(`^ship "([^"]*)" should have a new scout container$`, c.shipShouldHaveANewScoutContainer)
	ctx.Step(`^all requested ships have containers$`, c.allRequestedShipsHaveContainers)
}
