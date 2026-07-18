package grpc

import (
	"testing"

	"github.com/stretchr/testify/require"
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
