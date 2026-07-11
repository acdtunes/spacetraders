package grpc

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// sp-a5j7 Phase 2: supply-first sourcing is operator-tunable via [manufacturing] config.yaml
// (RULINGS #5), the same live-config discipline as the iv65 ceiling. These are the mandated
// end-to-end round-trip pins: a captain setting input_rescue_multiplier /
// input_era_end_price_first / input_sourcing_disabled must produce a built
// goods_factory_coordinator command carrying those values through the REAL launch path — live
// config.yaml -> injectManufacturingConfig -> the registry read -> the built command — with the
// sp-ts82 live discipline (a stale persisted key is discarded for the current config.yaml).
// Helpers (newManufacturingFactoryTestServer, goodsFactoryLaunchConfig,
// buildRecoveredGoodsFactoryCommand) are shared with the ceiling live test in this package.

func TestGoodsFactoryResolvesRescueMultiplierFromLiveConfig(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{InputRescueMultiplier: 1.3})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(nil))
	require.Equal(t, 1.3, cmd.InputRescueMultiplier)
	require.False(t, cmd.InputEraEndPriceFirst)
	require.False(t, cmd.InputSourcingDisabled)
}

func TestGoodsFactoryResolvesEraEndAndDisabledFromLiveConfig(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{InputEraEndPriceFirst: true, InputSourcingDisabled: true})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(nil))
	require.True(t, cmd.InputEraEndPriceFirst)
	require.True(t, cmd.InputSourcingDisabled)
}

// Unset live config leaves the rescue multiplier at the 0 sentinel (resolved to the 1.2 default
// downstream) with supply-first ON — the daemon never hardcodes an operational value.
func TestGoodsFactoryUnsetSourcingIsZeroSentinelAndSupplyFirstOn(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(nil))
	require.Equal(t, 0.0, cmd.InputRescueMultiplier, "unset multiplier must stay the 0 sentinel, not a hardcoded default")
	require.False(t, cmd.InputEraEndPriceFirst)
	require.False(t, cmd.InputSourcingDisabled, "supply-first must stay ON by default")
}

// sp-ts82 live discipline: a STALE persisted key must be discarded for the current config.yaml.
func TestGoodsFactoryLiveSourcingOverridesStalePersisted(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{InputRescueMultiplier: 1.25})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(map[string]interface{}{
		"input_rescue_multiplier": 9.9, // stale copy from a prior boot
	}))
	require.Equal(t, 1.25, cmd.InputRescueMultiplier, "live 1.25 must override the stale persisted 9.9")
}

// Symmetric half: dropping the knobs from config.yaml (unset live) must CLEAR stale persisted
// keys — otherwise a stale copy would shadow the now-absent live value.
func TestGoodsFactoryUnsetLiveClearsStalePersistedSourcing(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(map[string]interface{}{
		"input_rescue_multiplier":   9.9,
		"input_era_end_price_first": true,
		"input_sourcing_disabled":   true,
	}))
	require.Equal(t, 0.0, cmd.InputRescueMultiplier, "unset live must clear the stale persisted multiplier")
	require.False(t, cmd.InputEraEndPriceFirst, "unset live must clear the stale era-end flag")
	require.False(t, cmd.InputSourcingDisabled, "unset live must clear the stale disable flag")
}
