package grpc

import (
	"testing"

	"github.com/stretchr/testify/require"

	goodsCmd "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/commands"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// sp-vdld: the factory-siting coordinator is LIVE BY DEFAULT (Admiral: no dark-shipping)
// and its [manufacturing.siting] weights/caps are operator-tunable (RULINGS #5). These pin
// the end-to-end launch round-trip — live config.yaml → injectSitingConfig's launch-config
// write → the registry read in buildSitingCoordinatorCommand → the built command — and the
// sp-ts82 live discipline (a stale persisted key is discarded in favor of config.yaml).
// Reuses newManufacturingFactoryTestServer (SitingConfig nests under ManufacturingConfig).

func sitingLaunchConfig(stale map[string]interface{}) map[string]interface{} {
	cfg := map[string]interface{}{
		"container_id": "siting-vdld",
		"agent_symbol": "AGENT-1",
	}
	for k, v := range stale {
		cfg[k] = v
	}
	return cfg
}

func buildRecoveredSitingCommand(t *testing.T, s *DaemonServer, persisted map[string]interface{}) *goodsCmd.RunSitingCoordinatorCommand {
	t.Helper()
	got, err := s.buildCommandForType("siting_coordinator", persisted, 3, "siting-vdld")
	require.NoError(t, err)
	cmd, ok := got.(*goodsCmd.RunSitingCoordinatorCommand)
	require.True(t, ok, "expected *RunSitingCoordinatorCommand, got %T", got)
	return cmd
}

// ABSENT config → coordinator ACTIVE (the default-ON contract). With no siting_disabled
// anywhere, the built command carries Disabled=false, so the coordinator runs on deploy
// without any enablement flip — the pin the Admiral's no-dark-shipping ruling mandates.
func TestSitingCoordinatorLiveByDefault(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{})
	cmd := buildRecoveredSitingCommand(t, s, sitingLaunchConfig(nil))
	require.False(t, cmd.Disabled,
		"absent config must leave the siting coordinator ACTIVE (LIVE by default) — no dark-shipping")
	require.Equal(t, 3, cmd.PlayerID)
	require.Equal(t, "siting-vdld", cmd.ContainerID)
}

// The emergency escape hatch resolves live (RULINGS #5 off-switch).
func TestSitingCoordinatorResolvesDisabledFromLiveConfig(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{
		Siting: config.SitingConfig{SitingDisabled: true},
	})
	cmd := buildRecoveredSitingCommand(t, s, sitingLaunchConfig(nil))
	require.True(t, cmd.Disabled)
}

// sp-ts82 live discipline: dropping the escape hatch from config.yaml must CLEAR a stale
// persisted siting_disabled — otherwise a stale disable from a prior boot would silently
// keep the coordinator off after the captain removed it, re-opening the dark-shipping gap.
func TestSitingCoordinatorUnsetLiveClearsStalePersistedDisabled(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{})
	cmd := buildRecoveredSitingCommand(t, s, sitingLaunchConfig(map[string]interface{}{
		"siting_disabled": true, // stale copy from a prior boot
	}))
	require.False(t, cmd.Disabled,
		"unset live must clear the stale persisted disable → coordinator ACTIVE")
}

// A captain tuning weights/caps must produce a command carrying those values — through the
// whole config pipeline, not set directly on the struct.
func TestSitingCoordinatorResolvesWeightsFromLiveConfig(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{
		Siting: config.SitingConfig{
			TickIntervalSecs:    120,
			TopK:                8,
			WeightTourAlignment: 2.0,
			MaxChainsPerSystem:  5,
		},
	})
	cmd := buildRecoveredSitingCommand(t, s, sitingLaunchConfig(nil))
	require.Equal(t, 120, cmd.TickIntervalSecs)
	require.Equal(t, 8, cmd.TopK)
	require.Equal(t, 2.0, cmd.WeightTourAlignment)
	require.Equal(t, 5, cmd.MaxChainsPerSystem)
}

// sp-ts82: a STALE persisted weight from a prior boot must be discarded for the current
// config.yaml value on the recovery rebuild.
func TestSitingCoordinatorLiveWeightOverridesStalePersisted(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{
		Siting: config.SitingConfig{WeightTourAlignment: 1.5},
	})
	cmd := buildRecoveredSitingCommand(t, s, sitingLaunchConfig(map[string]interface{}{
		"siting_weight_tour_alignment": 9.9, // stale copy from a prior boot
	}))
	require.Equal(t, 1.5, cmd.WeightTourAlignment, "live 1.5 must override the stale persisted 9.9")
}

// Unset live leaves the numeric knobs at the 0 sentinel (resolved to defaults downstream in
// resolveSitingConfig) — the daemon never hardcodes an operational value into the launch config.
func TestSitingCoordinatorUnsetKnobsAreZeroSentinel(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{})
	cmd := buildRecoveredSitingCommand(t, s, sitingLaunchConfig(nil))
	require.Equal(t, 0, cmd.TickIntervalSecs, "unset tick must stay the 0 sentinel, not a hardcoded default")
	require.Equal(t, 0.0, cmd.WeightTourAlignment)
	require.Equal(t, 0, cmd.TopK)
}
