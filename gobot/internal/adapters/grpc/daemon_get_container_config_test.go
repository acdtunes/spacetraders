package grpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// sp-aoy2: `container get` must display the config as it is persisted in the
// database (Store A — the source of truth that live mutations write), NOT the
// in-memory entity's Metadata() which NewContainer freezes at launch (Store B).
// This locks the read path onto Store A while keeping the runtime/lifecycle
// fields sourced from the live in-memory entity.

// TestGetContainer_ReflectsLivePersistedConfigWithoutRestart is the core
// regression: after a live config mutation rewrites only the persisted config
// (no restart, the in-memory entity untouched), GetContainer returns the NEW
// config, not a launch-frozen snapshot.
func TestGetContainer_ReflectsLivePersistedConfigWithoutRestart(t *testing.T) {
	s, _, playerID := newRecoveryTestServer(t)
	ctx := context.Background()
	impl := NewDaemonServiceImpl(s)

	const containerID = "contract_fleet_coordinator-AOY2-CONFIG"

	// Launch metadata frozen into the in-memory entity by NewContainer (Store B).
	launchMeta := map[string]interface{}{
		"container_id":     containerID,
		"standby_stations": []string{"X1-TW-A1"},
	}
	entity := container.NewContainer(containerID, container.ContainerTypeContractFleetCoordinator,
		playerID, -1, nil, launchMeta, nil)
	require.NoError(t, s.containerRepo.Add(ctx, entity, "contract_fleet_coordinator"))

	// Register the live in-memory runner so GetContainer resolves the entity for its
	// runtime/lifecycle fields — exactly as a running daemon holds it.
	runner := NewContainerRunner(entity, s.mediator, nil, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	defer runner.cancelFunc()
	s.containers[containerID] = runner

	// Baseline: the displayed config matches the launch snapshot.
	resp, err := impl.GetContainer(ctx, &pb.GetContainerRequest{ContainerId: containerID})
	require.NoError(t, err)
	require.Contains(t, resp.Metadata, `"X1-TW-A1"`)
	require.NotContains(t, resp.Metadata, `"X1-TW-B2"`)

	// LIVE MUTATION: a `fleet hub add X1-TW-B2` rewrites ONLY the persisted config
	// (Store A). The in-memory entity is NOT touched — no restart, no rebuild.
	mutated := `{"container_id":"` + containerID + `","standby_stations":["X1-TW-A1","X1-TW-B2"]}`
	require.NoError(t, s.containerRepo.UpdateContainerConfig(ctx, containerID, playerID, mutated))

	resp2, err := impl.GetContainer(ctx, &pb.GetContainerRequest{ContainerId: containerID})
	require.NoError(t, err)
	require.Contains(t, resp2.Metadata, `"X1-TW-B2"`,
		"container get must reflect the live-persisted config without a daemon restart (sp-aoy2)")

	// Proof the new value came from Store A (the DB) and not Store B (the entity):
	// the in-memory entity's Metadata() is still frozen at the launch snapshot.
	frozen, err := json.Marshal(entity.Metadata())
	require.NoError(t, err)
	require.NotContains(t, string(frozen), "X1-TW-B2",
		"the in-memory entity's Metadata stays frozen at launch — the new config came from the DB")
}

// TestGetContainer_RuntimeFieldsComeFromLiveEntityNotDB is the guard: the
// runtime/lifecycle fields (status, iterations, restart count) must keep coming
// from the live in-memory entity, NOT the persisted DB row. Only the config JSON
// moved to Store A. Here the live entity is RUNNING while its DB row is still
// PENDING (no status write), and GetContainer must report RUNNING.
func TestGetContainer_RuntimeFieldsComeFromLiveEntityNotDB(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	ctx := context.Background()
	impl := NewDaemonServiceImpl(s)

	const containerID = "arb_run-AOY2-RUNTIME"
	entity := container.NewContainer(containerID, container.ContainerTypeTrading, playerID, 5, nil,
		map[string]interface{}{"ship_symbol": "SHIP-A"}, nil)
	// Add persists the row while the entity is PENDING.
	require.NoError(t, s.containerRepo.Add(ctx, entity, "arb_run"))

	// Advance ONLY the live entity to RUNNING; the DB status row stays PENDING.
	require.NoError(t, entity.Start())

	runner := NewContainerRunner(entity, s.mediator, nil, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	defer runner.cancelFunc()
	s.containers[containerID] = runner

	// Sanity: a DB-sourced status would read PENDING here.
	var model persistence.ContainerModel
	require.NoError(t, db.First(&model, "id = ?", containerID).Error)
	require.Equal(t, "PENDING", model.Status)

	resp, err := impl.GetContainer(ctx, &pb.GetContainerRequest{ContainerId: containerID})
	require.NoError(t, err)
	require.Equal(t, "RUNNING", resp.Container.Status,
		"status must come from the live in-memory entity, not the DB row")
	require.EqualValues(t, 5, resp.Container.MaxIterations,
		"runtime fields must come from the live entity, not the DB row")
}
