package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	bootstrapCmd "github.com/andrescamacho/spacetraders-go/internal/application/bootstrap/commands"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// readHomeCoverage feeds the bootstrap heartbeat's coverage=N/M gauge (sp-d4lwj). A FUEL_STATION
// carries the MARKETPLACE trait but workflow scout-all-markets excludes it by design
// (AssignScoutingFleetHandler.filterNonFuelStations) — a probe circuit never visits one. Counting
// it in the denominator made 100% an unreachable ceiling, indistinguishable from a genuine
// shortfall. These pin the fix's invariant (coverage reaches 1.0 exactly when the scout engine has
// toured everything it is designed to tour) and the deliberate fuel-station decision.

// coverageObserverDB opens a fresh in-memory test DB with one seeded player (satisfies
// market_data's PlayerID foreign key) and returns its id.
func coverageObserverDB(t *testing.T) (*gorm.DB, int) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	player := persistence.PlayerModel{AgentSymbol: "SP-COVERAGE", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	return db, player.ID
}

// coverageWaypoint seeds a MARKETPLACE-trait waypoint of the given TYPE (e.g. "PLANET" or
// "FUEL_STATION" — the type FindAllMarketsInSystem filters scoutability on). No era row exists in
// this DB, so leaving EraID unset (NULL) matches the "no open era" scope every era-scoped read here
// falls back to.
func coverageWaypoint(t *testing.T, db *gorm.DB, symbol, system, wpType string) {
	t.Helper()
	require.NoError(t, db.Create(&persistence.WaypointModel{
		WaypointSymbol: symbol, SystemSymbol: system, Type: wpType,
		X: 1, Y: 1, Traits: `["MARKETPLACE"]`,
	}).Error)
}

// coverageFreshMarket seeds one just-scanned trade good at symbol, making it read as "covered" by
// ListMarketsInSystem's freshness window.
func coverageFreshMarket(t *testing.T, db *gorm.DB, playerID int, symbol string) {
	t.Helper()
	require.NoError(t, db.Create(&persistence.MarketData{
		PlayerID: playerID, WaypointSymbol: symbol, GoodSymbol: "FUEL",
		PurchasePrice: 10, SellPrice: 5, TradeVolume: 100, LastUpdated: time.Now(),
	}).Error)
}

// coverageObserverFor builds a bootstrapObserver with only the market repo wired — everything
// readHomeCoverage touches.
func coverageObserverFor(db *gorm.DB) *bootstrapObserver {
	return &bootstrapObserver{marketRepo: persistence.NewMarketRepositoryAdapter(persistence.NewMarketRepository(db))}
}

// TestReadHomeCoverage_FuelStationOnlyGapReadsAsComplete is the RED-then-GREEN case from the live
// evidence (era torwind-2026-08-09, X1-DU34): 28/29 MARKETPLACE waypoints have data, and the ONE
// gap (X1-DU34-B6) is a FUEL_STATION scout-all-markets never tours by design. Coverage must read
// 1.0 for this shape — the scout engine has done everything it is designed to do.
func TestReadHomeCoverage_FuelStationOnlyGapReadsAsComplete(t *testing.T) {
	db, playerID := coverageObserverDB(t)
	coverageWaypoint(t, db, "X1-TT-A1", "X1-TT", "PLANET")
	coverageFreshMarket(t, db, playerID, "X1-TT-A1")
	coverageWaypoint(t, db, "X1-TT-A2", "X1-TT", "ORBITAL_STATION")
	coverageFreshMarket(t, db, playerID, "X1-TT-A2")
	coverageWaypoint(t, db, "X1-TT-B6", "X1-TT", "FUEL_STATION") // never toured, never scanned

	obs := bootstrapCmd.Observation{HomeSystem: "X1-TT"}
	coverageObserverFor(db).readHomeCoverage(context.Background(), playerID, &obs)

	require.Equal(t, 2, obs.MarketsTotal, "the FUEL_STATION is never on a probe circuit — it must not inflate the denominator")
	require.Equal(t, 2, obs.MarketsCovered, "both real markets have fresh data")
	require.Equal(t, 1.0, obs.CoverageFraction(), "an untoured FUEL_STATION must not be an unreachable ceiling")
}

// TestReadHomeCoverage_RealGapStillDragsCoverageDown is the discriminating-power guard: fixing the
// fuel-station ceiling must not blind the gauge to an actual scanning shortfall. One real market
// (A2) is never scanned and must still drag the ratio below 1.0, even alongside a FUEL_STATION
// (C43) that is fully excluded despite holding its own (stale) data.
func TestReadHomeCoverage_RealGapStillDragsCoverageDown(t *testing.T) {
	db, playerID := coverageObserverDB(t)
	coverageWaypoint(t, db, "X1-TT-A1", "X1-TT", "PLANET")
	coverageFreshMarket(t, db, playerID, "X1-TT-A1") // scanned
	coverageWaypoint(t, db, "X1-TT-A2", "X1-TT", "ORBITAL_STATION")
	// A2 intentionally has no market row: the real, unscanned gap.
	coverageWaypoint(t, db, "X1-TT-C43", "X1-TT", "FUEL_STATION")
	coverageFreshMarket(t, db, playerID, "X1-TT-C43") // opportunistically scanned, excluded regardless

	obs := bootstrapCmd.Observation{HomeSystem: "X1-TT"}
	coverageObserverFor(db).readHomeCoverage(context.Background(), playerID, &obs)

	require.Equal(t, 2, obs.MarketsTotal, "only the two real markets are scoutable")
	require.Equal(t, 1, obs.MarketsCovered, "the untoured real market (A2) must still count as a gap")
	require.InDelta(t, 0.5, obs.CoverageFraction(), 1e-9, "a genuine shortfall must still pull coverage below 1.0")
}

// TestReadHomeCoverage_IncidentalFuelStationDataIsNeitherTerm pins the deliberate sp-d4lwj
// decision: a FUEL_STATION that picks up real, fresh market data (a hull refuelled there) is
// counted in NEITHER numerator nor denominator. The data itself is not discarded — it stays live
// and queryable via `market list` — it simply is not what THIS ratio measures ("has the designed
// scout job finished"), so two fully-scanned real markets plus a bonus-scanned fuel station reads
// exactly 100%, not something inflated or deflated by the bonus.
func TestReadHomeCoverage_IncidentalFuelStationDataIsNeitherTerm(t *testing.T) {
	db, playerID := coverageObserverDB(t)
	coverageWaypoint(t, db, "X1-TT-A1", "X1-TT", "PLANET")
	coverageFreshMarket(t, db, playerID, "X1-TT-A1")
	coverageWaypoint(t, db, "X1-TT-A2", "X1-TT", "ORBITAL_STATION")
	coverageFreshMarket(t, db, playerID, "X1-TT-A2")
	coverageWaypoint(t, db, "X1-TT-I62", "X1-TT", "FUEL_STATION")
	coverageFreshMarket(t, db, playerID, "X1-TT-I62") // fresh, tradeable — the I62/J63 live shape

	obs := bootstrapCmd.Observation{HomeSystem: "X1-TT"}
	coverageObserverFor(db).readHomeCoverage(context.Background(), playerID, &obs)

	require.Equal(t, 2, obs.MarketsTotal, "the fuel station's presence must not inflate the denominator")
	require.Equal(t, 2, obs.MarketsCovered, "the fuel station's fresh data must not inflate the numerator either")
	require.Equal(t, 1.0, obs.CoverageFraction())
}
