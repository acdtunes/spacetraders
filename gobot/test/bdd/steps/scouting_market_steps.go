package steps

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// scoutingMarketContext holds state for market scouting scenarios
type scoutingMarketContext struct {
	waypointSymbol string
	tradeGoods     []market.TradeGood
	market         *market.Market
	foundGood      *market.TradeGood
	hasGoodResult  bool
	goodsCount     int
	currentError   error
}

func (mc *scoutingMarketContext) reset() {
	mc.waypointSymbol = ""
	mc.tradeGoods = []market.TradeGood{}
	mc.market = nil
	mc.foundGood = nil
	mc.hasGoodResult = false
	mc.goodsCount = 0
	mc.currentError = nil
}

func InitializeScoutingMarketSteps(ctx *godog.ScenarioContext) {
	mc := &scoutingMarketContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		mc.reset()
		return ctx, nil
	})

	// Background
	ctx.Step(`^I have a market at waypoint "([^"]*)"$`, mc.iHaveAMarketAtWaypoint)

	// Given steps for setting up trade goods
	ctx.Step(`^I have the following trade goods:$`, mc.iHaveTheFollowingTradeGoods)
	ctx.Step(`^I have created a market with these goods at "([^"]*)"$`, mc.iHaveCreatedAMarketWithTheseGoods)

	// When steps - market creation
	ctx.Step(`^I create a market with waypoint "([^"]*)" at timestamp "([^"]*)" with these goods$`,
		mc.iCreateAMarketWithTheseGoods)
	ctx.Step(`^I create a market with waypoint "([^"]*)" at timestamp "([^"]*)" with no goods$`,
		mc.iCreateAMarketWithNoGoods)
	ctx.Step(`^I attempt to create a market with empty waypoint at timestamp "([^"]*)"$`,
		mc.iAttemptToCreateAMarketWithEmptyWaypoint)
	ctx.Step(`^I attempt to create a market with waypoint "([^"]*)" at empty timestamp$`,
		mc.iAttemptToCreateAMarketWithEmptyTimestamp)

	// When steps - market queries
	ctx.Step(`^I search for good "([^"]*)" in the market$`, mc.iSearchForGoodInTheMarket)
	ctx.Step(`^I check if the market has good "([^"]*)"$`, mc.iCheckIfTheMarketHasGood)
	ctx.Step(`^I count the goods in the market$`, mc.iCountTheGoodsInTheMarket)

	// Then steps - assertions
	ctx.Step(`^the market should have waypoint symbol "([^"]*)"$`, mc.theMarketShouldHaveWaypointSymbol)
	ctx.Step(`^the market should have (\d+) trade goods$`, mc.theMarketShouldHaveTradeGoods)
	ctx.Step(`^the market should have last updated "([^"]*)"$`, mc.theMarketShouldHaveLastUpdated)
	ctx.Step(`^I should find the good with purchase price (\d+) and sell price (\d+)$`,
		mc.iShouldFindTheGoodWithPrices)
	// More specific patterns to avoid collision with value_object_steps
	ctx.Step(`^the has good result should be (true|false)$`, mc.theResultShouldBe)
	ctx.Step(`^the goods count should be (\d+)$`, mc.theCountShouldBe)
	ctx.Step(`^I should get a market error "([^"]*)"$`, mc.iShouldGetAnError)
	// Fallback for generic patterns (will be lower precedence due to registration order)
	ctx.Step(`^the result should be (true|false)$`, mc.theResultShouldBe)
	ctx.Step(`^the count should be (\d+)$`, mc.theCountShouldBe)
	ctx.Step(`^I should get an error "([^"]*)"$`, mc.iShouldGetAnError)
}

func (mc *scoutingMarketContext) iHaveAMarketAtWaypoint(waypoint string) error {
	mc.waypointSymbol = waypoint
	return nil
}

func (mc *scoutingMarketContext) iHaveTheFollowingTradeGoods(table *godog.Table) error {
	mc.tradeGoods = []market.TradeGood{}

	// Skip header row
	for i := 1; i < len(table.Rows); i++ {
		row := table.Rows[i]
		cells := row.Cells

		symbol := cells[0].Value
		supply := cells[1].Value
		activity := cells[2].Value
		purchasePrice := parseIntHelper(cells[3].Value)
		sellPrice := parseIntHelper(cells[4].Value)
		tradeVolume := parseIntHelper(cells[5].Value)

		var supplyPtr *string
		var activityPtr *string

		if supply != "" {
			supplyPtr = &supply
		}
		if activity != "" {
			activityPtr = &activity
		}

		tradeGood, err := market.NewTradeGood(symbol, supplyPtr, activityPtr, purchasePrice, sellPrice, tradeVolume)
		if err != nil {
			return fmt.Errorf("failed to create trade good: %w", err)
		}

		mc.tradeGoods = append(mc.tradeGoods, *tradeGood)
	}

	return nil
}

func (mc *scoutingMarketContext) iHaveCreatedAMarketWithTheseGoods(waypoint string) error {
	timestamp := time.Now()
	mkt, err := market.NewMarket(waypoint, mc.tradeGoods, timestamp)
	if err != nil {
		return fmt.Errorf("failed to create market: %w", err)
	}
	mc.market = mkt
	return nil
}

func (mc *scoutingMarketContext) iCreateAMarketWithTheseGoods(waypoint, timestamp string) error {
	ts, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return fmt.Errorf("failed to parse timestamp: %w", err)
	}

	mkt, err := market.NewMarket(waypoint, mc.tradeGoods, ts)
	if err != nil {
		return fmt.Errorf("unexpected error creating market: %w", err)
	}

	mc.market = mkt
	sharedMarket = mkt // Share with other contexts
	return nil
}

func (mc *scoutingMarketContext) iCreateAMarketWithNoGoods(waypoint, timestamp string) error {
	ts, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return fmt.Errorf("failed to parse timestamp: %w", err)
	}

	mkt, err := market.NewMarket(waypoint, []market.TradeGood{}, ts)
	if err != nil {
		return fmt.Errorf("unexpected error creating market: %w", err)
	}

	mc.market = mkt
	sharedMarket = mkt // Share with other contexts
	return nil
}

func (mc *scoutingMarketContext) iAttemptToCreateAMarketWithEmptyWaypoint(timestamp string) error {
	ts, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return fmt.Errorf("failed to parse timestamp: %w", err)
	}

	mkt, err := market.NewMarket("", mc.tradeGoods, ts)
	mc.currentError = err
	mc.market = mkt
	return nil
}

func (mc *scoutingMarketContext) iAttemptToCreateAMarketWithEmptyTimestamp(waypoint string) error {
	mkt, err := market.NewMarket(waypoint, mc.tradeGoods, time.Time{})
	mc.currentError = err
	mc.market = mkt
	return nil
}

func (mc *scoutingMarketContext) iSearchForGoodInTheMarket(symbol string) error {
	if mc.market == nil {
		return fmt.Errorf("market should not be nil")
	}
	mc.foundGood = mc.market.FindGood(symbol)
	return nil
}

func (mc *scoutingMarketContext) iCheckIfTheMarketHasGood(symbol string) error {
	if mc.market == nil {
		return fmt.Errorf("market should not be nil")
	}
	mc.hasGoodResult = mc.market.HasGood(symbol)
	return nil
}

func (mc *scoutingMarketContext) iCountTheGoodsInTheMarket() error {
	if mc.market == nil {
		return fmt.Errorf("market should not be nil")
	}
	mc.goodsCount = mc.market.GoodsCount()
	return nil
}

func (mc *scoutingMarketContext) theMarketShouldHaveWaypointSymbol(expectedSymbol string) error {
	if mc.market == nil {
		return fmt.Errorf("market should not be nil")
	}
	if mc.market.WaypointSymbol() != expectedSymbol {
		return fmt.Errorf("expected waypoint symbol '%s' but got '%s'", expectedSymbol, mc.market.WaypointSymbol())
	}
	return nil
}

func (mc *scoutingMarketContext) theMarketShouldHaveTradeGoods(expectedCount int) error {
	if mc.market == nil {
		return fmt.Errorf("market should not be nil")
	}
	if mc.market.GoodsCount() != expectedCount {
		return fmt.Errorf("expected %d trade goods but got %d", expectedCount, mc.market.GoodsCount())
	}
	return nil
}

func (mc *scoutingMarketContext) theMarketShouldHaveLastUpdated(expectedTimestamp string) error {
	if mc.market == nil {
		return fmt.Errorf("market should not be nil")
	}
	expectedTime, err := time.Parse(time.RFC3339, expectedTimestamp)
	if err != nil {
		return fmt.Errorf("failed to parse expected timestamp: %w", err)
	}
	if !mc.market.LastUpdated().Equal(expectedTime) {
		return fmt.Errorf("expected timestamp '%s' but got '%s'", expectedTime, mc.market.LastUpdated())
	}
	return nil
}

func (mc *scoutingMarketContext) iShouldFindTheGoodWithPrices(purchasePrice, sellPrice int) error {
	if mc.foundGood == nil {
		return fmt.Errorf("should have found a trade good")
	}
	if mc.foundGood.PurchasePrice() != purchasePrice {
		return fmt.Errorf("expected purchase price %d but got %d", purchasePrice, mc.foundGood.PurchasePrice())
	}
	if mc.foundGood.SellPrice() != sellPrice {
		return fmt.Errorf("expected sell price %d but got %d", sellPrice, mc.foundGood.SellPrice())
	}
	return nil
}

func (mc *scoutingMarketContext) theResultShouldBe(expected string) error {
	expectedBool := expected == "true"
	if mc.hasGoodResult != expectedBool {
		return fmt.Errorf("expected result %v but got %v", expectedBool, mc.hasGoodResult)
	}
	return nil
}

func (mc *scoutingMarketContext) theCountShouldBe(expected int) error {
	if mc.goodsCount != expected {
		return fmt.Errorf("expected count %d but got %d", expected, mc.goodsCount)
	}
	return nil
}

func (mc *scoutingMarketContext) iShouldGetAnError(expectedErrorMsg string) error {
	if mc.currentError == nil {
		return fmt.Errorf("expected an error but got nil")
	}
	if !strings.Contains(mc.currentError.Error(), expectedErrorMsg) {
		return fmt.Errorf("expected error containing '%s' but got '%s'", expectedErrorMsg, mc.currentError.Error())
	}
	return nil
}

// Helper to parse integers from table cells
func parseIntHelper(s string) int {
	var result int
	fmt.Sscanf(s, "%d", &result)
	return result
}
