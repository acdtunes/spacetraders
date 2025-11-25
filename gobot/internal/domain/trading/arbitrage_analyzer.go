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
//
// STRATEGY (data-driven from 109 successful trades, 2025-11-25):
//
// Hard filters (skip if ANY fails):
//   - Buy supply = HIGH only (2.9% price drift vs 30-70% for others)
//   - Sell activity = WEAK or RESTRICTED (5-14% drift vs 33% for GROWING)
//   - Minimum estimated profit >= 5000 credits (worth the ship time)
//
// Scoring (among filtered opportunities):
//   - Estimated profit (higher = better)
//   - Profit efficiency = profit / distance
//   - Distance penalty (closer = better)
//
// Key insight: The good doesn't determine volatility - market conditions do.
// SHIP_PLATING is fine if supply=HIGH. No blacklists needed.
type ArbitrageAnalyzer struct {
	// Minimum estimated profit to consider (filters low-value trades)
	minEstimatedProfit int
	// Distance penalty factor
	distancePenalty float64
}

// NewArbitrageAnalyzer creates a new analyzer with data-driven filters
//
// Strategy based on analysis of 109 successful trades (2025-11-25):
//   - Buy supply HIGH: 2.9% drift, +79.70 profit/sec, 79% win rate
//   - Sell activity WEAK: 5.1% drift, +8.31 profit/sec
//   - All other conditions: 30-70% drift, negative profit
//
// No supply/activity scoring weights needed - we use HARD FILTERS instead.
func NewArbitrageAnalyzer() *ArbitrageAnalyzer {
	return &ArbitrageAnalyzer{
		minEstimatedProfit: 5000, // Skip trades under 5000 credits
		distancePenalty:    0.1,  // Small penalty for distance
	}
}

// AnalyzeMarketPair analyzes a buy/sell market pair for arbitrage potential.
//
// HARD FILTERS (data-driven from 109 successful trades, 2025-11-25):
//  1. Buy supply MUST be HIGH (2.9% drift vs 30-70% for others)
//  2. Sell activity MUST be WEAK or RESTRICTED (5-14% drift vs 33% for GROWING)
//  3. Estimated profit MUST be >= 5000 credits (worth the ship time)
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
//   - ArbitrageOpportunity if ALL filters pass
//   - Error if any filter fails or markets cannot be analyzed
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

	// Extract supply/activity (handle nil pointers)
	buySupply := "MODERATE"
	if buyTradeGood.Supply() != nil {
		buySupply = *buyTradeGood.Supply()
	}

	sellActivity := "WEAK"
	if sellTradeGood.Activity() != nil {
		sellActivity = *sellTradeGood.Activity()
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// HARD FILTER 1: Buy supply MUST be HIGH
	// ═══════════════════════════════════════════════════════════════════════════
	// Data: HIGH supply = 2.9% price drift, +79.70 profit/sec, 79% win rate
	//       MODERATE    = 32.3% drift, -34.54 profit/sec (LOSING)
	//       LIMITED     = 41.6% drift, -6.81 profit/sec (LOSING)
	//       ABUNDANT    = 69.7% drift, -28.25 profit/sec (LOSING)
	if buySupply != "HIGH" {
		return nil, fmt.Errorf("buy supply %s not HIGH (required for stable prices)", buySupply)
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// HARD FILTER 2: Sell activity MUST be WEAK or RESTRICTED
	// ═══════════════════════════════════════════════════════════════════════════
	// Data: WEAK       = 5.1% sell drift, +8.31 profit/sec
	//       RESTRICTED = 14.5% drift, +96.94 profit/sec (high profit, acceptable risk)
	//       STRONG     = 20.3% drift, -17.46 profit/sec (LOSING)
	//       GROWING    = 33.6% drift, -517.18 profit/sec (CATASTROPHIC)
	if sellActivity != "WEAK" && sellActivity != "RESTRICTED" {
		return nil, fmt.Errorf("sell activity %s not WEAK/RESTRICTED (required for stable prices)", sellActivity)
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

	// ═══════════════════════════════════════════════════════════════════════════
	// HARD FILTER 3: Estimated profit MUST be >= minEstimatedProfit
	// ═══════════════════════════════════════════════════════════════════════════
	// Low-value trades waste ship time. With 40-unit cargo:
	// - 5000 credit min = 125 credits/unit profit minimum
	// - This filters out DIAMONDS (1630 avg), IRON_ORE (788 avg), etc.
	// - Keeps FABRICS (51K), MACHINERY (48K), AMMUNITION (28K), etc.
	estimatedProfit := (sellPrice - buyPrice) * cargoCapacity
	if estimatedProfit < a.minEstimatedProfit {
		return nil, fmt.Errorf("estimated profit %d below minimum %d", estimatedProfit, a.minEstimatedProfit)
	}

	// All hard filters passed - create opportunity
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
// SIMPLIFIED SCORING (2025-11-25):
// Since we now use HARD FILTERS for supply/activity, scoring is purely profit-based:
//
//	score = estimated_profit + profit_efficiency - distance_penalty
//
// Components:
//   - Estimated Profit: Raw profit amount (primary driver)
//   - Profit Efficiency: profit/distance ratio (favors nearby high-profit trades)
//   - Distance Penalty: Small penalty for long routes
//
// All opportunities reaching this function have already passed:
//   - Buy supply = HIGH (stable buy prices)
//   - Sell activity = WEAK/RESTRICTED (stable sell prices)
//   - Minimum profit >= 5000 credits
//
// Higher scores indicate better opportunities.
func (a *ArbitrageAnalyzer) ScoreOpportunity(opp *ArbitrageOpportunity) float64 {
	estimatedProfit := float64(opp.EstimatedProfit())

	// Profit efficiency: profit per distance unit (proxy for profit/time)
	// Favors high-profit trades that are close (maximize profit per second)
	// Add 1.0 to distance to avoid division by zero
	profitEfficiency := estimatedProfit / (opp.Distance() + 1.0)

	// Distance penalty: small penalty for long routes
	distancePenalty := opp.Distance() * a.distancePenalty

	// Final score: profit + efficiency - distance
	// Scale efficiency to be meaningful relative to profit
	return estimatedProfit + (profitEfficiency * 10.0) - distancePenalty
}

// NOTE: SupplyToScore and ActivityToScore methods were removed (2025-11-25).
// We now use HARD FILTERS instead of weighted scoring for supply/activity.
// See AnalyzeMarketPair for the filter logic.
