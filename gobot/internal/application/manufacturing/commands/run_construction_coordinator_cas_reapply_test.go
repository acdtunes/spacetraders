package commands

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// These are the sp-wa7c regression tests for the construction coordinator's
// per-tick claim release (releaseClaims), exercised against a REAL, version-
// guarded ShipRepository (sqlite) rather than a fake — a fake cannot reproduce
// the ships.version CAS conflict that the fix resolves.
//
// The live gate-FAB stall: releaseClaims runs on EVERY drain tick (deferred in
// drainOnce) over the hulls FindByContainer returns. FindByContainer serves the
// repository's short-lived ship-list cache, so under sp-ubwi's fan-out (2-3
// concurrent lot-tasks on shared hulls) the tick's release operated on a STALE
// snapshot and its raw Save() last-write-wins-clobbered a concurrent writer's
// fresh cargo/nav update — aborting FAB supply tasks mid-flight. The migrated
// releaseClaims re-applies ONLY the release on the FRESH row via SaveWithRetry,
// so a colliding cargo update survives, and its applicability guard lives INSIDE
// the mutation so a hull re-claimed by another container is skipped, not ripped
// away.
//
// The disabled-CAS / last-write-wins fallback that releaseClaims now inherits by
// routing through SaveWithRetry is proven at the repository boundary in
// ship_repository_cas_retry_test.go (TestSaveWithRetry_Disabled_FallsBackToLast
// WriteWins / _ExhaustsThenFallsBackToLastWriteWins), so it is not duplicated here.

// constructionReapplyStubWaypoints forces modelToDomain's denormalized-coordinate
// fallback so the real repo needs no waypoint rows (mirrors the api-package stub).
type constructionReapplyStubWaypoints struct{}

func (constructionReapplyStubWaypoints) GetWaypoint(_ context.Context, _, _ string, _ int) (*shared.Waypoint, error) {
	return nil, errors.New("stub: use denormalized fallback")
}

// seedGateHauler seeds a docked hull claimed by containerID at row version 1 (so
// the version-guarded CAS path engages — a v0 row would take the unconditional
// insert branch and never guard a concurrent writer), carrying 100 IRON_ORE.
func seedGateHauler(t *testing.T, db *gorm.DB, symbol string, playerID int, containerID string) {
	t.Helper()
	assignedAt := time.Now().Add(-1 * time.Minute)
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       symbol,
		PlayerID:         playerID,
		AssignmentStatus: "active",
		ContainerID:      &containerID,
		AssignedAt:       &assignedAt,
		NavStatus:        "DOCKED",
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
}

// GREEN (the fix): a concurrent writer unloads cargo on the same gate hull while
// the coordinator holds a cached snapshot of it. The migrated releaseClaims
// re-applies ONLY the release on the FRESH row, so the concurrent cargo unload
// SURVIVES (no clobber) while the claim is still returned to idle. Under the old
// raw Save() the tick's stale snapshot (cargo 100) would have last-write-wins
// clobbered the concurrent unload (cargo 60) back up — the exact FAB stall.
func TestReleaseClaims_ConcurrentCargoWriteSurvivesTickRelease_NoClobber(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	playerRow := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&playerRow).Error)
	pid := shared.MustNewPlayerID(playerRow.ID)

	const gateCtr = "gate-fab-ctr"
	require.NoError(t, db.Create(&persistence.ContainerModel{ID: gateCtr, PlayerID: playerRow.ID, Status: "RUNNING"}).Error)
	seedGateHauler(t, db, "GATE-1", playerRow.ID, gateCtr)

	// The coordinator's repo. Warm its ship-list cache with the claimed hull
	// BEFORE the concurrent write, so the tick's FindByContainer serves the stale
	// snapshot (cargo 100) exactly as a live drain tick does under fan-out.
	coordinatorRepo := api.NewShipRepository(nil, nil, nil, constructionReapplyStubWaypoints{}, db, nil)
	warm, err := coordinatorRepo.FindByContainer(context.Background(), gateCtr, pid)
	require.NoError(t, err)
	require.Len(t, warm, 1, "cache warmed with the one claimed gate hull")

	// A second, independent writer (a concurrent lot-task) unloads 40 units
	// (100 -> 60) and commits, bumping the row version behind the coordinator's
	// cache. It keeps the hull claimed by the same container so the tick still
	// finds + releases it. A distinct repo instance leaves the coordinator's
	// cache untouched, so its snapshot stays stale — the fan-out contention.
	writerRepo := api.NewShipRepository(nil, nil, nil, constructionReapplyStubWaypoints{}, db, nil)
	other, err := writerRepo.FindBySymbol(context.Background(), "GATE-1", pid)
	require.NoError(t, err)
	require.NoError(t, other.RemoveCargo("IRON_ORE", 40))
	require.NoError(t, writerRepo.Save(context.Background(), other))

	handler := NewRunConstructionCoordinatorHandler(nil, nil, coordinatorRepo, nil, nil, shared.NewRealClock())
	handler.releaseClaims(context.Background(), gateCtr, pid)

	var row persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "GATE-1").First(&row).Error)

	// The concurrent cargo unload SURVIVED — the fix. The old last-write-wins
	// Save would have written the tick's stale snapshot (cargo 100) here.
	require.Equal(t, 60, row.CargoUnits, "concurrent cargo unload must survive the tick release (no clobber)")
	// The claim was still returned to idle on the fresh row.
	require.Equal(t, "idle", row.AssignmentStatus, "hull released to idle pool at tick end")
	require.Nil(t, row.ContainerID, "container claim cleared")
}

// GREEN (the guard): between the tick's cached read and the release, the hull is
// re-claimed by ANOTHER container (a fresh acquisition by a different operation).
// The migrated releaseClaims re-checks ownership on the FRESH row inside the
// mutation, so it reports changed=false and SKIPS the write — the new owner's
// claim survives, with no spurious version bump. Under the old raw Save() the
// tick's stale snapshot (still claimed by us) would ForceRelease and clobber the
// hull to idle, ripping it out from under its new owner.
func TestReleaseClaims_SkipsHullReclaimedByAnotherContainer(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	playerRow := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&playerRow).Error)
	pid := shared.MustNewPlayerID(playerRow.ID)

	const ourCtr = "gate-fab-ctr"
	const otherCtr = "other-op-ctr"
	require.NoError(t, db.Create(&persistence.ContainerModel{ID: ourCtr, PlayerID: playerRow.ID, Status: "RUNNING"}).Error)
	require.NoError(t, db.Create(&persistence.ContainerModel{ID: otherCtr, PlayerID: playerRow.ID, Status: "RUNNING"}).Error)
	seedGateHauler(t, db, "GATE-2", playerRow.ID, ourCtr)

	// Warm the coordinator's cache while the hull is still ours.
	coordinatorRepo := api.NewShipRepository(nil, nil, nil, constructionReapplyStubWaypoints{}, db, nil)
	warm, err := coordinatorRepo.FindByContainer(context.Background(), ourCtr, pid)
	require.NoError(t, err)
	require.Len(t, warm, 1)

	// Concurrent re-claim: the hull now belongs to another container at a bumped
	// version. Modeled with a direct row write (a valid DB precondition) so the
	// coordinator's cache stays stale — the behavior under test is releaseClaims
	// reading its stale snapshot and correctly declining to touch the fresh row.
	require.NoError(t, db.Model(&persistence.ShipModel{}).
		Where("ship_symbol = ?", "GATE-2").
		Updates(map[string]interface{}{
			"version":           2,
			"container_id":      otherCtr,
			"assignment_status": "active",
		}).Error)

	handler := NewRunConstructionCoordinatorHandler(nil, nil, coordinatorRepo, nil, nil, shared.NewRealClock())
	handler.releaseClaims(context.Background(), ourCtr, pid)

	var row persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "GATE-2").First(&row).Error)

	// The new owner's claim SURVIVED — releaseClaims skipped the write because the
	// fresh row is no longer ours. The old raw Save would have ripped it to idle.
	require.Equal(t, "active", row.AssignmentStatus, "another container's fresh claim must not be ripped away")
	require.NotNil(t, row.ContainerID, "the hull must still be claimed by its new owner")
	require.Equal(t, otherCtr, *row.ContainerID, "claim still held by the re-claiming container")
	require.Equal(t, 2, row.Version, "no spurious version bump — the guard skipped the write")
}
