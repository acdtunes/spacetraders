package commands

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
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

	// defaultRelaunchBackoffMaxSeconds is the ceiling for the per-hull ADAPTIVE relaunch
	// backoff (sp-1pli) when the config leaves it unset. When a hull keeps fast-failing,
	// the coordinator doubles that hull's relaunch cooldown from the base up to this
	// ceiling, so a fleet-wide-infeasible market is not hammered with a discovery+solver
	// pass every base cooldown (862 tour-run log lines in 20 minutes prompted sp-1pli).
	//
	// sp-nxrt LOWERED this 1800->600 (30min->10min). The old 30min ceiling was only
	// needed because SLEEP was the sole response to a fast-fail — a hull in a thin/stale
	// pocket spiralled 6->12->24->30min parked (~238 hull-hours/day of pure parking). Now
	// the 2nd consecutive fast-fail escalates to MOVEMENT (reposition-reach, see
	// cooldownFor), so ever-longer sleep is no longer how a stuck hull is handled: the
	// remaining backoff exists only to rate-limit a GENUINELY map-wide-dead neighbourhood
	// the reach-armed relaunch also could not escape, for which 10min is ample. A named
	// config knob (RelaunchBackoffMaxSecs / [trade_fleet].relaunch_backoff_max_minutes,
	// RULINGS #5) — retune without a rebuild.
	defaultRelaunchBackoffMaxSeconds = 600

	// minProductiveTourDuration is the fast-fail line between an honest trade leg and
	// a tour that never really flew (sp-1pli). It is a hardcoded mechanism constant,
	// not a config knob (RULINGS #5 governs operational values; this is an internal
	// classification threshold) — deliberately conservative/asymmetric: biased toward
	// NOT escalating rather than wrongly punishing a hull that spent its one sp-zhii
	// rescue-reposition jump before ultimately starving, which can look identical to a
	// short real trade leg from duration alone. A missed escalation just costs one more
	// base-cooldown cycle before the next fast-fail exit catches it.
	minProductiveTourDuration = 90 * time.Second

	// defaultMassParkWindowSeconds and defaultMassParkMinHulls define the restart-induced
	// mass-park signature the sp-1pli backoff must NOT read as thin market depth (sp-nkci).
	// A daemon blip/restart force-parks the whole trade fleet in one narrow window; every
	// one of those short synchronized exits looks like an unproductive fast-fail, so the
	// backoff would double every hull's cooldown at once and idle the fleet in lockstep
	// (~12min observed). Organic thin-depth is the opposite shape — it parks ONE hull at a
	// time, when ITS own market dies (the lxwn rich->tapped->rich cycle), spread over
	// minutes. So a park is treated as a restart signature (and exempted from the backoff)
	// only when at least defaultMassParkMinHulls idle hulls released within
	// defaultMassParkWindowSeconds of each other. 120s comfortably spans a restart's
	// force-release sweep; 4 hulls is well above any organic 1-2-hull coincidence yet far
	// below the ~10-heavy fleet a blip parks at once. Both are config knobs (RULINGS #5).
	defaultMassParkWindowSeconds = 120
	defaultMassParkMinHulls      = 4
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

	// sp-nkci mass-park exemption knobs (RULINGS #5, live-by-default). A daemon
	// blip/restart force-parks the whole trade fleet in one window; sp-1pli must not
	// misread that synchronized park as fleet-wide thin depth and ramp every hull's
	// cooldown in lockstep. See detectMassPark / cooldownFor.
	//
	// MassParkExemptDisabled is the kill switch — false (default) leaves the exemption ON.
	MassParkExemptDisabled bool
	// MassParkWindowSecs is the co-park window that marks a synchronized restart park;
	// <=0 uses defaultMassParkWindowSeconds.
	MassParkWindowSecs int
	// MassParkMinHulls is how many idle hulls must have released within the window to call
	// it a mass-park; <=0 uses defaultMassParkMinHulls.
	MassParkMinHulls int
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

	// captainEvents emits the coordinator error-loop event (sp-e2l1, rollout sp-6wxq)
	// when a reconcile pass fails with the identical error for DefaultStreakThreshold
	// consecutive ticks — the s88 silent-stuck class (a launcher never wired, or the
	// fleet listing failing forever) becomes an interrupt-visible captain event instead
	// of ERROR lines nothing outside the container reads. Optional-injection via
	// SetEventRecorder, nil-safe like the contract coordinator's captainEvents.
	captainEvents captain.EventRecorder
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

// SetEventRecorder wires the captain outbox the coordinator emits its
// error-loop event through (sp-6wxq). Optional-injection like SetTourLauncher:
// without it the streak monitor still tracks and logs, it just cannot escalate
// to a captain event (nil-safe, see health.RecordErrorLoop).
func (h *RunTradeFleetCoordinatorHandler) SetEventRecorder(rec captain.EventRecorder) {
	h.captainEvents = rec
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

	// errMon makes a reconcile pass that fails with the identical error every tick
	// observable (sp-e2l1): once the streak crosses DefaultStreakThreshold it emits a
	// captain event instead of just another ERROR line. Created once per Handle
	// invocation (one container run) and threaded into reconcileOnce, so the streak
	// persists across ticks and the crossing is unit-testable at the reconcile seam.
	errMon := health.NewMonitor(health.DefaultStreakThreshold)

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
		// Streak-track the pass outcome (sp-6wxq): a non-error pass resets the streak,
		// a pass that fails with the identical error every tick (launcher unwired, or
		// the fleet listing failing) crosses the threshold and emits the error-loop
		// captain event instead of only logging ERROR forever. Placed here rather than
		// inside reconcileOnce so its signature — the unit the tests drive — is
		// unchanged; the per-hull launch-failure "candidates>0 but 0 launched" case is
		// deliberately not tracked (0-launched is ambiguous with all-cooling-down).
		h.noteReconcile(ctx, cmd, errMon, err)

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

	// sp-nkci: a daemon blip parks the whole fleet in one window; those synchronized parks
	// are a restart signature, not thin depth, so exempt them from the sp-1pli backoff
	// (below) rather than ramp every hull in lockstep. Live by default (RULINGS #5); the
	// captain can disable it or retune the window/threshold via config.
	var massParkExempt map[string]bool
	if !cmd.MassParkExemptDisabled {
		massParkExempt = detectMassPark(idle, cmd.massParkWindow(), cmd.massParkMinHulls())
	}

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

		// cooldown is BASE unless sp-1pli's adaptive backoff has escalated this specific
		// hull past a run of unproductive exits — see cooldownFor. reachEscalated is set
		// (sp-nxrt) once the hull hit its 2nd consecutive fast-fail: the relaunch is armed
		// to reposition-reach to a fresh system instead of the coordinator sleeping longer
		// on a dead lane. A hull whose park is part of a restart-induced mass-park (sp-nkci)
		// is exempt from BOTH the sleep ramp and the movement escalation.
		cooldown, reachEscalated := h.cooldownFor(ship, baseCooldown, backoffMax, massParkExempt[ship.ShipSymbol()], logger)

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
			RepositionReachEscalated:         reachEscalated,
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
	}

	return launched, nil
}

// noteReconcile records one reconcile pass at the "reconcile" streak checkpoint
// (sp-6wxq): a nil err is a success that resets the streak; a non-nil err that
// repeats identically for DefaultStreakThreshold consecutive passes crosses and
// emits the coordinator error-loop captain event. Edge-triggered — only the exact
// crossing pass emits — and nil-safe on the recorder via health.RecordErrorLoop.
func (h *RunTradeFleetCoordinatorHandler) noteReconcile(ctx context.Context, cmd *RunTradeFleetCoordinatorCommand, errMon *health.Monitor, err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if streak, crossed := errMon.Note("reconcile", msg); crossed {
		health.RecordErrorLoop(h.captainEvents, common.LoggerFromContext(ctx), cmd.ContainerID, cmd.PlayerID.Value(), "reconcile", err, streak)
	}
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

// detectMassPark returns the set of idle hull symbols whose park is part of a
// restart-induced mass-park (sp-nkci): at least minHulls idle hulls released within
// `window` of each other. A daemon blip/restart force-parks the whole trade fleet in one
// narrow window, and that synchronized park must NOT be fed to the sp-1pli thin-depth
// backoff — organic thin-depth parks a hull at a time (when ITS market dies), so a
// tight cluster of many simultaneous parks is a restart signature, not a depth signal.
// An empty set (no cluster, or fewer than minHulls idle hulls) means nothing is exempt,
// so the backoff behaves exactly as before for the spread-out single-hull case.
func detectMassPark(idle []*navigation.Ship, window time.Duration, minHulls int) map[string]bool {
	exempt := make(map[string]bool)
	if minHulls <= 0 || len(idle) < minHulls {
		return exempt
	}

	// Only hulls with a real release anchor can be part of a park cluster (a never-toured
	// hull has no releasedAt and is not adaptive anyway — cooldownFor short-circuits it).
	type park struct {
		symbol string
		at     time.Time
	}
	parks := make([]park, 0, len(idle))
	for _, ship := range idle {
		assignment := ship.Assignment()
		if assignment == nil || assignment.ReleasedAt() == nil {
			continue
		}
		parks = append(parks, park{symbol: ship.ShipSymbol(), at: *assignment.ReleasedAt()})
	}
	if len(parks) < minHulls {
		return exempt
	}

	// A hull is in a mass-park when at least minHulls parks (including itself) fall within
	// `window` of its own release. O(n^2) over the fleet's idle hulls (tens) — trivial.
	for i := range parks {
		coincident := 0
		for j := range parks {
			if absDuration(parks[i].at.Sub(parks[j].at)) <= window {
				coincident++
			}
		}
		if coincident >= minHulls {
			exempt[parks[i].symbol] = true
		}
	}
	return exempt
}

// absDuration returns the absolute value of a duration.
func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
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
	// reachEscalated is set once a hull hits its 2nd consecutive fast-fail (sp-nxrt part a):
	// the relaunch is armed with reposition-reach (the broadened 2-4-gate-hop discovery)
	// so the hull MOVES to a fresh system instead of the coordinator sleeping ever longer
	// on a lane that is gone from HERE. It stays armed while the coordinator backs off a
	// map-wide-dead neighbourhood (streak >= 3) and is cleared only by a productive tour —
	// a recovered hull relaunches normally. reconcileOnce copies it onto the launch spec.
	reachEscalated bool
}

// cooldownFor resolves the relaunch cooldown to apply to one idle hull this pass AND
// whether that relaunch should be reach-escalated (sp-1pli + sp-nxrt). A hull that has
// never toured (no release recorded) is unscored, uses base, and is not escalated —
// exactly like cooldownRemaining's own nil-check.
//
// Otherwise the hull's last release is scored AT MOST ONCE (guarded by scoredRelease):
// a tour that ran for at least minProductiveTourDuration is productive and resets the
// hull straight back to base (and disarms any reach escalation); a shorter exit is an
// unproductive fast-fail and drives the escalation ladder below. Every escalation logs
// one INFO line (never a reset), so an idle hull merely waiting out an already-scored
// cooldown across many ticks stays silent.
//
// The fast-fail ladder (sp-nxrt part a) — the fix for ~238 hull-hours/day of pure
// parking, where SLEEP was the sole response and a hull spiralled 6->12->24->30min:
//
//	1st fast-fail   -> DOUBLE the sleep (base -> 2*base). The market HERE may just be
//	                   thin (the lxwn rich->tapped->rich cycle), so wait one cycle in
//	                   place — cheaper than moving. Reach stays off.
//	2nd consecutive -> ESCALATE TO MOVEMENT. Waiting-in-place did not help: the lane is
//	                   gone from HERE. Arm reposition-reach on the relaunch and drop the
//	                   sleep back to the base breather so the hull MOVES promptly instead
//	                   of a longer sleep. This is the biggest single tempo lever.
//	3rd+ consecutive-> Even the reach-armed relaunch (broadened to 2-4 gate hops) found
//	                   no ground worth the jump — genuine map-wide margin exhaustion.
//	                   RESUME the bounded sleep backoff (do not hammer a dead map every
//	                   base cooldown) while KEEPING reach armed for the instant a ground
//	                   reopens.
func (h *RunTradeFleetCoordinatorHandler) cooldownFor(ship *navigation.Ship, base, max time.Duration, massParkExempt bool, logger common.ContainerLogger) (time.Duration, bool) {
	assignment := ship.Assignment()
	if assignment == nil || assignment.ReleasedAt() == nil {
		return base, false // never toured — nothing to score
	}
	releasedAt := *assignment.ReleasedAt()

	bo := h.backoff[ship.ShipSymbol()]
	if bo == nil {
		bo = &hullBackoff{cooldown: base}
		h.backoff[ship.ShipSymbol()] = bo
	}

	if !releasedAt.After(bo.scoredRelease) {
		return bo.cooldown, bo.reachEscalated // this exit was already scored on a prior tick
	}
	bo.scoredRelease = releasedAt

	// sp-nkci: a restart-induced mass-park (many hulls force-parked in one window) is not
	// a thin-depth signal — do NOT feed it to the adaptive backoff, and (sp-nxrt) do NOT
	// let it trigger the movement escalation: repositioning the whole fleet off a daemon
	// blip would be a mass reposition-churn event. Mark the release scored (so the same
	// park is never re-scored on a later tick as hulls relaunch and the cluster dissipates)
	// but leave the hull's cooldown, streak, AND reach flag untouched: a synchronized park
	// says nothing about market depth, so it neither escalates nor resets. One INFO line
	// (guarded by scoredRelease, so once per park) records why the fleet did not ramp.
	if massParkExempt {
		logger.Log("INFO", fmt.Sprintf(
			"Trade hull %s parked in a fleet-wide mass-park window — exempt from sp-1pli adaptive backoff (sp-nkci), cooldown held at %s",
			ship.ShipSymbol(), bo.cooldown.Truncate(time.Second)), map[string]interface{}{
			"action":        "trade_fleet_masspark_exempt",
			"ship_symbol":   ship.ShipSymbol(),
			"cooldown_secs": int(bo.cooldown.Seconds()),
		})
		return bo.cooldown, bo.reachEscalated
	}

	if releasedAt.Sub(assignment.AssignedAt()) >= minProductiveTourDuration {
		// Productive: a fresh ground was found and traded. Reset to base and disarm reach —
		// the recovered hull relaunches normally, not force-armed forever.
		bo.consecutiveUnproductive = 0
		bo.cooldown = base
		bo.reachEscalated = false
		return bo.cooldown, false
	}

	bo.consecutiveUnproductive++
	switch {
	case bo.consecutiveUnproductive == 1:
		// 1st fast-fail: wait ONE lengthened cycle in place (the market may just be thin).
		bo.cooldown = clampDuration(base*2, max)
		logger.Log("INFO", fmt.Sprintf(
			"Trade hull %s cooldown escalating to %s after %d consecutive unproductive exit(s) — fleet-wide infeasibility backoff",
			ship.ShipSymbol(), bo.cooldown.Truncate(time.Second), bo.consecutiveUnproductive), map[string]interface{}{
			"action":                   "trade_fleet_backoff_escalate",
			"ship_symbol":              ship.ShipSymbol(),
			"new_cooldown_secs":        int(bo.cooldown.Seconds()),
			"consecutive_unproductive": bo.consecutiveUnproductive,
		})
	case bo.consecutiveUnproductive == 2:
		// 2nd consecutive fast-fail: the lane is gone from HERE — MOVE instead of sleeping
		// longer. Arm reposition-reach and relaunch at the base breather (sp-nxrt part a).
		bo.reachEscalated = true
		bo.cooldown = base
		logger.Log("INFO", fmt.Sprintf(
			"Trade hull %s escalating to MOVEMENT after %d consecutive unproductive exit(s) — arming reposition-reach and relaunching at the %s base breather instead of a longer sleep (sp-nxrt)",
			ship.ShipSymbol(), bo.consecutiveUnproductive, bo.cooldown.Truncate(time.Second)), map[string]interface{}{
			"action":                   "trade_fleet_movement_escalate",
			"ship_symbol":              ship.ShipSymbol(),
			"new_cooldown_secs":        int(bo.cooldown.Seconds()),
			"consecutive_unproductive": bo.consecutiveUnproductive,
			"reposition_reach_armed":   true,
		})
	default:
		// 3rd+ consecutive fast-fail: the reach-armed relaunch could not escape either —
		// genuine map-wide exhaustion. Resume the bounded sleep backoff, keep reach armed.
		bo.cooldown = clampDuration(bo.cooldown*2, max)
		logger.Log("INFO", fmt.Sprintf(
			"Trade hull %s cooldown escalating to %s after %d consecutive unproductive exit(s) — reposition-reach did not rescue it, backing off (bounded, reposition-reach stays armed)",
			ship.ShipSymbol(), bo.cooldown.Truncate(time.Second), bo.consecutiveUnproductive), map[string]interface{}{
			"action":                   "trade_fleet_backoff_escalate",
			"ship_symbol":              ship.ShipSymbol(),
			"new_cooldown_secs":        int(bo.cooldown.Seconds()),
			"consecutive_unproductive": bo.consecutiveUnproductive,
			"reposition_reach_armed":   true,
		})
	}
	return bo.cooldown, bo.reachEscalated
}

// clampDuration caps d at max (the per-hull backoff ceiling, RULINGS #5). A tiny helper so
// the ladder's two escalation arms clamp identically.
func clampDuration(d, max time.Duration) time.Duration {
	if d > max {
		return max
	}
	return d
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

// massParkWindow resolves the mass-park co-park window (sp-nkci), applying the default
// when unset.
func (c *RunTradeFleetCoordinatorCommand) massParkWindow() time.Duration {
	secs := c.MassParkWindowSecs
	if secs <= 0 {
		secs = defaultMassParkWindowSeconds
	}
	return time.Duration(secs) * time.Second
}

// massParkMinHulls resolves the mass-park hull threshold (sp-nkci), applying the default
// when unset.
func (c *RunTradeFleetCoordinatorCommand) massParkMinHulls() int {
	if c.MassParkMinHulls <= 0 {
		return defaultMassParkMinHulls
	}
	return c.MassParkMinHulls
}

// maxConcurrentLabel renders the concurrency cap for the start log — "unlimited" for
// the <=0 (fleet-size-bounded) case.
func maxConcurrentLabel(max int) string {
	if max <= 0 {
		return "unlimited"
	}
	return fmt.Sprintf("%d", max)
}
