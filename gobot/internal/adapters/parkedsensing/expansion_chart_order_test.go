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
func everyUnchartedWaypoint() []string {
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
func TestReorderedTour_ChartsShipyardThenMarketsThenAsteroidsAndCoversEverything(t *testing.T) {
	db, _, commander, ports := tourFixture(t)

	ticks := runTourToStandDown(t, db, ports)

	// (a) NOTHING IS SKIPPED. Every uncharted waypoint in the system is charted,
	// asteroids included. This is the safety half: an ordering that became a
	// filter would still pass the order assertion below.
	covered := append([]string(nil), commander.charted...)
	sort.Strings(covered)
	require.Equal(t, everyUnchartedWaypoint(), covered,
		"the tour must remain EXHAUSTIVE — every waypoint charted, asteroids included")

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
		"X1-T-D1", // GAS_GIANT — 72 of 546, unproven, so behind the markets
		"X1-T-A1", // ASTEROID \
		"X1-T-A2", // ASTEROID  > 0 of 3297, charted last
		"X1-T-A3", // ASTEROID /
		"X1-T-A4",
	}, commander.charted,
		"shipyard type before the markets, markets before the gas giant, and everything before the asteroids")

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

	// (e) THE COMPLETION SIGNAL REACHES ZERO, and it does so honestly: no
	// waypoint in the system is still uncharted. Reordering leaves this exactly
	// as it was, which is the whole reason it is a safer change than filtering.
	require.Equal(t, 0, row.UnchartedCount,
		"a non-zero count strands the system PENDING forever and stalls the frontier behind it")
	require.Empty(t, unchartedSymbols(t, db, "X1-T"),
		"and the count is zero because the map is genuinely finished, not because anything was excluded from it")
}

// A seed standing ON an uncharted waypoint charts it before flying anywhere,
// even a barren one — and then resumes in priority order.
//
// This is the DEPLOY-DAY shape and it is reachable no other way: the live fleet
// has seeds parked on asteroids right now (TORWIND-18 on X1-AJ10-B26B), left
// there by the old arbitrary order. The chart under the hull's feet costs no
// flight, so taking it first is right; what must NOT happen is the tour then
// carrying on alphabetically.
func TestReorderedTour_ASeedOnAnAsteroidTakesTheFreeChartThenResumesByPriority(t *testing.T) {
	db, ships, commander, ports := tourFixture(t)
	ships.at = "X1-T-A2" // mid-tour on an asteroid, as the old order would have left it

	runTourToStandDown(t, db, ports)

	require.Equal(t, []string{
		"X1-T-A2", // free: the hull is already standing here, no flight to pay
		"X1-T-Z9", // then the shipyard type, ahead of everything else
		"X1-T-B1",
		"X1-T-C1",
		"X1-T-F1",
		"X1-T-D1", // gas giant behind the markets
		"X1-T-A1", // and the remaining asteroids last
		"X1-T-A3",
		"X1-T-A4",
	}, commander.charted,
		"the free chart underfoot comes first, but the tour must then jump to the station rather than continuing alphabetically")

	covered := append([]string(nil), commander.charted...)
	sort.Strings(covered)
	require.Equal(t, everyUnchartedWaypoint(), covered, "and the map still finishes")
}

// A system of nothing but asteroids is still toured in full. Barren is a sorting
// tier, not an exemption — this is the live shape of X1-KC84 (51 asteroids) and
// five others, and the seed must work them all and only then stand down.
func TestReorderedTour_AnAllAsteroidSystemIsStillFullyChartedThenReleased(t *testing.T) {
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

	require.Equal(t, []string{"X1-U-A1", "X1-U-A2", "X1-U-A3"}, commander.charted,
		"all three asteroids are charted — sorting them last must never turn into skipping them")
	require.Nil(t, row.SeedShip, "and only then is the seed released")
	require.Equal(t, 0, row.UnchartedCount)
	require.Empty(t, unchartedSymbols(t, db, "X1-U"))
}
