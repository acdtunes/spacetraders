package grpc

import (
	"testing"

	"github.com/stretchr/testify/require"

	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// boolPtr is a tiny helper for the *bool enabled knob.
func boolPtr(b bool) *bool { return &b }

// buildTradeCmd runs the full config round-trip the daemon uses: inject the
// [trade_fleet] knobs into a launch config, then rebuild the command from it (exactly
// what creation and restart recovery do). It asserts the identity keys survive and
// returns the reconstructed command for knob assertions.
func buildTradeCmd(t *testing.T, tf config.TradeFleetConfig) *tradingCmd.RunTradeFleetCoordinatorCommand {
	t.Helper()
	s := &DaemonServer{tradeFleetConfig: tf}
	cfgMap := map[string]interface{}{
		"container_id": "trade-coord-1",
		"agent_symbol": "TORWIND",
	}
	s.injectTradeFleetConfig(cfgMap)

	built := buildTradeFleetCoordinatorCommand(newConfigReader(cfgMap), 1, "trade-coord-1")
	cmd, ok := built.(*tradingCmd.RunTradeFleetCoordinatorCommand)
	require.True(t, ok, "build must return *RunTradeFleetCoordinatorCommand")
	require.Equal(t, "trade-coord-1", cmd.ContainerID)
	require.Equal(t, "TORWIND", cmd.AgentSymbol)
	require.Equal(t, 1, cmd.PlayerID.Value())
	return cmd
}

// An empty [trade_fleet] section defaults ON with all knobs at 0 (the coordinator's
// own defaults) — the default-ON intent, and no trade_fleet_disabled key is written.
func TestTradeFleetConfig_DefaultEnabled(t *testing.T) {
	cmd := buildTradeCmd(t, config.TradeFleetConfig{}) // Enabled nil => EnabledOrDefault() true
	require.True(t, cmd.Enabled, "an unset [trade_fleet] section must default ON")
	require.Equal(t, 0, cmd.CooldownSecs)
	require.Equal(t, 0, cmd.MaxConcurrentTours)
}

// enabled: false is the real off-switch: it round-trips through the inverted
// trade_fleet_disabled key back to Enabled=false.
func TestTradeFleetConfig_ExplicitDisabled(t *testing.T) {
	cmd := buildTradeCmd(t, config.TradeFleetConfig{Enabled: boolPtr(false)})
	require.False(t, cmd.Enabled, "enabled: false must disable the coordinator")
}

// enabled: true is ON, same as unset.
func TestTradeFleetConfig_ExplicitEnabled(t *testing.T) {
	cmd := buildTradeCmd(t, config.TradeFleetConfig{Enabled: boolPtr(true)})
	require.True(t, cmd.Enabled)
}

// Every knob the captain sets round-trips verbatim, including the int64 caps.
func TestTradeFleetConfig_KnobsRoundTrip(t *testing.T) {
	cmd := buildTradeCmd(t, config.TradeFleetConfig{
		Enabled:               boolPtr(true),
		CooldownSeconds:       240,
		MaxConcurrentTours:    8,
		TickSeconds:           45,
		MaxHops:               4,
		MaxSpend:              300000,
		MinMargin:             3,
		ReplanLimit:           2,
		WorkingCapitalReserve: 50000,
	})
	require.True(t, cmd.Enabled)
	require.Equal(t, 240, cmd.CooldownSecs)
	require.Equal(t, 8, cmd.MaxConcurrentTours)
	require.Equal(t, 45, cmd.TickIntervalSecs)
	require.Equal(t, 4, cmd.MaxHops)
	require.Equal(t, int64(300000), cmd.MaxSpend)
	require.Equal(t, 3, cmd.MinMargin)
	require.Equal(t, 2, cmd.ReplanLimit)
	require.Equal(t, int64(50000), cmd.WorkingCapitalReserve)
}

// resolveTradeFleetConfig makes config.yaml the live source of truth: a stale knob
// persisted at a prior boot is cleared and NOT allowed to shadow the current (now
// unset) live value, while the coordinator's identity keys survive untouched. This is
// what makes the edit-config + restart retune path work for a recovered coordinator.
func TestTradeFleetConfig_ResolveClearsStalePersistedKeys(t *testing.T) {
	// Live config leaves cooldown unset (0 => coordinator default) but the persisted
	// launch config still carries a stale 999 from a prior boot.
	s := &DaemonServer{tradeFleetConfig: config.TradeFleetConfig{Enabled: boolPtr(true)}}
	persisted := map[string]interface{}{
		"container_id":              "trade-coord-1",
		"agent_symbol":              "TORWIND",
		"trade_fleet_cooldown_secs": 999,
		"trade_fleet_disabled":      true, // stale "off" from a prior boot
	}

	s.resolveTradeFleetConfig(persisted)

	// Identity preserved.
	require.Equal(t, "trade-coord-1", persisted["container_id"])
	require.Equal(t, "TORWIND", persisted["agent_symbol"])
	// Stale knobs cleared (live config did not set them).
	_, hasCooldown := persisted["trade_fleet_cooldown_secs"]
	require.False(t, hasCooldown, "stale cooldown must be cleared so the default applies")
	_, hasDisabled := persisted["trade_fleet_disabled"]
	require.False(t, hasDisabled, "stale disabled must be cleared so live enabled=true wins")

	// The rebuilt command reflects the LIVE config, not the stale persisted keys.
	cmd, ok := buildTradeFleetCoordinatorCommand(newConfigReader(persisted), 1, "trade-coord-1").(*tradingCmd.RunTradeFleetCoordinatorCommand)
	require.True(t, ok)
	require.True(t, cmd.Enabled, "live enabled=true must win over the stale persisted disabled")
	require.Equal(t, 0, cmd.CooldownSecs, "stale 999 cooldown must not shadow the live default")
}
