package parkedsensing_test

// THE REORDERED TOUR, driven end to end over consecutive ticks.
//
// WHY THIS FILE EXISTS SEPARATELY FROM THE ENGINE'S OWN TESTS. The expansion
// engine is tested against a fake UnchartedCatalog that hands back a canned
// list, so against that fake the ordering is simply assumed. The claims here are
// claims about the REAL catalog read and about what the engine does with it over
// TIME, so this file wires the real stack:
//
//	AdvanceExpansion -> WaypointCatalogPort -> waypoints table   (the ordering)
//	                 -> LedgerPort          -> sensing_systems   (the completion signal)
//
// A charting seed is then stepped tick by tick against those two tables until it
// stands down, exactly as the daemon steps it.
//
// WHAT IS AND IS NOT BEING CHANGED. The tour is still EXHAUSTIVE: every waypoint
// is charted, asteroids included, and uncharted_count still falls to zero the
// same way it always did. Only the SEQUENCE changes. That is why every test
// below asserts completeness alongside order — an ordering that quietly became a
// filter would look like a speedup and would permanently blind the fleet to the
// rest of the map.

import (
	"context"
	"encoding/json"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	adapterSensing "github.com/andrescamacho/spacetraders-go/internal/adapters/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	appSensing "github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// tourShips is the ships table as the engine reads it: one hull, whose position
// MOVES when the engine navigates it. A fixed position could not express a tour
// at all.
type tourShips struct{ at string }

func (s *tourShips) ShipAt(_ context.Context, _ int, _ string) (appSensing.ShipPos, error) {
	return appSensing.ShipPos{Waypoint: s.at, NavStatus: navigation.NavStatusInOrbit, Found: true}, nil
}

func (s *tourShips) DockedProbeAt(_ context.Context, _ int, _ string) (string, bool, error) {
	return "", false, nil
}

// No hull is lendable and no counter is staffed by one: this fixture is about the
// tour's ORDER, and an available borrow would add a dispatch the ordering
// assertions would have to account for.
func (s *tourShips) DockedBuyerAt(_ context.Context, _ int, _ string) (string, bool, error) {
	return "", false, nil
}

func (s *tourShips) LendableHulls(_ context.Context, _ int, _ int) ([]appSensing.LendableHull, error) {
	return nil, nil
}

// tourSeedCommander is the flying half, and it WRITES BACK. Charting a waypoint
// clears its UNCHARTED trait in the waypoints table, which is what lets the next
// tick's catalog read see the tour advance. A commander that only recorded calls
// would leave the tour running forever and prove nothing about termination.
type tourSeedCommander struct {
	db    *gorm.DB
	ships *tourShips
	// visited is every waypoint the hull was ever SENT to; charted is every
	// waypoint it actually charted. They differ — the hull charts the waypoint
	// under its feet without being navigated there — and the distinction is what
	// makes "nothing was skipped" checkable.
	visited []string
	charted []string
}

func (c *tourSeedCommander) NavigateTo(_ context.Context, _ int, _, waypoint string) error {
	c.visited = append(c.visited, waypoint)
	c.ships.at = waypoint
	return nil
}

func (c *tourSeedCommander) Chart(_ context.Context, _ int, _ string) error {
	c.charted = append(c.charted, c.ships.at)
	return nil
}

// RefreshWaypoint re-reads the waypoint and persists what charting revealed —
// here, that it is no longer uncharted.
func (c *tourSeedCommander) RefreshWaypoint(ctx context.Context, _ int, _, waypoint string) (bool, error) {
	traits, _ := json.Marshal([]string{"BARREN"})
	err := c.db.WithContext(ctx).Model(&persistence.WaypointModel{}).
		Where("waypoint_symbol = ?", waypoint).
		Update("traits", string(traits)).Error
	return false, err
}

func (c *tourSeedCommander) JumpTo(_ context.Context, _ int, _, _, _ string) error { return nil }
func (c *tourSeedCommander) ReadMarketAt(_ context.Context, _ int, _ string) error { return nil }
func (c *tourSeedCommander) SyncWaypoints(_ context.Context, _ int, _ string) error {
	return nil
}

// tourScreen mirrors what the real screen does to the column under test: it
// re-reads the uncharted count through the SAME port the tour reads its work
// list from, and writes it to sensing_systems. Stubbing the count out would
// leave the completion signal untested, and the completion signal is what
// releases the hull.
func tourScreen(db *gorm.DB, catalog *adapterSensing.WaypointCatalogPort) appSensing.SystemScreener {
	repo := persistence.NewSensingLedgerRepository(db)
	return func(ctx context.Context, system string) (appSensing.ScreenResult, error) {
		count, err := catalog.ListUnchartedCount(ctx, system)
		if err != nil {
			return appSensing.ScreenResult{}, err
		}
		if err := repo.UpsertSystem(ctx, persistence.SensingSystemModel{
			PlayerID: testPlayerID, SystemSymbol: system,
			Verdict: appSensing.VerdictPending, UnchartedCount: count,
		}); err != nil {
			return appSensing.ScreenResult{}, err
		}
		return appSensing.ScreenResult{Verdict: appSensing.VerdictPending}, nil
	}
}

// tourSystemRow reads sensing_systems directly rather than through the
// repository, so an assertion about the stored errand cannot be filtered by the
// code under test.
func tourSystemRow(t *testing.T, db *gorm.DB, system string) persistence.SensingSystemModel {
	t.Helper()
	var row persistence.SensingSystemModel
	require.NoError(t, db.Where("player_id = ? AND system_symbol = ?", testPlayerID, system).
		First(&row).Error)
	return row
}

// unchartedSymbols reads the waypoints table for rows STILL carrying the
// UNCHARTED trait — the ground truth behind the ledger's count.
func unchartedSymbols(t *testing.T, db *gorm.DB, system string) []string {
	t.Helper()
	var rows []persistence.WaypointModel
	require.NoError(t, db.Where("system_symbol = ? AND traits LIKE ?", system, "%UNCHARTED%").
		Order("waypoint_symbol").Find(&rows).Error)
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.WaypointSymbol)
	}
	return out
}

// everyUnchartedWaypoint is the full work set the tour must cover — the nine
// uncharted waypoints of tourFixture, in no particular order.
// everyChartableWaypoint is the fixture's uncharted set MINUS the barren tier
// (sp-erdz7). The four asteroids are still ROWS in the system — they are simply
// no longer charting work, on a census of 0 markets and 0 shipyards in 114,838
// charted asteroids across two universes.
func everyChartableWaypoint() []string {
	return []string{
		"X1-T-B1", "X1-T-C1", "X1-T-D1", "X1-T-F1", "X1-T-Z9",
	}
}

// tourFixture builds a system with the live shape and ALL FOUR TIERS present:
// mostly asteroids, one orbital station, a moon, a planet, a gas giant, and the
// lone fuel station that is the live X1-AJ10 case — TORWIND-18 sat on an
// asteroid there with 54 asteroids and one FUEL_STATION left, and a FUEL_STATION
// is 1129-for-1129 a market.
//
// EVERY TIER IS REPRESENTED ON PURPOSE. A fixture missing the gas giant could
// not distinguish "market types come before the unproven ones" from "market
// types come before asteroids", and would pass either way.
//
// THE STATION SORTS LAST ALPHABETICALLY (Z9). Under the old order it was the
// final stop of a nine-waypoint tour; it must now be the first. A fixture whose
// station happened to sort early could not tell the two rules apart.
func tourFixture(t *testing.T) (*gorm.DB, *tourShips, *tourSeedCommander, appSensing.ExpandPorts) {
	t.Helper()
	db := newShipPortsDB(t)
	uncharted := []string{"UNCHARTED"}
	require.NoError(t, db.Create(&[]persistence.WaypointModel{
		typedWaypointRow("X1-T-A1", "X1-T", "ASTEROID", uncharted),
		typedWaypointRow("X1-T-A2", "X1-T", "ASTEROID", uncharted),
		typedWaypointRow("X1-T-A3", "X1-T", "ASTEROID", uncharted),
		typedWaypointRow("X1-T-A4", "X1-T", "ASTEROID", uncharted),
		typedWaypointRow("X1-T-B1", "X1-T", "MOON", uncharted),
		typedWaypointRow("X1-T-C1", "X1-T", "PLANET", uncharted),
		typedWaypointRow("X1-T-D1", "X1-T", "GAS_GIANT", uncharted),
		typedWaypointRow("X1-T-F1", "X1-T", "FUEL_STATION", uncharted),
		typedWaypointRow("X1-T-Z9", "X1-T", "ORBITAL_STATION", uncharted),
		typedWaypointRow("X1-T-GATE", "X1-T", "JUMP_GATE", []string{"MARKETPLACE"}),
	}).Error)

	repo := persistence.NewSensingLedgerRepository(db)
	require.NoError(t, repo.UpsertSystem(context.Background(), persistence.SensingSystemModel{
		PlayerID: testPlayerID, SystemSymbol: "X1-T",
		Verdict: appSensing.VerdictPending, UnchartedCount: 9,
	}))
	require.NoError(t, repo.SetSeed(
		context.Background(), testPlayerID, "X1-T", "PROBE-SEED", appSensing.SeedStateCharting))

	ships := &tourShips{at: "X1-T-GATE"}
	commander := &tourSeedCommander{db: db, ships: ships}
	catalog := adapterSensing.NewWaypointCatalogPort(persistence.NewGormWaypointRepository(db), db, testPlayerID)

	return db, ships, commander, appSensing.ExpandPorts{
		Gates:       keyTestGates{adjacency: map[string][]string{}},
		Ledger:      adapterSensing.NewLedgerPort(repo),
		Screen:      tourScreen(db, catalog),
		SeedShip:    commander,
		Ships:       ships,
		MarketGoods: keyTestMarkets{},
		Yards:       keyTestYards{bySystem: map[string][]string{}},
		Uncharted:   catalog,
	}
}

// runTourToStandDown steps the engine until the errand is cleared, and fails
// rather than looping forever if it never is. The bound IS the termination
// assertion: eight waypoints need a navigate and a chart each plus one closing
// tick, so a tour that never ended would exhaust the budget here instead of
// hanging the suite.
func runTourToStandDown(t *testing.T, db *gorm.DB, ports appSensing.ExpandPorts) int {
	t.Helper()
	ctx := context.Background()
	for tick := 1; tick <= 40; tick++ {
		_, err := appSensing.AdvanceExpansion(ctx, ports, testPlayerID, appSensing.ExpandKnobs{
			SpendEnabled: true, MinBudgetRate: 0.05, Whitelist: map[string]bool{"FUEL": true},
		}, 1.0)
		require.NoError(t, err)
		if tourSystemRow(t, db, "X1-T").SeedShip == nil {
			return tick
		}
	}
	t.Fatal("the charting seed never stood down: the tour did not terminate, which strands the hull and pins the system PENDING forever")
	return 0
}

// THE HEADLINE. One reordered tour, start to stand-down.
func TestReorderedTour_ChartsShipyardThenMarketsAndSkipsTheAsteroidsEntirely(t *testing.T) {
	db, _, commander, ports := tourFixture(t)

	ticks := runTourToStandDown(t, db, ports)

	// (a) EVERYTHING WORTH CHARTING IS CHARTED, AND NOTHING ELSE. The four
	// asteroids are gone from the tour; every other uncharted waypoint is still
	// flown. This is the half that would catch the skip widening past the barren
	// tier — the order assertion below passes either way.
	covered := append([]string(nil), commander.charted...)
	sort.Strings(covered)
	require.Equal(t, everyChartableWaypoint(), covered,
		"only the barren tier may be skipped — every market- and shipyard-bearing type is still charted")

	// (b) and (c) THE SEQUENCE, across all four tiers. Shipyard type first, then
	// the market-bearing types a scanner can be parked on, then the unproven gas
	// giant, then the barren rock. The station is first despite sorting LAST
	// alphabetically — which is what the old order gave us, and the reason a seed
	// could burn dozens of hours before revealing a market.
	require.Equal(t, []string{
		"X1-T-Z9", // ORBITAL_STATION — 523 of the 567 known shipyards
		"X1-T-B1", // MOON         \
		"X1-T-C1", // PLANET        > market-bearing: a scanner can sit here
		"X1-T-F1", // FUEL_STATION /  and start producing trade data
		"X1-T-D1", // GAS_GIANT — 133 of 1154, rare not never, so still flown LAST
	}, commander.charted,
		"shipyard type before the markets, markets before the gas giant, and no asteroid flown at all")

	// (d) THE TOUR TERMINATES AND THE SEED STANDS DOWN. The errand is cleared and
	// the hull is parked as a spare, so it stays counted by the probe cap and can
	// be re-tasked.
	require.Positive(t, ticks)
	row := tourSystemRow(t, db, "X1-T")
	require.Nil(t, row.SeedShip, "the errand must be cleared")
	spare := slotRows(t, db, "X1-T-D1")
	require.Len(t, spare, 1, "the stood-down seed must hold a placement row or it drops out of the probe cap")
	require.Equal(t, appSensing.SlotKindSpare, spare[0].SlotKind)
	require.Equal(t, appSensing.SlotStateParked, spare[0].State)
	require.Equal(t, "PROBE-SEED", *spare[0].AssignedShip)

	// (e) THE COMPLETION SIGNAL REACHES ZERO WHILE BARREN ROWS REMAIN UNCHARTED,
	// and that pairing is the whole of sp-erdz7's risk.
	//
	// The four asteroids are STILL uncharted rows in the database — the fix does
	// not chart them and does not pretend to. What changed is that they are no
	// longer outstanding WORK, and because the count and the work list are both
	// computed by unchartedIn they agree about that. Had the filter gone in only
	// one of them, this is precisely where it would show: the tour would finish
	// with UnchartedCount stuck at 4, verdictFor would never write the system off,
	// seedlessTargets would keep sending probes, and the frontier would stall here
	// permanently.
	require.Equal(t, 0, row.UnchartedCount,
		"a non-zero count strands the system PENDING forever and stalls the frontier behind it")
	require.ElementsMatch(t, []string{"X1-T-A1", "X1-T-A2", "X1-T-A3", "X1-T-A4"},
		unchartedSymbols(t, db, "X1-T"),
		"the asteroids remain uncharted ROWS — deliberately. The count is zero because they are not work, not because they were charted")
}

// A seed standing ON an uncharted waypoint charts it before flying anywhere,
// even a barren one — and then resumes in priority order.
//
// This is the DEPLOY-DAY shape and it is reachable no other way: the live fleet
// has seeds parked on asteroids right now (TORWIND-18 on X1-AJ10-B26B), left
// there by the old exhaustive order. Those hulls must not wedge — they must
// recognise there is nothing to do underfoot and get on with the system.
func TestReorderedTour_ASeedLeftStandingOnAnAsteroidLeavesWithoutChartingIt(t *testing.T) {
	db, ships, commander, ports := tourFixture(t)
	ships.at = "X1-T-A2" // mid-tour on an asteroid, as the old order would have left it

	runTourToStandDown(t, db, ports)

	require.Equal(t, []string{
		"X1-T-Z9", // straight to the shipyard type — the rock underfoot is not work
		"X1-T-B1",
		"X1-T-C1",
		"X1-T-F1",
		"X1-T-D1", // gas giant behind the markets, and the tour ends there
	}, commander.charted,
		"the seed must leave the asteroid uncharted and fly to the station")

	require.NotContains(t, commander.charted, "X1-T-A2",
		"THE SKIP HOLDS EVEN WHEN THE CHART IS FREE, and that consistency is the point. The engine takes its next stop from the same work list the count is computed over, so a waypoint that is not work is not charted — even standing on it. Charting it here would put a waypoint in the charted set that the completion signal never tracked, which is the divergence this whole design avoids")

	covered := append([]string(nil), commander.charted...)
	sort.Strings(covered)
	require.Equal(t, everyChartableWaypoint(), covered,
		"and the rest of the map still finishes")
}

// THE ACCEPTANCE CRITERION, END TO END (sp-erdz7): a system whose only remaining
// waypoints are barren reaches a terminal state and releases its seed WITHOUT
// flying anywhere.
//
// This is the live shape of X1-KC84 (51 asteroids) and five others. Before this
// change each was ~50 hours of charting at 1.1 waypoints/hr to discover nothing;
// now the seed stands down on the first tick. The danger it is guarding is the
// opposite failure — a narrowed work list with an un-narrowed count, which ends
// the tour while pinning the system PENDING forever.
func TestReorderedTour_AnAllAsteroidSystemIsReleasedWithoutFlyingAnywhere(t *testing.T) {
	db := newShipPortsDB(t)
	uncharted := []string{"UNCHARTED"}
	require.NoError(t, db.Create(&[]persistence.WaypointModel{
		typedWaypointRow("X1-U-A1", "X1-U", "ASTEROID", uncharted),
		typedWaypointRow("X1-U-A2", "X1-U", "ASTEROID", uncharted),
		typedWaypointRow("X1-U-A3", "X1-U", "ASTEROID", uncharted),
		typedWaypointRow("X1-U-GATE", "X1-U", "JUMP_GATE", []string{"MARKETPLACE"}),
	}).Error)
	repo := persistence.NewSensingLedgerRepository(db)
	ctx := context.Background()
	require.NoError(t, repo.UpsertSystem(ctx, persistence.SensingSystemModel{
		PlayerID: testPlayerID, SystemSymbol: "X1-U",
		Verdict: appSensing.VerdictPending, UnchartedCount: 3,
	}))
	require.NoError(t, repo.SetSeed(ctx, testPlayerID, "X1-U", "PROBE-SEED", appSensing.SeedStateCharting))

	ships := &tourShips{at: "X1-U-GATE"}
	commander := &tourSeedCommander{db: db, ships: ships}
	catalog := adapterSensing.NewWaypointCatalogPort(persistence.NewGormWaypointRepository(db), db, testPlayerID)
	ports := appSensing.ExpandPorts{
		Gates:       keyTestGates{adjacency: map[string][]string{}},
		Ledger:      adapterSensing.NewLedgerPort(repo),
		Screen:      tourScreen(db, catalog),
		SeedShip:    commander,
		Ships:       ships,
		MarketGoods: keyTestMarkets{},
		Yards:       keyTestYards{bySystem: map[string][]string{}},
		Uncharted:   catalog,
	}

	var row persistence.SensingSystemModel
	for tick := 1; tick <= 40; tick++ {
		_, err := appSensing.AdvanceExpansion(ctx, ports, testPlayerID, appSensing.ExpandKnobs{
			SpendEnabled: true, MinBudgetRate: 0.05, Whitelist: map[string]bool{"FUEL": true},
		}, 1.0)
		require.NoError(t, err)
		require.NoError(t, db.Where("player_id = ? AND system_symbol = ?", testPlayerID, "X1-U").
			First(&row).Error)
		if row.SeedShip == nil {
			break
		}
	}

	require.Empty(t, commander.charted,
		"not one asteroid is flown: this system's entire remaining work is barren, and ~50 hours of charting at 1.1 waypoints/hr would have revealed nothing")
	require.Nil(t, row.SeedShip,
		"THE ACCEPTANCE CRITERION: a system whose only remaining waypoints are skipped must reach a TERMINAL state and RELEASE its seed. A seed never released is a hull stranded and a system pinned PENDING forever")
	require.Equal(t, 0, row.UnchartedCount,
		"and the completion signal must agree, or verdictFor never writes the system off and the frontier stalls behind it")
	require.ElementsMatch(t, []string{"X1-U-A1", "X1-U-A2", "X1-U-A3"},
		unchartedSymbols(t, db, "X1-U"),
		"the rows are still uncharted and that is deliberate — completion here means 'nothing worth flying to', not 'everything charted'")
}
