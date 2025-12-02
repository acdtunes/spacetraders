package manufacturing

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Supply-aware multipliers from design doc
// See docs/PARALLEL_MANUFACTURING_SYSTEM_DESIGN.md - Trade Size Calculation
// This prevents crashing supply by limiting purchase based on supply scarcity
var supplyMultipliers = map[string]float64{
	"ABUNDANT": 0.80, // Plenty of buffer
	"HIGH":     0.60, // Sweet spot - maintain stability
	"MODERATE": 0.40, // Careful - could drop to LIMITED
	"LIMITED":  0.20, // Very careful - critical supply
	"SCARCE":   0.10, // Minimal - supply nearly depleted
}

// Activity-based modifiers for position sizing (applied to base supply multipliers)
// Based on data analysis: WEAK activity at EXPORT = lowest prices (buy more aggressive)
// STRONG activity at EXPORT = highest prices (buy more conservative)
var activityModifiers = map[string]float64{
	"WEAK":       1.15, // Low activity = low prices = buy 15% more
	"GROWING":    1.05, // Moderate = buy 5% more
	"STRONG":     0.85, // High activity = higher prices = buy 15% less
	"RESTRICTED": 0.75, // Restricted = buy 25% less (worst prices)
}

const (
	// DefaultSupplyMultiplier is used when supply level is unknown
	DefaultSupplyMultiplier = 0.40 // Conservative (MODERATE)

	// DefaultActivityModifier is used when activity level is unknown
	DefaultActivityModifier = 1.0 // No adjustment

	// MaxPurchaseRounds limits the purchase loop iterations
	MaxPurchaseRounds = 10
)

// Purchaser handles purchase operations with supply-aware limits.
// Consolidates the repeated purchase loop pattern found throughout the task worker.
type Purchaser interface {
	// ExecutePurchaseLoop performs iterative purchasing until cargo full or limit reached
	ExecutePurchaseLoop(ctx context.Context, params PurchaseLoopParams) (*PurchaseResult, error)

	// CalculateSupplyAwareLimit determines safe purchase quantity based on supply and activity level.
	// Activity modifiers adjust the base supply multiplier:
	// - WEAK activity = lower prices = buy more aggressively (+15%)
	// - STRONG activity = higher prices = buy more conservatively (-15%)
	CalculateSupplyAwareLimit(supply, activity string, tradeVolume int) int
}

// PurchaseLoopParams contains parameters for the purchase loop.
type PurchaseLoopParams struct {
	ShipSymbol        string
	PlayerID          shared.PlayerID
	Good              string
	TaskID            string
	DesiredQty        int    // 0 = fill cargo
	Market            string // Where to purchase
	Factory           string // For ledger context
	RequireHighSupply bool   // For COLLECT_SELL: require HIGH/ABUNDANT
}

// PurchaseResult contains the results of a purchase loop.
type PurchaseResult struct {
	TotalUnitsAdded int
	TotalCost       int
	Rounds          int
}

// ManufacturingPurchaser implements Purchaser for manufacturing operations.
type ManufacturingPurchaser struct {
	mediator       common.Mediator
	shipRepo       navigation.ShipRepository
	marketRepo     market.MarketRepository
	ledgerRecorder LedgerRecorder
}

// NewManufacturingPurchaser creates a new purchaser service.
func NewManufacturingPurchaser(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	marketRepo market.MarketRepository,
	ledgerRecorder LedgerRecorder,
) *ManufacturingPurchaser {
	return &ManufacturingPurchaser{
		mediator:       mediator,
		shipRepo:       shipRepo,
		marketRepo:     marketRepo,
		ledgerRecorder: ledgerRecorder,
	}
}

// ExecutePurchaseLoop performs iterative purchasing until cargo full or limit reached.
// OPTIMIZED: Loads ship once at the start and tracks cargo locally to avoid repeated GetShip API calls.
func (p *ManufacturingPurchaser) ExecutePurchaseLoop(
	ctx context.Context,
	params PurchaseLoopParams,
) (*PurchaseResult, error) {
	logger := common.LoggerFromContext(ctx)

	result := &PurchaseResult{}
	playerIDInt := params.PlayerID.Value()

	// OPTIMIZATION: Load ship ONCE at the start instead of every loop iteration
	// This saves N-1 GetShip API calls where N is the number of purchase rounds
	ship, err := p.shipRepo.FindBySymbol(ctx, params.ShipSymbol, params.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("failed to load ship: %w", err)
	}

	// PRE-CHECK: If ship already has cargo of the target good, return early for RequireHighSupply tasks
	if params.RequireHighSupply {
		existingCargo := ship.Cargo().GetItemUnits(params.Good)
		if existingCargo > 0 {
			logger.Log("INFO", "Ship already has cargo - skipping purchase loop", map[string]interface{}{
				"good":     params.Good,
				"quantity": existingCargo,
			})
			result.TotalUnitsAdded = existingCargo
			return result, nil
		}
	}

	// OPTIMIZATION: Track available cargo locally instead of reloading ship each iteration
	// After each purchase, we subtract the units added from our local tracking
	availableCargo := ship.AvailableCargoSpace()

	for result.Rounds < MaxPurchaseRounds {
		// Check cargo space using local tracking (no API call)
		if availableCargo <= 0 {
			break
		}

		// Get fresh market data (from DB cache, updated by market refresh after purchase)
		marketData, err := p.marketRepo.GetMarketData(ctx, params.Market, playerIDInt)
		if err != nil {
			if result.Rounds == 0 {
				return nil, fmt.Errorf("failed to get market data: %w", err)
			}
			break // Already have some goods, continue with what we have
		}

		// Find supply level, activity, and trade volume
		var supplyLevel string
		var activityLevel string
		var tradeVolume int
		for _, good := range marketData.TradeGoods() {
			if good.Symbol() == params.Good {
				if good.Supply() != nil {
					supplyLevel = *good.Supply()
				}
				if good.Activity() != nil {
					activityLevel = *good.Activity()
				}
				tradeVolume = good.TradeVolume()
				break
			}
		}

		// For COLLECT_SELL tasks, require HIGH or ABUNDANT supply
		if params.RequireHighSupply {
			if supplyLevel == "" {
				if result.Rounds == 0 {
					return nil, fmt.Errorf("factory %s does not export %s", params.Market, params.Good)
				}
				break
			}

			if supplyLevel != "HIGH" && supplyLevel != "ABUNDANT" {
				if result.Rounds == 0 {
					return nil, fmt.Errorf("factory %s supply is %s, need HIGH or ABUNDANT - will retry", params.Market, supplyLevel)
				}
				break
			}
		}

		// Determine quantity using local cargo tracking
		quantity := availableCargo
		if params.DesiredQty > 0 {
			remaining := params.DesiredQty - result.TotalUnitsAdded
			if remaining <= 0 {
				break
			}
			if remaining < quantity {
				quantity = remaining
			}
		}

		// Apply supply and activity-aware limit
		supplyAwareLimit := p.CalculateSupplyAwareLimit(supplyLevel, activityLevel, tradeVolume)
		if supplyAwareLimit > 0 && supplyAwareLimit < quantity {
			quantity = supplyAwareLimit
		}

		if quantity <= 0 {
			break
		}

		// Keep market refresh to detect price/supply movement from our buys
		// The next loop iteration's GetMarketData() reads from DB cache, so we need the refresh
		purchaseResp, err := p.mediator.Send(ctx, &shipCargo.PurchaseCargoCommand{
			ShipSymbol: params.ShipSymbol,
			GoodSymbol: params.Good,
			Units:      quantity,
			PlayerID:   params.PlayerID,
		})
		if err != nil {
			if result.Rounds == 0 {
				return nil, fmt.Errorf("failed to purchase %s: %w", params.Good, err)
			}
			break
		}

		resp := purchaseResp.(*shipCargo.PurchaseCargoResponse)
		result.TotalUnitsAdded += resp.UnitsAdded
		result.TotalCost += resp.TotalCost
		result.Rounds++

		// OPTIMIZATION: Update local cargo tracking instead of reloading ship
		availableCargo -= resp.UnitsAdded

		pricePerUnit := 0
		if resp.UnitsAdded > 0 {
			pricePerUnit = resp.TotalCost / resp.UnitsAdded
		}

		logger.Log("INFO", "Purchased goods", map[string]interface{}{
			"good":           params.Good,
			"units":          resp.UnitsAdded,
			"cost":           resp.TotalCost,
			"price_per_unit": pricePerUnit,
			"supply":         supplyLevel,
			"activity":       activityLevel,
			"round":          result.Rounds,
			"cargo_remaining": availableCargo,
		})

		// Record ledger transaction
		if p.ledgerRecorder != nil {
			_ = p.ledgerRecorder.RecordPurchase(ctx, PurchaseRecordParams{
				PlayerID:     playerIDInt,
				TaskID:       params.TaskID,
				Good:         params.Good,
				Quantity:     resp.UnitsAdded,
				PricePerUnit: pricePerUnit,
				TotalCost:    resp.TotalCost,
				SourceMarket: params.Market,
				Factory:      params.Factory,
				SupplyLevel:  supplyLevel,
			})
		}

		if resp.UnitsAdded == 0 {
			break
		}
	}

	return result, nil
}

// CalculateSupplyAwareLimit determines safe purchase quantity based on supply and activity level.
// Activity modifiers adjust the base supply multiplier based on data analysis showing:
// - WEAK activity at EXPORT markets = lowest prices = buy more aggressively
// - STRONG activity at EXPORT markets = highest prices = buy more conservatively
// See docs/PARALLEL_MANUFACTURING_SYSTEM_DESIGN.md - Trade Size Calculation
func (p *ManufacturingPurchaser) CalculateSupplyAwareLimit(supply, activity string, tradeVolume int) int {
	if tradeVolume <= 0 {
		return 0 // No limit if trade volume unknown
	}

	// Base multiplier from supply level
	multiplier, ok := supplyMultipliers[supply]
	if !ok {
		multiplier = DefaultSupplyMultiplier // Default to conservative (MODERATE)
	}

	// Activity modifier adjusts the base multiplier
	activityMod, ok := activityModifiers[activity]
	if !ok {
		activityMod = DefaultActivityModifier // No adjustment if unknown
	}

	// Apply activity modifier to base multiplier
	adjustedMultiplier := multiplier * activityMod

	// Cap at 1.0 to never exceed trade volume
	if adjustedMultiplier > 1.0 {
		adjustedMultiplier = 1.0
	}

	return int(float64(tradeVolume) * adjustedMultiplier)
}
