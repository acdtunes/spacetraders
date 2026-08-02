package navigation

func (s *Ship) HasJumpDrive() bool {
	for _, module := range s.modules {
		if module.IsJumpDrive() {
			return true
		}
	}
	return false
}

// GetJumpDriveRange returns the range of the ship's jump drive, or 0 if none
func (s *Ship) GetJumpDriveRange() int {
	for _, module := range s.modules {
		if module.IsJumpDrive() {
			return module.Range()
		}
	}
	return 0
}

// HasWarpDrive reports whether the ship has a warp drive module installed
// (MODULE_WARP_DRIVE_*). Only a ship with a warp drive can execute an off-gate
// warp leg between systems; RouteExecutor fails a warp request closed
// when this is false rather than letting the live API reject it.
func (s *Ship) HasWarpDrive() bool {
	for _, module := range s.modules {
		if module.IsWarpDrive() {
			return true
		}
	}
	return false
}

// IsScoutType checks if ship is suitable for scouting (SATELLITE role)
// Excludes EXCAVATOR and other mining/hauling roles
func (s *Ship) IsScoutType() bool {
	return s.role == roleSatellite
}

// ReactorSymbol returns the reactor type symbol (e.g., "REACTOR_SOLAR_I").
func (s *Ship) ReactorSymbol() string {
	return s.reactorSymbol
}

func (s *Ship) ReactorName() string {
	return s.reactorName
}

// ReactorPowerOutput returns the hull's total power budget. Reactors have no
// swap/upgrade endpoint in the SpaceTraders API - this value is permanent for
// the life of the ship.
func (s *Ship) ReactorPowerOutput() int {
	return s.reactorPowerOutput
}

// ReactorRequirements returns the reactor's own power/crew/slot requirements.
func (s *Ship) ReactorRequirements() ShipRequirements {
	return s.reactorRequirements
}

// ModuleSlots returns the frame's total module slot capacity. Frames have no
// swap/upgrade endpoint - this value is permanent for the life of the ship.
func (s *Ship) ModuleSlots() int {
	return s.moduleSlots
}

// MountingPoints returns the frame's total mounting point capacity. Frames
// have no swap/upgrade endpoint - this value is permanent for the life of
// the ship.
func (s *Ship) MountingPoints() int {
	return s.mountingPoints
}

// Mounts returns the ship's installed mounts (mining lasers, gas siphons,
// sensor arrays, weapons, etc.).
func (s *Ship) Mounts() []*ShipMount {
	return s.mounts
}

func (s *Ship) CrewCurrent() int {
	return s.crewCurrent
}

// CrewRequired returns the crew required to operate the ship at its current
// module/mount loadout.
func (s *Ship) CrewRequired() int {
	return s.crewRequired
}

func (s *Ship) CrewCapacity() int {
	return s.crewCapacity
}
