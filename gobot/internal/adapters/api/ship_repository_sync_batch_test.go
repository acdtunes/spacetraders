package api

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// batchSyncFakeAPIClient serves a fleet big enough for the upsert, not the read, to be what breaks.
type batchSyncFakeAPIClient struct {
	domainPorts.APIClient
	ships []*navigation.ShipData
}

func (f *batchSyncFakeAPIClient) ListShips(_ context.Context, _ string) ([]*navigation.ShipData, error) {
	return f.ships, nil
}

type batchSyncFakePlayerRepo struct {
	player.PlayerRepository
	p *player.Player
}

func (f *batchSyncFakePlayerRepo) FindByID(_ context.Context, _ shared.PlayerID) (*player.Player, error) {
	return f.p, nil
}

// batchSyncFakeWaypointProvider always errors, so shipDataToModel skips its optional lookup.
type batchSyncFakeWaypointProvider struct{}

func (batchSyncFakeWaypointProvider) GetWaypoint(_ context.Context, _, _ string, _ int) (*shared.Waypoint, error) {
	return nil, errors.New("stub: waypoint lookup not needed by this test")
}

// TestShipUpsertBatchRows_StaysUnderBindParameterCeiling asserts against the LIVE column count,
// so adding a column to ShipModel trips this rather than the fleet-wide sync.
func TestShipUpsertBatchRows_StaysUnderBindParameterCeiling(t *testing.T) {
	cols := shipModelColumnCount()
	require.Greater(t, cols, 0, "ShipModel must expose persisted columns")

	rows := shipUpsertBatchRows()
	require.GreaterOrEqual(t, rows, 1, "a batch must carry at least one row or no sync can ever run")
	require.LessOrEqual(t, rows*cols, maxBindParameters,
		"one batch would emit %d bind parameters against a %d ceiling: at %d columns the batch may be at most %d rows",
		rows*cols, maxBindParameters, cols, maxBindParameters/cols)
}

// TestSyncAllFromAPI_PersistsFleetLargerThanOneBatch: past some fleet size a whole-fleet INSERT
// exceeds the bind-parameter ceiling and fails the sync outright, after the API read is paid for.
// Callers run this behind a fail-closed guard, so it stops a coordinator, not just one write.
func TestSyncAllFromAPI_PersistsFleetLargerThanOneBatch(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	p := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok-live", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&p).Error)
	pid := shared.MustNewPlayerID(p.ID)

	// More than one batch, so the sync cannot pass by fitting in a single statement.
	fleetSize := shipUpsertBatchRows() + 250
	ships := make([]*navigation.ShipData, 0, fleetSize)
	for i := 0; i < fleetSize; i++ {
		ships = append(ships, &navigation.ShipData{
			Symbol:      fmt.Sprintf("TORWIND-%d", i),
			Location:    "X1-TEST-A1",
			NavStatus:   "IN_ORBIT",
			FrameSymbol: "FRAME_PROBE",
		})
	}

	repo := NewShipRepository(
		&batchSyncFakeAPIClient{ships: ships},
		&batchSyncFakePlayerRepo{p: &player.Player{ID: pid, Token: "tok-live"}},
		nil, batchSyncFakeWaypointProvider{}, db, nil,
	)

	count, err := repo.SyncAllFromAPI(context.Background(), pid)
	require.NoError(t, err, "a fleet larger than one batch must still persist")
	require.Equal(t, fleetSize, count)

	var persisted int64
	require.NoError(t, db.Model(&persistence.ShipModel{}).
		Where("player_id = ?", p.ID).Count(&persisted).Error)
	require.Equal(t, int64(fleetSize), persisted,
		"every hull the live fleet reported must land, not just the first batch")
}

// TestSyncAllFromAPI_LargeFleetUpsertIsIdempotent: the sync re-runs over the same hulls, so
// losing ON CONFLICT would duplicate the fleet rather than update it in place.
func TestSyncAllFromAPI_LargeFleetUpsertIsIdempotent(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	p := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok-live", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&p).Error)
	pid := shared.MustNewPlayerID(p.ID)

	fleetSize := shipUpsertBatchRows() + 250
	ships := make([]*navigation.ShipData, 0, fleetSize)
	for i := 0; i < fleetSize; i++ {
		ships = append(ships, &navigation.ShipData{
			Symbol:      fmt.Sprintf("TORWIND-%d", i),
			Location:    "X1-TEST-A1",
			NavStatus:   "DOCKED",
			FrameSymbol: "FRAME_PROBE",
		})
	}

	apiClient := &batchSyncFakeAPIClient{ships: ships}
	repo := NewShipRepository(
		apiClient,
		&batchSyncFakePlayerRepo{p: &player.Player{ID: pid, Token: "tok-live"}},
		nil, batchSyncFakeWaypointProvider{}, db, nil,
	)

	_, err = repo.SyncAllFromAPI(context.Background(), pid)
	require.NoError(t, err)

	// Same hulls, moved. The second sync must UPDATE all of them, not insert duplicates.
	for _, s := range ships {
		s.NavStatus = "IN_ORBIT"
		s.Location = "X1-TEST-B2"
	}
	_, err = repo.SyncAllFromAPI(context.Background(), pid)
	require.NoError(t, err)

	var persisted int64
	require.NoError(t, db.Model(&persistence.ShipModel{}).
		Where("player_id = ?", p.ID).Count(&persisted).Error)
	require.Equal(t, int64(fleetSize), persisted, "a re-sync must update in place, never duplicate")

	var moved int64
	require.NoError(t, db.Model(&persistence.ShipModel{}).
		Where("player_id = ? AND location_symbol = ?", p.ID, "X1-TEST-B2").Count(&moved).Error)
	require.Equal(t, int64(fleetSize), moved, "every hull past the first batch must take the update")
}
