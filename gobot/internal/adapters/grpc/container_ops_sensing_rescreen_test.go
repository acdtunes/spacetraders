package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// The operator rescreen, driven end to end through the REAL
// persistence path. The engine-level consequences (the sweep re-screening a
// re-opened system, and reaching a different verdict under a changed whitelist)
// are covered in internal/application/scouting/commands; what is under test here
// is the verb itself and, above all, the write set it is allowed to have.

func seedRescreenLedger(t *testing.T, db *gorm.DB) {
	t.Helper()
	scanned := time.Date(2026, 7, 27, 8, 0, 0, 0, time.UTC)
	swept := time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC)
	ship := "ORION-7"
	yard := "X1-AA-Y1"

	require.NoError(t, db.Create(&[]persistence.SensingSystemModel{
		{PlayerID: 1, SystemSymbol: "X1-AA", Verdict: "NO_WHITELIST", CatalogSyncedAt: &swept},
		{PlayerID: 1, SystemSymbol: "X1-BB", Verdict: "IN_SCOPE", CatalogSyncedAt: &swept},
	}).Error)
	require.NoError(t, db.Create(&[]persistence.SensingSlotModel{{
		PlayerID: 1, WaypointSymbol: "X1-AA-M1", SystemSymbol: "X1-AA",
		SlotKind: "MARKET", State: "PARKED", AssignedShip: &ship, PurchaseYard: &yard,
		WhitelistGoods: `["FOOD"]`, SpreadEWMA: 37.5, LastScanAt: &scanned, DepthCredits: 4_200,
	}}).Error)
}

// The verb re-opens every verdict and reports the count, so an operator can see
// it actually matched something rather than silently doing nothing.
func TestRescreenSensing_ReopensVerdicts(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	seedRescreenLedger(t, db)
	s := &DaemonServer{db: db}

	res, err := s.RescreenSensing(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, int64(2), res.SystemsReopened)

	var systems []persistence.SensingSystemModel
	require.NoError(t, db.Where("player_id = ?", 1).Find(&systems).Error)
	for _, row := range systems {
		require.Equal(t, "PENDING", row.Verdict, "%s awaits re-screening", row.SystemSymbol)
		require.NotNil(t, row.CatalogSyncedAt, "%s keeps its catalog sweep", row.SystemSymbol)
	}

	// The slot rows are not this verb's to touch at all — see the money-guard pin
	// below, and RescreenSensing's own comment for why the projection half needs a
	// screen change rather than a wider write here.
	var slot persistence.SensingSlotModel
	require.NoError(t, db.Where("player_id = ? AND waypoint_symbol = ?", 1, "X1-AA-M1").First(&slot).Error)
	require.Equal(t, `["FOOD"]`, slot.WhitelistGoods)
}

// THE MONEY-GUARD PIN (RULINGS #4). A hull already bought and standing on station
// must come through a rescreen untouched: the probe cap counts ledger rows, so a
// verb that disturbed state or assigned_ship would make the fleet read smaller
// than it is and authorise buying a replacement for a probe already there.
func TestRescreenSensing_LeavesParkedHullsAndTheProbeCountAlone(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	seedRescreenLedger(t, db)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()

	before, err := repo.CountOwnedProbes(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, int64(1), before)

	s := &DaemonServer{db: db}
	_, err = s.RescreenSensing(ctx, 1)
	require.NoError(t, err)

	after, err := repo.CountOwnedProbes(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, before, after, "the probe cap sees the same fleet after a rescreen")

	var slot persistence.SensingSlotModel
	require.NoError(t, db.Where("player_id = ? AND waypoint_symbol = ?", 1, "X1-AA-M1").First(&slot).Error)
	require.Equal(t, "PARKED", slot.State, "a rescreen re-judges; it does not un-place a hull")
	require.Equal(t, "ORION-7", *slot.AssignedShip, "the hull it is already paying for stays named")
	require.Equal(t, "MARKET", slot.SlotKind)
	require.Equal(t, `["FOOD"]`, slot.WhitelistGoods, "the rotation still knows what this slot watches")
	require.NotNil(t, slot.LastScanAt, "scan history is whitelist-independent and expensive to rebuild")
	require.Equal(t, 37.5, slot.SpreadEWMA)
}

// Idempotent, so an operator unsure whether it landed can just run it again.
func TestRescreenSensing_IsIdempotent(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	seedRescreenLedger(t, db)
	s := &DaemonServer{db: db}
	ctx := context.Background()

	first, err := s.RescreenSensing(ctx, 1)
	require.NoError(t, err)
	second, err := s.RescreenSensing(ctx, 1)
	require.NoError(t, err)

	require.Equal(t, first.SystemsReopened, second.SystemsReopened,
		"the same rows match; the second pass simply has nothing left to change")

	var slot persistence.SensingSlotModel
	require.NoError(t, db.Where("player_id = ? AND waypoint_symbol = ?", 1, "X1-AA-M1").First(&slot).Error)
	require.Equal(t, "PARKED", slot.State)
	require.Equal(t, "ORION-7", *slot.AssignedShip)
}

// An empty ledger is not an error — a fleet that has not screened anything yet
// has nothing to invalidate, and the operator gets zeros rather than a failure.
func TestRescreenSensing_EmptyLedgerReportsZeros(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	s := &DaemonServer{db: db}

	res, err := s.RescreenSensing(context.Background(), 1)
	require.NoError(t, err)
	require.Zero(t, res.SystemsReopened)
}
