package trading

import (
	"errors"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ManufacturingOpportunity represents a high-demand good that can be manufactured and sold.
// Unlike ArbitrageOpportunity, this doesn't have a known buy price since manufacturing
// costs are dynamic and market-driven.
//
// The opportunity is based on:
//   - High purchase price at import market (demand signal)
//   - Good is manufacturable (exists in supply chain map)
//   - Market activity and supply levels (quality indicators)
//   - Dependency tree for production planning
//
// Immutability: All fields are private with read-only getters.
type ManufacturingOpportunity struct {
	good           string
	sellMarket     *shared.Waypoint
	purchasePrice  int // What market pays per unit (import price)
	activity       string // WEAK, GROWING, STRONG, RESTRICTED
	supply         string // SCARCE, LIMITED, MODERATE, HIGH, ABUNDANT
	dependencyTree *goods.SupplyChainNode
	treeDepth      int
	inputCount     int // Number of direct inputs needed
	score          float64 // Composite score for ranking
	discoveredAt   time.Time
}

// NewManufacturingOpportunity creates a new manufacturing opportunity with validation.
//
// Parameters:
//   - good: Trade good symbol (e.g., "ADVANCED_CIRCUITRY", "MACHINERY")
//   - sellMarket: Waypoint where we sell goods (import market with high demand)
//   - purchasePrice: What the market pays per unit (market's purchase_price)
//   - activity: Market activity level (WEAK, GROWING, STRONG, RESTRICTED)
//   - supply: Market supply level (SCARCE, LIMITED, MODERATE, HIGH, ABUNDANT)
//   - dependencyTree: Supply chain tree from SupplyChainResolver
//
// Returns error if:
//   - good symbol is empty
//   - sellMarket is nil
//   - purchasePrice is non-positive
//   - dependencyTree is nil
func NewManufacturingOpportunity(
	good string,
	sellMarket *shared.Waypoint,
	purchasePrice int,
	activity string,
	supply string,
	dependencyTree *goods.SupplyChainNode,
) (*ManufacturingOpportunity, error) {
	if good == "" {
		return nil, errors.New("good symbol required")
	}
	if sellMarket == nil {
		return nil, errors.New("sell market required")
	}
	if purchasePrice <= 0 {
		return nil, errors.New("purchase price must be positive")
	}
	if dependencyTree == nil {
		return nil, errors.New("dependency tree required")
	}

	opp := &ManufacturingOpportunity{
		good:           good,
		sellMarket:     sellMarket,
		purchasePrice:  purchasePrice,
		activity:       activity,
		supply:         supply,
		dependencyTree: dependencyTree,
		treeDepth:      dependencyTree.TotalDepth(),
		inputCount:     len(dependencyTree.Children),
		discoveredAt:   time.Now(),
	}

	// Calculate composite score
	opp.score = opp.calculateScore()

	return opp, nil
}

// Getters - provide read-only access to maintain immutability

func (o *ManufacturingOpportunity) Good() string {
	return o.good
}

func (o *ManufacturingOpportunity) SellMarket() *shared.Waypoint {
	return o.sellMarket
}

func (o *ManufacturingOpportunity) PurchasePrice() int {
	return o.purchasePrice
}

func (o *ManufacturingOpportunity) DependencyTree() *goods.SupplyChainNode {
	return o.dependencyTree
}

func (o *ManufacturingOpportunity) TreeDepth() int {
	return o.treeDepth
}

func (o *ManufacturingOpportunity) InputCount() int {
	return o.inputCount
}

func (o *ManufacturingOpportunity) DiscoveredAt() time.Time {
	return o.discoveredAt
}

func (o *ManufacturingOpportunity) Activity() string {
	return o.activity
}

func (o *ManufacturingOpportunity) Supply() string {
	return o.supply
}

func (o *ManufacturingOpportunity) Score() float64 {
	return o.score
}

// EstimatedRevenue calculates expected revenue based on cargo capacity.
// Actual revenue depends on how many units are manufactured (market-driven).
func (o *ManufacturingOpportunity) EstimatedRevenue(cargoCapacity int) int {
	return o.purchasePrice * cargoCapacity
}

// calculateScore computes a composite score for ranking opportunities.
// Higher scores indicate better opportunities.
//
// Scoring factors (data-driven from arbitrage analysis of 163 trades):
//   - Purchase price (40%): Higher price = more potential revenue
//   - Activity (30%): WEAK > RESTRICTED > STRONG > GROWING (REVERSED!)
//     - WEAK markets have stable prices (+28.94 profit/sec)
//     - GROWING markets have volatile prices (-524.70 profit/sec due to competition)
//   - Supply (20%): For SELL markets, indicates market depth
//     - HIGH/ABUNDANT = market actively trades this good (volume)
//     - SCARCE = market depleted or low volume (risky)
//   - Tree depth penalty (10%): Shallower trees = faster execution
//   - Direct arbitrage bonus (+100): Prioritize quick wins to fund manufacturing
func (o *ManufacturingOpportunity) calculateScore() float64 {
	// Normalize purchase price to 0-100 scale (assuming max ~50000)
	priceScore := float64(o.purchasePrice) / 500.0 // 50000 -> 100
	if priceScore > 100 {
		priceScore = 100
	}

	// Activity score (0-100) - REVERSED from intuition!
	// WEAK markets have stable prices, competition kills margins
	activityScore := 0.0
	switch o.activity {
	case "WEAK":
		activityScore = 100 // Best - stable prices
	case "RESTRICTED":
		activityScore = 50 // Moderate - limited but stable
	case "STRONG":
		activityScore = 25 // Poor - high competition
	case "GROWING":
		activityScore = 0 // Worst - volatile, margin erosion
	default:
		activityScore = 50 // Unknown - assume moderate
	}

	// Supply score (0-100) - for sell markets, indicates volume/depth
	// Markets with higher supply levels trade more volume
	supplyScore := 0.0
	switch o.supply {
	case "ABUNDANT":
		supplyScore = 100
	case "HIGH":
		supplyScore = 80
	case "MODERATE":
		supplyScore = 60
	case "LIMITED":
		supplyScore = 40
	case "SCARCE":
		supplyScore = 20
	default:
		supplyScore = 50 // Unknown
	}

	// Depth penalty (0-100, lower depth = higher score)
	depthScore := 100.0 - float64(o.treeDepth)*20.0
	if depthScore < 0 {
		depthScore = 0
	}

	// Base weighted composite score
	baseScore := (priceScore * 0.40) +
		(activityScore * 0.30) +
		(supplyScore * 0.20) +
		(depthScore * 0.10)

	// Direct arbitrage bonus: +100 for opportunities with AcquisitionBuy at root
	// This prioritizes quick wins (buy from HIGH/ABUNDANT source, sell immediately)
	// to generate income that funds the slower manufacturing pipelines
	// Check both: no children AND root is BUY method (more robust detection)
	if o.dependencyTree != nil && o.dependencyTree.AcquisitionMethod == goods.AcquisitionBuy {
		return baseScore + 100.0
	}

	return baseScore
}

// String returns a human-readable representation
func (o *ManufacturingOpportunity) String() string {
	return fmt.Sprintf("ManufacturingOpportunity{good=%s, price=%d, score=%.1f, activity=%s, supply=%s, depth=%d}",
		o.good, o.purchasePrice, o.score, o.activity, o.supply, o.treeDepth)
}
