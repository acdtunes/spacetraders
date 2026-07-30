package persistence_test

// Integration tests (real GORM/sqlite) for the placement worklist's ORDER and for
// the attempt stamp that drives it (sp-cwnwb).
//
// The order is the contract here, not an incidental detail of the read. The
// placement machine works a fixed budget over a queue that DOES NOT DRAIN — a slot
// whose move is refused stays in the state it was in, so it is back on the list
// next tick — and any order that is stable across ticks therefore gives the list a
// permanent head that the budget never sees past. Measured live before this
// existed: 266 BOUGHT + 52 IN_TRANSIT slots, ~40 actions' worth of ticks, zero
// dispatches, and one slot 22.5 hours old that had never been attempted once.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
)

// attemptedSlot builds a BOUGHT slot carrying a hull and an attempt stamp. A nil
// stamp is a slot the placement machine has never tried.
func attemptedSlot(waypoint string, ship string, lastAttempt *time.Time) persistence.SensingSlotModel {
	m := slot(waypoint, "BOUGHT")
	m.AssignedShip = strptr(ship)
	m.LastAttemptAt = lastAttempt
	return m
}

// TestSensingLedger_PlacementWorklist_OrdersLeastRecentlyAttemptedFirst pins the
// rotation against a fixture whose alphabetical order is the exact REVERSE of the
// wanted one.
//
// That inversion is deliberate and load-bearing. A fixture where the two orders
// agree, or even partly agree, cannot tell a working rotation from the fixed
// waypoint sort it replaces — it would pass on the buggy read.
func TestSensingLedger_PlacementWorklist_OrdersLeastRecentlyAttemptedFirst(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	base := time.Now().UTC().Add(-24 * time.Hour)
	recent := base.Add(3 * time.Hour)
	middle := base.Add(2 * time.Hour)
	oldest := base.Add(1 * time.Hour)

	// Alphabetically A,B,C,D — by attempt recency D,C,B,A.
	require.NoError(t, db.Create(&[]persistence.SensingSlotModel{
		attemptedSlot("X1-AA-A1", "PROBE-A", &recent),
		attemptedSlot("X1-AA-B1", "PROBE-B", &middle),
		attemptedSlot("X1-AA-C1", "PROBE-C", &oldest),
		attemptedSlot("X1-AA-D1", "PROBE-D", nil), // never attempted
	}).Error)

	got, err := repo.PlacementWorklist(ctx, 1, "BOUGHT", "IN_TRANSIT")
	require.NoError(t, err)
	require.Len(t, got, 4)

	var order []string
	for _, m := range got {
		order = append(order, m.WaypointSymbol)
	}
	require.Equal(t, []string{"X1-AA-D1", "X1-AA-C1", "X1-AA-B1", "X1-AA-A1"}, order,
		"worklist must be least-recently-attempted first with the never-attempted slot ahead of all of them; "+
			"the alphabetical order this fixture inverts is exactly what starved the tail")
}

// TestSensingLedger_PlacementWorklist_NeverAttemptedSlotsComeFirst isolates the
// NULL half, because it is the half that is engine-dependent: Postgres sorts NULLs
// LAST on an ascending order and SQLite sorts them FIRST, so a read written as a
// bare `ORDER BY last_attempt_at` would put the never-tried slots at the back in
// production while passing every test here.
func TestSensingLedger_PlacementWorklist_NeverAttemptedSlotsComeFirst(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	long := time.Now().UTC().Add(-22 * time.Hour)
	// The attempted slot sorts FIRST alphabetically, so neither the symbol order
	// nor a NULLS-last order could produce the wanted answer by accident.
	require.NoError(t, db.Create(&[]persistence.SensingSlotModel{
		attemptedSlot("X1-AA-A1", "PROBE-TRIED", &long),
		attemptedSlot("X1-ZZ-Z1", "PROBE-NEVER", nil),
	}).Error)

	got, err := repo.PlacementWorklist(ctx, 1, "BOUGHT")
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "X1-ZZ-Z1", got[0].WaypointSymbol,
		"a slot the machine has never tried must outrank one it tried 22 hours ago, whatever the symbols say")
}

// TestSensingLedger_PlacementWorklist_TieBreaksOnWaypointToMakeTheOrderTotal.
// Slots stamped inside one tick readily share a timestamp, and an order that is
// unspecified among ties reintroduces a stable head among them — the bug, at
// smaller scale. The tie-break is what makes the order total.
func TestSensingLedger_PlacementWorklist_TieBreaksOnWaypointToMakeTheOrderTotal(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	shared := time.Now().UTC().Add(-time.Hour)
	require.NoError(t, db.Create(&[]persistence.SensingSlotModel{
		attemptedSlot("X1-AA-C1", "PROBE-C", &shared),
		attemptedSlot("X1-AA-A1", "PROBE-A", &shared),
		attemptedSlot("X1-AA-B1", "PROBE-B", &shared),
	}).Error)

	got, err := repo.PlacementWorklist(ctx, 1, "BOUGHT")
	require.NoError(t, err)
	var order []string
	for _, m := range got {
		order = append(order, m.WaypointSymbol)
	}
	require.Equal(t, []string{"X1-AA-A1", "X1-AA-B1", "X1-AA-C1"}, order,
		"equal stamps must fall back to a deterministic symbol order")
}

// TestSensingLedger_PlacementWorklist_EmptyStateListMatchesNothing mirrors
// SlotsByState's contract: an accidentally-empty filter must not hand the caller
// the whole ledger and have the placement machine act on every row at once.
func TestSensingLedger_PlacementWorklist_EmptyStateListMatchesNothing(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	require.NoError(t, db.Create(&[]persistence.SensingSlotModel{
		attemptedSlot("X1-AA-A1", "PROBE-A", nil),
	}).Error)

	got, err := repo.PlacementWorklist(ctx, 1)
	require.NoError(t, err)
	require.Empty(t, got, "an empty state list must match nothing, never everything")
}

// TestSensingLedger_MarkPlacementAttempt_WritesOnlyTheStamp is the RULINGS #4
// guard. The probe cap counts slots by state and assigned_ship, so a fairness
// stamp that disturbed either could drop a hull out of the count and authorise
// re-buying a probe the player already owns. The counted total is asserted
// directly, not merely the two columns, because the count is what spends money.
func TestSensingLedger_MarkPlacementAttempt_WritesOnlyTheStamp(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	require.NoError(t, db.Create(&[]persistence.SensingSlotModel{
		attemptedSlot("X1-AA-A1", "PROBE-A", nil),
	}).Error)

	before, err := repo.CountOwnedProbes(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, int64(1), before)

	require.NoError(t, repo.MarkPlacementAttempt(ctx, 1, "X1-AA-A1", "MARKET"))

	var got persistence.SensingSlotModel
	require.NoError(t, db.Where("player_id = ? AND waypoint_symbol = ? AND slot_kind = ?", 1, "X1-AA-A1", "MARKET").
		First(&got).Error)
	require.NotNil(t, got.LastAttemptAt, "the stamp was not written")
	require.Equal(t, "BOUGHT", got.State, "the stamp moved the state machine")
	require.NotNil(t, got.AssignedShip)
	require.Equal(t, "PROBE-A", *got.AssignedShip, "the stamp disturbed the assigned hull")

	after, err := repo.CountOwnedProbes(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, before, after,
		"stamping an attempt changed the probe cap's count; a fairness hint must never be able to authorise a purchase")
}

// TestSensingLedger_MarkPlacementAttempt_MissingSlotIsNotAnError: the slot may
// have been transitioned or reaped between the worklist read and the stamp. The
// stamp is a hint about ordering, not a claim that the row still exists, and
// failing the tick over it would abandon placements that genuinely advanced.
func TestSensingLedger_MarkPlacementAttempt_MissingSlotIsNotAnError(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)

	require.NoError(t, repo.MarkPlacementAttempt(context.Background(), 1, "X1-GONE-M1", "MARKET"))
}

// TestSensingLedger_MarkPlacementAttempt_IsPlayerScoped keeps one player's
// bookkeeping out of another's, matching every other write in this ledger.
func TestSensingLedger_MarkPlacementAttempt_IsPlayerScoped(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	other := attemptedSlot("X1-AA-A1", "PROBE-OTHER", nil)
	other.PlayerID = 2
	require.NoError(t, db.Create(&[]persistence.SensingSlotModel{
		attemptedSlot("X1-AA-A1", "PROBE-A", nil),
		other,
	}).Error)

	require.NoError(t, repo.MarkPlacementAttempt(ctx, 1, "X1-AA-A1", "MARKET"))

	var got persistence.SensingSlotModel
	require.NoError(t, db.Where("player_id = ? AND waypoint_symbol = ?", 2, "X1-AA-A1").First(&got).Error)
	require.Nil(t, got.LastAttemptAt, "stamping player 1's slot also stamped player 2's")
}

// --- the one clause behaviour cannot pin under SQLite -------------------------

// capturingGormLogger records the SQL GORM actually emits.
type capturingGormLogger struct {
	gormlogger.Interface
	mu         sync.Mutex
	statements []string
}

func (l *capturingGormLogger) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.statements = append(l.statements, sql)
}

// TestSensingLedger_PlacementWorklist_OrdersNullsFirstExplicitlyForPostgres asserts
// the SQL TEXT, which is not this file's habit and needs its reason stated.
//
// The NULL-first clause is the one part of this order that no behavioural test can
// falsify here, because the test database AGREES WITH IT BY ACCIDENT: SQLite sorts
// NULLs first on an ascending order, so a bare `ORDER BY last_attempt_at ASC`
// produces the wanted answer under every fixture in this file. Postgres does the
// OPPOSITE — NULLs last on ASC — so that same bare order would send every
// never-attempted slot to the BACK of the worklist in production, which is exactly
// the slot the rotation exists to reach first and exactly the shape the live defect
// took (a hull 22.5 hours old that had never been tried once).
//
// A mutation dropping the clause therefore survives every behavioural test in this
// file while breaking production. Pinning the emitted SQL is what kills it. Delete
// this test only when the placement worklist is covered against a real Postgres.
func TestSensingLedger_PlacementWorklist_OrdersNullsFirstExplicitlyForPostgres(t *testing.T) {
	db := newSensingLedgerDB(t)
	capture := &capturingGormLogger{Interface: db.Logger}
	repo := persistence.NewSensingLedgerRepository(db.Session(&gorm.Session{Logger: capture}))

	_, err := repo.PlacementWorklist(context.Background(), 1, "BOUGHT")
	require.NoError(t, err)

	var worklistSQL string
	for _, sql := range capture.statements {
		if strings.Contains(sql, "sensing_slots") && strings.Contains(sql, "ORDER BY") {
			worklistSQL = sql
			break
		}
	}
	require.NotEmpty(t, worklistSQL, "captured no ordered sensing_slots query: statements=%v", capture.statements)

	require.Contains(t, worklistSQL, "(last_attempt_at IS NULL) DESC",
		"the worklist order must place never-attempted slots first EXPLICITLY: Postgres sorts NULLs last on ASC, "+
			"so relying on the engine default would put every never-attempted slot at the back in production "+
			"while every behavioural test here still passed")
}
