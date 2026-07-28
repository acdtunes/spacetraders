package commands

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
)

// heavyCapKey is the autosizer's ONE live-tunable knob. It is the FIRST autosizer_* tune key:
// every other autosizer knob remains config.yaml + restart, so adding more is a deliberate act,
// not a pattern to copy blindly.
const heavyCapKey = "heavy_cap"

// FleetAutosizerTunableDefaults returns the autosizer's live-tunable knob → documented-default map
// — the SINGLE source the daemon's tune bounds registry reads, so a registry default can never
// drift from the coordinator's own (the anti-drift test in container_ops_tune_test.go pins this).
//
// Exactly ONE lever is exposed: heavy_cap, the ceiling on owned HEAVY HULLS. The money guards it
// sits beside — the immutable 50k reserve floor, the 25%-treasury rule, the era-payback gate — are
// compile-time consts and are deliberately NOT tunable.
func FleetAutosizerTunableDefaults() map[string]int {
	return map[string]int{
		heavyCapKey: defaultHeavyCap,
	}
}

// SetHeavyCapReader wires the per-tick live-config snapshot for heavy_cap.
func (h *RunFleetAutosizerCoordinatorHandler) SetHeavyCapReader(r liveconfig.Reader) {
	h.heavyCapCfg = r
}

// liveHeavyCap resolves heavy_cap from the container's persisted config each tick (the sp-ts82
// Pattern-C hot reload), falling back to launchValue.
//
// reconcileOnce already re-resolves the whole config every tick — it simply resolved from the
// launch-frozen command struct — so this replaces the SOURCE of one knob, not the cadence.
//
// Fallback chain, deliberately never zeroing:
//   - nil reader        → launch value (the coordinator is not wired for live config)
//   - snapshot error    → launch value (transient DB blip; NEVER 0, which would read as an
//     operator hold and silently stop all heavy buying)
//   - key absent or ≤ 0 → launch value. `tune <key> 0` DELETES the key, so absence IS the revert.
//     This is also why a tuned 0 cannot express the operator hold: holding at zero is a
//     config.yaml + restart operation (see config.FleetAutosizerConfig.HeavyCap).
//   - positive value    → the live value
func (h *RunFleetAutosizerCoordinatorHandler) liveHeavyCap(ctx context.Context, cmd *RunFleetAutosizerCoordinatorCommand, launchValue int) int {
	if h.heavyCapCfg == nil {
		return launchValue
	}
	snap, err := h.heavyCapCfg.Snapshot(ctx, cmd.ContainerID, cmd.PlayerID)
	if err != nil {
		return launchValue
	}
	if v := snap.PositiveIntOrZero(heavyCapKey); v > 0 {
		return v
	}
	return launchValue
}
