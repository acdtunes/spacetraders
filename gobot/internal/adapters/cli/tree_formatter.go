package cli

import (
	"fmt"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
)

// TreeFormatter provides rich visualization of supply chain dependency trees
type TreeFormatter struct {
	useColors bool
	useEmojis bool
}

// NewTreeFormatter creates a new tree formatter
func NewTreeFormatter(useColors, useEmojis bool) *TreeFormatter {
	return &TreeFormatter{
		useColors: useColors,
		useEmojis: useEmojis,
	}
}

// FormatTree renders a supply chain tree with visual indicators
func (f *TreeFormatter) FormatTree(root *goods.SupplyChainNode) string {
	if root == nil {
		return "(empty tree)"
	}

	var builder strings.Builder
	f.formatNode(&builder, root, "", true, true)
	return builder.String()
}

// formatNode recursively formats a node and its children
func (f *TreeFormatter) formatNode(builder *strings.Builder, node *goods.SupplyChainNode, prefix string, isLast bool, isRoot bool) {
	// Build the tree structure prefix
	var linePrefix string
	if isRoot {
		linePrefix = ""
	} else if isLast {
		linePrefix = prefix + "└── "
	} else {
		linePrefix = prefix + "├── "
	}

	// Status icon
	statusIcon := f.getStatusIcon(node)

	// Method indicator
	methodColor := f.getMethodColor(node.AcquisitionMethod)
	methodText := string(node.AcquisitionMethod)

	// Activity/Supply indicators
	activityText := f.getActivityText(node)

	// Quantity info
	quantityText := ""
	if node.Completed && node.QuantityAcquired > 0 {
		quantityText = fmt.Sprintf(" (%d units)", node.QuantityAcquired)
	}

	// Waypoint info
	waypointText := ""
	if node.WaypointSymbol != "" {
		waypointText = fmt.Sprintf(" @ %s", node.WaypointSymbol)
	}

	// Build the complete line
	line := fmt.Sprintf("%s%s %s [%s%s%s]%s%s%s\n",
		linePrefix,
		statusIcon,
		node.Good,
		methodColor,
		methodText,
		f.colorReset(),
		activityText,
		quantityText,
		waypointText,
	)

	builder.WriteString(line)

	// Format children
	if len(node.Children) > 0 {
		var childPrefix string
		if isRoot {
			childPrefix = ""
		} else if isLast {
			childPrefix = prefix + "    "
		} else {
			childPrefix = prefix + "│   "
		}

		for i, child := range node.Children {
			isLastChild := i == len(node.Children)-1
			f.formatNode(builder, child, childPrefix, isLastChild, false)
		}
	}
}

// getStatusIcon returns a visual indicator for node status
func (f *TreeFormatter) getStatusIcon(node *goods.SupplyChainNode) string {
	if !f.useEmojis {
		if node.Completed {
			return "[✓]"
		}
		return "[ ]"
	}

	if node.Completed {
		return "✅"
	}
	return "⏳"
}

// getMethodColor returns ANSI color code for acquisition method
func (f *TreeFormatter) getMethodColor(method goods.AcquisitionMethod) string {
	if !f.useColors {
		return ""
	}

	switch method {
	case goods.AcquisitionBuy:
		return "\033[32m" // Green
	case goods.AcquisitionFabricate:
		return "\033[33m" // Yellow
	default:
		return ""
	}
}

// getActivityText returns formatted activity and supply level
func (f *TreeFormatter) getActivityText(node *goods.SupplyChainNode) string {
	if node.MarketActivity == "" && node.SupplyLevel == "" {
		return ""
	}

	parts := []string{}
	if node.MarketActivity != "" {
		parts = append(parts, node.MarketActivity)
	}
	if node.SupplyLevel != "" {
		parts = append(parts, node.SupplyLevel)
	}

	if len(parts) == 0 {
		return ""
	}

	return ", " + strings.Join(parts, ", ")
}

// colorReset returns ANSI reset code
func (f *TreeFormatter) colorReset() string {
	if !f.useColors {
		return ""
	}
	return "\033[0m"
}

// FormatTreeSummary creates a compact summary of the tree
func (f *TreeFormatter) FormatTreeSummary(root *goods.SupplyChainNode) string {
	if root == nil {
		return "No dependency tree"
	}

	totalNodes := root.CountNodes()
	buyCount, fabricateCount := root.CountByAcquisitionMethod()
	depth := root.TotalDepth()

	completedCount := 0
	nodes := root.FlattenToList()
	for _, node := range nodes {
		if node.Completed {
			completedCount++
		}
	}

	progress := 0
	if totalNodes > 0 {
		progress = (completedCount * 100) / totalNodes
	}

	return fmt.Sprintf(
		"Tree: %d nodes (%d BUY, %d FABRICATE), depth=%d, progress=%d%%",
		totalNodes, buyCount, fabricateCount, depth, progress,
	)
}

// FormatCompactTree renders a compact single-line tree representation
func (f *TreeFormatter) FormatCompactTree(root *goods.SupplyChainNode) string {
	if root == nil {
		return "(empty)"
	}

	nodes := root.FlattenToList()
	parts := make([]string, 0, len(nodes))

	for _, node := range nodes {
		status := " "
		if node.Completed {
			status = "✓"
		}

		method := "B"
		if node.AcquisitionMethod == goods.AcquisitionFabricate {
			method = "F"
		}

		parts = append(parts, fmt.Sprintf("[%s%s:%s]", status, method, node.Good))
	}

	return strings.Join(parts, " → ")
}

// FormatNodeDetails provides detailed information about a specific node
func (f *TreeFormatter) FormatNodeDetails(node *goods.SupplyChainNode) string {
	if node == nil {
		return "No node"
	}

	var builder strings.Builder

	builder.WriteString(fmt.Sprintf("Good:              %s\n", node.Good))
	builder.WriteString(fmt.Sprintf("Acquisition:       %s\n", node.AcquisitionMethod))
	builder.WriteString(fmt.Sprintf("Completed:         %v\n", node.Completed))

	if node.QuantityAcquired > 0 {
		builder.WriteString(fmt.Sprintf("Quantity Acquired: %d units\n", node.QuantityAcquired))
	}

	if node.WaypointSymbol != "" {
		builder.WriteString(fmt.Sprintf("Waypoint:          %s\n", node.WaypointSymbol))
	}

	if node.MarketActivity != "" {
		builder.WriteString(fmt.Sprintf("Market Activity:   %s\n", node.MarketActivity))
	}

	if node.SupplyLevel != "" {
		builder.WriteString(fmt.Sprintf("Supply Level:      %s\n", node.SupplyLevel))
	}

	if len(node.Children) > 0 {
		builder.WriteString(fmt.Sprintf("Dependencies:      %d inputs required\n", len(node.Children)))
		for i, child := range node.Children {
			status := "pending"
			if child.Completed {
				status = "completed"
			}
			builder.WriteString(fmt.Sprintf("  %d. %s (%s)\n", i+1, child.Good, status))
		}
	}

	return builder.String()
}
