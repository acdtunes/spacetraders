package grpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
)

// sp-fwxm THE O34Q WRITE-SIDE PIN (ferry variant). The worker-ferry jump bound is daemon-global
// tuning ([trade_fleet].reposition_jump_bound, the SAME knob the tour reposition rides, sp-kl16),
// so PersistWorkerFerryWorker must STAMP it into the launch config — the write half of the persist
// boundary. Unlike the TOP-LEVEL tour (never persisted via PersistContainer), the ferry IS
// persisted via PersistContainer and REBUILT by buildWorkerFerryCommand on every start (fresh AND
// recovery), so WITHOUT this write the rebuild reads 0 and the ferry's cross-cluster leg silently
// degrades to the strict resolver that fail-closes the vdld far-cluster launch — exactly the
// sp-o34q class the scout relay suffered live. Paired with the round-trip read half in the same
// assertion below.
func TestPersistWorkerFerryWorker_StampsRepositionJumpBoundFromTradeFleetConfig(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	s.tradeFleetConfig.RepositionJumpBound = 9 // the captain's [trade_fleet] override

	require.NoError(t, s.PersistWorkerFerryWorker(context.Background(), "ferry-FWXM-BOUND", "LIGHT-7", "X1-C81-MARKET", playerID, "worker_rebalancer_coordinator-1"))

	var model persistence.ContainerModel
	require.NoError(t, db.First(&model, "id = ?", "ferry-FWXM-BOUND").Error)
	require.Contains(t, model.Config, `"reposition_jump_bound":9`, "PersistWorkerFerryWorker must stamp the [trade_fleet] reposition jump bound so the rebuild reads it back (the o34q write side)")

	// The whole boundary round-trips: the rebuild from the persisted config reloads the stamped
	// bound, so a recovered or coordinator-started ferry still routes over the stored adjacency at
	// the captain's bound — never silently degraded to the strict 5-jump resolver.
	var cfg map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(model.Config), &cfg))
	rebuilt, err := s.buildCommandForType("worker_ferry", cfg, playerID, "ferry-FWXM-BOUND")
	require.NoError(t, err)
	require.Equal(t, 9, rebuilt.(*tradingCmd.WorkerFerryCommand).RepositionJumpBound, "the rebuilt ferry command must reload the reposition jump bound across the persist boundary")
}

// An ABSENT reposition_jump_bound (a ferry persisted from a config that predates the knob) rebuilds
// to 0, which the ferry's Handle resolves to the coordinator default (12, resolveRepositionJumpBound)
// — so the bound is never a magic value baked at the persist layer, yet the ferry still rides the
// bounded resolver rather than the strict Path.
func TestBuildWorkerFerryCommand_AbsentBoundRebuildsToZeroForHandlerDefault(t *testing.T) {
	s, _, playerID := newRecoveryTestServer(t)

	cfg := map[string]interface{}{
		"ship_symbol":    "LIGHT-7",
		"destination":    "X1-C81-MARKET",
		"coordinator_id": "worker_rebalancer_coordinator-1",
	}
	rebuilt, err := s.buildCommandForType("worker_ferry", cfg, playerID, "ferry-FWXM-ABSENT")
	require.NoError(t, err)
	require.Equal(t, 0, rebuilt.(*tradingCmd.WorkerFerryCommand).RepositionJumpBound, "an absent knob must rebuild to 0 so the ferry Handle applies the resolver default (never a persist-layer magic value)")
}
