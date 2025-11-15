package steps

import (
	"context"
	"fmt"
	"time"

	"github.com/cucumber/godog"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
)

type listMarketDataContext struct {
	db           *gorm.DB
	marketRepo   *persistence.MarketRepositoryGORM
	playerRepo   *persistence.GormPlayerRepository
	playerID     uint
	systemSymbol string
	maxAge       int
	response     *scouting.ListMarketDataResponse
	err          error
}

func InitializeListMarketDataScenario(ctx *godog.ScenarioContext) {
	c := &listMarketDataContext{}

	// Given steps
	ctx.Step(`^a player with ID (\d+)$`, c.aPlayerWithID)
	ctx.Step(`^market data exists for waypoint "([^"]*)" in system "([^"]*)" with (\d+) trade goods$`, c.marketDataExistsForWaypointInSystem)
	ctx.Step(`^market data exists for waypoint "([^"]*)" in system "([^"]*)" with (\d+) trade goods updated (\d+) minutes ago$`, c.marketDataExistsForWaypointInSystemUpdatedMinutesAgo)

	// When steps
	ctx.Step(`^I query all markets in system "([^"]*)" with max age (\d+) minutes$`, c.iQueryAllMarketsInSystemWithMaxAge)

	// Then steps
	ctx.Step(`^the list query should succeed$`, c.theListQueryShouldSucceed)
	ctx.Step(`^the market list should contain (\d+) markets$`, c.theMarketListShouldContainNMarkets)
	ctx.Step(`^the market list should be empty$`, c.theMarketListShouldBeEmpty)
	ctx.Step(`^the market list should include waypoint "([^"]*)"$`, c.theMarketListShouldIncludeWaypoint)
	ctx.Step(`^the market list should not include waypoint "([^"]*)"$`, c.theMarketListShouldNotIncludeWaypoint)

	// Setup/teardown
	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		return ctx, c.setupDatabase()
	})
}

func (c *listMarketDataContext) setupDatabase() error {
	// Create in-memory SQLite database
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("failed to open test database: %w", err)
	}

	// Auto-migrate the models
	err = db.AutoMigrate(
		&persistence.MarketData{},
		&persistence.TradeGoodData{},
		&persistence.PlayerModel{},
	)
	if err != nil {
		return fmt.Errorf("failed to migrate database: %w", err)
	}

	c.db = db
	c.marketRepo = persistence.NewMarketRepository(db)
	c.playerRepo = persistence.NewGormPlayerRepository(db)
	return nil
}

func (c *listMarketDataContext) aPlayerWithID(playerID int) error {
	c.playerID = uint(playerID)
	return c.ensurePlayerExists(playerID)
}

// ensurePlayerExists ensures a player with the given ID exists in the repository
func (c *listMarketDataContext) ensurePlayerExists(playerID int) error {
	// Check if player already exists
	_, err := c.playerRepo.FindByID(context.Background(), playerID)
	if err == nil {
		return nil // Player already exists
	}

	// Create and save player
	agentSymbol := fmt.Sprintf("AGENT-%d", playerID)
	token := fmt.Sprintf("token-%d", playerID)

	p := player.NewPlayer(playerID, agentSymbol, token)
	return c.playerRepo.Save(context.Background(), p)
}

func (c *listMarketDataContext) marketDataExistsForWaypointInSystem(waypointSymbol, systemSymbol string, count int) error {
	return c.createMarketData(waypointSymbol, systemSymbol, count, time.Now())
}

func (c *listMarketDataContext) marketDataExistsForWaypointInSystemUpdatedMinutesAgo(waypointSymbol, systemSymbol string, count, minutesAgo int) error {
	timestamp := time.Now().Add(-time.Duration(minutesAgo) * time.Minute)
	return c.createMarketData(waypointSymbol, systemSymbol, count, timestamp)
}

func (c *listMarketDataContext) createMarketData(waypointSymbol, systemSymbol string, count int, timestamp time.Time) error {
	// Create sample trade goods
	tradeGoods := make([]market.TradeGood, count)
	for i := 0; i < count; i++ {
		symbol := fmt.Sprintf("GOOD_%d", i+1)
		supply := "MODERATE"
		activity := "STRONG"
		good, err := market.NewTradeGood(symbol, &supply, &activity, 100, 150, 500)
		if err != nil {
			return err
		}
		tradeGoods[i] = *good
	}

	// Persist to database
	ctx := context.Background()
	err := c.marketRepo.UpsertMarketData(ctx, c.playerID, waypointSymbol, tradeGoods, timestamp)
	if err != nil {
		return fmt.Errorf("failed to upsert market data: %w", err)
	}

	return nil
}

func (c *listMarketDataContext) iQueryAllMarketsInSystemWithMaxAge(systemSymbol string, maxAge int) error {
	c.systemSymbol = systemSymbol
	c.maxAge = maxAge

	// Create handler
	handler := scouting.NewListMarketDataHandler(c.marketRepo)

	// Create query
	query := &scouting.ListMarketDataQuery{
		PlayerID:      c.playerID,
		SystemSymbol:  systemSymbol,
		MaxAgeMinutes: maxAge,
	}

	// Execute query
	ctx := context.Background()
	response, err := handler.Handle(ctx, query)
	if err == nil {
		c.response, _ = response.(*scouting.ListMarketDataResponse)
	}
	c.err = err

	return nil
}

func (c *listMarketDataContext) theListQueryShouldSucceed() error {
	if c.err != nil {
		return fmt.Errorf("list query should not return error: %w", c.err)
	}
	if c.response == nil {
		return fmt.Errorf("response should not be nil")
	}
	return nil
}

func (c *listMarketDataContext) theMarketListShouldContainNMarkets(count int) error {
	if len(c.response.Markets) != count {
		return fmt.Errorf("expected %d markets, got %d", count, len(c.response.Markets))
	}
	return nil
}

func (c *listMarketDataContext) theMarketListShouldBeEmpty() error {
	if len(c.response.Markets) != 0 {
		return fmt.Errorf("expected empty market list, got %d markets", len(c.response.Markets))
	}
	return nil
}

func (c *listMarketDataContext) theMarketListShouldIncludeWaypoint(waypointSymbol string) error {
	for _, m := range c.response.Markets {
		if m.WaypointSymbol() == waypointSymbol {
			return nil
		}
	}
	return fmt.Errorf("market list should include waypoint %s", waypointSymbol)
}

func (c *listMarketDataContext) theMarketListShouldNotIncludeWaypoint(waypointSymbol string) error {
	for _, m := range c.response.Markets {
		if m.WaypointSymbol() == waypointSymbol {
			return fmt.Errorf("market list should not include waypoint %s", waypointSymbol)
		}
	}
	return nil
}
