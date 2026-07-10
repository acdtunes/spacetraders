package navigation

// InstallFeasibility reports whether a candidate module or mount can be
// installed on a ship, and if not, which of the ship's fixed hull budgets
// block it. Reactors, frames, and crew capacity have no swap/upgrade
// endpoint in the SpaceTraders API (sp-el60) - a hull's power output, module
// slots, mounting points, and crew capacity are permanent for the life of
// the ship, so these are the only three ways a candidate install can be
// blocked. More than one field can be short at once (e.g. both power and
// slots).
type InstallFeasibility struct {
	// RequirementsKnown is true only when the candidate's power/slot/crew
	// requirements were actually resolved from a real data source (sp-el60
	// acceptance fix). It is false for the Go zero value of
	// InstallFeasibility and for UnknownRequirementsFeasibility(), so an
	// unconstructed or explicitly-unknown verdict can never be mistaken for
	// a computed one. Callers must check this before trusting CanInstall.
	RequirementsKnown bool
	// CanInstall is true only when RequirementsKnown is true and
	// PowerShort, SlotShort, and CrewShort are all 0. A verdict built
	// without known requirements (RequirementsKnown false) always reports
	// CanInstall false — there is no zero-filled-requirements path that can
	// produce a false "fits" verdict.
	CanInstall bool
	// PowerShort is how much additional reactor power output would be
	// needed to fit the candidate, or 0 if the power budget already covers
	// it.
	PowerShort int
	// SlotShort is how many additional module slots (for a module) or
	// mounting points (for a mount) would be needed, or 0 if a slot is
	// already free.
	SlotShort int
	// CrewShort is how much additional crew capacity would be needed to
	// cover the candidate's crew requirement, or 0 if capacity already
	// covers it.
	CrewShort int
}

// UnknownRequirementsFeasibility is the verdict callers must use when a
// candidate's power/slot/crew requirements cannot be resolved from a real
// data source (sp-el60 acceptance fix) — e.g. no ship in the fleet has ever
// carried the candidate's symbol, so there is nothing to look up. It always
// reports RequirementsKnown false and CanInstall false; never construct a
// "fits" verdict from zero-filled or guessed requirements.
func UnknownRequirementsFeasibility() InstallFeasibility {
	return InstallFeasibility{RequirementsKnown: false, CanInstall: false}
}

// CheckModuleInstallFeasibility reports whether candidate can be installed
// on ship given its current reactor power budget, module slot budget, and
// crew capacity. Power is drawn from a single reactor budget shared by every
// installed module AND mount; module slots are a budget separate from
// mounting points (see CheckMountInstallFeasibility). Pure function - does
// not mutate ship or candidate.
func CheckModuleInstallFeasibility(ship *Ship, candidate *ShipModule) InstallFeasibility {
	req := candidate.Requirements()
	return newInstallFeasibility(
		ship.ReactorPowerOutput()-installedPower(ship), req.Power(),
		ship.ModuleSlots()-installedModuleSlots(ship), req.Slots(),
		ship.CrewCapacity()-ship.CrewRequired(), req.Crew(),
	)
}

// CheckMountInstallFeasibility reports whether candidate can be installed on
// ship given its current reactor power budget, mounting point budget, and
// crew capacity. Power is drawn from the same shared reactor budget as
// CheckModuleInstallFeasibility; mounting points are a budget separate from
// module slots. Pure function - does not mutate ship or candidate.
func CheckMountInstallFeasibility(ship *Ship, candidate *ShipMount) InstallFeasibility {
	req := candidate.Requirements()
	return newInstallFeasibility(
		ship.ReactorPowerOutput()-installedPower(ship), req.Power(),
		ship.MountingPoints()-installedMountingPoints(ship), req.Slots(),
		ship.CrewCapacity()-ship.CrewRequired(), req.Crew(),
	)
}

// newInstallFeasibility turns three (remaining budget, additional need)
// pairs into an InstallFeasibility. A need that exceeds what remains is a
// shortfall of that magnitude; a need already covered by what remains blocks
// nothing.
func newInstallFeasibility(powerRemaining, powerNeed, slotsRemaining, slotsNeed, crewRemaining, crewNeed int) InstallFeasibility {
	f := InstallFeasibility{RequirementsKnown: true}
	if gap := powerNeed - powerRemaining; gap > 0 {
		f.PowerShort = gap
	}
	if gap := slotsNeed - slotsRemaining; gap > 0 {
		f.SlotShort = gap
	}
	if gap := crewNeed - crewRemaining; gap > 0 {
		f.CrewShort = gap
	}
	f.CanInstall = f.PowerShort == 0 && f.SlotShort == 0 && f.CrewShort == 0
	return f
}

// PowerUsed reports how much of the ship's reactor power output is currently
// drawn by installed modules and mounts combined. Exported for callers that
// need the ship's power budget directly (e.g. the "ship info" / outfitting
// listing power/slots summary, sp-el60) without checking a specific
// candidate via CheckModuleInstallFeasibility/CheckMountInstallFeasibility.
func PowerUsed(ship *Ship) int {
	return installedPower(ship)
}

// ModuleSlotsUsed reports how many of the ship's module slots are currently
// occupied by installed modules. Exported for the same reason as PowerUsed.
func ModuleSlotsUsed(ship *Ship) int {
	return installedModuleSlots(ship)
}

// MountingPointsUsed reports how many of the ship's mounting points are
// currently occupied by installed mounts. Exported for the same reason as
// PowerUsed.
func MountingPointsUsed(ship *Ship) int {
	return installedMountingPoints(ship)
}

// installedPower sums the power draw of every currently-installed module and
// mount. Power is drawn from a single reactor shared by both (sp-el60).
func installedPower(ship *Ship) int {
	total := 0
	for _, m := range ship.Modules() {
		total += m.Requirements().Power()
	}
	for _, mt := range ship.Mounts() {
		total += mt.Requirements().Power()
	}
	return total
}

// installedModuleSlots sums the module-slot cost of every
// currently-installed module. Module slots are a budget separate from
// mounting points.
func installedModuleSlots(ship *Ship) int {
	total := 0
	for _, m := range ship.Modules() {
		total += m.Requirements().Slots()
	}
	return total
}

// installedMountingPoints sums the mounting-point cost of every
// currently-installed mount. Mounting points are a budget separate from
// module slots.
func installedMountingPoints(ship *Ship) int {
	total := 0
	for _, mt := range ship.Mounts() {
		total += mt.Requirements().Slots()
	}
	return total
}
