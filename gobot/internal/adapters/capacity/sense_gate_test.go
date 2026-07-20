package capacity_test

// sp-3idiw — the SENSE lane folds the live jump-gate construction shortfall into the
// reconciler's demand alongside contract demand. A nil reader (unwired) yields no gate
// demand: the sensor is byte-identical to its contract-only self (the emergency-disable /
// OFF path). A wired reader surfaces the gate as a HubKindGate demand hub.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	capacityAdapters "github.com/andrescamacho/spacetraders-go/internal/adapters/capacity"
	"github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// fakeGateShortfall doubles the live-API gate-shortfall boundary.
type fakeGateShortfall struct {
	shortfall capacityAdapters.GateShortfall
	err       error
}

func (f fakeGateShortfall) GateShortfall(context.Context, int) (capacityAdapters.GateShortfall, error) {
	return f.shortfall, f.err
}

func gateHubOf(signals capacity.Signals) *capacity.HubDemand {
	for i := range signals.Demand.Hubs {
		if signals.Demand.Hubs[i].Kind == capacity.HubKindGate {
			return &signals.Demand.Hubs[i]
		}
	}
	return nil
}

// Behavior: a wired gate reader surfaces the gate construction shortfall as a HubKindGate
// demand hub folded into the reconciler's demand snapshot.
func TestSense_FoldsGateShortfallIntoDemand(t *testing.T) {
	db := newTestDB(t)
	playerID := createPlayer(t, db, "GATER")
	reader := fakeGateShortfall{shortfall: capacityAdapters.GateShortfall{
		GateWaypoint: "X1-UM5-I56",
		Materials:    []capacityAdapters.GateMaterialShortfall{{TradeSymbol: "FAB_MATS", Remaining: 1600}},
	}}
	sensor := capacityAdapters.NewSensor(db, fakeTreasury{credits: 1},
		capacityAdapters.WithSensorClock(&shared.MockClock{CurrentTime: t0}),
		capacityAdapters.WithGateShortfallReader(reader))

	signals, err := sensor.Sense(context.Background(), playerID)

	require.NoError(t, err)
	gate := gateHubOf(signals)
	require.NotNil(t, gate, "the live gate shortfall must be folded into the reconciler's demand")
	require.Equal(t, "X1-UM5-I56", gate.HubSymbol)
}

// Behavior (byte-identical OFF): no gate reader wired → no gate demand ever appears. The
// reconciler is exactly its contract-only self (the emergency-disable path).
func TestSense_NilGateReaderYieldsNoGateDemand(t *testing.T) {
	db := newTestDB(t)
	playerID := createPlayer(t, db, "NOGATE")

	signals, err := newSensorUnderTest(db, fakeTreasury{credits: 1}).Sense(context.Background(), playerID)

	require.NoError(t, err)
	require.Nil(t, gateHubOf(signals), "no gate reader wired → no gate demand (byte-identical OFF path)")
}

// Behavior: a gate-reader FAILURE fails closed to no gate demand — a live-API hiccup
// degrades the gate signal (logged) but never blocks the tick or fabricates a depot.
func TestSense_GateReaderErrorFailsClosed(t *testing.T) {
	db := newTestDB(t)
	playerID := createPlayer(t, db, "GATEERR")
	reader := fakeGateShortfall{err: context.DeadlineExceeded}
	sensor := capacityAdapters.NewSensor(db, fakeTreasury{credits: 1},
		capacityAdapters.WithSensorClock(&shared.MockClock{CurrentTime: t0}),
		capacityAdapters.WithGateShortfallReader(reader))

	signals, err := sensor.Sense(context.Background(), playerID)

	require.NoError(t, err, "a gate-reader failure must never block the tick")
	require.Nil(t, gateHubOf(signals), "a gate-reader failure fails closed to no gate demand")
}
