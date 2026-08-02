package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

func (h *RunTradeFleetCoordinatorHandler) launchTourForHull(
	ctx context.Context,
	cmd *RunTradeFleetCoordinatorCommand,
	ship *navigation.Ship,
	cooldown time.Duration,
	reachEscalated bool,
	logger common.ContainerLogger,
) bool {
	spec := buildTourLaunchSpec(cmd, ship.ShipSymbol(), reachEscalated)
	containerID, lerr := h.launcher.LaunchTour(ctx, spec)
	if lerr != nil {
		// A single hull's launch failure (e.g. it was claimed between the snapshot
		// and the launch, or the daemon refused it) must not abort the pass — the
		// rest of the fleet still gets serviced, and this hull retries next tick.
		logger.Log("WARNING", fmt.Sprintf("Failed to relaunch tour for trade hull %s: %v", ship.ShipSymbol(), lerr), map[string]interface{}{
			"action":      "trade_fleet_relaunch_failed",
			"ship_symbol": ship.ShipSymbol(),
		})
		return false
	}
	reachNote := ""
	if reachEscalated {
		reachNote = ", reposition-reach ARMED (escalate-to-movement, sp-nxrt)"
	}
	logger.Log("INFO", fmt.Sprintf(
		"Relaunched continuous tour for trade hull %s (prior exit: %s, cooldown %s, container %s%s)",
		ship.ShipSymbol(), priorExitReasonLabel(ship), cooldown.Truncate(time.Second), containerID, reachNote), map[string]interface{}{
		"action":            "trade_fleet_relaunch",
		"ship_symbol":       ship.ShipSymbol(),
		"container_id":      containerID,
		"prior_exit_reason": priorExitReason(ship),
		"cooldown_secs":     int(cooldown.Seconds()),
		"reposition_reach":  reachEscalated,
	})
	return true
}

// relaunchHungTours is the sp-m3122 liveness watchdog — the primary fix. For each RUNNING
// trade tour it reads the tour's last real-PROGRESS time (plan/navigate/arrive/buy/sell) and,
// if the tour has been parked-and-silent past the stall threshold, KILLS the container and
// relaunches a fresh tour on the hull — the automated form of the captain's manual
// `container stop <hung-tour>` (which is how the sp-m3122 incident was cleared by hand). A
// fresh tour re-plans from scratch against current sinks and offloads any held cargo via the
// existing held-cargo-aware path (sp-2v69u), so a hull stranded FULL by a hung tour resumes
// selling. This self-heals ANY hang — restart-induced or otherwise — and is the backstop that
// makes keeping the mid-jump restart-resume safe (the bead's explicit allowance).
//
// It NEVER kills a hull IN_TRANSIT: a flying hull is on a legitimately-long leg (multi-hop
// travel / jump), which is progress, not a stall — this is how the watchdog keys on PROGRESS
// rather than wall-clock silence. And it fails CLOSED: without BOTH ports wired, on a liveness
// read error, or for any container whose progress it cannot read, it kills nothing — a
// watchdog that cannot confirm a hang must never "remedy" one. Returns (killed, relaunched);
// a kill whose fresh launch failed is counted in killed but not relaunched (the released hull
// rejoins the idle bucket and the idle path relaunches it next tick).
func (h *RunTradeFleetCoordinatorHandler) relaunchHungTours(
	ctx context.Context,
	cmd *RunTradeFleetCoordinatorCommand,
	running []*navigation.Ship,
	now time.Time,
	logger common.ContainerLogger,
) (killed, relaunched int) {
	// The watchdog ENGINE is shared with the long-haul engine (relaunchHungContainers, see
	// watchdog.go) so ONE watchdog serves trade tours AND long-haul hauls (sp-mepj), not a
	// fork. Trade supplies its own tour-relaunch closure; the fail-closed progress read, the
	// IN_TRANSIT skip, the kill, the kill-failed-no-relaunch rule, and the stall threshold all
	// live in the shared engine. The held-cargo-aware fresh tour (sp-2v69u) is the relaunch's
	// own behavior, so a hull stranded FULL by a hung tour still resumes selling.
	return relaunchHungContainers(ctx, running, now, logger, watchdogConfig{
		liveness:   h.tourLiveness,
		stopper:    h.tourStopper,
		playerID:   cmd.PlayerID,
		threshold:  cmd.watchdogStallThreshold(),
		humanLabel: "Trade fleet",
		action:     "trade_fleet",
		relaunch: func(ctx context.Context, shipSymbol string) (string, error) {
			return h.launcher.LaunchTour(ctx, buildTourLaunchSpec(cmd, shipSymbol, false))
		},
	})
}

// reclaimDeadContainerAbsorption promptly releases market_absorption_ledger reservations held
// by containers that no longer exist (sp-m3122 part 3). Nil-safe: without a reclaimer wired it
// is a no-op. Best-effort: a reclaim error is logged and swallowed (the TTL sweep remains the
// backstop), never aborting the reconcile pass.
func (h *RunTradeFleetCoordinatorHandler) reclaimDeadContainerAbsorption(ctx context.Context, cmd *RunTradeFleetCoordinatorCommand, logger common.ContainerLogger) {
	if h.absorptionReclaimer == nil {
		return
	}
	reclaimed, err := h.absorptionReclaimer.ReclaimDeadContainerAbsorption(ctx, cmd.PlayerID)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Trade fleet: dead-container absorption reclaim failed: %v", err), map[string]interface{}{
			"action": "trade_fleet_absorption_reclaim_failed",
		})
		return
	}
	if reclaimed > 0 {
		logger.Log("INFO", fmt.Sprintf("Trade fleet: reclaimed %d absorption reservation(s) held by non-existent containers (sp-m3122)", reclaimed), map[string]interface{}{
			"action":    "trade_fleet_absorption_reclaimed",
			"reclaimed": reclaimed,
		})
	}
}

// buildTourLaunchSpec assembles the launch request for one hull, shared by the idle-relaunch
// path and the sp-m3122 watchdog relaunch (one spec shape, no drift). reachEscalated arms
// reposition-reach for this tour (sp-nxrt); the watchdog always passes false — a fresh tour on
// a hung hull re-plans normally, it was not a margins-death fast-fail.
func buildTourLaunchSpec(cmd *RunTradeFleetCoordinatorCommand, shipSymbol string, reachEscalated bool) TourLaunchSpec {
	return TourLaunchSpec{
		ShipSymbol:               shipSymbol,
		MaxHops:                  cmd.MaxHops,
		MaxSpend:                 cmd.MaxSpend,
		MinMargin:                cmd.MinMargin,
		ReplanLimit:              cmd.ReplanLimit,
		WorkingCapitalReserve:    cmd.WorkingCapitalReserve,
		AgentSymbol:              cmd.AgentSymbol,
		Iterations:               tourIterationsContinuous,
		PlayerID:                 cmd.PlayerID.Value(),
		RepositionReachEscalated: reachEscalated,
	}
}

// TourLaunchSpec is the launch request the coordinator hands the daemon for one idle
// hull (sp-1278). It carries only decision inputs — the daemon owns claim, container
// persistence, and the operation="trade" stamp (StartTourRun), so the coordinator
// stays a pure decision loop that claims nothing itself (RULINGS #3/#7).
type TourLaunchSpec struct {
	ShipSymbol            string
	MaxHops               int
	MaxSpend              int64
	MinMargin             int
	ReplanLimit           int
	WorkingCapitalReserve int64
	AgentSymbol           string
	Iterations            int
	PlayerID              int

	// RepositionReachEscalated arms reposition-reach for THIS launch (sp-nxrt part a),
	// overriding the daemon-global reposition_reach_enabled. The fleet coordinator sets it
	// on the relaunch of a hull that has fast-failed twice in a row: instead of doubling the
	// hull's sleep again, it relaunches promptly with the broadened 2-4-gate-hop reach
	// discovery armed, so a hull whose lane died HERE moves to a fresh system it could not
	// see over the default 1-hop scan. Zero value (false) is a normal launch — byte-identical
	// to a config-only reach setting — so every non-escalated relaunch and the captain CLI
	// path are unchanged.
	RepositionReachEscalated bool
}

// TourLauncher starts one recovery-safe, guarded continuous tour container for an idle
// trade hull and returns its container ID. Implemented by the daemon server
// (DaemonServer.LaunchTour → StartTourRun), so the launch goes through the SAME path
// `workflow tour-run` uses: the atomic operation="trade" ClaimShip, the single-writer
// container row, and release-on-death all apply, and the coordinator never touches ship
// state. Mirrors appContract.IdleArbLauncher's shape (a narrow port faked trivially in
// tests).
type TourLauncher interface {
	LaunchTour(ctx context.Context, spec TourLaunchSpec) (containerID string, err error)
}

// TourLivenessPort reports the last real-PROGRESS time of each named running tour container
// (sp-m3122): the most recent moment the tour actually did something — planned, navigated,
// arrived, bought, or sold. It is deliberately NOT the container's wall-clock heartbeat: a
// tour whose worker goroutine has silently died or wedged still has a fresh heartbeat (a
// separate goroutine keeps stamping it), which is exactly how a hung tour masqueraded as
// healthy for hours. The daemon implements it over the container activity trail it single-
// writes; a container absent from the returned map has no known progress and is left alone
// (the watchdog never kills a tour whose liveness it could not read). Optional-injection like
// ActiveContainerShipsPort: a nil port makes the watchdog inert (fail-closed).
type TourLivenessPort interface {
	LastTourProgress(ctx context.Context, playerID shared.PlayerID, containerIDs []string) (map[string]time.Time, error)
}

// TourStopper kills one hung tour container by ID (sp-m3122) — the watchdog's remedy, the
// automated form of the captain's manual `container stop <hung-tour>`. The daemon implements
// it (StopContainer): the stop releases the hull's claim, so the next relaunch re-claims it
// cleanly. Optional-injection: without a stopper the watchdog is inert (fail-closed — it can
// detect a hang but must not "handle" it by leaving the hull relaunched under a still-live
// doomed container).
type TourStopper interface {
	StopTour(ctx context.Context, containerID, reason string) error
}

// DeadContainerAbsorptionReclaimer promptly releases market_absorption_ledger reservations
// held by containers that no longer exist (sp-m3122 part 3), rather than waiting for the TTL
// sweep — so phantom reservations left by a restart or by a just-killed hung tour never make
// open sinks look contended. The daemon implements it over the ledger's existing dead-
// container sweep. Optional-injection: a nil reclaimer skips the reclaim (nil-safe).
type DeadContainerAbsorptionReclaimer interface {
	ReclaimDeadContainerAbsorption(ctx context.Context, playerID shared.PlayerID) (int, error)
}

// SetTourLauncher wires the daemon-server launcher each relaunch spawns its tour
// container through (sp-1278). Optional-injection like the contract coordinator's
// SetIdleArbLauncher: without it a reconcile pass is a fail-closed no-op (it cannot
// launch), never a panic.
func (h *RunTradeFleetCoordinatorHandler) SetTourLauncher(launcher TourLauncher) {
	h.launcher = launcher
}

// SetEventRecorder wires the captain outbox the coordinator emits its
// error-loop event through. Optional-injection like SetTourLauncher:
// without it the streak monitor still tracks and logs, it just cannot escalate
// to a captain event (nil-safe, see health.RecordErrorLoop).
func (h *RunTradeFleetCoordinatorHandler) SetEventRecorder(rec captain.EventRecorder) {
	h.captainEvents = rec
}

// SetTourLiveness wires the sp-m3122 watchdog's progress signal — the port that reports each
// running tour's last real-progress time. Optional-injection like SetTourLauncher: without it
// (or without a stopper) the watchdog is inert (fail-closed), never a panic.
func (h *RunTradeFleetCoordinatorHandler) SetTourLiveness(port TourLivenessPort) {
	h.tourLiveness = port
}

// SetTourStopper wires the sp-m3122 watchdog's remedy — the port that kills a hung tour
// container. Optional-injection like SetTourLauncher: without it the watchdog can detect a
// hang but takes no action (fail-closed).
func (h *RunTradeFleetCoordinatorHandler) SetTourStopper(port TourStopper) {
	h.tourStopper = port
}

// SetAbsorptionReclaimer wires the sp-m3122 part-3 dead-container absorption reclaim.
// Optional-injection: without it the reclaim is skipped (nil-safe).
func (h *RunTradeFleetCoordinatorHandler) SetAbsorptionReclaimer(port DeadContainerAbsorptionReclaimer) {
	h.absorptionReclaimer = port
}
