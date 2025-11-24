package market

import (
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// MarketPriceHistory represents a point-in-time snapshot of market prices.
// Used for volatility analysis and ML training.
// This is an immutable entity - all fields are private with getters only.
type MarketPriceHistory struct {
	id             int
	waypointSymbol string
	goodSymbol     string
	playerID       shared.PlayerID
	purchasePrice  int    // What market pays us to sell
	sellPrice      int    // What market charges us to buy
	supply         *string // SCARCE, LIMITED, MODERATE, HIGH, ABUNDANT (or nil)
	activity       *string // WEAK, GROWING, STRONG, RESTRICTED (or nil)
	tradeVolume    int
	recordedAt     time.Time
}

// NewMarketPriceHistory creates a new price history entry with validation.
// The recordedAt timestamp is set to the current time automatically.
func NewMarketPriceHistory(
	waypointSymbol string,
	goodSymbol string,
	playerID shared.PlayerID,
	purchasePrice int,
	sellPrice int,
	supply *string,
	activity *string,
	tradeVolume int,
) (*MarketPriceHistory, error) {
	// Validate waypoint symbol
	if waypointSymbol == "" {
		return nil, ErrInvalidWaypointSymbol
	}

	// Validate good symbol
	if goodSymbol == "" {
		return nil, ErrInvalidGoodSymbol
	}

	// Validate player ID
	if playerID.IsZero() {
		return nil, ErrInvalidPlayerID
	}

	// Validate prices
	if purchasePrice < 0 {
		return nil, ErrInvalidPrice
	}
	if sellPrice < 0 {
		return nil, ErrInvalidPrice
	}

	// Validate trade volume
	if tradeVolume < 0 {
		return nil, ErrInvalidTradeVolume
	}

	// Validate supply if provided
	if supply != nil && *supply != "" {
		if !validSupplyValues[*supply] {
			return nil, ErrInvalidSupply
		}
	}

	// Validate activity if provided
	if activity != nil && *activity != "" {
		if !validActivityValues[*activity] {
			return nil, ErrInvalidActivity
		}
	}

	return &MarketPriceHistory{
		waypointSymbol: waypointSymbol,
		goodSymbol:     goodSymbol,
		playerID:       playerID,
		purchasePrice:  purchasePrice,
		sellPrice:      sellPrice,
		supply:         supply,
		activity:       activity,
		tradeVolume:    tradeVolume,
		recordedAt:     time.Now(),
	}, nil
}

// NewMarketPriceHistoryWithID creates a price history entry with an existing ID.
// This is used when loading from the database.
func NewMarketPriceHistoryWithID(
	id int,
	waypointSymbol string,
	goodSymbol string,
	playerID shared.PlayerID,
	purchasePrice int,
	sellPrice int,
	supply *string,
	activity *string,
	tradeVolume int,
	recordedAt time.Time,
) (*MarketPriceHistory, error) {
	history, err := NewMarketPriceHistory(
		waypointSymbol,
		goodSymbol,
		playerID,
		purchasePrice,
		sellPrice,
		supply,
		activity,
		tradeVolume,
	)
	if err != nil {
		return nil, err
	}

	history.id = id
	history.recordedAt = recordedAt
	return history, nil
}

// Getters (immutable entity - no setters)

func (h *MarketPriceHistory) ID() int {
	return h.id
}

func (h *MarketPriceHistory) WaypointSymbol() string {
	return h.waypointSymbol
}

func (h *MarketPriceHistory) GoodSymbol() string {
	return h.goodSymbol
}

func (h *MarketPriceHistory) PlayerID() shared.PlayerID {
	return h.playerID
}

func (h *MarketPriceHistory) PurchasePrice() int {
	return h.purchasePrice
}

func (h *MarketPriceHistory) SellPrice() int {
	return h.sellPrice
}

func (h *MarketPriceHistory) Supply() *string {
	return h.supply
}

func (h *MarketPriceHistory) Activity() *string {
	return h.activity
}

func (h *MarketPriceHistory) TradeVolume() int {
	return h.tradeVolume
}

func (h *MarketPriceHistory) RecordedAt() time.Time {
	return h.recordedAt
}

// PriceSpread returns the bid-ask spread as a percentage.
// This represents the markup between what the market pays and what it charges.
func (h *MarketPriceHistory) PriceSpread() float64 {
	if h.purchasePrice == 0 {
		return 0
	}
	return float64(h.sellPrice-h.purchasePrice) / float64(h.purchasePrice) * 100
}
