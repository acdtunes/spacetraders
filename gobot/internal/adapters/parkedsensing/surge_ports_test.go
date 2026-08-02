package parkedsensing_test

// Integration tests (real GORM/sqlite, no mocks) for the sensing surge's pool read
// (sp-zvywu Part 1). The port is a SET DIFFERENCE expressed in SQL, so the only
// honest place to pin it is against a real database: the era predicate, the
// market_data anti-join and the trait decoding are all things a fake would simply
// agree with.
//
// The three properties that matter, and what each one costs when it is wrong:
//
//	era scoping    a dead-era system 404s on contact. sp-l0aqy billed ~290 API
//	               failures an hour for ten hours to exactly this table, read
//	               unscoped, while utilisation sat at 88% against an 85% ceiling.
//	priced-exit    a system that gains market_data must LEAVE the pool, or the surge
//	               re-sends probes to systems it has already covered, forever.
//	priceability   a system with no charted marketplace can never be priced by
//	               parking a probe, so ranking one consumes a dispatch slot a
//	               priceable system needed.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

const surgePlayerID = 1

// newSurgePoolDB returns a test DB with one OPEN era and one CLOSED one, which is the
// shape the era predicate has to discriminate against. Without a closed era present
// an era-scoping test cannot fail, whatever the predicate says.
func newSurgePoolDB(t *testing.T) (*gorm.DB, int, int) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	// OUR player, created FIRST so it owns id 1: market_data.player_id carries a foreign
	// key to players(id), and the harness enforces foreign keys by default (sp-55aa).
	// Creating it first also keeps the cross-player test honest — a second player
	// auto-incremented into OUR id would make that test pass by collision.
	require.NoError(t, db.Create(&persistence.PlayerModel{
		ID: surgePlayerID, AgentSymbol: "ORION", Token: "t", CreatedAt: time.Now().UTC(),
	}).Error)

	closedAt := time.Now().UTC().Add(-24 * time.Hour)
	dead := persistence.EraModel{Name: "vega", AgentSymbol: "VEGA", PlayerID: surgePlayerID, ClosedAt: &closedAt}
	require.NoError(t, db.Create(&dead).Error)
	open := persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: surgePlayerID}
	require.NoError(t, db.Create(&open).Error)
	require.NotEqual(t, dead.EraID, open.EraID, "the two eras must be distinguishable")

	return db, open.EraID, dead.EraID
}

// putWaypoint writes one waypoint row with the given traits, in the given era.
func putWaypoint(t *testing.T, db *gorm.DB, system, waypoint string, eraID int, traits string) {
	t.Helper()
	require.NoError(t, db.Create(&persistence.WaypointModel{
		WaypointSymbol: waypoint,
		SystemSymbol:   system,
		Type:           "PLANET",
		Traits:         traits,
		EraID:          &eraID,
	}).Error)
}

// putMarketRow prices one waypoint for the player — the fact that takes its system
// out of the pool.
func putMarketRow(t *testing.T, db *gorm.DB, waypoint string) {
	t.Helper()
	require.NoError(t, db.Create(&persistence.MarketData{
		WaypointSymbol: waypoint, GoodSymbol: "FOOD",
		PurchasePrice: 100, SellPrice: 90, TradeVolume: 10,
		LastUpdated: time.Now().UTC(), PlayerID: surgePlayerID,
	}).Error)
}

const (
	marketTraits    = `["MARKETPLACE"]`
	unchartedMarket = `["UNCHARTED","MARKETPLACE"]`
	barrenTraits    = `["BARREN"]`
)

// poolSystems reduces the port's answer to the symbols it named, for the tests whose
// subject is membership rather than shape.
func poolSystems(t *testing.T, db *gorm.DB) []string {
	t.Helper()
	pool, err := parkedsensing.NewUnpricedPoolPort(db).UnpricedSystems(context.Background(), surgePlayerID)
	require.NoError(t, err)
	systems := make([]string, 0, len(pool))
	for _, system := range pool {
		systems = append(systems, system.System)
	}
	return systems
}

// A DEAD-ERA SYSTEM IS NEVER IN THE POOL, AND DEAD-ERA WAYPOINTS NEVER INFLATE THE
// RANKING. Two reads stand behind the set difference and BOTH are era-scoped, so the
// fixture has to pin each predicate separately: scoping either one alone already
// produces the right MEMBERSHIP — the richness read drops the dead-era system from the
// candidate set, and the marketplace read drops its market — so a fixture that asserted
// membership alone leaves either predicate free to be deleted with nothing going red.
// The first version of this test did exactly that.
//
//	the MARKETPLACE read's predicate is what keeps X1-DEAD out. It is charted in the
//	OPEN era (its barren waypoint), it is unpriced, and it is therefore a candidate as
//	far as the richness read can tell — its only marketplace being dead-era is the
//	single fact excluding it.
//	the RICHNESS read's predicate is what keeps X1-LIVE's Waypoints honest. The system
//	is charted in both universes, and only the open-era row may be counted.
//
// sp-l0aqy is the bill for getting this call wrong on this table: ~290 API failures an
// hour for ten hours, because a dead-era system 404s the moment anything contacts it.
func TestUnpricedPool_ExcludesDeadEraSystemsAndDeadEraWaypoints(t *testing.T) {
	db, openEra, deadEra := newSurgePoolDB(t)
	putWaypoint(t, db, "X1-LIVE", "X1-LIVE-M1", openEra, marketTraits)
	putWaypoint(t, db, "X1-LIVE", "X1-LIVE-A9", deadEra, barrenTraits)
	putWaypoint(t, db, "X1-DEAD", "X1-DEAD-A1", openEra, barrenTraits)
	putWaypoint(t, db, "X1-DEAD", "X1-DEAD-M1", deadEra, marketTraits)

	pool, err := parkedsensing.NewUnpricedPoolPort(db).UnpricedSystems(context.Background(), surgePlayerID)
	require.NoError(t, err)

	require.Len(t, pool, 1,
		"a probe sent to a system whose only marketplace belongs to a closed universe 404s on contact")
	require.Equal(t, "X1-LIVE", pool[0].System)
	require.Equal(t, []string{"X1-LIVE-M1"}, pool[0].MarketWaypoints,
		"and the dead-era marketplace is not offered as a target either")
	require.Equal(t, 1, pool[0].Waypoints,
		"richness counts OPEN-era waypoint rows only — a dead-era row must not inflate the ranking")
}

// AN UNRESOLVABLE ERA REFUSES RATHER THAN ANSWERING. This is the difference between
// persistence.OpenEraScope and the openEraID + eraScopePredicate pair beside it: the
// latter's nil path resolves to `era_id IS NULL`, which still hands back the
// pre-backfill rows. For a reader whose output becomes flight plans, answering from
// the wrong universe is worse than not answering.
func TestUnpricedPool_RefusesWhenTheOpenEraCannotBeResolved(t *testing.T) {
	db, _, deadEra := newSurgePoolDB(t)
	putWaypoint(t, db, "X1-LIVE", "X1-LIVE-M1", deadEra, marketTraits)
	// Close every era, so there is no open universe to scope to.
	require.NoError(t, db.Model(&persistence.EraModel{}).
		Where("closed_at IS NULL").
		Update("closed_at", time.Now().UTC()).Error)

	pool, err := parkedsensing.NewUnpricedPoolPort(db).UnpricedSystems(context.Background(), surgePlayerID)

	require.Error(t, err, "an unresolvable era must refuse, not fall back to the pre-backfill rows")
	require.Empty(t, pool, "and hand back nothing for the surge to act on")
}

// A SYSTEM THAT GAINS market_data LEAVES THE POOL. This is the pass's own termination
// condition: a probe arrives, scans, and the system it was sent to stops being a
// candidate — so the surge converges instead of re-sending hulls to covered systems.
//
// ONE PRICED WAYPOINT IS ENOUGH, and that is deliberate rather than incidental: the
// bead's own measurement is "systems with ANY market_data", so a system with three
// markets and one of them priced is already generating the planner data the relocator
// scores a region on. Requiring every market to be priced would keep the fleet
// re-sending probes to systems it has already reached.
func TestUnpricedPool_ASystemThatGainsMarketDataLeavesThePool(t *testing.T) {
	db, openEra, _ := newSurgePoolDB(t)
	putWaypoint(t, db, "X1-DARK", "X1-DARK-M1", openEra, marketTraits)
	putWaypoint(t, db, "X1-LIT", "X1-LIT-M1", openEra, marketTraits)
	putWaypoint(t, db, "X1-LIT", "X1-LIT-M2", openEra, marketTraits)

	require.ElementsMatch(t, []string{"X1-DARK", "X1-LIT"}, poolSystems(t, db),
		"precondition: both systems are charted, unpriced candidates")

	putMarketRow(t, db, "X1-LIT-M1")

	require.Equal(t, []string{"X1-DARK"}, poolSystems(t, db),
		"the scanned system is out of the pool, even though its second market is still unpriced")
}

// ANOTHER PLAYER'S PRICES DO NOT COVER OURS. market_data is player-partitioned, and
// reading it unpartitioned would silently hide systems from every player but the one
// that happened to scan first.
func TestUnpricedPool_IsScopedToThePlayersOwnPrices(t *testing.T) {
	db, openEra, _ := newSurgePoolDB(t)
	putWaypoint(t, db, "X1-SHARED", "X1-SHARED-M1", openEra, marketTraits)
	other := persistence.PlayerModel{AgentSymbol: "OTHER", Token: "t", CreatedAt: time.Now().UTC()}
	require.NoError(t, db.Create(&other).Error)
	require.NotEqual(t, surgePlayerID, other.ID, "the two players must be distinguishable")
	require.NoError(t, db.Create(&persistence.MarketData{
		WaypointSymbol: "X1-SHARED-M1", GoodSymbol: "FOOD",
		PurchasePrice: 100, SellPrice: 90, TradeVolume: 10,
		LastUpdated: time.Now().UTC(), PlayerID: other.ID,
	}).Error)

	require.Equal(t, []string{"X1-SHARED"}, poolSystems(t, db),
		"our fleet still holds no price for this system, whoever else does")
}

// ONLY PRICEABLE SYSTEMS ARE OFFERED, and the port carries the evidence the ranking
// needs with them.
//
// Three exclusions, each for its own reason: a system with no marketplace at all can
// never be priced by parking a probe; an UNCHARTED marketplace's trait list is not
// evidence of a market, because traits are unknown until somebody charts the
// waypoint; and a traits column that cannot be decoded is evidence of nothing.
func TestUnpricedPool_OffersOnlyPriceableSystemsWithTheirRankingInputs(t *testing.T) {
	db, openEra, _ := newSurgePoolDB(t)
	// Two charted markets and two other waypoints: the shape the ranking reads.
	putWaypoint(t, db, "X1-RICH", "X1-RICH-M1", openEra, marketTraits)
	putWaypoint(t, db, "X1-RICH", "X1-RICH-M2", openEra, marketTraits)
	putWaypoint(t, db, "X1-RICH", "X1-RICH-A1", openEra, barrenTraits)
	putWaypoint(t, db, "X1-RICH", "X1-RICH-A2", openEra, barrenTraits)
	// Excluded: no marketplace anywhere in it.
	putWaypoint(t, db, "X1-BARREN", "X1-BARREN-A1", openEra, barrenTraits)
	// Excluded: its only marketplace is UNCHARTED, so the trait says nothing.
	putWaypoint(t, db, "X1-UNCHARTED", "X1-UNCHARTED-M1", openEra, unchartedMarket)
	// Excluded: the traits column cannot be decoded, so it is not evidence of a market.
	putWaypoint(t, db, "X1-CORRUPT", "X1-CORRUPT-M1", openEra, `["MARKETPLACE"`)

	pool, err := parkedsensing.NewUnpricedPoolPort(db).UnpricedSystems(context.Background(), surgePlayerID)
	require.NoError(t, err)

	require.Len(t, pool, 1, "only the system a probe could actually price is offered")
	require.Equal(t, "X1-RICH", pool[0].System)
	require.Equal(t, []string{"X1-RICH-M1", "X1-RICH-M2"}, pool[0].MarketWaypoints,
		"both charted markets, in a deterministic order — the surge picks the first free one")
	require.Equal(t, 4, pool[0].Waypoints,
		"richness counts EVERY open-era waypoint row, not just the markets")
}

// THE ORDER IS STABLE ACROSS READS. The surge ranks on top of this, and a store that
// returned a different order each time would make the surge's own ranking
// irreproducible however total its comparator was.
func TestUnpricedPool_ReturnsAStableOrderAcrossReads(t *testing.T) {
	db, openEra, _ := newSurgePoolDB(t)
	for i := 0; i < 12; i++ {
		system := fmt.Sprintf("X1-S%02d", i)
		putWaypoint(t, db, system, system+"-M2", openEra, marketTraits)
		putWaypoint(t, db, system, system+"-M1", openEra, marketTraits)
	}

	first := poolSystems(t, db)
	require.Len(t, first, 12)
	require.Equal(t, first, poolSystems(t, db), "the same store answers identically")

	pool, err := parkedsensing.NewUnpricedPoolPort(db).UnpricedSystems(context.Background(), surgePlayerID)
	require.NoError(t, err)
	require.Equal(t, []string{"X1-S00-M1", "X1-S00-M2"}, pool[0].MarketWaypoints,
		"marketplaces come back sorted, not in insertion order")
}
