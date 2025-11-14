package steps

import (
	"context"
	"fmt"
	"time"

	"github.com/cucumber/godog"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

type MarketRepositoryContext struct {
	db                *gorm.DB
	repo              *persistence.MarketRepositoryGORM
	playerID          uint
	waypointSymbol    string
	tradeGoods        []market.TradeGood
	retrievedMarket   *market.Market
	retrievedMarkets  []market.Market
	retrieveError     error
	systemSymbol      string
	maxAgeMinutes     int

	// Multi-player test state
	player2ID         uint
	player1TradeGoods []market.TradeGood
	player2TradeGoods []market.TradeGood
}

func InitializeMarketRepositoryScenario(ctx *godog.ScenarioContext) {
	c := &MarketRepositoryContext{}

	// Background steps
	ctx.Step(`^a clean test database$`, c.aCleanTestDatabase)
	ctx.Step(`^a market repository instance$`, c.aMarketRepositoryInstance)

	// Given steps
	ctx.Step(`^a player with ID (\d+)$`, c.aPlayerWithID)
	ctx.Step(`^a waypoint "([^"]*)"$`, c.aWaypoint)
	ctx.Step(`^market data with (\d+) trade goods:$`, c.marketDataWithTradeGoodsTable)
	ctx.Step(`^market data with (\d+) trade goods$`, c.marketDataWithNTradeGoods)
	ctx.Step(`^existing market data with \d+ trade goods:$`, c.existingMarketDataWithTradeGoods)
	ctx.Step(`^the market data is already persisted$`, c.theMarketDataIsAlreadyPersisted)
	ctx.Step(`^the following markets in system "([^"]*)":$`, c.theFollowingMarketsInSystem)
	ctx.Step(`^market data for player (\d+) with (\d+) trade goods$`, c.marketDataForPlayerWithNTradeGoods)

	// When steps
	ctx.Step(`^I upsert the market data$`, c.iUpsertTheMarketData)
	ctx.Step(`^I upsert new market data with (\d+) trade goods:$`, c.iUpsertNewMarketDataWithTradeGoods)
	ctx.Step(`^I get market data for player (\d+) and waypoint "([^"]*)"$`, c.iGetMarketDataForPlayerAndWaypoint)
	ctx.Step(`^I list markets for player (\d+) in system "([^"]*)" with max age (\d+) minutes$`, c.iListMarketsForPlayerInSystemWithMaxAge)
	ctx.Step(`^I upsert market data for both players$`, c.iUpsertMarketDataForBothPlayers)

	// Then steps
	ctx.Step(`^the market data should be persisted successfully$`, c.theMarketDataShouldBePersistedSuccessfully)
	ctx.Step(`^the database should contain (\d+) market record$`, c.theDatabaseShouldContainNMarketRecords)
	ctx.Step(`^the database should contain (\d+) trade goods records$`, c.theDatabaseShouldContainNTradeGoodsRecords)
	ctx.Step(`^the trade goods should be updated to the new values$`, c.theTradeGoodsShouldBeUpdatedToNewValues)
	ctx.Step(`^the market should be retrieved successfully$`, c.theMarketShouldBeRetrievedSuccessfully)
	ctx.Step(`^the market should have waypoint "([^"]*)"$`, c.theMarketShouldHaveWaypoint)
	ctx.Step(`^the retrieved market should have (\d+) trade goods$`, c.theMarketShouldHaveNTradeGoods)
	ctx.Step(`^the market should contain trade good "([^"]*)" with purchase price (\d+)$`, c.theMarketShouldContainTradeGoodWithPurchasePrice)
	ctx.Step(`^the market should be nil$`, c.theMarketShouldBeNil)
	ctx.Step(`^there should be no error$`, c.thereShouldBeNoError)
	ctx.Step(`^(\d+) markets should be returned$`, c.nMarketsShouldBeReturned)
	ctx.Step(`^the markets should include "([^"]*)"$`, c.theMarketsShouldInclude)
	ctx.Step(`^the markets should not include "([^"]*)"$`, c.theMarketsShouldNotInclude)
	ctx.Step(`^player (\d+) should have (\d+) trade goods for waypoint "([^"]*)"$`, c.playerShouldHaveNTradeGoodsForWaypoint)
}

func (c *MarketRepositoryContext) aCleanTestDatabase() error {
	// Create in-memory SQLite database
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("failed to open test database: %w", err)
	}

	// Auto-migrate the models
	err = db.AutoMigrate(
		&persistence.MarketData{},
		&persistence.TradeGoodData{},
	)
	if err != nil {
		return fmt.Errorf("failed to migrate database: %w", err)
	}

	c.db = db
	return nil
}

func (c *MarketRepositoryContext) aMarketRepositoryInstance() error {
	c.repo = persistence.NewMarketRepository(c.db)
	return nil
}

func (c *MarketRepositoryContext) aPlayerWithID(playerID int) error {
	c.playerID = uint(playerID)
	// Also set in shared context so other scenarios can access it
	return sharedPlayerHasPlayerID(playerID)
}

func (c *MarketRepositoryContext) aWaypoint(waypoint string) error {
	c.waypointSymbol = waypoint
	return nil
}

func (c *MarketRepositoryContext) marketDataWithTradeGoodsTable(n int, table *godog.Table) error {
	goods, err := c.parseTradeGoodsTable(table)
	if err != nil {
		return err
	}
	c.tradeGoods = goods
	return nil
}

func (c *MarketRepositoryContext) marketDataWithNTradeGoods(n int) error {
	c.tradeGoods = []market.TradeGood{}
	return nil
}

func (c *MarketRepositoryContext) existingMarketDataWithTradeGoods(table *godog.Table) error {
	return c.marketDataWithTradeGoodsTable(0, table)
}

func (c *MarketRepositoryContext) theMarketDataIsAlreadyPersisted() error {
	ctx := context.Background()
	timestamp := time.Now()

	err := c.repo.UpsertMarketData(ctx, c.playerID, c.waypointSymbol, c.tradeGoods, timestamp)
	if err != nil {
		return fmt.Errorf("failed to persist initial market data: %w", err)
	}
	return nil
}

func (c *MarketRepositoryContext) theFollowingMarketsInSystem(systemSymbol string, table *godog.Table) error {
	c.systemSymbol = systemSymbol
	ctx := context.Background()

	for i, row := range table.Rows[1:] { // Skip header
		waypoint := row.Cells[0].Value
		tradeGoodsCount := parseInt(row.Cells[1].Value)
		ageMinutes := parseInt(row.Cells[2].Value)

		// Create dummy trade goods
		goods := make([]market.TradeGood, tradeGoodsCount)
		for j := 0; j < tradeGoodsCount; j++ {
			symbol := fmt.Sprintf("GOOD_%d_%d", i, j)
			supply := "MODERATE"
			activity := "STRONG"
			good, err := market.NewTradeGood(symbol, &supply, &activity, 100, 150, 500)
			if err != nil {
				return err
			}
			goods[j] = *good
		}

		// Calculate timestamp based on age
		timestamp := time.Now().Add(-time.Duration(ageMinutes) * time.Minute)

		err := c.repo.UpsertMarketData(ctx, c.playerID, waypoint, goods, timestamp)
		if err != nil {
			return fmt.Errorf("failed to persist market %s: %w", waypoint, err)
		}
	}

	return nil
}

func (c *MarketRepositoryContext) marketDataForPlayerWithNTradeGoods(playerID, n int) error {
	goods := make([]market.TradeGood, n)
	for i := 0; i < n; i++ {
		symbol := fmt.Sprintf("GOOD_P%d_%d", playerID, i)
		supply := "MODERATE"
		activity := "STRONG"
		good, err := market.NewTradeGood(symbol, &supply, &activity, 100, 150, 500)
		if err != nil {
			return err
		}
		goods[i] = *good
	}

	if playerID == 1 {
		c.playerID = 1
		c.player1TradeGoods = goods
	} else if playerID == 2 {
		c.player2ID = 2
		c.player2TradeGoods = goods
	}

	return nil
}

func (c *MarketRepositoryContext) iUpsertTheMarketData() error {
	ctx := context.Background()
	timestamp := time.Now()

	err := c.repo.UpsertMarketData(ctx, c.playerID, c.waypointSymbol, c.tradeGoods, timestamp)
	c.retrieveError = err
	return nil
}

func (c *MarketRepositoryContext) iUpsertNewMarketDataWithTradeGoods(n int, table *godog.Table) error {
	goods, err := c.parseTradeGoodsTable(table)
	if err != nil {
		return err
	}
	c.tradeGoods = goods

	ctx := context.Background()
	timestamp := time.Now()

	err = c.repo.UpsertMarketData(ctx, c.playerID, c.waypointSymbol, c.tradeGoods, timestamp)
	c.retrieveError = err
	return nil
}

func (c *MarketRepositoryContext) iGetMarketDataForPlayerAndWaypoint(playerID int, waypoint string) error {
	ctx := context.Background()

	market, err := c.repo.GetMarketData(ctx, uint(playerID), waypoint)
	c.retrievedMarket = market
	c.retrieveError = err
	return nil
}

func (c *MarketRepositoryContext) iListMarketsForPlayerInSystemWithMaxAge(playerID int, systemSymbol string, maxAge int) error {
	ctx := context.Background()

	markets, err := c.repo.ListMarketsInSystem(ctx, uint(playerID), systemSymbol, maxAge)
	c.retrievedMarkets = markets
	c.retrieveError = err
	return nil
}

func (c *MarketRepositoryContext) iUpsertMarketDataForBothPlayers() error {
	ctx := context.Background()
	timestamp := time.Now()

	// Upsert for player 1
	err := c.repo.UpsertMarketData(ctx, c.playerID, c.waypointSymbol, c.player1TradeGoods, timestamp)
	if err != nil {
		return fmt.Errorf("failed to upsert for player 1: %w", err)
	}

	// Upsert for player 2
	err = c.repo.UpsertMarketData(ctx, c.player2ID, c.waypointSymbol, c.player2TradeGoods, timestamp)
	if err != nil {
		return fmt.Errorf("failed to upsert for player 2: %w", err)
	}

	return nil
}

func (c *MarketRepositoryContext) theMarketDataShouldBePersistedSuccessfully() error {
	if c.retrieveError != nil {
		return fmt.Errorf("expected no error when upserting market data, but got: %v", c.retrieveError)
	}
	return nil
}

func (c *MarketRepositoryContext) theDatabaseShouldContainNMarketRecords(n int) error {
	var count int64
	err := c.db.Model(&persistence.MarketData{}).Count(&count).Error
	if err != nil {
		return fmt.Errorf("failed to count market records: %v", err)
	}
	if int64(n) != count {
		return fmt.Errorf("expected %d market records, got %d", n, count)
	}
	return nil
}

func (c *MarketRepositoryContext) theDatabaseShouldContainNTradeGoodsRecords(n int) error {
	var count int64
	err := c.db.Model(&persistence.TradeGoodData{}).Count(&count).Error
	if err != nil {
		return fmt.Errorf("failed to count trade goods records: %v", err)
	}
	if int64(n) != count {
		return fmt.Errorf("expected %d trade goods records, got %d", n, count)
	}
	return nil
}

func (c *MarketRepositoryContext) theTradeGoodsShouldBeUpdatedToNewValues() error {
	ctx := context.Background()

	// Retrieve the market to verify
	retrievedMarket, err := c.repo.GetMarketData(ctx, c.playerID, c.waypointSymbol)
	if err != nil {
		return fmt.Errorf("failed to retrieve market for verification: %v", err)
	}
	if retrievedMarket == nil {
		return fmt.Errorf("market should exist but got nil")
	}

	// Verify the goods match the new values
	if len(c.tradeGoods) != retrievedMarket.GoodsCount() {
		return fmt.Errorf("trade goods count mismatch: expected %d, got %d", len(c.tradeGoods), retrievedMarket.GoodsCount())
	}

	for _, expectedGood := range c.tradeGoods {
		actualGood := retrievedMarket.FindGood(expectedGood.Symbol())
		if actualGood == nil {
			return fmt.Errorf("trade good %s not found", expectedGood.Symbol())
		}
		if expectedGood.PurchasePrice() != actualGood.PurchasePrice() {
			return fmt.Errorf("purchase price mismatch for %s: expected %d, got %d",
				expectedGood.Symbol(), expectedGood.PurchasePrice(), actualGood.PurchasePrice())
		}
		if expectedGood.SellPrice() != actualGood.SellPrice() {
			return fmt.Errorf("sell price mismatch for %s: expected %d, got %d",
				expectedGood.Symbol(), expectedGood.SellPrice(), actualGood.SellPrice())
		}
	}

	return nil
}

func (c *MarketRepositoryContext) theMarketShouldBeRetrievedSuccessfully() error {
	if c.retrieveError != nil {
		return fmt.Errorf("expected no error when retrieving market, but got: %v", c.retrieveError)
	}
	if c.retrievedMarket == nil {
		return fmt.Errorf("expected market to be retrieved, but got nil")
	}
	return nil
}

func (c *MarketRepositoryContext) theMarketShouldHaveWaypoint(waypoint string) error {
	if c.retrievedMarket == nil {
		return fmt.Errorf("cannot check waypoint: retrieved market is nil")
	}
	if c.retrievedMarket.WaypointSymbol() != waypoint {
		return fmt.Errorf("waypoint mismatch: expected %s, got %s", waypoint, c.retrievedMarket.WaypointSymbol())
	}
	return nil
}

func (c *MarketRepositoryContext) theMarketShouldHaveNTradeGoods(n int) error {
	if c.retrievedMarket == nil {
		return fmt.Errorf("cannot check trade goods count: retrieved market is nil")
	}
	if c.retrievedMarket.GoodsCount() != n {
		return fmt.Errorf("trade goods count mismatch: expected %d, got %d", n, c.retrievedMarket.GoodsCount())
	}
	return nil
}

func (c *MarketRepositoryContext) theMarketShouldContainTradeGoodWithPurchasePrice(symbol string, price int) error {
	if c.retrievedMarket == nil {
		return fmt.Errorf("cannot check trade good: retrieved market is nil")
	}
	good := c.retrievedMarket.FindGood(symbol)
	if good == nil {
		return fmt.Errorf("trade good %s not found in market", symbol)
	}
	if good.PurchasePrice() != price {
		return fmt.Errorf("purchase price mismatch for %s: expected %d, got %d", symbol, price, good.PurchasePrice())
	}
	return nil
}

func (c *MarketRepositoryContext) theMarketShouldBeNil() error {
	if c.retrievedMarket != nil {
		return fmt.Errorf("expected market to be nil, but got: %+v", c.retrievedMarket)
	}
	return nil
}

func (c *MarketRepositoryContext) thereShouldBeNoError() error {
	if c.retrieveError != nil {
		return fmt.Errorf("expected no error, but got: %v", c.retrieveError)
	}
	return nil
}

func (c *MarketRepositoryContext) nMarketsShouldBeReturned(n int) error {
	if n != len(c.retrievedMarkets) {
		return fmt.Errorf("markets count mismatch: expected %d, got %d", n, len(c.retrievedMarkets))
	}
	return nil
}

func (c *MarketRepositoryContext) theMarketsShouldInclude(waypoint string) error {
	found := false
	for _, m := range c.retrievedMarkets {
		if m.WaypointSymbol() == waypoint {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("expected markets to include %s, but it was not found", waypoint)
	}
	return nil
}

func (c *MarketRepositoryContext) theMarketsShouldNotInclude(waypoint string) error {
	for _, m := range c.retrievedMarkets {
		if m.WaypointSymbol() == waypoint {
			return fmt.Errorf("expected markets to NOT include %s, but it was found", waypoint)
		}
	}
	return nil
}

func (c *MarketRepositoryContext) playerShouldHaveNTradeGoodsForWaypoint(playerID int, n int, waypoint string) error {
	ctx := context.Background()

	market, err := c.repo.GetMarketData(ctx, uint(playerID), waypoint)
	if err != nil {
		return fmt.Errorf("failed to retrieve market for player %d: %v", playerID, err)
	}
	if market == nil {
		return fmt.Errorf("market should exist for player %d but got nil", playerID)
	}
	if n != market.GoodsCount() {
		return fmt.Errorf("trade goods count mismatch for player %d: expected %d, got %d", playerID, n, market.GoodsCount())
	}

	return nil
}

// Helper methods

func (c *MarketRepositoryContext) parseTradeGoodsTable(table *godog.Table) ([]market.TradeGood, error) {
	goods := make([]market.TradeGood, 0, len(table.Rows)-1)

	for _, row := range table.Rows[1:] { // Skip header
		symbol := row.Cells[0].Value
		supply := row.Cells[1].Value
		activity := row.Cells[2].Value
		purchasePrice := parseInt(row.Cells[3].Value)
		sellPrice := parseInt(row.Cells[4].Value)
		tradeVolume := parseInt(row.Cells[5].Value)

		good, err := market.NewTradeGood(symbol, &supply, &activity, purchasePrice, sellPrice, tradeVolume)
		if err != nil {
			return nil, fmt.Errorf("failed to create trade good: %w", err)
		}
		goods = append(goods, *good)
	}

	return goods, nil
}
