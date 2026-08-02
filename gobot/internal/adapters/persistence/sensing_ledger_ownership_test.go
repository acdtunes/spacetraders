package persistence_test

// COLUMN OWNERSHIP on sensing_slots.
//
// Three writers touch this table, and until this contract existed they overlapped:
// every one of them wrote nearly the whole row, so whichever committed last
// reverted the others' columns. These tests pin who owns what, so that
// re-widening any write set fails by name rather than by a slot quietly reading
// staler than the truth.
//
//	MarkScanned        last_scan_at, spread_ewma          (+ updated_at)
//	TransitionSlot     state, assigned_ship, purchase_yard (+ updated_at)
//	UpsertSlotMetadata whitelist_goods, depth_credits, system_symbol, era_id
//	UpsertSpareSlot    state, assigned_ship, slot_kind, system_symbol, era_id
//
// WHY THE SCAN COLUMNS ARE THE LOAD-BEARING HALF: the scan pacer runs
// concurrently with the reconcile, and MarkScanned is a column-scoped UPDATE that
// can commit at any moment — including between a TransitionSlot's load and its
// write. Nothing but MarkScanned may name those two columns, or that scan is
// reverted.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
)

// scanEpoch is the stamp a scan wrote; staleEpoch is what the row held before it.
var (
	scanEpoch  = time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	staleEpoch = time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
)

// THE LOST-UPDATE WINDOW, reproduced exactly.
//
// A real interleave is: TransitionSlot loads the row, MarkScanned commits a fresh
// scan, TransitionSlot writes back the copy it loaded — reverting the scan. The
// state at the moment of that write is DB=fresh, in-transaction copy=stale, and
// that is precisely what this test constructs: the row is seeded with the scan
// already committed, and the mutate callback (which runs in the window, between
// the load and the write) sets the copy's scan columns back to their pre-scan
// values.
//
// The interleave cannot be driven with a real concurrent MarkScanned here: the
// sqlite test harness pins the pool to ONE physical connection, so any write
// issued from inside the transaction blocks forever waiting for the connection
// the transaction is holding. This construction needs no second connection and is
// deterministic, which is better for a regression pin anyway.
//
// The transition itself is PARKED→WANTED on a MARKET slot — the exact edge the
// filed slot reaper will drive, and the one the old rotation invariant
// (SPARE excluded from scanning) was the only thing preventing.
func TestSensingLedger_TransitionSlot_LeavesTheScanColumnsAlone(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	parked := slot("X1-AA-M1", "PARKED")
	parked.SlotKind = "MARKET"
	parked.AssignedShip = strptr("ORION-1")
	require.NoError(t, repo.UpsertSpareSlot(ctx, parked))

	// The scan the pacer committed while the transition was in flight.
	require.NoError(t, repo.MarkScanned(ctx, 1, "X1-AA-M1", "MARKET", scanEpoch, 42.5))

	cleared := ""
	require.NoError(t, repo.TransitionSlot(ctx, 1, "X1-AA-M1", "MARKET", "PARKED", "WANTED",
		func(m *persistence.SensingSlotModel) {
			// The legal write: this transition releases the hull.
			m.AssignedShip = &cleared

			// The stale copy. A transition that loaded the row before the scan
			// committed carries exactly these values, and must not write them.
			m.LastScanAt = &staleEpoch
			m.SpreadEWMA = 0
		}))

	found, err := repo.SlotsByState(ctx, 1, "WANTED")
	require.NoError(t, err)
	require.Len(t, found, 1)

	require.Equal(t, "WANTED", found[0].State, "the transition itself must still land")
	require.Equal(t, "", derefOr(found[0].AssignedShip), "and so must its own owned column")

	require.NotNil(t, found[0].LastScanAt)
	require.WithinDuration(t, scanEpoch, *found[0].LastScanAt, time.Second,
		"a scan committed inside the transition's window must survive it")
	require.InDelta(t, 42.5, found[0].SpreadEWMA, 0.0001,
		"the smoothed spread is MarkScanned's column and no transition may revert it")
}

// The whole ownership set at once: every column a transition does NOT own is
// dirtied on the loaded copy, and none of them may reach the row.
//
// This is the column-set pin in behavioural form — it fails if any column is
// added back to the write, and unlike a SQL-string assertion it cannot be
// satisfied by a statement that names the column and happens to write the same
// value.
func TestSensingLedger_TransitionSlot_WritesNothingOutsideItsOwnedColumns(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	original := slot("X1-AA-M1", "PARKED")
	original.SlotKind = "MARKET"
	original.AssignedShip = strptr("ORION-1")
	original.WhitelistGoods = `["FUEL"]`
	original.DepthCredits = 4200
	require.NoError(t, repo.UpsertSpareSlot(ctx, original))
	require.NoError(t, repo.MarkScanned(ctx, 1, "X1-AA-M1", "MARKET", scanEpoch, 42.5))

	yard := "X1-AA-Y1"
	require.NoError(t, repo.TransitionSlot(ctx, 1, "X1-AA-M1", "MARKET", "PARKED", "QUEUED",
		func(m *persistence.SensingSlotModel) {
			m.PurchaseYard = &yard // owned: must land

			// None of these are the transition's to write.
			m.SystemSymbol = "X1-ZZ"
			m.SlotKind = "SPARE"
			m.WhitelistGoods = `["CLOTHING"]`
			m.DepthCredits = 1
			m.SpreadEWMA = 0
			m.LastScanAt = &staleEpoch
		}))

	var row persistence.SensingSlotModel
	require.NoError(t, db.Where("player_id = ? AND waypoint_symbol = ?", 1, "X1-AA-M1").First(&row).Error)

	require.Equal(t, "QUEUED", row.State)
	require.Equal(t, yard, derefOr(row.PurchaseYard))

	require.Equal(t, "X1-AA", row.SystemSymbol, "system_symbol is not a transition's column")
	require.Equal(t, "MARKET", row.SlotKind, "slot_kind is not a transition's column")
	require.Equal(t, `["FUEL"]`, row.WhitelistGoods, "the screen owns the goods list")
	require.Equal(t, int64(4200), row.DepthCredits, "the screen owns depth")
	require.InDelta(t, 42.5, row.SpreadEWMA, 0.0001, "MarkScanned owns the spread")
	require.NotNil(t, row.LastScanAt)
	require.WithinDuration(t, scanEpoch, *row.LastScanAt, time.Second, "MarkScanned owns the stamp")
}

// A transition that asks for no field changes must write its STATE and nothing
// else — not even a carry-back of the columns it is allowed to own.
//
// This is what makes the filed slot reaper safe by construction: it is
// a state-only transition, so its UPDATE names one column, and no concurrent
// writer of assigned_ship or purchase_yard can be reverted by it.
func TestSensingLedger_TransitionSlot_StateOnlyWriteNamesOnlyState(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	seed := slot("X1-AA-M1", "PARKED")
	seed.AssignedShip = strptr("ORION-1")
	seed.PurchaseYard = strptr("X1-AA-Y1")
	require.NoError(t, repo.UpsertSpareSlot(ctx, seed))

	statements := captureUpdates(t, db)

	require.NoError(t, repo.TransitionSlot(ctx, 1, "X1-AA-M1", "MARKET", "PARKED", "WANTED", nil))

	// Only the SET clause is examined. Column OWNERSHIP is about what a statement
	// WRITES, and the WHERE clause names columns for a different reason entirely —
	// slot_kind is in it because it is part of the row's address, which
	// narrows what this write can touch rather than widening it. Asserting over the
	// whole statement conflated the two and would fail on a predicate that made the
	// write strictly safer.
	sql := setClauseOf(t, statements.onlySlotUpdate(t))
	require.Contains(t, sql, "state", "the state flip IS the transition")
	require.Contains(t, sql, "updated_at")
	require.NotContains(t, sql, "assigned_ship",
		"a transition that changed no hull must not name the column a concurrent writer may own")
	require.NotContains(t, sql, "purchase_yard")
	require.NotContains(t, sql, "spread_ewma")
	require.NotContains(t, sql, "last_scan_at")
	require.NotContains(t, sql, "whitelist_goods")
	require.NotContains(t, sql, "depth_credits")
	require.NotContains(t, sql, "slot_kind")
	require.NotContains(t, sql, "system_symbol")
}

// setClauseOf returns the assignment half of an UPDATE — everything before the
// WHERE — so an ownership assertion reads what the statement writes and not what
// it matches on.
func setClauseOf(t *testing.T, sql string) string {
	t.Helper()
	where := strings.Index(sql, " WHERE ")
	require.Greater(t, where, 0, "an ownership-checked UPDATE always carries a WHERE: %s", sql)
	return sql[:where]
}

// Naming a column only when the callback CHANGED it must not depend on HOW the
// callback changed it.
//
// The owned columns are pointers into the loaded row, so a callback can either
// replace the pointer (what the port does) or write through the one it was
// handed. A snapshot taken as a plain struct copy would share the pointer target
// with the row, so the second form would compare equal to itself and be dropped
// from the write — a hull assignment silently lost.
func TestSensingLedger_TransitionSlot_HonoursAnInPlaceFieldWrite(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	seed := slot("X1-AA-M1", "PARKED")
	seed.AssignedShip = strptr("ORION-1")
	require.NoError(t, repo.UpsertSpareSlot(ctx, seed))

	require.NoError(t, repo.TransitionSlot(ctx, 1, "X1-AA-M1", "MARKET", "PARKED", "IN_TRANSIT",
		func(m *persistence.SensingSlotModel) {
			*m.AssignedShip = "ORION-2" // through the pointer, not replacing it
		}))

	var row persistence.SensingSlotModel
	require.NoError(t, db.Where("player_id = ? AND waypoint_symbol = ?", 1, "X1-AA-M1").First(&row).Error)
	require.Equal(t, "IN_TRANSIT", row.State)
	require.Equal(t, "ORION-2", derefOr(row.AssignedShip), "a hull written in place must still reach the row")
}

// A SCREEN re-declaration over a placement that is already filled must refresh
// what the screen measured and touch nothing else.
//
// The hull is the money-critical half (RULINGS #4). A screen row is always a
// WANTED carrying no ship, so a blanket conflict set writes NULL over
// assigned_ship — and CountOwnedProbes counts hulls through this column, so the
// probe we are standing on disappears from the cap and the buy queue is cleared
// to purchase a replacement for it. The scan history is the other half: zeroing
// it makes the slot read as never scanned and pulls its next scan forward.
func TestSensingLedger_UpsertSlotMetadata_CannotClobberAFilledPlacement(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	filled := slot("X1-AA-M1", "PARKED")
	filled.AssignedShip = strptr("ORION-1")
	filled.PurchaseYard = strptr("X1-AA-Y1")
	require.NoError(t, repo.UpsertSpareSlot(ctx, filled))
	require.NoError(t, repo.MarkScanned(ctx, 1, "X1-AA-M1", "MARKET", scanEpoch, 42.5))

	// The screen re-declares the waypoint as an unfilled want, with fresh goods.
	rescreen := slot("X1-AA-M1", "WANTED")
	rescreen.WhitelistGoods = `["FUEL","CLOTHING"]`
	rescreen.DepthCredits = 7300
	require.NoError(t, repo.UpsertSlotMetadata(ctx, rescreen))

	var row persistence.SensingSlotModel
	require.NoError(t, db.Where("player_id = ? AND waypoint_symbol = ?", 1, "X1-AA-M1").First(&row).Error)

	require.Equal(t, `["FUEL","CLOTHING"]`, row.WhitelistGoods, "the screen's own columns still refresh")
	require.Equal(t, int64(7300), row.DepthCredits)

	require.Equal(t, "PARKED", row.State, "a re-screen must not walk the state machine backwards")
	require.Equal(t, "ORION-1", derefOr(row.AssignedShip),
		"a re-screen must never drop a hull out of the probe-cap count")
	require.Equal(t, "X1-AA-Y1", derefOr(row.PurchaseYard))
	require.NotNil(t, row.LastScanAt)
	require.WithinDuration(t, scanEpoch, *row.LastScanAt, time.Second)
	require.InDelta(t, 42.5, row.SpreadEWMA, 0.0001)
}

// Standing a hull down on a waypoint that already carries a SPARE row must
// re-point the row at the hull now standing there — the write is how the ledger
// learns which probe it is.
//
// It must NOT carry the screen's columns along with it: a stand-down measures
// nothing, so its empty goods list would erase a market's whitelist, and an empty
// whitelist drops the waypoint out of the screen's hit set for good.
func TestSensingLedger_UpsertSpareSlot_RefreshesTheHullAndLeavesTheScreensColumns(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	existing := slot("X1-AA-Y1", "PARKED")
	existing.SlotKind = "SPARE"
	existing.AssignedShip = strptr("PROBE-1")
	existing.WhitelistGoods = `["FUEL"]`
	existing.DepthCredits = 4200
	require.NoError(t, repo.UpsertSpareSlot(ctx, existing))
	require.NoError(t, repo.MarkScanned(ctx, 1, "X1-AA-Y1", "SPARE", scanEpoch, 42.5))

	// A second seed finishes on the same waypoint and stands down there.
	standDown := persistence.SensingSlotModel{
		PlayerID:       1,
		WaypointSymbol: "X1-AA-Y1",
		SystemSymbol:   "X1-AA",
		SlotKind:       "SPARE",
		State:          "PARKED",
		AssignedShip:   strptr("PROBE-2"),
	}
	require.NoError(t, repo.UpsertSpareSlot(ctx, standDown))

	var row persistence.SensingSlotModel
	require.NoError(t, db.Where("player_id = ? AND waypoint_symbol = ?", 1, "X1-AA-Y1").First(&row).Error)

	require.Equal(t, "PARKED", row.State)
	require.Equal(t, "SPARE", row.SlotKind)
	require.Equal(t, "PROBE-2", derefOr(row.AssignedShip), "the row must name the hull actually standing here")

	require.Equal(t, `["FUEL"]`, row.WhitelistGoods, "a stand-down measures nothing and must erase nothing")
	require.Equal(t, int64(4200), row.DepthCredits)
	require.NotNil(t, row.LastScanAt)
	require.WithinDuration(t, scanEpoch, *row.LastScanAt, time.Second)
	require.InDelta(t, 42.5, row.SpreadEWMA, 0.0001)

	owned, err := repo.CountOwnedProbes(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, int64(1), owned, "one waypoint, one row, one hull")
}

// --- helpers -----------------------------------------------------------------

func derefOr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// capturedSQL collects the UPDATE statements GORM issues, so a test can assert
// the literal column set of a write rather than only its effect.
type capturedSQL struct{ stmts []string }

func (c *capturedSQL) onlySlotUpdate(t *testing.T) string {
	t.Helper()
	var hits []string
	for _, s := range c.stmts {
		if strings.Contains(s, "sensing_slots") {
			hits = append(hits, s)
		}
	}
	require.Len(t, hits, 1, "expected exactly one UPDATE against sensing_slots")
	return hits[0]
}

func captureUpdates(t *testing.T, db *gorm.DB) *capturedSQL {
	t.Helper()
	captured := &capturedSQL{}
	name := "sp_wgjb7_capture_update"
	require.NoError(t, db.Callback().Update().After("gorm:update").Register(name, func(tx *gorm.DB) {
		captured.stmts = append(captured.stmts, tx.Statement.SQL.String())
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove(name) })
	return captured
}
