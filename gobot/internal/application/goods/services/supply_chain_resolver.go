package services

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

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

// NewSupplyChainResolverWithStrategy creates a resolver with a specific acquisition strategy
func NewSupplyChainResolverWithStrategy(
	supplyChainMap map[string][]string,
	marketRepo market.MarketRepository,
	strategy AcquisitionStrategy,
) *SupplyChainResolver {
	return &SupplyChainResolver{
		supplyChainMap: supplyChainMap,
		marketRepo:     marketRepo,
		strategy:       strategy,
	}
}

// SetStrategy changes the acquisition strategy
func (r *SupplyChainResolver) SetStrategy(strategy AcquisitionStrategy) {
	r.strategy = strategy
}

// Strategy returns the current acquisition strategy
func (r *SupplyChainResolver) Strategy() AcquisitionStrategy {
	return r.strategy
}

// BuildDependencyTree constructs a complete dependency tree for producing a target good.
// It recursively resolves all required inputs, querying markets to determine whether
// each good should be purchased (BUY) or manufactured (FABRICATE).
//
// The algorithm:
// 1. Check if the good is available in any market → mark as BUY
// 2. If not available, check if it can be fabricated from the supply chain map
// 3. Recursively build trees for all required inputs
// 4. Detect circular dependencies
// 5. Populate market activity and supply levels for BUY nodes
//
// Returns the root node of the dependency tree.
func (r *SupplyChainResolver) BuildDependencyTree(
	ctx context.Context,
	targetGood string,
	systemSymbol string,
	playerID int,
) (*goods.SupplyChainNode, error) {
	visited := make(map[string]bool)
	// Force fabrication for the target good (root), even if available in markets
	return r.buildTreeRecursive(ctx, targetGood, systemSymbol, playerID, visited, []string{}, true)
}

// buildTreeRecursive is the internal recursive function for tree building
// isTargetGood forces fabrication for the root good, even if available in markets
func (r *SupplyChainResolver) buildTreeRecursive(
	ctx context.Context,
	goodSymbol string,
	systemSymbol string,
	playerID int,
	visited map[string]bool,
	path []string,
	isTargetGood bool,
) (*goods.SupplyChainNode, error) {
	// Detect cycles
	if visited[goodSymbol] {
		return nil, &goods.ErrCircularDependency{
			Good:  goodSymbol,
			Chain: append(path, goodSymbol),
		}
	}

	// Mark as visiting
	visited[goodSymbol] = true
	defer func() { visited[goodSymbol] = false }()

	// Add to path for cycle detection
	currentPath := append(path, goodSymbol)

	// Decide whether to BUY or FABRICATE based on strategy
	// The target good is always fabricated, so skip this check for root
	if !isTargetGood {
		shouldBuy, marketData := r.shouldBuyGood(ctx, goodSymbol, systemSymbol, playerID)
		if shouldBuy && marketData != nil {
			// Strategy says to buy this good
			node := goods.NewSupplyChainNode(goodSymbol, goods.AcquisitionBuy)
			node.MarketActivity = marketData.Activity
			node.SupplyLevel = marketData.Supply
			node.WaypointSymbol = marketData.WaypointSymbol
			return node, nil
		}
	}

	// Strategy says to fabricate (or good not available for purchase)
	inputs, exists := r.supplyChainMap[goodSymbol]
	if !exists {
		// Good cannot be purchased or fabricated
		return nil, &goods.ErrUnknownGood{Good: goodSymbol}
	}

	// CRITICAL: Verify a factory (EXPORT market) exists in THIS system for the fabricated good
	// A factory is a market that EXPORTS (produces) the good - not IMPORT or EXCHANGE
	factory, err := r.findFactory(ctx, goodSymbol, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("error finding factory for %s: %w", goodSymbol, err)
	}
	if factory == nil {
		return nil, fmt.Errorf("no factory in system %s exports %s - cannot manufacture (only IMPORT/EXCHANGE markets exist)", systemSymbol, goodSymbol)
	}

	// Create fabrication node with factory location
	node := goods.NewSupplyChainNode(goodSymbol, goods.AcquisitionFabricate)
	node.WaypointSymbol = factory.WaypointSymbol

	// Recursively build trees for all required inputs (not target goods)
	for _, inputGood := range inputs {
		childNode, err := r.buildTreeRecursive(ctx, inputGood, systemSymbol, playerID, visited, currentPath, false)
		if err != nil {
			return nil, err
		}
		node.AddChild(childNode)
	}

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
		Price:          factory.SellPrice,
	}, nil
}

// findBestMarketToBuyFrom queries markets to find the best market to buy a good from.
// Uses scored selection preferring EXPORT > EXCHANGE > IMPORT trade types,
// then ABUNDANT > HIGH > MODERATE > LIMITED > SCARCE supply levels.
// Returns market data if found, nil if not available.
func (r *SupplyChainResolver) findBestMarketToBuyFrom(
	ctx context.Context,
	goodSymbol string,
	systemSymbol string,
	playerID int,
) (*MarketResult, error) {
	// Use FindBestMarketForBuying which scores markets by trade_type, supply, and activity
	bestMarket, err := r.marketRepo.FindBestMarketForBuying(ctx, goodSymbol, systemSymbol, playerID)
	if err != nil {
		// If error is "not found", the good is not available in any market
		// This is expected behavior, not an error
		return nil, nil
	}

	if bestMarket == nil {
		return nil, nil // Not available in any market
	}

	return &MarketResult{
		WaypointSymbol: bestMarket.WaypointSymbol,
		Activity:       bestMarket.Activity,
		Supply:         bestMarket.Supply,
		Price:          bestMarket.SellPrice,
	}, nil
}

// shouldBuyGood determines whether to buy a good based on the acquisition strategy.
// Returns (shouldBuy, marketData) - if shouldBuy is false, the good should be fabricated instead.
func (r *SupplyChainResolver) shouldBuyGood(
	ctx context.Context,
	goodSymbol string,
	systemSymbol string,
	playerID int,
) (bool, *MarketResult) {
	// First, check if a market exists to buy from
	marketData, err := r.findBestMarketToBuyFrom(ctx, goodSymbol, systemSymbol, playerID)
	if err != nil || marketData == nil {
		// No market available - must fabricate (if possible)
		return false, nil
	}

	// Check if fabrication is even possible
	_, canFabricate := r.supplyChainMap[goodSymbol]

	switch r.strategy {
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
		case "HIGH", "ABUNDANT":
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

		// Check supply level - fabricate if SCARCE or LIMITED
		switch marketData.Supply {
		case "SCARCE", "LIMITED":
			// Poor supply - prefer fabrication to increase supply
			return false, marketData
		case "MODERATE", "HIGH", "ABUNDANT":
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

// ValidateChain checks if a good can be produced with the current supply chain map
func (r *SupplyChainResolver) ValidateChain(targetGood string) error {
	return goods.ValidateSupplyChain(targetGood)
}

// DetectCycles checks if there are any circular dependencies in the supply chain map
func (r *SupplyChainResolver) DetectCycles(targetGood string) error {
	visited := make(map[string]bool)
	return r.detectCyclesRecursive(targetGood, visited, []string{})
}

// detectCyclesRecursive is the internal recursive function for cycle detection
func (r *SupplyChainResolver) detectCyclesRecursive(
	goodSymbol string,
	visited map[string]bool,
	path []string,
) error {
	if visited[goodSymbol] {
		return &goods.ErrCircularDependency{
			Good:  goodSymbol,
			Chain: append(path, goodSymbol),
		}
	}

	// Check if it's a raw material (end of chain)
	inputs, exists := r.supplyChainMap[goodSymbol]
	if !exists {
		return nil // Raw material, no further dependencies
	}

	// Mark as visiting
	visited[goodSymbol] = true
	defer func() { visited[goodSymbol] = false }()

	// Add to path
	currentPath := append(path, goodSymbol)

	// Check all inputs recursively
	for _, inputGood := range inputs {
		if err := r.detectCyclesRecursive(inputGood, visited, currentPath); err != nil {
			return err
		}
	}

	return nil
}

// MarketResult contains market information for a good
type MarketResult struct {
	WaypointSymbol string
	Activity       string // WEAK, GROWING, STRONG, RESTRICTED
	Supply         string // SCARCE, LIMITED, MODERATE, HIGH, ABUNDANT
	Price          int    // Sell price for exports
}
