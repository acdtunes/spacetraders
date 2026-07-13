package grpc

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// sp-yfzi: the production acquisition strategy is operator-tunable via [manufacturing]
// production_strategy (RULINGS #5). These are the end-to-end round-trip pins through the REAL launch
// path — live config.yaml -> injectManufacturingConfig -> registry read in
// buildGoodsFactoryCoordinatorCommand -> the built command — plus the sp-ts82 live discipline (a
// stale persisted key is discarded in favour of the current config.yaml). The shared helpers
// (newManufacturingFactoryTestServer / goodsFactoryLaunchConfig / buildRecoveredGoodsFactoryCommand)
// live in command_factory_input_ceiling_live_test.go.

// Unset live config resolves to the scarcity-gated "smart" default: recursive production runs ON
// without the captain naming it (the sp-yfzi Admiral directive).
func TestGoodsFactoryUnsetProductionStrategyDefaultsToSmart(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(nil))
	require.Equal(t, "smart", cmd.ProductionStrategy, "unset production_strategy must default to smart")
}

// A captain pinning "prefer-buy" dials back to the sp-jav2 buy-all-inputs posture — resolved live.
func TestGoodsFactoryResolvesProductionStrategyFromLiveConfig(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{ProductionStrategy: "prefer-buy"})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(nil))
	require.Equal(t, "prefer-buy", cmd.ProductionStrategy)
}

// sp-ts82 live discipline: dropping production_strategy from config.yaml (unset live) must CLEAR a
// stale persisted key rather than let it shadow the now-absent live value — so it reverts to the
// smart default. This guards that production_strategy was added to the manufacturingConfigKeys
// clear-list.
func TestGoodsFactoryUnsetLiveClearsStalePersistedProductionStrategy(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(map[string]interface{}{
		"production_strategy": "prefer-fabricate",
	}))
	require.Equal(t, "smart", cmd.ProductionStrategy, "unset live must clear the stale persisted strategy and revert to smart")
}
