package manufacturing

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands"
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

const (
	// DefaultSupplyMultiplier is used when supply level is unknown
	DefaultSupplyMultiplier = 0.40 // Conservative (MODERATE)

	// MaxPurchaseRounds limits the purchase loop iterations
	MaxPurchaseRounds = 10
)

// Purchaser handles purchase operations with supply-aware limits.
// Consolidates the repeated purchase loop pattern found throughout the task worker.
type Purchaser interface {
	// ExecutePurchaseLoop performs iterative purchasing until cargo full or limit reached
	ExecutePurchaseLoop(ctx context.Context, params PurchaseLoopParams) (*PurchaseResult, error)

	// CalculateSupplyAwareLimit determines safe purchase quantity based on supply level
	CalculateSupplyAwareLimit(supply string, tradeVolume int) int
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
func (p *ManufacturingPurchaser) ExecutePurchaseLoop(
	ctx context.Context,
	params PurchaseLoopParams,
) (*PurchaseResult, error) {
	logger := common.LoggerFromContext(ctx)

	result := &PurchaseResult{}
	playerIDInt := params.PlayerID.Value()

	// PRE-CHECK: If ship already has cargo, check before starting purchase loop.
	// This handles the case where a retry task is assigned to a ship that already
	// has cargo from a previous attempt but supply has dropped.
	if params.RequireHighSupply {
		ship, err := p.shipRepo.FindBySymbol(ctx, params.ShipSymbol, params.PlayerID)
		if err == nil && ship != nil {
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
	}

	for result.Rounds < MaxPurchaseRounds {
		// Get fresh market data
		marketData, err := p.marketRepo.GetMarketData(ctx, params.Market, playerIDInt)
		if err != nil {
			if result.Rounds == 0 {
				return nil, fmt.Errorf("failed to get market data: %w", err)
			}
			break // Already have some goods, continue with what we have
		}

		// Find supply level and trade volume
		var supplyLevel string
		var tradeVolume int
		for _, good := range marketData.TradeGoods() {
			if good.Symbol() == params.Good {
				if good.Supply() != nil {
					supplyLevel = *good.Supply()
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

		// Reload ship for available cargo
		ship, err := p.shipRepo.FindBySymbol(ctx, params.ShipSymbol, params.PlayerID)
		if err != nil {
			return nil, fmt.Errorf("failed to reload ship: %w", err)
		}

		availableCargo := ship.AvailableCargoSpace()
		if availableCargo <= 0 {
			break
		}

		// Determine quantity
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

		// Apply supply-aware limit
		supplyAwareLimit := p.CalculateSupplyAwareLimit(supplyLevel, tradeVolume)
		if supplyAwareLimit > 0 && supplyAwareLimit < quantity {
			quantity = supplyAwareLimit
		}

		if quantity <= 0 {
			break
		}

		// Purchase goods
		purchaseResp, err := p.mediator.Send(ctx, &shipCmd.PurchaseCargoCommand{
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

		resp := purchaseResp.(*shipCmd.PurchaseCargoResponse)
		result.TotalUnitsAdded += resp.UnitsAdded
		result.TotalCost += resp.TotalCost
		result.Rounds++

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
			"round":          result.Rounds,
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

// CalculateSupplyAwareLimit determines safe purchase quantity based on supply level.
// See docs/PARALLEL_MANUFACTURING_SYSTEM_DESIGN.md - Trade Size Calculation
func (p *ManufacturingPurchaser) CalculateSupplyAwareLimit(supply string, tradeVolume int) int {
	if tradeVolume <= 0 {
		return 0 // No limit if trade volume unknown
	}

	multiplier, ok := supplyMultipliers[supply]
	if !ok {
		multiplier = DefaultSupplyMultiplier // Default to conservative (MODERATE)
	}

	return int(float64(tradeVolume) * multiplier)
}
