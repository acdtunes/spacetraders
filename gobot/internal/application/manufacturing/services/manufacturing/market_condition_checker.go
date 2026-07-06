package manufacturing

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// MarketConditionChecker evaluates market conditions for task readiness.
type MarketConditionChecker struct {
	marketRepo    market.MarketRepository
	readinessSpec *manufacturing.TaskReadinessSpecification
}

// NewMarketConditionChecker creates a new market condition checker.
func NewMarketConditionChecker(
	marketRepo market.MarketRepository,
	readinessSpec *manufacturing.TaskReadinessSpecification,
) *MarketConditionChecker {
	if readinessSpec == nil {
		readinessSpec = manufacturing.NewTaskReadinessSpecification()
	}
	return &MarketConditionChecker{
		marketRepo:    marketRepo,
		readinessSpec: readinessSpec,
	}
}

// IsSellMarketSaturated checks if the sell market has HIGH or ABUNDANT supply.
// A saturated market means prices will be low and we should avoid selling there.
func (c *MarketConditionChecker) IsSellMarketSaturated(
	ctx context.Context,
	sellMarket, good string,
	playerID int,
) bool {
	supply := c.getSupplyLevel(ctx, sellMarket, good, playerID)
	return supply.IsSaturated()
}

// IsFactoryOutputReady checks if factory has HIGH/ABUNDANT supply for collection.
// This is the execution check (more lenient than assignment).
func (c *MarketConditionChecker) IsFactoryOutputReady(
	ctx context.Context,
	factorySymbol, outputGood string,
	playerID int,
) bool {
	supply := c.getSupplyLevel(ctx, factorySymbol, outputGood, playerID)
	return supply.IsFavorableForCollection()
}

// IsFactoryInputSaturated checks if factory already has HIGH/ABUNDANT supply of an input.
// If saturated, we don't need to deliver more of this input.
func (c *MarketConditionChecker) IsFactoryInputSaturated(
	ctx context.Context,
	factorySymbol, inputGood string,
	playerID int,
) bool {
	supply := c.getSupplyLevel(ctx, factorySymbol, inputGood, playerID)
	return supply.IsSaturated()
}

func (c *MarketConditionChecker) getSupplyLevel(
	ctx context.Context,
	waypointSymbol, good string,
	playerID int,
) manufacturing.SupplyLevel {
	if c.marketRepo == nil {
		return manufacturing.SupplyLevelModerate // Default when can't check
	}

	marketData, err := c.marketRepo.GetMarketData(ctx, waypointSymbol, playerID)
	if err != nil || marketData == nil {
		return manufacturing.SupplyLevelModerate // Default when can't check
	}

	tradeGood := marketData.FindGood(good)
	if tradeGood == nil || tradeGood.Supply() == nil {
		return manufacturing.SupplyLevelModerate // Default when can't check
	}

	return manufacturing.ParseSupplyLevel(*tradeGood.Supply())
}
