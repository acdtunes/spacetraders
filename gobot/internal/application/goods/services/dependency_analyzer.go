package services

import (
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
)

// ParallelLevel represents a group of nodes that can be executed in parallel
type ParallelLevel struct {
	Nodes []*goods.SupplyChainNode
	Depth int // Depth in the tree (0 = leaves, increases toward root)
}

// DependencyAnalyzer analyzes supply chain dependencies to identify parallelizable nodes
type DependencyAnalyzer struct{}

// NewDependencyAnalyzer creates a new dependency analyzer
func NewDependencyAnalyzer() *DependencyAnalyzer {
	return &DependencyAnalyzer{}
}

// IdentifyParallelLevels groups nodes by dependency level for parallel execution.
// Returns levels ordered from leaves to root (execution order).
//
// Algorithm:
// 1. Start from leaf nodes (no dependencies)
// 2. Group nodes at the same depth
// 3. Ensure no interdependencies within a level
// 4. Return levels in bottom-up execution order
//
// Example tree:
//
//	ADVANCED_CIRCUITRY (depth 2)
//	├── ELECTRONICS (depth 1)
//	│   ├── SILICON_CRYSTALS (depth 0)
//	│   └── COPPER (depth 0)
//	└── MICROPROCESSORS (depth 1)
//	    └── SILICON_CRYSTALS (depth 0 - shared)
//
// Result:
// Level 0: [SILICON_CRYSTALS, COPPER] - can run in parallel
// Level 1: [ELECTRONICS, MICROPROCESSORS] - can run in parallel (both wait for level 0)
// Level 2: [ADVANCED_CIRCUITRY] - waits for level 1
func (a *DependencyAnalyzer) IdentifyParallelLevels(root *goods.SupplyChainNode) []ParallelLevel {
	// Build depth map for all nodes
	depthMap := make(map[string]int) // good -> max depth from leaves
	a.computeDepths(root, depthMap)

	// Get all unique nodes
	allNodes := root.FlattenToList()

	// Group nodes by depth
	levelMap := make(map[int][]*goods.SupplyChainNode)
	for _, node := range allNodes {
		depth := depthMap[node.Good]
		levelMap[depth] = append(levelMap[depth], node)
	}

	// Find max depth
	maxDepth := 0
	for depth := range levelMap {
		if depth > maxDepth {
			maxDepth = depth
		}
	}

	// Build result array (bottom-up: leaves to root)
	result := make([]ParallelLevel, 0, maxDepth+1)
	for depth := 0; depth <= maxDepth; depth++ {
		if nodes, exists := levelMap[depth]; exists {
			result = append(result, ParallelLevel{
				Nodes: nodes,
				Depth: depth,
			})
		}
	}

	return result
}

// computeDepths calculates the maximum depth from leaves for each node
// Depth is defined as:
// - Leaf nodes (no children): depth = 0
// - Internal nodes: depth = max(child depths) + 1
func (a *DependencyAnalyzer) computeDepths(node *goods.SupplyChainNode, depthMap map[string]int) int {
	// Check if already computed
	if depth, exists := depthMap[node.Good]; exists {
		return depth
	}

	// Base case: leaf node
	if node.IsLeaf() {
		depthMap[node.Good] = 0
		return 0
	}

	// Recursive case: compute max child depth
	maxChildDepth := 0
	for _, child := range node.Children {
		childDepth := a.computeDepths(child, depthMap)
		if childDepth > maxChildDepth {
			maxChildDepth = childDepth
		}
	}

	depth := maxChildDepth + 1
	depthMap[node.Good] = depth
	return depth
}

// CanParallelize returns true if the given nodes have no interdependencies
// and can be safely executed in parallel
func (a *DependencyAnalyzer) CanParallelize(nodes []*goods.SupplyChainNode) bool {
	if len(nodes) <= 1 {
		return true
	}

	// Check if any node depends on another in the group
	goods := make(map[string]bool)
	for _, node := range nodes {
		goods[node.Good] = true
	}

	// For each node, check if its dependencies include any other node in the group
	for _, node := range nodes {
		if a.hasInternalDependency(node, goods) {
			return false
		}
	}

	return true
}

// hasInternalDependency checks if a node depends on any good in the provided set
func (a *DependencyAnalyzer) hasInternalDependency(node *goods.SupplyChainNode, goodsSet map[string]bool) bool {
	for _, child := range node.Children {
		if goodsSet[child.Good] {
			return true
		}
		// Recursively check child dependencies
		if a.hasInternalDependency(child, goodsSet) {
			return true
		}
	}
	return false
}

// EstimateParallelSpeedup estimates the speedup factor from parallel execution
// Returns: (sequential_time / parallel_time)
func (a *DependencyAnalyzer) EstimateParallelSpeedup(levels []ParallelLevel) float64 {
	if len(levels) == 0 {
		return 1.0
	}

	sequentialNodes := 0
	for _, level := range levels {
		sequentialNodes += len(level.Nodes)
	}

	// Parallel time = number of levels (assuming unlimited ships)
	parallelLevels := len(levels)

	if parallelLevels == 0 {
		return 1.0
	}

	return float64(sequentialNodes) / float64(parallelLevels)
}
