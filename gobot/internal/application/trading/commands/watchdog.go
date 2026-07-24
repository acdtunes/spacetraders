package commands

// watchdog.go — the SHARED sp-m3122 liveness-watchdog engine. Extracted from the
// trade-fleet coordinator (run_trade_fleet_coordinator.go) so ONE watchdog serves BOTH
// trade tours AND long-haul hauls (sp-mepj), not a fork: the design's "one watchdog serves
// trade tours AND long-haul". Everything fleet-agnostic lives here — the fail-closed
// progress read, the IN_TRANSIT skip (a flying hull is progressing), the kill, the stall
// threshold, the kill-failed-no-relaunch rule; each caller supplies only its own two
// fleet-specific inputs: the running-hull set it partitions, and a relaunch closure.

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// watchdogConfig is one fleet's binding of the shared watchdog engine: the two driven ports
// (progress read + kill — both keyed on containerID, so the daemon's implementations are
// reused verbatim across fleets), the stall threshold, a human label + machine action prefix
// for the honest logs, and the fleet's own relaunch closure (trade → LaunchTour, long-haul →
// LaunchLongHaul).
type watchdogConfig struct {
	liveness   TourLivenessPort
	stopper    TourStopper
	playerID   shared.PlayerID
	threshold  time.Duration
	humanLabel string // e.g. "Trade fleet" / "Long-haul" — message text
	action     string // e.g. "trade_fleet" / "longhaul" — structured action prefix
	// relaunch launches a fresh container for a hull whose hung one was just killed, returning
	// the new container ID. A relaunch failure is counted killed-not-relaunched (the released
	// hull rejoins the idle bucket and the idle path relaunches it next tick).
	relaunch func(ctx context.Context, shipSymbol string) (containerID string, err error)
}

// relaunchHungContainers is the shared watchdog engine. For each RUNNING container-claimed
// hull it reads the hull's last real-PROGRESS time and, if it has been parked-and-silent past
// the stall threshold, KILLS the container (which releases the hull) and relaunches a fresh
// one via cfg.relaunch — the automated form of the captain's manual `container stop`. It
// NEVER kills a hull IN_TRANSIT (a flying hull is on a legitimately-long leg — progress, not a
// stall), and it fails CLOSED: without BOTH ports, on a read error, or for any hull whose
// progress it cannot read, it kills nothing. Returns (killed, relaunched); a kill whose fresh
// launch failed is counted killed not relaunched.
func relaunchHungContainers(
	ctx context.Context,
	running []*navigation.Ship,
	now time.Time,
	logger common.ContainerLogger,
	cfg watchdogConfig,
) (killed, relaunched int) {
	// Fail closed: the watchdog acts only when it can both READ progress and KILL. A wiring
	// gap or a partial injection must never leave it detecting a hang it cannot remedy.
	if cfg.liveness == nil || cfg.stopper == nil || len(running) == 0 {
		return 0, 0
	}

	// Only PARKED-and-idle running hulls are watchdog candidates. Two states are
	// legitimately-waiting, not hung, and must never be killed even when the log-derived
	// progress looks old: a hull IN_TRANSIT is on a legitimately-long leg (progress), and a
	// hull mid-cooldown (IsOnCooldown) is waiting out a game jump/reactor timer it cannot act
	// through — a far sp-tp5c3 tour's multi-minute cooldown is a silent log gap the watchdog
	// would otherwise misread as a hang and false-kill (sp-39hjn), orphaning the hull's
	// reserved-but-unexecuted sells. The cooldown is read from the hull's OWN state, not the
	// log timestamp; a stale/past cooldown does NOT shield a genuinely hung hull. Gather the
	// remaining candidates' container IDs for the progress read.
	parked := make([]*navigation.Ship, 0, len(running))
	containerIDs := make([]string, 0, len(running))
	for _, ship := range running {
		if ship.IsInTransit() || ship.IsOnCooldown(now) {
			continue
		}
		parked = append(parked, ship)
		containerIDs = append(containerIDs, ship.ContainerID())
	}
	if len(parked) == 0 {
		return 0, 0
	}

	progress, err := cfg.liveness.LastTourProgress(ctx, cfg.playerID, containerIDs)
	if err != nil {
		// Fail closed for THIS tick: without the progress set we cannot prove any hull hung.
		logger.Log("WARNING", fmt.Sprintf("%s watchdog: could not read tour progress — killing nothing this tick: %v", cfg.humanLabel, err), map[string]interface{}{
			"action": cfg.action + "_watchdog_progress_read_failed",
		})
		return 0, 0
	}

	for _, ship := range parked {
		last, ok := progress[ship.ContainerID()]
		if !ok || last.IsZero() {
			continue // unknown progress — never kill a hull whose liveness we could not read
		}
		stalledFor := now.Sub(last)
		if stalledFor < cfg.threshold {
			continue // still progressing within the threshold
		}

		// HUNG. Kill the container (which releases the hull), then relaunch a fresh one.
		reason := fmt.Sprintf("sp-m3122 watchdog: RUNNING but hung — no progress for %s (>= %s stall threshold)", stalledFor.Truncate(time.Second), cfg.threshold)
		if serr := cfg.stopper.StopTour(ctx, ship.ContainerID(), reason); serr != nil {
			logger.Log("WARNING", fmt.Sprintf("%s watchdog: failed to kill hung container %s on %s: %v", cfg.humanLabel, ship.ContainerID(), ship.ShipSymbol(), serr), map[string]interface{}{
				"action":       cfg.action + "_watchdog_kill_failed",
				"ship_symbol":  ship.ShipSymbol(),
				"container_id": ship.ContainerID(),
			})
			continue // do NOT relaunch: the doomed container may still hold the hull
		}
		killed++

		containerID, lerr := cfg.relaunch(ctx, ship.ShipSymbol())
		if lerr != nil {
			logger.Log("WARNING", fmt.Sprintf("%s watchdog: killed hung container on %s but relaunch failed (retry next tick): %v", cfg.humanLabel, ship.ShipSymbol(), lerr), map[string]interface{}{
				"action":      cfg.action + "_watchdog_relaunch_failed",
				"ship_symbol": ship.ShipSymbol(),
			})
			continue
		}
		relaunched++
		logger.Log("INFO", fmt.Sprintf(
			"%s hull %s made no progress for %s (>= %s stall threshold) — HUNG: killed container %s and relaunched fresh %s (sp-m3122)",
			cfg.humanLabel, ship.ShipSymbol(), stalledFor.Truncate(time.Second), cfg.threshold, ship.ContainerID(), containerID), map[string]interface{}{
			"action":           cfg.action + "_watchdog_relaunch",
			"ship_symbol":      ship.ShipSymbol(),
			"killed_container": ship.ContainerID(),
			"new_container":    containerID,
			"stalled_secs":     int(stalledFor.Seconds()),
			"threshold_secs":   int(cfg.threshold.Seconds()),
		})
	}
	return killed, relaunched
}
