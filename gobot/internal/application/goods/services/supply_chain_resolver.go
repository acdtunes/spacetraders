package services

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// SupplyChainResolver builds dependency trees for goods production.
// It determines whether each good should be bought or fabricated based on
// market availability, following the "always buy if available" strategy.
type SupplyChainResolver struct {
	supplyChainMap map[string][]string
	marketRepo     market.MarketRepository
}

// NewSupplyChainResolver creates a new supply chain resolver
func NewSupplyChainResolver(
	supplyChainMap map[string][]string,
	marketRepo market.MarketRepository,
) *SupplyChainResolver {
	return &SupplyChainResolver{
		supplyChainMap: supplyChainMap,
		marketRepo:     marketRepo,
	}
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
	return r.buildTreeRecursive(ctx, targetGood, systemSymbol, playerID, visited, []string{})
}

// buildTreeRecursive is the internal recursive function for tree building
func (r *SupplyChainResolver) buildTreeRecursive(
	ctx context.Context,
	goodSymbol string,
	systemSymbol string,
	playerID int,
	visited map[string]bool,
	path []string,
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

	// Check if available in any market (prefer buying)
	marketData, err := r.findExportMarket(ctx, goodSymbol, systemSymbol, playerID)
	if err == nil && marketData != nil {
		// Good is available for purchase
		node := goods.NewSupplyChainNode(goodSymbol, goods.AcquisitionBuy)
		node.MarketActivity = marketData.Activity
		node.SupplyLevel = marketData.Supply
		node.WaypointSymbol = marketData.WaypointSymbol
		return node, nil
	}

	// Not available for purchase, check if it can be fabricated
	inputs, exists := r.supplyChainMap[goodSymbol]
	if !exists {
		// Good cannot be purchased or fabricated
		return nil, &goods.ErrUnknownGood{Good: goodSymbol}
	}

	// Create fabrication node
	node := goods.NewSupplyChainNode(goodSymbol, goods.AcquisitionFabricate)

	// Recursively build trees for all required inputs
	for _, inputGood := range inputs {
		childNode, err := r.buildTreeRecursive(ctx, inputGood, systemSymbol, playerID, visited, currentPath)
		if err != nil {
			return nil, err
		}
		node.AddChild(childNode)
	}

	return node, nil
}

// findExportMarket queries markets to find where a good is sold
// Returns market data if found, nil if not available
func (r *SupplyChainResolver) findExportMarket(
	ctx context.Context,
	goodSymbol string,
	systemSymbol string,
	playerID int,
) (*MarketResult, error) {
	// Use FindCheapestMarketSelling to check if the good is available
	cheapestMarket, err := r.marketRepo.FindCheapestMarketSelling(ctx, goodSymbol, systemSymbol, playerID)
	if err != nil {
		// If error is "not found", the good is not available in any market
		// This is expected behavior, not an error
		return nil, nil
	}

	if cheapestMarket == nil {
		return nil, nil // Not available in any market
	}

	// Get full market data to extract activity and supply
	marketData, err := r.marketRepo.GetMarketData(ctx, cheapestMarket.WaypointSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get market data: %w", err)
	}

	// Extract trade good details
	tradeGood := marketData.FindGood(goodSymbol)
	if tradeGood == nil {
		return nil, nil
	}

	result := &MarketResult{
		WaypointSymbol: cheapestMarket.WaypointSymbol,
		Activity:       "",
		Supply:         "",
		Price:          cheapestMarket.SellPrice,
	}

	// Extract activity and supply if available
	if tradeGood.Activity() != nil {
		result.Activity = *tradeGood.Activity()
	}
	if tradeGood.Supply() != nil {
		result.Supply = *tradeGood.Supply()
	}

	return result, nil
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
