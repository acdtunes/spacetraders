package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
)

// ScoutPostTunableDefaults maps every LIVE-tunable scout-post-coordinator knob to its
// documented default — the value that applies when neither the live container config
// nor the launch command carries a positive one. The daemon's tune bounds registry
// reads THIS map (mirroring SizerTunableDefaults), so the defaults-of-record stay in
// this file next to the consts they mirror, and the map's KEY SET is the contract for
// which keys the watchdog live-overlays per tick (resolveManningStallConfig).
func ScoutPostTunableDefaults() map[string]int {
	return map[string]int{
		"manning_stall_cycles":             defaultManningStallCycles,
		"manning_stall_correction_cap":     defaultManningStallCorrectionCap,
		"scout_cross_system_relay_enabled": defaultScoutCrossSystemRelayEnabled, // int-mode flag (0=off)
		"scout_relay_max_hops":             defaultScoutRelayMaxHops,
		"market_drift_max_age_secs":        defaultMarketDriftMaxAgeSecs,
	}
}

// resolveMarketDriftMaxAge resolves the debounced market-set re-cut's AGE trigger for
// one tick, mirroring resolveManningStallConfig's live-overlay + <= 0 -> default idiom.
//
// NOTE what this is NOT. It is not a market-DATA freshness cap and is deliberately not
// derived from the scan rotation the way the tour path's caps are (sp-k4z5b): it bounds
// how long a PENDING PARTITION DRIFT waits before forcing a re-cut, measured from the
// moment the drift was first noticed, and a larger charted map does not invalidate it.
// What it did share with those caps was being unreachable without a daemon bounce, and
// that is what this fixes.
func resolveMarketDriftMaxAge(cmd *RunScoutPostCoordinatorCommand, live liveconfig.Snapshot) time.Duration {
	secs := cmd.MarketDriftMaxAgeSecs
	if live != nil {
		secs = live.PositiveIntOrZero("market_drift_max_age_secs")
	}
	if secs <= 0 {
		return defaultMarketDriftMaxAge
	}
	return time.Duration(secs) * time.Second
}

// scoutRelayConfig is the cross-system reuse relay's resolved per-tick knobs.
type scoutRelayConfig struct {
	enabled bool
	maxHops int
}

// resolveScoutRelayConfig resolves the cross-system reuse relay's two knobs for one tick,
// mirroring resolveManningStallConfig's live-overlay + <= 0 -> default idiom. A non-nil live snapshot
// is AUTHORITATIVE (launch values share the same config column, so an untuned knob still reads its
// launch value): scout_cross_system_relay_enabled reads as a > 0 flag (so `tune ... 0` genuinely
// disarms), scout_relay_max_hops falls to defaultScoutRelayMaxHops when absent/zeroed. A nil snapshot
// (reader unwired, or the read failed) runs on the launch command — the fail-safe launch behavior.
func resolveScoutRelayConfig(cmd *RunScoutPostCoordinatorCommand, live liveconfig.Snapshot) scoutRelayConfig {
	enabledFlag := cmd.ScoutCrossSystemRelayEnabled
	maxHops := cmd.ScoutRelayMaxHops
	if live != nil {
		enabledFlag = live.PositiveIntOrZero("scout_cross_system_relay_enabled")
		maxHops = live.PositiveIntOrZero("scout_relay_max_hops")
	}
	if maxHops <= 0 {
		maxHops = defaultScoutRelayMaxHops
	}
	return scoutRelayConfig{enabled: enabledFlag > 0, maxHops: maxHops}
}

// resolveManningStallConfig resolves the watchdog's two knobs for one tick. live is
// the tick-start snapshot of the container's persisted config (nil when the reader is unwired
// or the read failed — the tick then runs on the launch command, the fail-safe launch
// behavior). For these TUNABLE knobs a non-nil snapshot is AUTHORITATIVE (launch values share
// the same config column, so an untuned knob still reads its launch value here); a
// zeroed/absent key falls to the documented default — the `tune <key> 0` revert. Mirrors
// resolveSizerConfig's live-overlay + the <= 0 -> default idiom this file uses everywhere.
func resolveManningStallConfig(cmd *RunScoutPostCoordinatorCommand, live liveconfig.Snapshot) (cycles, correctionCap int) {
	cycles = cmd.ManningStallCycles
	correctionCap = cmd.ManningStallCorrectionCap
	if live != nil {
		cycles = live.PositiveIntOrZero("manning_stall_cycles")
		correctionCap = live.PositiveIntOrZero("manning_stall_correction_cap")
	}
	if cycles <= 0 {
		cycles = defaultManningStallCycles
	}
	if correctionCap <= 0 {
		correctionCap = defaultManningStallCorrectionCap
	}
	return cycles, correctionCap
}

// liveConfigSnapshot takes the tick's live-config snapshot for the manning watchdog.
// A nil reader (not wired — tests, minimal boots) or a read error yields nil, which
// resolveManningStallConfig treats as "run this tick on the launch command" — the fail-safe
// launch behavior, never a half-applied config. The read is logged, not fatal: a transient DB
// gap must never kill the reconcile loop or churn a tour.
func (h *RunScoutPostCoordinatorHandler) liveConfigSnapshot(ctx context.Context, cmd *RunScoutPostCoordinatorCommand) liveconfig.Snapshot {
	if h.liveConfig == nil {
		return nil
	}
	snap, err := h.liveConfig.Snapshot(ctx, cmd.ContainerID, cmd.PlayerID.Value())
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Scout post live config unreadable — this tick's watchdog knobs run on launch values: %v", err), nil)
		return nil
	}
	return snap
}

// resolveRespawnCap resolves the respawn-loop cap for this coordinator: the
// consecutive-respawn ceiling ([scouting] respawn_attempt_cap, else
// defaultScoutRespawnAttemptCap). Mirrors repositionFailureCooldown's <= 0 -> default shape.
func resolveRespawnCap(cmd *RunScoutPostCoordinatorCommand) int {
	if cmd.RespawnAttemptCap <= 0 {
		return defaultScoutRespawnAttemptCap
	}
	return cmd.RespawnAttemptCap
}

// resolveMaxRepositionJumps returns the expendable-probe reposition reach: the
// launch-config [scouting] max_reposition_jumps, or defaultMaxRepositionJumps when
// unset (RULINGS #5, the <= 0 -> default idiom this file uses for every other knob).
func resolveMaxRepositionJumps(cmd *RunScoutPostCoordinatorCommand) int {
	if cmd.MaxRepositionJumps <= 0 {
		return defaultMaxRepositionJumps
	}
	return cmd.MaxRepositionJumps
}

// resolveGateReconcileMaxDispatch returns the gate-reconcile per-tick relay cap: the
// launch config's GateReconcileMaxDispatch when positive, else defaultGateReconcileMaxDispatch
// (RULINGS #5, the <= 0 => default idiom). This is the rate-budget guard on the sweep.
func resolveGateReconcileMaxDispatch(cmd *RunScoutPostCoordinatorCommand) int {
	if cmd.GateReconcileMaxDispatch <= 0 {
		return defaultGateReconcileMaxDispatch
	}
	return cmd.GateReconcileMaxDispatch
}

// repositionFailureCooldown resolves the FAILED-relay cooldown: the launch config's
// [scouting] reposition_failure_cooldown_secs when positive, else the 30-min default. Mirrors
// resolveMaxRepositionJumps' <= 0 -> default shape.
func repositionFailureCooldown(cmd *RunScoutPostCoordinatorCommand) time.Duration {
	if cmd.RepositionFailureCooldownSecs <= 0 {
		return defaultRepositionFailureCooldown
	}
	return time.Duration(cmd.RepositionFailureCooldownSecs) * time.Second
}
