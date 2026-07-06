package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

func TestWaypointAddStampsEraIDFromOpenEra(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	closedAt := time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "torwind", AgentSymbol: "TORWIND", PlayerID: 1, ClosedAt: &closedAt}).Error)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 2}).Error)

	var openEra persistence.EraModel
	require.NoError(t, db.Where("name = ?", "orion").First(&openEra).Error)

	repo := persistence.NewGormWaypointRepository(db)
	wp, err := shared.NewWaypoint("X1-ABC-A1", 3, 4)
	require.NoError(t, err)
	wp.SystemSymbol = "X1-ABC"
	wp.Type = "PLANET"
	require.NoError(t, repo.Add(context.Background(), wp))

	var model persistence.WaypointModel
	require.NoError(t, db.Where("waypoint_symbol = ?", "X1-ABC-A1").First(&model).Error)
	require.NotNil(t, model.EraID)
	require.Equal(t, openEra.EraID, *model.EraID)
}

func TestWaypointLiveReadsScopeToOpenEraWhileOldRowsStayReachableByEra(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	closedAt := time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "torwind", AgentSymbol: "TORWIND", PlayerID: 1, ClosedAt: &closedAt}).Error)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 2}).Error)

	var oldEra, openEra persistence.EraModel
	require.NoError(t, db.Where("name = ?", "torwind").First(&oldEra).Error)
	require.NoError(t, db.Where("name = ?", "orion").First(&openEra).Error)

	oldID, openID := oldEra.EraID, openEra.EraID
	require.NoError(t, db.Create(&persistence.WaypointModel{WaypointSymbol: "X1-SYS-OLD", SystemSymbol: "X1-SYS", Type: "PLANET", X: 1, Y: 1, EraID: &oldID}).Error)
	require.NoError(t, db.Create(&persistence.WaypointModel{WaypointSymbol: "X1-SYS-NEW", SystemSymbol: "X1-SYS", Type: "PLANET", X: 2, Y: 2, EraID: &openID}).Error)

	repo := persistence.NewGormWaypointRepository(db)

	live, err := repo.ListBySystem(context.Background(), "X1-SYS")
	require.NoError(t, err)
	liveSymbols := symbolsOf(live)
	require.Equal(t, []string{"X1-SYS-NEW"}, liveSymbols)

	historical, err := repo.ListBySystemForEra(context.Background(), "X1-SYS", oldID)
	require.NoError(t, err)
	require.Equal(t, []string{"X1-SYS-OLD"}, symbolsOf(historical))
}

func symbolsOf(waypoints []*shared.Waypoint) []string {
	out := make([]string, 0, len(waypoints))
	for _, w := range waypoints {
		out = append(out, w.Symbol)
	}
	return out
}
