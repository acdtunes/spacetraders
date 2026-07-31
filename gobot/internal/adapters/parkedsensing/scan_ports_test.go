package parkedsensing_test

// The scan runner carries no logic worth testing — it delegates to the fleet's
// existing market scanner. What it DOES carry is the attribution the whole
// budget model rests on, and a mis-tagged call is silent: nothing fails, spend
// just lands in the wrong budget and the pacer's residual chases itself
// downward. So that is what these tests pin.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	adapterSensing "github.com/andrescamacho/spacetraders-go/internal/adapters/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	appSensing "github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/apibudget"
)

// capturingScanner records the context it was handed, which is the only way to
// observe the tags from outside the api package.
type capturingScanner struct {
	ctx      context.Context
	playerID uint
	waypoint string
	calls    int
	scanned  bool
	err      error
}

func (c *capturingScanner) ScanAndSaveMarketWithOutcome(ctx context.Context, playerID uint, waypointSymbol string) (bool, error) {
	c.ctx, c.playerID, c.waypoint = ctx, playerID, waypointSymbol
	c.calls++
	return c.scanned, c.err
}

func TestScanRunnerPort_TagsEveryScanAsLowPriorityScanningSpend(t *testing.T) {
	spy := &capturingScanner{scanned: true}
	port := adapterSensing.NewScanRunnerPort(spy)

	scanned, err := port.Run(context.Background(), testPlayerID, "X1-AA-M1")
	require.NoError(t, err)
	require.True(t, scanned, "a scan that wrote data must report as one")

	require.Equal(t, apibudget.SourceScanning, api.SourceForTest(spy.ctx),
		"a parked-probe scan billed to any other source double-counts against the pacer's own residual")

	priority, explicit := api.PriorityForTest(spy.ctx)
	require.True(t, explicit,
		"scans must set priority EXPLICITLY; leaving it to endpoint classification puts Get Market at NORMAL, ahead of trade-critical calls it should yield to")
	require.Equal(t, api.PriorityLow, priority)

	require.Equal(t, uint(testPlayerID), spy.playerID)
	require.Equal(t, "X1-AA-M1", spy.waypoint)
}

func TestScanRunnerPort_PropagatesScanFailure(t *testing.T) {
	scanErr := errors.New("market unreachable")
	port := adapterSensing.NewScanRunnerPort(&capturingScanner{err: scanErr})

	scanned, err := port.Run(context.Background(), testPlayerID, "X1-AA-M1")
	require.ErrorIs(t, err, scanErr)
	require.False(t, scanned, "a failed scan wrote nothing and must not report as a scan")
}

// A non-positive player is rejected before the scan is issued. The pacer stamps
// the slot and retries either way, so the value of failing here is that the
// wasted call is never made.
func TestScanRunnerPort_RejectsAnInvalidPlayerWithoutScanning(t *testing.T) {
	spy := &capturingScanner{}
	port := adapterSensing.NewScanRunnerPort(spy)

	_, err := port.Run(context.Background(), 0, "X1-AA-M1")
	require.Error(t, err)
	require.Zero(t, spy.calls, "an invalid player must not reach the API")
}

// MarkScanned is the scan path's only write. This proves the port reaches the
// real column set — and, more importantly, that it leaves the placement
// machine's columns alone, which is what lets the pacer and the reconcile run
// concurrently on the same row.
func TestLedgerPort_MarkScannedTouchesOnlyTheScanColumns(t *testing.T) {
	db := newShipPortsDB(t)
	ship := "PROBE-1"
	require.NoError(t, db.Create(&persistence.SensingSlotModel{
		PlayerID: testPlayerID, WaypointSymbol: "X1-AA-M1", SystemSymbol: "X1-AA",
		SlotKind: appSensing.SlotKindMarket, State: appSensing.SlotStateParked,
		AssignedShip: &ship, WhitelistGoods: "[]",
	}).Error)

	port := adapterSensing.NewLedgerPort(persistence.NewSensingLedgerRepository(db))
	at := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, port.MarkScanned(context.Background(), testPlayerID, "X1-AA-M1", appSensing.SlotKindMarket, at, 0.42))

	var got persistence.SensingSlotModel
	require.NoError(t, db.Where("player_id = ? AND waypoint_symbol = ?", testPlayerID, "X1-AA-M1").First(&got).Error)
	require.InDelta(t, 0.42, got.SpreadEWMA, 1e-9)
	require.NotNil(t, got.LastScanAt)
	require.WithinDuration(t, at, got.LastScanAt.UTC(), time.Second)
	require.Equal(t, appSensing.SlotStateParked, got.State, "a scan must never move the state machine")
	require.Equal(t, ship, *got.AssignedShip, "a scan must never touch the hull assignment")
}

// --- the two scan clocks (sp-zml2u) ------------------------------------------

// A COMPLETED scan advances BOTH clocks. The freshness stamp is the point, and
// the attempt stamp rides along because a scan that wrote data is also a turn the
// rotation took — a slot that never gets declined would otherwise pace off a
// permanently NULL attempt column.
func TestLedgerPort_MarkScannedAdvancesBothScanClocks(t *testing.T) {
	db := newShipPortsDB(t)
	require.NoError(t, db.Create(&persistence.SensingSlotModel{
		PlayerID: testPlayerID, WaypointSymbol: "X1-AA-M1", SystemSymbol: "X1-AA",
		SlotKind: appSensing.SlotKindMarket, State: appSensing.SlotStateParked,
		WhitelistGoods: "[]",
	}).Error)

	port := adapterSensing.NewLedgerPort(persistence.NewSensingLedgerRepository(db))
	at := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, port.MarkScanned(context.Background(), testPlayerID, "X1-AA-M1", appSensing.SlotKindMarket, at, 0.42))

	var got persistence.SensingSlotModel
	require.NoError(t, db.Where("player_id = ? AND waypoint_symbol = ?", testPlayerID, "X1-AA-M1").First(&got).Error)
	require.NotNil(t, got.LastScanAt, "a completed scan claims freshness")
	require.WithinDuration(t, at, got.LastScanAt.UTC(), time.Second)
	require.NotNil(t, got.LastScanAttemptAt, "a completed scan is also a turn, and must pace like one")
	require.WithinDuration(t, at, got.LastScanAttemptAt.UTC(), time.Second)
}

// A DECLINED turn advances the pacing clock ALONE. This is the whole freshness
// fix at the storage layer: last_scan_at is a claim that market data was written,
// and a budget decline writes none — while last_scan_attempt_at must still move,
// or the next reconcile re-paces the slot from its last real scan and it is due
// again immediately.
func TestLedgerPort_MarkScanAttemptedMovesOnlyThePacingClock(t *testing.T) {
	db := newShipPortsDB(t)
	scannedAt := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	ship := "PROBE-1"
	require.NoError(t, db.Create(&persistence.SensingSlotModel{
		PlayerID: testPlayerID, WaypointSymbol: "X1-AA-M1", SystemSymbol: "X1-AA",
		SlotKind: appSensing.SlotKindMarket, State: appSensing.SlotStateParked,
		AssignedShip: &ship, WhitelistGoods: "[]",
		LastScanAt: &scannedAt, SpreadEWMA: 0.42,
	}).Error)

	port := adapterSensing.NewLedgerPort(persistence.NewSensingLedgerRepository(db))
	declinedAt := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, port.MarkScanAttempted(context.Background(), testPlayerID, "X1-AA-M1", appSensing.SlotKindMarket, declinedAt))

	var got persistence.SensingSlotModel
	require.NoError(t, db.Where("player_id = ? AND waypoint_symbol = ?", testPlayerID, "X1-AA-M1").First(&got).Error)

	require.NotNil(t, got.LastScanAttemptAt)
	require.WithinDuration(t, declinedAt, got.LastScanAttemptAt.UTC(), time.Second,
		"a declined turn must still pace, or the rotation spins at full speed on the next rebuild")

	require.NotNil(t, got.LastScanAt)
	require.WithinDuration(t, scannedAt, got.LastScanAt.UTC(), time.Second,
		"a declined turn wrote no market data and must not claim freshness for any")
	require.InDelta(t, 0.42, got.SpreadEWMA, 1e-9,
		"a spread computed on a declined turn is derived from data no scan backs; it is not persisted")
	require.Equal(t, appSensing.SlotStateParked, got.State, "a scan attempt must never move the state machine")
	require.Equal(t, ship, *got.AssignedShip, "a scan attempt must never touch the hull assignment")
}

// ParkedSlotViews reads the two columns into the two fields that need them: the
// rotation paces on the ATTEMPT clock, the staleness gauge reports the DATA
// clock. Reading one column into both is the collapse sp-zml2u removes.
func TestLedgerPort_ParkedSlotViewsSeparatesThePacingClockFromTheFreshnessClaim(t *testing.T) {
	db := newShipPortsDB(t)
	dataAt := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	attemptAt := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, db.Create(&persistence.SensingSlotModel{
		PlayerID: testPlayerID, WaypointSymbol: "X1-AA-M1", SystemSymbol: "X1-AA",
		SlotKind: appSensing.SlotKindMarket, State: appSensing.SlotStateParked,
		WhitelistGoods: "[]", LastScanAt: &dataAt, LastScanAttemptAt: &attemptAt,
	}).Error)

	port := adapterSensing.NewLedgerPort(persistence.NewSensingLedgerRepository(db))
	views, err := port.ParkedSlotViews(context.Background(), testPlayerID)
	require.NoError(t, err)
	require.Len(t, views, 1)

	require.WithinDuration(t, attemptAt, views[0].LastScan, time.Second,
		"the rotation paces on the attempt clock")
	require.WithinDuration(t, dataAt, views[0].LastDataAt, time.Second,
		"the staleness gauge reports the age of the DATA, which a declined turn does not refresh")
}

// THE ROLLOUT CASE, and the one that decides whether this change is safe to
// deploy at all.
//
// Every pre-existing row carries a NULL last_scan_attempt_at on the first tick
// after the migration. Read as the zero time that would make the ENTIRE rotation
// due at once — one full-speed sweep of every slot, which is precisely the
// regression the two-clock split exists to prevent. The coalesce to last_scan_at
// is what makes that first tick pace exactly as the last one did.
func TestLedgerPort_ParkedSlotViewsCoalescesANullAttemptClockToTheOldStamp(t *testing.T) {
	db := newShipPortsDB(t)
	scannedAt := time.Now().UTC().Add(-20 * time.Minute).Truncate(time.Second)
	require.NoError(t, db.Create(&persistence.SensingSlotModel{
		PlayerID: testPlayerID, WaypointSymbol: "X1-AA-M1", SystemSymbol: "X1-AA",
		SlotKind: appSensing.SlotKindMarket, State: appSensing.SlotStateParked,
		WhitelistGoods: "[]", LastScanAt: &scannedAt, // LastScanAttemptAt left NULL, as the migration leaves it
	}).Error)

	port := adapterSensing.NewLedgerPort(persistence.NewSensingLedgerRepository(db))
	views, err := port.ParkedSlotViews(context.Background(), testPlayerID)
	require.NoError(t, err)
	require.Len(t, views, 1)

	require.False(t, views[0].LastScan.IsZero(),
		"a NULL attempt clock read as the zero time makes every migrated slot due at once — a full-speed sweep of the whole rotation")
	require.WithinDuration(t, scannedAt, views[0].LastScan, time.Second,
		"the first tick after the migration must pace exactly as the last one did")
}

// A slot the rotation has genuinely never touched still reads as never
// attempted, so it keeps its immediate first turn. The coalesce must not
// manufacture a stamp out of a row that has neither.
func TestLedgerPort_ParkedSlotViewsLeavesANeverTouchedSlotDueImmediately(t *testing.T) {
	db := newShipPortsDB(t)
	require.NoError(t, db.Create(&persistence.SensingSlotModel{
		PlayerID: testPlayerID, WaypointSymbol: "X1-AA-M1", SystemSymbol: "X1-AA",
		SlotKind: appSensing.SlotKindMarket, State: appSensing.SlotStateParked,
		WhitelistGoods: "[]",
	}).Error)

	port := adapterSensing.NewLedgerPort(persistence.NewSensingLedgerRepository(db))
	views, err := port.ParkedSlotViews(context.Background(), testPlayerID)
	require.NoError(t, err)
	require.Len(t, views, 1)

	require.True(t, views[0].LastScan.IsZero(), "a never-attempted slot must still fall due immediately")
	require.True(t, views[0].LastDataAt.IsZero(), "a never-scanned slot is excluded from the staleness gauge, not clamped")
}

// A scan of a placement that no longer exists is an error, never an upsert: a
// row conjured by the scan path would later be read back as a real placement
// and dispatched to.
func TestLedgerPort_MarkScannedOnAMissingSlotFails(t *testing.T) {
	db := newShipPortsDB(t)
	port := adapterSensing.NewLedgerPort(persistence.NewSensingLedgerRepository(db))

	require.Error(t, port.MarkScanned(context.Background(), testPlayerID, "X1-AA-GHOST", appSensing.SlotKindMarket, time.Now().UTC(), 1.0))

	var count int64
	require.NoError(t, db.Model(&persistence.SensingSlotModel{}).Count(&count).Error)
	require.Zero(t, count)
}
