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

	// tourIterationsContinuous makes every relaunched tour a CONTINUOUS run:
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
	// A ceiling this LOW is safe only because sleep is not the sole response to a fast-fail.
	// Sleep-only needs a far higher one and pays for it: under a 30min ceiling a hull in a
	// thin/stale pocket spirals 6->12->24->30min parked (~238 hull-hours/day of pure
	// parking). Here the 2nd consecutive fast-fail escalates to MOVEMENT (reposition-reach,
	// see cooldownFor), so ever-longer sleep is not how a stuck hull is handled: the
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

	// defaultWatchdogStallSeconds is the sp-m3122 liveness-watchdog stall threshold: how
	// long a RUNNING tour may make ZERO real progress (no plan/navigate/arrive/buy/sell)
	// before it is declared HUNG, killed, and relaunched fresh. The watchdog is ALWAYS ON
	// (no arm-seam) — this is the one tunable, an operational value (RULINGS #5), not a
	// feature flag. 12 min sits in the bead's 10-15min band: far above any single legit
	// silent leg (a jump cooldown, a market/plan micro-wait — the longest legs, multi-hop
	// travel, are IN_TRANSIT and skipped outright), far below the multi-hour strandings the
	// watchdog exists to end. Keying on PROGRESS (not wall-clock) is what makes a slow-but-
	// healthy tour safe: a flying hull is progressing, and a docked one advances its progress
	// signal on every real step. A config knob (WatchdogStallSecs /
	// trade_fleet.watchdog_stall_seconds) — retune without a rebuild.
	defaultWatchdogStallSeconds = 720

	// defaultFullHullPausePct is the sp-tgll8 inventory-pressure governor threshold: the
	// percentage of trade-fleet hulls sitting FULL (cargo at capacity, unable to offload)
	// above which the coordinator PAUSES relaunching EMPTY idle hulls into NEW buying tours
	// this tick — governing buy-rate by sell-rate so a fleet drowning in unsold inventory
	// stops buying more it cannot sell. 65% is conservative: a healthy fleet (hulls buy,
	// carry, sell — rarely more than a fraction FULL at once) is nowhere near it, so the
	// governor is byte-identical below saturation. LADEN idle hulls ALWAYS relaunch (they can
	// sell), so the fleet drains and un-throttles — no wedge (RULINGS #4). A config knob
	// (FullHullPausePct / [trade_fleet].full_hull_pause_pct, RULINGS #5): ships ARMED (no
	// on/off flag), softened by RAISING toward 100 (unreachable ⇒ effectively off).
	defaultFullHullPausePct = 65
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
	// replan_limit->2, working_capital_reserve->the non-tunable floor, sp-05glh flat). Sourced
	// live from config.yaml's [trade_fleet] section on every build so an edit+restart
	// retunes a recovered coordinator (sp-ts82 live-config pattern).
	MaxHops               int
	MaxSpend              int64
	MinMargin             int
	ReplanLimit           int
	WorkingCapitalReserve int64

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

	// WatchdogStallSecs is the sp-m3122 liveness-watchdog stall threshold — how long a
	// RUNNING tour may make ZERO real progress before it is declared HUNG, killed, and
	// relaunched fresh; <=0 uses defaultWatchdogStallSeconds (12 min). The watchdog itself
	// has NO on/off flag (it ships ARMED): a captain who ever needs to soften it raises this
	// threshold. RULINGS #5 (operational value, not an arm-seam).
	WatchdogStallSecs int

	// FullHullPausePct is the sp-tgll8 inventory-pressure governor threshold — the percentage
	// of trade-fleet hulls sitting FULL above which the coordinator pauses relaunching EMPTY
	// idle hulls into new buying tours this tick (LADEN idle hulls always relaunch to drain);
	// <=0 uses defaultFullHullPausePct (65%). Ships ARMED (no on/off flag, RULINGS #5); a
	// captain softens it by raising the threshold toward 100 (unreachable ⇒ effectively off).
	FullHullPausePct int
}

// RunTradeFleetCoordinatorResponse reports reconcile progress. Because the loop is
// infinite it is only observed on context cancellation (shutdown).
type RunTradeFleetCoordinatorResponse struct {
	Ticks    int
	Launched int
	Errors   []string
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

	// captainEvents emits the coordinator error-loop event (rollout sp-6wxq)
	// when a reconcile pass fails with the identical error for DefaultStreakThreshold
	// consecutive ticks — the s88 silent-stuck class (a launcher never wired, or the
	// fleet listing failing forever) becomes an interrupt-visible captain event instead
	// of ERROR lines nothing outside the container reads. Optional-injection via
	// SetEventRecorder, nil-safe like the contract coordinator's captainEvents.
	captainEvents captain.EventRecorder

	// tourLiveness / tourStopper are the sp-m3122 liveness watchdog's two ports: read each
	// running tour's last real-progress time, and kill a tour that has stalled past the
	// threshold. BOTH must be wired for the watchdog to act (fail-closed): without them a
	// reconcile pass detects nothing and kills nothing, byte-identical to before.
	tourLiveness TourLivenessPort
	tourStopper  TourStopper

	// absorptionReclaimer promptly releases absorption reservations of dead containers
	// (sp-m3122 part 3). Optional; nil skips the reclaim.
	absorptionReclaimer DeadContainerAbsorptionReclaimer

	// startupReclaimDone gates the one-shot restart absorption reclaim to the FIRST reconcile
	// pass of this handler (a fresh handler per daemon process — so it re-runs on every daemon
	// restart, the case that strands phantom reservations). After that, the reclaim only runs
	// when the watchdog actually killed a tour this pass.
	startupReclaimDone bool
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
	// observable: once the streak crosses DefaultStreakThreshold it emits a
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
		// Streak-track the pass outcome: a non-error pass resets the streak,
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

	now := h.clock.Now()

	idle, running := partitionTradeFleet(ships)

	// Liveness watchdog: a RUNNING claim is no longer trusted as healthy. Kill and
	// relaunch fresh any running tour that has made ZERO real progress past the stall
	// threshold (a hung mid-jump restart-resume, or any other silent wedge). Runs BEFORE the
	// empty-idle early return — a fleet whose ONLY problem is a hung RUNNING tour has an empty
	// idle bucket, yet that tour is exactly what must be killed. Because the progress signal is
	// persistent, a daemon-restart hang is detected on the FIRST pass (its last progress
	// predates the restart), not one stall-threshold later.
	watchdogKilled, watchdogRelaunched := h.relaunchHungTours(ctx, cmd, running, now, logger)

	// sp-m3122 part 3: promptly release absorption reservations held by containers that no
	// longer exist — on restart (this handler's first pass) and after any watchdog kill (the
	// just-killed container's holds) — rather than waiting for the TTL sweep, so phantom
	// reservations never make open sinks look contended.
	if !h.startupReclaimDone || watchdogKilled > 0 {
		h.reclaimDeadContainerAbsorption(ctx, cmd, logger)
	}
	h.startupReclaimDone = true

	if len(idle) == 0 {
		return watchdogRelaunched, nil
	}

	// Deterministic relaunch order so a max_concurrent cap picks the same hulls every
	// tick and the tests are stable.
	sort.Slice(idle, func(i, j int) bool { return idle[i].ShipSymbol() < idle[j].ShipSymbol() })

	baseCooldown := cmd.cooldownDuration()
	backoffMax := cmd.relaunchBackoffMaxDuration()
	maxConcurrent := cmd.MaxConcurrentTours
	// A watchdog kill+relaunch is a 1:1 replacement, so it nets zero against the cap; a kill
	// whose relaunch failed frees a slot. launched already counts the watchdog relaunches.
	launched := watchdogRelaunched
	runningTours := len(running) - watchdogKilled + watchdogRelaunched

	// sp-nkci: a daemon blip parks the whole fleet in one window; those synchronized parks
	// are a restart signature, not thin depth, so exempt them from the sp-1pli backoff
	// (below) rather than ramp every hull in lockstep. Live by default (RULINGS #5); the
	// captain can disable it or retune the window/threshold via config.
	var massParkExempt map[string]bool
	if !cmd.MassParkExemptDisabled {
		massParkExempt = detectMassPark(idle, cmd.massParkWindow(), cmd.massParkMinHulls())
	}

	pauseEmptyRelaunch, fullPct := inventoryPressurePause(idle, running, cmd, logger)

	for _, ship := range idle {
		if declineRetiredHull(ship, logger) {
			continue
		}
		// sp-tgll8: under fleet saturation, hold an EMPTY idle hull this tick rather than
		// start a NEW buying tour into a fleet drowning in unsold inventory. A LADEN idle hull
		// (cargo to sell) is never held — blocking a sell would wedge the fleet (RULINGS #4).
		if pauseEmptyRelaunch && ship.CargoUnits() == 0 {
			logger.Log("INFO", fmt.Sprintf(
				"Trade hull %s held this tick under inventory pressure (%d%% of the fleet is FULL) — not starting a new buying tour on an EMPTY hull while the fleet cannot offload",
				ship.ShipSymbol(), fullPct), map[string]interface{}{
				"action":      "trade_fleet_inventory_pressure_hold",
				"ship_symbol": ship.ShipSymbol(),
				"full_pct":    fullPct,
			})
			continue
		}
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

		if !reachEscalated && escalateReachAfterVeto(ship, logger) {
			reachEscalated = true
		}

		if holdForCooldown(ship, now, cooldown, logger) {
			continue
		}

		if !h.launchTourForHull(ctx, cmd, ship, cooldown, reachEscalated, logger) {
			continue
		}
		runningTours++
		launched++
	}

	return launched, nil
}

// inventoryPressurePause is the sp-tgll8 governor: govern buy-rate by sell-rate. When too
// much of the trade fleet is sitting FULL of cargo it cannot offload, PAUSE relaunching EMPTY
// idle hulls into NEW buying tours this tick — the fleet stops buying what it cannot sell.
// LADEN idle hulls are NEVER paused (they must always relaunch to sell, so the fleet drains
// and the governor un-throttles — no wedge, RULINGS #4). Conservative default (65% FULL) so a
// healthy fleet is byte-identical.
func inventoryPressurePause(idle, running []*navigation.Ship, cmd *RunTradeFleetCoordinatorCommand, logger common.ContainerLogger) (bool, int) {
	fullHulls, totalHulls := fullHullPressure(idle, running)
	pausePct := cmd.fullHullPausePct()
	if totalHulls == 0 || fullHulls*100 <= pausePct*totalHulls {
		return false, 0
	}
	fullPct := fullHulls * 100 / totalHulls
	logger.Log("INFO", fmt.Sprintf(
		"Trade fleet inventory pressure %d%% FULL (%d/%d) exceeds the %d%% pause threshold — pausing new buying tours on EMPTY idle hulls this tick (laden hulls still relaunch to sell)",
		fullPct, fullHulls, totalHulls, pausePct), map[string]interface{}{
		"action":      "trade_fleet_inventory_pressure_pause",
		"full_hulls":  fullHulls,
		"total_hulls": totalHulls,
		"full_pct":    fullPct,
		"pause_pct":   pausePct,
	})
	return true, fullPct
}

// declineRetiredHull is the retirement gate: an operator-marked hull is planned no further
// tours ONCE ITS HOLD IS EMPTY. While it still carries cargo it relaunches like any other
// laden hull, because that relaunch is how it sells — declining a laden hull would park it
// loaded for good, and a stranded laden hull is worse than a trading one (RULINGS #4).
//
// It touches no claim and no dedication, so a hull marked mid-tour is not even seen here:
// it is in `running`, its container keeps flying it, and the gate applies to the tour AFTER
// that one (RULINGS #3 — retirement never becomes a second writer).
func declineRetiredHull(ship *navigation.Ship, logger common.ContainerLogger) bool {
	if !ship.RetirementDrained() {
		return false
	}
	logger.Log("INFO", fmt.Sprintf(
		"Trade hull %s is retiring and its hold is empty — declining to plan it another tour; it is drained and ready to scrap",
		ship.ShipSymbol()), map[string]interface{}{
		"action":      "trade_fleet_retirement_declined",
		"ship_symbol": ship.ShipSymbol(),
	})
	return true
}

// escalateReachAfterVeto arms reposition-reach for a hull the honest-completion veto sent
// back — the definitive dead-ground case: its run did not merely trade badly, it ended unable
// to sell what it was holding where it stands. The tour's exit sweep can only sell in the
// hull's CURRENT system, so relaunching onto those same markets re-inherits the same unsold
// obligation and is refused again, burning a container per turn while the cargo never moves.
//
// Derived per pass from the hull's persisted release reason — no cross-tick state (RULINGS
// #2). It only widens where the hull may LOOK for a buyer; it relaxes no margin, spend or
// sell guard (RULINGS #4), and it does not touch the veto itself.
func escalateReachAfterVeto(ship *navigation.Ship, logger common.ContainerLogger) bool {
	if priorExitReason(ship) != common.ReleaseReasonHonestCompletionVeto {
		return false
	}
	logger.Log("INFO", fmt.Sprintf(
		"Trade hull %s came back from the honest-completion veto (cargo it could not sell where it stands) — relaunching with reposition-reach ARMED instead of onto the ground that just refused it",
		ship.ShipSymbol()), map[string]interface{}{
		"action":            "trade_fleet_stranded_reach_escalation",
		"ship_symbol":       ship.ShipSymbol(),
		"prior_exit_reason": priorExitReason(ship),
	})
	return true
}

func holdForCooldown(ship *navigation.Ship, now time.Time, cooldown time.Duration, logger common.ContainerLogger) bool {
	remaining := cooldownRemaining(ship, now, cooldown)
	if remaining <= 0 {
		return false
	}
	logger.Log("INFO", fmt.Sprintf(
		"Trade hull %s parked %s ago — cooling down %s more before relaunch (letting the ground breathe)",
		ship.ShipSymbol(), (cooldown-remaining).Truncate(time.Second), remaining.Truncate(time.Second)), map[string]interface{}{
		"action":            "trade_fleet_cooldown_hold",
		"ship_symbol":       ship.ShipSymbol(),
		"cooldown_secs":     int(cooldown.Seconds()),
		"remaining_secs":    int(remaining.Seconds()),
		"prior_exit_reason": priorExitReason(ship),
	})
	return true
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
