package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	scoutingCmd "github.com/andrescamacho/spacetraders-go/internal/application/scouting/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-o34q REGRESSION PIN. 8k9m wired the expendable-probe reposition bound through the
// in-memory call chain (RepositionToWaypointWithinJumps -> travelWithJumpBound -> jumpPath
// -> RepositionPath) and every coordinator test passed — because fakeScoutDaemonClient
// CAPTURES the *ScoutRepositionCommand struct directly and never crosses the real
// serialize->config->rebuild boundary. That boundary dropped MaxRepositionJumps at three
// sites: DaemonClientLocal.PersistContainer's scout_reposition case, PersistScoutRepositionWorker's
// config map, and buildScoutRepositionCommand's read-back. So the LIVE reposition container ran
// with bound 0, jumpPath fell through to the strict fetch-through Path, and a 12-jump post
// produced the verbatim ErrUnroutable "within 5 jumps" — four corpses, crash-loop.
//
// This pins the WHOLE round-trip the coordinator fakes cannot: persist a relay at bound 9 ->
// the persisted launch config carries max_reposition_jumps -> a start/recovery rebuild reloads
// it onto the command, so the worker's travel resolves RepositionPath at the SAME reach the
// coordinator selected instead of the strict 5-jump cap.
func TestPersistScoutReposition_MaxRepositionJumps_RoundTripsThroughRebuild(t *testing.T) {
	client, server, db, playerID := newLocalClientHarness(t)

	cmd := &scoutingCmd.ScoutRepositionCommand{
		PlayerID:            shared.MustNewPlayerID(playerID),
		ShipSymbol:          "SAT-1",
		DestinationWaypoint: "X1-FAR-A1",
		CoordinatorID:       "scout-coord-1",
		MaxRepositionJumps:  9,
	}

	require.NoError(t, client.PersistContainer(context.Background(), daemon.ContainerKindScoutReposition, "relay-1", uint(playerID), cmd))

	// The persisted launch config must carry the bound (the segment 8k9m dropped).
	model := loadContainerRow(t, db, "relay-1")
	require.Equal(t, "SCOUT_REPOSITION", model.ContainerType)
	require.NotNil(t, model.ParentContainerID)
	require.Equal(t, "scout-coord-1", *model.ParentContainerID, "the relay links its coordinator parent")
	config := containerConfig(t, model)
	require.EqualValues(t, 9, config["max_reposition_jumps"], "the persisted relay config must carry the reposition bound the coordinator selected")

	// A start/recovery rebuild from that config must reload the bound onto the command, so
	// travelWithJumpBound resolves RepositionPath at the same reach (not the strict 5-jump cap).
	rebuilt, err := server.buildCommandForType("scout_reposition", config, playerID, "relay-1")
	require.NoError(t, err)
	repoCmd, ok := rebuilt.(*scoutingCmd.ScoutRepositionCommand)
	require.True(t, ok, "scout_reposition must rebuild a ScoutRepositionCommand")
	require.Equal(t, 9, repoCmd.MaxRepositionJumps, "the rebuilt relay must reload the bound — else the worker degrades to the strict resolver")
	require.Equal(t, "SAT-1", repoCmd.ShipSymbol)
	require.Equal(t, "X1-FAR-A1", repoCmd.DestinationWaypoint)
	require.Equal(t, "scout-coord-1", repoCmd.CoordinatorID)
}

// A relay persisted with NO explicit bound (the mis-wired / legacy-config case) rebuilds at
// bound 0 — which travelWithJumpBound degrades to the STRICT resolver. This pins the safe
// fallback: an omitted bound can never accidentally relax the sp-qxa4 unreadable-gate
// discipline; only an explicitly persisted positive bound routes past unreadable gates.
func TestPersistScoutReposition_NoBound_RebuildsStrictFallback(t *testing.T) {
	client, server, db, playerID := newLocalClientHarness(t)

	cmd := &scoutingCmd.ScoutRepositionCommand{
		PlayerID:            shared.MustNewPlayerID(playerID),
		ShipSymbol:          "SAT-2",
		DestinationWaypoint: "X1-NEAR-A1",
		CoordinatorID:       "scout-coord-1",
		// MaxRepositionJumps omitted (0)
	}

	require.NoError(t, client.PersistContainer(context.Background(), daemon.ContainerKindScoutReposition, "relay-2", uint(playerID), cmd))

	config := containerConfig(t, loadContainerRow(t, db, "relay-2"))
	rebuilt, err := server.buildCommandForType("scout_reposition", config, playerID, "relay-2")
	require.NoError(t, err)
	repoCmd := rebuilt.(*scoutingCmd.ScoutRepositionCommand)
	require.Equal(t, 0, repoCmd.MaxRepositionJumps, "an omitted bound rebuilds at 0 — the strict-resolver fallback, never an accidental relaxation")
}
