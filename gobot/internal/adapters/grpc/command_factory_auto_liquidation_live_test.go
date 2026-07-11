package grpc

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// newAutoLiquidationTestServer builds a factory-only DaemonServer whose live
// (boot-loaded) config.yaml auto-liquidation knobs are `live`. sp-39oi mirrors the
// sp-ts82 live-config discipline: the contract coordinator resolves these knobs from THIS
// live config on every command build — creation AND restart recovery — so a stale
// persisted launch config can never shadow the current config.yaml.
func newAutoLiquidationTestServer(live config.AutoLiquidationSettings) *DaemonServer {
	s := &DaemonServer{
		containerSpecs: make(map[string]ContainerSpec),
		contractConfig: config.ContractConfig{AutoLiquidation: live},
	}
	s.registerContainerSpecs()
	return s
}

// The disable toggle resolves live: a coordinator persisted disabled must re-enable when
// config.yaml drops the flag and the daemon restarts (the stale key cannot keep it off),
// and a live disable must take effect on the recovery rebuild.
func TestContractCoordinatorResolvesAutoLiquidationDisabledFromLiveConfig(t *testing.T) {
	// live: feature ON (Disabled false) but a stale key says it was turned off.
	s := newAutoLiquidationTestServer(config.AutoLiquidationSettings{})
	cmd := buildRecoveredCoordinator(t, s, idleArbLaunchConfig(map[string]interface{}{
		"auto_liquidation_disabled": true,
	}))
	require.False(t, cmd.AutoLiquidationDisabled, "stale disabled=true must not survive a live re-enable (default ON)")

	// live: feature OFF -> the toggle takes effect on the recovery rebuild too.
	s = newAutoLiquidationTestServer(config.AutoLiquidationSettings{Disabled: true})
	cmd = buildRecoveredCoordinator(t, s, idleArbLaunchConfig(nil))
	require.True(t, cmd.AutoLiquidationDisabled, "live disabled=true must take effect on the recovery rebuild")
}

// The min-jettison floor resolves live, and an absent live section clears a stale
// persisted floor back to the sentinel 0 (jettison OFF) — a stale copy can never keep a
// jettison floor armed after the captain removes it (RULINGS #5: nothing destroyed without
// an explicit, current floor).
func TestContractCoordinatorResolvesMinJettisonValueFromLiveConfig(t *testing.T) {
	cases := []struct {
		name      string
		live      config.AutoLiquidationSettings
		persisted map[string]interface{}
		wantFloor int
	}{
		{
			name:      "live floor overrides stale persisted floor",
			live:      config.AutoLiquidationSettings{MinJettisonValue: 5000},
			persisted: idleArbLaunchConfig(map[string]interface{}{"liquidation_min_jettison_value": 2000}),
			wantFloor: 5000,
		},
		{
			name:      "absent live section clears a stale floor to 0 (jettison OFF)",
			live:      config.AutoLiquidationSettings{},
			persisted: idleArbLaunchConfig(map[string]interface{}{"liquidation_min_jettison_value": 2000}),
			wantFloor: 0,
		},
		{
			name:      "key-less recovered coordinator picks up a newly-set live floor",
			live:      config.AutoLiquidationSettings{MinJettisonValue: 8000},
			persisted: idleArbLaunchConfig(nil),
			wantFloor: 8000,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newAutoLiquidationTestServer(tc.live)
			cmd := buildRecoveredCoordinator(t, s, tc.persisted)
			require.Equal(t, tc.wantFloor, cmd.LiquidationMinJettisonValue)
		})
	}
}
