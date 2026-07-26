package api

import (
	"context"
	"errors"
	"sync"
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

// SyncShipFromAPI is the reconciliation read every 4214 in-transit adoption
// leans on. When it persists a hull that is MID-TRANSIT it must also arm the
// arrival scheduler: ScheduleAllPending only runs at daemon boot, so without
// this the adopted row would sit IN_TRANSIT with no timer and no ARRIVED
// event until the next restart, and every waiter would fall back to its slow
// park path.

type syncScheduleFakeAPIClient struct {
	domainPorts.APIClient
	shipData *navigation.ShipData
}

func (f *syncScheduleFakeAPIClient) GetShip(_ context.Context, _, _ string) (*navigation.ShipData, error) {
	return f.shipData, nil
}

type syncScheduleFakePlayerRepo struct {
	player.PlayerRepository
	p *player.Player
}

func (f *syncScheduleFakePlayerRepo) FindByID(_ context.Context, _ shared.PlayerID) (*player.Player, error) {
	return f.p, nil
}

type syncScheduleFakeWaypointProvider struct{}

func (syncScheduleFakeWaypointProvider) GetWaypoint(_ context.Context, _, _ string, _ int) (*shared.Waypoint, error) {
	return nil, errors.New("stub: use denormalized fallback")
}

// spyArrivalScheduler records every ScheduleArrival call.
type spyArrivalScheduler struct {
	mu    sync.Mutex
	ships []*navigation.Ship
}

func (s *spyArrivalScheduler) ScheduleArrival(ship *navigation.Ship) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ships = append(s.ships, ship)
}

func (s *spyArrivalScheduler) calls() []*navigation.Ship {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*navigation.Ship(nil), s.ships...)
}

func setupSyncScheduleRepo(t *testing.T, shipData *navigation.ShipData) (*ShipRepository, *spyArrivalScheduler, shared.PlayerID) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	playerRow := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok-a", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&playerRow).Error)
	playerID := shared.MustNewPlayerID(playerRow.ID)

	repo := NewShipRepository(
		&syncScheduleFakeAPIClient{shipData: shipData},
		&syncScheduleFakePlayerRepo{p: &player.Player{ID: playerID, Token: "tok-a"}},
		nil, syncScheduleFakeWaypointProvider{}, db, nil,
	)
	spy := &spyArrivalScheduler{}
	repo.SetArrivalScheduler(spy)
	return repo, spy, playerID
}

func TestSyncShipFromAPI_InTransitShip_ArmsArrivalScheduler(t *testing.T) {
	arrival := time.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339)
	repo, spy, playerID := setupSyncScheduleRepo(t, &navigation.ShipData{
		Symbol:      "ENDURANCE-1",
		Location:    "X1-TEST-B2",
		NavStatus:   "IN_TRANSIT",
		ArrivalTime: arrival,
		EngineSpeed: 10,
	})

	fresh, err := repo.SyncShipFromAPI(context.Background(), "ENDURANCE-1", playerID)
	require.NoError(t, err)
	require.True(t, fresh.IsInTransit())

	calls := spy.calls()
	require.Len(t, calls, 1, "an in-transit sync must arm the arrival scheduler")
	require.Equal(t, "ENDURANCE-1", calls[0].ShipSymbol())
	require.NotNil(t, calls[0].ArrivalTime())
}

func TestSyncShipFromAPI_ParkedShip_DoesNotArmArrivalScheduler(t *testing.T) {
	repo, spy, playerID := setupSyncScheduleRepo(t, &navigation.ShipData{
		Symbol:      "ENDURANCE-2",
		Location:    "X1-TEST-B2",
		NavStatus:   "IN_ORBIT",
		EngineSpeed: 10,
	})

	fresh, err := repo.SyncShipFromAPI(context.Background(), "ENDURANCE-2", playerID)
	require.NoError(t, err)
	require.False(t, fresh.IsInTransit())
	require.Empty(t, spy.calls(), "a parked hull must not arm an arrival timer")
}
