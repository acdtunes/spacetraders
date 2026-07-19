package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	scoutingCmd "github.com/andrescamacho/spacetraders-go/internal/application/scouting/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-o34q REGRESSION PIN: a coordinator-level fake (fakeScoutDaemonClient) CAPTURES the
// *ScoutRepositionCommand struct directly and never crosses the real
// serialize->config->rebuild boundary, so a coordinator test alone cannot catch
// MaxRepositionJumps being dropped across DaemonClientLocal.PersistContainer's
// scout_reposition case, PersistScoutRepositionWorker's config map, or
// buildScoutRepositionCommand's read-back.
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

// sp-4yse CRITICAL-PATH PIN. The worker's start path rebuilds the command FROM the persisted
// config (StartScoutReposition -> buildScoutRepositionCommand), NOT from the in-memory command,
// so the 0-hop gate-charting intent must survive the serialize->config->rebuild boundary — the
// exact boundary the sp-o34q bound was dropped across. Without this round-trip the LIVE relay
// rebuilds with ChartGateOnArrival=false and charts nothing even on the initial dispatch, so
// the 0-hop backlog (VH23/TD90) never drains despite the coordinator flagging it. A default
// relay (no flag) rebuilds to the plain non-charting reposition — a manning relay never charts.
func TestPersistScoutReposition_ChartGateOnArrival_RoundTripsThroughRebuild(t *testing.T) {
	client, server, db, playerID := newLocalClientHarness(t)

	cmd := &scoutingCmd.ScoutRepositionCommand{
		PlayerID:            shared.MustNewPlayerID(playerID),
		ShipSymbol:          "SAT-0",
		DestinationWaypoint: "X1-DARK-A1",
		CoordinatorID:       "scout-coord-1",
		MaxRepositionJumps:  12,
		ChartGateOnArrival:  true,
	}
	require.NoError(t, client.PersistContainer(context.Background(), daemon.ContainerKindScoutReposition, "relay-chart", uint(playerID), cmd))

	config := containerConfig(t, loadContainerRow(t, db, "relay-chart"))
	require.Equal(t, true, config["chart_gate_on_arrival"], "the persisted relay config must carry the 0-hop charting intent")

	rebuilt, err := server.buildCommandForType("scout_reposition", config, playerID, "relay-chart")
	require.NoError(t, err)
	repoCmd, ok := rebuilt.(*scoutingCmd.ScoutRepositionCommand)
	require.True(t, ok, "scout_reposition must rebuild a ScoutRepositionCommand")
	require.True(t, repoCmd.ChartGateOnArrival, "the rebuilt relay must reload the charting intent — else the live worker degrades to a plain market navigate that never charts (sp-4yse)")

	// A default relay (no flag) rebuilds to the plain non-charting reposition.
	plain := &scoutingCmd.ScoutRepositionCommand{
		PlayerID:            shared.MustNewPlayerID(playerID),
		ShipSymbol:          "SAT-2",
		DestinationWaypoint: "X1-NEAR-A1",
		CoordinatorID:       "scout-coord-1",
	}
	require.NoError(t, client.PersistContainer(context.Background(), daemon.ContainerKindScoutReposition, "relay-plain", uint(playerID), plain))
	plainConfig := containerConfig(t, loadContainerRow(t, db, "relay-plain"))
	plainRebuilt, err := server.buildCommandForType("scout_reposition", plainConfig, playerID, "relay-plain")
	require.NoError(t, err)
	require.False(t, plainRebuilt.(*scoutingCmd.ScoutRepositionCommand).ChartGateOnArrival, "an omitted flag rebuilds to a plain non-charting reposition — a manning relay never charts")
}
