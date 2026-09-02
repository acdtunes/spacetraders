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

// tradeFleetConfigKeys must stay in LOCKSTEP with injectTradeFleetConfig: any key the
// injector can write but the list omits survives resolveTradeFleetConfig's clear, so a
// value persisted at a prior boot shadows the current config.yaml forever (the sp-ts82
// stale-shadow failure). Drive the injector with every knob set and assert the written
// key set is covered.
func TestTradeFleetConfig_EveryInjectedKeyIsCleared(t *testing.T) {
	written := map[string]bool{}
	for _, enabled := range []bool{true, false} { // trade_fleet_disabled is written only when OFF
		s := &DaemonServer{tradeFleetConfig: config.TradeFleetConfig{
			Enabled:                   boolPtr(enabled),
			CooldownSeconds:           240,
			MaxConcurrentTours:        8,
			TickSeconds:               45,
			MaxHops:                   4,
			MaxSpend:                  300000,
			MinMargin:                 3,
			ReplanLimit:               2,
			WorkingCapitalReserve:     50000,
			RelaunchBackoffMaxMinutes: 20,
			MassParkExemptDisabled:    true,
			MassParkWindowSeconds:     180,
			MassParkMinHulls:          5,
			WatchdogStallSeconds:      900,
			FullHullPausePct:          70,
			SpecialistFractionPct:     25,
			FatLaneMultiplePct:        300,
			SpecialistCadenceMinutes:  90,
		}}
		cfgMap := map[string]interface{}{}
		s.injectTradeFleetConfig(cfgMap)
		for k := range cfgMap {
			written[k] = true
		}
	}

	listed := map[string]bool{}
	for _, k := range tradeFleetConfigKeys {
		listed[k] = true
	}
	for k := range written {
		require.True(t, listed[k], "injectTradeFleetConfig writes %q but tradeFleetConfigKeys omits it: a stale persisted value would shadow config.yaml", k)
	}
}

// The MVT specialist-pool knobs obey the same live-config discipline as their
// trade_fleet_* siblings despite their unprefixed key names: dropping the knob from
// config.yaml must fall back to the coordinator's spec default, not to the value
// persisted at a prior boot.
func TestTradeFleetConfig_ResolveClearsStaleSpecialistKnobs(t *testing.T) {
	s := &DaemonServer{tradeFleetConfig: config.TradeFleetConfig{Enabled: boolPtr(true)}}
	persisted := map[string]interface{}{
		"container_id":               "trade-coord-1",
		"agent_symbol":               "TORWIND",
		"specialist_fraction_pct":    25,  // stale, live config no longer sets it
		"fat_lane_multiple_pct":      400, // stale
		"specialist_cadence_minutes": 15,  // stale
	}

	s.resolveTradeFleetConfig(persisted)

	for _, key := range []string{"specialist_fraction_pct", "fat_lane_multiple_pct", "specialist_cadence_minutes"} {
		_, present := persisted[key]
		require.Falsef(t, present, "stale %s must be cleared so the spec default applies", key)
	}

	cmd, ok := buildTradeFleetCoordinatorCommand(newConfigReader(persisted), 1, "trade-coord-1").(*tradingCmd.RunTradeFleetCoordinatorCommand)
	require.True(t, ok)
	require.Equal(t, tradingCmd.DefaultSpecialistFractionPct, cmd.SpecialistFractionPct)
	require.Equal(t, tradingCmd.DefaultFatLaneMultiplePct, cmd.FatLaneMultiplePct)
	require.Equal(t, tradingCmd.DefaultSpecialistCadenceMinutes, cmd.SpecialistCadenceMinutes)
}

// And a knob the captain DOES set still rides through resolve → rebuild.
func TestTradeFleetConfig_SpecialistKnobsRoundTrip(t *testing.T) {
	cmd := buildTradeCmd(t, config.TradeFleetConfig{
		Enabled:                  boolPtr(true),
		SpecialistFractionPct:    25,
		FatLaneMultiplePct:       300,
		SpecialistCadenceMinutes: 90,
	})
	require.Equal(t, 25, cmd.SpecialistFractionPct)
	require.Equal(t, 300, cmd.FatLaneMultiplePct)
	require.Equal(t, 90, cmd.SpecialistCadenceMinutes)
}

// The trade-mvt tag → overrides link LaunchTour rides: the hull's fleet tag alone selects
// the MVT loop for its launch, a plain "trade" hull gets no override at all, and the reach
// escalation composes with either tag.
func TestTourOverridesFor_TradeMVTTagSelectsTheLoop(t *testing.T) {
	mvt := tourOverridesFor(tradingCmd.TourLaunchSpec{ShipSymbol: "HULL-1", Fleet: "trade-mvt"})
	require.NotNil(t, mvt, "a trade-mvt hull must carry overrides")
	require.True(t, mvt.MVTLoop, "a trade-mvt hull selects the MVT loop")
	require.False(t, mvt.RepositionReachEnabled, "the tag alone never arms reach")

	require.Nil(t, tourOverridesFor(tradingCmd.TourLaunchSpec{ShipSymbol: "HULL-1", Fleet: "trade"}),
		"a trade hull is a config-only launch")
	require.Nil(t, tourOverridesFor(tradingCmd.TourLaunchSpec{ShipSymbol: "HULL-1"}),
		"an untagged spec (the captain CLI path) is a config-only launch")

	escalated := tourOverridesFor(tradingCmd.TourLaunchSpec{ShipSymbol: "HULL-1", Fleet: "trade", RepositionReachEscalated: true})
	require.NotNil(t, escalated)
	require.True(t, escalated.RepositionReachEnabled)
	require.False(t, escalated.MVTLoop, "escalating a trade hull must not switch its path")

	both := tourOverridesFor(tradingCmd.TourLaunchSpec{ShipSymbol: "HULL-1", Fleet: "trade-mvt", RepositionReachEscalated: true})
	require.NotNil(t, both)
	require.True(t, both.MVTLoop)
	require.True(t, both.RepositionReachEnabled)
}
