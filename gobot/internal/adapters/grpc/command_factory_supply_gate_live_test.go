package grpc

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// sp-a5j7: the LEADING supply-state gate is operator-tunable via [manufacturing] config.yaml
// (RULINGS #5), the same live-config discipline as the iv65 ceiling. These are the mandated
// end-to-end round-trip pins: a captain setting input_supply_gate_park_level /
// input_supply_gate_disabled must produce a built goods_factory_coordinator command carrying
// those values through the REAL launch path — live config.yaml → injectManufacturingConfig's
// launch-config write → the registry read in buildGoodsFactoryCoordinatorCommand → the built
// command — with the sp-ts82 live discipline (a stale persisted key is discarded for the
// current config.yaml). Helpers (newManufacturingFactoryTestServer, goodsFactoryLaunchConfig,
// buildRecoveredGoodsFactoryCommand) are shared with the ceiling live test in this package.

// A captain configuring input_supply_gate_park_level: "LIMITED" must produce a command
// carrying it — through the whole config pipeline, not set directly on the struct.
func TestGoodsFactoryResolvesSupplyGateParkLevelFromLiveConfig(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{InputSupplyGateParkLevel: "LIMITED"})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(nil))
	require.Equal(t, "LIMITED", cmd.InputSupplyGateParkLevel)
	require.False(t, cmd.InputSupplyGateDisabled)
}

// The emergency disable flag resolves live too (RULINGS #5 off-switch).
func TestGoodsFactoryResolvesSupplyGateDisabledFromLiveConfig(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{InputSupplyGateDisabled: true})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(nil))
	require.True(t, cmd.InputSupplyGateDisabled)
}

// Unset live config leaves the park level at the "" sentinel (resolved to the SCARCE default
// downstream in buyGood) and the guard enabled — the daemon never hardcodes an operational
// value into the launch config.
func TestGoodsFactoryUnsetSupplyGateIsEmptySentinelAndEnabled(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(nil))
	require.Equal(t, "", cmd.InputSupplyGateParkLevel, "unset park level must stay the empty sentinel, not a hardcoded default")
	require.False(t, cmd.InputSupplyGateDisabled)
}

// sp-ts82 live discipline: a STALE persisted input_supply_gate_park_level from a prior boot
// must be discarded in favor of the current config.yaml value on the recovery rebuild.
func TestGoodsFactoryLiveSupplyGateOverridesStalePersisted(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{InputSupplyGateParkLevel: "LIMITED"})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(map[string]interface{}{
		"input_supply_gate_park_level": "MODERATE", // stale copy from a prior boot
	}))
	require.Equal(t, "LIMITED", cmd.InputSupplyGateParkLevel, "live LIMITED must override the stale persisted MODERATE")
}

// Symmetric half: dropping the knob from config.yaml (unset live) must CLEAR a stale persisted
// key to the empty sentinel — otherwise a stale copy would shadow the now-absent live value,
// the exact honesty gap the manufacturingConfigKeys clear-list closes.
func TestGoodsFactoryUnsetLiveClearsStalePersistedSupplyGate(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(map[string]interface{}{
		"input_supply_gate_park_level": "MODERATE",
		"input_supply_gate_disabled":   true,
	}))
	require.Equal(t, "", cmd.InputSupplyGateParkLevel, "unset live must clear the stale persisted park level")
	require.False(t, cmd.InputSupplyGateDisabled, "unset live must clear the stale persisted disable flag")
}
