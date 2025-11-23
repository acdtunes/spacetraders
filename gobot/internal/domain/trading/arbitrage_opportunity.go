package trading

import (
	"errors"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ArbitrageOpportunity represents an immutable profitable trading opportunity.
// It captures all the data needed to evaluate and execute a buy->navigate->sell arbitrage cycle.
//
// Price Terminology (from ship's perspective):
//   - BuyPrice: What we PAY to acquire goods (market's sell_price)
//   - SellPrice: What we RECEIVE when selling goods (market's purchase_price)
//
// Immutability: All fields are private with read-only getters to ensure value object semantics.
type ArbitrageOpportunity struct {
	good            string
	buyMarket       *shared.Waypoint
	sellMarket      *shared.Waypoint
	buyPrice        int     // What we pay (market sell_price)
	sellPrice       int     // What we receive (market purchase_price)
	profitPerUnit   int     // sellPrice - buyPrice
	profitMargin    float64 // (profitPerUnit / buyPrice) × 100
	distance        float64 // Euclidean distance between markets
	estimatedProfit int     // profitPerUnit × cargoCapacity
	cargoCapacity   int     // Ship cargo capacity
	buySupply       string  // SCARCE, LIMITED, MODERATE, HIGH, ABUNDANT
	sellActivity    string  // WEAK, GROWING, STRONG, RESTRICTED
	score           float64 // Calculated via scoring algorithm
	viability       bool    // profitMargin >= minMargin
}

// NewArbitrageOpportunity creates a new arbitrage opportunity with validation.
// All calculations (profitPerUnit, profitMargin, estimatedProfit, score, viability) are performed during construction.
//
// Parameters:
//   - good: Trade good symbol (e.g., "IRON_ORE", "PRECIOUS_STONES")
//   - buyMarket: Waypoint where we purchase goods (export market)
//   - sellMarket: Waypoint where we sell goods (import market)
//   - buyPrice: What we pay per unit (market's sell_price)
//   - sellPrice: What we receive per unit (market's purchase_price)
//   - cargoCapacity: Ship cargo capacity in units
//   - buySupply: Supply level at buy market (SCARCE, LIMITED, MODERATE, HIGH, ABUNDANT)
//   - sellActivity: Activity level at sell market (WEAK, GROWING, STRONG, RESTRICTED)
//   - minMargin: Minimum profit margin threshold (e.g., 10.0 for 10%)
//
// Returns error if:
//   - good symbol is empty
//   - buyMarket or sellMarket is nil
//   - prices are non-positive
//   - sellPrice <= buyPrice (no profit possible)
//   - cargoCapacity is non-positive
//   - supply or activity values are invalid
func NewArbitrageOpportunity(
	good string,
	buyMarket *shared.Waypoint,
	sellMarket *shared.Waypoint,
	buyPrice int,
	sellPrice int,
	cargoCapacity int,
	buySupply string,
	sellActivity string,
	minMargin float64,
) (*ArbitrageOpportunity, error) {
	// Validate inputs
	if good == "" {
		return nil, errors.New("good symbol required")
	}
	if buyMarket == nil {
		return nil, errors.New("buy market required")
	}
	if sellMarket == nil {
		return nil, errors.New("sell market required")
	}
	if buyPrice <= 0 {
		return nil, errors.New("buy price must be positive")
	}
	if sellPrice <= 0 {
		return nil, errors.New("sell price must be positive")
	}
	if sellPrice <= buyPrice {
		return nil, fmt.Errorf("sell price (%d) must exceed buy price (%d)", sellPrice, buyPrice)
	}
	if cargoCapacity <= 0 {
		return nil, errors.New("cargo capacity must be positive")
	}
	if !isValidSupply(buySupply) {
		return nil, fmt.Errorf("invalid supply value: %s", buySupply)
	}
	if !isValidActivity(sellActivity) {
		return nil, fmt.Errorf("invalid activity value: %s", sellActivity)
	}

	// Calculate derived values
	profitPerUnit := sellPrice - buyPrice
	profitMargin := (float64(profitPerUnit) / float64(buyPrice)) * 100.0
	estimatedProfit := profitPerUnit * cargoCapacity
	distance := buyMarket.DistanceTo(sellMarket)
	viability := profitMargin >= minMargin

	opp := &ArbitrageOpportunity{
		good:            good,
		buyMarket:       buyMarket,
		sellMarket:      sellMarket,
		buyPrice:        buyPrice,
		sellPrice:       sellPrice,
		profitPerUnit:   profitPerUnit,
		profitMargin:    profitMargin,
		distance:        distance,
		estimatedProfit: estimatedProfit,
		cargoCapacity:   cargoCapacity,
		buySupply:       buySupply,
		sellActivity:    sellActivity,
		viability:       viability,
		score:           0, // Will be set by analyzer
	}

	return opp, nil
}

// Getters - provide read-only access to maintain immutability

func (o *ArbitrageOpportunity) Good() string {
	return o.good
}

func (o *ArbitrageOpportunity) BuyMarket() *shared.Waypoint {
	return o.buyMarket
}

func (o *ArbitrageOpportunity) SellMarket() *shared.Waypoint {
	return o.sellMarket
}

func (o *ArbitrageOpportunity) BuyPrice() int {
	return o.buyPrice
}

func (o *ArbitrageOpportunity) SellPrice() int {
	return o.sellPrice
}

func (o *ArbitrageOpportunity) ProfitPerUnit() int {
	return o.profitPerUnit
}

func (o *ArbitrageOpportunity) ProfitMargin() float64 {
	return o.profitMargin
}

func (o *ArbitrageOpportunity) Distance() float64 {
	return o.distance
}

func (o *ArbitrageOpportunity) EstimatedProfit() int {
	return o.estimatedProfit
}

func (o *ArbitrageOpportunity) CargoCapacity() int {
	return o.cargoCapacity
}

func (o *ArbitrageOpportunity) BuySupply() string {
	return o.buySupply
}

func (o *ArbitrageOpportunity) SellActivity() string {
	return o.sellActivity
}

func (o *ArbitrageOpportunity) Score() float64 {
	return o.score
}

func (o *ArbitrageOpportunity) IsViable() bool {
	return o.viability
}

// SetScore updates the score (called by analyzer after construction)
func (o *ArbitrageOpportunity) SetScore(score float64) {
	o.score = score
}

// EstimatedNetProfit calculates net profit after deducting fuel costs
func (o *ArbitrageOpportunity) EstimatedNetProfit(fuelCost int) int {
	return o.estimatedProfit - fuelCost
}

// Valid supply values
var validSupply = map[string]bool{
	"SCARCE":   true,
	"LIMITED":  true,
	"MODERATE": true,
	"HIGH":     true,
	"ABUNDANT": true,
}

// Valid activity values
var validActivity = map[string]bool{
	"WEAK":       true,
	"GROWING":    true,
	"STRONG":     true,
	"RESTRICTED": true,
}

func isValidSupply(supply string) bool {
	return validSupply[supply]
}

func isValidActivity(activity string) bool {
	return validActivity[activity]
}

// String returns a human-readable representation
func (o *ArbitrageOpportunity) String() string {
	return fmt.Sprintf("ArbitrageOpportunity{good=%s, margin=%.1f%%, profit=%d, score=%.0f}",
		o.good, o.profitMargin, o.estimatedProfit, o.score)
}
