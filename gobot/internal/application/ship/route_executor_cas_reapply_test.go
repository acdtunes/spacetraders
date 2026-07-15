package ship

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// transitStubWaypoints forces modelToDomain's denormalized-coordinate fallback so
// the real repo needs no waypoint rows (mirrors the api-package stubWaypoints).
type transitStubWaypoints struct{}

func (transitStubWaypoints) GetWaypoint(_ context.Context, _, _ string, _ int) (*shared.Waypoint, error) {
	return nil, errors.New("stub: use denormalized fallback")
}

// arrivedNowSubscriber delivers the ARRIVED event immediately so
// WaitForShipArrival returns on the happy (event) path without any 30s poll,
// exactly as ShipStateScheduler's publish would in production.
type arrivedNowSubscriber struct{ stubSubscriber }

func (arrivedNowSubscriber) SubscribeArrived(symbol string) <-chan domainNavigation.ShipArrivedEvent {
	ch := make(chan domainNavigation.ShipArrivedEvent, 1)
	ch <- domainNavigation.ShipArrivedEvent{ShipSymbol: symbol, Status: domainNavigation.NavStatusInOrbit}
	return ch
}

// This is the regression for the reported incident's nav side (sp-wa7c): "a
// nav/arrival-style write must not clobber a fresh cargo quantity on the same
// ship." The route executor's post-transit persist previously wrote its whole
// stale in-memory snapshot with Save(); on a ships.version conflict that
// last-write-wins upsert clobbered a concurrent writer's fresh cargo. The
// migrated persist re-applies ONLY the arrival transition on the fresh row via
// SaveWithRetry, so a colliding cargo update survives.
func TestWaitForCurrentTransit_ArrivalWriteDoesNotClobberFreshCargo(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	playerRow := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&playerRow).Error)
	pid := shared.MustNewPlayerID(playerRow.ID)

	arrivedAt := time.Now().Add(-1 * time.Minute)
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "TRANSIT-1",
		PlayerID:         playerRow.ID,
		AssignmentStatus: "idle",
		NavStatus:        "IN_TRANSIT",
		LocationSymbol:   "X1-TEST-DEST",
		SystemSymbol:     "X1-TEST",
		ArrivalTime:      &arrivedAt,
		FuelCurrent:      500,
		FuelCapacity:     1000,
		CargoCapacity:    200,
		CargoUnits:       100,
		CargoInventory:   `[{"symbol":"IRON_ORE","name":"Iron Ore","description":"x","units":100}]`,
		EngineSpeed:      10,
		Version:          1,
	}).Error)

	shipRepo := api.NewShipRepository(nil, nil, nil, transitStubWaypoints{}, db, nil)
	executor := NewRouteExecutor(shipRepo, nil, nil, nil, nil, nil, nil, arrivedNowSubscriber{})

	// The executor's in-memory snapshot: still IN_TRANSIT, cargo 100.
	snapshot, err := shipRepo.FindBySymbol(context.Background(), "TRANSIT-1", pid)
	require.NoError(t, err)
	require.True(t, snapshot.IsInTransit())

	// Concurrent writer unloads 50 units (100 -> 50) and commits, bumping the row
	// version behind the executor's back. It keeps the hull IN_TRANSIT so the
	// arrival persist still has a transition to apply on the fresh row.
	other, err := shipRepo.FindBySymbol(context.Background(), "TRANSIT-1", pid)
	require.NoError(t, err)
	require.NoError(t, other.RemoveCargo("IRON_ORE", 50))
	require.NoError(t, shipRepo.Save(context.Background(), other))

	// Drive the migrated transit-wait persist on the stale snapshot.
	require.NoError(t, executor.waitForCurrentTransit(context.Background(), snapshot, pid))

	var row persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TRANSIT-1").First(&row).Error)

	// The concurrent cargo write SURVIVED — the old last-write-wins Save would
	// have written the snapshot's stale 100 here.
	require.Equal(t, 50, row.CargoUnits, "concurrent cargo unload must survive the arrival persist (no clobber)")
	// The arrival transition was still applied on the fresh row.
	require.Equal(t, "IN_ORBIT", row.NavStatus, "arrival transition re-applied on fresh state")
	require.Nil(t, row.ArrivalTime, "arrival clock cleared")
}
