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

// ErrNoInSystemExporter indicates a good HAS a recipe (it is a known, manufacturable
// good) but no EXPORT market (factory) for it exists IN THE TARGET SYSTEM yet — only
// IMPORT/EXCHANGE markets do. For gate-construction materials this is a NOT-YET-BUILT
// supply chain (the export factory is created later during GATE), so the factory
// coordinator treats it as an honest-pause + backoff (await the build) rather than an
// unrecoverable crash (sp-lor4). Typed so the coordinator can distinguish it from a
// genuine ErrUnknownGood / ErrCircularDependency; its message is kept byte-identical to
// the pre-sp-lor4 inline error so existing log greps still match.
type ErrNoInSystemExporter struct {
	Good   string
	System string
}

func (e *ErrNoInSystemExporter) Error() string {
	return fmt.Sprintf("no factory in system %s exports %s - cannot manufacture (only IMPORT/EXCHANGE markets exist)", e.System, e.Good)
}

// ErrInsufficientCargo indicates ship cannot hold required goods
type ErrInsufficientCargo struct {
	ShipSymbol     string
	RequiredSpace  int
	AvailableSpace int
}

func (e *ErrInsufficientCargo) Error() string {
	return fmt.Sprintf("ship %s has insufficient cargo space: need %d, have %d",
		e.ShipSymbol, e.RequiredSpace, e.AvailableSpace)
}
