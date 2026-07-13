package grpc

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// sp-jav2 / FACTORY_DOCTRINE X1: the fabricate depth cap is operator-tunable via [manufacturing]
// config.yaml (RULINGS #5). These are the end-to-end round-trip pins: a captain setting
// fabricate_max_depth / fabricate_depth_cap_disabled must produce a built goods_factory_coordinator
// command carrying those values through the REAL launch path — live config.yaml →
// injectManufacturingConfig's launch-config write → the registry read in
// buildGoodsFactoryCoordinatorCommand → the built command — and the sp-ts82 live discipline (a
// stale persisted key is discarded in favor of the current config.yaml). The shared helpers
// (newManufacturingFactoryTestServer / goodsFactoryLaunchConfig / buildRecoveredGoodsFactoryCommand)
// live in command_factory_input_ceiling_live_test.go.

func TestGoodsFactoryResolvesFabricateDepthFromLiveConfig(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{
		FabricateMaxDepth: 2,
	})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(nil))
	require.Equal(t, 2, cmd.FabricateMaxDepth)
	require.False(t, cmd.FabricateDepthCapDisabled)
}

// The emergency disable flag resolves live too (RULINGS #5 off-switch).
func TestGoodsFactoryResolvesFabricateDepthDisabledFromLiveConfig(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{FabricateDepthCapDisabled: true})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(nil))
	require.True(t, cmd.FabricateDepthCapDisabled)
}

// Unset live config leaves the depth at the 0 sentinel (resolved to depth-3 downstream in the
// resolver) and the cap enabled — the daemon never hardcodes an operational value into the launch
// config.
func TestGoodsFactoryUnsetFabricateDepthIsZeroSentinelAndEnabled(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(nil))
	require.Equal(t, 0, cmd.FabricateMaxDepth, "unset depth must stay the 0 sentinel, not a hardcoded default")
	require.False(t, cmd.FabricateDepthCapDisabled)
}

// sp-ts82 live discipline: dropping the knobs from config.yaml (unset live) must CLEAR stale
// persisted keys — otherwise a stale copy would shadow the now-absent live value. This guards that
// fabricate_max_depth / fabricate_depth_cap_disabled were added to the manufacturingConfigKeys
// clear-list.
func TestGoodsFactoryUnsetLiveClearsStalePersistedFabricateDepth(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(map[string]interface{}{
		"fabricate_max_depth":          9,
		"fabricate_depth_cap_disabled": true,
	}))
	require.Equal(t, 0, cmd.FabricateMaxDepth, "unset live must clear the stale persisted depth")
	require.False(t, cmd.FabricateDepthCapDisabled, "unset live must clear the stale persisted disable flag")
}
