package grpc

import (
	"testing"

	"github.com/stretchr/testify/require"

	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
)

// sp-nxrt part (a): the trade-fleet coordinator escalates a twice-fast-failed hull to
// MOVEMENT by relaunching it with reposition-reach armed for THAT launch only. The launch
// carries a *TourRunOverrides that applyTourRunOverrides layers onto the freshly-built
// tour config — so the config→command→coordinator path that reads reposition_reach_enabled
// (registry OptionalBool) picks up the per-launch arming without a daemon-global config flip.
func TestApplyTourRunOverrides_ArmsReachForEscalatedLaunch(t *testing.T) {
	// nil overrides = a normal launch: the config's own (global) value is untouched.
	cfg := map[string]interface{}{"reposition_reach_enabled": false}
	applyTourRunOverrides(cfg, nil)
	require.Equal(t, false, cfg["reposition_reach_enabled"], "a nil override is byte-identical to a config-only launch")

	// An escalated launch arms reach even when the global config had it off.
	applyTourRunOverrides(cfg, &TourRunOverrides{RepositionReachEnabled: true})
	require.Equal(t, true, cfg["reposition_reach_enabled"], "the escalation arms reach for this launch")
}

// The override only ever ARMS reach (false -> true); it never downgrades a captain who has
// globally enabled reach. A non-escalated override on a reach-on config leaves it on.
func TestApplyTourRunOverrides_NeverDowngradesGlobalReach(t *testing.T) {
	cfg := map[string]interface{}{"reposition_reach_enabled": true}
	applyTourRunOverrides(cfg, &TourRunOverrides{RepositionReachEnabled: false})
	require.Equal(t, true, cfg["reposition_reach_enabled"],
		"a non-escalated override must never disarm globally-enabled reach")
}

// The tag → mvt_loop link: MVTLoop writes the selector key only when set, so an unselected
// launch (false or nil overrides) leaves the config without it — the legacy path, and the
// rollback lever step 2 relies on.
func TestApplyTourRunOverrides_MVTLoopWritesTheSelectorKeyOnlyWhenSet(t *testing.T) {
	cfg := map[string]interface{}{"ship_symbol": "HULL-1"}
	applyTourRunOverrides(cfg, nil)
	require.Equal(t, map[string]interface{}{"ship_symbol": "HULL-1"}, cfg, "nil overrides leave the config untouched")

	applyTourRunOverrides(cfg, &TourRunOverrides{MVTLoop: false})
	_, present := cfg["mvt_loop"]
	require.False(t, present, "an unselected launch must not write mvt_loop")

	applyTourRunOverrides(cfg, &TourRunOverrides{MVTLoop: true})
	require.Equal(t, true, cfg["mvt_loop"], "a selected launch writes mvt_loop: true")
}

// The mvt_loop → MVTLoop link: the command builder reads the selector back and the four
// loop knobs default to the spec values when absent.
func TestBuildTourCoordinatorCommand_MVTLoopReadsTheSelectorAndKnobDefaults(t *testing.T) {
	build := func(cfg map[string]interface{}) *tradingCmd.RunTourCoordinatorCommand {
		t.Helper()
		cmd, ok := buildTourCoordinatorCommand(newConfigReader(cfg), 1, "ctr-1").(*tradingCmd.RunTourCoordinatorCommand)
		require.True(t, ok, "build must return *RunTourCoordinatorCommand")
		return cmd
	}
	on := build(map[string]interface{}{"ship_symbol": "HULL-1", "mvt_loop": true})
	require.True(t, on.MVTLoop, "mvt_loop: true selects the MVT loop")

	off := build(map[string]interface{}{"ship_symbol": "HULL-1"})
	require.False(t, off.MVTLoop, "an absent mvt_loop is the legacy path")
	require.Equal(t, tradingCmd.DefaultYieldWindowSells, off.YieldWindowSells)
	require.Equal(t, tradingCmd.DefaultYieldMinSells, off.YieldMinSells)
	require.Equal(t, tradingCmd.DefaultClaimReachHops, off.ClaimReachHops)
	require.Equal(t, tradingCmd.DefaultSpecialistCadenceMinutes, off.SpecialistCadenceMinutes)
}
