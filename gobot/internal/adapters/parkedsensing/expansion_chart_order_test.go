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

func (c *tourSeedCommander) Dock(_ context.Context, _ int, _ string) error { return nil }

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

// everyChartableWaypoint is the full work set the tour must cover: every one of
// tourFixture's nine uncharted waypoints, in sorted order. Nothing is exempt —
// a chart pays its own reward whatever the waypoint turns out to hold.
func everyChartableWaypoint() []string {
	return []string{
		"X1-T-A1", "X1-T-A2", "X1-T-A3", "X1-T-A4",
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
// assertion: each waypoint needs a navigate and a chart, plus one closing tick,
// so a tour that never ended would exhaust the budget here instead of hanging
// the suite.
func runTourToStandDown(t *testing.T, db *gorm.DB, ports appSensing.ExpandPorts) int {
	t.Helper()
	ctx := context.Background()
	for tick := 1; tick <= 40; tick++ {
		_, err := appSensing.AdvanceExpansion(ctx, ports, testPlayerID, appSensing.ExpandKnobs{
			SeedsEnabled: true, MinBudgetRate: 0.05, Whitelist: map[string]bool{"FUEL": true},
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
func TestReorderedTour_ChartsShipyardThenMarketsThenTheBarrenTier(t *testing.T) {
	db, _, commander, ports := tourFixture(t)

	ticks := runTourToStandDown(t, db, ports)

	// (a) EVERY UNCHARTED WAYPOINT IS CHARTED. This is the half that would catch
	// the ordering quietly becoming a filter — the order assertion below passes
	// either way, because a dropped tail is still a correctly ordered prefix.
	covered := append([]string(nil), commander.charted...)
	sort.Strings(covered)
	require.Equal(t, everyChartableWaypoint(), covered,
		"the tour is exhaustive: every uncharted waypoint pays a chart reward, so none may be left behind")

	// (b) and (c) THE SEQUENCE, across all four tiers. Shipyard type first, then
	// the market-bearing types a scanner can be parked on, then the unproven gas
	// giant, then the barren rock. The station is first despite sorting LAST
	// alphabetically, which is what a flat catalog order would have given us.
	require.Equal(t, []string{
		"X1-T-Z9", // ORBITAL_STATION — the shipyard-bearing tier
		"X1-T-B1", // MOON         \
		"X1-T-C1", // PLANET        > market-bearing: a scanner can sit here
		"X1-T-F1", // FUEL_STATION /  and start producing trade data
		"X1-T-D1", // GAS_GIANT — a market rarely, so ahead of the barren tier
		"X1-T-A1", // ASTEROID \
		"X1-T-A2", // ASTEROID  > barren: LAST, and still every one of them flown
		"X1-T-A3", // ASTEROID  /
		"X1-T-A4", // ASTEROID /
	}, commander.charted,
		"shipyard type before the markets, markets before the gas giant, and the barren tier last but not omitted")

	// (d) THE TOUR TERMINATES AND THE SEED STANDS DOWN. The errand is cleared and
	// the hull is parked as a spare, so it stays counted by the probe cap and can
	// be re-tasked.
	require.Positive(t, ticks)
	row := tourSystemRow(t, db, "X1-T")
	require.Nil(t, row.SeedShip, "the errand must be cleared")
	spare := slotRows(t, db, "X1-T-A4")
	require.Len(t, spare, 1, "the stood-down seed must hold a placement row or it drops out of the probe cap")
	require.Equal(t, appSensing.SlotKindSpare, spare[0].SlotKind)
	require.Equal(t, appSensing.SlotStateParked, spare[0].State)
	require.Equal(t, "PROBE-SEED", *spare[0].AssignedShip)

	// (e) THE COMPLETION SIGNAL REACHES ZERO ONLY WHEN NOTHING IS LEFT UNCHARTED.
	// The count and the work list are both computed by unchartedIn, so they agree
	// by construction; had a filter gone into one of them, this is where it would
	// show — either a tour that finished with the count stuck above zero, or a
	// count of zero with rows still dark.
	require.Equal(t, 0, row.UnchartedCount,
		"a non-zero count strands the system PENDING forever and stalls the frontier behind it")
	require.Empty(t, unchartedSymbols(t, db, "X1-T"),
		"the count reads zero because the system is charted, and nothing may be left dark behind it")
}

// A SEED STANDING ON AN UNCHARTED WAYPOINT CHARTS IT BEFORE FLYING ANYWHERE,
// barren or not, and then resumes in priority order.
//
// The tier is an ordering over the stops still to be REACHED; a stop already
// underfoot has no walk left to save, so deferring it would pay a second flight
// back for a chart already free. This is also the shape a hull re-tasked mid-tour
// arrives in, so it must not wedge.
func TestReorderedTour_ASeedStandingOnAnAsteroidTakesTheFreeChartFirst(t *testing.T) {
	db, ships, commander, ports := tourFixture(t)
	ships.at = "X1-T-A2" // mid-tour on an asteroid

	runTourToStandDown(t, db, ports)

	require.Equal(t, []string{
		"X1-T-A2", // the rock underfoot: charted where it stands, no flight owed
		"X1-T-Z9", // then the tier order resumes at the shipyard type
		"X1-T-B1",
		"X1-T-C1",
		"X1-T-F1",
		"X1-T-D1",
		"X1-T-A1", // and the remaining barren rock closes the tour
		"X1-T-A3",
		"X1-T-A4",
	}, commander.charted,
		"a free chart underfoot is taken immediately; the tier decides only where the hull FLIES next")

	covered := append([]string(nil), commander.charted...)
	sort.Strings(covered)
	require.Equal(t, everyChartableWaypoint(), covered,
		"and the rest of the map still finishes")
}

// AN ALL-BARREN SYSTEM IS TOURED IN FULL, and its seed is released only when the
// last rock is charted. A system's charting income does not depend on what its
// waypoints hold, so there is no shape of system that is finished while any of it
// is still dark.
//
// The failure this guards is the opposite pairing — a work list and a count that
// disagree — which either ends the tour with the system pinned PENDING forever or
// writes the system off with charts still owed.
func TestReorderedTour_AnAllAsteroidSystemIsTouredInFull(t *testing.T) {
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
			SeedsEnabled: true, MinBudgetRate: 0.05, Whitelist: map[string]bool{"FUEL": true},
		}, 1.0)
		require.NoError(t, err)
		require.NoError(t, db.Where("player_id = ? AND system_symbol = ?", testPlayerID, "X1-U").
			First(&row).Error)
		if row.SeedShip == nil {
			break
		}
	}

	charted := append([]string(nil), commander.charted...)
	sort.Strings(charted)
	require.Equal(t, []string{"X1-U-A1", "X1-U-A2", "X1-U-A3"}, charted,
		"every rock is a paid chart, so all three are flown")
	require.Nil(t, row.SeedShip,
		"the seed must reach a TERMINAL state and be RELEASED once the system is charted. A seed never released is a hull stranded and a system pinned PENDING forever")
	require.Equal(t, 0, row.UnchartedCount,
		"and the completion signal must agree, or verdictFor never writes the system off and the frontier stalls behind it")
	require.Empty(t, unchartedSymbols(t, db, "X1-U"),
		"completion means everything charted; a zero count over dark rows would be the count lying about the work")
}
