package steps

import (
	"context"
	"fmt"
	"strings"

	"github.com/cucumber/godog"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// tradeGoodContext holds state for trade good scenarios
type tradeGoodContext struct {
	tradeGood    *market.TradeGood
	currentError error
}

func (tc *tradeGoodContext) reset() {
	tc.tradeGood = nil
	tc.currentError = nil
}

func InitializeTradeGoodSteps(ctx *godog.ScenarioContext) {
	tc := &tradeGoodContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		tc.reset()
		return ctx, nil
	})

	// Background
	ctx.Step(`^I have market trade good data$`, tc.iHaveMarketMarketData)

	// Trade good creation - successful
	ctx.Step(`^I create a trade good with symbol "([^"]*)", supply "([^"]*)", activity "([^"]*)", purchase price (\d+), sell price (\d+), and trade volume (\d+)$`,
		tc.iCreateATradeGoodWithAllFields)
	ctx.Step(`^I create a trade good with symbol "([^"]*)", no supply, no activity, purchase price (\d+), sell price (\d+), and trade volume (\d+)$`,
		tc.iCreateATradeGoodWithNilFields)

	// Trade good creation - failures
	ctx.Step(`^I attempt to create a trade good with symbol "([^"]*)", supply "([^"]*)", activity "([^"]*)", purchase price (-?\d+), sell price (-?\d+), and trade volume (-?\d+)$`,
		tc.iAttemptToCreateATradeGood)

	// Assertions
	ctx.Step(`^the trade good should have symbol "([^"]*)"$`, tc.theTradeGoodShouldHaveSymbol)
	ctx.Step(`^the trade good should have supply "([^"]*)"$`, tc.theTradeGoodShouldHaveSupply)
	ctx.Step(`^the trade good should have activity "([^"]*)"$`, tc.theTradeGoodShouldHaveActivity)
	ctx.Step(`^the trade good should have purchase price (\d+)$`, tc.theTradeGoodShouldHavePurchasePrice)
	ctx.Step(`^the trade good should have sell price (\d+)$`, tc.theTradeGoodShouldHaveSellPrice)
	ctx.Step(`^the trade good should have trade volume (\d+)$`, tc.theTradeGoodShouldHaveTradeVolume)
	ctx.Step(`^the trade good should have nil supply$`, tc.theTradeGoodShouldHaveNilSupply)
	ctx.Step(`^the trade good should have nil activity$`, tc.theTradeGoodShouldHaveNilActivity)

	// Error assertions - specific to trade good validation errors
	ctx.Step(`^I should get an error "([^"]*)"$`, tc.iShouldGetAnError)
}

func (tc *tradeGoodContext) iHaveMarketMarketData() error {
	// Background setup - nothing to do
	return nil
}

func (tc *tradeGoodContext) iCreateATradeGoodWithAllFields(symbol, supply, activity string, purchasePrice, sellPrice, tradeVolume int) error {
	supplyPtr := &supply
	activityPtr := &activity

	tradeGood, err := market.NewTradeGood(symbol, supplyPtr, activityPtr, purchasePrice, sellPrice, tradeVolume)
	if err != nil {
		return fmt.Errorf("unexpected error creating trade good: %w", err)
	}

	tc.tradeGood = tradeGood
	return nil
}

func (tc *tradeGoodContext) iCreateATradeGoodWithNilFields(symbol string, purchasePrice, sellPrice, tradeVolume int) error {
	tradeGood, err := market.NewTradeGood(symbol, nil, nil, purchasePrice, sellPrice, tradeVolume)
	if err != nil {
		return fmt.Errorf("unexpected error creating trade good: %w", err)
	}

	tc.tradeGood = tradeGood
	return nil
}

func (tc *tradeGoodContext) iAttemptToCreateATradeGood(symbol, supply, activity string, purchasePrice, sellPrice, tradeVolume int) error {
	supplyPtr := &supply
	activityPtr := &activity

	tradeGood, err := market.NewTradeGood(symbol, supplyPtr, activityPtr, purchasePrice, sellPrice, tradeVolume)
	tc.currentError = err
	sharedErr = err // Share error for cross-context assertions
	tc.tradeGood = tradeGood
	return nil
}

func (tc *tradeGoodContext) theTradeGoodShouldHaveSymbol(expectedSymbol string) error {
	if tc.tradeGood == nil {
		return fmt.Errorf("trade good should not be nil")
	}
	if tc.tradeGood.Symbol() != expectedSymbol {
		return fmt.Errorf("expected symbol '%s' but got '%s'", expectedSymbol, tc.tradeGood.Symbol())
	}
	return nil
}

func (tc *tradeGoodContext) theTradeGoodShouldHaveSupply(expectedSupply string) error {
	if tc.tradeGood == nil {
		return fmt.Errorf("trade good should not be nil")
	}
	if tc.tradeGood.Supply() == nil {
		return fmt.Errorf("supply should not be nil")
	}
	if *tc.tradeGood.Supply() != expectedSupply {
		return fmt.Errorf("expected supply '%s' but got '%s'", expectedSupply, *tc.tradeGood.Supply())
	}
	return nil
}

func (tc *tradeGoodContext) theTradeGoodShouldHaveActivity(expectedActivity string) error {
	if tc.tradeGood == nil {
		return fmt.Errorf("trade good should not be nil")
	}
	if tc.tradeGood.Activity() == nil {
		return fmt.Errorf("activity should not be nil")
	}
	if *tc.tradeGood.Activity() != expectedActivity {
		return fmt.Errorf("expected activity '%s' but got '%s'", expectedActivity, *tc.tradeGood.Activity())
	}
	return nil
}

func (tc *tradeGoodContext) theTradeGoodShouldHavePurchasePrice(expectedPrice int) error {
	if tc.tradeGood == nil {
		return fmt.Errorf("trade good should not be nil")
	}
	if tc.tradeGood.PurchasePrice() != expectedPrice {
		return fmt.Errorf("expected purchase price %d but got %d", expectedPrice, tc.tradeGood.PurchasePrice())
	}
	return nil
}

func (tc *tradeGoodContext) theTradeGoodShouldHaveSellPrice(expectedPrice int) error {
	if tc.tradeGood == nil {
		return fmt.Errorf("trade good should not be nil")
	}
	if tc.tradeGood.SellPrice() != expectedPrice {
		return fmt.Errorf("expected sell price %d but got %d", expectedPrice, tc.tradeGood.SellPrice())
	}
	return nil
}

func (tc *tradeGoodContext) theTradeGoodShouldHaveTradeVolume(expectedVolume int) error {
	if tc.tradeGood == nil {
		return fmt.Errorf("trade good should not be nil")
	}
	if tc.tradeGood.TradeVolume() != expectedVolume {
		return fmt.Errorf("expected trade volume %d but got %d", expectedVolume, tc.tradeGood.TradeVolume())
	}
	return nil
}

func (tc *tradeGoodContext) theTradeGoodShouldHaveNilSupply() error {
	if tc.tradeGood == nil {
		return fmt.Errorf("trade good should not be nil")
	}
	if tc.tradeGood.Supply() != nil {
		return fmt.Errorf("expected supply to be nil but got '%s'", *tc.tradeGood.Supply())
	}
	return nil
}

func (tc *tradeGoodContext) theTradeGoodShouldHaveNilActivity() error {
	if tc.tradeGood == nil {
		return fmt.Errorf("trade good should not be nil")
	}
	if tc.tradeGood.Activity() != nil {
		return fmt.Errorf("expected activity to be nil but got '%s'", *tc.tradeGood.Activity())
	}
	return nil
}

// iShouldGetAnError is handled by other step definitions - we check tc.currentError directly in scenarios
func (tc *tradeGoodContext) iShouldGetAnError(expectedErrorMsg string) error {
	if tc.currentError == nil {
		return fmt.Errorf("expected an error but got nil")
	}
	if !strings.Contains(tc.currentError.Error(), expectedErrorMsg) {
		return fmt.Errorf("expected error containing '%s' but got '%s'", expectedErrorMsg, tc.currentError.Error())
	}
	return nil
}
