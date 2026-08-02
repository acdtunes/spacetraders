package services

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// MarketResult contains market information for a good
type MarketResult struct {
	WaypointSymbol string
	Activity       string // WEAK, GROWING, STRONG, RESTRICTED
	Supply         string // SCARCE, LIMITED, MODERATE, HIGH, ABUNDANT
	Price          int    // Sell price for exports
}

// AcquisitionStrategy determines how the resolver chooses between buying and fabricating goods
type AcquisitionStrategy string

const (
	// StrategyPreferBuy always buys if a market exists (fastest, default)
	StrategyPreferBuy AcquisitionStrategy = "prefer-buy"

	// StrategyPreferFabricate always fabricates if a recipe exists (deepest supply chain)
	StrategyPreferFabricate AcquisitionStrategy = "prefer-fabricate"

	// StrategySmart fabricates when supply is SCARCE/LIMITED, buys when MODERATE/HIGH/ABUNDANT
	StrategySmart AcquisitionStrategy = "smart"
)

// SupplyChainResolver builds dependency trees for goods production.
// It determines whether each good should be bought or fabricated based on
// market availability and the configured acquisition strategy.
type SupplyChainResolver struct {
	supplyChainMap map[string][]string
	marketRepo     market.MarketRepository
	strategy       AcquisitionStrategy
}

// NewSupplyChainResolver creates a new supply chain resolver with default strategy (prefer-buy)
func NewSupplyChainResolver(
	supplyChainMap map[string][]string,
	marketRepo market.MarketRepository,
) *SupplyChainResolver {
	return &SupplyChainResolver{
		supplyChainMap: supplyChainMap,
		marketRepo:     marketRepo,
		strategy:       StrategyPreferBuy,
	}
}

// BuildDependencyTree constructs a complete dependency tree for producing a target good.
// It recursively resolves all required inputs, querying markets to determine whether
// each good should be purchased (BUY) or manufactured (FABRICATE).
//
// The algorithm:
//  1. Find the factory that produces the target good
//  2. If factory has HIGH/ABUNDANT supply → create direct arbitrage node (AcquisitionBuy)
//  3. If no factory exists → check if good can be bought from market (direct arbitrage)
//  4. Otherwise, build full dependency tree:
//     a. Check if child goods are available in markets → mark as BUY
//     b. If not available, check if can be fabricated from supply chain map
//     c. Recursively build trees for all required inputs
//     d. Detect circular dependencies
//     e. Populate market activity and supply levels for BUY nodes
//
// Returns the root node of the dependency tree.
func (r *SupplyChainResolver) BuildDependencyTree(
	ctx context.Context,
	targetGood string,
	systemSymbol string,
	playerID int,
) (*goods.SupplyChainNode, error) {
	// Step 1: Find the factory for the target good
	factory, err := r.findFactory(ctx, targetGood, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("error finding factory for %s: %w", targetGood, err)
	}

	// Step 2: No factory exists - check if good can be bought from market OR manufactured
	if factory == nil {
		marketData := r.findBestMarketToBuyFrom(ctx, targetGood, systemSymbol, playerID)
		if marketData != nil {
			// Found a market - create direct arbitrage node (just buy from market)
			node := goods.NewSupplyChainNode(targetGood, goods.AcquisitionBuy)
			node.WaypointSymbol = marketData.WaypointSymbol
			node.SupplyLevel = marketData.Supply
			node.MarketActivity = marketData.Activity
			return node, nil
		}

		// No market found - check if good can be manufactured (exists in supply chain map)
		// This handles cases where the mock repository doesn't track trade_type
		// but the good is still manufacturable
		if _, exists := r.supplyChainMap[targetGood]; exists {
			// Good is manufacturable - build the tree recursively
			// The tree will use FABRICATE for this good
			return r.newTreeWalk(systemSymbol, playerID).build(ctx, targetGood, []string{}, true)
		}

		// No factory, no market, and not manufacturable - unknown good error
		return nil, &goods.ErrUnknownGood{Good: targetGood}
	}

	// Step 3: Check if good has a recipe (requires inputs) vs source good (no inputs)
	inputs, hasRecipe := r.supplyChainMap[targetGood]

	// Step 3a: Source good with no recipe - use AcquisitionBuy (true direct arbitrage)
	// Examples: RAW_MATERIALS sourced from extractors, not manufactured
	if !hasRecipe || len(inputs) == 0 {
		node := goods.NewSupplyChainNode(targetGood, goods.AcquisitionBuy)
		node.WaypointSymbol = factory.WaypointSymbol
		node.SupplyLevel = factory.Supply
		node.MarketActivity = factory.Activity
		// No children = true source good (buy from source, sell to destination)
		return node, nil
	}

	// Step 3b: Good has a recipe - always use AcquisitionFabricate to ensure proper pipeline creation
	// Even if factory supply is HIGH/ABUNDANT, we need the full dependency tree for:
	// - Proper factory state tracking
	// - Future ACQUIRE_DELIVER task creation when supply drops
	// The pipeline_planner will check supply level and may skip initial ACQUIRE_DELIVER tasks

	// Step 4: Build full manufacturing tree (includes factory supply level for optimization)
	return r.newTreeWalk(systemSymbol, playerID).build(ctx, targetGood, []string{}, true)
}

// treeWalk is one dependency-tree traversal: every market lookup in it resolves against the same
// system and player, and visited is the cycle-detection set shared down the whole recursion.
type treeWalk struct {
	resolver     *SupplyChainResolver
	systemSymbol string
	playerID     int
	visited      map[string]bool
}

func (r *SupplyChainResolver) newTreeWalk(systemSymbol string, playerID int) *treeWalk {
	return &treeWalk{resolver: r, systemSymbol: systemSymbol, playerID: playerID, visited: make(map[string]bool)}
}

// build is the recursive tree builder.
// isTargetGood forces fabrication for the root good, even if available in markets
func (w *treeWalk) build(
	ctx context.Context,
	goodSymbol string,
	path []string,
	isTargetGood bool,
) (*goods.SupplyChainNode, error) {
	// Detect cycles
	if w.visited[goodSymbol] {
		return nil, &goods.ErrCircularDependency{
			Good:  goodSymbol,
			Chain: append(path, goodSymbol),
		}
	}

	w.visited[goodSymbol] = true
	defer func() { w.visited[goodSymbol] = false }()

	// Add to path for cycle detection
	currentPath := append(path, goodSymbol)

	// The target good is always fabricated, so the strategy only decides for non-root nodes.
	if !isTargetGood {
		shouldBuy, marketData := w.resolver.shouldBuyGood(ctx, goodSymbol, w.systemSymbol, w.playerID)
		if shouldBuy && marketData != nil {
			return buyLeafNode(goodSymbol, marketData), nil
		}
	}

	// Fabricate depth cap: beyond it, resolve this input to a market-BUY instead of recursing into
	// its sub-chain. len(path) is the current node's depth (root == 0, its direct inputs == 1).
	// A SAFETY BACKSTOP, not the terminator — StrategySmart above already stops the recursion at
	// every abundant good, so this only bounds how deep a genuinely-scarce sub-chain may fabricate.
	// A market-less BUY leaf is safe: buyGood re-resolves the source at buy time and PARKS if none
	// exists. The root is exempt; disabled restores unbounded recursion.
	depthCfg := fabricateDepthConfigFromContext(ctx)
	if !isTargetGood && !depthCfg.disabled && len(path) >= depthCfg.maxDepth {
		return buyLeafNode(goodSymbol, w.resolver.findBestMarketToBuyFrom(ctx, goodSymbol, w.systemSymbol, w.playerID)), nil
	}

	inputs, exists := w.resolver.supplyChainMap[goodSymbol]
	if !exists {
		// Good cannot be purchased or fabricated
		return nil, &goods.ErrUnknownGood{Good: goodSymbol}
	}

	node, err := w.resolver.fabricationNode(ctx, goodSymbol, w.systemSymbol, w.playerID)
	if err != nil {
		return nil, err
	}

	// Recursively build trees for all required inputs (not target goods)
	for _, inputGood := range inputs {
		childNode, err := w.build(ctx, inputGood, currentPath, false)
		if err != nil {
			return nil, err
		}
		node.AddChild(childNode)
	}

	return node, nil
}

// buyLeafNode builds a terminal BUY node, populating the market hints downstream tree consumers
// use when a source is known. The node stands without them: nothing here commits to a purchase.
func buyLeafNode(goodSymbol string, marketData *MarketResult) *goods.SupplyChainNode {
	node := goods.NewSupplyChainNode(goodSymbol, goods.AcquisitionBuy)
	if marketData != nil {
		node.MarketActivity = marketData.Activity
		node.SupplyLevel = marketData.Supply
		node.WaypointSymbol = marketData.WaypointSymbol
	}
	return node
}

// fabricationNode builds the FABRICATE node for a recipe good, which requires a factory — a market
// that EXPORTS the good, not one that imports or exchanges it — in THIS system.
//
// A recipe good with no in-system exporter is a NOT-YET-BUILT supply chain (its exporter is built
// later at GATE), not a hard fault, so it returns a typed error the factory coordinator
// honest-pauses on rather than crashing.
func (r *SupplyChainResolver) fabricationNode(ctx context.Context, goodSymbol, systemSymbol string, playerID int) (*goods.SupplyChainNode, error) {
	factory, err := r.findFactory(ctx, goodSymbol, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("error finding factory for %s: %w", goodSymbol, err)
	}
	if factory == nil {
		return nil, &goods.ErrNoInSystemExporter{Good: goodSymbol, System: systemSymbol}
	}

	node := goods.NewSupplyChainNode(goodSymbol, goods.AcquisitionFabricate)
	node.WaypointSymbol = factory.WaypointSymbol
	node.SupplyLevel = factory.Supply      // Capture supply level for pipeline optimization
	node.MarketActivity = factory.Activity // Capture activity for market state tracking
	return node, nil
}

// findFactory finds a market that EXPORTS (produces) a specific good.
// Only returns markets with trade_type=EXPORT, which are actual factories.
// Returns nil if no factory produces this good in the system.
func (r *SupplyChainResolver) findFactory(
	ctx context.Context,
	goodSymbol string,
	systemSymbol string,
	playerID int,
) (*MarketResult, error) {
	factory, err := r.marketRepo.FindFactoryForGood(ctx, goodSymbol, systemSymbol, playerID)
	if err != nil {
		return nil, err
	}
	if factory == nil {
		return nil, nil // No factory exports this good
	}

	return &MarketResult{
		WaypointSymbol: factory.WaypointSymbol,
		Activity:       factory.Activity,
		Supply:         factory.Supply,
		Price:          factory.Ask,
	}, nil
}

// findBestMarketToBuyFrom queries markets to find the best market to buy a good from.
// Uses scored selection preferring EXPORT > EXCHANGE > IMPORT trade types,
// then ABUNDANT > HIGH > MODERATE > LIMITED > SCARCE supply levels.
// Returns market data if found, nil if not available. A repo error is folded into that
// same nil: "not found" is the expected shape of an unavailable good, so this step has no
// failure of its own to report.
func (r *SupplyChainResolver) findBestMarketToBuyFrom(
	ctx context.Context,
	goodSymbol string,
	systemSymbol string,
	playerID int,
) *MarketResult {
	bestMarket, err := r.marketRepo.FindBestMarketForBuying(ctx, goodSymbol, systemSymbol, playerID)
	if err != nil {
		// If error is "not found", the good is not available in any market
		// This is expected behavior, not an error
		return nil
	}

	if bestMarket == nil {
		return nil // Not available in any market
	}

	return &MarketResult{
		WaypointSymbol: bestMarket.WaypointSymbol,
		Activity:       bestMarket.Activity,
		Supply:         bestMarket.Supply,
		Price:          bestMarket.Ask,
	}
}

// effectiveStrategy resolves the acquisition strategy for one good: a per-good override wins over
// the ctx-scoped run strategy, which in turn wins over the resolver's own default. Estimators and
// directly-built commands stamp neither, so they keep r.strategy.
func (r *SupplyChainResolver) effectiveStrategy(ctx context.Context, goodSymbol string) AcquisitionStrategy {
	globalStrategy := string(r.strategy)
	if runStrategy := productionStrategyFromContext(ctx); runStrategy != "" {
		globalStrategy = runStrategy
	}
	return AcquisitionStrategy(goodGatingOverridesFromContext(ctx).StrategyFor(goodSymbol, globalStrategy))
}

// shouldBuyGood determines whether to buy a good based on the acquisition strategy.
// Returns (shouldBuy, marketData) - if shouldBuy is false, the good should be fabricated instead.
func (r *SupplyChainResolver) shouldBuyGood(
	ctx context.Context,
	goodSymbol string,
	systemSymbol string,
	playerID int,
) (bool, *MarketResult) {
	marketData := r.findBestMarketToBuyFrom(ctx, goodSymbol, systemSymbol, playerID)
	if marketData == nil {
		// No market available - must fabricate (if possible)
		return false, nil
	}

	// Raw materials like SILICON_CRYSTALS exist in the map with empty inputs {}
	// They can't be fabricated - they must be bought
	inputs, exists := r.supplyChainMap[goodSymbol]
	hasRecipe := exists && len(inputs) > 0

	// CRITICAL: Also check if a factory (EXPORT market) exists in THIS system
	// Even if a recipe exists, we can't fabricate without a factory
	// Example: SILICON_CRYSTALS has a recipe (needs EXPLOSIVES) but no factory in X1-YZ19
	canFabricate := false
	if hasRecipe {
		factory, err := r.findFactory(ctx, goodSymbol, systemSymbol, playerID)
		canFabricate = err == nil && factory != nil
	}

	switch r.effectiveStrategy(ctx, goodSymbol) {
	case StrategyPreferBuy:
		// Always buy if market exists (original behavior)
		return true, marketData

	case StrategyPreferFabricate:
		// Fabricate if recipe exists AND supply is not good (HIGH/ABUNDANT)
		// This is more aggressive than "smart" - fabricates for MODERATE supply too
		if !canFabricate {
			// No recipe - must buy
			return true, marketData
		}

		// Only buy if supply is excellent
		switch marketData.Supply {
		case supplyHigh, supplyAbundant:
			// Good supply - buy directly
			return true, marketData
		default:
			// SCARCE, LIMITED, MODERATE, or unknown - prefer fabrication
			return false, marketData
		}

	case StrategySmart:
		// Fabricate if supply is poor (SCARCE/LIMITED), buy if supply is good
		if !canFabricate {
			// No recipe - must buy
			return true, marketData
		}

		switch marketData.Supply {
		case supplyScarce, supplyLimited:
			// Poor supply - prefer fabrication to increase supply
			return false, marketData
		case supplyModerate, supplyHigh, supplyAbundant:
			// Good supply - buy directly
			return true, marketData
		default:
			// Unknown supply level - default to buying
			return true, marketData
		}

	default:
		// Unknown strategy - default to buying
		return true, marketData
	}
}
