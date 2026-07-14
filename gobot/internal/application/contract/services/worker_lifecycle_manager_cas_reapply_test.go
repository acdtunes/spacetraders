package services

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// reclaimStubWaypoints forces modelToDomain's denormalized-coordinate fallback so
// the real repo needs no waypoint rows (mirrors the api-package stub).
type reclaimStubWaypoints struct{}

func (reclaimStubWaypoints) GetWaypoint(_ context.Context, _, _ string, _ int) (*shared.Waypoint, error) {
	return nil, errors.New("stub: use denormalized fallback")
}

// stubFailedContainerRepo reports a fixed set of FAILED-worker summaries so the
// reclaim path enumerates exactly the container(s) the test set up.
type stubFailedContainerRepo struct {
	summaries []persistence.ContainerSummary
}

func (r *stubFailedContainerRepo) ListByStatusSimple(_ context.Context, status string, _ *int) ([]persistence.ContainerSummary, error) {
	if status != "FAILED" {
		return nil, nil
	}
	return r.summaries, nil
}

// reclaimRaceRepo wraps the REAL ship repository and, the first time the reclaim
// loads the dead worker's roster (FindByContainer), commits a concurrent writer's
// update on the same hull — bumping ships.version behind the reclaim's snapshot.
// This reproduces, against a real sqlite row, the exact last-write-wins race the
// sp-wa7c migration closes: the reclaim's release must land on the FRESH row, not
// clobber the concurrent write back to the snapshot.
type reclaimRaceRepo struct {
	navigation.ShipRepository
	once   sync.Once
	inject func()
}

func (r *reclaimRaceRepo) FindByContainer(ctx context.Context, containerID string, playerID shared.PlayerID) ([]*navigation.Ship, error) {
	ships, err := r.ShipRepository.FindByContainer(ctx, containerID, playerID)
	r.once.Do(func() {
		if r.inject != nil {
			r.inject()
		}
	})
	return ships, err
}

// TestReclaimInterruptedWorkers_ReleaseDoesNotClobberFreshCargo drives the
// interrupted-worker reclaim (a migrated ForceRelease site, sp-wa7c) against a
// real, version-guarded repository. A concurrent writer unloads cargo on the same
// hull while the reclaim holds its snapshot; the migrated SaveWithRetry re-applies
// ONLY the release on the fresh row, so the concurrent cargo update survives
// instead of being last-write-wins clobbered by the stale snapshot.
func TestReclaimInterruptedWorkers_ReleaseDoesNotClobberFreshCargo(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	playerRow := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&playerRow).Error)
	pid := shared.MustNewPlayerID(playerRow.ID)

	deadWorker := "dead-worker"
	require.NoError(t, db.Create(&persistence.ContainerModel{ID: deadWorker, PlayerID: playerRow.ID, Status: "FAILED"}).Error)

	assignedAt := time.Now().Add(-1 * time.Hour)
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "WORK-1",
		PlayerID:         playerRow.ID,
		AssignmentStatus: "active",
		ContainerID:      &deadWorker,
		AssignedAt:       &assignedAt,
		NavStatus:        "IN_ORBIT",
		LocationSymbol:   "X1-TEST-A1",
		SystemSymbol:     "X1-TEST",
		FuelCurrent:      400,
		FuelCapacity:     1000,
		CargoCapacity:    200,
		CargoUnits:       100,
		CargoInventory:   `[{"symbol":"IRON_ORE","name":"Iron Ore","description":"x","units":100}]`,
		EngineSpeed:      10,
		Version:          1,
	}).Error)

	realRepo := api.NewShipRepository(nil, nil, nil, reclaimStubWaypoints{}, db, nil)
	raceRepo := &reclaimRaceRepo{ShipRepository: realRepo}
	raceRepo.inject = func() {
		// Concurrent writer unloads 40 units (100 -> 60), keeping the hull on the
		// dead worker so the reclaim still has a release to apply on the fresh row.
		other, ferr := realRepo.FindBySymbol(context.Background(), "WORK-1", pid)
		require.NoError(t, ferr)
		require.NoError(t, other.RemoveCargo("IRON_ORE", 40))
		require.NoError(t, realRepo.Save(context.Background(), other))
	}

	containerRepo := &stubFailedContainerRepo{summaries: []persistence.ContainerSummary{
		{ID: deadWorker, ContainerType: "CONTRACT_WORKFLOW", Status: "FAILED"},
	}}
	manager := NewWorkerLifecycleManager(nil, containerRepo, raceRepo)

	reclaimed, err := manager.ReclaimShipsFromInterruptedWorkers(context.Background(), playerRow.ID, shared.NewRealClock())
	require.NoError(t, err)
	require.Equal(t, 1, reclaimed, "the interrupted hull is reclaimed")

	var row persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "WORK-1").First(&row).Error)

	require.Equal(t, 60, row.CargoUnits, "concurrent cargo unload must survive the interrupted-worker reclaim (no clobber)")
	require.Equal(t, "idle", row.AssignmentStatus, "the interrupted claim is released on fresh state")
	require.Nil(t, row.ContainerID, "container link cleared")
}

// TestReclaimInterruptedWorkers_SkipsHullReclaimedByAnotherContainer proves the
// migrated closure's applicability guard (RULINGS #7): if a concurrent writer
// re-claims the hull to a LIVE container before the reclaim's release lands, the
// re-apply on the fresh row sees a foreign container, reports changed=false, and
// skips the write — so the reclaim never steals a hull out from under its new
// owner (the pre-migration last-write-wins Save would have clobbered it to idle).
func TestReclaimInterruptedWorkers_SkipsHullReclaimedByAnotherContainer(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	playerRow := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&playerRow).Error)
	pid := shared.MustNewPlayerID(playerRow.ID)

	deadWorker := "dead-worker"
	liveContainer := "live-ctr"
	require.NoError(t, db.Create(&persistence.ContainerModel{ID: deadWorker, PlayerID: playerRow.ID, Status: "FAILED"}).Error)
	require.NoError(t, db.Create(&persistence.ContainerModel{ID: liveContainer, PlayerID: playerRow.ID, Status: "RUNNING"}).Error)

	assignedAt := time.Now().Add(-1 * time.Hour)
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "WORK-2",
		PlayerID:         playerRow.ID,
		AssignmentStatus: "active",
		ContainerID:      &deadWorker,
		AssignedAt:       &assignedAt,
		NavStatus:        "IN_ORBIT",
		LocationSymbol:   "X1-TEST-A1",
		SystemSymbol:     "X1-TEST",
		FuelCurrent:      400,
		FuelCapacity:     1000,
		CargoCapacity:    200,
		CargoUnits:       0,
		EngineSpeed:      10,
		Version:          1,
	}).Error)

	realRepo := api.NewShipRepository(nil, nil, nil, reclaimStubWaypoints{}, db, nil)
	raceRepo := &reclaimRaceRepo{ShipRepository: realRepo}
	raceRepo.inject = func() {
		// Concurrent writer transfers the hull to a LIVE container, bumping the row
		// version behind the reclaim's snapshot.
		other, ferr := realRepo.FindBySymbol(context.Background(), "WORK-2", pid)
		require.NoError(t, ferr)
		require.NoError(t, other.TransferToContainer(liveContainer, shared.NewRealClock()))
		require.NoError(t, realRepo.Save(context.Background(), other))
	}

	containerRepo := &stubFailedContainerRepo{summaries: []persistence.ContainerSummary{
		{ID: deadWorker, ContainerType: "CONTRACT_WORKFLOW", Status: "FAILED"},
	}}
	manager := NewWorkerLifecycleManager(nil, containerRepo, raceRepo)

	reclaimed, err := manager.ReclaimShipsFromInterruptedWorkers(context.Background(), playerRow.ID, shared.NewRealClock())
	require.NoError(t, err)
	require.Equal(t, 0, reclaimed, "a hull re-claimed by a live container is not reclaimed")

	var row persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "WORK-2").First(&row).Error)

	require.Equal(t, "active", row.AssignmentStatus, "the hull stays claimed to its new live container")
	require.NotNil(t, row.ContainerID, "the reclaim must not release a hull re-claimed elsewhere")
	require.Equal(t, liveContainer, *row.ContainerID, "the reclaim did not steal the hull from its new owner")
}
