package grpc

import (
	"testing"

	"github.com/stretchr/testify/require"

	goodsCmd "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/commands"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// sp-iv65: the ladder-chase input price ceiling is operator-tunable via [manufacturing]
// config.yaml (RULINGS #5). This is the mandated end-to-end round-trip pin: a captain
// setting input_price_ceiling_multiplier / input_price_ceiling_disabled must produce a
// built goods_factory_coordinator command carrying those values, exercising the REAL launch
// path — live config.yaml → injectManufacturingConfig's launch-config write → the registry
// read in buildGoodsFactoryCoordinatorCommand → the built command — and the sp-ts82 live
// discipline (a stale persisted key is discarded in favor of the current config.yaml).

func newManufacturingFactoryTestServer(live config.ManufacturingConfig) *DaemonServer {
	s := &DaemonServer{
		containerSpecs:      make(map[string]ContainerSpec),
		manufacturingConfig: live,
	}
	s.registerContainerSpecs()
	return s
}

// goodsFactoryLaunchConfig carries the mandatory coordinator identity keys plus whatever
// stale input_price_ceiling_* keys a case wants to plant.
func goodsFactoryLaunchConfig(stale map[string]interface{}) map[string]interface{} {
	cfg := map[string]interface{}{
		"target_good":   "ADVANCED_CIRCUITRY",
		"system_symbol": "X1-TEST",
		"container_id":  "goods-iv65",
	}
	for k, v := range stale {
		cfg[k] = v
	}
	return cfg
}

func buildRecoveredGoodsFactoryCommand(t *testing.T, s *DaemonServer, persisted map[string]interface{}) *goodsCmd.RunFactoryCoordinatorCommand {
	t.Helper()
	got, err := s.buildCommandForType("goods_factory_coordinator", persisted, 3, "goods-iv65")
	require.NoError(t, err)
	cmd, ok := got.(*goodsCmd.RunFactoryCoordinatorCommand)
	require.True(t, ok, "expected *RunFactoryCoordinatorCommand, got %T", got)
	return cmd
}

// A captain configuring input_price_ceiling_multiplier: 2.0 must produce a command carrying
// 2.0 — through the whole config pipeline, not set directly on the struct.
func TestGoodsFactoryResolvesInputPriceCeilingFromLiveConfig(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{InputPriceCeilingMultiplier: 2.0})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(nil))
	require.Equal(t, 2.0, cmd.InputPriceCeilingMultiplier)
	require.False(t, cmd.InputPriceCeilingDisabled)
}

// The emergency disable flag resolves live too (RULINGS #5 off-switch).
func TestGoodsFactoryResolvesInputPriceCeilingDisabledFromLiveConfig(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{InputPriceCeilingDisabled: true})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(nil))
	require.True(t, cmd.InputPriceCeilingDisabled)
}

// Unset live config leaves the multiplier at the 0 sentinel (resolved to the 1.5 default
// downstream in buyGood) and the guard enabled — the daemon never hardcodes an operational
// value into the launch config.
func TestGoodsFactoryUnsetInputPriceCeilingIsZeroSentinelAndEnabled(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(nil))
	require.Equal(t, 0.0, cmd.InputPriceCeilingMultiplier, "unset multiplier must stay the 0 sentinel, not a hardcoded default")
	require.False(t, cmd.InputPriceCeilingDisabled)
}

// sp-ts82 live discipline: a STALE persisted input_price_ceiling_multiplier from a prior
// boot must be discarded in favor of the current config.yaml value on the recovery rebuild.
func TestGoodsFactoryLiveInputPriceCeilingOverridesStalePersisted(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{InputPriceCeilingMultiplier: 1.8})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(map[string]interface{}{
		"input_price_ceiling_multiplier": 9.9, // stale copy from a prior boot
	}))
	require.Equal(t, 1.8, cmd.InputPriceCeilingMultiplier, "live 1.8 must override the stale persisted 9.9")
}

// Symmetric half: dropping the knob from config.yaml (unset live) must CLEAR a stale
// persisted key to the 0 sentinel — otherwise a stale copy would shadow the now-absent live
// value, the exact honesty gap the manufacturingConfigKeys clear-list closes.
func TestGoodsFactoryUnsetLiveClearsStalePersistedInputPriceCeiling(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(map[string]interface{}{
		"input_price_ceiling_multiplier": 9.9,
		"input_price_ceiling_disabled":   true,
	}))
	require.Equal(t, 0.0, cmd.InputPriceCeilingMultiplier, "unset live must clear the stale persisted multiplier")
	require.False(t, cmd.InputPriceCeilingDisabled, "unset live must clear the stale persisted disable flag")
}
