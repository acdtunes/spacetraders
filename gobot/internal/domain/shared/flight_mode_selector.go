package shared

import "sort"

// FlightModeStrategy defines the interface for flight mode selection strategies.
//
// Each strategy encapsulates the logic for determining if a particular flight mode
// can be used given the current fuel situation. This follows the Strategy pattern
// and Open/Closed Principle - new flight modes can be added without modifying
// existing code.
type FlightModeStrategy interface {
	// CanUse determines if this flight mode can be used with the given fuel situation
	CanUse(currentFuel, fuelCost, safetyMargin int) bool

	// Priority returns the priority of this mode (higher = preferred)
	Priority() int

	// Mode returns the flight mode this strategy represents
	Mode() FlightMode
}

// BurnModeStrategy implements the strategy for BURN flight mode.
//
// BURN mode is the fastest (2x fuel cost) and has highest priority when fuel permits.
type BurnModeStrategy struct{}

// NewBurnModeStrategy creates a new BURN mode strategy
func NewBurnModeStrategy() *BurnModeStrategy {
	return &BurnModeStrategy{}
}

func (s *BurnModeStrategy) CanUse(currentFuel, fuelCost, safetyMargin int) bool {
	burnConfig := flightModeConfigs[FlightModeBurn]
	cruiseConfig := flightModeConfigs[FlightModeCruise]
	burnCost := int(float64(fuelCost) * burnConfig.FuelRate / cruiseConfig.FuelRate)

	// Special case: exact match to burn threshold → use BURN (unless safety margin is very high)
	if currentFuel == burnCost+safetyMargin && safetyMargin < burnCost {
		return true
	}

	// Check BURN with safety margin (need MORE than minimum + margin for safety)
	return currentFuel > burnCost+safetyMargin
}

func (s *BurnModeStrategy) Priority() int {
	return 3 // Highest priority (fastest)
}

func (s *BurnModeStrategy) Mode() FlightMode {
	return FlightModeBurn
}

// CruiseModeStrategy implements the strategy for CRUISE flight mode.
//
// CRUISE mode is the standard speed (1x fuel cost) and has medium priority.
type CruiseModeStrategy struct{}

// NewCruiseModeStrategy creates a new CRUISE mode strategy
func NewCruiseModeStrategy() *CruiseModeStrategy {
	return &CruiseModeStrategy{}
}

func (s *CruiseModeStrategy) CanUse(currentFuel, fuelCost, safetyMargin int) bool {
	// Special case: exact match to cruise threshold → use CRUISE (unless safety margin is very high)
	if currentFuel == fuelCost+safetyMargin && safetyMargin < fuelCost {
		return true
	}

	// Try CRUISE (standard: 1x fuel cost - need MORE than minimum + margin)
	return currentFuel > fuelCost+safetyMargin
}

func (s *CruiseModeStrategy) Priority() int {
	return 2 // Medium priority (standard speed)
}

func (s *CruiseModeStrategy) Mode() FlightMode {
	return FlightModeCruise
}

// DriftModeStrategy implements the strategy for DRIFT flight mode.
//
// DRIFT mode is the slowest but most fuel efficient and is the fallback when
// fuel is too low for other modes.
type DriftModeStrategy struct{}

// NewDriftModeStrategy creates a new DRIFT mode strategy
func NewDriftModeStrategy() *DriftModeStrategy {
	return &DriftModeStrategy{}
}

func (s *DriftModeStrategy) CanUse(currentFuel, fuelCost, safetyMargin int) bool {
	// DRIFT is always available as the most fuel-efficient fallback
	return true
}

func (s *DriftModeStrategy) Priority() int {
	return 1 // Lowest priority (slowest but most efficient)
}

func (s *DriftModeStrategy) Mode() FlightMode {
	return FlightModeDrift
}

// FlightModeSelector selects the optimal flight mode based on available strategies.
//
// This implements the Strategy pattern with priority-based selection. Strategies
// are evaluated in priority order (highest first), and the first strategy that
// can be used is selected.
type FlightModeSelector struct {
	strategies []FlightModeStrategy
}

// NewFlightModeSelector creates a new flight mode selector with the given strategies.
//
// If no strategies are provided, it uses the default strategies (BURN, CRUISE, DRIFT).
func NewFlightModeSelector(strategies ...FlightModeStrategy) *FlightModeSelector {
	if len(strategies) == 0 {
		// Default strategies in priority order
		strategies = []FlightModeStrategy{
			NewBurnModeStrategy(),
			NewCruiseModeStrategy(),
			NewDriftModeStrategy(),
		}
	}

	// Sort strategies by priority (highest first)
	sort.Slice(strategies, func(i, j int) bool {
		return strategies[i].Priority() > strategies[j].Priority()
	})

	return &FlightModeSelector{
		strategies: strategies,
	}
}

// SelectOptimalMode selects the best flight mode for the given fuel situation.
//
// Strategy: ALWAYS minimize travel time. Use fastest mode that leaves
// at least safetyMargin fuel remaining.
//
// Priority order: BURN > CRUISE > DRIFT
//
// Parameters:
//   - currentFuel: Ship's current fuel level
//   - fuelCost: Fuel cost for CRUISE mode (baseline)
//   - safetyMargin: Minimum fuel to keep as reserve
//
// Returns:
//   - Optimal flight mode (BURN, CRUISE, or DRIFT)
func (s *FlightModeSelector) SelectOptimalMode(currentFuel, fuelCost, safetyMargin int) FlightMode {
	// Evaluate strategies in priority order
	for _, strategy := range s.strategies {
		if strategy.CanUse(currentFuel, fuelCost, safetyMargin) {
			return strategy.Mode()
		}
	}

	// Should never reach here if DRIFT strategy is included (it always returns true)
	// But return DRIFT as safe default
	return FlightModeDrift
}
