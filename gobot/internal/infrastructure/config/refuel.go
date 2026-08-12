package config

// RefuelConfig holds the fleet's ONE refuel threshold, read at daemon start into the
// route executor's refuel strategy.
type RefuelConfig struct {
	// Threshold is the fuel fraction below which a hull tops off, on arrival and before
	// departure alike. A value under the strategy's floor is held there; 0 → the floor.
	Threshold float64 `mapstructure:"threshold"`
}
