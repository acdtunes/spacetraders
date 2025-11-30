package shared

import "context"

// contextKey is a type for context keys to avoid collisions
type contextKey int

const (
	operationContextKey    contextKey = iota
	skipMarketRefreshKey              // Skip market refresh after cargo transactions (optimization)
)

// OperationContext provides traceability from high-level operations (containers)
// down to individual financial transactions.
//
// This enables profit/loss tracking at the operation level by linking all
// child transactions (refuel, cargo purchases, cargo sales, etc.) back to
// their parent operation container.
//
// Example usage:
//
//	context := NewOperationContext("contract-work-COOPER-23-abc123", "contract_workflow")
//	refuelCmd := &RefuelShipCommand{
//	    ShipSymbol: "COOPER-23",
//	    Context: context,
//	}
//
// When the refuel handler records a ledger transaction, it will include
// the container ID as related_entity_id, enabling queries like:
//
//	SELECT SUM(amount) FROM transactions
//	WHERE related_entity_type = 'container'
//	  AND related_entity_id = 'contract-work-COOPER-23-abc123'
type OperationContext struct {
	// ContainerID is the unique identifier of the container running this operation
	// Examples: "contract-work-COOPER-23-abc123", "goods_factory-ELECTRONICS-def456"
	ContainerID string

	// OperationType is the type of operation being performed
	// Examples: "contract_workflow", "goods_factory_coordinator", "mining_worker"
	OperationType string
}

// NewOperationContext creates a new operation context with validation
func NewOperationContext(containerID, operationType string) *OperationContext {
	if containerID == "" || operationType == "" {
		return nil
	}
	return &OperationContext{
		ContainerID:   containerID,
		OperationType: operationType,
	}
}

// IsValid returns true if the context has required fields
func (c *OperationContext) IsValid() bool {
	return c != nil && c.ContainerID != "" && c.OperationType != ""
}

// String returns a human-readable representation of the context
func (c *OperationContext) String() string {
	if c == nil {
		return "<no context>"
	}
	return c.OperationType + ":" + c.ContainerID
}

// NormalizedOperationType converts command_type to normalized operation_type for ledger
// Maps from container command types to user-facing operation types:
//   - arbitrage_worker → arbitrage
//   - contract_workflow → contract
//   - balance_ship_position → fleet rebalancing
//   - goods_factory_coordinator → factory
//   - manufacturing_worker → manufacturing
func (c *OperationContext) NormalizedOperationType() string {
	if c == nil || c.OperationType == "" {
		return ""
	}

	switch c.OperationType {
	case "arbitrage_worker":
		return "arbitrage"
	case "contract_workflow":
		return "contract"
	case "balance_ship_position":
		return "fleet rebalancing"
	case "goods_factory_coordinator":
		return "factory"
	case "manufacturing_worker":
		return "manufacturing"
	default:
		// Return as-is for unknown types
		return c.OperationType
	}
}

// WithOperationContext adds an operation context to the context
func WithOperationContext(ctx context.Context, opCtx *OperationContext) context.Context {
	return context.WithValue(ctx, operationContextKey, opCtx)
}

// OperationContextFromContext extracts the operation context from context, or returns nil if not found
func OperationContextFromContext(ctx context.Context) *OperationContext {
	if opCtx, ok := ctx.Value(operationContextKey).(*OperationContext); ok {
		return opCtx
	}
	return nil
}

// WithSkipMarketRefresh returns a context that signals to skip market refresh after cargo transactions.
// This optimization reduces API calls for operations that manage their own market scanning.
func WithSkipMarketRefresh(ctx context.Context) context.Context {
	return context.WithValue(ctx, skipMarketRefreshKey, true)
}

// SkipMarketRefreshFromContext returns true if the context has skip market refresh flag set.
func SkipMarketRefreshFromContext(ctx context.Context) bool {
	if skip, ok := ctx.Value(skipMarketRefreshKey).(bool); ok {
		return skip
	}
	return false
}
