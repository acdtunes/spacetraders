package navigation_test

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// newFeasibilityTestShip builds a ship with the given reactor power output,
// module slot / mounting point budgets, crew capacity, and installed
// modules/mounts, for exercising CheckModuleInstallFeasibility /
// CheckMountInstallFeasibility. crewRequired lets a case pre-load crew usage
// directly rather than deriving it from installed items - it mirrors how the
// SpaceTraders API reports crew.required as a single pre-aggregated value
// per ship, not something this package derives by summing each module's own
// requirements (see the crew comment in ship_feasibility.go).
func newFeasibilityTestShip(t *testing.T, reactorPower, moduleSlots, mountingPoints, crewCapacity, crewRequired int, modules []*navigation.ShipModule, mounts []*navigation.ShipMount) *navigation.Ship {
	t.Helper()
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	cargo, err := shared.NewCargo(40, 0, nil)
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	location, err := shared.NewWaypoint("X1-AU21-K82", 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	ship, err := navigation.NewShip(
		"TESTSHIP-1",
		shared.MustNewPlayerID(1),
		location,
		fuel,
		100,
		40,
		cargo,
		9,
		"FRAME_FRIGATE",
		"FRIGATE",
		modules,
		navigation.NavStatusInOrbit,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	ship.SetReactor("REACTOR_TEST", "Test Reactor", reactorPower, navigation.NewShipRequirements(0, 0, 0))
	ship.SetSlots(moduleSlots, mountingPoints)
	ship.SetMounts(mounts)
	ship.SetCrew(0, crewRequired, crewCapacity)
	return ship
}

func TestCheckModuleInstallFeasibility_Fits(t *testing.T) {
	ship := newFeasibilityTestShip(t, 10, 5, 5, 5, 0, nil, nil)
	candidate := navigation.NewShipModule("MODULE_CARGO_HOLD_I", 15, 0, navigation.NewShipRequirements(5, 1, 1))

	got := navigation.CheckModuleInstallFeasibility(ship, candidate)

	if !got.CanInstall {
		t.Fatalf("CanInstall = false, want true: %+v", got)
	}
	if got.PowerShort != 0 || got.SlotShort != 0 || got.CrewShort != 0 {
		t.Errorf("expected no shortfalls, got %+v", got)
	}
}

func TestCheckModuleInstallFeasibility_PowerShort(t *testing.T) {
	ship := newFeasibilityTestShip(t, 5, 5, 5, 5, 0, nil, nil)
	candidate := navigation.NewShipModule("MODULE_CARGO_HOLD_I", 15, 0, navigation.NewShipRequirements(10, 0, 1))

	got := navigation.CheckModuleInstallFeasibility(ship, candidate)

	if got.CanInstall {
		t.Fatalf("CanInstall = true, want false: %+v", got)
	}
	if got.PowerShort != 5 {
		t.Errorf("PowerShort = %d, want 5", got.PowerShort)
	}
	if got.SlotShort != 0 || got.CrewShort != 0 {
		t.Errorf("expected only PowerShort blocking, got %+v", got)
	}
}

func TestCheckModuleInstallFeasibility_SlotShort(t *testing.T) {
	installed := navigation.NewShipModule("MODULE_CREW_QUARTERS_I", 5, 0, navigation.NewShipRequirements(1, 0, 1))
	ship := newFeasibilityTestShip(t, 20, 1, 5, 5, 0, []*navigation.ShipModule{installed}, nil)
	candidate := navigation.NewShipModule("MODULE_CARGO_HOLD_I", 15, 0, navigation.NewShipRequirements(1, 0, 1))

	got := navigation.CheckModuleInstallFeasibility(ship, candidate)

	if got.CanInstall {
		t.Fatalf("CanInstall = true, want false: %+v", got)
	}
	if got.SlotShort != 1 {
		t.Errorf("SlotShort = %d, want 1", got.SlotShort)
	}
	if got.PowerShort != 0 || got.CrewShort != 0 {
		t.Errorf("expected only SlotShort blocking, got %+v", got)
	}
}

func TestCheckModuleInstallFeasibility_CrewShort(t *testing.T) {
	ship := newFeasibilityTestShip(t, 20, 5, 5, 2, 2, nil, nil)
	candidate := navigation.NewShipModule("MODULE_CREW_QUARTERS_I", 5, 0, navigation.NewShipRequirements(1, 1, 1))

	got := navigation.CheckModuleInstallFeasibility(ship, candidate)

	if got.CanInstall {
		t.Fatalf("CanInstall = true, want false: %+v", got)
	}
	if got.CrewShort != 1 {
		t.Errorf("CrewShort = %d, want 1", got.CrewShort)
	}
	if got.PowerShort != 0 || got.SlotShort != 0 {
		t.Errorf("expected only CrewShort blocking, got %+v", got)
	}
}

// TestCheckModuleInstallFeasibility_PowerSharedWithMounts proves the power
// budget is a single pool drawn down by BOTH installed modules and installed
// mounts, not a per-collection budget - a candidate module install can be
// blocked purely by an installed mount's power draw, since both draw from
// the same reactor (sp-el60).
func TestCheckModuleInstallFeasibility_PowerSharedWithMounts(t *testing.T) {
	installedMount := navigation.NewShipMount("MOUNT_MINING_LASER_I", "Mining Laser I", 30, nil, navigation.NewShipRequirements(5, 0, 1))
	ship := newFeasibilityTestShip(t, 5, 5, 5, 5, 0, nil, []*navigation.ShipMount{installedMount})
	candidate := navigation.NewShipModule("MODULE_CARGO_HOLD_I", 15, 0, navigation.NewShipRequirements(1, 0, 1))

	got := navigation.CheckModuleInstallFeasibility(ship, candidate)

	if got.CanInstall {
		t.Fatalf("CanInstall = true, want false: %+v", got)
	}
	if got.PowerShort != 1 {
		t.Errorf("PowerShort = %d, want 1 (5 reactor - 5 mount = 0 remaining, candidate needs 1)", got.PowerShort)
	}
}

// TestCheckModuleInstallFeasibility_FrigateEmpiricalCase encodes the
// empirical ledger from the live frigate power gate that motivated sp-el60:
// reactor powerOutput 31 vs 33 required (gap 2) blocked installing
// MODULE_CARGO_HOLD_III; removing two 1-power processors
// (MODULE_MINERAL_PROCESSOR_I, MODULE_GAS_PROCESSOR_I) closed the gap. This
// scenario must be computable offline instead of by live trial-and-error
// installs.
func TestCheckModuleInstallFeasibility_FrigateEmpiricalCase(t *testing.T) {
	mineralProcessor := navigation.NewShipModule("MODULE_MINERAL_PROCESSOR_I", 0, 0, navigation.NewShipRequirements(1, 0, 1))
	gasProcessor := navigation.NewShipModule("MODULE_GAS_PROCESSOR_I", 0, 0, navigation.NewShipRequirements(1, 0, 1))
	cargoHoldIII := navigation.NewShipModule("MODULE_CARGO_HOLD_III", 100, 0, navigation.NewShipRequirements(31, 0, 1))

	// Before: reactor=31, installed processors=2 power, candidate=31 power -> needs 33, gap 2.
	before := newFeasibilityTestShip(t, 31, 5, 5, 10, 0,
		[]*navigation.ShipModule{mineralProcessor, gasProcessor}, nil)

	got := navigation.CheckModuleInstallFeasibility(before, cargoHoldIII)

	if got.CanInstall {
		t.Fatalf("CanInstall = true, want false before removing processors: %+v", got)
	}
	if got.PowerShort != 2 {
		t.Fatalf("PowerShort = %d, want 2 (31 reactor vs 33 required)", got.PowerShort)
	}

	// After: remove both 1-power processors -> 31 reactor vs 31 required, fits exactly.
	after := newFeasibilityTestShip(t, 31, 5, 5, 10, 0, nil, nil)

	got = navigation.CheckModuleInstallFeasibility(after, cargoHoldIII)

	if !got.CanInstall {
		t.Fatalf("CanInstall = false, want true after removing processors: %+v", got)
	}
	if got.PowerShort != 0 {
		t.Errorf("PowerShort = %d, want 0 after removing processors", got.PowerShort)
	}
}

func TestCheckMountInstallFeasibility_Fits(t *testing.T) {
	ship := newFeasibilityTestShip(t, 20, 5, 5, 5, 0, nil, nil)
	candidate := navigation.NewShipMount("MOUNT_MINING_LASER_I", "Mining Laser I", 30, nil, navigation.NewShipRequirements(5, 0, 1))

	got := navigation.CheckMountInstallFeasibility(ship, candidate)

	if !got.CanInstall {
		t.Fatalf("CanInstall = false, want true: %+v", got)
	}
}

// TestCheckMountInstallFeasibility_SlotShort proves mounting points are a
// budget separate from module slots: a full module-slot budget must not
// block a mount install, and mounting points are consumed only by installed
// mounts, not installed modules.
// TestPowerUsedModuleSlotsUsedMountingPointsUsed proves the exported budget
// summary helpers (used by the "ship info" / outfitting-listing power/slots
// section, sp-el60) agree with the totals CheckModuleInstallFeasibility
// derives internally.
func TestPowerUsedModuleSlotsUsedMountingPointsUsed(t *testing.T) {
	module := navigation.NewShipModule("MODULE_MINERAL_PROCESSOR_I", 0, 0, navigation.NewShipRequirements(1, 0, 1))
	mount := navigation.NewShipMount("MOUNT_MINING_LASER_I", "Mining Laser I", 30, nil, navigation.NewShipRequirements(5, 0, 1))
	ship := newFeasibilityTestShip(t, 31, 5, 3, 10, 0, []*navigation.ShipModule{module}, []*navigation.ShipMount{mount})

	if got := navigation.PowerUsed(ship); got != 6 {
		t.Errorf("PowerUsed = %d, want 6 (1 module + 5 mount)", got)
	}
	if got := navigation.ModuleSlotsUsed(ship); got != 1 {
		t.Errorf("ModuleSlotsUsed = %d, want 1", got)
	}
	if got := navigation.MountingPointsUsed(ship); got != 1 {
		t.Errorf("MountingPointsUsed = %d, want 1", got)
	}
}

func TestCheckMountInstallFeasibility_SlotShort(t *testing.T) {
	installedModule := navigation.NewShipModule("MODULE_CARGO_HOLD_I", 15, 0, navigation.NewShipRequirements(1, 0, 1))
	installedMount := navigation.NewShipMount("MOUNT_SENSOR_ARRAY_I", "Sensor Array I", 0, nil, navigation.NewShipRequirements(1, 0, 1))
	// moduleSlots=5 (plenty free), mountingPoints=1 (already full via installedMount).
	ship := newFeasibilityTestShip(t, 20, 5, 1, 5, 0,
		[]*navigation.ShipModule{installedModule}, []*navigation.ShipMount{installedMount})
	candidate := navigation.NewShipMount("MOUNT_GAS_SIPHON_I", "Gas Siphon I", 30, nil, navigation.NewShipRequirements(1, 0, 1))

	got := navigation.CheckMountInstallFeasibility(ship, candidate)

	if got.CanInstall {
		t.Fatalf("CanInstall = true, want false: %+v", got)
	}
	if got.SlotShort != 1 {
		t.Errorf("SlotShort = %d, want 1", got.SlotShort)
	}
}
