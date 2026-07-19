package watchkeeper

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

// --- sp-zlfv price-regime detector ---
//
// Captain-configurable tripwires emit a DEFERRED market.regime_shift event
// when a good-class or symbol's market price crosses a threshold.
// Edge-triggered with cooldown via HasSince (sp-1hak lesson): one event per
// crossing, not per poll. No tripwires configured means no scan at all (zero
// overhead when unset).

func regimeThresholdPtr(v int) *int          { return &v }
func regimeMultiplierPtr(v float64) *float64 { return &v }

func TestDetectRegimeShiftFiresOnAbsoluteThresholdCrossing(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	require.NoError(t, db.Create(&persistence.MarketData{
		WaypointSymbol: "X1-TEST-A1", GoodSymbol: "IRON_ORE", PlayerID: playerID,
		PurchasePrice: 100, SellPrice: 250, TradeVolume: 100, LastUpdated: now,
	}).Error)
	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: time.Hour, ShipIdle: time.Hour,
		RegimeTripwires: []RegimeTripwire{
			{Good: "ORE", Direction: "bid-above", Threshold: regimeThresholdPtr(200), Window: 30 * time.Minute},
		},
	}

	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))

	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, captain.EventMarketRegimeShift, events[0].Type)

	var payload struct {
		Good      string `json:"good"`
		Market    string `json:"market"`
		Price     int    `json:"price"`
		Baseline  int    `json:"baseline"`
		Threshold int    `json:"threshold"`
	}
	require.NoError(t, json.Unmarshal([]byte(events[0].Payload), &payload))
	require.Equal(t, "IRON_ORE", payload.Good)
	require.Equal(t, "X1-TEST-A1", payload.Market)
	require.Equal(t, 250, payload.Price)
	require.Equal(t, 200, payload.Threshold)
}

func TestDetectRegimeShiftDoesNotFireBelowThreshold(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	require.NoError(t, db.Create(&persistence.MarketData{
		WaypointSymbol: "X1-TEST-A1", GoodSymbol: "IRON_ORE", PlayerID: playerID,
		PurchasePrice: 100, SellPrice: 150, TradeVolume: 100, LastUpdated: now,
	}).Error)
	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: time.Hour, ShipIdle: time.Hour,
		RegimeTripwires: []RegimeTripwire{
			{Good: "ORE", Direction: "bid-above", Threshold: regimeThresholdPtr(200), Window: 30 * time.Minute},
		},
	}

	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))

	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Empty(t, events, "regime shift fired below threshold")
}

func TestDetectRegimeShiftSilentWithNoTripwiresConfigured(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	// A price that WOULD cross if any tripwire were configured.
	require.NoError(t, db.Create(&persistence.MarketData{
		WaypointSymbol: "X1-TEST-A1", GoodSymbol: "IRON_ORE", PlayerID: playerID,
		PurchasePrice: 100, SellPrice: 999, TradeVolume: 100, LastUpdated: now,
	}).Error)
	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: time.Hour, ShipIdle: time.Hour}

	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))

	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Empty(t, events, "regime detector fired with no tripwires configured")
}

// sp-a6e0: tripwires are one-shot, so the detector no longer applies a
// Window-based re-fire cooldown. It only suppresses a DUPLICATE while an
// identical crossing is still UNPROCESSED (the HasUnprocessed idiom the sibling
// credits-crossing detector uses) — NOT a time window. The true re-fire
// guarantee lives one level up, where the supervisor CONSUMES the tripwire on a
// delivered wake (see regime_oneshot_test.go). Window's only surviving role is
// the multiplier baseline lookback.
func TestDetectRegimeShiftDedupesWhileUnprocessedNotByTimeWindow(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	require.NoError(t, db.Create(&persistence.MarketData{
		WaypointSymbol: "X1-TEST-A1", GoodSymbol: "IRON_ORE", PlayerID: playerID,
		PurchasePrice: 100, SellPrice: 250, TradeVolume: 100, LastUpdated: now,
	}).Error)
	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: time.Hour, ShipIdle: time.Hour,
		RegimeTripwires: []RegimeTripwire{
			{Good: "ORE", Direction: "bid-above", Threshold: regimeThresholdPtr(200), Window: 30 * time.Minute},
		},
	}

	// First scan emits one crossing.
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))
	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)

	// While that crossing is still UNPROCESSED, a re-scan does not duplicate it.
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now.Add(time.Minute)))
	events, err = store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1, "no duplicate while the crossing is still pending delivery")

	// Once processed there is no pending duplicate to suppress, and — crucially
	// — WELL WITHIN the 30m Window — the still-crossing market re-emits: the
	// detector is not a time-window cooldown. (In production the tripwire would
	// already be consumed by then, so this re-scan would find nothing to fire.)
	require.NoError(t, store.MarkProcessed(context.Background(), []int64{events[0].ID}, now))
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now.Add(time.Minute)))
	events, err = store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1, "with no unprocessed duplicate to suppress, a still-crossing market re-emits (no Window cooldown)")
}

func TestDetectRegimeShiftBidBelowDirection(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	require.NoError(t, db.Create(&persistence.MarketData{
		WaypointSymbol: "X1-TEST-A1", GoodSymbol: "HYDROCARBON", PlayerID: playerID,
		PurchasePrice: 10, SellPrice: 5, TradeVolume: 100, LastUpdated: now,
	}).Error)
	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: time.Hour, ShipIdle: time.Hour,
		RegimeTripwires: []RegimeTripwire{
			{Good: "GAS", Direction: "bid-below", Threshold: regimeThresholdPtr(20), Window: 30 * time.Minute},
		},
	}

	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))

	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, captain.EventMarketRegimeShift, events[0].Type)
}

func TestDetectRegimeShiftMultiplierModeUsesRecordedBaseline(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	require.NoError(t, db.Create(&persistence.MarketData{
		WaypointSymbol: "X1-TEST-A1", GoodSymbol: "IRON_ORE", PlayerID: playerID,
		PurchasePrice: 100, SellPrice: 240, TradeVolume: 100, LastUpdated: now,
	}).Error)
	// Recorded baseline within the lookback window: 80/unit two hours ago.
	require.NoError(t, db.Create(&persistence.MarketPriceHistoryModel{
		WaypointSymbol: "X1-TEST-A1", GoodSymbol: "IRON_ORE", PlayerID: playerID,
		PurchasePrice: 70, SellPrice: 80, TradeVolume: 100, RecordedAt: now.Add(-2 * time.Hour),
	}).Error)
	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: time.Hour, ShipIdle: time.Hour,
		RegimeTripwires: []RegimeTripwire{
			// 3x the 80/unit baseline = 240: current price 240 crosses.
			{Good: "ORE", Direction: "bid-above", Multiplier: regimeMultiplierPtr(3.0), Window: 4 * time.Hour},
		},
	}

	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))

	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)

	var payload struct {
		Baseline  int `json:"baseline"`
		Threshold int `json:"threshold"`
	}
	require.NoError(t, json.Unmarshal([]byte(events[0].Payload), &payload))
	require.Equal(t, 80, payload.Baseline)
	require.Equal(t, 240, payload.Threshold)
}

func TestDetectRegimeShiftMultiplierModeSkipsWithoutBaseline(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	require.NoError(t, db.Create(&persistence.MarketData{
		WaypointSymbol: "X1-TEST-A1", GoodSymbol: "IRON_ORE", PlayerID: playerID,
		PurchasePrice: 100, SellPrice: 999, TradeVolume: 100, LastUpdated: now,
	}).Error)
	// No price history recorded: nothing to compare a multiplier against.
	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: time.Hour, ShipIdle: time.Hour,
		RegimeTripwires: []RegimeTripwire{
			{Good: "ORE", Direction: "bid-above", Multiplier: regimeMultiplierPtr(3.0), Window: 4 * time.Hour},
		},
	}

	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))

	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Empty(t, events, "multiplier tripwire fired with no recorded baseline")
}

// sp-a6e0: Window's ONLY surviving role after one-shot is the multiplier-mode
// baseline lookback. A price-history sample OUTSIDE the window must not be
// adopted as the baseline; the tripwire has nothing in-window to compare
// against, so it does not fire. Paired with
// TestDetectRegimeShiftMultiplierModeUsesRecordedBaseline (an in-window sample
// IS used), this pins Window as the baseline lookback and nothing else.
func TestDetectRegimeShiftMultiplierBaselineHonorsWindowLookback(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	require.NoError(t, db.Create(&persistence.MarketData{
		WaypointSymbol: "X1-TEST-A1", GoodSymbol: "IRON_ORE", PlayerID: playerID,
		PurchasePrice: 100, SellPrice: 240, TradeVolume: 100, LastUpdated: now,
	}).Error)
	// Only sample is 2h old — outside the 1h window below.
	require.NoError(t, db.Create(&persistence.MarketPriceHistoryModel{
		WaypointSymbol: "X1-TEST-A1", GoodSymbol: "IRON_ORE", PlayerID: playerID,
		PurchasePrice: 70, SellPrice: 80, TradeVolume: 100, RecordedAt: now.Add(-2 * time.Hour),
	}).Error)
	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: time.Hour, ShipIdle: time.Hour,
		RegimeTripwires: []RegimeTripwire{
			{Good: "ORE", Direction: "bid-above", Multiplier: regimeMultiplierPtr(3.0), Window: time.Hour},
		},
	}

	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))

	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Empty(t, events, "a baseline sample older than Window must not be used as the multiplier baseline")
}

func TestDetectRegimeShiftLiteralSymbolListMatchesExactGoodOnly(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	require.NoError(t, db.Create(&persistence.MarketData{
		WaypointSymbol: "X1-TEST-A1", GoodSymbol: "IRON_ORE", PlayerID: playerID,
		PurchasePrice: 100, SellPrice: 250, TradeVolume: 100, LastUpdated: now,
	}).Error)
	require.NoError(t, db.Create(&persistence.MarketData{
		WaypointSymbol: "X1-TEST-A1", GoodSymbol: "COPPER_ORE", PlayerID: playerID,
		PurchasePrice: 100, SellPrice: 250, TradeVolume: 100, LastUpdated: now,
	}).Error)
	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: time.Hour, ShipIdle: time.Hour,
		RegimeTripwires: []RegimeTripwire{
			{Good: "COPPER_ORE", Direction: "bid-above", Threshold: regimeThresholdPtr(200), Window: 30 * time.Minute},
		},
	}

	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))

	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1, "literal symbol tripwire must match only the named good, not the whole ORE class")
	var payload struct {
		Good string `json:"good"`
	}
	require.NoError(t, json.Unmarshal([]byte(events[0].Payload), &payload))
	require.Equal(t, "COPPER_ORE", payload.Good)
}

func TestDetectRegimeShiftNotClassifiedAsInterrupt(t *testing.T) {
	// sp-zlfv requires market.regime_shift to be DEFERRED class: it must
	// never appear in the default interrupt allowlist, since a price
	// crossing rides the next wake rather than forcing one.
	require.False(t, captain.IsInterrupt(captain.EventMarketRegimeShift, nil),
		"market.regime_shift must be deferred, not interrupt")
}
