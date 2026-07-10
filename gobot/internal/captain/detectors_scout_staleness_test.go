package watchkeeper

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

// seedMarkets creates `count` distinct priced-market waypoints (indices start..start+count-1)
// in `system`, each with the given most-recent scan time, so a test can compose a system out
// of a stale batch and a fresh batch without waypoint-key collisions.
func seedMarkets(t *testing.T, db *gorm.DB, playerID int, system string, start, count int, lastUpdated time.Time) {
	t.Helper()
	for i := start; i < start+count; i++ {
		wp := fmt.Sprintf("%s-M%02d", system, i)
		require.NoError(t, db.Create(&persistence.MarketData{
			WaypointSymbol: wp, GoodSymbol: "IRON_ORE", PurchasePrice: 10, SellPrice: 12,
			TradeVolume: 100, LastUpdated: lastUpdated, PlayerID: playerID,
		}).Error)
	}
}

func eventsOfType(t *testing.T, store captain.EventStore, playerID int, typ captain.EventType) []*captain.Event {
	t.Helper()
	all, err := store.FindUnprocessed(context.Background(), playerID, 100)
	require.NoError(t, err)
	var out []*captain.Event
	for _, e := range all {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func stalenessConfig(playerID int) DetectorConfig {
	return DetectorConfig{
		PlayerID:                        playerID,
		StalenessHidingStaleAge:         75 * time.Minute,
		StalenessHidingMinPricedMarkets: 10,
		StalenessHidingThreshold:        5,
		StalenessHidingCooldown:         3 * time.Hour,
	}
}

// TestStalenessHidingRevenue_FiresOnMarketRichStaleSystem is the load-bearing case: a
// market-rich system whose markets have aged past the tour cap fires exactly one
// scout.staleness_hiding_revenue naming the priced/stale counts — the XT71/UQ87 signal
// that previously alarmed nothing. A fresh rich system and a stale-but-poor system stay
// silent, and a second run does not duplicate (HasSince dedup).
func TestStalenessHidingRevenue_FiresOnMarketRichStaleSystem(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	stale := now.Add(-2 * time.Hour) // past the 75min cap
	fresh := now.Add(-5 * time.Minute)

	// X1-XT71: 12 priced markets, 8 stale + 4 fresh → priced 12 >= 10, stale 8 >= 5 → fires.
	seedMarkets(t, db, playerID, "X1-XT71", 0, 8, stale)
	seedMarkets(t, db, playerID, "X1-XT71", 8, 4, fresh)
	// X1-FRESH: 12 priced markets, all fresh → stale 0 < 5 → silent.
	seedMarkets(t, db, playerID, "X1-FRESH", 0, 12, fresh)
	// X1-POOR: 3 priced markets, all stale → priced 3 < 10 → silent (not market-rich).
	seedMarkets(t, db, playerID, "X1-POOR", 0, 3, stale)

	cfg := stalenessConfig(playerID)
	require.NoError(t, detectScoutStaleness(context.Background(), db, store, cfg, now))

	evs := eventsOfType(t, store, playerID, captain.EventStalenessHidingRevenue)
	require.Len(t, evs, 1)
	require.Equal(t, "X1-XT71", evs[0].Ship)
	require.Contains(t, evs[0].Payload, `"priced_markets":12`)
	require.Contains(t, evs[0].Payload, `"stale_markets":8`)

	// Re-run: the HasSince cooldown suppresses a duplicate for the same system.
	require.NoError(t, detectScoutStaleness(context.Background(), db, store, cfg, now))
	require.Len(t, eventsOfType(t, store, playerID, captain.EventStalenessHidingRevenue), 1)
}

// TestStalenessHidingRevenue_Disabled proves the zero-config gate: no scan, no event.
func TestStalenessHidingRevenue_Disabled(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	seedMarkets(t, db, playerID, "X1-XT71", 0, 12, now.Add(-2*time.Hour))

	// Both layers disabled (zero-value config) → detector is a no-op.
	require.NoError(t, detectScoutStaleness(context.Background(), db, store, DetectorConfig{PlayerID: playerID}, now))
	require.Empty(t, eventsOfType(t, store, playerID, captain.EventStalenessHidingRevenue))
}

func postProposalConfig(playerID int) DetectorConfig {
	return DetectorConfig{
		PlayerID:                     playerID,
		PostProposalMinPricedMarkets: 10,
		PostProposalFreshness:        60 * time.Minute,
		PostProposalAvgHop:           3 * time.Minute,
		PostProposalCooldown:         6 * time.Hour,
	}
}

// openEra creates an open (unclosed) era row and returns its id, so a layer-3 test can
// stamp scout posts into the era postedSystems scopes to.
func openEra(t *testing.T, db *gorm.DB, playerID int) int {
	t.Helper()
	era := persistence.EraModel{Name: fmt.Sprintf("era-%d", time.Now().UnixNano()), AgentSymbol: "AGT", PlayerID: playerID}
	require.NoError(t, db.Create(&era).Error)
	return era.EraID
}

// TestPostProposal_FiresOnMarketRichUnpostedSystem: a system priced past the threshold
// with NO post fires one scout.post_proposal whose hull count comes from the circuit math
// (25 markets → 2 probes, NOT a default of 1). A system that already has a post (standing
// or frontier sweep-once) is skipped, and a market-poor system is skipped.
func TestPostProposal_FiresOnMarketRichUnpostedSystem(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	fresh := now.Add(-5 * time.Minute)
	eraID := openEra(t, db, playerID)

	// X1-RICH: 25 priced markets, no post → proposal, required = ceil(25*3/60) = 2 hulls.
	seedMarkets(t, db, playerID, "X1-RICH", 0, 25, fresh)
	// X1-POSTED: 25 priced markets but already covered by a frontier sweep-once post → skip.
	seedMarkets(t, db, playerID, "X1-POSTED", 0, 25, fresh)
	require.NoError(t, db.Create(&persistence.ScoutPostModel{
		PlayerID: playerID, SystemSymbol: "X1-POSTED", Kind: "sweep_once", Hulls: 1,
		FreshnessTargetSeconds: 3600, EraID: &eraID, CreatedAt: now,
	}).Error)
	// X1-SMALL: 5 priced markets → below the market-rich threshold → skip.
	seedMarkets(t, db, playerID, "X1-SMALL", 0, 5, fresh)

	cfg := postProposalConfig(playerID)
	require.NoError(t, detectScoutStaleness(context.Background(), db, store, cfg, now))

	evs := eventsOfType(t, store, playerID, captain.EventScoutPostProposal)
	require.Len(t, evs, 1)
	require.Equal(t, "X1-RICH", evs[0].Ship)
	require.Contains(t, evs[0].Payload, `"priced_markets":25`)
	require.Contains(t, evs[0].Payload, `"proposed_hulls":2`)

	// Re-run: cooldown dedup holds.
	require.NoError(t, detectScoutStaleness(context.Background(), db, store, cfg, now))
	require.Len(t, eventsOfType(t, store, playerID, captain.EventScoutPostProposal), 1)
}

// TestPostProposal_SkipsPostedAcrossEra proves postedSystems' era scoping: a CLOSED-era
// post over a system must NOT suppress a live-era proposal (era close wipes market_data
// but leaves closed-era scout_posts behind), while an open-era post would.
func TestPostProposal_SkipsPostedAcrossEra(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	fresh := now.Add(-5 * time.Minute)

	// A CLOSED-era post over X1-RICH must NOT suppress the proposal — postedSystems scopes
	// to the open era only.
	closedEra := persistence.EraModel{Name: "era-closed", AgentSymbol: "AGT", PlayerID: playerID, ClosedAt: &now}
	require.NoError(t, db.Create(&closedEra).Error)
	openID := openEra(t, db, playerID)
	require.NoError(t, db.Create(&persistence.ScoutPostModel{
		PlayerID: playerID, SystemSymbol: "X1-RICH", Kind: "standing", Hulls: 1,
		FreshnessTargetSeconds: 3600, EraID: &closedEra.EraID, CreatedAt: now,
	}).Error)
	_ = openID
	seedMarkets(t, db, playerID, "X1-RICH", 0, 15, fresh)

	cfg := postProposalConfig(playerID)
	require.NoError(t, detectScoutStaleness(context.Background(), db, store, cfg, now))

	evs := eventsOfType(t, store, playerID, captain.EventScoutPostProposal)
	require.Len(t, evs, 1, "a closed-era post must not suppress a live-era proposal")
	require.Equal(t, "X1-RICH", evs[0].Ship)
}
