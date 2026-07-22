package commands

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
)

// sp-tlekc reach_mode: ONE dial (1-3) composes the 13 granular reach knobs from a coherent
// preset. Phase 1 is default-off / passthrough — an UNSET dial leaves the granular resolution
// byte-identical, and an explicit reach_mode overlays the preset AFTER the granular fallbacks.

// reach_mode=balanced (2) reproduces the LIVE SNAPSHOT (the reproduce-today anchor, §5):
// breadth65/bias40/pf3/hops8/reuse1/ceiling100k/relay3/snowball1/timeout1800/off-gate 5,400,10,1.
// This is the load-bearing parity: setting the new dial to balanced arms the exact live gate.
func TestResolveConfig_ReachModeBalanced_ReproducesLiveSnapshot(t *testing.T) {
	cmd := testCmd()
	cmd.ReachMode = reachModeBalanced

	c := resolveConfig(cmd, nil)

	require.Equal(t, 65, c.BreadthFractionPercent, "breadth")
	require.Equal(t, 40, c.ObjectiveBiasPercent, "objective bias")
	require.Equal(t, 3, c.MaxDepthPathfinders, "pathfinders")
	require.Equal(t, 8, c.MaxDepthHops, "depth hops")
	require.True(t, c.ProbeReuseEnabled, "reuse ARMED at balanced (the live snapshot)")
	require.Equal(t, 100_000, c.ReuseValueCeiling, "reuse value ceiling")
	require.Equal(t, 3, c.EdgeRelayMaxHops, "edge relay hops")
	require.True(t, c.SnowballNeighbors, "snowball ARMED at balanced")
	require.Equal(t, 1800*time.Second, c.PostInflightTimeout, "in-flight reap timeout")
	require.Equal(t, 5, c.OffGateQueueExhaustionCycles, "off-gate queue exhaustion")
	require.Equal(t, 400, c.OffGateWarpRangeFuel, "off-gate warp fuel")
	require.Equal(t, 10, c.OffGateValueWeight, "off-gate value weight")
	require.Equal(t, 1, c.OffGateFuelWeight, "off-gate fuel weight")
}

// reach_mode UNSET (0) resolves to the DEFAULT preset (balanced) — the granular knobs are gone, so
// there is no passthrough: an untuned coordinator runs the live-snapshot balanced reach. This is the
// mutation guard for the `if reachMode <= 0 { reachMode = defaultReachMode }` default: drop it and an
// unset dial applies no preset (zero-valued reach), failing these assertions.
func TestResolveConfig_ReachModeUnset_DefaultsToBalanced(t *testing.T) {
	cmd := testCmd() // no reach_mode → the documented default (balanced)

	c := resolveConfig(cmd, nil)

	require.Equal(t, 65, c.BreadthFractionPercent, "unset reach_mode → balanced breadth")
	require.True(t, c.ProbeReuseEnabled, "unset reach_mode → balanced arms reuse (the live snapshot)")
	require.Equal(t, 100_000, c.ReuseValueCeiling, "unset reach_mode → balanced reuse ceiling")
	require.True(t, c.SnowballNeighbors, "unset reach_mode → balanced arms snowball")
	require.Equal(t, 1800*time.Second, c.PostInflightTimeout, "unset reach_mode → balanced arms the reap")
}

// The probe-sourcing terms + the anti-overpay price ceiling are now IMMUTABLE internal consts (not
// operator knobs): every resolved config carries them regardless of the dials. This pins the §2C/§2D
// hardcoding — the ceiling is 100k, the hop-penalty 50k, the sibling margin 30k, always.
func TestResolveConfig_ImmutableSourcingAndCeilingConsts(t *testing.T) {
	c := resolveConfig(testCmd(), nil)
	require.Equal(t, maxProbePrice, c.MaxProbePrice, "the price ceiling is the immutable const")
	require.Equal(t, 100000, c.MaxProbePrice, "the price ceiling is hardcoded 100k (§2C)")
	require.Equal(t, hopPenaltyCredits, c.ProximalYardHopPenalty, "the hop penalty is the internal const")
	require.Equal(t, siblingPriceMarginCredits, c.ProbeSiblingPriceMargin, "the sibling margin is the internal const")
}

// reach_mode shallow (1) is pure-BFS / reuse-off; deep (3) is the aggressive outward drive. The
// presets compose the coupled values together, so a shallow reach can never leave reuse armed and
// a deep reach always carries its relay + ceiling (the sp-6vep footguns are unexpressible).
func TestResolveConfig_ReachModeShallowAndDeep(t *testing.T) {
	shallow := resolveConfig(func() *RunFrontierExpansionCoordinatorCommand {
		c := testCmd()
		c.ReachMode = reachModeShallow
		return c
	}(), nil)
	require.Equal(t, 100, shallow.BreadthFractionPercent, "shallow is pure BFS (breadth 100)")
	require.False(t, shallow.ProbeReuseEnabled, "shallow reuse is OFF")

	deep := resolveConfig(func() *RunFrontierExpansionCoordinatorCommand {
		c := testCmd()
		c.ReachMode = reachModeDeep
		return c
	}(), nil)
	require.Equal(t, 50, deep.BreadthFractionPercent, "deep drives breadth down to 50")
	require.Equal(t, 10, deep.MaxDepthHops, "deep reaches hop 10")
	require.True(t, deep.ProbeReuseEnabled, "deep reuse is ARMED")
	require.Equal(t, 250_000, deep.ReuseValueCeiling, "deep reuse ceiling 250k (armed WITH its ceiling)")
}

// A live-tuned reach_mode overrides the launch/default preset next tick (the sp-vwek live-config
// precedence): a live snapshot's reach_mode governs, so `tune reach_mode 1` disarms reuse with no
// restart.
func TestResolveConfig_ReachModeLiveOverridesLaunch(t *testing.T) {
	cmd := testCmd()
	cmd.ReachMode = reachModeDeep // launch value: deep

	// A live snapshot tuning reach_mode=shallow wins over the launch deep.
	c := resolveConfig(cmd, liveconfig.Snapshot{"reach_mode": reachModeShallow})

	require.Equal(t, 100, c.BreadthFractionPercent, "the live shallow preset wins over the launch deep")
	require.False(t, c.ProbeReuseEnabled, "live shallow disarms reuse next tick")
}
