package grpc

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// sp-rh2z: the chain P&L kill-switch is operator-tunable via [manufacturing] config.yaml
// (RULINGS #5). These are the mandated end-to-end round-trip pins: a captain setting
// chain_pnl_kill_threshold_per_hour / chain_pnl_window_hours / chain_pnl_kill_disabled must
// produce a built goods_factory_coordinator command carrying those values through the REAL
// launch path — live config.yaml → injectManufacturingConfig's launch-config write → the
// registry read in buildGoodsFactoryCoordinatorCommand → the built command — and the sp-ts82
// live discipline (a stale persisted key is discarded in favor of the current config.yaml).
// The shared helpers (newManufacturingFactoryTestServer / goodsFactoryLaunchConfig /
// buildRecoveredGoodsFactoryCommand) live in command_factory_input_ceiling_live_test.go.

func TestGoodsFactoryResolvesChainPnLKillFromLiveConfig(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{
		ChainPnLKillThresholdPerHour: 45000,
		ChainPnLWindowHours:          8,
	})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(nil))
	require.Equal(t, 45000, cmd.ChainPnLKillThresholdPerHour)
	require.Equal(t, 8, cmd.ChainPnLWindowHours)
	require.False(t, cmd.ChainPnLKillDisabled)
}

// The emergency disable flag resolves live too (RULINGS #5 off-switch).
func TestGoodsFactoryResolvesChainPnLKillDisabledFromLiveConfig(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{ChainPnLKillDisabled: true})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(nil))
	require.True(t, cmd.ChainPnLKillDisabled)
}

// Unset live config leaves the knobs at the 0 sentinel (resolved to 30000/hr + 6h downstream
// in the coordinator) and the switch enabled — the daemon never hardcodes an operational value
// into the launch config.
func TestGoodsFactoryUnsetChainPnLKillIsZeroSentinelAndEnabled(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(nil))
	require.Equal(t, 0, cmd.ChainPnLKillThresholdPerHour, "unset threshold must stay the 0 sentinel, not a hardcoded default")
	require.Equal(t, 0, cmd.ChainPnLWindowHours, "unset window must stay the 0 sentinel")
	require.False(t, cmd.ChainPnLKillDisabled)
}

// sp-ts82 live discipline: a STALE persisted chain_pnl_kill_threshold_per_hour from a prior
// boot must be discarded in favor of the current config.yaml value on the recovery rebuild.
func TestGoodsFactoryLiveChainPnLKillOverridesStalePersisted(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{ChainPnLKillThresholdPerHour: 25000})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(map[string]interface{}{
		"chain_pnl_kill_threshold_per_hour": 99999, // stale copy from a prior boot
	}))
	require.Equal(t, 25000, cmd.ChainPnLKillThresholdPerHour, "live 25000 must override the stale persisted 99999")
}

// Symmetric half: dropping the knobs from config.yaml (unset live) must CLEAR stale persisted
// keys to the 0 sentinel — otherwise a stale copy would shadow the now-absent live value, the
// exact honesty gap the manufacturingConfigKeys clear-list closes.
func TestGoodsFactoryUnsetLiveClearsStalePersistedChainPnLKill(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(map[string]interface{}{
		"chain_pnl_kill_threshold_per_hour": 99999,
		"chain_pnl_window_hours":            12,
		"chain_pnl_kill_disabled":           true,
	}))
	require.Equal(t, 0, cmd.ChainPnLKillThresholdPerHour, "unset live must clear the stale persisted threshold")
	require.Equal(t, 0, cmd.ChainPnLWindowHours, "unset live must clear the stale persisted window")
	require.False(t, cmd.ChainPnLKillDisabled, "unset live must clear the stale persisted disable flag")
}
