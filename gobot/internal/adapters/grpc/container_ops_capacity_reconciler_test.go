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

// BOOT-STANDING-ARMED (sp-ov8z, epic sp-difa — the ARMING half of zero-intervention cold start).
// This DELIBERATELY reverses the earlier "st-fyr deploy-inert hard requirement": the reconciler is
// now a member of bootStandingCoordinatorTypes so an era transition + daemon boot self-starts it
// with no manual `workflow capacity-reconciler`. The reversal is safe ONLY because sp-2jrz
// (stop-is-complete-retire) landed — a mid-era restart re-adopts a live reconciler unchanged, and a
// decommission STOP retires it cleanly. NOTE (flagged on sp-ov8z): once boot-standing, a bare STOP no
// longer decommissions the reconciler across a restart — boot re-launches it — so a durable
// decommission additionally needs config dry_run/disable (see the sp-udgc demand-driven-boot-guard
// pattern). If this reverts to deploy-inert, flip this back to NotEqual.
func TestCapacityReconciler_IsBootStandingArmed(t *testing.T) {
	require.Contains(t, bootStandingCoordinatorTypes, container.ContainerTypeCapacityReconciler,
		"the capacity reconciler must be boot-standing-armed for zero-intervention cold start (sp-ov8z, safe post-sp-2jrz)")
}

// Double-launch guard: a second explicit start for a player who already has an
// ACTIVE (PENDING/RUNNING) reconciler must refuse loudly instead of spawning a
// twin standing loop — two reconcilers would double-execute tier-1..3 actions
// and double-file capital proposals once the actuation lanes land.
func TestCapacityReconcilerCoordinatorRefusesDoubleLaunch(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	insertRunningContainer(t, db, "capacity-reconciler-existing", "capacity_reconciler_coordinator",
		string(container.ContainerTypeCapacityReconciler), `{"container_id":"capacity-reconciler-existing"}`, playerID, nil)

	_, err := s.CapacityReconcilerCoordinator(context.Background(), playerID, false)

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

// DryRun default (st-y07): with nothing set — no config.yaml dry_run and no
// launch --dry-run flag persisted — a rebuilt coordinator is ARMED. Observe-only
// is strictly opt-in; a fresh deploy never silently freezes the engine.
func TestCapacityReconcilerDryRunDefaultsOff(t *testing.T) {
	s := newFactoryTestServer() // zero capacityReconcilerConfig: dry_run unset in config.yaml

	built, err := s.buildCommandForType("capacity_reconciler_coordinator",
		jsonRoundTrip(t, map[string]interface{}{"container_id": "capacity-reconciler-7"}), 7, "capacity-reconciler-7")
	require.NoError(t, err)

	cmd := built.(*capacityCmd.RunCapacityReconcilerCoordinatorCommand)
	require.False(t, cmd.DryRun, "DryRun must default OFF — observe-only is opt-in, never the default posture")
}

// DryRun from config.yaml (st-y07): setting [capacity_reconciler] dry_run=true
// arms every rebuild of the coordinator observe-only, resolved LIVE like the
// calibration knobs — the capacity_dry_run key is cleared and re-injected from
// the boot-loaded config on every build, so a config edit + daemon restart
// freezes a recovered coordinator to watch mode.
func TestCapacityReconcilerDryRunResolvesLiveFromConfigYAML(t *testing.T) {
	s := newFactoryTestServer()
	s.capacityReconcilerConfig = config.CapacityReconcilerConfig{DryRun: true}

	built, err := s.buildCommandForType("capacity_reconciler_coordinator",
		jsonRoundTrip(t, map[string]interface{}{"container_id": "capacity-reconciler-7"}), 7, "capacity-reconciler-7")
	require.NoError(t, err)

	cmd := built.(*capacityCmd.RunCapacityReconcilerCoordinatorCommand)
	require.True(t, cmd.DryRun, "[capacity_reconciler] dry_run=true must arm the coordinator observe-only")
}

// DryRun launch-flag survives recovery (st-y07, mirrors bootstrap's
// bootstrap_launch_dry_run): a container started with the CLI --dry-run persists
// capacity_launch_dry_run as an IDENTITY key (NOT a live-config key), so it is
// preserved verbatim through the buildCommandForType round-trip. A dry-run
// container therefore STAYS dry-run across a daemon restart even when config.yaml
// says nothing — a daemon bounce must never silently arm an engine an operator
// deliberately launched in watch mode.
func TestCapacityReconcilerDryRunLaunchFlagSurvivesRecovery(t *testing.T) {
	s := newFactoryTestServer() // config.yaml dry_run absent; only the launch flag is set

	persisted := map[string]interface{}{
		"container_id":            "capacity-reconciler-7",
		"capacity_launch_dry_run": true, // the CLI --dry-run decision, persisted at launch
	}

	built, err := s.buildCommandForType("capacity_reconciler_coordinator", jsonRoundTrip(t, persisted), 7, "capacity-reconciler-7")
	require.NoError(t, err)

	cmd := built.(*capacityCmd.RunCapacityReconcilerCoordinatorCommand)
	require.True(t, cmd.DryRun,
		"a --dry-run launch must survive restart recovery — the persisted launch flag is not a live-config key that gets cleared")
}

// DryRun launch WRITE side (st-y07): the CLI --dry-run flag, threaded through the
// RPC into CapacityReconcilerCoordinator, must persist capacity_launch_dry_run on
// the container so recovery can read it back (the READ side is proven above). An
// armed launch persists no such key, so it never accidentally freezes on restart.
func TestCapacityReconcilerCoordinatorPersistsLaunchDryRunFlag(t *testing.T) {
	cases := []struct {
		name       string
		dryRun     bool
		wantKeySet bool
	}{
		{name: "dry-run launch persists the sticky flag", dryRun: true, wantKeySet: true},
		{name: "armed launch persists no dry-run flag", dryRun: false, wantKeySet: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, playerID := newRecoveryTestServer(t)
			ctx := context.Background()

			containerID, err := s.CapacityReconcilerCoordinator(ctx, playerID, tc.dryRun)
			require.NoError(t, err)
			// Release the blocking launch goroutine so it exits cleanly.
			if r := s.registeredRunner(containerID); r != nil {
				r.cancelFunc()
			}

			model, err := s.containerRepo.Get(ctx, containerID, playerID)
			require.NoError(t, err)
			if tc.wantKeySet {
				require.Contains(t, model.Config, "capacity_launch_dry_run",
					"a --dry-run launch must persist the sticky flag so recovery keeps it dry-run")
			} else {
				require.NotContains(t, model.Config, "capacity_launch_dry_run",
					"an armed launch must persist NO dry-run flag — restart must not silently freeze it")
			}
		})
	}
}
