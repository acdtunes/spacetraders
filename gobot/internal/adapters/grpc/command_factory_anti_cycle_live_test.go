package grpc

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// sp-r5a6: the input-poison anti-cycle is operator/analyst-tunable via [manufacturing]
// config.yaml (RULINGS #5). These are the mandated end-to-end round-trip pins: a captain setting
// input_recovery_reattempt_minutes / anti_cycle_disabled must produce a built
// goods_factory_coordinator command carrying those values through the REAL launch path — live
// config.yaml → injectManufacturingConfig's launch-config write → the registry read in
// buildGoodsFactoryCoordinatorCommand → the built command — and the sp-ts82 live discipline (a
// stale persisted key is discarded in favor of the current config.yaml). The shared helpers
// (newManufacturingFactoryTestServer / goodsFactoryLaunchConfig / buildRecoveredGoodsFactoryCommand)
// live in command_factory_input_ceiling_live_test.go.

func TestGoodsFactoryResolvesAntiCycleFromLiveConfig(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{
		InputRecoveryReattemptMinutes: 240,
	})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(nil))
	require.Equal(t, 240, cmd.InputRecoveryReattemptMinutes)
	require.False(t, cmd.AntiCycleDisabled)
}

// The emergency disable flag resolves live too (RULINGS #5 off-switch).
func TestGoodsFactoryResolvesAntiCycleDisabledFromLiveConfig(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{AntiCycleDisabled: true})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(nil))
	require.True(t, cmd.AntiCycleDisabled)
}

// Unset live config leaves the half-life at the 0 sentinel (resolved to 194min downstream in the
// coordinator) and the anti-cycle enabled — the daemon never hardcodes an operational value into
// the launch config.
func TestGoodsFactoryUnsetAntiCycleIsZeroSentinelAndEnabled(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(nil))
	require.Equal(t, 0, cmd.InputRecoveryReattemptMinutes, "unset half-life must stay the 0 sentinel, not a hardcoded default")
	require.False(t, cmd.AntiCycleDisabled)
}

// sp-ts82 live discipline: a STALE persisted input_recovery_reattempt_minutes from a prior boot
// must be discarded in favor of the current config.yaml value on the recovery rebuild.
func TestGoodsFactoryLiveAntiCycleOverridesStalePersisted(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{InputRecoveryReattemptMinutes: 120})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(map[string]interface{}{
		"input_recovery_reattempt_minutes": 99999, // stale copy from a prior boot
	}))
	require.Equal(t, 120, cmd.InputRecoveryReattemptMinutes, "live 120 must override the stale persisted 99999")
}

// Symmetric half: dropping the knobs from config.yaml (unset live) must CLEAR stale persisted
// keys — otherwise a stale copy would shadow the now-absent live value, the exact honesty gap the
// manufacturingConfigKeys clear-list closes.
func TestGoodsFactoryUnsetLiveClearsStalePersistedAntiCycle(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(map[string]interface{}{
		"input_recovery_reattempt_minutes": 99999,
		"anti_cycle_disabled":              true,
	}))
	require.Equal(t, 0, cmd.InputRecoveryReattemptMinutes, "unset live must clear the stale persisted half-life")
	require.False(t, cmd.AntiCycleDisabled, "unset live must clear the stale persisted disable flag")
}
