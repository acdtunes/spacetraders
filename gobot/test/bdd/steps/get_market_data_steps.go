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

type getMarketDataContext struct {
	db              *gorm.DB
	marketRepo      *persistence.MarketRepositoryGORM
	playerRepo      *persistence.GormPlayerRepository
	playerID        uint
	waypointSymbol  string
	response        *scouting.GetMarketDataResponse
	err             error
}

func InitializeGetMarketDataScenario(ctx *godog.ScenarioContext) {
	c := &getMarketDataContext{}

	// Given steps
	ctx.Step(`^a player with ID (\d+)$`, c.aPlayerWithID)
	ctx.Step(`^market data exists for waypoint "([^"]*)" with (\d+) trade goods$`, c.marketDataExistsForWaypointWithNTradeGoods)

	// When steps
	ctx.Step(`^I query market data for waypoint "([^"]*)"$`, c.iQueryMarketDataForWaypoint)

	// Then steps
	ctx.Step(`^the market data query should succeed$`, c.theQueryShouldSucceed)
	ctx.Step(`^the queried market should have (\d+) trade goods$`, c.theMarketShouldHaveNTradeGoods)
	ctx.Step(`^the queried market should be nil$`, c.theMarketShouldBeNil)

	// Setup/teardown
	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		return ctx, c.setupDatabase()
	})
}

func (c *getMarketDataContext) setupDatabase() error {
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

func (c *getMarketDataContext) aPlayerWithID(playerID int) error {
	c.playerID = uint(playerID)
	return c.ensurePlayerExists(playerID)
}

// ensurePlayerExists ensures a player with the given ID exists in the repository
func (c *getMarketDataContext) ensurePlayerExists(playerID int) error {
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

func (c *getMarketDataContext) marketDataExistsForWaypointWithNTradeGoods(waypointSymbol string, count int) error {
	c.waypointSymbol = waypointSymbol

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
	timestamp := time.Now()
	err := c.marketRepo.UpsertMarketData(ctx, c.playerID, waypointSymbol, tradeGoods, timestamp)
	if err != nil {
		return fmt.Errorf("failed to upsert market data: %w", err)
	}

	return nil
}

func (c *getMarketDataContext) iQueryMarketDataForWaypoint(waypointSymbol string) error {
	c.waypointSymbol = waypointSymbol

	// Create handler
	handler := scouting.NewGetMarketDataHandler(c.marketRepo)

	// Create query
	query := &scouting.GetMarketDataQuery{
		PlayerID:       c.playerID,
		WaypointSymbol: waypointSymbol,
	}

	// Execute query
	ctx := context.Background()
	response, err := handler.Handle(ctx, query)
	if err == nil {
		var ok bool
		c.response, ok = response.(*scouting.GetMarketDataResponse)
		if !ok {
			return fmt.Errorf("unexpected response type: %T", response)
		}
	}
	c.err = err

	return nil
}

func (c *getMarketDataContext) theQueryShouldSucceed() error {
	if c.err != nil {
		return fmt.Errorf("query should not return error: %w", c.err)
	}
	if c.response == nil {
		return fmt.Errorf("response should not be nil")
	}
	return nil
}

func (c *getMarketDataContext) theMarketShouldHaveNTradeGoods(count int) error {
	if c.response.Market == nil {
		return fmt.Errorf("market should not be nil")
	}
	if c.response.Market.GoodsCount() != count {
		return fmt.Errorf("expected %d trade goods, got %d", count, c.response.Market.GoodsCount())
	}
	return nil
}

func (c *getMarketDataContext) theMarketShouldBeNil() error {
	if c.response.Market != nil {
		return fmt.Errorf("market should be nil")
	}
	return nil
}
