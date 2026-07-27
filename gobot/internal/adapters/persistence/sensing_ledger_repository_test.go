package persistence_test

// Integration tests (real GORM/sqlite, no mocks) for the parked-probe sensing
// ledger: the durable placement spine of the parked-probe model. The interesting
// behaviour is not round-tripping rows — it is the SLOT STATE MACHINE
// (WANTED→QUEUED→BOUGHT→IN_TRANSIT→PARKED) under concurrent writers, and the
// probe_cap read that decides whether another probe may be bought. So the tests
// below concentrate on the edges: a lost transition race, the exact ownership
// predicate across EVERY state, cross-player isolation, and era stamping.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// newSensingLedgerDB returns a test DB with one OPEN era, so era stamping has
// something to stamp (openEraID returns nil without it, which is the valid
// pre-close transition window but not what most of these tests exercise).
func newSensingLedgerDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)
	return db
}

func strptr(s string) *string { return &s }

// slot builds a minimally-populated slot row for the given waypoint and state.
func slot(waypoint, state string) persistence.SensingSlotModel {
	return persistence.SensingSlotModel{
		PlayerID:       1,
		WaypointSymbol: waypoint,
		SystemSymbol:   "X1-AA",
		SlotKind:       "MARKET",
		State:          state,
		WhitelistGoods: `["FUEL"]`,
	}
}

// Round trip: an upserted WANTED slot is found by SlotsByState, a re-upsert
// updates in place (composite PK — no duplicate row), and the era is stamped.
//
// The re-upsert asserts the SCREEN's own columns, because those are the ones a
// re-declaration owns (sp-wgjb7). It used to assert slot_kind and purchase_yard
// instead — columns that only moved because the conflict set named every column
// in the table, which is the lost-update bug this contract removed. The narrow
// half of that contract is pinned in sensing_ledger_ownership_test.go.
func TestSensingLedger_UpsertSlotMetadata_RoundTripsAndUpsertsInPlace(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.UpsertSlotMetadata(ctx, slot("X1-AA-M1", "WANTED")))

	found, err := repo.SlotsByState(ctx, 1, "WANTED")
	require.NoError(t, err)
	require.Len(t, found, 1)
	require.Equal(t, "X1-AA-M1", found[0].WaypointSymbol)
	require.Equal(t, "MARKET", found[0].SlotKind)
	require.Equal(t, `["FUEL"]`, found[0].WhitelistGoods)
	require.NotNil(t, found[0].EraID, "upsert must stamp the open era")
	require.False(t, found[0].UpdatedAt.IsZero(), "upsert must stamp updated_at")

	// Re-upsert the same (player, waypoint) with re-measured goods and depth:
	// one row, updated.
	second := slot("X1-AA-M1", "WANTED")
	second.WhitelistGoods = `["FUEL","CLOTHING"]`
	second.DepthCredits = 4200
	require.NoError(t, repo.UpsertSlotMetadata(ctx, second))

	found, err = repo.SlotsByState(ctx, 1, "WANTED")
	require.NoError(t, err)
	require.Len(t, found, 1, "re-upsert on the composite PK must not duplicate the row")
	require.Equal(t, `["FUEL","CLOTHING"]`, found[0].WhitelistGoods)
	require.Equal(t, int64(4200), found[0].DepthCredits)
}

// SlotsByState is variadic: several states at once, and an EMPTY state list
// matches nothing (never "everything" — an accidental empty filter must not
// hand a caller the whole ledger).
func TestSensingLedger_SlotsByState_MultipleStatesAndEmptyMatchesNothing(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.UpsertSlotMetadata(ctx, slot("X1-AA-M1", "WANTED")))
	require.NoError(t, repo.UpsertSlotMetadata(ctx, slot("X1-AA-M2", "QUEUED")))
	require.NoError(t, repo.UpsertSlotMetadata(ctx, slot("X1-AA-M3", "PARKED")))

	found, err := repo.SlotsByState(ctx, 1, "WANTED", "QUEUED")
	require.NoError(t, err)
	require.Len(t, found, 2)
	require.Equal(t, "X1-AA-M1", found[0].WaypointSymbol, "reads must be deterministically ordered")
	require.Equal(t, "X1-AA-M2", found[1].WaypointSymbol)

	none, err := repo.SlotsByState(ctx, 1)
	require.NoError(t, err)
	require.Empty(t, none, "an empty state filter must match nothing, not everything")
}

// SlotsBySystem returns every slot in one system regardless of state, and never
// another system's slots.
func TestSensingLedger_SlotsBySystem_ScopesToTheSystem(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.UpsertSlotMetadata(ctx, slot("X1-AA-M1", "WANTED")))
	require.NoError(t, repo.UpsertSlotMetadata(ctx, slot("X1-AA-M2", "PARKED")))
	other := slot("X1-BB-M1", "WANTED")
	other.SystemSymbol = "X1-BB"
	require.NoError(t, repo.UpsertSlotMetadata(ctx, other))

	found, err := repo.SlotsBySystem(ctx, 1, "X1-AA")
	require.NoError(t, err)
	require.Len(t, found, 2)
	for _, s := range found {
		require.Equal(t, "X1-AA", s.SystemSymbol)
	}
}

// THE STATE-MACHINE EDGE. Two writers both read a WANTED slot and both try to
// claim it. The first transition wins; the second — carrying the now-stale
// fromState — must LOSE with ErrSlotStateConflict rather than silently
// overwriting the winner's work (which would double-buy a probe).
func TestSensingLedger_TransitionSlot_SecondStaleTransitionConflicts(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.UpsertSlotMetadata(ctx, slot("X1-AA-M1", "WANTED")))

	err := repo.TransitionSlot(ctx, 1, "X1-AA-M1", "WANTED", "QUEUED", func(m *persistence.SensingSlotModel) {
		m.PurchaseYard = strptr("X1-AA-Y1")
	})
	require.NoError(t, err)

	// The mutation landed ATOMICALLY with the state flip.
	queued, err := repo.SlotsByState(ctx, 1, "QUEUED")
	require.NoError(t, err)
	require.Len(t, queued, 1)
	require.Equal(t, "QUEUED", queued[0].State)
	require.NotNil(t, queued[0].PurchaseYard)
	require.Equal(t, "X1-AA-Y1", *queued[0].PurchaseYard)

	// Second writer, stale fromState.
	err = repo.TransitionSlot(ctx, 1, "X1-AA-M1", "WANTED", "QUEUED", func(m *persistence.SensingSlotModel) {
		m.PurchaseYard = strptr("X1-AA-Y2")
	})
	require.ErrorIs(t, err, persistence.ErrSlotStateConflict)

	// The loser must not have overwritten the winner's yard.
	queued, err = repo.SlotsByState(ctx, 1, "QUEUED")
	require.NoError(t, err)
	require.Len(t, queued, 1)
	require.Equal(t, "X1-AA-Y1", *queued[0].PurchaseYard, "the losing transition must not mutate the row")
}

// A transition against a waypoint with no slot row is the same conflict, not a
// silent success and not an upsert.
func TestSensingLedger_TransitionSlot_MissingRowConflicts(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	err := repo.TransitionSlot(ctx, 1, "X1-AA-GHOST", "WANTED", "QUEUED", nil)
	require.ErrorIs(t, err, persistence.ErrSlotStateConflict)

	var count int64
	require.NoError(t, db.Model(&persistence.SensingSlotModel{}).Count(&count).Error)
	require.Zero(t, count, "a conflicting transition must never create a row")
}

// A transition must not leak across players: player 2 cannot flip player 1's slot.
func TestSensingLedger_TransitionSlot_IsolatedPerPlayer(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.UpsertSlotMetadata(ctx, slot("X1-AA-M1", "WANTED")))

	err := repo.TransitionSlot(ctx, 2, "X1-AA-M1", "WANTED", "QUEUED", nil)
	require.ErrorIs(t, err, persistence.ErrSlotStateConflict)

	still, err := repo.SlotsByState(ctx, 1, "WANTED")
	require.NoError(t, err)
	require.Len(t, still, 1, "another player's transition must not touch this player's slot")
}

// THE PROBE_CAP READ. Ownership means "a hull exists for this slot": states
// BOUGHT / IN_TRANSIT / PARKED **with** an assigned ship. WANTED and QUEUED are
// intents with no hull yet; a BOUGHT row without a ship symbol is not yet a
// hull either. This predicate gates probe spend, so every state is pinned.
func TestSensingLedger_CountOwnedProbes_CountsOnlyHullsAcrossEveryState(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	withShip := func(waypoint, state, ship string) persistence.SensingSlotModel {
		s := slot(waypoint, state)
		s.AssignedShip = strptr(ship)
		return s
	}

	// NOT owned: no hull yet.
	require.NoError(t, repo.UpsertSlotMetadata(ctx, slot("X1-AA-W1", "WANTED")))
	require.NoError(t, repo.UpsertSlotMetadata(ctx, slot("X1-AA-Q1", "QUEUED")))
	require.NoError(t, repo.UpsertSpareSlot(ctx, withShip("X1-AA-W2", "WANTED", "ORION-90")))
	require.NoError(t, repo.UpsertSpareSlot(ctx, withShip("X1-AA-Q2", "QUEUED", "ORION-91")))
	require.NoError(t, repo.UpsertSlotMetadata(ctx, slot("X1-AA-B0", "BOUGHT"))) // BOUGHT but no ship symbol

	// Owned: a hull exists.
	require.NoError(t, repo.UpsertSpareSlot(ctx, withShip("X1-AA-B1", "BOUGHT", "ORION-1")))
	require.NoError(t, repo.UpsertSpareSlot(ctx, withShip("X1-AA-T1", "IN_TRANSIT", "ORION-2")))
	require.NoError(t, repo.UpsertSpareSlot(ctx, withShip("X1-AA-P1", "PARKED", "ORION-3")))

	// A SPARE-kind parked probe is still a hull we paid for — it counts.
	spare := withShip("X1-AA-P2", "PARKED", "ORION-4")
	spare.SlotKind = "SPARE"
	require.NoError(t, repo.UpsertSpareSlot(ctx, spare))

	// Another player's hulls never count toward this player's cap.
	otherPlayer := withShip("X1-AA-P3", "PARKED", "RIGEL-1")
	otherPlayer.PlayerID = 2
	require.NoError(t, repo.UpsertSpareSlot(ctx, otherPlayer))

	count, err := repo.CountOwnedProbes(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, int64(4), count,
		"owned = BOUGHT/IN_TRANSIT/PARKED with an assigned ship (incl. SPARE), for THIS player only")

	otherCount, err := repo.CountOwnedProbes(ctx, 2)
	require.NoError(t, err)
	require.Equal(t, int64(1), otherCount)
}

// MarkScanned refreshes ONLY the scan stamp and the spread EWMA — it must not
// disturb the state machine or the slot's assignment.
func TestSensingLedger_MarkScanned_UpdatesOnlyScanFields(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	parked := slot("X1-AA-M1", "PARKED")
	parked.AssignedShip = strptr("ORION-1")
	require.NoError(t, repo.UpsertSpareSlot(ctx, parked))

	at := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	require.NoError(t, repo.MarkScanned(ctx, 1, "X1-AA-M1", at, 42.5))

	found, err := repo.SlotsByState(ctx, 1, "PARKED")
	require.NoError(t, err)
	require.Len(t, found, 1)
	require.NotNil(t, found[0].LastScanAt)
	require.WithinDuration(t, at, *found[0].LastScanAt, time.Second)
	require.InDelta(t, 42.5, found[0].SpreadEWMA, 0.0001)
	require.Equal(t, "PARKED", found[0].State, "MarkScanned must not touch the state machine")
	require.Equal(t, "ORION-1", *found[0].AssignedShip, "MarkScanned must not touch the assignment")
}

// MarkScanned on a slot that does not exist is an error, never an upsert: a
// phantom row would later be read as a real placement.
func TestSensingLedger_MarkScanned_MissingRowErrsWithoutUpsert(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	err := repo.MarkScanned(ctx, 1, "X1-AA-GHOST", time.Now().UTC(), 1.0)
	require.Error(t, err)
	require.True(t, errors.Is(err, gorm.ErrRecordNotFound), "want a wrapped gorm.ErrRecordNotFound, got %v", err)

	var count int64
	require.NoError(t, db.Model(&persistence.SensingSlotModel{}).Count(&count).Error)
	require.Zero(t, count, "MarkScanned must never create a row")
}

// System screening: upsert round-trips, re-upsert updates in place, reads filter
// by verdict and by player, and the open era is stamped.
func TestSensingLedger_UpsertSystem_RoundTripsAndFiltersByVerdict(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	screened := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	require.NoError(t, repo.UpsertSystem(ctx, persistence.SensingSystemModel{
		PlayerID: 1, SystemSymbol: "X1-AA", Verdict: "PENDING", UnchartedCount: 3,
	}))
	require.NoError(t, repo.UpsertSystem(ctx, persistence.SensingSystemModel{
		PlayerID: 1, SystemSymbol: "X1-BB", Verdict: "IN_SCOPE", ScreenedAt: &screened, DepthCredits: 5000,
	}))
	require.NoError(t, repo.UpsertSystem(ctx, persistence.SensingSystemModel{
		PlayerID: 2, SystemSymbol: "X1-CC", Verdict: "IN_SCOPE",
	}))

	inScope, err := repo.SystemsByVerdict(ctx, 1, "IN_SCOPE")
	require.NoError(t, err)
	require.Len(t, inScope, 1, "verdict and player must both scope the read")
	require.Equal(t, "X1-BB", inScope[0].SystemSymbol)
	require.Equal(t, int64(5000), inScope[0].DepthCredits)
	require.NotNil(t, inScope[0].ScreenedAt)
	require.NotNil(t, inScope[0].EraID, "upsert must stamp the open era")

	// Re-screen X1-AA into scope: one row, new verdict.
	require.NoError(t, repo.UpsertSystem(ctx, persistence.SensingSystemModel{
		PlayerID: 1, SystemSymbol: "X1-AA", Verdict: "IN_SCOPE", ScreenedAt: &screened,
		SeedShip: strptr("ORION-7"), SeedState: strptr("DISPATCHED"),
	}))

	inScope, err = repo.SystemsByVerdict(ctx, 1, "IN_SCOPE")
	require.NoError(t, err)
	require.Len(t, inScope, 2, "re-screen must update in place, not duplicate")

	pending, err := repo.SystemsByVerdict(ctx, 1, "PENDING")
	require.NoError(t, err)
	require.Empty(t, pending, "the re-screened system must have left PENDING")
}

// Era scoping on the planning reads: a DEAD era's rows must never resurface as
// live work (a universe reset would otherwise re-dispatch probes to waypoints
// that no longer exist). The ownership COUNT is deliberately NOT era-scoped —
// see the repository comment — so it is asserted here to keep counting.
func TestSensingLedger_PlanningReadsAreEraScoped_CountIsNot(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	owned := slot("X1-AA-P1", "PARKED")
	owned.AssignedShip = strptr("ORION-1")
	require.NoError(t, repo.UpsertSpareSlot(ctx, owned))
	require.NoError(t, repo.UpsertSystem(ctx, persistence.SensingSystemModel{
		PlayerID: 1, SystemSymbol: "X1-AA", Verdict: "IN_SCOPE",
	}))

	// Close the era and open a new one: last era's rows are now dead-era.
	closed := time.Now().UTC()
	require.NoError(t, db.Model(&persistence.EraModel{}).Where("era_id = ?", 1).
		Update("closed_at", closed).Error)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "rigel", AgentSymbol: "RIGEL", PlayerID: 1}).Error)

	parked, err := repo.SlotsByState(ctx, 1, "PARKED")
	require.NoError(t, err)
	require.Empty(t, parked, "a dead era's slots must not resurface as live work")

	systems, err := repo.SystemsByVerdict(ctx, 1, "IN_SCOPE")
	require.NoError(t, err)
	require.Empty(t, systems, "a dead era's screened systems must not resurface")

	bySystem, err := repo.SlotsBySystem(ctx, 1, "X1-AA")
	require.NoError(t, err)
	require.Empty(t, bySystem, "a dead era's slots must not resurface by system either")

	count, err := repo.CountOwnedProbes(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, int64(1), count,
		"the probe_cap read must stay era-AGNOSTIC: a hull we paid for still exists after an era close, "+
			"and under-counting it would authorise buying another")
}
