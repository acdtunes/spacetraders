package commands

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
)

// The autosizer's live-tunable keys. Both are BARE (unprefixed) on purpose: the daemon's
// resolveFleetAutosizerConfig CLEARS and re-injects every `autosizer_*` launch key from config.yaml
// on each container rebuild, so a tune written to a prefixed key is wiped by the next daemon bounce.
// A bare key is untouched by that cycle and therefore survives (see fleetAutosizerConfigKeys in
// container_ops_fleet_autosizer.go). Every OTHER autosizer knob remains config.yaml + restart, so
// adding to this list is a deliberate act, not a pattern to copy blindly.
const (
	// heavyCapKey caps owned HEAVY HULLS (capital exposure).
	heavyCapKey = "heavy_cap"

	// sizingEnabledKey is the autosizer's MASTER SWITCH. Off, the coordinator skips the whole
	// reconcile tick — see liveSizingEnabled for why this gates the READS and not just the buy.
	sizingEnabledKey = "sizing_enabled"
)

const (
	// defaultSizingEnabled is ON. The autosizer ships ARMED: the knob is the control, and the
	// decision to disable is a TUNE (which persists across daemon restarts). Shipping default-off
	// would be a silent behaviour change for anyone else running this code.
	defaultSizingEnabled = 1

	// sizingDisabled is the sentinel that switches sizing off.
	//
	// 1=ON, 2=OFF — deliberately NOT 0/1. `tune <key> 0` DELETES the key and means
	// revert-to-default fleet-wide, so a 0/1 encoding would make "off" literally unexpressible.
	// expansion_enabled learned this first; this follows that precedent exactly, including saying
	// so in the knob's registered description.
	sizingDisabled = 2
)

// FleetAutosizerTunableDefaults returns the autosizer's live-tunable knob → documented-default map
// — the SINGLE source the daemon's tune bounds registry reads, so a registry default can never
// drift from the coordinator's own (the anti-drift test in container_ops_tune_test.go pins this).
//
// TWO levers are exposed: sizing_enabled (the master switch) and heavy_cap (the ceiling on owned
// HEAVY HULLS). The money guards they sit beside — the immutable 50k reserve floor, the
// 25%-treasury rule, the era-payback gate — are compile-time consts and are deliberately NOT
// tunable.
func FleetAutosizerTunableDefaults() map[string]int {
	return map[string]int{
		heavyCapKey:      defaultHeavyCap,
		sizingEnabledKey: defaultSizingEnabled,
	}
}

// SetHeavyCapReader wires the per-tick live-config snapshot the autosizer's live knobs read.
// Named for heavy_cap because that was the first such knob; it now feeds sizing_enabled too.
func (h *RunFleetAutosizerCoordinatorHandler) SetHeavyCapReader(r liveconfig.Reader) {
	h.heavyCapCfg = r
}

// liveKnobs takes the tick's ONE live-config snapshot and resolves BOTH live-tunable knobs from it
// (the Pattern-C hot reload). One snapshot per tick is the documented liveconfig
// discipline — a second read would double the coordinator's config traffic and could see two
// different configs within a single tick.
//
// reconcileOnce already re-resolves the whole config every tick, so this changes the SOURCE of
// these knobs, not the cadence.
//
// Fallback chain, deliberately never zeroing:
//   - nil reader        → launch heavy cap, sizing ON (the coordinator is not wired for live config)
//   - snapshot error    → launch heavy cap, sizing ON (transient DB blip). Both fall back to the
//     LAUNCH behaviour rather than to a state derived from the failure: a DB blip must not silently
//     stop heavy buying, and it must not silently disable the whole autosizer for an operator who
//     never tuned it. The cost is that a PERSISTENTLY unreadable config cannot honour a pause —
//     the same limitation every sibling live knob carries.
//   - key absent or ≤ 0 → the documented default (heavy cap: launch value; sizing: ON).
//     `tune <key> 0` DELETES the key, so absence IS the revert.
//   - heavy cap positive → the live value.
//   - sizing == 2       → OFF. Anything else, including the absent-key 0, is ON.
func (h *RunFleetAutosizerCoordinatorHandler) liveKnobs(ctx context.Context, cmd *RunFleetAutosizerCoordinatorCommand, launchHeavyCap int) (heavyCap int, sizingOn bool) {
	if h.heavyCapCfg == nil {
		return launchHeavyCap, true
	}
	snap, err := h.heavyCapCfg.Snapshot(ctx, cmd.ContainerID, cmd.PlayerID)
	if err != nil {
		return launchHeavyCap, true
	}
	heavyCap = launchHeavyCap
	if v := snap.PositiveIntOrZero(heavyCapKey); v > 0 {
		heavyCap = v
	}
	return heavyCap, snap.PositiveIntOrZero(sizingEnabledKey) != sizingDisabled
}

// liveHeavyCap resolves heavy_cap alone from the container's persisted config. It is the
// single-knob view of liveKnobs, kept because heavy_cap's semantics are documented and tested
// independently of the master switch.
//
// This is also why a tuned 0 cannot express the heavy "operator hold": holding at zero is a
// config.yaml + restart operation (see config.FleetAutosizerConfig.HeavyCap).
func (h *RunFleetAutosizerCoordinatorHandler) liveHeavyCap(ctx context.Context, cmd *RunFleetAutosizerCoordinatorCommand, launchValue int) int {
	cap, _ := h.liveKnobs(ctx, cmd, launchValue)
	return cap
}
