package steps

import (
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/cucumber/godog"
)

type marketContext struct {
	// TradeGood context
	tradeGoodSymbol        string
	tradeGoodSupply        *string
	tradeGoodActivity      *string
	tradeGoodPurchasePrice int
	tradeGoodSellPrice     int
	tradeGoodTradeVolume   int
	tradeGood              *market.TradeGood
	tradeGoodErr           error

	// Market context
	waypointSymbol    string
	lastUpdated       time.Time
	tradeGoods        []market.TradeGood
	market            *market.Market
	marketErr         error
	foundGood         *market.TradeGood
	boolResult        bool
	intResult         int
	retrievedGoods    []market.TradeGood
	originalGoodsCopy []market.TradeGood
}

func (mc *marketContext) reset() {
	mc.tradeGoodSymbol = ""
	mc.tradeGoodSupply = nil
	mc.tradeGoodActivity = nil
	mc.tradeGoodPurchasePrice = 0
	mc.tradeGoodSellPrice = 0
	mc.tradeGoodTradeVolume = 0
	mc.tradeGood = nil
	mc.tradeGoodErr = nil

	mc.waypointSymbol = ""
	mc.lastUpdated = time.Time{}
	mc.tradeGoods = nil
	mc.market = nil
	mc.marketErr = nil
	mc.foundGood = nil
	mc.boolResult = false
	mc.intResult = 0
	mc.retrievedGoods = nil
	mc.originalGoodsCopy = nil
}

// TradeGood setup steps

func (mc *marketContext) aTradeGoodWith(table *godog.Table) error {
	for i, row := range table.Rows {
		if i == 0 {
			continue // Skip header
		}
		mc.tradeGoodSymbol = row.Cells[0].Value

		supplyStr := row.Cells[1].Value
		if supplyStr != "" {
			mc.tradeGoodSupply = &supplyStr
		} else {
			mc.tradeGoodSupply = nil
		}

		activityStr := row.Cells[2].Value
		if activityStr != "" {
			mc.tradeGoodActivity = &activityStr
		} else {
			mc.tradeGoodActivity = nil
		}

		fmt.Sscanf(row.Cells[3].Value, "%d", &mc.tradeGoodPurchasePrice)
		fmt.Sscanf(row.Cells[4].Value, "%d", &mc.tradeGoodSellPrice)
		fmt.Sscanf(row.Cells[5].Value, "%d", &mc.tradeGoodTradeVolume)
	}
	return nil
}

func (mc *marketContext) aTradeGoodWithSupply(supply string) error {
	mc.tradeGoodSymbol = "TEST_GOOD"
	if supply != "" {
		mc.tradeGoodSupply = &supply
	} else {
		mc.tradeGoodSupply = nil
	}
	mc.tradeGoodActivity = nil
	mc.tradeGoodPurchasePrice = 100
	mc.tradeGoodSellPrice = 150
	mc.tradeGoodTradeVolume = 50
	return nil
}

func (mc *marketContext) aTradeGoodWithActivity(activity string) error {
	mc.tradeGoodSymbol = "TEST_GOOD"
	mc.tradeGoodSupply = nil
	if activity != "" {
		mc.tradeGoodActivity = &activity
	} else {
		mc.tradeGoodActivity = nil
	}
	mc.tradeGoodPurchasePrice = 100
	mc.tradeGoodSellPrice = 150
	mc.tradeGoodTradeVolume = 50
	return nil
}

// TradeGood action steps

func (mc *marketContext) iCreateTheTradeGood() error {
	mc.tradeGood, mc.tradeGoodErr = market.NewTradeGood(
		mc.tradeGoodSymbol,
		mc.tradeGoodSupply,
		mc.tradeGoodActivity,
		mc.tradeGoodPurchasePrice,
		mc.tradeGoodSellPrice,
		mc.tradeGoodTradeVolume,
	)
	return nil
}

func (mc *marketContext) iAttemptToCreateTheTradeGood() error {
	return mc.iCreateTheTradeGood()
}

// TradeGood assertion steps

func (mc *marketContext) theTradeGoodShouldBeValid() error {
	if mc.tradeGoodErr != nil {
		return fmt.Errorf("expected trade good to be valid, got error: %s", mc.tradeGoodErr)
	}
	if mc.tradeGood == nil {
		return fmt.Errorf("expected trade good to be created, got nil")
	}
	return nil
}

func (mc *marketContext) tradeGoodCreationShouldFailWithError(expectedError string) error {
	if mc.tradeGoodErr == nil {
		return fmt.Errorf("expected error '%s', but trade good creation succeeded", expectedError)
	}
	if mc.tradeGoodErr.Error() != expectedError {
		return fmt.Errorf("expected error '%s', got '%s'", expectedError, mc.tradeGoodErr.Error())
	}
	return nil
}

func (mc *marketContext) theTradeGoodSymbolShouldBe(expected string) error {
	if mc.tradeGood.Symbol() != expected {
		return fmt.Errorf("expected symbol '%s', got '%s'", expected, mc.tradeGood.Symbol())
	}
	return nil
}

func (mc *marketContext) theTradeGoodSupplyShouldBe(expected string) error {
	supply := mc.tradeGood.Supply()
	if supply == nil || *supply != expected {
		actual := "<nil>"
		if supply != nil {
			actual = *supply
		}
		return fmt.Errorf("expected supply '%s', got '%s'", expected, actual)
	}
	return nil
}

func (mc *marketContext) theTradeGoodSupplyShouldBeNil() error {
	supply := mc.tradeGood.Supply()
	if supply != nil {
		return fmt.Errorf("expected supply to be nil, got '%s'", *supply)
	}
	return nil
}

func (mc *marketContext) theTradeGoodActivityShouldBe(expected string) error {
	activity := mc.tradeGood.Activity()
	if activity == nil || *activity != expected {
		actual := "<nil>"
		if activity != nil {
			actual = *activity
		}
		return fmt.Errorf("expected activity '%s', got '%s'", expected, actual)
	}
	return nil
}

func (mc *marketContext) theTradeGoodActivityShouldBeNil() error {
	activity := mc.tradeGood.Activity()
	if activity != nil {
		return fmt.Errorf("expected activity to be nil, got '%s'", *activity)
	}
	return nil
}

func (mc *marketContext) theTradeGoodPurchasePriceShouldBe(expected int) error {
	if mc.tradeGood.PurchasePrice() != expected {
		return fmt.Errorf("expected purchase price %d, got %d", expected, mc.tradeGood.PurchasePrice())
	}
	return nil
}

func (mc *marketContext) theTradeGoodSellPriceShouldBe(expected int) error {
	if mc.tradeGood.SellPrice() != expected {
		return fmt.Errorf("expected sell price %d, got %d", expected, mc.tradeGood.SellPrice())
	}
	return nil
}

func (mc *marketContext) theTradeGoodTradeVolumeShouldBe(expected int) error {
	if mc.tradeGood.TradeVolume() != expected {
		return fmt.Errorf("expected trade volume %d, got %d", expected, mc.tradeGood.TradeVolume())
	}
	return nil
}

// Market setup steps

func (mc *marketContext) tradeGoodsForMarket(table *godog.Table) error {
	mc.tradeGoods = make([]market.TradeGood, 0)
	for i, row := range table.Rows {
		if i == 0 {
			continue // Skip header
		}
		symbol := row.Cells[0].Value
		var supply, activity *string
		if row.Cells[1].Value != "" {
			val := row.Cells[1].Value
			supply = &val
		}
		if row.Cells[2].Value != "" {
			val := row.Cells[2].Value
			activity = &val
		}
		var purchasePrice, sellPrice, tradeVolume int
		fmt.Sscanf(row.Cells[3].Value, "%d", &purchasePrice)
		fmt.Sscanf(row.Cells[4].Value, "%d", &sellPrice)
		fmt.Sscanf(row.Cells[5].Value, "%d", &tradeVolume)

		good, err := market.NewTradeGood(symbol, supply, activity, purchasePrice, sellPrice, tradeVolume)
		if err != nil {
			return err
		}
		mc.tradeGoods = append(mc.tradeGoods, *good)
	}
	return nil
}

func (mc *marketContext) noTradeGoodsForMarket() error {
	mc.tradeGoods = []market.TradeGood{}
	return nil
}

func (mc *marketContext) aMarketAtWaypointUpdatedAt(waypoint, timestamp string) error {
	mc.waypointSymbol = waypoint
	var err error
	mc.lastUpdated, err = time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return err
	}
	return nil
}

func (mc *marketContext) aValidMarketWithTradeGoods(table *godog.Table) error {
	if err := mc.tradeGoodsForMarket(table); err != nil {
		return err
	}
	mc.waypointSymbol = "X1-MARKET"
	mc.lastUpdated = time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	return mc.iCreateTheMarket()
}

func (mc *marketContext) aValidMarketWithNoTradeGoods() error {
	mc.tradeGoods = []market.TradeGood{}
	mc.waypointSymbol = "X1-EMPTY"
	mc.lastUpdated = time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	return mc.iCreateTheMarket()
}

// Market action steps

func (mc *marketContext) iCreateTheMarket() error {
	mc.market, mc.marketErr = market.NewMarket(mc.waypointSymbol, mc.tradeGoods, mc.lastUpdated)
	if mc.marketErr == nil && mc.market != nil {
		// Store a copy of original goods for immutability tests
		mc.originalGoodsCopy = mc.market.TradeGoods()
	}
	return nil
}

func (mc *marketContext) iAttemptToCreateTheMarket() error {
	return mc.iCreateTheMarket()
}

func (mc *marketContext) iFindGood(symbol string) error {
	mc.foundGood = mc.market.FindGood(symbol)
	return nil
}

func (mc *marketContext) iCheckIfMarketHasGood(symbol string) error {
	mc.boolResult = mc.market.HasGood(symbol)
	return nil
}

func (mc *marketContext) iGetTheGoodsCount() error {
	mc.intResult = mc.market.GoodsCount()
	return nil
}

func (mc *marketContext) iGetTransactionLimitFor(symbol string) error {
	mc.intResult = mc.market.GetTransactionLimit(symbol)
	return nil
}

func (mc *marketContext) iGetTheTradeGoods() error {
	mc.retrievedGoods = mc.market.TradeGoods()
	return nil
}

func (mc *marketContext) iModifyTheReturnedTradeGoodsArray() error {
	// Attempt to modify the returned array
	if len(mc.retrievedGoods) > 0 {
		// This should not affect the original market
		mc.retrievedGoods = mc.retrievedGoods[:0]
	}
	return nil
}

// Market assertion steps

func (mc *marketContext) theMarketShouldBeValid() error {
	if mc.marketErr != nil {
		return fmt.Errorf("expected market to be valid, got error: %s", mc.marketErr)
	}
	if mc.market == nil {
		return fmt.Errorf("expected market to be created, got nil")
	}
	return nil
}

func (mc *marketContext) marketCreationShouldFailWithError(expectedError string) error {
	if mc.marketErr == nil {
		return fmt.Errorf("expected error '%s', but market creation succeeded", expectedError)
	}
	if mc.marketErr.Error() != expectedError {
		return fmt.Errorf("expected error '%s', got '%s'", expectedError, mc.marketErr.Error())
	}
	return nil
}

func (mc *marketContext) theMarketWaypointSymbolShouldBe(expected string) error {
	if mc.market.WaypointSymbol() != expected {
		return fmt.Errorf("expected waypoint symbol '%s', got '%s'", expected, mc.market.WaypointSymbol())
	}
	return nil
}

func (mc *marketContext) theMarketShouldHaveTradeGoods(count int) error {
	actual := mc.market.GoodsCount()
	if actual != count {
		return fmt.Errorf("expected %d trade goods, got %d", count, actual)
	}
	return nil
}

func (mc *marketContext) theMarketLastUpdatedShouldBe(timestamp string) error {
	expected, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return err
	}
	if !mc.market.LastUpdated().Equal(expected) {
		return fmt.Errorf("expected last updated %v, got %v", expected, mc.market.LastUpdated())
	}
	return nil
}

func (mc *marketContext) theFoundGoodShouldExist() error {
	if mc.foundGood == nil {
		return fmt.Errorf("expected good to be found, got nil")
	}
	return nil
}

func (mc *marketContext) theFoundGoodShouldNotExist() error {
	if mc.foundGood != nil {
		return fmt.Errorf("expected good to not be found, got %v", mc.foundGood)
	}
	return nil
}

func (mc *marketContext) theFoundGoodSymbolShouldBe(expected string) error {
	if mc.foundGood.Symbol() != expected {
		return fmt.Errorf("expected found good symbol '%s', got '%s'", expected, mc.foundGood.Symbol())
	}
	return nil
}

func (mc *marketContext) theFoundGoodPurchasePriceShouldBe(expected int) error {
	if mc.foundGood.PurchasePrice() != expected {
		return fmt.Errorf("expected found good purchase price %d, got %d", expected, mc.foundGood.PurchasePrice())
	}
	return nil
}

func (mc *marketContext) theMarketShouldHaveTheGood() error {
	if !mc.boolResult {
		return fmt.Errorf("expected market to have the good")
	}
	return nil
}

func (mc *marketContext) theMarketShouldNotHaveTheGood() error {
	if mc.boolResult {
		return fmt.Errorf("expected market to not have the good")
	}
	return nil
}

func (mc *marketContext) theGoodsCountShouldBe(expected int) error {
	if mc.intResult != expected {
		return fmt.Errorf("expected goods count %d, got %d", expected, mc.intResult)
	}
	return nil
}

func (mc *marketContext) theTransactionLimitShouldBe(expected int) error {
	if mc.intResult != expected {
		return fmt.Errorf("expected transaction limit %d, got %d", expected, mc.intResult)
	}
	return nil
}

func (mc *marketContext) theOriginalMarketTradeGoodsShouldBeUnchanged() error {
	// Get current goods from market
	currentGoods := mc.market.TradeGoods()

	// Compare with the copy we made at creation
	if len(currentGoods) != len(mc.originalGoodsCopy) {
		return fmt.Errorf("market goods were mutated: expected %d goods, got %d", len(mc.originalGoodsCopy), len(currentGoods))
	}

	for i := range currentGoods {
		if currentGoods[i].Symbol() != mc.originalGoodsCopy[i].Symbol() {
			return fmt.Errorf("market goods were mutated at index %d", i)
		}
	}

	return nil
}

// RegisterMarketSteps registers all market step definitions
func RegisterMarketSteps(sc *godog.ScenarioContext) {
	ctx := &marketContext{}

	// TradeGood setup steps
	sc.Step(`^a trade good with:$`, ctx.aTradeGoodWith)
	sc.Step(`^a trade good with supply "([^"]*)"$`, ctx.aTradeGoodWithSupply)
	sc.Step(`^a trade good with activity "([^"]*)"$`, ctx.aTradeGoodWithActivity)

	// TradeGood action steps
	sc.Step(`^I create the trade good$`, ctx.iCreateTheTradeGood)
	sc.Step(`^I attempt to create the trade good$`, ctx.iAttemptToCreateTheTradeGood)

	// TradeGood assertion steps
	sc.Step(`^the trade good should be valid$`, ctx.theTradeGoodShouldBeValid)
	sc.Step(`^trade good creation should fail with error "([^"]*)"$`, ctx.tradeGoodCreationShouldFailWithError)
	sc.Step(`^the trade good symbol should be "([^"]*)"$`, ctx.theTradeGoodSymbolShouldBe)
	sc.Step(`^the trade good supply should be "([^"]*)"$`, ctx.theTradeGoodSupplyShouldBe)
	sc.Step(`^the trade good supply should be nil$`, ctx.theTradeGoodSupplyShouldBeNil)
	sc.Step(`^the trade good activity should be "([^"]*)"$`, ctx.theTradeGoodActivityShouldBe)
	sc.Step(`^the trade good activity should be nil$`, ctx.theTradeGoodActivityShouldBeNil)
	sc.Step(`^the trade good purchase price should be (\d+)$`, ctx.theTradeGoodPurchasePriceShouldBe)
	sc.Step(`^the trade good sell price should be (\d+)$`, ctx.theTradeGoodSellPriceShouldBe)
	sc.Step(`^the trade good trade volume should be (\d+)$`, ctx.theTradeGoodTradeVolumeShouldBe)

	// Market setup steps
	sc.Step(`^trade goods for market:$`, ctx.tradeGoodsForMarket)
	sc.Step(`^no trade goods for market$`, ctx.noTradeGoodsForMarket)
	sc.Step(`^a market at waypoint "([^"]*)" updated at "([^"]*)"$`, ctx.aMarketAtWaypointUpdatedAt)
	sc.Step(`^a valid market with trade goods:$`, ctx.aValidMarketWithTradeGoods)
	sc.Step(`^a valid market with no trade goods$`, ctx.aValidMarketWithNoTradeGoods)

	// Market action steps
	sc.Step(`^I create the market$`, ctx.iCreateTheMarket)
	sc.Step(`^I attempt to create the market$`, ctx.iAttemptToCreateTheMarket)
	sc.Step(`^I find good "([^"]*)"$`, ctx.iFindGood)
	sc.Step(`^I check if market has good "([^"]*)"$`, ctx.iCheckIfMarketHasGood)
	sc.Step(`^I get the goods count$`, ctx.iGetTheGoodsCount)
	sc.Step(`^I get transaction limit for "([^"]*)"$`, ctx.iGetTransactionLimitFor)
	sc.Step(`^I get the trade goods$`, ctx.iGetTheTradeGoods)
	sc.Step(`^I modify the returned trade goods array$`, ctx.iModifyTheReturnedTradeGoodsArray)

	// Market assertion steps
	sc.Step(`^the market should be valid$`, ctx.theMarketShouldBeValid)
	sc.Step(`^market creation should fail with error "([^"]*)"$`, ctx.marketCreationShouldFailWithError)
	sc.Step(`^the market waypoint symbol should be "([^"]*)"$`, ctx.theMarketWaypointSymbolShouldBe)
	sc.Step(`^the market should have (\d+) trade goods$`, ctx.theMarketShouldHaveTradeGoods)
	sc.Step(`^the market last updated should be "([^"]*)"$`, ctx.theMarketLastUpdatedShouldBe)
	sc.Step(`^the found good should exist$`, ctx.theFoundGoodShouldExist)
	sc.Step(`^the found good should not exist$`, ctx.theFoundGoodShouldNotExist)
	sc.Step(`^the found good symbol should be "([^"]*)"$`, ctx.theFoundGoodSymbolShouldBe)
	sc.Step(`^the found good purchase price should be (\d+)$`, ctx.theFoundGoodPurchasePriceShouldBe)
	sc.Step(`^the market should have the good$`, ctx.theMarketShouldHaveTheGood)
	sc.Step(`^the market should not have the good$`, ctx.theMarketShouldNotHaveTheGood)
	sc.Step(`^the goods count should be (\d+)$`, ctx.theGoodsCountShouldBe)
	sc.Step(`^the transaction limit should be (\d+)$`, ctx.theTransactionLimitShouldBe)
	sc.Step(`^the original market trade goods should be unchanged$`, ctx.theOriginalMarketTradeGoodsShouldBeUnchanged)
}
