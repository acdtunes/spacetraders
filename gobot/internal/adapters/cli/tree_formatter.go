package cli

import (
	"fmt"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
)

// ANSI escape codes used when the TreeFormatter renders with colors enabled.
const (
	ansiColorGreen  = "\033[32m"
	ansiColorYellow = "\033[33m"
	ansiColorReset  = "\033[0m"
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
		return ansiColorGreen
	case goods.AcquisitionFabricate:
		return ansiColorYellow
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
	return ansiColorReset
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
