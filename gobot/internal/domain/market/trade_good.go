package market

import (
	"errors"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// TradeGood represents a single commodity available at a market (immutable value object).
// Prices follow the market's perspective:
// - PurchasePrice: What the market PAYS when buying from ships (market bids)
// - SellPrice: What the market CHARGES when selling to ships (market asks)
type TradeGood struct {
	symbol        string
	supply        *string   // SCARCE, LIMITED, MODERATE, HIGH, ABUNDANT (or nil)
	activity      *string   // WEAK, GROWING, STRONG, RESTRICTED (or nil)
	purchasePrice int       // What ship RECEIVES when selling to market
	sellPrice     int       // What ship PAYS when buying from market
	tradeVolume   int       // Trading volume
	tradeType     TradeType // EXPORT, IMPORT, or EXCHANGE
}

// Valid activity values
var validActivityValues = map[string]bool{
	"WEAK":       true,
	"GROWING":    true,
	"STRONG":     true,
	"RESTRICTED": true,
}

// NewTradeGood creates a new TradeGood with validation
func NewTradeGood(symbol string, supply *string, activity *string, purchasePrice, sellPrice, tradeVolume int, tradeType TradeType) (*TradeGood, error) {
	if symbol == "" {
		return nil, errors.New("symbol cannot be empty")
	}

	if purchasePrice < 0 {
		return nil, errors.New("purchase price must be non-negative")
	}
	if sellPrice < 0 {
		return nil, errors.New("sell price must be non-negative")
	}

	if tradeVolume < 0 {
		return nil, errors.New("trade volume must be non-negative")
	}

	// Validate supply if provided
	if supply != nil && *supply != "" {
		if !shared.IsValidSupply(*supply) {
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
		tradeType:     tradeType,
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

func (t *TradeGood) TradeType() TradeType {
	return t.tradeType
}
