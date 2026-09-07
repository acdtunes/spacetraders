package persistence_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
)

// TWO RESERVES AT ONE WAYPOINT COEXIST — the test that catches a waypoint key.
func TestSensingLedger_TwoReservesAtOneWaypointCoexist(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.UpsertSpareHull(ctx, 1, "ORION-1", "X1-AA-YARD", "X1-AA"))
	require.NoError(t, repo.UpsertSpareHull(ctx, 1, "ORION-2", "X1-AA-YARD", "X1-AA"))

	pool, err := repo.SpareHulls(ctx, 1)
	require.NoError(t, err)
	require.Len(t, pool, 2, "one waypoint, two hulls, two rows — a hull dropped here is a probe re-bought")
	require.Equal(t, "ORION-1", pool[0].ShipSymbol, "hull-ordered, so a claim is reproducible tick to tick")
	require.Equal(t, "ORION-2", pool[1].ShipSymbol)
	require.Equal(t, "X1-AA-YARD", pool[0].WaypointSymbol)
	require.Equal(t, "X1-AA", pool[0].SystemSymbol)
}

func TestSensingLedger_ReRecordingAReserveMovesItRatherThanDuplicatingIt(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.UpsertSpareHull(ctx, 1, "ORION-1", "X1-AA-YARD", "X1-AA"))
	require.NoError(t, repo.UpsertSpareHull(ctx, 1, "ORION-1", "X1-BB-YARD", "X1-BB"))

	pool, err := repo.SpareHulls(ctx, 1)
	require.NoError(t, err)
	require.Len(t, pool, 1, "one hull, one reserve row")
	require.Equal(t, "X1-BB-YARD", pool[0].WaypointSymbol, "and it carries the hull's current waypoint")
}

func TestSensingLedger_DeleteSpareHull_LeavesTheCoLocatedSiblingsAlone(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.UpsertSpareHull(ctx, 1, "ORION-1", "X1-AA-YARD", "X1-AA"))
	require.NoError(t, repo.UpsertSpareHull(ctx, 1, "ORION-2", "X1-AA-YARD", "X1-AA"))

	require.NoError(t, repo.DeleteSpareHull(ctx, 1, "ORION-1"))

	pool, err := repo.SpareHulls(ctx, 1)
	require.NoError(t, err)
	require.Len(t, pool, 1)
	require.Equal(t, "ORION-2", pool[0].ShipSymbol)

	require.NoError(t, repo.DeleteSpareHull(ctx, 1, "ORION-1"))
}

func TestSensingLedger_CountOwnedProbes_CountsReserveHulls(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	parked := slot("X1-AA-M1", "PARKED")
	parked.AssignedShip = strptr("ORION-PARKED")
	require.NoError(t, repo.UpsertSpareSlot(ctx, parked))

	before, err := repo.CountOwnedProbes(ctx, 1)
	require.NoError(t, err)
	require.EqualValues(t, 1, before)

	require.NoError(t, repo.UpsertSpareHull(ctx, 1, "ORION-1", "X1-AA-YARD", "X1-AA"))
	require.NoError(t, repo.UpsertSpareHull(ctx, 1, "ORION-2", "X1-AA-YARD", "X1-AA"))

	after, err := repo.CountOwnedProbes(ctx, 1)
	require.NoError(t, err)
	require.EqualValues(t, 3, after, "both reserves count — an under-count here re-buys probes we own")
}

// Named by both a placement and a reserve is still ONE probe, never two.
func TestSensingLedger_CountOwnedProbes_DedupesAHullNamedByBothAPlacementAndAReserve(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	parked := slot("X1-AA-M1", "PARKED")
	parked.AssignedShip = strptr("ORION-1")
	require.NoError(t, repo.UpsertSpareSlot(ctx, parked))
	require.NoError(t, repo.UpsertSpareHull(ctx, 1, "ORION-1", "X1-AA-M1", "X1-AA"))

	count, err := repo.CountOwnedProbes(ctx, 1)
	require.NoError(t, err)
	require.EqualValues(t, 1, count, "one hull is one probe, however many rows name it")
}

func TestSensingLedger_ReservesAreScopedToTheirPlayer(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.UpsertSpareHull(ctx, 1, "ORION-1", "X1-AA-YARD", "X1-AA"))
	require.NoError(t, repo.UpsertSpareHull(ctx, 2, "OTHER-1", "X1-AA-YARD", "X1-AA"))

	pool, err := repo.SpareHulls(ctx, 1)
	require.NoError(t, err)
	require.Len(t, pool, 1)
	require.Equal(t, "ORION-1", pool[0].ShipSymbol)

	count, err := repo.CountOwnedProbes(ctx, 1)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
}
