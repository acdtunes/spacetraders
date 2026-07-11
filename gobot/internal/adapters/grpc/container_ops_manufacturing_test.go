package grpc

import (
	"testing"

	"github.com/stretchr/testify/require"

	goodsCmd "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/commands"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// buildGoodsFactoryCmd runs the full config round-trip the daemon uses: inject the
// [manufacturing] knobs into a launch config, then rebuild the command from it
// (exactly what creation and restart recovery do via resolveManufacturingConfig).
func buildGoodsFactoryCmd(t *testing.T, mfg config.ManufacturingConfig) *goodsCmd.RunFactoryCoordinatorCommand {
	t.Helper()
	s := &DaemonServer{manufacturingConfig: mfg}
	cfgMap := map[string]interface{}{
		"target_good":   "MICROPROCESSORS",
		"system_symbol": "X1-TEST",
		"container_id":  "goods-cfg-1",
	}
	s.injectManufacturingConfig(cfgMap)

	built := buildGoodsFactoryCoordinatorCommand(newConfigReader(cfgMap), 1, "goods-cfg-1")
	cmd, ok := built.(*goodsCmd.RunFactoryCoordinatorCommand)
	require.True(t, ok, "build must return *RunFactoryCoordinatorCommand")
	require.Equal(t, "goods-cfg-1", cmd.ContainerID)
	return cmd
}

// buildManufacturingCmd is buildGoodsFactoryCmd's counterpart for
// manufacturing_coordinator (the parallel task-based pipeline coordinator) — the
// other build path sp-kk61 wires to the same [manufacturing] knob.
func buildManufacturingCmd(t *testing.T, mfg config.ManufacturingConfig) *goodsCmd.RunParallelManufacturingCoordinatorCommand {
	t.Helper()
	s := &DaemonServer{manufacturingConfig: mfg}
	cfgMap := map[string]interface{}{
		"system_symbol": "X1-TEST",
		"container_id":  "mfg-cfg-1",
	}
	s.injectManufacturingConfig(cfgMap)

	built := buildManufacturingCoordinatorCommand(newConfigReader(cfgMap), 1, "mfg-cfg-1")
	cmd, ok := built.(*goodsCmd.RunParallelManufacturingCoordinatorCommand)
	require.True(t, ok, "build must return *RunParallelManufacturingCoordinatorCommand")
	require.Equal(t, "mfg-cfg-1", cmd.ContainerID)
	return cmd
}

// An empty [manufacturing] section leaves WorkingCapitalReserve at 0 — the pre-sp-kk61
// behavior, preserved. Downstream, goods_factory_coordinator's own immutable 50000
// lower bound (sp-agzj's effectiveReserveFloor = max(50000, configured)) is untouched;
// manufacturing_coordinator simply carries no reserve, matching its no-floor purchaser.
func TestManufacturingConfig_DefaultUnset(t *testing.T) {
	factoryCmd := buildGoodsFactoryCmd(t, config.ManufacturingConfig{})
	require.Equal(t, 0, factoryCmd.WorkingCapitalReserve, "unset config must leave the knob at 0 (defers to the 50k floor)")

	mfgCmd := buildManufacturingCmd(t, config.ManufacturingConfig{})
	require.Equal(t, 0, mfgCmd.WorkingCapitalReserve)
}

// A configured reserve reaches BOTH command types identically — this is the gap
// sp-kk61 closes: before this, no CLI flag or config key populated
// RunFactoryCoordinatorCommand.WorkingCapitalReserve at all, so every factory was
// stuck at the 50k floor with no operator-reachable knob.
func TestManufacturingConfig_ConfiguredReserveReachesBothCommandTypes(t *testing.T) {
	mfg := config.ManufacturingConfig{WorkingCapitalReserve: 1000000}

	factoryCmd := buildGoodsFactoryCmd(t, mfg)
	require.Equal(t, 1000000, factoryCmd.WorkingCapitalReserve, "goods_factory_coordinator must carry the configured 1M reserve")

	mfgCmd := buildManufacturingCmd(t, mfg)
	require.Equal(t, 1000000, mfgCmd.WorkingCapitalReserve, "manufacturing_coordinator must carry the same configured 1M reserve")
}

// resolveManufacturingConfig makes config.yaml the live source of truth for BOTH
// command types: a stale working_capital_reserve persisted at a prior boot is cleared
// and not allowed to shadow the current (now unset) live value, mirroring
// TestTradeFleetConfig_ResolveClearsStalePersistedKeys. This is what makes the
// edit-config + restart retune path work for a coordinator recovered after a daemon
// restart (sp-ts82) — dropping the knob from config.yaml must fall back to the 50k
// floor, which only happens if the stale persisted key is actually removed.
func TestManufacturingConfig_ResolveClearsStalePersistedKeys(t *testing.T) {
	s := &DaemonServer{manufacturingConfig: config.ManufacturingConfig{}} // live config now unset
	persisted := map[string]interface{}{
		"target_good":             "IRON",
		"system_symbol":           "X1-TEST",
		"container_id":            "goods-recover-1",
		"working_capital_reserve": 1000000, // stale from a prior boot when config.yaml set it
	}

	s.resolveManufacturingConfig(persisted)

	// Identity preserved.
	require.Equal(t, "IRON", persisted["target_good"])
	require.Equal(t, "X1-TEST", persisted["system_symbol"])
	// Stale knob cleared (live config no longer sets it).
	_, hasReserve := persisted["working_capital_reserve"]
	require.False(t, hasReserve, "stale working_capital_reserve must be cleared so the live (unset) value wins")

	// The rebuilt command reflects the LIVE config, not the stale persisted key —
	// falling back to goods_factory_coordinator's own 50k floor downstream.
	cmd, ok := buildGoodsFactoryCoordinatorCommand(newConfigReader(persisted), 1, "goods-recover-1").(*goodsCmd.RunFactoryCoordinatorCommand)
	require.True(t, ok)
	require.Equal(t, 0, cmd.WorkingCapitalReserve, "stale 1M reserve must not survive recovery once live config is unset")
}

// The same resolve/recovery guarantee for manufacturing_coordinator, plus the
// "future-proofing" precedence the bead calls for: a LIVE non-zero config.yaml value
// overwrites whatever a persisted launch config carried from a prior boot. The
// daemon's config.yaml is the sole source of truth on every build — creation and
// recovery alike — never the launch config blob.
func TestManufacturingConfig_ResolveLiveValueOverwritesStalePersisted(t *testing.T) {
	s := &DaemonServer{manufacturingConfig: config.ManufacturingConfig{WorkingCapitalReserve: 750000}}
	persisted := map[string]interface{}{
		"system_symbol":           "X1-TEST",
		"container_id":            "mfg-recover-1",
		"working_capital_reserve": 1000000, // stale value from a prior boot / different config
	}

	s.resolveManufacturingConfig(persisted)

	require.Equal(t, 750000, persisted["working_capital_reserve"], "the live config value must overwrite the stale persisted one")

	cmd, ok := buildManufacturingCoordinatorCommand(newConfigReader(persisted), 1, "mfg-recover-1").(*goodsCmd.RunParallelManufacturingCoordinatorCommand)
	require.True(t, ok)
	require.Equal(t, 750000, cmd.WorkingCapitalReserve, "manufacturing_coordinator must rebuild with the LIVE 750k reserve, not the stale 1M")
}
