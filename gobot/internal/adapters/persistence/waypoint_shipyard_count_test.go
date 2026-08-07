package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// ChartedShipyardCount is the denominator of the fleet's shipyard-read rotation,
// so its unresolved path has to refuse rather than answer. The permissive scope
// helper degrades to `era_id IS NULL`, which on a backfilled table matches no row
// and hands back a confident zero — a count the budget cannot tell apart from a
// real one, and one that shortens every yard's interval and the anti-starvation
// bound with it.

func TestChartedShipyardCountRefusesWhenNoEraIsOpen(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	closedAt := time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "torwind", AgentSymbol: "TORWIND", PlayerID: 1, ClosedAt: &closedAt}).Error)

	var closedEra persistence.EraModel
	require.NoError(t, db.Where("name = ?", "torwind").First(&closedEra).Error)
	closedID := closedEra.EraID
	require.NoError(t, db.Create(&persistence.WaypointModel{
		WaypointSymbol: "X1-OLD-Y1", SystemSymbol: "X1-OLD", Type: "ORBITAL_STATION",
		X: 1, Y: 1, Traits: `["SHIPYARD"]`, EraID: &closedID,
	}).Error)

	repo := persistence.NewGormWaypointRepository(db)
	_, err = repo.ChartedShipyardCount(context.Background())

	require.Error(t, err,
		"with every era closed the count must refuse, not answer zero from a clause that matches nothing")
}

// The OTHER fact the permissive helper collapses into the same nil: not "every era
// is closed" but "the eras table could not be read at all". It is the transient
// case, and the one that makes this defect a live hazard rather than an
// end-of-universe curiosity — a budget that has been pacing correctly for hours
// starts admitting everything the moment one read of the ledger fails, then
// recovers, leaving nothing behind to explain the burst.
//
// A yard row stamped with a real era is staged deliberately: it is what makes the
// permissive answer WRONG rather than merely empty. `era_id IS NULL` matches no
// such row, so the count comes back zero while the map plainly is not.
func TestChartedShipyardCountRefusesWhenTheEraLedgerCannotBeRead(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 2}).Error)

	var openEra persistence.EraModel
	require.NoError(t, db.Where("name = ?", "orion").First(&openEra).Error)
	openID := openEra.EraID
	require.NoError(t, db.Create(&persistence.WaypointModel{
		WaypointSymbol: "X1-NEW-Y1", SystemSymbol: "X1-NEW", Type: "ORBITAL_STATION",
		X: 1, Y: 1, Traits: `["SHIPYARD"]`, EraID: &openID,
	}).Error)

	repo := persistence.NewGormWaypointRepository(db)
	before, err := repo.ChartedShipyardCount(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, before, "the charted yard is counted while the ledger is readable")

	// The ledger goes unreadable underneath a budget that was pacing correctly.
	require.NoError(t, db.Migrator().DropTable(&persistence.EraModel{}))

	_, err = repo.ChartedShipyardCount(context.Background())
	require.Error(t, err,
		"an unreadable era ledger must refuse, not report an empty map while a charted yard sits in the table")
}

// The resolved path is unchanged: open-era yards are counted, dead-era yards are
// not. Counting a dead-era yard would lengthen every live yard's interval, which
// is why this read is era-scoped in the first place.
func TestChartedShipyardCountCountsOpenEraYardsOnly(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	closedAt := time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "torwind", AgentSymbol: "TORWIND", PlayerID: 1, ClosedAt: &closedAt}).Error)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 2}).Error)

	var oldEra, openEra persistence.EraModel
	require.NoError(t, db.Where("name = ?", "torwind").First(&oldEra).Error)
	require.NoError(t, db.Where("name = ?", "orion").First(&openEra).Error)
	oldID, openID := oldEra.EraID, openEra.EraID

	require.NoError(t, db.Create(&persistence.WaypointModel{
		WaypointSymbol: "X1-OLD-Y1", SystemSymbol: "X1-OLD", Type: "ORBITAL_STATION",
		X: 1, Y: 1, Traits: `["SHIPYARD"]`, EraID: &oldID,
	}).Error)
	require.NoError(t, db.Create(&persistence.WaypointModel{
		WaypointSymbol: "X1-NEW-Y1", SystemSymbol: "X1-NEW", Type: "ORBITAL_STATION",
		X: 2, Y: 2, Traits: `["MARKETPLACE","SHIPYARD"]`, EraID: &openID,
	}).Error)
	require.NoError(t, db.Create(&persistence.WaypointModel{
		WaypointSymbol: "X1-NEW-P1", SystemSymbol: "X1-NEW", Type: "PLANET",
		X: 3, Y: 3, Traits: `["MARKETPLACE"]`, EraID: &openID,
	}).Error)

	repo := persistence.NewGormWaypointRepository(db)
	count, err := repo.ChartedShipyardCount(context.Background())

	require.NoError(t, err)
	require.Equal(t, 1, count, "only the open-era SHIPYARD waypoint counts")
}
