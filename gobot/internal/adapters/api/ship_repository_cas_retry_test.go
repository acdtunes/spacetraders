package api

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// SaveWithRetry resolves the concurrent-writer conflict class that the prior
// version-conflict tripwire only *detected*. These tests reuse newShipWriteTestRepo /
// seedShip (ship_repository_version_test.go) and drive real conflicts against
// the same sqlite test DB, mirroring TestSave_DetectsVersionConflictAndFallsBack.

// bumpRowVersion simulates a concurrent writer committing between SaveWithRetry's
// find and its CAS save: it advances the row's version (so the pending CAS
// conflicts) and nudges fuel_current by +1 (so a clobber-vs-preserve outcome is
// observable in the row).
func bumpRowVersion(t *testing.T, db *gorm.DB, symbol string) {
	t.Helper()
	require.NoError(t, db.Model(&persistence.ShipModel{}).
		Where("ship_symbol = ?", symbol).
		Updates(map[string]interface{}{
			"version":      gorm.Expr("version + 1"),
			"fuel_current": gorm.Expr("fuel_current + 1"),
		}).Error)
}

// GREEN (the fix): a concurrent writer commits +400 fuel between our find and
// our save. With re-apply retry, SaveWithRetry re-loads the FRESH row (seeing
// the +400), re-applies our +10 on top, and the CAS save lands — so BOTH
// writers' mutations survive (100 + 400 + 10 = 510). Under the old last-write-
// wins (see the Disabled test) our stale +10 would have clobbered the +400.
func TestSaveWithRetry_ReappliesOnConflict_BothMutationsSurvive(t *testing.T) {
	repo, db, pid := newShipWriteTestRepo(t)
	seedShip(t, db, pid.Value(), "TORWIND-20", "IN_ORBIT", 100)

	// Writer B, loaded at the same version we will load at.
	b, err := repo.FindBySymbol(context.Background(), "TORWIND-20", pid)
	require.NoError(t, err)

	before := shipVersionConflicts.Load()
	injected := false

	final, saved, err := repo.SaveWithRetry(context.Background(), "TORWIND-20", pid,
		func(sh *navigation.Ship) (bool, error) {
			if !injected {
				injected = true
				// B commits a DIFFERENT mutation, advancing the row past our
				// loaded version — the conflict our CAS save will hit.
				require.NoError(t, b.Refuel(400))
				require.NoError(t, repo.Save(context.Background(), b))
			}
			return true, sh.Refuel(10)
		})

	require.NoError(t, err)
	require.True(t, saved, "a real mutation must persist")
	require.Equal(t, before+1, shipVersionConflicts.Load(), "the one conflict is still counted (telemetry kept)")
	require.Equal(t, 3, final.PersistedVersion(), "committed on retry at the fresh version (seed 1 → B 2 → us 3)")

	var row persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-20").First(&row).Error)
	require.Equal(t, 510, row.FuelCurrent, "re-applied on fresh state: 100 + B's 400 + our 10 — both writers survived, no lost write")
}

// No-regression / "today's behavior": with the escape hatch engaged
// (CASRetryDisabled), a conflict falls straight through to last-write-wins.
// Our stale +10 clobbers B's +400 (100 + 10 = 110, NOT 510).
func TestSaveWithRetry_Disabled_FallsBackToLastWriteWins(t *testing.T) {
	repo, db, pid := newShipWriteTestRepo(t)
	repo.SetCASRetryPolicy(0, true) // disabled → legacy last-write-wins
	seedShip(t, db, pid.Value(), "TORWIND-21", "IN_ORBIT", 100)

	b, err := repo.FindBySymbol(context.Background(), "TORWIND-21", pid)
	require.NoError(t, err)

	before := shipVersionConflicts.Load()
	injected := false

	_, saved, err := repo.SaveWithRetry(context.Background(), "TORWIND-21", pid,
		func(sh *navigation.Ship) (bool, error) {
			if !injected {
				injected = true
				require.NoError(t, b.Refuel(400))
				require.NoError(t, repo.Save(context.Background(), b))
			}
			return true, sh.Refuel(10)
		})

	require.NoError(t, err, "conflict is never surfaced as an error")
	require.True(t, saved)
	require.Equal(t, before+1, shipVersionConflicts.Load(), "the conflict is counted exactly once, like today")

	var row persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-21").First(&row).Error)
	require.Equal(t, 110, row.FuelCurrent, "disabled → last-write-wins clobbers B's mutation (sp-60ff behavior preserved)")
}

// No-regression on exhaustion: if every attempt conflicts (a hot row), retry
// exhausts and falls back to last-write-wins — never a deadlock, error, or hang.
// max=1 → 2 CAS attempts (0 and 1), each counted, then the fallback.
func TestSaveWithRetry_ExhaustsThenFallsBackToLastWriteWins(t *testing.T) {
	repo, db, pid := newShipWriteTestRepo(t)
	repo.SetCASRetryPolicy(1, false) // 1 retry → attempts 0 and 1, then fallback
	seedShip(t, db, pid.Value(), "TORWIND-22", "IN_ORBIT", 100)

	before := shipVersionConflicts.Load()

	_, saved, err := repo.SaveWithRetry(context.Background(), "TORWIND-22", pid,
		func(sh *navigation.Ship) (bool, error) {
			// Advance the row on EVERY attempt so the CAS never lands.
			bumpRowVersion(t, db, "TORWIND-22")
			return true, sh.Refuel(10)
		})

	require.NoError(t, err, "exhaustion falls back, it does not error")
	require.True(t, saved, "the fallback still persists our mutation")
	require.Equal(t, before+2, shipVersionConflicts.Load(), "both CAS attempts (0 and 1) are counted")

	var row persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-22").First(&row).Error)
	// attempt0 finds fuel 100 → bump→101 → +10 stale; retry finds 101 → bump→102
	// → +10 = 111 → last-write-wins writes 111 (clobbering the bump's 102).
	require.Equal(t, 111, row.FuelCurrent, "the last-write-wins fallback wrote our final re-applied state")
}

// Happy path (live-by-default): no conflict → the CAS save commits on the first
// attempt, advancing the entity's version, with zero conflicts counted.
func TestSaveWithRetry_NoConflict_CommitsFirstAttempt(t *testing.T) {
	repo, db, pid := newShipWriteTestRepo(t)
	seedShip(t, db, pid.Value(), "TORWIND-23", "IN_ORBIT", 100)

	before := shipVersionConflicts.Load()

	final, saved, err := repo.SaveWithRetry(context.Background(), "TORWIND-23", pid,
		func(sh *navigation.Ship) (bool, error) { return true, sh.Refuel(10) })

	require.NoError(t, err)
	require.True(t, saved)
	require.Equal(t, before, shipVersionConflicts.Load(), "no conflict on an uncontended row")
	require.Equal(t, 2, final.PersistedVersion(), "a committed CAS save advances the version")

	var row persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-23").First(&row).Error)
	require.Equal(t, 110, row.FuelCurrent)
}

// A mutation that reports changed=false (the ship is already in the desired
// state — a concurrent writer got there first) skips the write entirely: no row
// change, no version bump, no conflict counted.
func TestSaveWithRetry_NoChange_SkipsWrite(t *testing.T) {
	repo, db, pid := newShipWriteTestRepo(t)
	seedShip(t, db, pid.Value(), "TORWIND-24", "IN_ORBIT", 100)

	before := shipVersionConflicts.Load()

	final, saved, err := repo.SaveWithRetry(context.Background(), "TORWIND-24", pid,
		func(sh *navigation.Ship) (bool, error) { return false, nil })

	require.NoError(t, err)
	require.False(t, saved, "no write when the mutation reports no change")
	require.Equal(t, before, shipVersionConflicts.Load())
	require.Equal(t, 1, final.PersistedVersion(), "version untouched")

	var row persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-24").First(&row).Error)
	require.Equal(t, 100, row.FuelCurrent, "row untouched")
	require.Equal(t, 1, row.Version, "no spurious version bump")
}

// RULINGS #5: retry is LIVE BY DEFAULT (unset → built-in default), tunable, and
// the disable flag is the escape hatch that wins over any retry count.
func TestResolvedCASRetries_LiveByDefaultWithDisableEscape(t *testing.T) {
	repo, _, _ := newShipWriteTestRepo(t)

	require.Equal(t, defaultMaxCASRetries, repo.resolvedCASRetries(), "unset → built-in default (live)")

	repo.SetCASRetryPolicy(0, false)
	require.Equal(t, defaultMaxCASRetries, repo.resolvedCASRetries(), "explicit 0 → default (live)")

	repo.SetCASRetryPolicy(5, false)
	require.Equal(t, 5, repo.resolvedCASRetries(), "configured value is honored")

	repo.SetCASRetryPolicy(0, true)
	require.Equal(t, 0, repo.resolvedCASRetries(), "disabled → 0 retries (today's last-write-wins)")

	repo.SetCASRetryPolicy(5, true)
	require.Equal(t, 0, repo.resolvedCASRetries(), "disable escape wins over any retry count")
}
