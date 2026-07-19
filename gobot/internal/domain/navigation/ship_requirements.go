package navigation

// ShipRequirements captures the power, crew, and slot cost of installing a
// module or mount on a ship. Every module and mount declares its own
// requirements (SpaceTraders API: ShipRequirements on ShipModule/ShipMount/
// ShipReactor); a ship's currently-installed total is checked against the
// hull's fixed budgets (reactor power output, frame module slots / mounting
// points) to determine whether a candidate item can be installed.
//
// Reactors, frames, and engines have NO swap/upgrade endpoint in the
// SpaceTraders API - a hull's power budget (reactor.powerOutput) and slot
// budgets (frame.moduleSlots / frame.mountingPoints) are fixed for the life
// of the ship. Only modules and mounts can be installed/removed to fit
// within those permanent budgets.
type ShipRequirements struct {
	power int
	crew  int
	slots int
}

// NewShipRequirements creates a new ShipRequirements value object. All three
// fields are optional in the API schema (no "required" array on
// ShipRequirements) - zero is a valid, meaningful value, not an error state.
func NewShipRequirements(power, crew, slots int) ShipRequirements {
	return ShipRequirements{power: power, crew: crew, slots: slots}
}

// Power returns the reactor power draw required to operate this module/mount.
func (r ShipRequirements) Power() int {
	return r.power
}

// Crew returns the additional crew required to operate this module/mount.
func (r ShipRequirements) Crew() int {
	return r.crew
}

// Slots returns the number of slots this module/mount consumes: a module
// slot for a ShipModule's requirements, a mounting point for a ShipMount's.
func (r ShipRequirements) Slots() int {
	return r.slots
}
