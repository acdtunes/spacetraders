package watchkeeper

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/marketscan"
)

// sp-k4z5b: the planner-staleness detector's boundary is the tour planner's OWN boundary,
// and the planner's stopped being a constant when the scan budget made it a function of
// map size. A detector holding a fixed 75 minutes reports fiction the moment the map
// outgrows it — and at 4,389 markets it would have woken the captain about four fifths of
// a perfectly healthy map on every single poll.

// The incident's live budget: 0.70 req/s shared across the charted map at the standing
// clamp of 8.
func incidentScanBudget() marketscan.Budget {
	return marketscan.Budget{RateReqPerSec: 0.70, ValueClampR: 8}
}

// Unwired, the detector keeps its documented floor — the pre-sp-k4z5b behaviour, so a
// watchkeeper that never calls SetMarketScanBudget is unchanged.
func TestPlannerStaleAge_UnwiredBudgetKeepsTheDocumentedFloor(t *testing.T) {
	db, playerID, _ := setupDB(t)
	cfg := stalenessConfig(playerID) // 75-minute floor, no MarketScanBudget

	require.Equal(t, 75*time.Minute, resolvedPlannerStaleAge(context.Background(), db, cfg, 4389))
}

// Wired, the boundary widens with the map — with nobody editing anything.
func TestPlannerStaleAge_WidensWithTheChartedMap(t *testing.T) {
	db, playerID, _ := setupDB(t)
	cfg := stalenessConfig(playerID)
	cfg.MarketScanBudget = incidentScanBudget()
	ctx := context.Background()

	small := resolvedPlannerStaleAge(ctx, db, cfg, 300)
	large := resolvedPlannerStaleAge(ctx, db, cfg, 4389)

	require.Equal(t, 75*time.Minute, small, "on a 300-market map the documented floor still binds")
	require.Greater(t, large, 75*time.Minute,
		"at 4,389 markets a 75-minute boundary calls a healthy map stale on every poll")
	require.Greater(t, large, 2*time.Hour, "a two-hour-old row is one rotation, not evidence of a coverage gap")
	require.Equal(t, marketscan.FreshnessCap(75*time.Minute, incidentScanBudget(), 4389), large,
		"the detector must resolve the SAME cap the tour planner does, not an approximation of it")
}

// ONE knob moves the planner and the detector watching it. The detector claims "the
// planner is dropping these lanes", so a tune that widened the planner while leaving the
// detector on its old floor would turn the detector into a liar.
func TestPlannerStaleAge_ReadsTheTunedTourFloor(t *testing.T) {
	db, playerID, _ := setupDB(t)
	cfg := stalenessConfig(playerID)
	cfg.MarketScanBudget = incidentScanBudget()
	ctx := context.Background()

	// A floor tuned ABOVE the rotation bound is what an operator reaches for mid-incident,
	// and the detector must follow it.
	seedTradeFleetCoordinator(t, db, playerID, "RUNNING", map[string]interface{}{
		plannerMarketDataFloorKey: 6000,
	})
	require.Equal(t, 6000*time.Minute, resolvedPlannerStaleAge(ctx, db, cfg, 4389))
}

// Every failure mode of that read leaves the documented floor standing rather than
// erroring a supervisor tick or silently widening the boundary: this is a detector
// threshold, and an unreadable row is not evidence of anything.
func TestPlannerStaleAge_UnreadableTourFloorFallsBackToTheDocumentedFloor(t *testing.T) {
	ctx := context.Background()
	expected := marketscan.FreshnessCap(75*time.Minute, incidentScanBudget(), 4389)

	cases := []struct {
		name   string
		status string
		config map[string]interface{}
		seed   bool
	}{
		{name: "no trade fleet coordinator running", seed: false},
		{name: "coordinator stopped", status: "STOPPED", config: map[string]interface{}{plannerMarketDataFloorKey: 6000}, seed: true},
		{name: "column carries no floor", status: "RUNNING", config: map[string]interface{}{"trade_fleet_tick_secs": 120}, seed: true},
		{name: "floor reverted to default", status: "RUNNING", config: map[string]interface{}{plannerMarketDataFloorKey: 0}, seed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, playerID, _ := setupDB(t)
			cfg := stalenessConfig(playerID)
			cfg.MarketScanBudget = incidentScanBudget()
			if tc.seed {
				seedTradeFleetCoordinator(t, db, playerID, tc.status, tc.config)
			}
			require.Equal(t, expected, resolvedPlannerStaleAge(ctx, db, cfg, 4389))
		})
	}
}

// End to end: the map that triggered the incident no longer wakes the captain. Twelve
// markets last scanned two hours ago in one market-rich system cleared the old 75-minute
// threshold and fired; against the live rotation two hours is one rotation and the system
// is simply waiting its turn.
func TestScoutStaleness_RotationExplainedMapDoesNotWakeTheCaptain(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	cfg := stalenessConfig(playerID)
	cfg.MarketScanBudget = incidentScanBudget()

	seedMarkets(t, db, playerID, "X1-XT71", 0, 12, now.Add(-2*time.Hour))
	// Pad the map out until the rotation itself cannot beat two hours. These carry fresh
	// reads and stay market-poor per system, so they never fire on their own — they exist
	// only to be the DENOMINATOR, which is the whole point: the boundary is an output of
	// budget / markets known.
	for s := 0; s < 125; s++ {
		seedMarkets(t, db, playerID, systemName(s), 0, 8, now.Add(-5*time.Minute))
	}
	require.Greater(t, resolvedPlannerStaleAge(context.Background(), db, cfg, 12+125*8), 2*time.Hour,
		"fixture must saturate past the two-hour reading, or this test proves nothing")

	require.NoError(t, detectScoutStaleness(context.Background(), db, store, cfg, now))
	require.Empty(t, eventsOfType(t, store, playerID, captain.EventStalenessHidingRevenue),
		"a system one rotation behind is not hiding revenue — it is waiting its turn")
}

// And the detector is still a detector: markets aged past what the rotation can explain
// still wake the captain, because at that point the scanner has failed its own guarantee.
func TestScoutStaleness_PastRotationBoundStillWakesTheCaptain(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	cfg := stalenessConfig(playerID)
	cfg.MarketScanBudget = incidentScanBudget()

	// 12 markets is a small map, so the rotation bound is short and the 75-minute floor
	// binds — a two-hour-old market-rich system is genuinely dark.
	seedMarkets(t, db, playerID, "X1-XT71", 0, 12, now.Add(-2*time.Hour))

	require.NoError(t, detectScoutStaleness(context.Background(), db, store, cfg, now))
	events := eventsOfType(t, store, playerID, captain.EventStalenessHidingRevenue)
	require.Len(t, events, 1, "genuinely dead market data must still wake the captain")
	require.Contains(t, events[0].Payload, `"stale_markets":12`)
	require.Contains(t, events[0].Payload, `"stale_age_minutes":75`,
		"the event must report the boundary actually applied, not a constant")
}

func systemName(i int) string {
	return "X1-PAD" + string(rune('A'+i/26)) + string(rune('A'+i%26))
}

func seedTradeFleetCoordinator(t *testing.T, db *gorm.DB, playerID int, status string, config map[string]interface{}) {
	t.Helper()
	raw, err := json.Marshal(config)
	require.NoError(t, err)
	now := time.Now()
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID:            "trade_fleet_coordinator-detector-test",
		PlayerID:      playerID,
		ContainerType: string(container.ContainerTypeTradeFleetCoordinator),
		CommandType:   "trade_fleet_coordinator",
		Status:        status,
		Config:        string(raw),
		StartedAt:     &now,
		HeartbeatAt:   &now,
	}).Error)
}
