package watchkeeper

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"gorm.io/gorm"
)

// containerlessEvents returns only the hull.containerless events for a player.
func containerlessEvents(t *testing.T, store *persistence.GormCaptainEventRepository, playerID int) []*captain.Event {
	t.Helper()
	all, err := store.FindUnprocessed(context.Background(), playerID, 100)
	require.NoError(t, err)
	var out []*captain.Event
	for _, e := range all {
		if e.Type == captain.EventPinnedHullContainerless {
			out = append(out, e)
		}
	}
	return out
}

func insertDedicatedShip(t *testing.T, db *gorm.DB, playerID int, symbol, fleet string, releasedAt *time.Time, cargoUnits, cargoCap int) {
	t.Helper()
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:     symbol,
		PlayerID:       playerID,
		NavStatus:      "DOCKED",
		LocationSymbol: "X1-GQ92-A4",
		DedicatedFleet: fleet,
		ReleasedAt:     releasedAt,
		CargoUnits:     cargoUnits,
		CargoCapacity:  cargoCap,
	}).Error)
}

// The sp-v63s watchdog acceptance: a hull PINNED to a fleet, containerless past the
// threshold, fires exactly one interrupt-class hull.containerless event naming the
// hull and its laden cargo — and re-running the detector does not duplicate it.
func TestContainerlessPinnedHull_FiresOneInterruptEvent(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	released := now.Add(-12 * time.Minute)
	insertDedicatedShip(t, db, playerID, "TORWIND-19", "trade", &released, 160, 225)

	cfg := DetectorConfig{PlayerID: playerID, PinnedHullContainerless: 5 * time.Minute, ShipIdle: time.Hour}
	require.NoError(t, detectContainerlessPinnedHulls(context.Background(), db, store, cfg, now))

	events := containerlessEvents(t, store, playerID)
	require.Len(t, events, 1, "a pinned hull containerless past the threshold must fire exactly one event")
	require.Equal(t, "TORWIND-19", events[0].Ship)
	require.Contains(t, events[0].Payload, "TORWIND-19")
	require.Contains(t, events[0].Payload, "trade")
	require.Contains(t, events[0].Payload, `"cargo_units":160`)
	require.Contains(t, events[0].Payload, `"cargo_capacity":225`)

	require.True(t, captain.IsInterrupt(captain.EventPinnedHullContainerless, nil),
		"a stranded pinned revenue hull must force a wake, not ride the next cadence")

	// Re-running the detector must not duplicate while the state persists (no spam).
	require.NoError(t, detectContainerlessPinnedHulls(context.Background(), db, store, cfg, now))
	require.Len(t, containerlessEvents(t, store, playerID), 1, "the detector must be edge-triggered, not per-poll")
}

// A pinned hull WITH a running container is healthy — silent.
func TestContainerlessPinnedHull_WithRunningContainer_Silent(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	released := now.Add(-12 * time.Minute)
	insertDedicatedShip(t, db, playerID, "TORWIND-2C", "tour", &released, 74, 225)

	started := now.Add(-1 * time.Hour)
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: "tour-run-TORWIND-2C-live", PlayerID: playerID, Status: "RUNNING",
		Config: `{"ship_symbol":"TORWIND-2C","max_iterations":-1}`, StartedAt: &started,
	}).Error)

	cfg := DetectorConfig{PlayerID: playerID, PinnedHullContainerless: 5 * time.Minute, ShipIdle: time.Hour}
	require.NoError(t, detectContainerlessPinnedHulls(context.Background(), db, store, cfg, now))

	require.Empty(t, containerlessEvents(t, store, playerID),
		"a pinned hull with a running container must stay silent")
}

// contractStandingCoordinatorFleets is the sp-jetm exemption config under test:
// one entry, mirroring defaultStandingCoordinatorFleets.
func contractStandingCoordinatorFleets() []StandingCoordinatorFleet {
	return []StandingCoordinatorFleet{{Fleet: "contract", ContainerType: "CONTRACT_FLEET_COORDINATOR"}}
}

// A contract-fleet hull pooled-idle between claims (containerless well past the
// threshold, no direct container) stays silent WHILE the contract fleet's pool
// coordinator has a RUNNING container — pooled-idle is by design (sp-jetm). The
// coordinator's config deliberately does NOT reference this ship's symbol: the
// exemption is fleet-based (DedicatedFleet match), not a per-ship config join.
func TestContainerlessPinnedHull_ContractFleetWithRunningCoordinator_Silent(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	released := now.Add(-30 * time.Minute)
	insertDedicatedShip(t, db, playerID, "TORWIND-CF1", "contract", &released, 0, 40)

	started := now.Add(-2 * time.Hour)
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: "contract-fleet-coordinator-live", PlayerID: playerID, Status: "RUNNING",
		ContainerType: "CONTRACT_FLEET_COORDINATOR",
		Config:        `{"container_id":"contract-fleet-coordinator-live","dedicated_ships":[]}`,
		StartedAt:     &started,
	}).Error)

	cfg := DetectorConfig{
		PlayerID: playerID, PinnedHullContainerless: 5 * time.Minute, ShipIdle: time.Hour,
		StandingCoordinatorFleets: contractStandingCoordinatorFleets(),
	}
	require.NoError(t, detectContainerlessPinnedHulls(context.Background(), db, store, cfg, now))

	require.Empty(t, containerlessEvents(t, store, playerID),
		"a contract-fleet hull pooled-idle between claims must stay silent while its coordinator runs")
}

// The SAME contract-fleet hull fires once its fleet's coordinator is no longer
// RUNNING (here: dead/FAILED) — the coordinator dying is a genuine loss mode the
// watchdog must still catch, not something the pool exemption should hide.
func TestContainerlessPinnedHull_ContractFleetCoordinatorNotRunning_Fires(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	released := now.Add(-30 * time.Minute)
	insertDedicatedShip(t, db, playerID, "TORWIND-CF2", "contract", &released, 0, 40)

	started := now.Add(-2 * time.Hour)
	stopped := now.Add(-20 * time.Minute)
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: "contract-fleet-coordinator-dead", PlayerID: playerID, Status: "FAILED",
		ContainerType: "CONTRACT_FLEET_COORDINATOR",
		Config:        `{"container_id":"contract-fleet-coordinator-dead","dedicated_ships":[]}`,
		StartedAt:     &started, StoppedAt: &stopped,
	}).Error)

	cfg := DetectorConfig{
		PlayerID: playerID, PinnedHullContainerless: 5 * time.Minute, ShipIdle: time.Hour,
		StandingCoordinatorFleets: contractStandingCoordinatorFleets(),
	}
	require.NoError(t, detectContainerlessPinnedHulls(context.Background(), db, store, cfg, now))

	events := containerlessEvents(t, store, playerID)
	require.Len(t, events, 1, "a contract-fleet hull must fire once its pool coordinator is no longer running")
	require.Equal(t, "TORWIND-CF2", events[0].Ship)
	require.Contains(t, events[0].Payload, "contract")
}

// A tour-pinned hull still fires even when StandingCoordinatorFleets is
// configured (with "contract") AND a contract coordinator happens to be
// RUNNING at the same time — the exemption must not leak across fleets
// (regression: the v63s matrix stays green for the class it was built for).
func TestContainerlessPinnedHull_TourFleetWithContractCoordinatorRunning_StillFires(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	released := now.Add(-12 * time.Minute)
	insertDedicatedShip(t, db, playerID, "TORWIND-TR1", "tour", &released, 74, 225)

	started := now.Add(-2 * time.Hour)
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: "contract-fleet-coordinator-live", PlayerID: playerID, Status: "RUNNING",
		ContainerType: "CONTRACT_FLEET_COORDINATOR",
		Config:        `{"container_id":"contract-fleet-coordinator-live","dedicated_ships":[]}`,
		StartedAt:     &started,
	}).Error)

	cfg := DetectorConfig{
		PlayerID: playerID, PinnedHullContainerless: 5 * time.Minute, ShipIdle: time.Hour,
		StandingCoordinatorFleets: contractStandingCoordinatorFleets(),
	}
	require.NoError(t, detectContainerlessPinnedHulls(context.Background(), db, store, cfg, now))

	events := containerlessEvents(t, store, playerID)
	require.Len(t, events, 1, "a tour-pinned hull must keep firing — the contract exemption must not leak to other fleets")
	require.Equal(t, "TORWIND-TR1", events[0].Ship)
}

// An UNDEDICATED hull with no container is not the watchdog's concern (detectIdleShips
// owns generic idleness) — silent here.
func TestContainerlessPinnedHull_UndedicatedHull_Silent(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	released := now.Add(-12 * time.Minute)
	insertDedicatedShip(t, db, playerID, "TORWIND-7", "", &released, 0, 40) // dedicated_fleet=''

	cfg := DetectorConfig{PlayerID: playerID, PinnedHullContainerless: 5 * time.Minute, ShipIdle: time.Hour}
	require.NoError(t, detectContainerlessPinnedHulls(context.Background(), db, store, cfg, now))

	require.Empty(t, containerlessEvents(t, store, playerID),
		"an undedicated hull must not fire the pinned-hull watchdog")
}

// A pinned hull only BRIEFLY containerless (normal redeploy+recovery churn) must not
// fire — the age gate tolerates the transient window.
func TestContainerlessPinnedHull_BrieflyContainerless_Silent(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	released := now.Add(-2 * time.Minute) // < 5m threshold
	insertDedicatedShip(t, db, playerID, "TORWIND-2B", "trade", &released, 0, 225)

	cfg := DetectorConfig{PlayerID: playerID, PinnedHullContainerless: 5 * time.Minute, ShipIdle: time.Hour}
	require.NoError(t, detectContainerlessPinnedHulls(context.Background(), db, store, cfg, now))

	require.Empty(t, containerlessEvents(t, store, playerID),
		"a hull containerless for less than the threshold (normal recovery churn) must stay silent")
}

// A pinned hull that never held an assignment (released_at NULL) is a launch/config
// concern, not a silent death — the watchdog stays silent (no age to measure).
func TestContainerlessPinnedHull_NeverAssigned_Silent(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	insertDedicatedShip(t, db, playerID, "TORWIND-1F", "trade", nil, 0, 225)

	cfg := DetectorConfig{PlayerID: playerID, PinnedHullContainerless: 5 * time.Minute, ShipIdle: time.Hour}
	require.NoError(t, detectContainerlessPinnedHulls(context.Background(), db, store, cfg, now))

	require.Empty(t, containerlessEvents(t, store, playerID),
		"a dedicated hull that never held an assignment must not fire (no containerless-since anchor)")
}

// Disabled (threshold <= 0) is a no-op even for a long-stranded pinned hull.
func TestContainerlessPinnedHull_DisabledThreshold_Silent(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	released := now.Add(-1 * time.Hour)
	insertDedicatedShip(t, db, playerID, "TORWIND-19", "trade", &released, 160, 225)

	cfg := DetectorConfig{PlayerID: playerID, PinnedHullContainerless: 0, ShipIdle: time.Hour}
	require.NoError(t, detectContainerlessPinnedHulls(context.Background(), db, store, cfg, now))

	require.Empty(t, containerlessEvents(t, store, playerID))
}
