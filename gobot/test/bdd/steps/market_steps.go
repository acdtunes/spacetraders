package steps

import (
	"context"
	"fmt"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
	"github.com/cucumber/godog"
)

type marketContext struct {
	waypointSymbol   string
	tradeGoods       []tradeGoodBuilder
	market           *trading.Market
	err              error
	foundTradeGood   *trading.TradeGood
	found            bool
	transactionLimit int
}

type tradeGoodBuilder struct {
	symbol        string
	sellPrice     int
	purchasePrice int
	tradeVolume   int
	supply        string
}

func (mc *marketContext) reset() {
	mc.waypointSymbol = ""
	mc.tradeGoods = []tradeGoodBuilder{}
	mc.market = nil
	mc.err = nil
	mc.foundTradeGood = nil
	mc.found = false
	mc.transactionLimit = 0
}

// Given steps

func (mc *marketContext) aMarketAtWaypoint(waypointSymbol string) error {
	mc.waypointSymbol = waypointSymbol
	return nil
}

func (mc *marketContext) theMarketSellsAtCreditsWithTradeVolume(symbol string, sellPrice, tradeVolume int) error {
	mc.tradeGoods = append(mc.tradeGoods, tradeGoodBuilder{
		symbol:        symbol,
		sellPrice:     sellPrice,
		purchasePrice: sellPrice + 10, // Default purchase price
		tradeVolume:   tradeVolume,
		supply:        "MODERATE",
	})
	return nil
}

func (mc *marketContext) aMarketWithAtCredits(symbol string, sellPrice int) error {
	mc.waypointSymbol = "X1-TEST-A1"
	mc.tradeGoods = []tradeGoodBuilder{
		{
			symbol:        symbol,
			sellPrice:     sellPrice,
			purchasePrice: sellPrice + 10,
			tradeVolume:   100,
			supply:        "MODERATE",
		},
	}
	return nil
}

func (mc *marketContext) aMarketWithWithTradeVolume(symbol string, tradeVolume int) error {
	mc.waypointSymbol = "X1-TEST-A1"
	mc.tradeGoods = []tradeGoodBuilder{
		{
			symbol:        symbol,
			sellPrice:     50,
			purchasePrice: 60,
			tradeVolume:   tradeVolume,
			supply:        "MODERATE",
		},
	}
	return nil
}

// When steps

func (mc *marketContext) iCreateTheMarket() error {
	// Convert tradeGoodBuilders to trading.TradeGood
	tradeGoods := make([]trading.TradeGood, len(mc.tradeGoods))
	for i, tg := range mc.tradeGoods {
		tradeGoods[i] = trading.TradeGood{
			Symbol:        tg.symbol,
			Supply:        tg.supply,
			SellPrice:     tg.sellPrice,
			PurchasePrice: tg.purchasePrice,
			TradeVolume:   tg.tradeVolume,
		}
	}

	mc.market, mc.err = trading.NewMarket(mc.waypointSymbol, tradeGoods)
	return nil
}

func (mc *marketContext) iCreateTheMarketWithNoTradeGoods() error {
	mc.tradeGoods = []tradeGoodBuilder{}
	return mc.iCreateTheMarket()
}

func (mc *marketContext) iTryToCreateTheMarket() error {
	return mc.iCreateTheMarket()
}

func (mc *marketContext) iGetTradeGood(symbol string) error {
	// First create the market if needed
	if mc.market == nil {
		if err := mc.iCreateTheMarket(); err != nil {
			return err
		}
		if mc.err != nil {
			return nil // Let the Then step handle the error
		}
	}

	mc.foundTradeGood, mc.found = mc.market.GetTradeGood(symbol)
	return nil
}

func (mc *marketContext) iGetTransactionLimitFor(symbol string) error {
	// First create the market if needed
	if mc.market == nil {
		if err := mc.iCreateTheMarket(); err != nil {
			return err
		}
		if mc.err != nil {
			return nil // Let the Then step handle the error
		}
	}

	mc.transactionLimit = mc.market.GetTransactionLimit(symbol)
	return nil
}

func (mc *marketContext) iCheckIfMarketHas(symbol string) error {
	// First create the market if needed
	if mc.market == nil {
		if err := mc.iCreateTheMarket(); err != nil {
			return err
		}
		if mc.err != nil {
			return nil // Let the Then step handle the error
		}
	}

	mc.found = mc.market.HasGood(symbol)
	return nil
}

// Then steps

func (mc *marketContext) theMarketShouldBeCreatedSuccessfully() error {
	if mc.err != nil {
		return fmt.Errorf("expected no error but got: %v", mc.err)
	}
	if mc.market == nil {
		return fmt.Errorf("expected market to be created but got nil")
	}
	return nil
}

func (mc *marketContext) theWaypointShouldBe(expectedWaypoint string) error {
	if mc.market == nil {
		return fmt.Errorf("market is nil")
	}
	actualWaypoint := mc.market.WaypointSymbol()
	if actualWaypoint != expectedWaypoint {
		return fmt.Errorf("expected waypoint '%s' but got '%s'", expectedWaypoint, actualWaypoint)
	}
	return nil
}

func (mc *marketContext) theMarketShouldHaveTradeGoods(expectedCount int) error {
	// Check both local context market and shared market (for scouting market context)
	var actualCount int
	if mc.market != nil {
		actualCount = len(mc.market.TradeGoods())
	} else if sharedMarket != nil {
		actualCount = sharedMarket.GoodsCount()
	} else {
		return fmt.Errorf("market is nil")
	}

	if actualCount != expectedCount {
		return fmt.Errorf("expected %d trade goods but got %d", expectedCount, actualCount)
	}
	return nil
}

func (mc *marketContext) iShouldGetAnError(expectedError string) error {
	// Check both local context error and shared error (for cross-context assertions)
	actualErr := mc.err
	if actualErr == nil {
		actualErr = sharedErr // Check shared error from other contexts (e.g., trade good validation)
	}

	if actualErr == nil {
		return fmt.Errorf("expected error '%s' but got no error", expectedError)
	}
	// Use contains instead of exact match to handle detailed error messages
	if !strings.Contains(actualErr.Error(), expectedError) {
		return fmt.Errorf("expected error containing '%s' but got '%s'", expectedError, actualErr.Error())
	}
	return nil
}

func (mc *marketContext) iShouldFindTheTradeGood() error {
	if !mc.found {
		return fmt.Errorf("expected to find trade good but did not")
	}
	if mc.foundTradeGood == nil {
		return fmt.Errorf("expected trade good to be non-nil")
	}
	return nil
}

func (mc *marketContext) theSellPriceShouldBeCredits(expectedPrice int) error {
	if mc.foundTradeGood == nil {
		return fmt.Errorf("no trade good to check price for")
	}
	if mc.foundTradeGood.SellPrice != expectedPrice {
		return fmt.Errorf("expected sell price %d but got %d", expectedPrice, mc.foundTradeGood.SellPrice)
	}
	return nil
}

func (mc *marketContext) iShouldNotFindTheTradeGood() error {
	if mc.found {
		return fmt.Errorf("expected not to find trade good but did")
	}
	if mc.foundTradeGood != nil {
		return fmt.Errorf("expected trade good to be nil")
	}
	return nil
}

func (mc *marketContext) theTransactionLimitShouldBe(expectedLimit int) error {
	if mc.transactionLimit != expectedLimit {
		return fmt.Errorf("expected transaction limit %d but got %d", expectedLimit, mc.transactionLimit)
	}
	return nil
}

func (mc *marketContext) theMarketShouldHaveTheGood() error {
	if !mc.found {
		return fmt.Errorf("expected market to have the good but it does not")
	}
	return nil
}

func (mc *marketContext) theMarketShouldNotHaveTheGood() error {
	if mc.found {
		return fmt.Errorf("expected market not to have the good but it does")
	}
	return nil
}

// InitializeMarketScenario registers all market-related step definitions
func InitializeMarketScenario(ctx *godog.ScenarioContext) {
	mc := &marketContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		mc.reset()
		return ctx, nil
	})

	// Given steps
	ctx.Step(`^a market at waypoint "([^"]*)"$`, mc.aMarketAtWaypoint)
	ctx.Step(`^the market sells "([^"]*)" at (\d+) credits with trade volume (\d+)$`, mc.theMarketSellsAtCreditsWithTradeVolume)
	ctx.Step(`^a market with "([^"]*)" at (\d+) credits$`, mc.aMarketWithAtCredits)
	ctx.Step(`^a market with "([^"]*)" with trade volume (\d+)$`, mc.aMarketWithWithTradeVolume)

	// When steps
	ctx.Step(`^I create the market$`, mc.iCreateTheMarket)
	ctx.Step(`^I create the market with no trade goods$`, mc.iCreateTheMarketWithNoTradeGoods)
	ctx.Step(`^I try to create the market$`, mc.iTryToCreateTheMarket)
	ctx.Step(`^I get trade good "([^"]*)"$`, mc.iGetTradeGood)
	ctx.Step(`^I get transaction limit for "([^"]*)"$`, mc.iGetTransactionLimitFor)
	ctx.Step(`^I check if market has "([^"]*)"$`, mc.iCheckIfMarketHas)

	// Then steps
	ctx.Step(`^the market should be created successfully$`, mc.theMarketShouldBeCreatedSuccessfully)
	ctx.Step(`^the waypoint should be "([^"]*)"$`, mc.theWaypointShouldBe)
	ctx.Step(`^the market should have (\d+) trade goods$`, mc.theMarketShouldHaveTradeGoods)
	ctx.Step(`^I should get an error "([^"]*)"$`, mc.iShouldGetAnError)
	ctx.Step(`^I should find the trade good$`, mc.iShouldFindTheTradeGood)
	ctx.Step(`^the sell price should be (\d+) credits$`, mc.theSellPriceShouldBeCredits)
	ctx.Step(`^I should not find the trade good$`, mc.iShouldNotFindTheTradeGood)
	ctx.Step(`^the transaction limit should be (\d+)$`, mc.theTransactionLimitShouldBe)
	ctx.Step(`^the market should have the good$`, mc.theMarketShouldHaveTheGood)
	ctx.Step(`^the market should not have the good$`, mc.theMarketShouldNotHaveTheGood)
}
