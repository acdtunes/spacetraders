package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// These tests cover sp-vp9k: nav.route.origin + route.departureTime must be
// persisted for IN_TRANSIT ships so DB consumers (the visualizer Contract Ops
// tab) can compute exact transit progress instead of approximating departure
// from poll timing. ship_dto.go historically dropped both fields; here we drive
// the persistence + round-trip half of the fix (the DTO half is pinned in
// client_ship_mapper_test.go).

// syncNavOriginFakeAPIClient returns one caller-supplied ShipData from ListShips
// so SyncAllFromAPI has something to upsert without a live SpaceTraders API.
type syncNavOriginFakeAPIClient struct {
	domainPorts.APIClient
	shipData *navigation.ShipData
}

func (f *syncNavOriginFakeAPIClient) ListShips(_ context.Context, _ string) ([]*navigation.ShipData, error) {
	return []*navigation.ShipData{f.shipData}, nil
}

// syncNavOriginFakePlayerRepo supplies just enough of player.PlayerRepository for
// SyncAllFromAPI to resolve an API token.
type syncNavOriginFakePlayerRepo struct {
	player.PlayerRepository
	p *player.Player
}

func (f *syncNavOriginFakePlayerRepo) FindByID(_ context.Context, _ shared.PlayerID) (*player.Player, error) {
	return f.p, nil
}

// syncNavOriginFakeWaypointProvider always errors, so the best-effort LOCATION
// lookup in shipDataToModel/modelToDomain is skipped. The transit ORIGIN under
// test is carried directly from the ShipData, not from this provider, so its
// absence has no bearing on the columns these tests assert.
type syncNavOriginFakeWaypointProvider struct{}

func (syncNavOriginFakeWaypointProvider) GetWaypoint(_ context.Context, _, _ string, _ int) (*shared.Waypoint, error) {
	return nil, errors.New("stub: waypoint lookup not needed by this test")
}

func setupNavOriginRepo(t *testing.T, shipData *navigation.ShipData) (*ShipRepository, shared.PlayerID, *gorm.DB) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	playerRow := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok-a", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&playerRow).Error)
	playerID := shared.MustNewPlayerID(playerRow.ID)

	apiClient := &syncNavOriginFakeAPIClient{shipData: shipData}
	playerRepo := &syncNavOriginFakePlayerRepo{p: &player.Player{ID: playerID, Token: "tok-a"}}
	repo := NewShipRepository(apiClient, playerRepo, nil, syncNavOriginFakeWaypointProvider{}, db, nil)
	return repo, playerID, db
}

const navOriginDeparture = "2024-01-01T12:00:00Z"

// TestSyncAllFromAPI_PersistsNavRouteOrigin is the primary behavior: an
// IN_TRANSIT ship synced from the API stores where it departed from (symbol +
// coordinates) and when, so the transit-progress consumers have exact endpoints.
// The docked variation guards that a ship with no route writes an empty origin
// and NULL departure rather than garbage.
func TestSyncAllFromAPI_PersistsNavRouteOrigin(t *testing.T) {
	t.Run("in_transit ship persists origin and departure from the route", func(t *testing.T) {
		repo, playerID, db := setupNavOriginRepo(t, &navigation.ShipData{
			Symbol:        "ENDURANCE-1",
			Location:      "X1-TEST-A1",
			NavStatus:     "IN_TRANSIT",
			OriginSymbol:  "X1-TEST-ORIGIN",
			OriginX:       12,
			OriginY:       -7,
			DepartureTime: navOriginDeparture,
		})

		_, err := repo.SyncAllFromAPI(context.Background(), playerID)
		require.NoError(t, err)

		var model persistence.ShipModel
		require.NoError(t, db.Where("ship_symbol = ?", "ENDURANCE-1").First(&model).Error)
		require.Equal(t, "X1-TEST-ORIGIN", model.OriginSymbol, "origin waypoint must persist so consumers know where the transit started")
		require.Equal(t, float64(12), model.OriginX, "origin x must persist")
		require.Equal(t, float64(-7), model.OriginY, "origin y must persist")
		require.NotNil(t, model.DepartureTime, "departure_time must persist for exact transit progress")
		wantDeparture, _ := time.Parse(time.RFC3339, navOriginDeparture)
		require.True(t, wantDeparture.Equal(*model.DepartureTime), "departure_time: want %s, got %s", wantDeparture, model.DepartureTime)
	})

	t.Run("docked ship with no route persists empty origin and null departure", func(t *testing.T) {
		repo, playerID, db := setupNavOriginRepo(t, &navigation.ShipData{
			Symbol:    "ENDURANCE-2",
			Location:  "X1-TEST-A1",
			NavStatus: "DOCKED",
		})

		_, err := repo.SyncAllFromAPI(context.Background(), playerID)
		require.NoError(t, err)

		var model persistence.ShipModel
		require.NoError(t, db.Where("ship_symbol = ?", "ENDURANCE-2").First(&model).Error)
		require.Empty(t, model.OriginSymbol, "a ship not in transit has no origin waypoint")
		require.Nil(t, model.DepartureTime, "a ship not in transit has no departure time")
	})
}

// TestNavRouteOriginSurvivesDomainRoundTrip guards the sp-90a3/w870/bi75 clobber
// class: every general ship Save rewrites the WHOLE row (UpdateAll upsert) from
// the in-memory domain ship. If origin/departure were sync-only (not carried on
// the domain ship), the first routine Save of an in-transit hull would silently
// zero them back out before a dashboard could read them. They must round-trip.
func TestNavRouteOriginSurvivesDomainRoundTrip(t *testing.T) {
	repo, playerID, db := setupNavOriginRepo(t, &navigation.ShipData{
		Symbol:        "ENDURANCE-1",
		Location:      "X1-TEST-A1",
		NavStatus:     "IN_TRANSIT",
		OriginSymbol:  "X1-TEST-ORIGIN",
		OriginX:       12,
		OriginY:       -7,
		DepartureTime: navOriginDeparture,
		// A well-formed hull so the domain reconstruct + Save re-validation passes
		// (engine_speed>0, fuel capacity matches) and the round-trip actually runs.
		EngineSpeed:   30,
		FuelCurrent:   380,
		FuelCapacity:  400,
		CargoCapacity: 60,
		FrameSymbol:   "FRAME_MINER",
	})

	// Sync writes origin/departure via the sync path.
	_, err := repo.SyncAllFromAPI(context.Background(), playerID)
	require.NoError(t, err)

	// Load into the domain and Save back — the round-trip that would clobber a
	// sync-only column to zero.
	ship, err := repo.FindBySymbol(context.Background(), "ENDURANCE-1", playerID)
	require.NoError(t, err)
	require.NoError(t, repo.Save(context.Background(), ship))

	var model persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "ENDURANCE-1").First(&model).Error)
	require.Equal(t, "X1-TEST-ORIGIN", model.OriginSymbol, "origin must survive a domain load->save, not be clobbered to zero")
	require.Equal(t, float64(12), model.OriginX, "origin x must survive a domain load->save")
	require.Equal(t, float64(-7), model.OriginY, "origin y must survive a domain load->save")
	require.NotNil(t, model.DepartureTime, "departure_time must survive a domain load->save")
	wantDeparture, _ := time.Parse(time.RFC3339, navOriginDeparture)
	require.True(t, wantDeparture.Equal(*model.DepartureTime), "departure_time: want %s, got %s", wantDeparture, model.DepartureTime)
}
