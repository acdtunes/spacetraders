package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	capacityCmd "github.com/andrescamacho/spacetraders-go/internal/application/capacity/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// DEPLOY-INERT pin (st-fyr hard requirement): a fresh deploy of the capacity
// reconciler changes NOTHING for live players. The engine must ONLY run when
// explicitly started (workflow capacity-reconciler / the RPC) — it must NEVER
// be boot-standing-armed the way the market-freshness sizer is (sp-orgp,
// bootStandingCoordinatorTypes). If a future change adds it to the boot set,
// this test is the tripwire.
func TestCapacityReconciler_NotBootStandingArmed(t *testing.T) {
	for _, ct := range bootStandingCoordinatorTypes {
		require.NotEqual(t, container.ContainerTypeCapacityReconciler, ct,
			"the capacity reconciler must stay deploy-inert: never boot-standing-armed")
	}
}

// Double-launch guard: a second explicit start for a player who already has an
// ACTIVE (PENDING/RUNNING) reconciler must refuse loudly instead of spawning a
// twin standing loop — two reconcilers would double-execute tier-1..3 actions
// and double-file capital proposals once the actuation lanes land.
func TestCapacityReconcilerCoordinatorRefusesDoubleLaunch(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	insertRunningContainer(t, db, "capacity-reconciler-existing", "capacity_reconciler_coordinator",
		string(container.ContainerTypeCapacityReconciler), `{"container_id":"capacity-reconciler-existing"}`, playerID, nil)

	_, err := s.CapacityReconcilerCoordinator(context.Background(), playerID)

	require.Error(t, err)
	require.Contains(t, err.Error(), "already running")
	require.Contains(t, err.Error(), "capacity-reconciler-existing",
		"the refusal must name the existing container so the operator can stop it")
}

// Restart recovery (RULINGS #2): a RUNNING capacity reconciler container must
// rebuild byte-identically from its persisted launch config through the SAME
// buildCommandForType the creation path uses — launch and recovery can never
// drift. The persisted config round-trips through JSON exactly as the
// container repository stores it.
func TestRecoveryFactoryRebuildsCapacityReconcilerCommand(t *testing.T) {
	s := newFactoryTestServer()

	launchConfig := map[string]interface{}{
		"container_id": "capacity-reconciler-7",
	}

	built, err := s.buildCommandForType("capacity_reconciler_coordinator", jsonRoundTrip(t, launchConfig), 7, "capacity-reconciler-7")
	require.NoError(t, err)

	cmd, ok := built.(*capacityCmd.RunCapacityReconcilerCoordinatorCommand)
	require.True(t, ok, "expected *RunCapacityReconcilerCoordinatorCommand, got %T", built)
	require.Equal(t, &capacityCmd.RunCapacityReconcilerCoordinatorCommand{
		PlayerID:    shared.MustNewPlayerID(7),
		ContainerID: "capacity-reconciler-7",
		// Every calibration knob zero: the coordinator resolves its documented
		// defaults (per-decision cap 25% included) at launch.
	}, cmd)
}

// Live-config discipline (the sp-ts82 pattern): the [capacity_reconciler]
// knobs are cleared and re-injected from the boot-loaded config.yaml on every
// build — creation and recovery alike — so a config edit + daemon restart
// retunes a recovered coordinator, and a stale persisted copy can never
// shadow the live value.
func TestCapacityReconcilerConfigResolvesLiveFromConfigYAML(t *testing.T) {
	s := newFactoryTestServer()
	s.capacityReconcilerConfig = config.CapacityReconcilerConfig{
		ReserveFloor:            400000,
		SurplusFraction:         0.1,
		PerDecisionCapPct:       10,
		ROIPaybackHorizonHours:  6,
		AddThresholdPerHullCrHr: 1500,
		StockerCapacityBudget:   240,
		TickIntervalSecs:        60,
		ApprovalThreshold:       250000,
	}

	// The persisted config carries STALE knob values from a prior boot; the
	// resolve must clear them and re-inject the live config.yaml values.
	persisted := map[string]interface{}{
		"container_id":                  "capacity-reconciler-7",
		"capacity_reserve_floor":        999999,
		"capacity_surplus_fraction":     0.9,
		"capacity_per_decision_cap_pct": 99,
	}

	built, err := s.buildCommandForType("capacity_reconciler_coordinator", jsonRoundTrip(t, persisted), 7, "capacity-reconciler-7")
	require.NoError(t, err)

	cmd := built.(*capacityCmd.RunCapacityReconcilerCoordinatorCommand)
	require.Equal(t, int64(400000), cmd.ReserveFloorCredits)
	require.Equal(t, 0.1, cmd.SurplusFraction)
	require.Equal(t, 10, cmd.PerDecisionCapPct)
	require.Equal(t, float64(6), cmd.ROIPaybackHorizonHours)
	require.Equal(t, float64(1500), cmd.AddThresholdPerHullCrHr)
	require.Equal(t, 240, cmd.StockerCapacityBudget)
	require.Equal(t, 60, cmd.TickIntervalSecs)
	require.Equal(t, int64(250000), cmd.ApprovalThresholdCredits)
}

// Dropping a knob from config.yaml must fall back to the coordinator's own
// default rather than staying shadowed by a stale persisted copy — the clear
// half of the live-config discipline.
func TestCapacityReconcilerConfigUnsetKnobFallsBackToDefault(t *testing.T) {
	s := newFactoryTestServer() // zero capacityReconcilerConfig: nothing set in config.yaml

	persisted := map[string]interface{}{
		"container_id":                  "capacity-reconciler-7",
		"capacity_per_decision_cap_pct": 99, // stale from a prior boot
	}

	built, err := s.buildCommandForType("capacity_reconciler_coordinator", jsonRoundTrip(t, persisted), 7, "capacity-reconciler-7")
	require.NoError(t, err)

	cmd := built.(*capacityCmd.RunCapacityReconcilerCoordinatorCommand)
	require.Zero(t, cmd.PerDecisionCapPct,
		"a knob dropped from config.yaml must resolve to zero (= the coordinator's documented default), not the stale persisted 99")
}
