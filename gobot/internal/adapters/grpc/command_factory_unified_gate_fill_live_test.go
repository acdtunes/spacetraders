package grpc

import (
	"testing"

	"github.com/stretchr/testify/require"

	goodsCmd "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/commands"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// sp-vh1s integration: the unified gate-fill master toggle + its gate output-buy throughput-pacing
// knobs are operator-tunable via [manufacturing] config.yaml (RULINGS #5, Admiral sign-off
// 2026-07-14). Without this config→command wiring the toggle never reaches the coordinators and the
// whole feature is unreachable regardless of config.yaml. These are the end-to-end round-trip pins
// through the REAL launch path — live config.yaml → injectManufacturingConfig's launch-config write →
// the registry read in buildGoodsFactoryCoordinatorCommand / buildConstructionCoordinatorCommand →
// the built command — plus the sp-ts82 live discipline (a stale persisted gate key is discarded in
// favour of the current config.yaml). Shared factory helpers (newManufacturingFactoryTestServer /
// goodsFactoryLaunchConfig / buildRecoveredGoodsFactoryCommand) live in
// command_factory_input_ceiling_live_test.go.

// A captain flipping unified_gate_fill on (with the pacing coefficients) must produce a
// goods_factory command carrying all of them — through the whole config pipeline, not set directly
// on the struct. This is the toggle actually reaching the factory coordinator.
func TestGoodsFactoryResolvesUnifiedGateFillFromLiveConfig(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{
		UnifiedGateFill:           true,
		GateOutputBuyRateMultiple: 3.0,
		GateOutputPerLotMultiple:  1.5,
		GateOutputPacingDisabled:  true,
	})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(nil))
	require.True(t, cmd.UnifiedGateFill, "unified_gate_fill=true in [manufacturing] must reach the factory command")
	require.Equal(t, 3.0, cmd.GateOutputBuyRateMultiple)
	require.Equal(t, 1.5, cmd.GateOutputPerLotMultiple)
	require.True(t, cmd.GateOutputPacingDisabled)
}

// Unset live config is byte-identical to a profit factory: the toggle is off and the pacing
// coefficients stay the 0 sentinel (resolved to 2.0/1.0 downstream, but only ever consulted for a
// gate node, so an OFF factory never sees them). The daemon hardcodes no operational value.
func TestGoodsFactoryUnsetUnifiedGateFillIsOffAndByteIdentical(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(nil))
	require.False(t, cmd.UnifiedGateFill, "unset unified_gate_fill must leave the factory OFF (byte-identical)")
	require.Equal(t, 0.0, cmd.GateOutputBuyRateMultiple, "unset coefficient must stay the 0 sentinel, not a hardcoded default")
	require.Equal(t, 0.0, cmd.GateOutputPerLotMultiple)
	require.False(t, cmd.GateOutputPacingDisabled)
	require.Empty(t, cmd.ConstructionSiteWaypoint, "no construction-site key → the run sells at a resale sink (OFF)")
}

// sp-ts82 live discipline: dropping unified_gate_fill (and the pacing knobs) from config.yaml
// (unset live) must CLEAR a stale persisted copy rather than let it shadow the now-absent live value
// — so flipping the toggle OFF actually takes effect on a recovered coordinator. This guards that the
// gate keys were added to the manufacturingConfigKeys clear-list.
func TestGoodsFactoryUnsetLiveClearsStalePersistedUnifiedGateFill(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(map[string]interface{}{
		"unified_gate_fill":             true,
		"gate_output_buy_rate_multiple": 9.9,
		"gate_output_pacing_disabled":   true,
	}))
	require.False(t, cmd.UnifiedGateFill, "unset live must clear the stale persisted toggle and revert to OFF")
	require.Equal(t, 0.0, cmd.GateOutputBuyRateMultiple, "unset live must clear the stale persisted pacing coefficient")
	require.False(t, cmd.GateOutputPacingDisabled, "unset live must clear the stale persisted pacing-disabled flag")
}

// The construction-site waypoint is a PER-LAUNCH key (like good_gating_overrides), not a global
// [manufacturing] knob — a gate-fill factory launch sets it to DELIVER the root output to its site.
// It must round-trip to the command AND survive the goods_factory rebuild (it is deliberately NOT in
// the manufacturingConfigKeys clear-list, which resolveManufacturingConfig would otherwise wipe).
func TestGoodsFactoryConstructionSiteWaypointIsPerLaunchAndSurvivesRebuild(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(map[string]interface{}{
		"construction_site_waypoint": "X1-GATE-99",
	}))
	require.Equal(t, "X1-GATE-99", cmd.ConstructionSiteWaypoint,
		"a per-launch construction_site_waypoint must reach the command and NOT be cleared by resolveManufacturingConfig")
}

func buildRecoveredConstructionCommand(t *testing.T, s *DaemonServer, persisted map[string]interface{}) *goodsCmd.RunConstructionCoordinatorCommand {
	t.Helper()
	got, err := s.buildCommandForType("construction_coordinator", persisted, 3, "constr-vh1s")
	require.NoError(t, err)
	cmd, ok := got.(*goodsCmd.RunConstructionCoordinatorCommand)
	require.True(t, ok, "expected *RunConstructionCoordinatorCommand, got %T", got)
	return cmd
}

// The construction drain gets the toggle from the SAME [manufacturing] unified_gate_fill knob — but
// via a surgical resolver that injects ONLY the toggle, so the drain's launch-config production_strategy
// is left untouched (running the full resolveManufacturingConfig would override it — a construction
// behavior change unrelated to this bead). ON reaches the drain; unset leaves it OFF (byte-identical).
func TestConstructionResolvesUnifiedGateFillFromLiveConfig(t *testing.T) {
	cases := []struct {
		name   string
		live   config.ManufacturingConfig
		expect bool
	}{
		{"toggle_on_reaches_the_drain", config.ManufacturingConfig{UnifiedGateFill: true}, true},
		{"unset_leaves_the_drain_off", config.ManufacturingConfig{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newManufacturingFactoryTestServer(tc.live)
			cmd := buildRecoveredConstructionCommand(t, s, map[string]interface{}{"container_id": "constr-vh1s"})
			require.Equal(t, tc.expect, cmd.UnifiedGateFill)
		})
	}
}
