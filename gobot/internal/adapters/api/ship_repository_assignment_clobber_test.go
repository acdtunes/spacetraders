package api

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// TestSave_StaleEntityDoesNotResurrectAReleasedClaim is the core regression,
// reproducing the observed production sequence on one continuously-running daemon:
// a coordinator claims the hull, the nav stack loads that entity and carries it for
// the whole flight, the coordinator exits and its release lands — and then the
// flight's arrival/refuel write-back loses the version race and last-write-wins the
// dead claim back onto the row. The hull is then permanently orphaned: assigned to a
// container that no longer exists, so no release path will ever free it.
func TestSave_StaleEntityDoesNotResurrectAReleasedClaim(t *testing.T) {
	repo, db, playerID := newShipWriteTestRepo(t)
	seedShip(t, db, playerID.Value(), "TORWIND-30", "IN_ORBIT", 100)
	seedContainerParent(t, db, "cargo-liquidation-48887734", playerID.Value())

	require.NoError(t, repo.ClaimShip(context.Background(), "TORWIND-30", "cargo-liquidation-48887734", playerID, "contract"))

	// The nav stack loads the hull ONCE and flies the whole leg on that entity.
	inFlight, err := repo.FindBySymbol(context.Background(), "TORWIND-30", playerID)
	require.NoError(t, err)
	require.Equal(t, "cargo-liquidation-48887734", inFlight.ContainerID(), "precondition: the snapshot holds the claim")

	// The container exits and its release lands.
	released, err := repo.ReleaseContainerClaim(context.Background(), "TORWIND-30", playerID, "container_stopped")
	require.NoError(t, err)
	require.True(t, released, "precondition: the release actually freed the hull")

	before := assignmentClobbersPrevented.Load()

	// The flight arrives and refuels — a routine write-back from the stale entity.
	require.NoError(t, inFlight.Refuel(50))
	require.NoError(t, repo.Save(context.Background(), inFlight))

	var row persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-30").First(&row).Error)
	require.Equal(t, "idle", row.AssignmentStatus, "a stale write-back must not resurrect a released claim")
	require.Nil(t, row.ContainerID, "the hull must stay free, not orphaned under a container that no longer exists")
	require.Equal(t, 150, row.FuelCurrent, "the refuel still lands — only ownership is defended")
	require.Equal(t, before+1, assignmentClobbersPrevented.Load(),
		"a prevented resurrection must be counted + logged, not silently swallowed")

	// The entity is healed rather than left permanently stale, so its next save is
	// trustworthy for these columns instead of conflicting for the rest of the flight.
	require.False(t, inFlight.IsAssigned(), "the stale entity must be re-synced to the row's ownership")
}

// The quiet half of the same bug, in the other direction: an entity loaded BEFORE a
// claim erases that claim on its next write-back, taking the hull out from under a
// running worker. ClaimShip mutates only the ownership columns, so unless the claim
// advances ships.version the write-back's version guard still matches and commits
// with no conflict logged at all.
func TestSave_StaleEntityDoesNotEraseAClaimTakenAfterItLoaded(t *testing.T) {
	repo, db, playerID := newShipWriteTestRepo(t)
	seedShip(t, db, playerID.Value(), "TORWIND-32", "IN_ORBIT", 100)
	seedContainerParent(t, db, "contract-worker-1", playerID.Value())

	// The entity is loaded while the hull is still free.
	inFlight, err := repo.FindBySymbol(context.Background(), "TORWIND-32", playerID)
	require.NoError(t, err)
	require.False(t, inFlight.IsAssigned(), "precondition: the snapshot predates the claim")

	require.NoError(t, repo.ClaimShip(context.Background(), "TORWIND-32", "contract-worker-1", playerID, "contract"))

	require.NoError(t, inFlight.Refuel(50))
	require.NoError(t, repo.Save(context.Background(), inFlight))

	var row persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-32").First(&row).Error)
	require.Equal(t, "active", row.AssignmentStatus, "a stale write-back must not erase a live claim")
	require.NotNil(t, row.ContainerID)
	require.Equal(t, "contract-worker-1", *row.ContainerID, "the worker must keep the hull it claimed")
	require.Equal(t, string(navigation.AssignmentOwnerContainer), row.AssignmentOwner)
	require.Equal(t, 150, row.FuelCurrent, "the refuel still lands — only ownership is defended")
}

// The scoping guard for the fix. SaveWithRetry re-applies its mutation on a FRESHLY
// loaded row, so its last-write-wins fallback carries the AUTHORITATIVE ownership —
// defending the persisted columns there would turn every release into a silent
// no-op. Retries are exhausted deliberately (the row moves on every attempt) so the
// release lands through that fallback, the exact path the fix must leave alone.
func TestSaveWithRetry_ReleaseStillClearsTheClaimThroughTheLastWriteWinsFallback(t *testing.T) {
	repo, db, playerID := newShipWriteTestRepo(t)
	repo.SetCASRetryPolicy(1) // attempts 0 and 1, then the fallback
	seedShip(t, db, playerID.Value(), "TORWIND-31", "IN_ORBIT", 100)
	seedContainerParent(t, db, "contract-worker-2", playerID.Value())

	require.NoError(t, repo.ClaimShip(context.Background(), "TORWIND-31", "contract-worker-2", playerID, "contract"))

	_, saved, err := repo.SaveWithRetry(context.Background(), "TORWIND-31", playerID,
		func(sh *navigation.Ship) (bool, error) {
			// A concurrent writer moves the row on every attempt, so the CAS never
			// lands and the release is applied through last-write-wins.
			bumpRowVersion(t, db, "TORWIND-31")
			sh.ForceRelease("worker_finished", repo.clock)
			return true, nil
		})

	require.NoError(t, err)
	require.True(t, saved)

	var row persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-31").First(&row).Error)
	require.Equal(t, "idle", row.AssignmentStatus, "a release re-applied on the fresh row must still clear the claim")
	require.Nil(t, row.ContainerID, "the container must be detached, not preserved")
	require.Equal(t, "worker_finished", row.ReleaseReason)
}

// A stale entity writes version = its own loaded version + 1. Left unclamped that
// moves the row BACKWARDS, so every later writer conflicts against it and the stale
// entity keeps winning — turning one instant race into a run of re-stamps (the
// production row sat at version 1 after hours of traffic while the conflict log
// reset from "past 27" to "past 3").
func TestSave_StaleEntityCannotLowerTheRowVersion(t *testing.T) {
	repo, db, playerID := newShipWriteTestRepo(t)
	seedShip(t, db, playerID.Value(), "TORWIND-33", "IN_ORBIT", 100)

	stale, err := repo.FindBySymbol(context.Background(), "TORWIND-33", playerID)
	require.NoError(t, err)
	require.Equal(t, 1, stale.PersistedVersion())

	for i := 0; i < 4; i++ {
		bumpRowVersion(t, db, "TORWIND-33")
	}
	var moved persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-33").First(&moved).Error)
	require.Equal(t, 5, moved.Version, "precondition: the row has moved well past the entity")

	require.NoError(t, stale.Refuel(1))
	require.NoError(t, repo.Save(context.Background(), stale))

	var row persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-33").First(&row).Error)
	require.GreaterOrEqual(t, row.Version, 5, "a stale entity must never move the row version backwards")
}

// Blast radius of the conflict fallback: the two single-write-path columns
// (ownership, and the dedicated_fleet tag) are taken from the row, and EVERYTHING
// else keeps today's last-write-wins behavior — a stale writer that refuelled must
// still land its fuel.
func TestSave_ConflictFallbackPreservesProtectedColumnsAndLandsTheRest(t *testing.T) {
	repo, db, playerID := newShipWriteTestRepo(t)
	seedShip(t, db, playerID.Value(), "TORWIND-34", "IN_ORBIT", 100)
	seedContainerParent(t, db, "mfg-worker-1", playerID.Value())

	stale, err := repo.FindBySymbol(context.Background(), "TORWIND-34", playerID)
	require.NoError(t, err)

	// Everything the entity cannot know about happens after its load.
	require.NoError(t, repo.AssignFleet(context.Background(), "TORWIND-34", "manufacturing", playerID))
	require.NoError(t, repo.ClaimShip(context.Background(), "TORWIND-34", "mfg-worker-1", playerID, "manufacturing"))
	bumpRowVersion(t, db, "TORWIND-34") // force the conflict branch

	require.NoError(t, stale.Refuel(50))
	require.NoError(t, repo.Save(context.Background(), stale))

	var row persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-34").First(&row).Error)
	require.Equal(t, "manufacturing", row.DedicatedFleet, "the live dedication survives the conflict fallback")
	require.Equal(t, "active", row.AssignmentStatus, "the live claim survives the conflict fallback")
	require.Equal(t, "mfg-worker-1", *row.ContainerID)
	require.Equal(t, 150, row.FuelCurrent, "non-protected columns keep last-write-wins: our stale 150 clobbers the bump's 101")
}
