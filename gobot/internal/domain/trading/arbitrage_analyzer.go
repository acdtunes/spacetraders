package trading

import (
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ArbitrageAnalyzer provides pure business logic for analyzing market pairs
// and scoring arbitrage opportunities.
//
// This is a domain service with no infrastructure dependencies (no database, API, etc.).
// All methods are stateless and deterministic.
type ArbitrageAnalyzer struct {
	// Scoring weights (configurable in future)
	profitWeight    float64
	supplyWeight    float64
	activityWeight  float64
	distancePenalty float64
}

// NewArbitrageAnalyzer creates a new analyzer with default scoring weights
func NewArbitrageAnalyzer() *ArbitrageAnalyzer {
	return &ArbitrageAnalyzer{
		profitWeight:    40.0, // 40% weight on profit margin
		supplyWeight:    20.0, // 20% weight on supply availability
		activityWeight:  20.0, // 20% weight on market activity
		distancePenalty: 0.1,  // Minimal distance penalty (tiebreaker)
	}
}

// AnalyzeMarketPair analyzes a buy/sell market pair for arbitrage potential.
//
// Parameters:
//   - good: Trade good symbol
//   - buyMarket: Market where we BUY goods (must export the good)
//   - buyTradeGood: TradeGood data at buy market
//   - sellMarket: Market where we SELL goods (must import the good)
//   - sellTradeGood: TradeGood data at sell market
//   - cargoCapacity: Ship cargo capacity
//   - minMargin: Minimum profit margin threshold
//
// Returns:
//   - ArbitrageOpportunity if viable (profitMargin >= minMargin)
//   - Error if markets cannot be analyzed or no profit possible
func (a *ArbitrageAnalyzer) AnalyzeMarketPair(
	good string,
	buyMarket *market.Market,
	buyTradeGood *market.TradeGood,
	sellMarket *market.Market,
	sellTradeGood *market.TradeGood,
	buyWaypoint *shared.Waypoint,
	sellWaypoint *shared.Waypoint,
	cargoCapacity int,
	minMargin float64,
) (*ArbitrageOpportunity, error) {
	// Validate inputs
	if buyTradeGood == nil || sellTradeGood == nil {
		return nil, fmt.Errorf("trade good data missing for %s", good)
	}

	// Extract prices (from ship's perspective)
	// - We PAY the market's sell_price when buying
	// - We RECEIVE the market's purchase_price when selling
	buyPrice := buyTradeGood.SellPrice()       // What we pay to acquire
	sellPrice := sellTradeGood.PurchasePrice() // What we receive when selling

	// Quick viability check
	if sellPrice <= buyPrice {
		return nil, fmt.Errorf("no profit: sell price (%d) <= buy price (%d)", sellPrice, buyPrice)
	}

	// Extract supply/activity (handle nil pointers)
	buySupply := "MODERATE"
	if buyTradeGood.Supply() != nil {
		buySupply = *buyTradeGood.Supply()
	}

	sellActivity := "WEAK"
	if sellTradeGood.Activity() != nil {
		sellActivity = *sellTradeGood.Activity()
	}

	// Create opportunity
	opp, err := NewArbitrageOpportunity(
		good,
		buyWaypoint,
		sellWaypoint,
		buyPrice,
		sellPrice,
		cargoCapacity,
		buySupply,
		sellActivity,
		minMargin,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create opportunity: %w", err)
	}

	// Calculate and set score
	score := a.ScoreOpportunity(opp)
	opp.SetScore(score)

	return opp, nil
}

// ScoreOpportunity calculates a composite score for an arbitrage opportunity.
//
// Formula:
//
//	score = (profitMargin × 40.0) + (supplyScore × 20.0) + (activityScore × 20.0) - (distance × 0.1)
//
// Component weights:
//   - Profit Margin (40%): Primary driver of profitability
//   - Supply Score (20%): Risk mitigation (ABUNDANT > SCARCE)
//   - Activity Score (20%): Demand stability (STRONG > WEAK)
//   - Distance Penalty (0.1): Fuel efficiency tiebreaker
//
// Higher scores indicate better opportunities.
func (a *ArbitrageAnalyzer) ScoreOpportunity(opp *ArbitrageOpportunity) float64 {
	profitScore := opp.ProfitMargin() * a.profitWeight
	supplyScore := a.SupplyToScore(opp.BuySupply()) * a.supplyWeight
	activityScore := a.ActivityToScore(opp.SellActivity()) * a.activityWeight
	distanceScore := opp.Distance() * a.distancePenalty

	return profitScore + supplyScore + activityScore - distanceScore
}

// SupplyToScore converts supply level to numeric score (0-20).
//
// Scoring rationale:
//   - ABUNDANT (20): Best - high availability, low stockout risk
//   - HIGH (15): Good availability
//   - MODERATE (10): Acceptable
//   - LIMITED (5): Risky - potential stockouts
//   - SCARCE (0): Worst - high risk of price spikes
//
// Higher scores favor markets with abundant supply for stable trading.
func (a *ArbitrageAnalyzer) SupplyToScore(supply string) float64 {
	switch supply {
	case "ABUNDANT":
		return 20.0
	case "HIGH":
		return 15.0
	case "MODERATE":
		return 10.0
	case "LIMITED":
		return 5.0
	case "SCARCE":
		return 0.0
	default:
		return 0.0
	}
}

// ActivityToScore converts activity level to numeric score (0-20).
//
// Scoring rationale:
//   - STRONG (20): Best - high demand, stable prices
//   - GROWING (15): Good - increasing demand
//   - WEAK (5): Poor - low demand, unstable prices
//   - RESTRICTED (0): Worst - may refuse trades
//
// Higher scores favor markets with strong activity for consistent demand.
func (a *ArbitrageAnalyzer) ActivityToScore(activity string) float64 {
	switch activity {
	case "STRONG":
		return 20.0
	case "GROWING":
		return 15.0
	case "WEAK":
		return 5.0
	case "RESTRICTED":
		return 0.0
	default:
		return 0.0
	}
}
