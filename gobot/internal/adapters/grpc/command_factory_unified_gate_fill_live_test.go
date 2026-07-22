package grpc

import (
	"testing"

	"github.com/stretchr/testify/require"

	goodsCmd "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/commands"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// sp-vh1s integration: the unified gate-fill master toggle is operator-tunable via [manufacturing]
// config.yaml (RULINGS #5). This pins the config→command round-trip through the REAL launch path for
// the SURVIVING consumer — the construction-supply drain (the goods-factory path that formerly shared
// the toggle was retired, sp-hoj8u): live config.yaml → resolveConstructionUnifiedGateFill's
// launch-config write → the registry read in buildConstructionCoordinatorCommand → the built command.

// newManufacturingTestServer / newFactoryTestServer live in daemon_test_helpers_test.go.

func buildRecoveredConstructionCommand(t *testing.T, s *DaemonServer, persisted map[string]interface{}) *goodsCmd.RunConstructionCoordinatorCommand {
	t.Helper()
	got, err := s.buildCommandForType("construction_coordinator", persisted, 3, "constr-vh1s")
	require.NoError(t, err)
	cmd, ok := got.(*goodsCmd.RunConstructionCoordinatorCommand)
	require.True(t, ok, "expected *RunConstructionCoordinatorCommand, got %T", got)
	return cmd
}

// The construction drain gets the toggle from the [manufacturing] unified_gate_fill knob via a
// surgical resolver that injects ONLY the toggle (leaving the drain's launch-config production_strategy
// untouched). ON reaches the drain; unset leaves it OFF (byte-identical to today).
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
			s := newManufacturingTestServer(tc.live)
			cmd := buildRecoveredConstructionCommand(t, s, map[string]interface{}{"container_id": "constr-vh1s"})
			require.Equal(t, tc.expect, cmd.UnifiedGateFill)
		})
	}
}
