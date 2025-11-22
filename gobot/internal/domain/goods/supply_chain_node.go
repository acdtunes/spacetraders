package goods

import (
	"time"
)

// AcquisitionMethod indicates how a good should be acquired
type AcquisitionMethod string

const (
	// AcquisitionBuy means the good should be purchased from a market
	AcquisitionBuy AcquisitionMethod = "BUY"

	// AcquisitionFabricate means the good must be manufactured from inputs
	AcquisitionFabricate AcquisitionMethod = "FABRICATE"
)

// SupplyChainNode represents a node in the dependency tree for producing a good.
// This is a recursive structure where each node can have child nodes representing
// its input requirements.
//
// NOTE: This uses a market-driven production model. No fixed quantities are tracked
// because SpaceTraders uses dynamic, supply/demand-driven production. The system
// acquires whatever quantity is available at markets, not calculated exact amounts.
type SupplyChainNode struct {
	// The good this node represents
	Good string

	// How to acquire this good (BUY from market or FABRICATE from inputs)
	AcquisitionMethod AcquisitionMethod

	// Child nodes representing required inputs (empty for raw materials)
	Children []*SupplyChainNode

	// Market activity level (WEAK, GROWING, STRONG) - used for selection
	MarketActivity string

	// Supply level (SCARCE, LIMITED, MODERATE, HIGH, ABUNDANT) - used for selection
	SupplyLevel string

	// Waypoint where this good can be acquired/manufactured (set during execution)
	WaypointSymbol string

	// Completion status (for tracking during execution)
	Completed bool

	// Quantity acquired during execution (set after completion, 0 if not yet acquired)
	QuantityAcquired int
}

// NewSupplyChainNode creates a new supply chain node
func NewSupplyChainNode(good string, method AcquisitionMethod) *SupplyChainNode {
	return &SupplyChainNode{
		Good:              good,
		AcquisitionMethod: method,
		Children:          make([]*SupplyChainNode, 0),
		MarketActivity:    "",
		SupplyLevel:       "",
		WaypointSymbol:    "",
		Completed:         false,
		QuantityAcquired:  0,
	}
}

// AddChild adds a child node to this node's inputs
func (n *SupplyChainNode) AddChild(child *SupplyChainNode) {
	n.Children = append(n.Children, child)
}

// IsLeaf returns true if this is a raw material with no inputs
func (n *SupplyChainNode) IsLeaf() bool {
	return len(n.Children) == 0
}

// TotalDepth returns the maximum depth of the tree from this node
func (n *SupplyChainNode) TotalDepth() int {
	if n.IsLeaf() {
		return 1
	}

	maxChildDepth := 0
	for _, child := range n.Children {
		childDepth := child.TotalDepth()
		if childDepth > maxChildDepth {
			maxChildDepth = childDepth
		}
	}

	return maxChildDepth + 1
}

// FlattenToList returns a breadth-first traversal of all nodes
func (n *SupplyChainNode) FlattenToList() []*SupplyChainNode {
	result := make([]*SupplyChainNode, 0)
	seen := make(map[string]bool)
	queue := []*SupplyChainNode{n}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		// Skip if we've already processed this good
		if seen[current.Good] {
			continue
		}

		seen[current.Good] = true
		result = append(result, current)

		for _, child := range current.Children {
			queue = append(queue, child)
		}
	}

	return result
}

// RequiredRawMaterials returns the unique list of leaf node goods (raw materials)
func (n *SupplyChainNode) RequiredRawMaterials() []string {
	materials := make(map[string]bool)
	n.collectRawMaterials(materials)

	result := make([]string, 0, len(materials))
	for material := range materials {
		result = append(result, material)
	}

	return result
}

// collectRawMaterials is a helper for RequiredRawMaterials
func (n *SupplyChainNode) collectRawMaterials(materials map[string]bool) {
	if n.IsLeaf() {
		materials[n.Good] = true
		return
	}

	for _, child := range n.Children {
		child.collectRawMaterials(materials)
	}
}

// EstimateProductionTime provides a rough estimate based on tree depth and market activity
// This is used for informational purposes only, not for timeout calculations
func (n *SupplyChainNode) EstimateProductionTime() time.Duration {
	depth := n.TotalDepth()
	baseTime := 2 * time.Minute // Base time per level

	// Adjust based on market activity
	activityMultiplier := 1.0
	switch n.MarketActivity {
	case "WEAK":
		activityMultiplier = 3.0 // Slow production
	case "GROWING":
		activityMultiplier = 1.5
	case "STRONG":
		activityMultiplier = 1.0 // Fast production
	default:
		activityMultiplier = 2.0 // Unknown, assume moderate
	}

	estimated := time.Duration(float64(depth) * float64(baseTime) * activityMultiplier)
	return estimated
}

// CountNodes returns the total number of nodes in the tree
func (n *SupplyChainNode) CountNodes() int {
	return len(n.FlattenToList())
}

// CountByAcquisitionMethod counts how many nodes use each acquisition method
func (n *SupplyChainNode) CountByAcquisitionMethod() (buyCount, fabricateCount int) {
	nodes := n.FlattenToList()
	for _, node := range nodes {
		if node.AcquisitionMethod == AcquisitionBuy {
			buyCount++
		} else if node.AcquisitionMethod == AcquisitionFabricate {
			fabricateCount++
		}
	}
	return buyCount, fabricateCount
}

// MarkCompleted marks this node as completed with quantity acquired
func (n *SupplyChainNode) MarkCompleted(quantity int) {
	n.Completed = true
	n.QuantityAcquired = quantity
}

// AllChildrenCompleted returns true if all child nodes are completed
func (n *SupplyChainNode) AllChildrenCompleted() bool {
	for _, child := range n.Children {
		if !child.Completed {
			return false
		}
	}
	return true
}
