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
	err      error
}

func (c *capturingScanner) ScanAndSaveMarket(ctx context.Context, playerID uint, waypointSymbol string) error {
	c.ctx, c.playerID, c.waypoint = ctx, playerID, waypointSymbol
	c.calls++
	return c.err
}

func TestScanRunnerPort_TagsEveryScanAsLowPriorityScanningSpend(t *testing.T) {
	spy := &capturingScanner{}
	port := adapterSensing.NewScanRunnerPort(spy)

	require.NoError(t, port.Run(context.Background(), testPlayerID, "X1-AA-M1"))

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

	require.ErrorIs(t, port.Run(context.Background(), testPlayerID, "X1-AA-M1"), scanErr)
}

// A non-positive player is rejected before the scan is issued. The pacer stamps
// the slot and retries either way, so the value of failing here is that the
// wasted call is never made.
func TestScanRunnerPort_RejectsAnInvalidPlayerWithoutScanning(t *testing.T) {
	spy := &capturingScanner{}
	port := adapterSensing.NewScanRunnerPort(spy)

	require.Error(t, port.Run(context.Background(), 0, "X1-AA-M1"))
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
	require.NoError(t, port.MarkScanned(context.Background(), testPlayerID, "X1-AA-M1", at, 0.42))

	var got persistence.SensingSlotModel
	require.NoError(t, db.Where("player_id = ? AND waypoint_symbol = ?", testPlayerID, "X1-AA-M1").First(&got).Error)
	require.InDelta(t, 0.42, got.SpreadEWMA, 1e-9)
	require.NotNil(t, got.LastScanAt)
	require.WithinDuration(t, at, got.LastScanAt.UTC(), time.Second)
	require.Equal(t, appSensing.SlotStateParked, got.State, "a scan must never move the state machine")
	require.Equal(t, ship, *got.AssignedShip, "a scan must never touch the hull assignment")
}

// A scan of a placement that no longer exists is an error, never an upsert: a
// row conjured by the scan path would later be read back as a real placement
// and dispatched to.
func TestLedgerPort_MarkScannedOnAMissingSlotFails(t *testing.T) {
	db := newShipPortsDB(t)
	port := adapterSensing.NewLedgerPort(persistence.NewSensingLedgerRepository(db))

	require.Error(t, port.MarkScanned(context.Background(), testPlayerID, "X1-AA-GHOST", time.Now().UTC(), 1.0))

	var count int64
	require.NoError(t, db.Model(&persistence.SensingSlotModel{}).Count(&count).Error)
	require.Zero(t, count)
}
