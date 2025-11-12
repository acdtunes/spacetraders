package shared

import "math"

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
// Priority order: BURN > CRUISE > DRIFT
func SelectOptimalFlightMode(currentFuel, fuelCost, safetyMargin int) FlightMode {
	// Try BURN first (fastest: 2x fuel cost)
	burnConfig := flightModeConfigs[FlightModeBurn]
	cruiseConfig := flightModeConfigs[FlightModeCruise]
	burnCost := int(float64(fuelCost) * burnConfig.FuelRate / cruiseConfig.FuelRate)

	if currentFuel >= burnCost+safetyMargin {
		return FlightModeBurn
	}

	// Try CRUISE next (standard: 1x fuel cost)
	if currentFuel >= fuelCost+safetyMargin {
		return FlightModeCruise
	}

	// Fall back to DRIFT (slowest but most fuel efficient)
	return FlightModeDrift
}

func (f FlightMode) String() string {
	return f.Name()
}
