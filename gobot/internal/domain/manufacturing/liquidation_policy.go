package manufacturing

// LiquidationPolicy determines how to handle orphaned cargo.
// Orphaned cargo occurs when a task fails mid-execution and the ship
// is left holding goods that weren't delivered or sold.
type LiquidationPolicy struct{}

// NewLiquidationPolicy creates a new policy.
func NewLiquidationPolicy() *LiquidationPolicy {
	return &LiquidationPolicy{}
}
