package steps

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cucumber/godog"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	infraports "github.com/andrescamacho/spacetraders-go/internal/infrastructure/ports"
	"github.com/andrescamacho/spacetraders-go/test/helpers"
)

type scoutTourContext struct {
	// Database and repositories
	db             *gorm.DB
	marketRepo     *persistence.MarketRepositoryGORM
	shipRepo       navigation.ShipRepository
	playerRepo     *persistence.GormPlayerRepository
	waypointRepo   *persistence.GormWaypointRepository
	mockPlayerRepo *helpers.MockPlayerRepository
	mockAPIClient  *helpers.MockAPIClient

	// Test state
	player     *player.Player
	ships      map[string]*navigation.Ship
	waypoints  map[string]*shared.Waypoint
	handler    *scouting.ScoutTourHandler
	command    *scouting.ScoutTourCommand
	response   *scouting.ScoutTourResponse
	err        error
	visitOrder []string // Track visit order for assertions

	// Timing
	startTime time.Time
}

func (c *scoutTourContext) reset() error {
	// Create in-memory SQLite database
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("failed to open test database: %w", err)
	}

	// Auto-migrate the models
	err = db.AutoMigrate(
		&persistence.PlayerModel{},
		&persistence.MarketData{},
		&persistence.TradeGoodData{},
	)
	if err != nil {
		return fmt.Errorf("failed to migrate database: %w", err)
	}

	c.db = db
	c.marketRepo = persistence.NewMarketRepository(db)
	c.playerRepo = persistence.NewGormPlayerRepository(db)
	c.waypointRepo = persistence.NewGormWaypointRepository(db)
	c.mockAPIClient = helpers.NewMockAPIClient()
	c.shipRepo = api.NewAPIShipRepository(c.mockAPIClient, c.playerRepo, c.waypointRepo)
	c.mockPlayerRepo = helpers.NewMockPlayerRepository()

	c.ships = make(map[string]*navigation.Ship)
	c.waypoints = make(map[string]*shared.Waypoint)
	c.response = nil
	c.err = nil
	c.visitOrder = []string{}

	return nil
}

// Background steps

func (c *scoutTourContext) aTestDatabase() error {
	// Already set up in reset()
	return nil
}

func (c *scoutTourContext) aRegisteredPlayerWithIDAndAgent(playerID int, agentSymbol string) error {
	c.player = player.NewPlayer(playerID, agentSymbol, "")
	c.mockPlayerRepo.AddPlayer(c.player)

	// Persist to database for market repository
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

func (c *scoutTourContext) thePlayerHasToken(token string) error {
	c.player.Token = token
	return nil
}

// Ship setup steps

func (c *scoutTourContext) thePlayerHasAShipAtWaypointWithStatus(shipSymbol, waypointSymbol, status string) error {
	waypoint, exists := c.waypoints[waypointSymbol]
	if !exists {
		waypoint, _ = shared.NewWaypoint(waypointSymbol, 0, 0)
		c.waypoints[waypointSymbol] = waypoint
	}

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
		shipSymbol, c.player.ID, waypoint, fuel, 100,
		40, cargo, 30, navStatus,
	)
	if err != nil {
		return err
	}

	c.ships[shipSymbol] = ship
	c.mockAPIClient.AddShip(ship)
	return nil
}

// Waypoint setup steps

func (c *scoutTourContext) theSystemHasWaypointWithCoordinates(systemSymbol, waypointSymbol string, x, y int) error {
	waypoint, err := shared.NewWaypoint(waypointSymbol, float64(x), float64(y))
	if err != nil {
		return err
	}
	c.waypoints[waypointSymbol] = waypoint
	return nil
}

func (c *scoutTourContext) theSystemHasTheFollowingWaypoints(systemSymbol string, table *godog.Table) error {
	// Skip header row
	for i := 1; i < len(table.Rows); i++ {
		row := table.Rows[i]
		if len(row.Cells) < 3 {
			return fmt.Errorf("invalid table row: expected 3 columns, got %d", len(row.Cells))
		}

		waypointSymbol := row.Cells[0].Value
		var x, y float64
		fmt.Sscanf(row.Cells[1].Value, "%f", &x)
		fmt.Sscanf(row.Cells[2].Value, "%f", &y)

		waypoint, err := shared.NewWaypoint(waypointSymbol, x, y)
		if err != nil {
			return err
		}
		c.waypoints[waypointSymbol] = waypoint
	}
	return nil
}

// Market data setup steps

func (c *scoutTourContext) theAPIWillReturnMarketDataForWithNTradeGoods(waypointSymbol string, count int) error {
	tradeGoods := make([]infraports.TradeGoodData, count)
	for i := 0; i < count; i++ {
		tradeGoods[i] = infraports.TradeGoodData{
			Symbol:        fmt.Sprintf("GOOD_%d", i+1),
			Supply:        "MODERATE",
			SellPrice:     100 + i*10,
			PurchasePrice: 150 + i*10,
			TradeVolume:   500,
		}
	}
	c.mockAPIClient.SetMarketData(waypointSymbol, tradeGoods)
	return nil
}

func (c *scoutTourContext) theAPIWillReturnMarketDataForAllWaypoints() error {
	for symbol := range c.waypoints {
		tradeGoods := []infraports.TradeGoodData{
			{
				Symbol:        "PRECIOUS_STONES",
				Supply:        "MODERATE",
				SellPrice:     100,
				PurchasePrice: 150,
				TradeVolume:   500,
			},
			{
				Symbol:        "IRON_ORE",
				Supply:        "ABUNDANT",
				SellPrice:     50,
				PurchasePrice: 75,
				TradeVolume:   1000,
			},
		}
		c.mockAPIClient.SetMarketData(symbol, tradeGoods)
	}
	return nil
}

// When steps

func (c *scoutTourContext) iExecuteScoutTourCommandWithShipMarketsAndNIteration(shipSymbol, marketsCsv string, iterations int) error {
	markets := strings.Split(marketsCsv, ",")
	for i := range markets {
		markets[i] = strings.TrimSpace(markets[i])
	}

	c.command = &scouting.ScoutTourCommand{
		PlayerID:   uint(c.player.ID),
		ShipSymbol: shipSymbol,
		Markets:    markets,
		Iterations: iterations,
	}

	// Create handler
	c.handler = scouting.NewScoutTourHandler(
		c.shipRepo,
		c.marketRepo,
		c.mockAPIClient,
		c.mockPlayerRepo,
	)

	c.startTime = time.Now()

	// Execute command
	ctx := context.Background()
	response, err := c.handler.Handle(ctx, c.command)
	c.err = err
	if err == nil {
		c.response, _ = response.(*scouting.ScoutTourResponse)
	}

	return nil
}

// Then steps

func (c *scoutTourContext) theCommandShouldSucceed() error {
	if c.err != nil {
		return fmt.Errorf("expected success but got error: %v", c.err)
	}
	if c.response == nil {
		return fmt.Errorf("response should not be nil")
	}
	return nil
}

func (c *scoutTourContext) theShipShouldBeAt(waypointSymbol string) error {
	ship, exists := c.ships[c.command.ShipSymbol]
	if !exists {
		return fmt.Errorf("ship not found in context")
	}

	if ship.CurrentLocation().Symbol != waypointSymbol {
		return fmt.Errorf("expected ship at %s but it's at %s", waypointSymbol, ship.CurrentLocation().Symbol)
	}
	return nil
}

func (c *scoutTourContext) marketDataShouldBePersistedForWaypoint(waypointSymbol string) error {
	ctx := context.Background()
	marketData, err := c.marketRepo.GetMarketData(ctx, uint(c.player.ID), waypointSymbol)
	if err != nil {
		return fmt.Errorf("failed to get market data: %w", err)
	}
	if marketData == nil {
		return fmt.Errorf("market data not found for waypoint %s", waypointSymbol)
	}
	return nil
}

func (c *scoutTourContext) theMarketShouldHaveNTradeGoods(count int) error {
	ctx := context.Background()
	marketData, err := c.marketRepo.GetMarketData(ctx, uint(c.player.ID), c.command.Markets[0])
	if err != nil {
		return fmt.Errorf("failed to get market data: %w", err)
	}
	if marketData == nil {
		return fmt.Errorf("market data not found")
	}
	if marketData.GoodsCount() != count {
		return fmt.Errorf("expected %d trade goods, got %d", count, marketData.GoodsCount())
	}
	return nil
}

func (c *scoutTourContext) theShipShouldVisitNMarkets(count int) error {
	if c.response.MarketsVisited != count {
		return fmt.Errorf("expected %d markets visited, got %d", count, c.response.MarketsVisited)
	}
	return nil
}

func (c *scoutTourContext) marketDataShouldBePersistedForAllNWaypoints(count int) error {
	// Check that all markets in command have persisted data
	ctx := context.Background()
	persistedCount := 0
	for _, waypoint := range c.command.Markets {
		marketData, err := c.marketRepo.GetMarketData(ctx, uint(c.player.ID), waypoint)
		if err != nil {
			return fmt.Errorf("failed to get market data for %s: %w", waypoint, err)
		}
		if marketData != nil {
			persistedCount++
		}
	}

	if persistedCount != count {
		return fmt.Errorf("expected %d markets persisted, got %d", count, persistedCount)
	}
	return nil
}

func (c *scoutTourContext) theTourShouldStartFrom(waypointSymbol string) error {
	// This would be verified by checking the first waypoint visited
	// For now, we'll check the response data
	if len(c.response.TourOrder) == 0 {
		return fmt.Errorf("tour order is empty")
	}
	if c.response.TourOrder[0] != waypointSymbol {
		return fmt.Errorf("expected tour to start from %s but started from %s", waypointSymbol, c.response.TourOrder[0])
	}
	return nil
}

func (c *scoutTourContext) theVisitOrderShouldBe(orderCsv string) error {
	expectedOrder := strings.Split(orderCsv, ",")
	for i := range expectedOrder {
		expectedOrder[i] = strings.TrimSpace(expectedOrder[i])
	}

	if len(c.response.TourOrder) != len(expectedOrder) {
		return fmt.Errorf("expected %d waypoints in order, got %d", len(expectedOrder), len(c.response.TourOrder))
	}

	for i, expected := range expectedOrder {
		if c.response.TourOrder[i] != expected {
			return fmt.Errorf("expected waypoint %d to be %s but got %s", i, expected, c.response.TourOrder[i])
		}
	}

	return nil
}

// Register steps

func InitializeScoutTourScenario(ctx *godog.ScenarioContext) {
	c := &scoutTourContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		return ctx, c.reset()
	})

	// Background steps
	ctx.Step(`^a test database$`, c.aTestDatabase)
	ctx.Step(`^a registered player with ID (\d+) and agent "([^"]*)"$`, c.aRegisteredPlayerWithIDAndAgent)
	ctx.Step(`^the player has token "([^"]*)"$`, c.thePlayerHasToken)

	// Ship setup steps
	ctx.Step(`^the player has a ship "([^"]*)" at waypoint "([^"]*)" with status "([^"]*)"$`, c.thePlayerHasAShipAtWaypointWithStatus)

	// Waypoint setup steps
	ctx.Step(`^the system "([^"]*)" has waypoint "([^"]*)" with coordinates (\d+), (\d+)$`, c.theSystemHasWaypointWithCoordinates)
	ctx.Step(`^the system "([^"]*)" has the following waypoints:$`, c.theSystemHasTheFollowingWaypoints)

	// Market data setup steps
	ctx.Step(`^the API will return market data for "([^"]*)" with (\d+) trade goods$`, c.theAPIWillReturnMarketDataForWithNTradeGoods)
	ctx.Step(`^the API will return market data for all waypoints$`, c.theAPIWillReturnMarketDataForAllWaypoints)

	// When steps
	ctx.Step(`^I execute ScoutTourCommand with ship "([^"]*)", markets "([^"]*)", and (\d+) iterations?$`, c.iExecuteScoutTourCommandWithShipMarketsAndNIteration)

	// Then steps
	ctx.Step(`^the scout tour command should succeed$`, c.theCommandShouldSucceed)
	ctx.Step(`^the scout ship should be at "([^"]*)"$`, c.theShipShouldBeAt)
	ctx.Step(`^market data should be persisted for waypoint "([^"]*)"$`, c.marketDataShouldBePersistedForWaypoint)
	ctx.Step(`^the persisted market should have (\d+) trade goods$`, c.theMarketShouldHaveNTradeGoods)
	ctx.Step(`^the scout ship should have visited (\d+) markets?$`, c.theShipShouldVisitNMarkets)
	ctx.Step(`^market data should be persisted for all (\d+) waypoints?$`, c.marketDataShouldBePersistedForAllNWaypoints)
	ctx.Step(`^the tour should start from "([^"]*)"$`, c.theTourShouldStartFrom)
	ctx.Step(`^the visit order should be "([^"]*)"$`, c.theVisitOrderShouldBe)
}
