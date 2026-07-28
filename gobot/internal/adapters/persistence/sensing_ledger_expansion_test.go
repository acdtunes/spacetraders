package persistence_test

// Integration tests (real GORM/sqlite, no mocks) for the three ledger verbs the
// expansion engine adds: reading every system row with its charting errand,
// writing that errand WITHOUT disturbing the screening columns beside it, and
// releasing a spare's placement row to a mission.
//
// The write-set isolation is the whole point of the middle one. The screening
// sweep and the expansion engine run concurrently against the same rows, and
// they stay out of each other's way only because SetSeed touches two columns and
// UpsertSystem touches the rest.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
)

// system builds a screened system row.
func systemRow(symbol, verdict string, uncharted int) persistence.SensingSystemModel {
	return persistence.SensingSystemModel{
		PlayerID:       1,
		SystemSymbol:   symbol,
		Verdict:        verdict,
		UnchartedCount: uncharted,
		DepthCredits:   4200,
	}
}

func TestSensingLedger_Systems_ReturnsEveryRowWithItsErrand(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.UpsertSystem(ctx, systemRow("X1-BB", "PENDING", 3)))
	require.NoError(t, repo.UpsertSystem(ctx, systemRow("X1-AA", "IN_SCOPE", 0)))
	require.NoError(t, repo.SetSeed(ctx, 1, "X1-BB", "PROBE-7", "CHARTING"))

	found, err := repo.Systems(ctx, 1)
	require.NoError(t, err)
	require.Len(t, found, 2)
	require.Equal(t, "X1-AA", found[0].SystemSymbol, "rows are ordered by symbol for a reproducible tick")
	require.Equal(t, "X1-BB", found[1].SystemSymbol)

	require.Equal(t, 3, found[1].UnchartedCount)
	require.NotNil(t, found[1].SeedShip)
	require.Equal(t, "PROBE-7", *found[1].SeedShip)
	require.NotNil(t, found[1].SeedState)
	require.Equal(t, "CHARTING", *found[1].SeedState)
	require.Nil(t, found[0].SeedShip, "a system with no errand carries no hull")
}

func TestSensingLedger_Systems_IsScopedToThePlayer(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.UpsertSystem(ctx, systemRow("X1-AA", "IN_SCOPE", 0)))
	other := systemRow("X1-ZZ", "IN_SCOPE", 5)
	other.PlayerID = 2
	require.NoError(t, repo.UpsertSystem(ctx, other))

	found, err := repo.Systems(ctx, 1)
	require.NoError(t, err)
	require.Len(t, found, 1)
	require.Equal(t, "X1-AA", found[0].SystemSymbol)
}

func TestSensingLedger_SetSeed_LeavesTheScreeningColumnsAlone(t *testing.T) {
	// The screening sweep owns verdict/uncharted_count/depth_credits and runs
	// concurrently with the expansion engine. A seed write that carried any of
	// them would put a stale verdict back over a fresh one.
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.UpsertSystem(ctx, systemRow("X1-BB", "IN_SCOPE", 3)))
	require.NoError(t, repo.SetSeed(ctx, 1, "X1-BB", "PROBE-7", "DISPATCHED"))

	found, err := repo.Systems(ctx, 1)
	require.NoError(t, err)
	require.Len(t, found, 1)
	require.Equal(t, "IN_SCOPE", found[0].Verdict)
	require.Equal(t, 3, found[0].UnchartedCount)
	require.Equal(t, int64(4200), found[0].DepthCredits)
	require.Equal(t, "PROBE-7", *found[0].SeedShip)
}

func TestSensingLedger_UpsertSystem_LeavesTheSeedColumnsAlone(t *testing.T) {
	// The MIRROR of the test above, and the load-bearing half. A seed's target
	// system is PENDING for the whole tour, and the screening sweep re-screens
	// PENDING systems on every tick — so if a screen could clear the errand, it
	// would do so mid-tour, every tick. The next expansion tick would then see
	// no active seed, order ANOTHER probe for the same system, and leave the
	// original hull named by nothing at all (its placement row was deleted when
	// the seed was claimed): invisible to CountOwnedProbes, and re-bought on
	// every repeat. Unbounded spend — RULINGS #4's forbidden direction.
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.UpsertSystem(ctx, systemRow("X1-BB", "PENDING", 3)))
	require.NoError(t, repo.SetSeed(ctx, 1, "X1-BB", "PROBE-7", "CHARTING"))

	// The sweep re-screens the system the seed is still working.
	rescreen := systemRow("X1-BB", "PENDING", 2)
	rescreen.DepthCredits = 99
	require.NoError(t, repo.UpsertSystem(ctx, rescreen))

	found, err := repo.Systems(ctx, 1)
	require.NoError(t, err)
	require.Len(t, found, 1)
	require.Equal(t, 2, found[0].UnchartedCount, "the screen's own columns must still update")
	require.Equal(t, int64(99), found[0].DepthCredits)

	require.NotNil(t, found[0].SeedShip, "a re-screen must never orphan the hull mid-tour")
	require.Equal(t, "PROBE-7", *found[0].SeedShip)
	require.NotNil(t, found[0].SeedState)
	require.Equal(t, "CHARTING", *found[0].SeedState)
}

func TestSensingLedger_SetSeed_ClearsTheErrandToNull(t *testing.T) {
	// An emptied errand must be NULL, not "". An empty-string hull would satisfy
	// every "is a hull on this system?" check written as a non-nil test.
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.UpsertSystem(ctx, systemRow("X1-BB", "PENDING", 3)))
	require.NoError(t, repo.SetSeed(ctx, 1, "X1-BB", "PROBE-7", "CHARTING"))
	require.NoError(t, repo.SetSeed(ctx, 1, "X1-BB", "", ""))

	found, err := repo.Systems(ctx, 1)
	require.NoError(t, err)
	require.Len(t, found, 1)
	require.Nil(t, found[0].SeedShip)
	require.Nil(t, found[0].SeedState)
}

func TestSensingLedger_SetSeed_RefusesAnUnscreenedSystem(t *testing.T) {
	// An errand on a system with no row would be a hull sent somewhere nothing
	// asked for, and an upsert here would conjure the row to justify it.
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)

	err := repo.SetSeed(context.Background(), 1, "X1-NOWHERE", "PROBE-7", "DISPATCHED")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestSensingLedger_SetSeed_IsScopedToThePlayer(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	other := systemRow("X1-BB", "PENDING", 3)
	other.PlayerID = 2
	require.NoError(t, repo.UpsertSystem(ctx, other))

	err := repo.SetSeed(ctx, 1, "X1-BB", "PROBE-7", "DISPATCHED")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound, "one player must not drive another's system row")
}

func TestSensingLedger_StampCatalogSynced_LeavesTheVerdictAndErrandAlone(t *testing.T) {
	// The stamp lands MID-TOUR, while the screening sweep may be re-screening
	// the same row and the errand is still driving the hull. A third disjoint
	// write set, for the same reason as SetSeed.
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.UpsertSystem(ctx, systemRow("X1-BB", "PENDING", 3)))
	require.NoError(t, repo.SetSeed(ctx, 1, "X1-BB", "PROBE-7", "DISPATCHED"))

	at := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, repo.StampCatalogSynced(ctx, 1, "X1-BB", at))

	found, err := repo.Systems(ctx, 1)
	require.NoError(t, err)
	require.Len(t, found, 1)
	require.NotNil(t, found[0].CatalogSyncedAt)
	require.WithinDuration(t, at, found[0].CatalogSyncedAt.UTC(), time.Second)
	require.Equal(t, "PENDING", found[0].Verdict)
	require.Equal(t, 3, found[0].UnchartedCount)
	require.Equal(t, "PROBE-7", *found[0].SeedShip, "the errand must survive the stamp")
}

func TestSensingLedger_StampCatalogSynced_RefusesAnUnknownSystem(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)

	err := repo.StampCatalogSynced(context.Background(), 1, "X1-NOWHERE", time.Now().UTC())
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestSensingLedger_UpsertSystem_CanRecordAnUnsweptCatalog(t *testing.T) {
	// NULL is the state that keeps a system a seed target, so a screen that did
	// not find the catalog known must be able to say so — and must not
	// accidentally erase a stamp a later sweep put there.
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.UpsertSystem(ctx, systemRow("X1-BB", "PENDING", 0)))
	found, err := repo.Systems(ctx, 1)
	require.NoError(t, err)
	require.Nil(t, found[0].CatalogSyncedAt, "an unstamped screen leaves the catalog unknown")

	stamped := systemRow("X1-BB", "PENDING", 0)
	at := time.Now().UTC()
	stamped.CatalogSyncedAt = &at
	require.NoError(t, repo.UpsertSystem(ctx, stamped))

	found, err = repo.Systems(ctx, 1)
	require.NoError(t, err)
	require.NotNil(t, found[0].CatalogSyncedAt)
}

func TestSensingLedger_DeleteSlot_RemovesTheRowAndTheProbeItCounted(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	spare := slot("X1-AA-Y1", "PARKED")
	spare.SlotKind = "SPARE"
	spare.AssignedShip = strptr("PROBE-7")
	require.NoError(t, repo.UpsertSpareSlot(ctx, spare))

	owned, err := repo.CountOwnedProbes(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, int64(1), owned)

	require.NoError(t, repo.DeleteSlot(ctx, 1, "X1-AA-Y1", "SPARE"))

	found, err := repo.SlotsByState(ctx, 1, "PARKED")
	require.NoError(t, err)
	require.Empty(t, found)

	// The hull is now named by its mission rather than by the ledger — an
	// accepted, bounded under-count that heals when the seed parks again.
	owned, err = repo.CountOwnedProbes(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, int64(0), owned)
}

func TestSensingLedger_DeleteSlot_IsIdempotentAndPlayerScoped(t *testing.T) {
	// The caller has already stamped the errand by the time it deletes, so it
	// cannot usefully unwind: a row that is already gone must not be an error.
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.DeleteSlot(ctx, 1, "X1-AA-GONE", "SPARE"))

	other := slot("X1-AA-M1", "PARKED")
	other.PlayerID = 2
	other.AssignedShip = strptr("PROBE-OTHER")
	require.NoError(t, repo.UpsertSpareSlot(ctx, other))

	require.NoError(t, repo.DeleteSlot(ctx, 1, "X1-AA-M1", "SPARE"))

	var survivors []persistence.SensingSlotModel
	require.NoError(t, db.Where("player_id = ?", 2).Find(&survivors).Error)
	require.Len(t, survivors, 1, "one player must never delete another's placement")
}
