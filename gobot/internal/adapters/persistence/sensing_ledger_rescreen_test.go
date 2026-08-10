package persistence_test

// THE OPERATOR RESCREEN and what it is forbidden to touch.
//
// A system's VERDICT is stamped with the goods whitelist as of the moment it was
// written, and NO_WHITELIST is durable — only PENDING systems are ever re-screened
// — so a system judged worthless under the old list is never reconsidered under a
// new one. ResetVerdictsToPending re-opens that judgement, and nothing else.
//
// EXACTLY that. A rescreen re-evaluates JUDGEMENT, never OWNERSHIP: the hulls are
// already bought and parked. Reaching a slot's state or assigned_ship would drop a
// paid-for probe out of CountOwnedProbes and authorise buying it a second time —
// the precise failure the column-ownership split exists to prevent, and
// the reason the assertions below are field-by-field rather than spot checks.
//
//	ResetVerdictsToPending   verdict   (+ updated_at)   — sensing_systems only
//
// The slot half of the rescreen is deliberately ABSENT; see the method's own
// comment for why clearing whitelist_goods cannot be done from this layer alone.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
)

var (
	rescreenScan    = time.Date(2026, 7, 27, 8, 0, 0, 0, time.UTC)
	rescreenCatalog = time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC)
	rescreenScreen  = time.Date(2026, 7, 26, 8, 0, 0, 0, time.UTC)
)

// judgedSystem is a fully-populated system row: a verdict, the catalog sweep
// stamp, a live charting errand, and the measurements a screen recorded.
func judgedSystem(system, verdict string) persistence.SensingSystemModel {
	return persistence.SensingSystemModel{
		PlayerID:        1,
		SystemSymbol:    system,
		Verdict:         verdict,
		ScreenedAt:      &rescreenScreen,
		UnchartedCount:  4,
		CatalogSyncedAt: &rescreenCatalog,
		SeedShip:        strptr("ORION-SEED"),
		SeedState:       strptr("CHARTING"),
		DepthCredits:    9_000,
	}
}

// parkedSlot is a fully-populated slot row: a hull on station, a scan history,
// and the goods projection the screen recorded when it was placed.
func parkedSlot(waypoint string) persistence.SensingSlotModel {
	return persistence.SensingSlotModel{
		PlayerID:       1,
		WaypointSymbol: waypoint,
		SystemSymbol:   "X1-AA",
		SlotKind:       "MARKET",
		State:          "PARKED",
		AssignedShip:   strptr("ORION-7"),
		PurchaseYard:   strptr("X1-AA-Y1"),
		WhitelistGoods: `["FOOD","CLOTHING"]`,
		SpreadEWMA:     37.5,
		LastScanAt:     &rescreenScan,
		DepthCredits:   4_200,
	}
}

func loadSystem(t *testing.T, db *gorm.DB, system string) persistence.SensingSystemModel {
	t.Helper()
	var m persistence.SensingSystemModel
	require.NoError(t, db.Where("player_id = ? AND system_symbol = ?", 1, system).First(&m).Error)
	return m
}

func loadSlot(t *testing.T, db *gorm.DB, waypoint string) persistence.SensingSlotModel {
	t.Helper()
	var m persistence.SensingSlotModel
	require.NoError(t, db.Where("player_id = ? AND waypoint_symbol = ?", 1, waypoint).First(&m).Error)
	return m
}

// --- the verdict reset ----------------------------------------------------------

// Every verdict goes back to PENDING, because PENDING is the only verdict the
// steady-state sweep re-screens. Both decided verdicts are covered: NO_WHITELIST
// is the one an operator is usually chasing (a widened list should re-open the
// systems it now accepts), and IN_SCOPE matters just as much the other way (a
// rotated list may no longer justify the placements standing there).
//
// catalog_synced_at SURVIVES, and that is not incidental. The catalog is a fact
// about the MAP, not a judgement made under a whitelist; clearing it would make
// every system read as unswept, and an unswept system is a charting-seed TARGET —
// so the rescreen would dispatch probes to re-chart a galaxy already sitting in
// the database. The seed fields survive for the same class of reason: an errand in
// flight must outlive an operator re-opening the map's verdicts.
func TestSensingLedger_ResetVerdictsToPending(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	require.NoError(t, db.Create(&[]persistence.SensingSystemModel{
		judgedSystem("X1-AA", "NO_WHITELIST"),
		judgedSystem("X1-BB", "IN_SCOPE"),
		judgedSystem("X1-CC", "PENDING"),
	}).Error)

	n, err := repo.ResetVerdictsToPending(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, int64(3), n, "every system the player holds is re-opened for screening")

	for _, system := range []string{"X1-AA", "X1-BB", "X1-CC"} {
		row := loadSystem(t, db, system)
		require.Equal(t, "PENDING", row.Verdict, "%s awaits re-screening", system)

		// The whole rest of the row is untouched — most of all the catalog stamp
		// and the errand.
		require.Equal(t, rescreenCatalog.UTC(), row.CatalogSyncedAt.UTC(),
			"%s keeps its catalog sweep — the map is not what the whitelist judged", system)
		require.Equal(t, "ORION-SEED", *row.SeedShip, "%s keeps its in-flight charting errand", system)
		require.Equal(t, "CHARTING", *row.SeedState)
		require.Equal(t, rescreenScreen.UTC(), row.ScreenedAt.UTC(), "%s keeps when it was last looked at", system)
		require.Equal(t, 4, row.UnchartedCount, "%s keeps its measured dark count", system)
		require.Equal(t, int64(9_000), row.DepthCredits)
	}
}

// THE MONEY-GUARD PIN (RULINGS #4). The rescreen writes sensing_systems and must
// not reach sensing_slots AT ALL: the probe cap counts slot rows, so disturbing
// state or assigned_ship would make the fleet read SMALLER than it is and
// authorise buying replacements for hulls standing on station. Asserted field by
// field, because a widened UPDATE is invisible to any check that only looks at the
// column it meant to change.
func TestSensingLedger_ResetVerdicts_LeavesEverySlotColumnAlone(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	require.NoError(t, db.Create(&[]persistence.SensingSystemModel{judgedSystem("X1-AA", "NO_WHITELIST")}).Error)
	require.NoError(t, db.Create(&[]persistence.SensingSlotModel{parkedSlot("X1-AA-M1")}).Error)
	before := loadSlot(t, db, "X1-AA-M1")

	owned, err := repo.CountOwnedProbes(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, int64(2), owned,
		"two hulls are on the books before the rescreen: the parked slot's, and the system row's charting errand")

	_, err = repo.ResetVerdictsToPending(ctx, 1)
	require.NoError(t, err)

	after := loadSlot(t, db, "X1-AA-M1")
	require.Equal(t, "PARKED", after.State, "a rescreen re-judges; it does not un-place a hull")
	require.Equal(t, "ORION-7", *after.AssignedShip,
		"the hull is already paid for — losing it here drops it out of CountOwnedProbes and re-buys it")
	require.Equal(t, "MARKET", after.SlotKind)
	require.Equal(t, "X1-AA-Y1", *after.PurchaseYard)
	require.Equal(t, before.WhitelistGoods, after.WhitelistGoods,
		"the goods projection is NOT cleared — see ResetVerdictsToPending for why that needs a screen change")
	require.Equal(t, rescreenScan.UTC(), after.LastScanAt.UTC(), "scan history is whitelist-independent")
	require.Equal(t, 37.5, after.SpreadEWMA)
	require.Equal(t, int64(4_200), after.DepthCredits)

	nowOwned, err := repo.CountOwnedProbes(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, owned, nowOwned, "the probe cap sees exactly the same fleet after a rescreen")
}

// --- scoping and idempotence ----------------------------------------------------

// A bulk write is exactly where a missing predicate does the most damage. Another
// player's ledger is not this operator's to re-open.
func TestSensingLedger_ResetVerdicts_TouchesOnlyThisPlayer(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	mine := judgedSystem("X1-AA", "NO_WHITELIST")
	theirs := judgedSystem("X1-BB", "NO_WHITELIST")
	theirs.PlayerID = 2
	require.NoError(t, db.Create(&[]persistence.SensingSystemModel{mine, theirs}).Error)

	n, err := repo.ResetVerdictsToPending(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, int64(1), n)

	var other persistence.SensingSystemModel
	require.NoError(t, db.Where("player_id = ? AND system_symbol = ?", 2, "X1-BB").First(&other).Error)
	require.Equal(t, "NO_WHITELIST", other.Verdict, "another player's verdicts are not re-opened")
}

// The rescreen invalidates what the ENGINE CAN SEE, so it carries the same era
// predicate the reads do. A dead era's rows are invisible to every planning read,
// so re-opening them would be work with no reader — and it would churn rows a
// closed era is supposed to have finished with.
func TestSensingLedger_ResetVerdicts_IsEraScopedLikeTheReads(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	deadEra := 4242
	current := judgedSystem("X1-AA", "NO_WHITELIST") // era_id NULL — visible to the reads
	closed := judgedSystem("X1-ZZ", "NO_WHITELIST")
	closed.EraID = &deadEra
	require.NoError(t, db.Create(&[]persistence.SensingSystemModel{current, closed}).Error)

	n, err := repo.ResetVerdictsToPending(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, int64(1), n, "only the rows the engine can actually read are re-opened")
	require.Equal(t, "PENDING", loadSystem(t, db, "X1-AA").Verdict)
	require.Equal(t, "NO_WHITELIST", loadSystem(t, db, "X1-ZZ").Verdict, "a closed era is left alone")
}

// Idempotent, so an operator unsure whether the rescreen landed can simply run it
// again. The second pass still MATCHES the rows — they are the same rows — and
// what it must not do is change anything.
func TestSensingLedger_ResetVerdicts_IsIdempotent(t *testing.T) {
	db := newSensingLedgerDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	require.NoError(t, db.Create(&[]persistence.SensingSystemModel{judgedSystem("X1-AA", "NO_WHITELIST")}).Error)
	require.NoError(t, db.Create(&[]persistence.SensingSlotModel{parkedSlot("X1-AA-M1")}).Error)

	_, err := repo.ResetVerdictsToPending(ctx, 1)
	require.NoError(t, err)
	first, firstSlot := loadSystem(t, db, "X1-AA"), loadSlot(t, db, "X1-AA-M1")

	_, err = repo.ResetVerdictsToPending(ctx, 1)
	require.NoError(t, err)
	second, secondSlot := loadSystem(t, db, "X1-AA"), loadSlot(t, db, "X1-AA-M1")

	require.Equal(t, first.Verdict, second.Verdict)
	require.Equal(t, first.CatalogSyncedAt.UTC(), second.CatalogSyncedAt.UTC())
	require.Equal(t, *first.SeedShip, *second.SeedShip)
	require.Equal(t, firstSlot.WhitelistGoods, secondSlot.WhitelistGoods)
	require.Equal(t, firstSlot.State, secondSlot.State)
	require.Equal(t, *firstSlot.AssignedShip, *secondSlot.AssignedShip)
	require.Equal(t, firstSlot.LastScanAt.UTC(), secondSlot.LastScanAt.UTC())
}
