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

// ABSENT config → coordinator ACTIVE. With no autosizer_disabled anywhere, the built command
// carries Disabled=false, so the autosizer runs on deploy without any enablement flip — the pin
// the Admiral's no-dark-shipping ruling mandates.
func TestAutosizerLiveByDefault(t *testing.T) {
	s := newFleetAutosizerTestServer(config.FleetAutosizerConfig{})
	cmd := buildRecoveredAutosizerCommand(t, s, autosizerLaunchConfig(nil))
	require.False(t, cmd.Disabled,
		"absent config must leave the autosizer ACTIVE (LIVE by default) — no dark-shipping")
	require.False(t, cmd.LightsDisabled, "lights live by default")
	require.False(t, cmd.HeaviesDisabled, "heavies live by default")
	require.Equal(t, 7, cmd.PlayerID)
	require.Equal(t, "autosizer-1txd", cmd.ContainerID)
}

// The master escape hatch + each per-class disable resolve live (RULINGS #5 off-switches).
func TestAutosizerResolvesDisablesFromLiveConfig(t *testing.T) {
	s := newFleetAutosizerTestServer(config.FleetAutosizerConfig{
		AutosizerDisabled: true,
		HeaviesDisabled:   true,
	})
	cmd := buildRecoveredAutosizerCommand(t, s, autosizerLaunchConfig(nil))
	require.True(t, cmd.Disabled)
	require.True(t, cmd.HeaviesDisabled)
	require.False(t, cmd.LightsDisabled, "only the heavies disable was set")
}

// sp-ts82 live discipline: dropping the escape hatch from config.yaml must CLEAR a stale
// persisted autosizer_disabled — otherwise a stale disable would silently keep the autosizer off
// after the captain removed it, re-opening the dark-shipping gap.
func TestAutosizerUnsetLiveClearsStalePersistedDisabled(t *testing.T) {
	s := newFleetAutosizerTestServer(config.FleetAutosizerConfig{})
	cmd := buildRecoveredAutosizerCommand(t, s, autosizerLaunchConfig(map[string]interface{}{
		"autosizer_disabled": true, // stale copy from a prior boot
	}))
	require.False(t, cmd.Disabled,
		"unset live must clear the stale persisted disable → autosizer ACTIVE")
}

// A captain tuning ceilings/knobs must produce a command carrying those values — through the whole
// config pipeline, not set directly on the struct.
func TestAutosizerResolvesKnobsFromLiveConfig(t *testing.T) {
	s := newFleetAutosizerTestServer(config.FleetAutosizerConfig{
		TickIntervalSecs:        120,
		PurchaseCapPerTick:      2,
		FleetCeilingTotal:       80,
		FleetCeilingHeavies:     12,
		PurchaseMarginOverFloor: 500000,
		LightRotationSlots:      4.0,
		HeavyMarginalRateFloor:  0.8,
		PaybackSafetyFactor:     0.6,
		ShipTypeHeavies:         "SHIP_REFINING_FREIGHTER",
	})
	cmd := buildRecoveredAutosizerCommand(t, s, autosizerLaunchConfig(nil))
	require.Equal(t, 120, cmd.TickIntervalSecs)
	require.Equal(t, 2, cmd.PurchaseCapPerTick)
	require.Equal(t, 80, cmd.FleetCeilingTotal)
	require.Equal(t, 12, cmd.FleetCeilingHeavies)
	require.Equal(t, int64(500000), cmd.PurchaseMarginOverFloor)
	require.Equal(t, 4.0, cmd.LightRotationSlots)
	require.Equal(t, 0.8, cmd.HeavyMarginalRateFloor)
	require.Equal(t, 0.6, cmd.PaybackSafetyFactor)
	require.Equal(t, "SHIP_REFINING_FREIGHTER", cmd.ShipTypeHeavies)
}

// sp-zbe6: the declining-rate unserved floor round-trips config.yaml → the built command, so a
// captain retunes the concentration-vs-saturation threshold by editing config.yaml and restarting
// (the sp-ts82 live-config discipline). It also clears a stale persisted copy in favor of the
// current config.yaml value.
func TestAutosizerResolvesDecliningRateUnservedFloorFromLiveConfig(t *testing.T) {
	s := newFleetAutosizerTestServer(config.FleetAutosizerConfig{
		DecliningRateUnservedFloor: 4,
	})
	cmd := buildRecoveredAutosizerCommand(t, s, autosizerLaunchConfig(map[string]interface{}{
		"autosizer_declining_rate_unserved_floor": 99, // stale copy from a prior boot
	}))
	require.Equal(t, 4, cmd.DecliningRateUnservedFloor,
		"live config.yaml value must be plumbed into the command and override the stale persisted 99")
}

// sp-ts82: a STALE persisted knob from a prior boot must be discarded for the current config.yaml
// value on the recovery rebuild.
func TestAutosizerLiveKnobOverridesStalePersisted(t *testing.T) {
	s := newFleetAutosizerTestServer(config.FleetAutosizerConfig{
		FleetCeilingTotal: 40,
	})
	cmd := buildRecoveredAutosizerCommand(t, s, autosizerLaunchConfig(map[string]interface{}{
		"autosizer_fleet_ceiling_total": 999, // stale copy from a prior boot
	}))
	require.Equal(t, 40, cmd.FleetCeilingTotal, "live 40 must override the stale persisted 999")
}

// Unset live leaves the numeric knobs at the 0 sentinel (resolved to defaults downstream in
// resolveFleetAutosizerConfig) — the daemon never hardcodes an operational value into the launch
// config.
func TestAutosizerUnsetKnobsAreZeroSentinel(t *testing.T) {
	s := newFleetAutosizerTestServer(config.FleetAutosizerConfig{})
	cmd := buildRecoveredAutosizerCommand(t, s, autosizerLaunchConfig(nil))
	require.Equal(t, 0, cmd.TickIntervalSecs, "unset tick must stay the 0 sentinel, not a hardcoded default")
	require.Equal(t, 0, cmd.FleetCeilingTotal)
	require.Equal(t, 0.0, cmd.PaybackSafetyFactor)
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
