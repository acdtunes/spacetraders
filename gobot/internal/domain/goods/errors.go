package goods

import "fmt"

// Domain errors for goods factory operations

// ErrInvalidFactoryState indicates an invalid state transition attempt
type ErrInvalidFactoryState struct {
	CurrentState string
	Attempted    string
}

func (e *ErrInvalidFactoryState) Error() string {
	return fmt.Sprintf("cannot %s factory in %s state", e.Attempted, e.CurrentState)
}

// ErrCircularDependency indicates a cycle was detected in the supply chain
type ErrCircularDependency struct {
	Good  string
	Chain []string
}

func (e *ErrCircularDependency) Error() string {
	return fmt.Sprintf("circular dependency detected for %s: %v", e.Good, e.Chain)
}

// ErrUnknownGood indicates a good is not found in the supply chain map
type ErrUnknownGood struct {
	Good string
}

func (e *ErrUnknownGood) Error() string {
	return fmt.Sprintf("unknown good: %s (not in supply chain map)", e.Good)
}

// ErrMarketNotFound indicates no market was found for a good
type ErrMarketNotFound struct {
	Good         string
	SystemSymbol string
	SearchType   string // "export" or "import"
}

func (e *ErrMarketNotFound) Error() string {
	return fmt.Sprintf("no market found that %ss %s in system %s", e.SearchType, e.Good, e.SystemSymbol)
}

// ErrProductionTimeout indicates production polling exceeded timeout
type ErrProductionTimeout struct {
	Good            string
	WaypointSymbol  string
	PollAttempts    int
	ElapsedDuration string
}

func (e *ErrProductionTimeout) Error() string {
	return fmt.Sprintf("production timeout for %s at %s after %d polls (%s)",
		e.Good, e.WaypointSymbol, e.PollAttempts, e.ElapsedDuration)
}

// ErrInsufficientCargo indicates ship cannot hold required goods
type ErrInsufficientCargo struct {
	ShipSymbol      string
	RequiredSpace   int
	AvailableSpace  int
}

func (e *ErrInsufficientCargo) Error() string {
	return fmt.Sprintf("ship %s has insufficient cargo space: need %d, have %d",
		e.ShipSymbol, e.RequiredSpace, e.AvailableSpace)
}
