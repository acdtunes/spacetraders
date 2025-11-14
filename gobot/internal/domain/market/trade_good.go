package market

import (
	"errors"
	"fmt"
)

// TradeGood represents a single commodity available at a market (immutable value object).
// Prices follow the market's perspective:
// - PurchasePrice: What the market PAYS when buying from ships (market bids)
// - SellPrice: What the market CHARGES when selling to ships (market asks)
type TradeGood struct {
	symbol        string
	supply        *string // SCARCE, LIMITED, MODERATE, HIGH, ABUNDANT (or nil)
	activity      *string // WEAK, GROWING, STRONG, RESTRICTED (or nil)
	purchasePrice int     // What ship RECEIVES when selling to market
	sellPrice     int     // What ship PAYS when buying from market
	tradeVolume   int     // Trading volume
}

// Valid supply values
var validSupplyValues = map[string]bool{
	"SCARCE":    true,
	"LIMITED":   true,
	"MODERATE":  true,
	"HIGH":      true,
	"ABUNDANT":  true,
}

// Valid activity values
var validActivityValues = map[string]bool{
	"WEAK":       true,
	"GROWING":    true,
	"STRONG":     true,
	"RESTRICTED": true,
}

// NewTradeGood creates a new TradeGood with validation
func NewTradeGood(symbol string, supply *string, activity *string, purchasePrice, sellPrice, tradeVolume int) (*TradeGood, error) {
	// Validate symbol
	if symbol == "" {
		return nil, errors.New("symbol cannot be empty")
	}

	// Validate prices
	if purchasePrice < 0 {
		return nil, errors.New("purchase price must be non-negative")
	}
	if sellPrice < 0 {
		return nil, errors.New("sell price must be non-negative")
	}

	// Validate trade volume
	if tradeVolume < 0 {
		return nil, errors.New("trade volume must be non-negative")
	}

	// Validate supply if provided
	if supply != nil && *supply != "" {
		if !validSupplyValues[*supply] {
			return nil, fmt.Errorf("invalid supply value: %s", *supply)
		}
	}

	// Validate activity if provided
	if activity != nil && *activity != "" {
		if !validActivityValues[*activity] {
			return nil, fmt.Errorf("invalid activity value: %s", *activity)
		}
	}

	return &TradeGood{
		symbol:        symbol,
		supply:        supply,
		activity:      activity,
		purchasePrice: purchasePrice,
		sellPrice:     sellPrice,
		tradeVolume:   tradeVolume,
	}, nil
}

// Getters (TradeGood is immutable, so only provide read access)

func (t *TradeGood) Symbol() string {
	return t.symbol
}

func (t *TradeGood) Supply() *string {
	return t.supply
}

func (t *TradeGood) Activity() *string {
	return t.activity
}

func (t *TradeGood) PurchasePrice() int {
	return t.purchasePrice
}

func (t *TradeGood) SellPrice() int {
	return t.sellPrice
}

func (t *TradeGood) TradeVolume() int {
	return t.tradeVolume
}
