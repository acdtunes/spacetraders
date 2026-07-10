package outfitting

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// outfitFakeAPIClient stubs only the APIClient methods the outfitting op calls:
// GetShipyard/GetAgent (floor gate), DockShip (dock), Install/RemoveShipModule
// (the modification), GetShip (SyncShipFromAPI persist) and GetShipModules
// (list). Every other method stays nil via the embedded interface. Call
// counters let tests assert the guard/claim ordering (e.g. a refused claim or a
// floor breach must never reach the install API).
type outfitFakeAPIClient struct {
	ports.APIClient

	shipData      *navigation.ShipData
	shipyard      *ports.ShipyardData
	shipyardErr   error
	agent         *player.AgentData
	agentErr      error
	installResult *ports.ModuleModificationResult
	installErr    error
	removeResult  *ports.ModuleModificationResult
	modules       []ports.ModuleInfo

	installCalls int
	removeCalls  int
	dockCalls    int
}

func (f *outfitFakeAPIClient) GetShip(_ context.Context, _, _ string) (*navigation.ShipData, error) {
	return f.shipData, nil
}

func (f *outfitFakeAPIClient) GetShipyard(_ context.Context, _, _, _ string) (*ports.ShipyardData, error) {
	if f.shipyardErr != nil {
		return nil, f.shipyardErr
	}
	return f.shipyard, nil
}

func (f *outfitFakeAPIClient) GetAgent(_ context.Context, _ string) (*player.AgentData, error) {
	if f.agentErr != nil {
		return nil, f.agentErr
	}
	return f.agent, nil
}

func (f *outfitFakeAPIClient) DockShip(_ context.Context, _, _ string) error {
	f.dockCalls++
	return nil
}

func (f *outfitFakeAPIClient) InstallShipModule(_ context.Context, _, _, _ string) (*ports.ModuleModificationResult, error) {
	f.installCalls++
	if f.installErr != nil {
		return nil, f.installErr
	}
	return f.installResult, nil
}

func (f *outfitFakeAPIClient) RemoveShipModule(_ context.Context, _, _, _ string) (*ports.ModuleModificationResult, error) {
	f.removeCalls++
	return f.removeResult, nil
}

func (f *outfitFakeAPIClient) GetShipModules(_ context.Context, _, _ string) ([]ports.ModuleInfo, error) {
	return f.modules, nil
}

type outfitFakePlayerRepo struct {
	player.PlayerRepository
	p *player.Player
}

func (f *outfitFakePlayerRepo) FindByID(_ context.Context, _ shared.PlayerID) (*player.Player, error) {
	return f.p, nil
}

// outfitFakeWaypointProvider always errors so modelToDomain falls back to
// building the ship's location straight from the persisted LocationSymbol.
type outfitFakeWaypointProvider struct{}

func (outfitFakeWaypointProvider) GetWaypoint(_ context.Context, _, _ string, _ int) (*shared.Waypoint, error) {
	return nil, errors.New("stub: waypoint lookup not needed by this test")
}

// newOutfitHarness wires a real ShipRepository + real container repository over
// a real (FK-enforcing) sqlite DB, with the fake API client. The real repo is
// used deliberately so ClaimShip, Dock, SyncShipFromAPI and the assignment
// round-trip are all exercised end to end.
func newOutfitHarness(t *testing.T, fake *outfitFakeAPIClient) (*OutfittingHandler, *gorm.DB, int) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	playerRow := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&playerRow).Error)
	playerID := shared.MustNewPlayerID(playerRow.ID)

	playerRepo := &outfitFakePlayerRepo{p: &player.Player{ID: playerID, Token: "tok"}}
	shipRepo := api.NewShipRepository(fake, playerRepo, nil, outfitFakeWaypointProvider{}, db, nil)
	containerRepo := persistence.NewContainerRepository(db)
	handler := NewOutfittingHandler(shipRepo, playerRepo, fake, containerRepo, nil)

	return handler, db, playerRow.ID
}

const cargoHold3 = "MODULE_CARGO_HOLD_III"

func fetchShip(t *testing.T, db *gorm.DB, symbol string) persistence.ShipModel {
	t.Helper()
	var model persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", symbol).First(&model).Error)
	return model
}

func containerCount(t *testing.T, db *gorm.DB, playerID int) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(&persistence.ContainerModel{}).Where("player_id = ?", playerID).Count(&n).Error)
	return n
}

// TestInstallModule_HappyPath is the claim → verify-cargo → floor → dock →
// install → persist → release flow. It asserts the response and the persisted
// row carry the NEW cargo capacity, and the claim is released (RULING #3/#7).
func TestInstallModule_HappyPath_PersistsCapacityAndReleasesClaim(t *testing.T) {
	fake := &outfitFakeAPIClient{
		shipData:      &navigation.ShipData{Symbol: "SHIP-1", Location: "X1-JP61-A1", NavStatus: "DOCKED", CargoCapacity: 320, EngineSpeed: 10, FrameSymbol: "FRAME_FRIGATE", Role: "HAULER"},
		shipyard:      &ports.ShipyardData{Symbol: "X1-JP61-A1", ModificationFee: 4200},
		agent:         &player.AgentData{Credits: 800000},
		installResult: &ports.ModuleModificationResult{Fee: 4200, CargoCapacity: 320, Modules: []ports.ModuleInfo{{Symbol: cargoHold3, Name: "Cargo Hold III", Capacity: 120}}},
	}
	handler, db, pid := newOutfitHarness(t, fake)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "SHIP-1", PlayerID: pid,
		NavStatus: "DOCKED", LocationSymbol: "X1-JP61-A1", SystemSymbol: "X1-JP61", EngineSpeed: 10,
		CargoCapacity: 200, CargoUnits: 1,
		CargoInventory:   `[{"symbol":"MODULE_CARGO_HOLD_III","name":"Cargo Hold III","description":"x","units":1}]`,
		Modules:          "[]",
		AssignmentStatus: "idle",
	}).Error)

	pidInt := pid
	resp, err := handler.Handle(context.Background(), &InstallModuleCommand{ShipSymbol: "SHIP-1", ModuleSymbol: cargoHold3, PlayerID: &pidInt})
	require.NoError(t, err)

	installResp, ok := resp.(*InstallModuleResponse)
	require.True(t, ok, "expected *InstallModuleResponse")
	require.True(t, installResp.Success)
	require.Equal(t, 320, installResp.CargoCapacity, "response must carry the new cargo capacity")
	require.Equal(t, 4200, installResp.Fee)
	require.Equal(t, 1, fake.installCalls, "install API must be called exactly once")
	require.GreaterOrEqual(t, fake.dockCalls, 1, "the ship must be docked before installing")

	model := fetchShip(t, db, "SHIP-1")
	require.Equal(t, 320, model.CargoCapacity, "the persisted cargo capacity must reflect the install")
	require.Equal(t, "idle", model.AssignmentStatus, "the claim must be released after the op")
	require.Nil(t, model.ContainerID, "the claim's container link must be cleared")
	require.Zero(t, containerCount(t, db, pid), "the lightweight outfitting container row must be removed")
}

// TestInstallModule_ModuleNotInCargo asserts the honest pre-flight error and
// that the claim is released (no install attempted).
func TestInstallModule_ModuleNotInCargo_HonestError_ReleasesClaim(t *testing.T) {
	fake := &outfitFakeAPIClient{
		shipyard: &ports.ShipyardData{ModificationFee: 4200},
		agent:    &player.AgentData{Credits: 800000},
	}
	handler, db, pid := newOutfitHarness(t, fake)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "SHIP-1", PlayerID: pid,
		NavStatus: "DOCKED", LocationSymbol: "X1-JP61-A1", SystemSymbol: "X1-JP61", EngineSpeed: 10,
		CargoCapacity: 200, CargoUnits: 0, CargoInventory: "[]", Modules: "[]", AssignmentStatus: "idle",
	}).Error)

	pidInt := pid
	_, err := handler.Handle(context.Background(), &InstallModuleCommand{ShipSymbol: "SHIP-1", ModuleSymbol: cargoHold3, PlayerID: &pidInt})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not in cargo")
	require.Equal(t, 0, fake.installCalls, "no install must be attempted when the module isn't in cargo")

	model := fetchShip(t, db, "SHIP-1")
	require.Equal(t, "idle", model.AssignmentStatus, "the claim must be released after the honest error")
	require.Nil(t, model.ContainerID)
	require.Zero(t, containerCount(t, db, pid))
}

// TestInstallModule_HullDedicatedToAnotherFleet is the RULING #7 regression: a
// hull pinned to another fleet must be refused inside the atomic claim, left
// untouched, and never reach the install API.
func TestInstallModule_HullDedicatedToAnotherFleet_Refused(t *testing.T) {
	fake := &outfitFakeAPIClient{}
	handler, db, pid := newOutfitHarness(t, fake)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "SHIP-1", PlayerID: pid,
		NavStatus: "DOCKED", LocationSymbol: "X1-JP61-A1", SystemSymbol: "X1-JP61", EngineSpeed: 10,
		CargoCapacity:    200,
		CargoUnits:       1,
		CargoInventory:   `[{"symbol":"MODULE_CARGO_HOLD_III","name":"x","description":"x","units":1}]`,
		Modules:          "[]",
		AssignmentStatus: "idle",
		DedicatedFleet:   "contract",
	}).Error)

	pidInt := pid
	_, err := handler.Handle(context.Background(), &InstallModuleCommand{ShipSymbol: "SHIP-1", ModuleSymbol: cargoHold3, PlayerID: &pidInt})
	require.Error(t, err)

	var dedicated *shared.ShipDedicatedToOtherFleetError
	require.ErrorAs(t, err, &dedicated, "the refusal must surface the typed dedication error")
	require.Equal(t, "contract", dedicated.Fleet)
	require.Equal(t, 0, fake.installCalls, "a refused claim must never reach the install API")

	model := fetchShip(t, db, "SHIP-1")
	require.Equal(t, "idle", model.AssignmentStatus, "the refused hull must be left untouched")
	require.Nil(t, model.ContainerID)
	require.Equal(t, "contract", model.DedicatedFleet, "the pin must remain intact")
	require.Zero(t, containerCount(t, db, pid), "the orphan container row must be cleaned up")
}

// TestInstallModule_FloorBreach is the RULING #4 money guard: a modification
// fee that would drop treasury below the working-capital reserve is refused
// before any spend, and the claim is released.
func TestInstallModule_FloorBreach_FailsClosed_NoInstall(t *testing.T) {
	fake := &outfitFakeAPIClient{
		shipyard: &ports.ShipyardData{ModificationFee: 4200},
		agent:    &player.AgentData{Credits: 51000}, // 51000 - 4200 = 46800 < 50000 reserve
	}
	handler, db, pid := newOutfitHarness(t, fake)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "SHIP-1", PlayerID: pid,
		NavStatus: "DOCKED", LocationSymbol: "X1-JP61-A1", SystemSymbol: "X1-JP61", EngineSpeed: 10,
		CargoCapacity:    200,
		CargoUnits:       1,
		CargoInventory:   `[{"symbol":"MODULE_CARGO_HOLD_III","name":"x","description":"x","units":1}]`,
		Modules:          "[]",
		AssignmentStatus: "idle",
	}).Error)

	pidInt := pid
	_, err := handler.Handle(context.Background(), &InstallModuleCommand{ShipSymbol: "SHIP-1", ModuleSymbol: cargoHold3, PlayerID: &pidInt})
	require.Error(t, err)
	require.Contains(t, err.Error(), "working-capital reserve")
	require.Equal(t, 0, fake.installCalls, "the install must be blocked by the floor guard")

	model := fetchShip(t, db, "SHIP-1")
	require.Equal(t, "idle", model.AssignmentStatus, "the claim must be released after the floor abort")
	require.Nil(t, model.ContainerID)
}

// TestInstallModule_ShipyardFeeUnreadable is the RULING #4 fail-closed case for
// the price: if the modification fee cannot be read, do not spend.
func TestInstallModule_ShipyardFeeUnreadable_FailsClosed(t *testing.T) {
	fake := &outfitFakeAPIClient{
		shipyardErr: errors.New("no shipyard at this waypoint"),
		agent:       &player.AgentData{Credits: 800000},
	}
	handler, db, pid := newOutfitHarness(t, fake)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "SHIP-1", PlayerID: pid,
		NavStatus: "DOCKED", LocationSymbol: "X1-JP61-A1", SystemSymbol: "X1-JP61", EngineSpeed: 10,
		CargoCapacity:    200,
		CargoUnits:       1,
		CargoInventory:   `[{"symbol":"MODULE_CARGO_HOLD_III","name":"x","description":"x","units":1}]`,
		Modules:          "[]",
		AssignmentStatus: "idle",
	}).Error)

	pidInt := pid
	_, err := handler.Handle(context.Background(), &InstallModuleCommand{ShipSymbol: "SHIP-1", ModuleSymbol: cargoHold3, PlayerID: &pidInt})
	require.Error(t, err)
	require.Contains(t, err.Error(), "shipyard modification fee")
	require.Equal(t, 0, fake.installCalls)

	model := fetchShip(t, db, "SHIP-1")
	require.Equal(t, "idle", model.AssignmentStatus)
}

// TestRemoveModule_HappyPath asserts a removal persists the reduced capacity and
// releases the claim.
func TestRemoveModule_HappyPath_PersistsReducedCapacity(t *testing.T) {
	fake := &outfitFakeAPIClient{
		shipData:     &navigation.ShipData{Symbol: "SHIP-1", Location: "X1-JP61-A1", NavStatus: "DOCKED", CargoCapacity: 200, EngineSpeed: 10, FrameSymbol: "FRAME_FRIGATE"},
		shipyard:     &ports.ShipyardData{ModificationFee: 4200},
		agent:        &player.AgentData{Credits: 800000},
		removeResult: &ports.ModuleModificationResult{Fee: 4200, CargoCapacity: 200, Modules: []ports.ModuleInfo{}},
	}
	handler, db, pid := newOutfitHarness(t, fake)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "SHIP-1", PlayerID: pid,
		NavStatus: "DOCKED", LocationSymbol: "X1-JP61-A1", SystemSymbol: "X1-JP61", EngineSpeed: 10,
		CargoCapacity:    320,
		CargoInventory:   "[]",
		Modules:          `[{"symbol":"MODULE_CARGO_HOLD_III","capacity":120,"range":0}]`,
		AssignmentStatus: "idle",
	}).Error)

	pidInt := pid
	resp, err := handler.Handle(context.Background(), &RemoveModuleCommand{ShipSymbol: "SHIP-1", ModuleSymbol: cargoHold3, PlayerID: &pidInt})
	require.NoError(t, err)

	removeResp, ok := resp.(*RemoveModuleResponse)
	require.True(t, ok)
	require.True(t, removeResp.Success)
	require.Equal(t, 200, removeResp.CargoCapacity)
	require.Equal(t, 1, fake.removeCalls)

	model := fetchShip(t, db, "SHIP-1")
	require.Equal(t, 200, model.CargoCapacity, "removing the hold must persist the reduced capacity")
	require.Equal(t, "idle", model.AssignmentStatus)
}

// TestRemoveModule_ModuleNotInstalled asserts the honest error when the module
// isn't installed on the ship.
func TestRemoveModule_ModuleNotInstalled_HonestError(t *testing.T) {
	fake := &outfitFakeAPIClient{
		shipyard: &ports.ShipyardData{ModificationFee: 4200},
		agent:    &player.AgentData{Credits: 800000},
	}
	handler, db, pid := newOutfitHarness(t, fake)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "SHIP-1", PlayerID: pid,
		NavStatus: "DOCKED", LocationSymbol: "X1-JP61-A1", SystemSymbol: "X1-JP61", EngineSpeed: 10,
		CargoCapacity: 200, Modules: "[]", AssignmentStatus: "idle",
	}).Error)

	pidInt := pid
	_, err := handler.Handle(context.Background(), &RemoveModuleCommand{ShipSymbol: "SHIP-1", ModuleSymbol: cargoHold3, PlayerID: &pidInt})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not installed")
	require.Equal(t, 0, fake.removeCalls)

	model := fetchShip(t, db, "SHIP-1")
	require.Equal(t, "idle", model.AssignmentStatus, "the claim must be released after the honest error")
}

// TestListShipModules asserts the read-only list path returns the installed
// modules from the live API.
func TestListShipModules_ReturnsModules(t *testing.T) {
	fake := &outfitFakeAPIClient{
		modules: []ports.ModuleInfo{{Symbol: cargoHold3, Name: "Cargo Hold III", Capacity: 120}},
	}
	handler, db, pid := newOutfitHarness(t, fake)

	require.NoError(t, db.Create(&persistence.ShipModel{ShipSymbol: "SHIP-1", PlayerID: pid, AssignmentStatus: "idle"}).Error)

	pidInt := pid
	resp, err := handler.Handle(context.Background(), &ListShipModulesQuery{ShipSymbol: "SHIP-1", PlayerID: &pidInt})
	require.NoError(t, err)

	listResp, ok := resp.(*ListShipModulesResponse)
	require.True(t, ok)
	require.Len(t, listResp.Modules, 1)
	require.Equal(t, cargoHold3, listResp.Modules[0].Symbol)
	require.Equal(t, 120, listResp.Modules[0].Capacity)
}
