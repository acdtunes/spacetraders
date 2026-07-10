package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// powerSlotsFakeWaypointProvider always errors, so modelToDomain's waypoint
// lookup falls back to the model's denormalized location fields (see
// modelToDomain's err != nil branch). This test only cares about the
// reactor/power/slot/crew round trip, not location fidelity - mirrors
// syncPreserveOwnerFakeWaypointProvider's rationale in the sibling sync test.
type powerSlotsFakeWaypointProvider struct{}

func (powerSlotsFakeWaypointProvider) GetWaypoint(_ context.Context, _, _ string, _ int) (*shared.Waypoint, error) {
	return nil, errors.New("stub: waypoint lookup not needed by this test")
}

// TestSaveThenFindBySymbol_RoundTripsReactorPowerSlotsCrew proves the
// reactor/power/slot/crew/mounts columns added in sp-el60 survive a full
// Save -> FindBySymbol round trip through the real conversion functions
// (shipToModel / modelToDomain) - not just raw column presence on the GORM
// model. Uses the same real-sqlite test-DB harness as the dedication tests,
// plus a fake IWaypointProvider since modelToDomain (unlike ClaimShip/
// AssignFleet) needs one.
func TestSaveThenFindBySymbol_RoundTripsReactorPowerSlotsCrew(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	playerModel := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&playerModel).Error)
	playerID := shared.MustNewPlayerID(playerModel.ID)

	repo := NewShipRepository(nil, nil, nil, powerSlotsFakeWaypointProvider{}, db, nil)
	ctx := context.Background()

	fuel, err := shared.NewFuel(100, 100)
	require.NoError(t, err)
	cargo, err := shared.NewCargo(40, 0, nil)
	require.NoError(t, err)
	location, err := shared.NewWaypoint("X1-AU21-K82", 0, 0)
	require.NoError(t, err)

	module := navigation.NewShipModule("MODULE_CARGO_HOLD_I", 15, 0, navigation.NewShipRequirements(1, 0, 1))
	mount := navigation.NewShipMount("MOUNT_MINING_LASER_I", "Mining Laser I", 30, []string{"IRON_ORE"}, navigation.NewShipRequirements(1, 0, 1))

	ship, err := navigation.NewShip(
		"ROUNDTRIP-1",
		playerID,
		location,
		fuel,
		100,
		40,
		cargo,
		9,
		"FRAME_FRIGATE",
		"FRIGATE",
		[]*navigation.ShipModule{module},
		navigation.NavStatusInOrbit,
	)
	require.NoError(t, err)
	ship.SetReactor("REACTOR_FISSION_I", "Fission Reactor I", 31, navigation.NewShipRequirements(0, 1, 0))
	ship.SetSlots(3, 2)
	ship.SetMounts([]*navigation.ShipMount{mount})
	ship.SetCrew(2, 3, 4)

	require.NoError(t, repo.Save(ctx, ship))

	// Schema-level check: the raw columns shipToModel wrote.
	var model persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ? AND player_id = ?", "ROUNDTRIP-1", playerID.Value()).First(&model).Error)
	require.Equal(t, "REACTOR_FISSION_I", model.ReactorSymbol)
	require.Equal(t, "Fission Reactor I", model.ReactorName)
	require.Equal(t, 31, model.ReactorPowerOutput)
	require.Equal(t, 1, model.ReactorRequirementsCrew)
	require.Equal(t, 3, model.ModuleSlots)
	require.Equal(t, 2, model.MountingPoints)
	require.Equal(t, 2, model.CrewCurrent)
	require.Equal(t, 3, model.CrewRequired)
	require.Equal(t, 4, model.CrewCapacity)
	require.Contains(t, model.Mounts, "MOUNT_MINING_LASER_I")

	// Behavior-level check: FindBySymbol (modelToDomain) reconstructs the
	// same values on a fresh domain aggregate.
	got, err := repo.FindBySymbol(ctx, "ROUNDTRIP-1", playerID)
	require.NoError(t, err)
	require.Equal(t, "REACTOR_FISSION_I", got.ReactorSymbol())
	require.Equal(t, "Fission Reactor I", got.ReactorName())
	require.Equal(t, 31, got.ReactorPowerOutput())
	require.Equal(t, 1, got.ReactorRequirements().Crew())
	require.Equal(t, 3, got.ModuleSlots())
	require.Equal(t, 2, got.MountingPoints())
	require.Equal(t, 2, got.CrewCurrent())
	require.Equal(t, 3, got.CrewRequired())
	require.Equal(t, 4, got.CrewCapacity())
	require.Len(t, got.Mounts(), 1)
	require.Equal(t, "MOUNT_MINING_LASER_I", got.Mounts()[0].Symbol())
	require.Equal(t, 1, got.Mounts()[0].Requirements().Power())
	require.Len(t, got.Modules(), 1)
	require.Equal(t, "MODULE_CARGO_HOLD_I", got.Modules()[0].Symbol())

	// Integration sanity: the round-tripped ship is directly usable by the
	// feasibility function (the whole point of sp-el60) with no extra wiring.
	// Free power = 31 reactor - 1 (installed module) - 1 (installed mount) = 29;
	// candidate needs 31, so it is short by 2.
	candidate := navigation.NewShipModule("MODULE_CARGO_HOLD_III", 100, 0, navigation.NewShipRequirements(31, 0, 1))
	feasibility := navigation.CheckModuleInstallFeasibility(got, candidate)
	require.False(t, feasibility.CanInstall)
	require.Equal(t, 2, feasibility.PowerShort)
}

// TestFindModuleRequirements_FoundOnAnotherShip proves a candidate's
// requirements can be resolved from anywhere in the fleet, not just the ship
// being queried (sp-el60 acceptance fix) - there is no catalog of unowned
// module specs, so a symbol observed installed on any ship is the only real
// data source for a candidate lookup on a different ship.
func TestFindModuleRequirements_FoundOnAnotherShip(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	playerModel := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&playerModel).Error)
	playerID := shared.MustNewPlayerID(playerModel.ID)

	repo := NewShipRepository(nil, nil, nil, powerSlotsFakeWaypointProvider{}, db, nil)
	ctx := context.Background()

	fuel, err := shared.NewFuel(100, 100)
	require.NoError(t, err)
	cargo, err := shared.NewCargo(40, 0, nil)
	require.NoError(t, err)
	location, err := shared.NewWaypoint("X1-AU21-K82", 0, 0)
	require.NoError(t, err)

	// A different ship from the one that will be queried - carries the
	// candidate symbol installed, with known requirements.
	holdIII := navigation.NewShipModule("MODULE_CARGO_HOLD_III", 40, 0, navigation.NewShipRequirements(1, 0, 2))
	carrier, err := navigation.NewShip("CARRIER-1", playerID, location, fuel, 100, 40, cargo, 9,
		"FRAME_FRIGATE", "FRIGATE", []*navigation.ShipModule{holdIII}, navigation.NavStatusInOrbit)
	require.NoError(t, err)
	require.NoError(t, repo.Save(ctx, carrier))

	got, found, err := repo.FindModuleRequirements(ctx, "MODULE_CARGO_HOLD_III")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, 1, got.Power())
	require.Equal(t, 0, got.Crew())
	require.Equal(t, 2, got.Slots())
}

// TestFindModuleRequirements_NotFoundAnywhere proves the lookup reports
// false (never a zero-valued ShipRequirements masquerading as a real one)
// when no ship in the fleet has ever carried the candidate symbol (sp-el60
// acceptance fix) - this is the fail-closed signal callers must turn into
// UnknownRequirementsFeasibility rather than a trivially-satisfied
// zero-filled feasibility check.
func TestFindModuleRequirements_NotFoundAnywhere(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	playerModel := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&playerModel).Error)
	playerID := shared.MustNewPlayerID(playerModel.ID)

	repo := NewShipRepository(nil, nil, nil, powerSlotsFakeWaypointProvider{}, db, nil)
	ctx := context.Background()

	fuel, err := shared.NewFuel(100, 100)
	require.NoError(t, err)
	cargo, err := shared.NewCargo(40, 0, nil)
	require.NoError(t, err)
	location, err := shared.NewWaypoint("X1-AU21-K82", 0, 0)
	require.NoError(t, err)

	module := navigation.NewShipModule("MODULE_CARGO_HOLD_I", 15, 0, navigation.NewShipRequirements(1, 0, 1))
	ship, err := navigation.NewShip("LONELY-1", playerID, location, fuel, 100, 40, cargo, 9,
		"FRAME_FRIGATE", "FRIGATE", []*navigation.ShipModule{module}, navigation.NavStatusInOrbit)
	require.NoError(t, err)
	require.NoError(t, repo.Save(ctx, ship))

	_, found, err := repo.FindModuleRequirements(ctx, "MODULE_CARGO_HOLD_III")
	require.NoError(t, err)
	require.False(t, found)
}
