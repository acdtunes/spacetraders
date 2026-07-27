package shared

import (
	"math"
)

// FlightMode represents flight mode with time/fuel characteristics
type FlightMode int

const (
	FlightModeCruise FlightMode = iota
	FlightModeDrift
	FlightModeBurn
	FlightModeStealth
)

type flightModeConfig struct {
	Name           string
	TimeMultiplier int
	FuelRate       float64
}

// TimeMultiplier is seconds per unit of distance per unit of engine speed, so a
// LOWER multiplier is a FASTER mode. DRIFT's is calibrated against a measured
// production crossing: 885s over distance 123.81 at engine speed 30.
var flightModeConfigs = map[FlightMode]flightModeConfig{
	FlightModeCruise:  {"CRUISE", 31, 1.0},   // Fast, standard fuel
	FlightModeDrift:   {"DRIFT", 214, 0.003}, // ~7x slower than CRUISE, minimal fuel
	FlightModeBurn:    {"BURN", 15, 2.0},     // Very fast, high fuel
	FlightModeStealth: {"STEALTH", 50, 1.0},  // Very slow, stealthy
}

func (f FlightMode) Name() string {
	if config, ok := flightModeConfigs[f]; ok {
		return config.Name
	}
	return "UNKNOWN"
}

func (f FlightMode) FuelCost(distance float64) int {
	if distance == 0 {
		return 0
	}
	config := flightModeConfigs[f]
	cost := distance * config.FuelRate
	if cost < 1 {
		return 1
	}
	return int(math.Ceil(cost))
}

// IsFasterThan reports whether f covers a given distance in less time than other.
// The enum's declaration order is not speed order, so every ranking of modes goes
// through this.
func (f FlightMode) IsFasterThan(other FlightMode) bool {
	return f.timeMultiplier() < other.timeMultiplier()
}

// ForRouteLeg is the mode a planned leg is actually flown at. DRIFT is a
// last-resort rescue for a hull that is already stranded, never a route mode: at
// ~7x CRUISE's travel time it costs far more than the fuel it saves. A leg planned
// for it flies CRUISE, and the tank is filled to cover that.
func (f FlightMode) ForRouteLeg() FlightMode {
	if f == FlightModeDrift {
		return FlightModeCruise
	}
	return f
}

// timeMultiplier reports f's seconds-per-distance-per-speed factor. An unrecognised
// mode ranks as the slowest so it is never picked as the faster option.
func (f FlightMode) timeMultiplier() int {
	config, ok := flightModeConfigs[f]
	if !ok {
		return math.MaxInt
	}
	return config.TimeMultiplier
}

// TravelTime calculates travel time in seconds
func (f FlightMode) TravelTime(distance float64, engineSpeed int) int {
	if distance == 0 {
		return 0
	}
	config := flightModeConfigs[f]
	if engineSpeed < 1 {
		engineSpeed = 1
	}
	travelSeconds := (distance * float64(config.TimeMultiplier)) / float64(engineSpeed)
	if travelSeconds < 1 {
		return 1
	}
	return int(travelSeconds)
}

// SelectOptimalFlightMode selects best flight mode based on available fuel.
//
// This function delegates to FlightModeSelector which implements the Strategy pattern.
// The Strategy pattern allows new flight modes to be added without modifying this code,
// adhering to the Open/Closed Principle.
//
// Strategy: ALWAYS minimize travel time. Use fastest mode that leaves
// at least safetyMargin fuel remaining.
//
// Special case: If fuel exactly equals burn cost, select BURN (willing to use all fuel).
//
// Priority order: BURN > CRUISE.
//
// Parameters:
//   - currentFuel: Ship's current fuel level
//   - fuelCost: Fuel cost for CRUISE mode (baseline)
//   - safetyMargin: Minimum fuel to keep as reserve
//
// Returns:
//   - Optimal flight mode (BURN or CRUISE)
//   - Whether the tank can actually pay for it; when false the caller must refuel
//     to fly the returned mode rather than settle for something slower.
func SelectOptimalFlightMode(currentFuel, fuelCost, safetyMargin int) (FlightMode, bool) {
	selector := NewFlightModeSelector()
	return selector.SelectOptimalMode(currentFuel, fuelCost, safetyMargin)
}

func (f FlightMode) String() string {
	return f.Name()
}

func IsValidFlightModeName(modeName string) bool {
	for _, config := range flightModeConfigs {
		if config.Name == modeName {
			return true
		}
	}
	return false
}
