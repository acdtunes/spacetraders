package watchkeeper

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

// --- sp-a6e0: captain-set price tripwires are ONE-SHOT ---
//
// A RegimeTripwire fires at most once: when the crossing it detects rides a
// DELIVERED wake, the supervisor CONSUMES the tripwire — removes it from the
// persisted RegimePolicy — exactly as sp-wfut consumes a satisfied credits
// bound and sp-oyer disarms a fired watch. The captain re-declares to re-arm.
// The persisted policy IS the durable one-shot state (a consumed tripwire is
// simply absent), so a restart never resurrects it (RULINGS #2).

// seedCrossingMarket records a market price that crosses an ORE bid-above/200
// tripwire, and arms exactly that tripwire.
func seedOreCrossingAndTripwire(t *testing.T, sup *Supervisor, s *captainStores, now time.Time, window time.Duration) {
	t.Helper()
	require.NoError(t, sup.db.Create(&persistence.MarketData{
		WaypointSymbol: "X1-TEST-A1", GoodSymbol: "IRON_ORE", PlayerID: s.playerID,
		PurchasePrice: 100, SellPrice: 250, TradeVolume: 100, LastUpdated: now,
	}).Error)
	require.NoError(t, SaveRegimePolicy(NewWorkspace(s.dir).StatePath(), RegimePolicy{
		Tripwires: []RegimeTripwire{
			{Good: "ORE", Direction: "bid-above", Threshold: regimeThresholdPtr(200), Window: window},
		},
	}))
}

func countRegimeEvents(t *testing.T, sup *Supervisor) int64 {
	t.Helper()
	var n int64
	require.NoError(t, sup.db.Model(&persistence.CaptainEventModel{}).
		Where("type = ?", string(captain.EventMarketRegimeShift)).Count(&n).Error)
	return n
}

func ackAllUnprocessed(t *testing.T, s *captainStores, at time.Time) {
	t.Helper()
	left, err := s.store.FindUnprocessed(context.Background(), s.playerID, 50)
	require.NoError(t, err)
	ids := make([]int64, len(left))
	for i, e := range left {
		ids[i] = e.ID
	}
	if len(ids) > 0 {
		require.NoError(t, s.store.MarkProcessed(context.Background(), ids, at))
	}
}

// (1) A crossing rides the due cadence wake, is delivered, and the tripwire is
// consumed from the persisted policy — exactly one delivered wake.
func TestTripwire_OneShot_ConsumedOnFire(t *testing.T) {
	sup, s, gw := newBridgeSupervisor(t)
	now := time.Now()
	sup.lastSession = now.Add(-2 * time.Hour) // cadence overdue: the deferred crossing rides this wake
	statePath := NewWorkspace(s.dir).StatePath()
	seedOreCrossingAndTripwire(t, sup, s, now, 30*time.Minute)

	ran, err := sup.Tick(context.Background(), now)
	require.NoError(t, err)
	require.True(t, ran, "an armed tripwire crossing rides the due cadence wake")
	require.Equal(t, 1, mailsTo(gw, "captain"), "exactly one delivered wake")
	require.Contains(t, gw.mails[0][2], string(captain.EventMarketRegimeShift),
		"the delivered wake carries the price crossing")

	got, err := LoadRegimePolicy(statePath)
	require.NoError(t, err)
	require.Empty(t, got.Tripwires, "a fired one-shot tripwire is consumed from the persisted policy")
}

// (2) Past its Window, a consumed tripwire does NOT re-fire. RED today: the
// detector's Window cooldown expires and re-emits the still-holding crossing.
func TestTripwire_NoRefireAfterConsume(t *testing.T) {
	sup, s, gw := newBridgeSupervisor(t)
	now := time.Now()
	sup.lastSession = now.Add(-2 * time.Hour)
	seedOreCrossingAndTripwire(t, sup, s, now, 30*time.Minute)

	ran, err := sup.Tick(context.Background(), now)
	require.NoError(t, err)
	require.True(t, ran)
	require.Equal(t, 1, mailsTo(gw, "captain"))
	require.Equal(t, int64(1), countRegimeEvents(t, sup))

	// Captain handled the crossing; advance PAST the tripwire's Window with the
	// crossing still holding in the market and cadence due again — a re-fire
	// would deliver a second crossing.
	ackAllUnprocessed(t, s, now)
	later := now.Add(31 * time.Minute) // past the 30m Window
	sup.lastSession = later.Add(-2 * time.Hour)
	_, err = sup.Tick(context.Background(), later)
	require.NoError(t, err)

	require.Equal(t, int64(1), countRegimeEvents(t, sup),
		"a consumed one-shot tripwire must not re-fire just because its Window elapsed")
}

// (3) Consumption survives a restart: a brand-new Supervisor over the same
// db/store/workspace re-reads the persisted policy and the tripwire stays gone
// AND does not re-fire (RULINGS #2: consumption must persist + reload).
func TestTripwire_ConsumptionSurvivesRestart(t *testing.T) {
	sup, s, _ := newBridgeSupervisor(t)
	now := time.Now()
	sup.lastSession = now.Add(-2 * time.Hour)
	seedOreCrossingAndTripwire(t, sup, s, now, 30*time.Minute)

	ran, err := sup.Tick(context.Background(), now)
	require.NoError(t, err)
	require.True(t, ran)

	// Simulate a watchkeeper restart.
	sup2, _ := reopenBridgeSupervisor(t, sup.db, s.playerID, s.store, s.dir)

	got, err := LoadRegimePolicy(NewWorkspace(s.dir).StatePath())
	require.NoError(t, err)
	require.Empty(t, got.Tripwires, "a consumed tripwire must not be resurrected by a restart (RULINGS #2)")

	// The reopened supervisor does not re-fire it either.
	ackAllUnprocessed(t, s, now)
	later := now.Add(31 * time.Minute)
	sup2.lastSession = later.Add(-2 * time.Hour)
	_, err = sup2.Tick(context.Background(), later)
	require.NoError(t, err)
	require.Equal(t, int64(1), countRegimeEvents(t, sup),
		"a restart must not resurrect and re-fire a consumed tripwire")
}

// (4) Independence: multiple tripwires; firing one consumes only that one and
// leaves the others armed.
func TestTripwires_Independent(t *testing.T) {
	sup, s, _ := newBridgeSupervisor(t)
	now := time.Now()
	sup.lastSession = now.Add(-2 * time.Hour)
	statePath := NewWorkspace(s.dir).StatePath()

	// Only IRON_ORE has a market price, so only the ORE tripwire crosses; the
	// FUEL tripwire has no market to cross and must stay armed.
	require.NoError(t, sup.db.Create(&persistence.MarketData{
		WaypointSymbol: "X1-TEST-A1", GoodSymbol: "IRON_ORE", PlayerID: s.playerID,
		PurchasePrice: 100, SellPrice: 250, TradeVolume: 100, LastUpdated: now,
	}).Error)
	require.NoError(t, SaveRegimePolicy(statePath, RegimePolicy{Tripwires: []RegimeTripwire{
		{Good: "ORE", Direction: "bid-above", Threshold: regimeThresholdPtr(200), Window: 30 * time.Minute},
		{Good: "FUEL", Direction: "bid-above", Threshold: regimeThresholdPtr(200), Window: 30 * time.Minute},
	}}))

	ran, err := sup.Tick(context.Background(), now)
	require.NoError(t, err)
	require.True(t, ran)

	got, err := LoadRegimePolicy(statePath)
	require.NoError(t, err)
	require.Len(t, got.Tripwires, 1, "firing one tripwire consumes only that one")
	require.Equal(t, "FUEL", got.Tripwires[0].Good, "the unfired tripwire stays armed")
}

// (guard) Consume ONLY after a successful delivery (ran==true). A delivery
// outage must not silently burn the one-shot: the tripwire stays armed so the
// crossing still reaches the captain once the channel recovers (sp-wfut parity).
func TestTripwire_NotConsumedWhenDeliveryFails(t *testing.T) {
	sup, s, _ := newBridgeSupervisor(t)
	gw := &flakyGateway{fail: true}
	sup.gw = gw
	now := time.Now()
	sup.lastSession = now.Add(-2 * time.Hour) // cadence overdue
	statePath := NewWorkspace(s.dir).StatePath()
	seedOreCrossingAndTripwire(t, sup, s, now, 30*time.Minute)

	_, err := sup.Tick(context.Background(), now)
	require.Error(t, err, "the delivery failed")
	require.Positive(t, gw.attempts(), "a delivery was attempted")

	got, err := LoadRegimePolicy(statePath)
	require.NoError(t, err)
	require.Len(t, got.Tripwires, 1, "a tripwire must NOT be consumed when the wake never reached the captain")
}
