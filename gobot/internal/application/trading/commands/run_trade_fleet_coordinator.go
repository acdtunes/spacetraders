package commands

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const (
	// tradeFleet is the Ship.DedicatedFleet() value this coordinator watches
	// (sp-sg35). A hull pinned here is claimed by its tour_run container under
	// operation="trade"; the coordinator itself claims NOTHING. Unpinning a hull
	// (DedicatedFleet() != "trade") removes it from this coordinator's view for
	// free — the captain's no-restart, per-hull off-switch (RULINGS #7 dedication
	// is the poach guard, and here it doubles as the opt-out).
	tradeFleet = "trade"

	// defaultTradeFleetTickSeconds is the reconcile cadence when the launch config
	// leaves it unset (RULINGS #5: parametrized, not hardcoded at the call site).
	// Mirrors the scout-post coordinator's 30s default — a park is at most one tick
	// of idle before relaunch.
	defaultTradeFleetTickSeconds = 30

	// defaultTradeFleetCooldownSeconds is the per-hull relaunch cooldown when the
	// config leaves it unset (bead sp-1278). A tour exits honestly when margins die
	// in BOTH systems; relaunching instantly would re-plan against the same tapped
	// ground and exit again. The cooldown lets the local ground breathe through the
	// lxwn rich->tapped->rich cycle (minutes) before the next tour re-plans against
	// a recovered market. 3min sits in the bead's 2-5min band.
	defaultTradeFleetCooldownSeconds = 180

	// tourIterationsContinuous makes every relaunched tour a CONTINUOUS run (sp-m5kv):
	// the tour re-plans and re-flies from its new position until margins die in both
	// systems (the honest exit), then THIS coordinator relaunches it after the
	// cooldown. It is fixed, not configurable: a finite tour would exit after N tours
	// and park the hull — exactly the captain-time sink this coordinator retires.
	tourIterationsContinuous = -1

	// defaultRelaunchBackoffMaxSeconds is the ceiling for the per-hull ADAPTIVE
	// relaunch backoff (sp-1pli) when the config leaves it unset. When a hull's
	// continuous tour exits unproductive (fast-fail, see minProductiveTourDuration),
	// the coordinator doubles that hull's relaunch cooldown from the base
	// (defaultTradeFleetCooldownSeconds or CooldownSecs) up to this ceiling, instead
	// of retrying the full discovery+solver pass every base cooldown forever against a
	// fleet-wide-infeasible market (862 tour-run log lines in 20 minutes prompted this
	// bead). 30min is the brief's own stated ceiling.
	defaultRelaunchBackoffMaxSeconds = 1800

	// minProductiveTourDuration is the fast-fail line between an honest trade leg and
	// a tour that never really flew (sp-1pli). It is a hardcoded mechanism constant,
	// not a config knob (RULINGS #5 governs operational values; this is an internal
	// classification threshold) — deliberately conservative/asymmetric: biased toward
	// NOT escalating rather than wrongly punishing a hull that spent its one sp-zhii
	// rescue-reposition jump before ultimately starving, which can look identical to a
	// short real trade leg from duration alone. A missed escalation just costs one more
	// base-cooldown cycle before the next fast-fail exit catches it.
	minProductiveTourDuration = 90 * time.Second
)

// RunTradeFleetCoordinatorCommand launches the standing trade-fleet coordinator for
// a player (sp-1278). Like the scout-post and contract-fleet coordinators it runs an
// infinite reconcile loop inside a single Handle() call; the container wraps that one
// loop (created with iterations=-1, so the container-level budget is irrelevant — it
// is NOT a CoordinatorOwnsIterations type).
type RunTradeFleetCoordinatorCommand struct {
	PlayerID    shared.PlayerID
	ContainerID string
	// AgentSymbol is threaded through to each tour launch (StartTourRun needs it to
	// resolve the agent's live treasury for the 25%-of-treasury spend cap).
	AgentSymbol string

	// TickIntervalSecs is the reconcile cadence; <=0 uses defaultTradeFleetTickSeconds.
	TickIntervalSecs int

	// Enabled is the captain's config off-switch (RULINGS #5). When false the
	// reconcile pass is inert — the container still runs, so flipping trade_fleet.enabled
	// back on in config.yaml and restarting the daemon re-arms it with no manual
	// relaunch. The default (true) is applied at config-resolution time, so a
	// recovered command with Enabled unset in an old persisted config still runs ON.
	Enabled bool

	// CooldownSecs is the per-hull relaunch cooldown; <=0 uses the default.
	CooldownSecs int

	// MaxConcurrentTours caps simultaneously-running trade tours; <=0 means unlimited
	// (bounded naturally by fleet size — every idle trade hull is relaunched). A
	// positive cap holds surplus idle hulls this tick, honest, until a running tour
	// frees a slot.
	MaxConcurrentTours int

	// Tour launch knobs, passed verbatim to StartTourRun. 0 => the tour's own
	// documented default for that knob (max_hops->6, max_spend->25% of live treasury,
	// replan_limit->2, working_capital_reserve->the non-tunable floor). Sourced live
	// from config.yaml's [trade_fleet] section on every build so an edit+restart
	// retunes a recovered coordinator (sp-ts82 live-config pattern).
	MaxHops               int
	MaxSpend              int64
	MinMargin             int
	ReplanLimit           int
	WorkingCapitalReserve int64
	// WorkingCapitalReserveTreasuryPct is the sp-yqx4 counter-cyclical floor percent, passed
	// verbatim to each StartTourRun. 0 => the tour's 40% default resolved at build.
	WorkingCapitalReserveTreasuryPct int

	// RelaunchBackoffMaxSecs caps the per-hull ADAPTIVE relaunch backoff (sp-1pli);
	// <=0 uses defaultRelaunchBackoffMaxSeconds. See relaunchBackoffMaxDuration and the
	// handler's hullBackoff/cooldownFor for the escalation mechanism itself.
	RelaunchBackoffMaxSecs int
}

// RunTradeFleetCoordinatorResponse reports reconcile progress. Because the loop is
// infinite it is only observed on context cancellation (shutdown).
type RunTradeFleetCoordinatorResponse struct {
	Ticks    int
	Launched int
	Errors   []string
}

// TourLaunchSpec is the launch request the coordinator hands the daemon for one idle
// hull (sp-1278). It carries only decision inputs — the daemon owns claim, container
// persistence, and the operation="trade" stamp (StartTourRun), so the coordinator
// stays a pure decision loop that claims nothing itself (RULINGS #3/#7).
type TourLaunchSpec struct {
	ShipSymbol                       string
	MaxHops                          int
	MaxSpend                         int64
	MinMargin                        int
	ReplanLimit                      int
	WorkingCapitalReserve            int64
	WorkingCapitalReserveTreasuryPct int // sp-yqx4 counter-cyclical floor percent (0 → 40% default at build)
	AgentSymbol                      string
	Iterations                       int
	PlayerID                         int
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

// RunTradeFleetCoordinatorHandler keeps continuous tours alive across the 'trade'
// fleet (sp-1278). Every reconcile pass snapshots the fleet, and for each trade hull
// parked by an honest tour exit (idle, past its cooldown) it relaunches a fresh
// continuous tour through the launcher — retiring the captain's hand-relaunch loop.
//
// It is deliberately the minimal fleet coordinator: it claims nothing (each tour
// container claims its own hull), it holds no per-hull relaunch state (the cooldown is
// DERIVED from the hull's last release time, so it survives coordinator restarts for
// free — RULINGS #2), and it never rewrites a tour's exit reason (it only READS the
// prior release reason to log it, so zhii/L5 honest-exit telemetry accumulates
// unchanged).
type RunTradeFleetCoordinatorHandler struct {
	shipRepo navigation.ShipRepository
	clock    shared.Clock

	// launcher starts each tour through the daemon's StartTourRun path (sp-1278). nil
	// (tests that never wire it, or a daemon boot before DI completes) fails the
	// reconcile pass honestly rather than silently launching nothing; wired via
	// SetTourLauncher, mirroring the contract coordinator's SetIdleArbLauncher idiom.
	launcher TourLauncher

	// backoff tracks each hull's adaptive relaunch cooldown (sp-1pli), keyed by ship
	// symbol. Deliberately in-memory only, NOT derived from persisted ship state like
	// the base cooldown (RULINGS #2 would prefer that, but no persisted field carries
	// "consecutive unproductive tour count") — a coordinator restart resets every hull
	// to the base cooldown, a documented, self-healing trade-off: worst case is one
	// extra fast-fail cycle per hull before backoff re-accumulates.
	backoff map[string]*hullBackoff
}

// NewRunTradeFleetCoordinatorHandler wires the coordinator. clock defaults to the real
// clock when nil (production). The tour launcher is injected separately via
// SetTourLauncher (the daemon server implements it), mirroring the contract fleet
// coordinator's launcher injection.
func NewRunTradeFleetCoordinatorHandler(shipRepo navigation.ShipRepository, clock shared.Clock) *RunTradeFleetCoordinatorHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RunTradeFleetCoordinatorHandler{shipRepo: shipRepo, clock: clock, backoff: make(map[string]*hullBackoff)}
}

// SetTourLauncher wires the daemon-server launcher each relaunch spawns its tour
// container through (sp-1278). Optional-injection like the contract coordinator's
// SetIdleArbLauncher: without it a reconcile pass is a fail-closed no-op (it cannot
// launch), never a panic.
func (h *RunTradeFleetCoordinatorHandler) SetTourLauncher(launcher TourLauncher) {
	h.launcher = launcher
}

// Handle runs the reconcile loop until the context is cancelled.
func (h *RunTradeFleetCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	logger := common.LoggerFromContext(ctx)

	cmd, ok := request.(*RunTradeFleetCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	tick := time.Duration(cmd.TickIntervalSecs) * time.Second
	if tick <= 0 {
		tick = defaultTradeFleetTickSeconds * time.Second
	}

	result := &RunTradeFleetCoordinatorResponse{Errors: []string{}}
	logger.Log("INFO", fmt.Sprintf("Trade fleet coordinator starting (tick %s, cooldown %s, backoff_max %s, max_concurrent %s, enabled %t)",
		tick, cmd.cooldownDuration(), cmd.relaunchBackoffMaxDuration(), maxConcurrentLabel(cmd.MaxConcurrentTours), cmd.Enabled), map[string]interface{}{
		"action":           "trade_fleet_coordinator_start",
		"container_id":     cmd.ContainerID,
		"enabled":          cmd.Enabled,
		"cooldown_secs":    int(cmd.cooldownDuration().Seconds()),
		"backoff_max_secs": int(cmd.relaunchBackoffMaxDuration().Seconds()),
		"max_concurrent":   cmd.MaxConcurrentTours,
	})

	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		launched, err := h.reconcileOnce(ctx, cmd)
		result.Launched += launched
		result.Ticks++
		if err != nil {
			result.Errors = append(result.Errors, err.Error())
			logger.Log("ERROR", fmt.Sprintf("Trade fleet reconcile failed: %v", err), nil)
		}

		select {
		case <-time.After(tick):
		case <-ctx.Done():
			return result, ctx.Err()
		}
	}
}

// reconcileOnce is one reconcile pass over the trade fleet. It is the unit the tests
// drive directly (the Handle loop just calls it on a timer).
//
// It snapshots the whole fleet once, partitions the 'trade'-dedicated hulls into idle
// relaunch candidates and currently-running tours (partitionTradeFleet), and for each
// idle candidate past its cooldown launches a fresh continuous tour — up to
// max_concurrent. It returns the number of tours launched this pass. A per-hull launch
// failure is logged and skipped (the rest of the fleet still gets serviced, RULINGS
// #1); only a fleet-listing failure aborts the pass.
func (h *RunTradeFleetCoordinatorHandler) reconcileOnce(ctx context.Context, cmd *RunTradeFleetCoordinatorCommand) (int, error) {
	logger := common.LoggerFromContext(ctx)

	// Config off-switch (RULINGS #5): inert when disabled, so the container can stay
	// resident and be re-armed by a config flip + restart with no manual relaunch.
	if !cmd.Enabled {
		return 0, nil
	}

	// Fail closed, don't panic, if the launcher was never wired: a reconcile that
	// cannot launch must not silently read as "nothing to do".
	if h.launcher == nil {
		return 0, fmt.Errorf("trade fleet coordinator: no tour launcher wired (call SetTourLauncher at startup)")
	}

	ships, err := h.shipRepo.FindAllByPlayer(ctx, cmd.PlayerID)
	if err != nil {
		return 0, fmt.Errorf("failed to list ships for trade fleet reconcile: %w", err)
	}

	idle, runningTours := partitionTradeFleet(ships)
	if len(idle) == 0 {
		return 0, nil
	}

	// Deterministic relaunch order so a max_concurrent cap picks the same hulls every
	// tick and the tests are stable.
	sort.Slice(idle, func(i, j int) bool { return idle[i].ShipSymbol() < idle[j].ShipSymbol() })

	baseCooldown := cmd.cooldownDuration()
	backoffMax := cmd.relaunchBackoffMaxDuration()
	maxConcurrent := cmd.MaxConcurrentTours
	now := h.clock.Now()
	launched := 0

	for _, ship := range idle {
		if maxConcurrent > 0 && runningTours >= maxConcurrent {
			logger.Log("INFO", fmt.Sprintf(
				"Trade fleet at max concurrent tours (%d) — holding %d idle hull(s) this tick",
				maxConcurrent, len(idle)-launched), map[string]interface{}{
				"action":         "trade_fleet_max_concurrent",
				"max_concurrent": maxConcurrent,
				"running_tours":  runningTours,
			})
			break
		}

		// cooldown is BASE unless sp-1pli's adaptive backoff has escalated this
		// specific hull past a run of unproductive exits — see cooldownFor.
		cooldown := h.cooldownFor(ship, baseCooldown, backoffMax, logger)

		if remaining := cooldownRemaining(ship, now, cooldown); remaining > 0 {
			logger.Log("INFO", fmt.Sprintf(
				"Trade hull %s parked %s ago — cooling down %s more before relaunch (letting the ground breathe)",
				ship.ShipSymbol(), (cooldown-remaining).Truncate(time.Second), remaining.Truncate(time.Second)), map[string]interface{}{
				"action":            "trade_fleet_cooldown_hold",
				"ship_symbol":       ship.ShipSymbol(),
				"cooldown_secs":     int(cooldown.Seconds()),
				"remaining_secs":    int(remaining.Seconds()),
				"prior_exit_reason": priorExitReason(ship),
			})
			continue
		}

		spec := TourLaunchSpec{
			ShipSymbol:                       ship.ShipSymbol(),
			MaxHops:                          cmd.MaxHops,
			MaxSpend:                         cmd.MaxSpend,
			MinMargin:                        cmd.MinMargin,
			ReplanLimit:                      cmd.ReplanLimit,
			WorkingCapitalReserve:            cmd.WorkingCapitalReserve,
			WorkingCapitalReserveTreasuryPct: cmd.WorkingCapitalReserveTreasuryPct,
			AgentSymbol:                      cmd.AgentSymbol,
			Iterations:                       tourIterationsContinuous,
			PlayerID:                         cmd.PlayerID.Value(),
		}
		containerID, lerr := h.launcher.LaunchTour(ctx, spec)
		if lerr != nil {
			// A single hull's launch failure (e.g. it was claimed between the snapshot
			// and the launch, or the daemon refused it) must not abort the pass — the
			// rest of the fleet still gets serviced, and this hull retries next tick.
			logger.Log("WARNING", fmt.Sprintf("Failed to relaunch tour for trade hull %s: %v", ship.ShipSymbol(), lerr), map[string]interface{}{
				"action":      "trade_fleet_relaunch_failed",
				"ship_symbol": ship.ShipSymbol(),
			})
			continue
		}

		runningTours++
		launched++
		logger.Log("INFO", fmt.Sprintf(
			"Relaunched continuous tour for trade hull %s (prior exit: %s, cooldown %s, container %s)",
			ship.ShipSymbol(), priorExitReasonLabel(ship), cooldown.Truncate(time.Second), containerID), map[string]interface{}{
			"action":            "trade_fleet_relaunch",
			"ship_symbol":       ship.ShipSymbol(),
			"container_id":      containerID,
			"prior_exit_reason": priorExitReason(ship),
			"cooldown_secs":     int(cooldown.Seconds()),
		})
	}

	return launched, nil
}

// partitionTradeFleet splits a fleet snapshot into the 'trade'-dedicated hulls that
// are parked and eligible for relaunch (idle, not in transit) and a count of those
// currently running a tour — the reconcile predicate (sp-1278), the analog of the
// scout reconciler detecting unmanned slots.
//
// The two captain off-switches are honored here for free, with no extra guard:
//   - Unpinned: a hull whose DedicatedFleet() != "trade" is simply not ours — skipped.
//   - Captain-reserved: a captain reservation is an ACTIVE assignment (owner=captain),
//     so it is neither idle nor a container-owned tour; IsReservedByCaptain() drops it
//     before either bucket, so a reserved hull is never relaunched AND never counted
//     against the concurrency cap.
//
// A hull mid-tour (a live container claim) is counted as a running tour and left
// untouched. An in-transit idle hull (a rare gap between release and the next claim
// while still flying) is neither relaunched this tick nor counted — StartTourRun would
// refuse a non-idle hull anyway, and counting it would distort the cap.
func partitionTradeFleet(ships []*navigation.Ship) (idle []*navigation.Ship, runningTours int) {
	for _, ship := range ships {
		if ship.DedicatedFleet() != tradeFleet {
			continue // not a trade hull (or unpinned — the captain's per-hull opt-out)
		}
		if ship.IsReservedByCaptain() {
			continue // captain reserved: respect it, never relaunch and never count it
		}
		if ship.IsAssigned() {
			runningTours++ // live container claim => a tour is running; leave it be
			continue
		}
		if ship.IsInTransit() {
			continue // idle but still flying — not a launch candidate this tick
		}
		idle = append(idle, ship) // parked by an honest tour exit: relaunch candidate
	}
	return idle, runningTours
}

// cooldownRemaining returns how much of the per-hull cooldown is still pending for an
// idle trade hull (sp-1278), or 0 when it is clear to relaunch. The cooldown is
// measured from the hull's last release time — the ContainerRunner stamps released_at
// when a tour terminates (ForceRelease -> assignment.Released), and a trade hull is
// only ever claimed by tour_run, so its last release IS its last tour's honest-exit
// time. That timestamp is persisted on the ship row, so the cooldown is respected
// across coordinator restarts with zero new state (RULINGS #2). A hull that has never
// run a tour (no release time) is clear immediately.
func cooldownRemaining(ship *navigation.Ship, now time.Time, cooldown time.Duration) time.Duration {
	if cooldown <= 0 {
		return 0
	}
	assignment := ship.Assignment()
	if assignment == nil || assignment.ReleasedAt() == nil {
		return 0 // never toured (or no terminal recorded) — nothing to cool down from
	}
	elapsed := now.Sub(*assignment.ReleasedAt())
	if elapsed >= cooldown {
		return 0
	}
	return cooldown - elapsed
}

// hullBackoff is the adaptive per-hull relaunch-cooldown state sp-1pli tracks in
// memory (RunTradeFleetCoordinatorHandler.backoff). cooldown starts at the base and
// only ever changes through cooldownFor: doubled (clamped to the configured max) on a
// freshly-scored unproductive exit, reset to base on a freshly-scored productive one.
// scoredRelease is the release timestamp already folded into cooldown/
// consecutiveUnproductive — it guards against rescoring the SAME parked exit on every
// subsequent reconcile tick while the hull just sits out its cooldown, which would
// otherwise runaway-escalate a single unproductive tour to the max within a few ticks
// instead of once per real tour cycle ("no per-tick spam", per the bead).
type hullBackoff struct {
	consecutiveUnproductive int
	cooldown                time.Duration
	scoredRelease           time.Time
}

// cooldownFor resolves the relaunch cooldown to apply to one idle hull this pass
// (sp-1pli). A hull that has never toured (no release recorded) is unscored and uses
// base, exactly like cooldownRemaining's own nil-check — never-toured hulls have
// nothing to be adaptive about.
//
// Otherwise the hull's last release is scored AT MOST ONCE (guarded by
// scoredRelease): a tour that ran for at least minProductiveTourDuration before
// exiting is productive and resets the hull straight back to base; a shorter exit is
// an unproductive fast-fail and DOUBLES the hull's cooldown, clamped to max. Every
// escalation (never a reset) logs one INFO line — the bead's explicit ask, and the
// only log this method emits, so an idle hull merely waiting out an already-scored
// cooldown across many ticks stays silent.
func (h *RunTradeFleetCoordinatorHandler) cooldownFor(ship *navigation.Ship, base, max time.Duration, logger common.ContainerLogger) time.Duration {
	assignment := ship.Assignment()
	if assignment == nil || assignment.ReleasedAt() == nil {
		return base // never toured — nothing to score
	}
	releasedAt := *assignment.ReleasedAt()

	bo := h.backoff[ship.ShipSymbol()]
	if bo == nil {
		bo = &hullBackoff{cooldown: base}
		h.backoff[ship.ShipSymbol()] = bo
	}

	if !releasedAt.After(bo.scoredRelease) {
		return bo.cooldown // this exit was already scored on a prior tick
	}
	bo.scoredRelease = releasedAt

	if releasedAt.Sub(assignment.AssignedAt()) >= minProductiveTourDuration {
		bo.consecutiveUnproductive = 0
		bo.cooldown = base
		return bo.cooldown
	}

	bo.consecutiveUnproductive++
	bo.cooldown *= 2
	if bo.cooldown > max {
		bo.cooldown = max
	}
	logger.Log("INFO", fmt.Sprintf(
		"Trade hull %s cooldown escalating to %s after %d consecutive unproductive exit(s) — fleet-wide infeasibility backoff",
		ship.ShipSymbol(), bo.cooldown.Truncate(time.Second), bo.consecutiveUnproductive), map[string]interface{}{
		"action":                   "trade_fleet_backoff_escalate",
		"ship_symbol":              ship.ShipSymbol(),
		"new_cooldown_secs":        int(bo.cooldown.Seconds()),
		"consecutive_unproductive": bo.consecutiveUnproductive,
	})
	return bo.cooldown
}

// priorExitReason returns the release reason stamped on the hull when its last tour
// terminated, or "" if none — read-only (the coordinator never rewrites it, so
// honest-exit telemetry is untouched).
func priorExitReason(ship *navigation.Ship) string {
	assignment := ship.Assignment()
	if assignment == nil || assignment.ReleaseReason() == nil {
		return ""
	}
	return *assignment.ReleaseReason()
}

// priorExitReasonLabel is priorExitReason with a human placeholder for the empty case,
// for the relaunch log line.
func priorExitReasonLabel(ship *navigation.Ship) string {
	if reason := priorExitReason(ship); reason != "" {
		return reason
	}
	return "unknown"
}

// cooldownDuration resolves the command's cooldown, applying the default when unset.
func (c *RunTradeFleetCoordinatorCommand) cooldownDuration() time.Duration {
	secs := c.CooldownSecs
	if secs <= 0 {
		secs = defaultTradeFleetCooldownSeconds
	}
	return time.Duration(secs) * time.Second
}

// relaunchBackoffMaxDuration resolves the command's adaptive-backoff ceiling (sp-1pli),
// applying the default when unset.
func (c *RunTradeFleetCoordinatorCommand) relaunchBackoffMaxDuration() time.Duration {
	secs := c.RelaunchBackoffMaxSecs
	if secs <= 0 {
		secs = defaultRelaunchBackoffMaxSeconds
	}
	return time.Duration(secs) * time.Second
}

// maxConcurrentLabel renders the concurrency cap for the start log — "unlimited" for
// the <=0 (fleet-size-bounded) case.
func maxConcurrentLabel(max int) string {
	if max <= 0 {
		return "unlimited"
	}
	return fmt.Sprintf("%d", max)
}
