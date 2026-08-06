package commands

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
)

// The growth coordinator's live-tunable keys. heavy_cap and growth_enabled are BARE (unprefixed)
// for the same reason the autosizer's are: they are the two knobs an operator reaches for
// mid-incident, and the cap read that sensing's reservation consumes resolves the BARE key first
// and the coordinator-prefixed launch key second, matched on its "_heavy_cap" suffix. Every OTHER
// growth knob remains a launch key, so adding to this list is a deliberate act.
const (
	// growthEnabledKey is the growth coordinator's MASTER SWITCH. Off, the coordinator skips the
	// whole reconcile tick — see liveKnobs for why this gates the READS and not just the buy.
	growthEnabledKey = "growth_enabled"

	// growthRunwayKey is the working-capital term's runway multiplier, in MILLI-hours. Live
	// because the observation that drives a retune — "heavies are never affordable, the runway
	// term is eating the treasury" — is one an operator makes while the fleet is running.
	growthRunwayKey = "growth_runway_milli_hours"
)

const (
	// defaultGrowthEnabled is ON. The coordinator ships ARMED: the knob is the control, and the
	// decision to disable is a TUNE (which persists across daemon restarts).
	defaultGrowthEnabled = 1

	// growthDisabled is the sentinel that switches growth off.
	//
	// 1=ON, 2=OFF — deliberately NOT 0/1. `tune <key> 0` DELETES the key and means
	// revert-to-default fleet-wide, so a 0/1 encoding would make "off" literally unexpressible.
	growthDisabled = 2
)

// FleetGrowthTunableDefaults returns the coordinator's live-tunable knob → documented-default map
// — the SINGLE source the daemon's tune bounds registry reads, so a registry default can never
// drift from the coordinator's own.
//
// THREE levers: the master switch, the ceiling on owned heavy hulls, and the working-capital
// runway. The money guards they sit beside — the immutable reserve floor and the treasury-share
// rule — are compile-time consts and are deliberately NOT tunable. The runway is not one of them:
// whatever it resolves to, it only ever ADDS to that floor and can never lower it.
func FleetGrowthTunableDefaults() map[string]int {
	return map[string]int{
		heavyCapKey:      defaultGrowthHeavyCap,
		growthEnabledKey: defaultGrowthEnabled,
		growthRunwayKey:  defaultGrowthRunwayMilliHours,
	}
}

// SetGrowthConfigReader wires the per-tick live-config snapshot the live knobs read.
func (h *RunFleetGrowthCoordinatorHandler) SetGrowthConfigReader(r liveconfig.Reader) {
	h.growthCfg = r
}

// liveKnobs takes the tick's ONE live-config snapshot and resolves every live-tunable knob from it
// (the Pattern-C hot reload). One snapshot per tick is the documented liveconfig discipline — a
// second read would double the coordinator's config traffic and could see two different configs
// within a single tick.
//
// Fallback chain, deliberately never zeroing:
//   - nil reader        → the launch values, growth ON (the coordinator is not wired for live config)
//   - snapshot error    → the launch values, growth ON (transient DB blip). Both fall back to the
//     LAUNCH behaviour rather than to a state derived from the failure: a DB blip must not silently
//     stop heavy buying, and it must not silently disable the coordinator for an operator who never
//     tuned it. The cost is that a PERSISTENTLY unreadable config cannot honour a pause — the same
//     limitation every sibling live knob carries.
//   - key absent or ≤ 0 → the launch value. `tune <key> 0` DELETES the key, so absence IS the revert.
//   - growth == 2       → OFF. Anything else, including the absent-key 0, is ON.
func (h *RunFleetGrowthCoordinatorHandler) liveKnobs(ctx context.Context, cmd *RunFleetGrowthCoordinatorCommand, launchHeavyCap, launchRunway int) (heavyCap, runwayMilliHours int, growthOn bool) {
	if h.growthCfg == nil {
		return launchHeavyCap, launchRunway, true
	}
	snap, err := h.growthCfg.Snapshot(ctx, cmd.ContainerID, cmd.PlayerID)
	if err != nil {
		return launchHeavyCap, launchRunway, true
	}
	heavyCap, runwayMilliHours = launchHeavyCap, launchRunway
	if v := snap.PositiveIntOrZero(heavyCapKey); v > 0 {
		heavyCap = v
	}
	if v := snap.PositiveIntOrZero(growthRunwayKey); v > 0 {
		runwayMilliHours = v
	}
	return heavyCap, runwayMilliHours, snap.PositiveIntOrZero(growthEnabledKey) != growthDisabled
}
