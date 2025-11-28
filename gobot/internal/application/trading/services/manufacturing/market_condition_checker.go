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

// IsFactorySupplyFavorable checks if the factory has ABUNDANT supply for collection.
// We require ABUNDANT (not just HIGH) to START a task, giving a buffer for supply drops.
// The executor will still collect if supply is HIGH when the ship arrives.
func (c *MarketConditionChecker) IsFactorySupplyFavorable(
	ctx context.Context,
	factorySymbol, good string,
	playerID int,
) bool {
	supply := c.getSupplyLevel(ctx, factorySymbol, good, playerID)
	// Require ABUNDANT to start assignment (stricter than execution)
	return supply == manufacturing.SupplyLevelAbundant
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

// CanSourceFromMarket checks if a source market has purchasable supply.
func (c *MarketConditionChecker) CanSourceFromMarket(
	ctx context.Context,
	sourceMarket, good string,
	playerID int,
) bool {
	supply := c.getSupplyLevel(ctx, sourceMarket, good, playerID)
	return supply.AllowsPurchase()
}

// GetReadinessConditions builds the readiness conditions for a task.
func (c *MarketConditionChecker) GetReadinessConditions(
	ctx context.Context,
	task *manufacturing.ManufacturingTask,
	playerID int,
) manufacturing.ReadinessConditions {
	cond := manufacturing.ReadinessConditions{
		DependenciesMet: len(task.DependsOn()) == 0,
	}

	switch task.TaskType() {
	case manufacturing.TaskTypeAcquireDeliver:
		cond.SourceSupply = c.getSupplyLevel(ctx, task.SourceMarket(), task.Good(), playerID)
		cond.FactorySupply = c.getSupplyLevel(ctx, task.FactorySymbol(), task.Good(), playerID)

	case manufacturing.TaskTypeCollectSell:
		cond.FactorySupply = c.getSupplyLevel(ctx, task.FactorySymbol(), task.Good(), playerID)
		cond.SellMarketSupply = c.getSupplyLevel(ctx, task.TargetMarket(), task.Good(), playerID)
		cond.FactoryReady = cond.FactorySupply.IsFavorableForCollection()
	}

	return cond
}

// GetSupplyLevel returns the supply level for a good at a market.
func (c *MarketConditionChecker) GetSupplyLevel(
	ctx context.Context,
	waypointSymbol, good string,
	playerID int,
) manufacturing.SupplyLevel {
	return c.getSupplyLevel(ctx, waypointSymbol, good, playerID)
}

// getSupplyLevel is the internal method to fetch supply level from market data.
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

// EvaluateTaskReadiness performs a complete readiness check for a task.
func (c *MarketConditionChecker) EvaluateTaskReadiness(
	ctx context.Context,
	task *manufacturing.ManufacturingTask,
	playerID int,
) *manufacturing.ReadinessAssessment {
	cond := c.GetReadinessConditions(ctx, task, playerID)
	return c.readinessSpec.EvaluateReadiness(task, cond)
}

// CanExecuteTask checks if a task can be executed given current market conditions.
func (c *MarketConditionChecker) CanExecuteTask(
	ctx context.Context,
	task *manufacturing.ManufacturingTask,
	playerID int,
) bool {
	cond := c.GetReadinessConditions(ctx, task, playerID)
	return c.readinessSpec.CanExecute(task, cond)
}
