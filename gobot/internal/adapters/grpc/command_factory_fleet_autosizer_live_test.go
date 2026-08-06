package grpc

import (
	"testing"

	"github.com/stretchr/testify/require"

	fleetCmd "github.com/andrescamacho/spacetraders-go/internal/application/fleet/commands"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// sp-1txd: the fleet capacity autosizer is LIVE BY DEFAULT (Admiral: no dark-shipping) and its
// [fleet_autosizer] knobs are operator-tunable (RULINGS #5). These pin the end-to-end launch
// round-trip — live config.yaml → injectFleetAutosizerConfig's launch-config write → the registry
// read in buildFleetAutosizerCommand → the built command — and the sp-ts82 live discipline (a
// stale persisted key is discarded in favor of config.yaml).

func newFleetAutosizerTestServer(live config.FleetAutosizerConfig) *DaemonServer {
	s := &DaemonServer{
		containerSpecs:       make(map[string]ContainerSpec),
		fleetAutosizerConfig: live,
	}
	s.registerContainerSpecs()
	return s
}

func autosizerLaunchConfig(stale map[string]interface{}) map[string]interface{} {
	cfg := map[string]interface{}{
		"container_id": "autosizer-1txd",
		"agent_symbol": "AGENT-1",
	}
	for k, v := range stale {
		cfg[k] = v
	}
	return cfg
}

func buildRecoveredAutosizerCommand(t *testing.T, s *DaemonServer, persisted map[string]interface{}) *fleetCmd.RunFleetAutosizerCoordinatorCommand {
	t.Helper()
	got, err := s.buildCommandForType("fleet_autosizer", persisted, 7, "autosizer-1txd")
	require.NoError(t, err)
	cmd, ok := got.(*fleetCmd.RunFleetAutosizerCoordinatorCommand)
	require.True(t, ok, "expected *RunFleetAutosizerCoordinatorCommand, got %T", got)
	return cmd
}

// ABSENT config → coordinator ACTIVE. The autosizer runs on deploy without any enablement flip —
// the pin the Admiral's no-dark-shipping ruling mandates.
func TestAutosizerLiveByDefault(t *testing.T) {
	s := newFleetAutosizerTestServer(config.FleetAutosizerConfig{})
	cmd := buildRecoveredAutosizerCommand(t, s, autosizerLaunchConfig(nil))
	require.Equal(t, 7, cmd.PlayerID)
	require.Equal(t, "autosizer-1txd", cmd.ContainerID)
}

// A captain tuning ceilings/knobs must produce a command carrying those values — through the whole
// config pipeline, not set directly on the struct.
func TestAutosizerResolvesKnobsFromLiveConfig(t *testing.T) {
	s := newFleetAutosizerTestServer(config.FleetAutosizerConfig{
		TickIntervalSecs:        120,
		PurchaseCapPerTick:      2,
		PurchaseMarginOverFloor: 500000,
		LightRotationSlots:      4.0,
		ShipTypeHeavies:         "SHIP_REFINING_FREIGHTER",
	})
	cmd := buildRecoveredAutosizerCommand(t, s, autosizerLaunchConfig(nil))
	require.Equal(t, 120, cmd.TickIntervalSecs)
	require.Equal(t, 2, cmd.PurchaseCapPerTick)
	require.Equal(t, int64(500000), cmd.PurchaseMarginOverFloor)
	require.Equal(t, 4.0, cmd.LightRotationSlots)
	require.Equal(t, "SHIP_REFINING_FREIGHTER", cmd.ShipTypeHeavies)
}

// A STALE persisted knob from a prior boot must be discarded for the current config.yaml
// value on the recovery rebuild.
func TestAutosizerLiveKnobOverridesStalePersisted(t *testing.T) {
	s := newFleetAutosizerTestServer(config.FleetAutosizerConfig{
		TickIntervalSecs: 40,
	})
	cmd := buildRecoveredAutosizerCommand(t, s, autosizerLaunchConfig(map[string]interface{}{
		"autosizer_tick_secs": 999, // stale copy from a prior boot
	}))
	require.Equal(t, 40, cmd.TickIntervalSecs, "live 40 must override the stale persisted 999")
}

// Unset live leaves the numeric knobs at the 0 sentinel (resolved to defaults downstream in
// resolveFleetAutosizerConfig) — the daemon never hardcodes an operational value into the launch
// config.
func TestAutosizerUnsetKnobsAreZeroSentinel(t *testing.T) {
	s := newFleetAutosizerTestServer(config.FleetAutosizerConfig{})
	cmd := buildRecoveredAutosizerCommand(t, s, autosizerLaunchConfig(nil))
	require.Equal(t, 0, cmd.TickIntervalSecs, "unset tick must stay the 0 sentinel, not a hardcoded default")
	require.Equal(t, 0, cmd.PurchaseCapPerTick)
	require.Nil(t, cmd.PreferDemandProximalYard, "unset proximal-yard must stay nil so the coordinator applies its true default")
}

// The default-TRUE prefer_demand_proximal_yard round-trips: unset → nil (default true downstream);
// explicit false in live config → a non-nil *bool carrying false (the captain's explicit opt-out
// survives, not collapsed into the default).
func TestAutosizerProximalYardExplicitFalseRoundTrips(t *testing.T) {
	no := false
	s := newFleetAutosizerTestServer(config.FleetAutosizerConfig{
		PreferDemandProximalYard: &no,
	})
	cmd := buildRecoveredAutosizerCommand(t, s, autosizerLaunchConfig(nil))
	require.NotNil(t, cmd.PreferDemandProximalYard, "explicit false must round-trip as a non-nil *bool")
	require.False(t, *cmd.PreferDemandProximalYard, "the captain's explicit opt-out must survive, not collapse into the default")
}

// The heavy_cap tune key must stay OUT of fleetAutosizerConfigKeys.
//
// resolveFleetAutosizerConfig CLEARS every key in that list and re-injects it from config.yaml on
// each container build. The live-tunable knob is the BARE key "heavy_cap"; adding it to the
// clear-list — the well-meaning "add it for consistency" edit — would wipe every tuned value on the
// next daemon bounce, so `tune heavy_cap 12` would appear to work and then silently revert.
//
// The registry anti-drift test catches a RENAME of heavyCapKey to the prefixed form; it cannot
// catch an ADDITION here. This is that assertion.
func TestFleetAutosizerConfigKeys_ExcludeTheBareHeavyCapTuneKey(t *testing.T) {
	for _, key := range fleetAutosizerConfigKeys {
		if key == "heavy_cap" {
			t.Fatal("the bare tune key \"heavy_cap\" must NOT be in fleetAutosizerConfigKeys — resolveFleetAutosizerConfig clears that list on every rebuild, so a tuned value would be silently wiped on the next daemon bounce")
		}
	}
}
