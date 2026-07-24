package commands

// run_longhaul_arb_coordinator.go — the standing LongHaulArbFleetCoordinator (sp-mepj).
//
// It is the FLEET MANAGER half of the long-haul arb engine, modeled exactly on the
// trade-fleet coordinator (run_trade_fleet_coordinator.go) so the sp-m3122 liveness
// watchdog + stateless recovery can be SHARED, not forked: an infinite reconcile loop
// inside one Handle() call, each pass a cheap read of the fleet that launches a per-hull
// long-haul worker for every idle long-haul-tagged hull. The worker half
// (RunLongHaulArbHandler) does the discovery/pricing/execution episode.
//
// ISOLATION (design §1): the engine touches ONLY hulls tagged dedicated_fleet="long-haul"
// (populated manually by the operator via `fleet add --operation long-haul --ship X`). It
// never sees contract, warehouse/stocker, or untagged trade hulls, and NEVER the command
// frigate (RULINGS #7) — the same DedicatedFleet() partition the trade coordinator uses,
// plus the shared domainContract.IsCommandHull exclusion.
//
// NO FEATURE FLAG (Admiral standing order): armed on deploy. There is no Enabled
// off-switch — the engine is naturally inert until the operator tags a hull long-haul
// (an empty long-haul fleet yields an empty idle bucket → zero launches), and the
// operator turns it off by untagging hulls. That IS the arm/disarm seam, in the operator's
// hands, not a config flag.

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const (
	// longHaulFleet is the Ship.DedicatedFleet() value this coordinator works. A hull
	// pinned here is claimed by its long-haul worker container under
	// operation="long-haul"; the coordinator itself claims NOTHING. Untagging a hull
	// (DedicatedFleet() != "long-haul") removes it from this coordinator's view for free
	// — the operator's no-restart, per-hull off-switch.
	longHaulFleet = "long-haul"

	// defaultLongHaulTickSeconds is the reconcile cadence when the launch config leaves
	// it unset. Mirrors the trade/scout-post coordinators' 30s default — a park is at
	// most one tick of idle before its next episode launches.
	defaultLongHaulTickSeconds = 30

	// longHaulIterationsContinuous makes every launched worker a CONTINUOUS run: the
	// worker discovers, prices, and flies out+backhaul episodes until it can find no
	// lane that clears the floor+guards, then exits and THIS coordinator relaunches it
	// next tick. Fixed, not configurable (mirrors tourIterationsContinuous): a finite
	// worker would exit after N episodes and park the hull, exactly the autonomy this
	// engine exists to provide.
	longHaulIterationsContinuous = -1
)

// LongHaulArbFleetCoordinatorCommand launches the standing long-haul arb coordinator for
// a player (sp-mepj). Like the trade-fleet coordinator it runs an infinite reconcile loop
// inside a single Handle() call; the container wraps that one loop (iterations=-1).
type LongHaulArbFleetCoordinatorCommand struct {
	PlayerID    shared.PlayerID
	ContainerID string
	// AgentSymbol is threaded through to each worker launch (the worker needs it to
	// resolve the agent's live treasury for the money envelope, Section 3).
	AgentSymbol string

	// TickIntervalSecs is the reconcile cadence; <=0 uses defaultLongHaulTickSeconds.
	TickIntervalSecs int

	// Money-envelope knobs (design §3), Admiral-authorized aggressive SIZING, live-tunable;
	// <=0 → the defaults. PerHaulCap caps a single out/backhaul buy (~1M) and is threaded to
	// each worker's per-buy envelope (the fail-closed reserve-floor fence). TotalExposureCap
	// (~2M) is still threaded to each worker for parity, but the coordinator NO LONGER
	// enforces it as a concurrent-haul ceiling (sp-bwjyn, Admiral uncap order): every idle
	// tagged hull launches each tick, and spend stays bounded per buy by the reserve-floor fence.
	PerHaulCap       int64
	TotalExposureCap int64

	// WatchdogStallSecs is the sp-m3122 liveness-watchdog stall threshold for long-haul
	// workers; <=0 uses defaultWatchdogStallSeconds (12 min, shared with the trade fleet). A
	// worker on a legitimately-long multi-hop leg is IN_TRANSIT (skipped by the watchdog), so
	// this only bounds a PARKED-and-silent worker.
	WatchdogStallSecs int
}

// watchdogStallThreshold resolves the long-haul watchdog stall threshold, defaulting to the
// same defaultWatchdogStallSeconds the trade fleet uses.
func (c *LongHaulArbFleetCoordinatorCommand) watchdogStallThreshold() time.Duration {
	if c.WatchdogStallSecs > 0 {
		return time.Duration(c.WatchdogStallSecs) * time.Second
	}
	return time.Duration(defaultWatchdogStallSeconds) * time.Second
}

// resolvedPerHaulCap returns the per-haul spend cap, defaulting the aggressive ~1M.
func (c *LongHaulArbFleetCoordinatorCommand) resolvedPerHaulCap() int64 {
	if c.PerHaulCap > 0 {
		return c.PerHaulCap
	}
	return defaultLongHaulPerHaulCap
}

// resolvedTotalExposureCap returns the total in-flight exposure cap, defaulting ~2M.
func (c *LongHaulArbFleetCoordinatorCommand) resolvedTotalExposureCap() int64 {
	if c.TotalExposureCap > 0 {
		return c.TotalExposureCap
	}
	return defaultLongHaulTotalExposureCap
}

// LongHaulArbFleetCoordinatorResponse reports reconcile progress. Because the loop is
// infinite it is only observed on context cancellation (shutdown).
type LongHaulArbFleetCoordinatorResponse struct {
	Ticks    int
	Launched int
	Errors   []string
}

// LongHaulLaunchSpec is the launch request the coordinator hands the daemon for one idle
// long-haul hull. It carries only decision inputs — the daemon owns claim, container
// persistence, and the operation="long-haul" stamp — so the coordinator stays a pure
// decision loop that claims nothing itself (RULINGS #3/#7). The money-envelope knobs
// (Section 3) are added in a later stage.
type LongHaulLaunchSpec struct {
	ShipSymbol  string
	AgentSymbol string
	PlayerID    int
	Iterations  int
	// PerHaulCap / TotalExposureCap thread the money envelope (design §3) to the worker so
	// its per-buy cushion fence + per-haul cap use the fleet-configured numbers.
	PerHaulCap       int64
	TotalExposureCap int64
}

// LongHaulLauncher starts one recovery-safe long-haul worker container for an idle
// long-haul hull and returns its container ID. Implemented by the daemon server, so the
// launch goes through the SAME path a manual worker start uses: the atomic
// operation="long-haul" ClaimShip, the single-writer container row, and release-on-death
// all apply, and the coordinator never touches ship state. Mirrors TourLauncher /
// IdleArbLauncher (a narrow port faked trivially in tests).
type LongHaulLauncher interface {
	LaunchLongHaul(ctx context.Context, spec LongHaulLaunchSpec) (containerID string, err error)
}

// LongHaulArbFleetCoordinatorHandler keeps a long-haul worker alive on every idle
// long-haul-tagged hull (sp-mepj). Deliberately the minimal fleet coordinator, exactly
// like the trade-fleet coordinator: it claims nothing (each worker claims its own hull),
// holds no per-hull relaunch state, and only READS the fleet.
type LongHaulArbFleetCoordinatorHandler struct {
	shipRepo navigation.ShipRepository
	clock    shared.Clock

	// launcher starts each worker through the daemon path. nil (a boot before DI
	// completes, or a test that never wires it) fails the reconcile pass honestly rather
	// than silently launching nothing; wired via SetLongHaulLauncher, mirroring the trade
	// coordinator's SetTourLauncher.
	launcher LongHaulLauncher

	// tourLiveness / tourStopper are the sp-m3122 watchdog's two ports — the SAME shared
	// engine + daemon implementations the trade-fleet coordinator uses (keyed on containerID,
	// fleet-agnostic). A long-haul hull mid-multi-hop for a long time is the exact sp-m3122
	// hang risk, so this reuse is load-bearing. BOTH must be wired for the watchdog to act
	// (fail-closed); without them a reconcile pass detects and kills nothing.
	tourLiveness TourLivenessPort
	tourStopper  TourStopper

	// absorptionReclaimer promptly releases absorption reservations held by dead long-haul
	// containers (sp-m3122 part 3) — reused verbatim (keyed on container liveness, not fleet).
	// Optional; nil skips the reclaim.
	absorptionReclaimer DeadContainerAbsorptionReclaimer

	// startupReclaimDone gates the one-shot restart absorption reclaim to this handler's FIRST
	// reconcile pass (a fresh handler per daemon process — so it re-runs on every restart, the
	// case that strands phantom reservations). After that, the reclaim only runs when the
	// watchdog actually killed a worker this pass.
	startupReclaimDone bool
}

// NewLongHaulArbFleetCoordinatorHandler wires the coordinator. clock defaults to the real
// clock when nil (production). The launcher is injected separately via SetLongHaulLauncher.
func NewLongHaulArbFleetCoordinatorHandler(shipRepo navigation.ShipRepository, clock shared.Clock) *LongHaulArbFleetCoordinatorHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &LongHaulArbFleetCoordinatorHandler{shipRepo: shipRepo, clock: clock}
}

// SetLongHaulLauncher wires the daemon-server launcher each worker spawns through.
// Optional-injection like the trade coordinator's SetTourLauncher: without it a reconcile
// pass is a fail-closed no-op (it cannot launch), never a panic.
func (h *LongHaulArbFleetCoordinatorHandler) SetLongHaulLauncher(launcher LongHaulLauncher) {
	h.launcher = launcher
}

// SetTourLiveness wires the sp-m3122 watchdog's progress signal (the SAME daemon port the
// trade coordinator uses). Optional-injection: without it (or without a stopper) the watchdog
// is inert (fail-closed).
func (h *LongHaulArbFleetCoordinatorHandler) SetTourLiveness(port TourLivenessPort) {
	h.tourLiveness = port
}

// SetTourStopper wires the sp-m3122 watchdog's remedy — the port that kills a hung container.
// Optional-injection: without it the watchdog can detect a hang but takes no action (fail-closed).
func (h *LongHaulArbFleetCoordinatorHandler) SetTourStopper(port TourStopper) {
	h.tourStopper = port
}

// SetAbsorptionReclaimer wires the sp-m3122 part-3 dead-container absorption reclaim (reused
// verbatim). Optional-injection: without it the reclaim is skipped (nil-safe).
func (h *LongHaulArbFleetCoordinatorHandler) SetAbsorptionReclaimer(port DeadContainerAbsorptionReclaimer) {
	h.absorptionReclaimer = port
}

// Handle runs the reconcile loop until the context is cancelled.
func (h *LongHaulArbFleetCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	logger := common.LoggerFromContext(ctx)

	cmd, ok := request.(*LongHaulArbFleetCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	tick := time.Duration(cmd.TickIntervalSecs) * time.Second
	if tick <= 0 {
		tick = defaultLongHaulTickSeconds * time.Second
	}

	result := &LongHaulArbFleetCoordinatorResponse{Errors: []string{}}
	logger.Log("INFO", fmt.Sprintf("Long-haul arb coordinator starting (tick %s)", tick), map[string]interface{}{
		"action":       "longhaul_coordinator_start",
		"container_id": cmd.ContainerID,
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
			logger.Log("ERROR", fmt.Sprintf("Long-haul reconcile failed: %v", err), nil)
		}

		select {
		case <-time.After(tick):
		case <-ctx.Done():
			return result, ctx.Err()
		}
	}
}

// reconcileOnce is one reconcile pass over the long-haul fleet — the unit the tests drive
// directly (the Handle loop just calls it on a timer). It snapshots the whole fleet once,
// keeps only the idle long-haul-tagged hulls (partitionLongHaulFleet), and launches a
// worker for each. A per-hull launch failure is logged and skipped (the rest of the fleet
// still gets serviced, RULINGS #1); only a fleet-listing / unwired-launcher failure aborts
// the pass.
func (h *LongHaulArbFleetCoordinatorHandler) reconcileOnce(ctx context.Context, cmd *LongHaulArbFleetCoordinatorCommand) (int, error) {
	logger := common.LoggerFromContext(ctx)

	// Fail closed, don't panic, if the launcher was never wired: a reconcile that cannot
	// launch must not silently read as "nothing to do".
	if h.launcher == nil {
		return 0, fmt.Errorf("long-haul arb coordinator: no worker launcher wired (call SetLongHaulLauncher at startup)")
	}

	ships, err := h.shipRepo.FindAllByPlayer(ctx, cmd.PlayerID)
	if err != nil {
		return 0, fmt.Errorf("failed to list ships for long-haul reconcile: %w", err)
	}

	now := h.clock.Now()
	idle, running := partitionLongHaulFleet(ships)

	// sp-m3122 liveness watchdog (SHARED engine): a long-haul hull mid-multi-hop for a long
	// time is the exact hang risk, so kill+relaunch any running worker that has made ZERO
	// progress past the stall threshold. Runs BEFORE the exposure-cap dispatch — a fleet whose
	// ONLY problem is a hung worker has an empty idle bucket, yet that worker must be killed.
	watchdogKilled, watchdogRelaunched := h.relaunchHungHauls(ctx, cmd, running, now, logger)

	// sp-m3122 part 3: promptly reclaim absorption reservations of dead long-haul containers —
	// on restart (this handler's first pass) and after any watchdog kill.
	if !h.startupReclaimDone || watchdogKilled > 0 {
		h.reclaimDeadContainerAbsorption(ctx, cmd, logger)
	}
	h.startupReclaimDone = true

	// Deterministic dispatch order (RULINGS #2): stable across ticks and tests.
	sort.Slice(idle, func(i, j int) bool { return idle[i].ShipSymbol() < idle[j].ShipSymbol() })

	// UNCAPPED CONCURRENCY (sp-bwjyn, Admiral order): launch a worker for EVERY idle tagged
	// long-haul hull each tick. The total-exposure CONCURRENCY ceiling that previously held
	// idle hulls when running >= totalExposureCap/perHaulCap is removed, so a tagged hull
	// never sits idle behind it. Spend is still fail-closed PER BUY inside each worker — the
	// reserve-floor fence (newLongHaulFence → common.ReserveFloorGate, the 200k cushion over
	// the immutable 50k floor, RULINGS #4/#5) and the per-haul cap (threaded via
	// buildLongHaulLaunchSpec) are untouched. launched already counts any watchdog relaunch.
	launched := watchdogRelaunched

	for _, ship := range idle {
		spec := buildLongHaulLaunchSpec(cmd, ship.ShipSymbol())
		containerID, lerr := h.launcher.LaunchLongHaul(ctx, spec)
		if lerr != nil {
			// A single hull's launch failure (claimed between the snapshot and the launch,
			// or the daemon refused it) must not abort the pass — the rest of the fleet
			// still gets serviced, and this hull retries next tick.
			logger.Log("WARNING", fmt.Sprintf("Failed to launch long-haul worker for hull %s: %v", ship.ShipSymbol(), lerr), map[string]interface{}{
				"action":      "longhaul_launch_failed",
				"ship_symbol": ship.ShipSymbol(),
			})
			continue
		}
		launched++
		logger.Log("INFO", fmt.Sprintf("Launched long-haul worker for hull %s (container %s)", ship.ShipSymbol(), containerID), map[string]interface{}{
			"action":       "longhaul_launch",
			"ship_symbol":  ship.ShipSymbol(),
			"container_id": containerID,
		})
	}

	return launched, nil
}

// buildLongHaulLaunchSpec assembles the launch request for one hull, shared by the idle
// dispatch and the sp-m3122 watchdog relaunch — one spec shape, no drift (mirrors the trade
// coordinator's buildTourLaunchSpec).
func buildLongHaulLaunchSpec(cmd *LongHaulArbFleetCoordinatorCommand, shipSymbol string) LongHaulLaunchSpec {
	return LongHaulLaunchSpec{
		ShipSymbol:       shipSymbol,
		AgentSymbol:      cmd.AgentSymbol,
		PlayerID:         cmd.PlayerID.Value(),
		Iterations:       longHaulIterationsContinuous,
		PerHaulCap:       cmd.resolvedPerHaulCap(),
		TotalExposureCap: cmd.resolvedTotalExposureCap(),
	}
}

// relaunchHungHauls binds the SHARED watchdog engine (relaunchHungContainers) to the
// long-haul fleet: the same TourLivenessPort / TourStopper daemon implementations the trade
// coordinator uses, the same fail-closed progress read and IN_TRANSIT skip, but a long-haul
// relaunch closure. This is the design's "ONE watchdog serves trade tours AND long-haul".
func (h *LongHaulArbFleetCoordinatorHandler) relaunchHungHauls(
	ctx context.Context,
	cmd *LongHaulArbFleetCoordinatorCommand,
	running []*navigation.Ship,
	now time.Time,
	logger common.ContainerLogger,
) (killed, relaunched int) {
	return relaunchHungContainers(ctx, running, now, logger, watchdogConfig{
		liveness:   h.tourLiveness,
		stopper:    h.tourStopper,
		playerID:   cmd.PlayerID,
		threshold:  cmd.watchdogStallThreshold(),
		humanLabel: "Long-haul",
		action:     "longhaul",
		relaunch: func(ctx context.Context, shipSymbol string) (string, error) {
			return h.launcher.LaunchLongHaul(ctx, buildLongHaulLaunchSpec(cmd, shipSymbol))
		},
	})
}

// reclaimDeadContainerAbsorption promptly releases absorption reservations held by long-haul
// containers that no longer exist (sp-m3122 part 3) — reused verbatim (keyed on container
// liveness, not fleet). Nil-safe; best-effort (a reclaim error is logged, never aborts the pass).
func (h *LongHaulArbFleetCoordinatorHandler) reclaimDeadContainerAbsorption(ctx context.Context, cmd *LongHaulArbFleetCoordinatorCommand, logger common.ContainerLogger) {
	if h.absorptionReclaimer == nil {
		return
	}
	reclaimed, err := h.absorptionReclaimer.ReclaimDeadContainerAbsorption(ctx, cmd.PlayerID)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Long-haul: dead-container absorption reclaim failed: %v", err), map[string]interface{}{
			"action": "longhaul_absorption_reclaim_failed",
		})
		return
	}
	if reclaimed > 0 {
		logger.Log("INFO", fmt.Sprintf("Long-haul: reclaimed %d absorption reservation(s) held by non-existent containers (sp-m3122)", reclaimed), map[string]interface{}{
			"action":    "longhaul_absorption_reclaimed",
			"reclaimed": reclaimed,
		})
	}
}

// partitionLongHaulFleet splits a fleet snapshot into the long-haul-tagged hulls that are
// idle and eligible for a worker launch, and those already running a worker — mirroring
// partitionTradeFleet (the coordinator whose watchdog this engine shares). The isolation
// and #7 guards are honored here:
//   - Untagged / other-fleet: DedicatedFleet() != "long-haul" — not ours, skipped.
//   - Command frigate: domainContract.IsCommandHull — NEVER a long-haul hull (RULINGS #7),
//     excluded from BOTH buckets even if the operator mis-tagged it.
//   - Captain-reserved: an active captain assignment — respected, never touched.
//   - Assigned: a live worker claim => returned in running (the watchdog's domain).
//   - In-transit idle: a rare release→next-claim gap while flying — not a launch candidate.
//   - Zero-cargo: a probe/satellite mis-pinned here can never haul — excluded from idle
//     dispatch (mirrors contract.RequireCargoCapacity), so it is never claim-spawned-crashed.
func partitionLongHaulFleet(ships []*navigation.Ship) (idle []*navigation.Ship, running []*navigation.Ship) {
	for _, ship := range ships {
		if ship.DedicatedFleet() != longHaulFleet {
			continue // not a long-haul hull (or untagged — the operator's per-hull opt-out)
		}
		if domainContract.IsCommandHull(ship) {
			continue // RULINGS #7: the command frigate is NEVER a long-haul hull
		}
		if ship.IsReservedByCaptain() {
			continue // captain reserved: respect it, never touch and never count it
		}
		if ship.IsAssigned() {
			running = append(running, ship) // live worker claim => an episode is running
			continue
		}
		if ship.IsInTransit() {
			continue // idle but still flying — not a launch candidate this tick
		}
		if ship.CargoCapacity() == 0 {
			continue // a 0-cargo probe cannot haul — never dispatch it (RequireCargoCapacity)
		}
		idle = append(idle, ship) // parked and dispatchable: launch a worker
	}
	return idle, running
}
