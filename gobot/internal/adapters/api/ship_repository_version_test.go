package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// stubWaypoints forces modelToDomain's fallback branch (denormalized model
// coordinates) so tests need no waypoint rows. Embeds the interface: only
// GetWaypoint is overridden. Shared by the mailbox-phase tests too.
type stubWaypoints struct{ system.IWaypointProvider }

func (stubWaypoints) GetWaypoint(_ context.Context, _ string, _ string, _ int) (*shared.Waypoint, error) {
	return nil, errors.New("stub: use denormalized fallback")
}

// newShipWriteTestRepo mirrors newDedicationTestRepo
// (ship_repository_claim_dedication_test.go:19) but with a waypoint-provider
// stub because these tests exercise FindBySymbol → modelToDomain. Shared by
// the mailbox-phase tests.
func newShipWriteTestRepo(t *testing.T) (*ShipRepository, *gorm.DB, shared.PlayerID) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	player := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	return NewShipRepository(nil, nil, nil, stubWaypoints{}, db, nil), db, shared.MustNewPlayerID(player.ID)
}

func seedShip(t *testing.T, db *gorm.DB, playerID int, symbol, navStatus string, fuelCurrent int) {
	t.Helper()
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       symbol,
		PlayerID:         playerID,
		AssignmentStatus: "idle",
		NavStatus:        navStatus,
		LocationSymbol:   "X1-KN67-A1",
		SystemSymbol:     "X1-KN67",
		FuelCurrent:      fuelCurrent,
		FuelCapacity:     1000,
		CargoCapacity:    40,
		EngineSpeed:      10,
		Version:          1,
	}).Error)
}

// Two entities loaded at the same version: the first Save wins and bumps the
// version; the second is a DETECTED conflict — counted, logged, and then
// applied via the legacy last-write-wins fallback (behavior preserved,
// visibility added). This is the probe: in production this counter measures
// how often the race class actually fires.
func TestSave_DetectsVersionConflictAndFallsBack(t *testing.T) {
	repo, db, pid := newShipWriteTestRepo(t)
	seedShip(t, db, pid.Value(), "TORWIND-10", "IN_ORBIT", 100)

	a, err := repo.FindBySymbol(context.Background(), "TORWIND-10", pid)
	require.NoError(t, err)
	b, err := repo.FindBySymbol(context.Background(), "TORWIND-10", pid)
	require.NoError(t, err)
	require.Equal(t, 1, a.PersistedVersion())

	before := shipVersionConflicts.Load()

	require.NoError(t, a.Refuel(50))
	require.NoError(t, repo.Save(context.Background(), a))
	require.Equal(t, before, shipVersionConflicts.Load(), "first save is conflict-free")
	require.Equal(t, 2, a.PersistedVersion(), "committed save advances the entity's version")

	require.NoError(t, b.Refuel(1))
	require.NoError(t, repo.Save(context.Background(), b), "conflict is telemetry, never an error")
	require.Equal(t, before+1, shipVersionConflicts.Load(), "stale-version save must be counted")

	var row persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-10").First(&row).Error)
	require.Equal(t, 101, row.FuelCurrent, "fallback preserves today's last-write-wins outcome")
}

// An API-born entity (PersistedVersion 0 — never loaded from a row) uses the
// legacy unconditional upsert: inserts and first-sync writes never count as
// conflicts.
func TestSave_UnknownVersionUsesLegacyUpsert(t *testing.T) {
	repo, db, pid := newShipWriteTestRepo(t)
	seedShip(t, db, pid.Value(), "TORWIND-12", "IN_ORBIT", 5)

	ship, err := repo.FindBySymbol(context.Background(), "TORWIND-12", pid)
	require.NoError(t, err)
	ship.SetPersistedVersion(0) // simulate API-born reconstruction

	before := shipVersionConflicts.Load()
	require.NoError(t, repo.Save(context.Background(), ship))
	require.Equal(t, before, shipVersionConflicts.Load())
}

// Back-to-back saves of the SAME entity never conflict: each committed save
// advances the entity's PersistedVersion in lockstep with the row.
func TestSave_SequentialSavesOfSameEntityNeverConflict(t *testing.T) {
	repo, db, pid := newShipWriteTestRepo(t)
	seedShip(t, db, pid.Value(), "TORWIND-13", "IN_ORBIT", 0)

	ship, err := repo.FindBySymbol(context.Background(), "TORWIND-13", pid)
	require.NoError(t, err)

	before := shipVersionConflicts.Load()
	for i := 0; i < 5; i++ {
		require.NoError(t, ship.Refuel(1))
		require.NoError(t, repo.Save(context.Background(), ship))
	}
	require.Equal(t, before, shipVersionConflicts.Load())
	require.Equal(t, 6, ship.PersistedVersion())
}
