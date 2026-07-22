package commands

import "time"

// This file holds the sp-tlekc REACH_MODE preset selector: ONE operator dial (1-3) that
// composes the frontier's depth/breadth balance, the edge-probe reuse relay, the off-gate
// explorer signal, and the in-flight reap timeout from a single COHERENT preset — collapsing
// the 13 granular reach knobs (breadth/bias/pathfinders/hops, reuse/ceiling/relay/snowball,
// the reap timeout, and the four off-gate ranking knobs) into one. The presets honor the knob
// COUPLINGS BY CONSTRUCTION (sp-6vep §2B): reuse is never armed without its value ceiling, and
// raised depth is never armed without its relay + reap timeout — so the footgun states the
// granular knobs could express (a lone probe_reuse_enabled that silently no-ops, a lone
// max_depth_hops that jams the in-flight cap) are simply unreachable.
//
// PHASE 1 (adopt, byte-identical): reach_mode is default-off / passthrough. An UNSET dial (0)
// leaves the granular resolution untouched — a merge is byte-identical to today (PLAYBOOK §10)
// — and an explicit 1/2/3 overlays the preset AFTER the granular fallbacks in resolveConfig, so
// setting reach_mode=balanced reproduces the live snapshot (the reproduce-today anchor, §5).

const (
	// reach_mode presets. Balanced (2) is the live snapshot — the reproduce-today anchor — and
	// the documented default once the granular knobs are retired (Phase 2).
	reachModeShallow  = 1 // pure-BFS near-ring sweep, reuse OFF: the cheapest, tightest reach
	reachModeBalanced = 2 // the live snapshot: 65/35 breadth/depth, reuse armed, snowball + reap on
	reachModeDeep     = 3 // aggressive outward drive: deeper hops, more pathfinders, wider reuse

	defaultReachMode = reachModeBalanced
)

// reachPreset is the coherent bundle of reach values ONE reach_mode selects — the 13 granular
// reach/off-gate knobs collapsed into a single dial. Every field maps 1:1 to the frontierConfig
// field applyReachPreset overlays, so the coupled values are always set together and can never
// drift into a footgun (a raised depth always carries its relay + reap; an armed reuse always
// carries its value ceiling).
type reachPreset struct {
	breadthFractionPercent       int
	objectiveBiasPercent         int
	maxDepthPathfinders          int
	maxDepthHops                 int
	probeReuseEnabled            bool
	reuseValueCeiling            int
	edgeRelayMaxHops             int
	snowballNeighbors            bool
	postInflightTimeout          time.Duration
	offGateQueueExhaustionCycles int
	offGateWarpRangeFuel         int
	offGateValueWeight           int
	offGateFuelWeight            int
}

// reachModePreset returns the coherent preset for a reach_mode. An out-of-range mode falls back
// to balanced (the safe live snapshot), never a footgun — so a bad tune can never strand the
// coordinator on an incoherent reach.
func reachModePreset(mode int) reachPreset {
	switch mode {
	case reachModeShallow:
		// Pure-BFS near-ring sweep: breadth 100 (0% depth), reuse OFF, no snowball, no reap. The
		// depth pathfinder knobs are inert at breadth 100 but carry safe values. Off-gate ranking
		// stays the balanced default (the signal is orthogonal to reach depth).
		return reachPreset{
			breadthFractionPercent:       100,
			objectiveBiasPercent:         0,
			maxDepthPathfinders:          1,
			maxDepthHops:                 2,
			probeReuseEnabled:            false,
			reuseValueCeiling:            0,
			edgeRelayMaxHops:             3,
			snowballNeighbors:            false,
			postInflightTimeout:          0,
			offGateQueueExhaustionCycles: 5,
			offGateWarpRangeFuel:         400,
			offGateValueWeight:           10,
			offGateFuelWeight:            1,
		}
	case reachModeDeep:
		// Aggressive outward drive (§3): breadth 50 / bias 60, 5 pathfinders to hop 10, reuse armed
		// to a 250k value ceiling, relay 5, snowball + a 1800s reap, and a wider off-gate reach
		// (queue-exhaustion 3, warp fuel 800, value weight 20).
		return reachPreset{
			breadthFractionPercent:       50,
			objectiveBiasPercent:         60,
			maxDepthPathfinders:          5,
			maxDepthHops:                 10,
			probeReuseEnabled:            true,
			reuseValueCeiling:            250_000,
			edgeRelayMaxHops:             5,
			snowballNeighbors:            true,
			postInflightTimeout:          1800 * time.Second,
			offGateQueueExhaustionCycles: 3,
			offGateWarpRangeFuel:         800,
			offGateValueWeight:           20,
			offGateFuelWeight:            1,
		}
	default:
		// reachModeBalanced — the live snapshot (reproduce-today anchor, sp-tlekc §5): breadth 65 /
		// bias 40, 3 pathfinders to hop 8, reuse armed to a 100k ceiling, relay 3, snowball + a 1800s
		// reap, off-gate 5/400/10/1.
		return reachPreset{
			breadthFractionPercent:       65,
			objectiveBiasPercent:         40,
			maxDepthPathfinders:          3,
			maxDepthHops:                 8,
			probeReuseEnabled:            true,
			reuseValueCeiling:            100_000,
			edgeRelayMaxHops:             3,
			snowballNeighbors:            true,
			postInflightTimeout:          1800 * time.Second,
			offGateQueueExhaustionCycles: 5,
			offGateWarpRangeFuel:         400,
			offGateValueWeight:           10,
			offGateFuelWeight:            1,
		}
	}
}

// applyReachPreset overlays a reach_mode preset onto the already-resolved config, replacing the
// 13 granular reach values with the preset's coherent bundle. It is called only for an EXPLICIT
// reach_mode (Phase 1 passthrough leaves an unset dial's granular resolution byte-identical).
func applyReachPreset(c *frontierConfig, mode int) {
	preset := reachModePreset(mode)
	c.BreadthFractionPercent = preset.breadthFractionPercent
	c.ObjectiveBiasPercent = preset.objectiveBiasPercent
	c.MaxDepthPathfinders = preset.maxDepthPathfinders
	c.MaxDepthHops = preset.maxDepthHops
	c.ProbeReuseEnabled = preset.probeReuseEnabled
	c.ReuseValueCeiling = preset.reuseValueCeiling
	c.EdgeRelayMaxHops = preset.edgeRelayMaxHops
	c.SnowballNeighbors = preset.snowballNeighbors
	c.PostInflightTimeout = preset.postInflightTimeout
	c.OffGateQueueExhaustionCycles = preset.offGateQueueExhaustionCycles
	c.OffGateWarpRangeFuel = preset.offGateWarpRangeFuel
	c.OffGateValueWeight = preset.offGateValueWeight
	c.OffGateFuelWeight = preset.offGateFuelWeight
}
