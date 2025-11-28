package manufacturing

// Liquidation policy constants - domain rules for orphaned cargo
const (
	// MinLiquidateCargoValue is the minimum cargo value (in credits) worth liquidating.
	// Below this threshold, cargo is jettisoned instead of sold.
	// This prevents wasting ship time on low-value liquidation runs.
	MinLiquidateCargoValue = 10000

	// MaxJettisonUnits is the maximum units to jettison in one operation.
	// This prevents accidental loss of large cargo volumes.
	MaxJettisonUnits = 100
)

// LiquidationPolicy determines how to handle orphaned cargo.
// Orphaned cargo occurs when a task fails mid-execution and the ship
// is left holding goods that weren't delivered or sold.
type LiquidationPolicy struct{}

// NewLiquidationPolicy creates a new policy.
func NewLiquidationPolicy() *LiquidationPolicy {
	return &LiquidationPolicy{}
}

// ShouldLiquidate returns true if cargo is valuable enough to sell.
// High-value cargo should be sold to recover investment.
func (p *LiquidationPolicy) ShouldLiquidate(estimatedValue int) bool {
	return estimatedValue >= MinLiquidateCargoValue
}

// ShouldJettison returns true if cargo should be discarded.
// Low-value cargo is not worth the time to sell.
func (p *LiquidationPolicy) ShouldJettison(estimatedValue int) bool {
	return estimatedValue < MinLiquidateCargoValue
}

// GetMinLiquidationValue returns the minimum value threshold for liquidation.
func (p *LiquidationPolicy) GetMinLiquidationValue() int {
	return MinLiquidateCargoValue
}

// GetMaxJettisonUnits returns the maximum units that can be jettisoned at once.
func (p *LiquidationPolicy) GetMaxJettisonUnits() int {
	return MaxJettisonUnits
}

// LiquidationDecision represents the policy decision for orphaned cargo.
type LiquidationDecision struct {
	Action         LiquidationAction
	EstimatedValue int
	Reason         string
}

// LiquidationAction represents the action to take for orphaned cargo.
type LiquidationAction string

const (
	// LiquidationActionSell indicates cargo should be sold at market.
	LiquidationActionSell LiquidationAction = "SELL"

	// LiquidationActionJettison indicates cargo should be discarded.
	LiquidationActionJettison LiquidationAction = "JETTISON"

	// LiquidationActionHold indicates cargo should be held for reassignment.
	LiquidationActionHold LiquidationAction = "HOLD"
)

// DecideAction determines the appropriate action for orphaned cargo.
// Takes into account the cargo value and whether there are matching tasks.
func (p *LiquidationPolicy) DecideAction(estimatedValue int, hasMatchingTask bool) LiquidationDecision {
	if hasMatchingTask {
		return LiquidationDecision{
			Action:         LiquidationActionHold,
			EstimatedValue: estimatedValue,
			Reason:         "matching task found - cargo will be used",
		}
	}

	if estimatedValue >= MinLiquidateCargoValue {
		return LiquidationDecision{
			Action:         LiquidationActionSell,
			EstimatedValue: estimatedValue,
			Reason:         "cargo value exceeds minimum threshold",
		}
	}

	return LiquidationDecision{
		Action:         LiquidationActionJettison,
		EstimatedValue: estimatedValue,
		Reason:         "cargo value below minimum threshold",
	}
}

// CalculateLiquidationPriority returns the priority for a liquidation task.
// Liquidation always has high priority to recover investment quickly.
func (p *LiquidationPolicy) CalculateLiquidationPriority() int {
	return PriorityLiquidate
}
