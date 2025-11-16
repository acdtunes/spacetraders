package shared

import (
	"fmt"
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

var flightModeConfigs = map[FlightMode]flightModeConfig{
	FlightModeCruise:  {"CRUISE", 31, 1.0},     // Fast, standard fuel
	FlightModeDrift:   {"DRIFT", 26, 0.003},    // Slow, minimal fuel
	FlightModeBurn:    {"BURN", 15, 2.0},       // Very fast, high fuel
	FlightModeStealth: {"STEALTH", 50, 1.0},    // Very slow, stealthy
}

// Name returns the mode name
func (f FlightMode) Name() string {
	if config, ok := flightModeConfigs[f]; ok {
		return config.Name
	}
	return "UNKNOWN"
}

// FuelCost calculates fuel cost for given distance
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

// TravelTime calculates travel time in seconds
func (f FlightMode) TravelTime(distance float64, engineSpeed int) int {
	if distance == 0 {
		return 0
	}
	config := flightModeConfigs[f]
	if engineSpeed < 1 {
		engineSpeed = 1
	}
	time := (distance * float64(config.TimeMultiplier)) / float64(engineSpeed)
	if time < 1 {
		return 1
	}
	return int(time)
}

// SelectOptimal selects optimal mode prioritizing speed while maintaining safety margin
//
// Strategy: ALWAYS minimize travel time. Use fastest mode that leaves
// at least safetyMargin fuel remaining.
//
// Special case: If fuel exactly equals burn cost, select BURN (willing to use all fuel).
//
// Priority order: BURN > CRUISE > DRIFT
func SelectOptimalFlightMode(currentFuel, fuelCost, safetyMargin int) FlightMode {
	// Try BURN first (fastest: 2x fuel cost)
	burnConfig := flightModeConfigs[FlightModeBurn]
	cruiseConfig := flightModeConfigs[FlightModeCruise]
	burnCost := int(float64(fuelCost) * burnConfig.FuelRate / cruiseConfig.FuelRate)

	// Special case: exact match to burn threshold → use BURN (unless safety margin is very high)
	if currentFuel == burnCost+safetyMargin && safetyMargin < burnCost {
		return FlightModeBurn
	}

	// Check BURN with safety margin (need MORE than minimum + margin for safety)
	if currentFuel > burnCost+safetyMargin {
		return FlightModeBurn
	}

	// Special case: exact match to cruise threshold → use CRUISE (unless safety margin is very high)
	if currentFuel == fuelCost+safetyMargin && safetyMargin < fuelCost {
		return FlightModeCruise
	}

	// Try CRUISE next (standard: 1x fuel cost - need MORE than minimum + margin)
	if currentFuel > fuelCost+safetyMargin {
		return FlightModeCruise
	}

	// Fall back to DRIFT (slowest but most fuel efficient)
	return FlightModeDrift
}

func (f FlightMode) String() string {
	return f.Name()
}

// IsValidFlightModeName checks if a mode name string is valid
func IsValidFlightModeName(modeName string) bool {
	for _, config := range flightModeConfigs {
		if config.Name == modeName {
			return true
		}
	}
	return false
}

// ParseFlightMode parses a flight mode name string into a FlightMode
func ParseFlightMode(modeName string) (FlightMode, error) {
	for mode, config := range flightModeConfigs {
		if config.Name == modeName {
			return mode, nil
		}
	}
	return FlightModeCruise, fmt.Errorf("invalid flight mode: %s", modeName)
}
