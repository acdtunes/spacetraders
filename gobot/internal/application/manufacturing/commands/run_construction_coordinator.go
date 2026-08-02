package commands

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/contract"
	mfgServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	mfgTypes "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// Type aliases matching the factory coordinator's pattern (the container command factory
// builds the command; the handler consumes it).
type RunConstructionCoordinatorCommand = mfgTypes.RunConstructionCoordinatorCommand
type RunConstructionCoordinatorResponse = mfgTypes.RunConstructionCoordinatorResponse

const (
	// constructionDrainTickInterval is the delay between drain ticks for a standing
	// (MaxIterations<=0) run. Sits in the same 30-60s band as the factory coordinator's
	// discovery cadence; overridable per-launch via TickSeconds.
	constructionDrainTickInterval = 30 * time.Second

	noWorkNoReadyConstruction = "no_ready_construction_tasks"
	noWorkNoIdleHauler        = "no_idle_hauler_in_system"
	// noWorkWorkersSaturated: every slot under max_workers is held by a supply still hauling, so this
	// tick starts nothing. It still activated, swept and reconciled — a saturated drain is working,
	// not stalled, and must not read as either.
	noWorkWorkersSaturated = "supply_workers_saturated"

	// constructionOperationContext labels the sourcing/delivery transactions for attribution.
	constructionOperationContext = "construction_supply"

	// defaultConstructionWorkerCap bounds concurrent supplyTask workers when a tick's pipeline
	// exposes no positive max_workers. This is defensive only — readyConstructionTasks yields
	// tasks solely from EXECUTING pipelines, which always carry a max_workers — and mirrors the
	// domain's default construction max_workers so an unset pipeline drains at the width the
	// planner would have chosen.
	defaultConstructionWorkerCap = 5

	// constructionSupplyTaskDefaultTimeout bounds a single supplyTask so one wedged task can never
	// hold its worker — and the slot that worker occupies under max_workers — forever. An unbounded
	// downstream wait (a hull already at the gate whose supply 4219s, a navigation/dock that never
	// returns, a bad task state) would otherwise permanently retire a slot, and enough of them starve
	// the drain of dispatch capacity while it still reports RUNNING. Must stay
	// generous enough never to cut a legitimate in-system source+deliver round trip (construction legs
	// are single-system): a multi-hop light-hauler round trip can exceed 10m, abandoning healthy long
	// hauls at the finish line and forcing the retry onto a fresh empty hull while the laden one
	// strands out of the pool — 30m clears that haul while still converting a genuine indefinite hang
	// into a logged, retried tick. Was operator-tunable per-launch; sp-sxyx6 retired the knob —
	// this const is now the sole value.
	constructionSupplyTaskDefaultTimeout = 30 * time.Minute

	// defaultConstructionLotUnits is the fallback per-lot hull-load used to size the fan-out when an
	// idle hull exposes no cargo capacity — a light hauler's hold. The fan-out prefers each idle
	// hull's ACTUAL capacity (representativeLotUnits); this only backstops a capacity-less hull so
	// ceil(remaining/lotUnits) never divides by zero.
	defaultConstructionLotUnits = 40

	// constructionSupplyTaskMaxTimeout is the ABSOLUTE ceiling on the depth-scaled per-task deadline
	// (scaledSupplyTaskTimeout): depth scaling gives a deep fabricate chain the headroom a flat deadline
	// cannot, and this ceiling still bounds a genuine hang so a mis-set depth/override can never hold a
	// tick open indefinitely. 30m*3 = 90m at the fleet default sits well under it.
	constructionSupplyTaskMaxTimeout = 2 * time.Hour

	// constructionDefaultChainDepth is the depth an unset (<=0) pipeline SupplyChainDepth resolves to
	// for timeout scaling. Must stay in lockstep with the fabricate resolver's own default
	// (services.defaultFabricateMaxDepth = 3) so the timeout scales by the SAME work-depth the resolver
	// fabricates down to before it market-buys the deeper inputs.
	constructionDefaultChainDepth = 3

	// constructionWorkerReapGrace is how long PAST its own task deadline a supply worker's registration
	// survives before the drain gives the worker up for dead and reclaims its hull. The deadline bounds
	// the TASK; nothing bounds the goroutine, which can stop running without ever reaching its
	// deregistration — wedged in a claim or release that ignores ctx, or unwound past it — and an
	// unbounded registration retires that hull permanently.
	//
	// It is a grace on top of the worker's own deadline, never a deadline of its own, so a legitimately
	// long haul is untouched: a 60-minute haul under a 90-minute deadline is nowhere near it. Only a
	// worker that has already blown its own contract, and then failed to clean up (a detached-ctx
	// release is itself bounded at 15s), is ever reaped. Generous on purpose — reaping early risks a
	// second hull-load bought against a bill the first is about to meet, reaping late costs one hull's
	// capacity for a while.
	constructionWorkerReapGrace = 10 * time.Minute

	// constructionDetachedPersistTimeout bounds the detached write persistCleanupCtx hands back
	// when the caller's ctx is already cancelled — the 15s constructionWorkerReapGrace relies on.
	constructionDetachedPersistTimeout = 15 * time.Second

	// constructionRefillCreateTimeout bounds one attempt at creating the next delivery task on the
	// fresh context enqueueReplenishmentIfNeeded must use.
	constructionRefillCreateTimeout = 15 * time.Second
)

// ConstructionProducer is the narrow slice of the shared ProductionExecutor the drain
// delegates ALL sourcing and delivery to — so the drain adds NO duplicate sourcing/nav
// logic. *services.ProductionExecutor satisfies it (ProduceGood sources the material into
// the hauler; DeliverToConstructionSite flies it to the site and supplies it).
type ConstructionProducer interface {
	ProduceGood(ctx context.Context, ship *navigation.Ship, node *goods.SupplyChainNode, systemSymbol string, playerID int, opContext *shared.OperationContext, inputsOnly bool) (*mfgServices.ProductionResult, error)
	DeliverToConstructionSite(ctx context.Context, shipSymbol, good, site string, playerID shared.PlayerID) (int, error)
}

// ConstructionActivator is the surviving activator wired each tick: it promotes PENDING
// DELIVER_TO_CONSTRUCTION tasks (deps complete, re-sourced) to READY. *services.SupplyMonitor
// satisfies it via ActivateConstructionTasks — the drain adds NO new activation logic.
type ConstructionActivator interface {
	ActivateConstructionTasks(ctx context.Context) int
}

// ConstructionTreeResolver builds the scarcity-gated supply-chain dependency tree for a FABRICATE
// material, so the drain PRODUCES a scarce intermediate that has a factory (recursing its
// sub-chain) instead of a flat one-level "fabricate root, buy every immediate input" node.
// *services.SupplyChainResolver satisfies it via BuildDependencyTree. It is an OPTIONAL collaborator
// wired by SetTreeResolver: left unset (nil), the drain falls back to the one-level node, so a
// coordinator built without it is byte-identical (and the buy-final path never consults it at all).
type ConstructionTreeResolver interface {
	BuildDependencyTree(ctx context.Context, targetGood, systemSymbol string, playerID int) (*goods.SupplyChainNode, error)
}

// ConstructionNavigator flies a claimed hull to a waypoint and confirms it docked — the
// warehouse leg of warehouse-first sourcing. *services.ProductionExecutor satisfies it via
// NavigateAndDock. OPTIONAL: wired alongside the inventory finder by SetInventorySource; left
// nil, warehouse-first is disabled and the drain buys at market as today (byte-identical).
type ConstructionNavigator interface {
	NavigateAndDock(ctx context.Context, shipSymbol, destination string, playerID shared.PlayerID) (*navigation.Ship, error)
}

// RunConstructionCoordinatorHandler is the thin construction-supply drain. Each
// tick it: runs the activator, polls READY DELIVER_TO_CONSTRUCTION tasks from EXECUTING
// pipelines, claims idle in-system haulers under the shared "manufacturing" identity, then
// delegates source+deliver to the ProductionExecutor and records pipeline progress. An
// unsourceable material is PARKED for resupply (never failed). It is queue-driven (not
// tree-driven) and holds no PERSISTENT state — a restart re-polls persistence and resumes; the one
// thing it carries between ticks is the live worker registry, which dies with the process the
// workers it tracks do.
type RunConstructionCoordinatorHandler struct {
	taskRepo     manufacturing.TaskRepository
	pipelineRepo manufacturing.PipelineRepository
	shipRepo     navigation.ShipRepository
	producer     ConstructionProducer
	// newActivator builds the surviving activator for a specific player each tick. It is a
	// per-player factory (not a fixed instance) because SupplyMonitor bakes in the playerID at
	// construction, whereas this handler is registered once and serves the command's PlayerID —
	// the same player-agnostic contract ProduceGood/ClaimShip follow. nil disables activation.
	newActivator func(playerID int) ConstructionActivator
	clock        shared.Clock
	// resolver builds the scarcity-gated dependency tree for a FABRICATE material. Optional
	// (wired by SetTreeResolver); nil falls back to the one-level fabricate node.
	resolver ConstructionTreeResolver
	// recordMu serializes the pipeline delivery read-modify-write (recordDelivery) across the
	// concurrent supplyTask workers: two workers supplying the SAME pipeline must not
	// both load-add-store its material counters and lose an update. It guards an in-tick section
	// only, not any cross-tick/persisted state.
	recordMu sync.Mutex
	// taskTimeout bounds a single supplyTask (claim→source→deliver→record) so one wedged task can
	// never silently freeze the whole drain goroutine. Defaulted in the constructor to
	// constructionSupplyTaskDefaultTimeout; in-package tests set a tiny bound directly to keep the
	// timeout test fast.
	taskTimeout time.Duration
	// reapGrace is how long past its task deadline a worker's REGISTRATION survives before the drain
	// reclaims its hull. Defaulted in the constructor to constructionWorkerReapGrace; in-package tests
	// set a tiny bound directly to keep the liveness test fast.
	reapGrace time.Duration
	warehouse warehouseSourcing
	// siteSource reads the LIVE construction site so each tick can reconcile the pipeline's delivered
	// counters against the server. Wired by SetConstructionSiteSource; left nil the drain
	// logs that reconciliation is OFF every tick rather than silently trusting a cache that only ever
	// drifts one way.
	siteSource manufacturing.ConstructionSiteRepository
	// supplies is the live worker registry. Supply workers outlive the tick that dispatched them, so
	// the worker budget, hull ownership and completion counts live here rather than inside a tick.
	supplies supplyWorkers
}

// NewRunConstructionCoordinatorHandler builds the drain. clock defaults to a RealClock when nil.
func NewRunConstructionCoordinatorHandler(
	taskRepo manufacturing.TaskRepository,
	pipelineRepo manufacturing.PipelineRepository,
	shipRepo navigation.ShipRepository,
	producer ConstructionProducer,
	newActivator func(playerID int) ConstructionActivator,
	clock shared.Clock,
) *RunConstructionCoordinatorHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RunConstructionCoordinatorHandler{
		taskRepo:     taskRepo,
		pipelineRepo: pipelineRepo,
		shipRepo:     shipRepo,
		producer:     producer,
		newActivator: newActivator,
		clock:        clock,
		taskTimeout:  constructionSupplyTaskDefaultTimeout,
		reapGrace:    constructionWorkerReapGrace,
	}
}

// SetConstructionSiteSource wires the LIVE construction-site read used to reconcile the pipeline's
// delivered counters each tick. It is the SAME shared ConstructionSiteRepository the
// planner reads site requirements through — not a second fetch path.
func (h *RunConstructionCoordinatorHandler) SetConstructionSiteSource(source manufacturing.ConstructionSiteRepository) {
	h.siteSource = source
}

// SetTreeResolver wires the scarcity-gated supply-chain resolver so the drain PRODUCES a
// FABRICATE material's scarce intermediates recursively instead of the flat one-level node. Optional
// — the daemon injects the shared goodsResolver singleton; left unset the drain uses the one-level
// fallback. A setter (not a constructor arg) keeps the existing coordinator
// tests, which never build the resolver tree, unchanged (nil → fallback → byte-identical).
func (h *RunConstructionCoordinatorHandler) SetTreeResolver(resolver ConstructionTreeResolver) {
	h.resolver = resolver
}

// SetInventorySource wires warehouse-first sourcing: the SHARED contract
// StorageInventoryFinder, the shared storage coordinator it withdraws through, the API client for
// the ship-to-ship transfer, and the navigator that flies the hull to the warehouse. A nil in ANY
// argument disables warehouse-first, so the drain buys at market as today (byte-identical). A setter
// (not a constructor arg) keeps every existing coordinator test — none of which wires warehouse
// inventory — unchanged; the daemon injects the same finder the contract path uses.
func (h *RunConstructionCoordinatorHandler) SetInventorySource(
	finder contract.InventorySourceFinder,
	coordinator storage.StorageCoordinator,
	apiClient domainPorts.APIClient,
	navigator ConstructionNavigator,
) {
	h.warehouse = warehouseSourcing{finder: finder, storage: coordinator, api: apiClient, navigator: navigator}
}

// Handle runs the standing drain loop until the container is cancelled (or MaxIterations is reached
// for a bounded run), then JOINS the supplies still in flight before reporting. Workers outlive
// their tick, so without that join a stopped coordinator would report done while a worker was still
// writing. A cancelled worker unwinds at once, so the join waits on its cleanup, never on its haul.
func (h *RunConstructionCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*RunConstructionCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type for construction coordinator")
	}

	last, err := h.drainLoop(ctx, cmd)
	if completed := h.awaitSupplies(cmd.ContainerID); last != nil {
		last.TasksDrained += completed
	}
	return last, err
}

// awaitSupplies joins this container's supply workers and reports how many of them delivered.
func (h *RunConstructionCoordinatorHandler) awaitSupplies(containerID string) int {
	h.supplies.wait(containerID)
	return h.supplies.harvest(containerID)
}

// drainLoop drains each tick on a timer. The per-tick delay is raced against cancellation so a stop
// is prompt. reconcile lives in drainOnce (the unit tests drive).
func (h *RunConstructionCoordinatorHandler) drainLoop(ctx context.Context, cmd *RunConstructionCoordinatorCommand) (*RunConstructionCoordinatorResponse, error) {
	logger := common.LoggerFromContext(ctx)

	tick := constructionDrainTickInterval
	if cmd.TickSeconds > 0 {
		tick = time.Duration(cmd.TickSeconds) * time.Second
	}

	iterations := 0
	var last *RunConstructionCoordinatorResponse
	for {
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		default:
		}

		resp, err := h.drainOnce(ctx, cmd)
		if err != nil {
			logger.Log("ERROR", fmt.Sprintf("Construction drain tick failed: %v", err), nil)
		} else {
			last = resp
		}

		iterations++
		if cmd.MaxIterations > 0 && iterations >= cmd.MaxIterations {
			return last, nil
		}

		select {
		case <-time.After(tick):
		case <-ctx.Done():
			return last, ctx.Err()
		}
	}
}

// drainOnce is one reconcile tick: activate, poll ready construction tasks from EXECUTING
// pipelines, and hand each to a supply worker with a claimed idle hauler.
//
// The tick DISPATCHES its workers and returns; it does not join them. A construction haul runs for
// tens of minutes under a deadline measured in hours, and joining it held the whole loop for that
// long: the next activation pass — which is also the FAILED-task retry sweep — could not start, so
// a recoverable leg sat dead and a hull bought mid-haul stayed unassigned until the slowest haul
// finished. Activation, the sweep, site reconciliation and hull discovery therefore run on the
// tick's cadence, and the hauls run underneath them.
func (h *RunConstructionCoordinatorHandler) drainOnce(ctx context.Context, cmd *RunConstructionCoordinatorCommand) (*RunConstructionCoordinatorResponse, error) {
	logger := common.LoggerFromContext(ctx)

	// Bound the worker registry before anything reads it. A worker that stopped running without
	// deregistering holds its hull, and its slot under max_workers, until the process dies — the drain
	// would go on reporting RUNNING on capacity it no longer has. Ahead of every path below, including
	// the ones that return early.
	h.reapAbandonedSupplies(ctx, cmd)

	h.activateConstructionTasks(ctx, cmd.PlayerID)

	tasks, err := h.readyConstructionTasks(ctx, cmd.PlayerID)
	if err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return h.tickReport(cmd.ContainerID, noWorkNoReadyConstruction), nil
	}

	// Re-read the LIVE construction sites and correct the pipelines' delivered counters BEFORE any
	// bill is consulted, so this tick sizes its buys against the server's outstanding requirement
	// rather than a cache that only ever drifts downward. Ahead of every path that can consult a
	// bill, including the ones that dispatch nothing. An EARLIER tick's worker may be mid-delivery
	// while it applies; the correction is raise-only and runs under recordMu, so a delivery recorded
	// in that gap survives.
	h.reconcilePipelinesFromSite(ctx, tasks, cmd.PlayerID)

	// The launch system if given, else the first ready task's construction site (gate-construction
	// tasks share the home gate's system). Construction legs are in-system, so restricting hauler
	// discovery this way makes an out-of-system hull UNSELECTABLE rather than claimed-then-failed.
	systemSymbol := cmd.SystemSymbol
	if systemSymbol == "" {
		systemSymbol = shared.ExtractSystemSymbol(tasks[0].ConstructionSite())
	}

	playerID := shared.MustNewPlayerID(cmd.PlayerID)
	// Discover the drain's OWN dedicated fleet FIRST, then supplement with opportunistic idle
	// hulls (see selectHaulers).
	idleShips, err := h.selectHaulers(ctx, cmd, playerID, systemSymbol)
	if err != nil {
		return nil, err
	}
	idleShips = h.dispatchableHaulers(idleShips)
	if len(idleShips) == 0 {
		return h.tickReport(cmd.ContainerID, noWorkNoIdleHauler), nil
	}

	// Sweep back any hull this container still holds with no worker behind it — a claim orphaned by
	// a release that failed to write. A hull a worker is still hauling with is skipped; its own
	// worker releases it (ship claims also auto-release on restart via ReleaseAllActive).
	defer h.releaseClaims(ctx, cmd.ContainerID, playerID)

	// max_workers bounds the supplies IN FLIGHT, not the ones one tick starts: a worker outlives its
	// tick, so this tick's budget is what the still-running hauls leave over. A cap lowered live
	// simply starves new dispatch until enough finish.
	workerCap := h.resolveWorkerCap(ctx, tasks)
	slots := workerCap - h.supplies.inFlight(cmd.ContainerID)
	if slots <= 0 {
		logger.Log("INFO", fmt.Sprintf("Construction drain: every supply slot is in flight (%d/%d) — activated and reconciled, dispatching nothing this tick", workerCap, workerCap), map[string]interface{}{
			"worker_cap": workerCap, "ready_tasks": len(tasks),
		})
		return h.tickReport(cmd.ContainerID, noWorkWorkersSaturated), nil
	}

	// Fan the ready materials into lot-tasks and pair each with an idle hauler. The pipeline stages
	// exactly ONE task per material, so pairing 1:1 would cap throughput at #materials regardless of
	// the worker cap. planDispatchLots fans each material into ceil(remaining/hull-load) lot-tasks —
	// bounded by the material's own remaining requirement, so concurrent lots never buy past what the
	// gate needs — and by the slots this tick has free. The whole pool is offered to the planner even
	// when it may only start a few of them, so a hull already laden with a material is still paired
	// with THAT material rather than trimmed off the end of the pool and its load re-bought.
	lots := h.planDispatchLots(ctx, tasks, idleShips, slots)
	if len(lots) == 0 {
		// Every ready material's bill is already met (a met/racing-replenishment leftover) — nothing to
		// buy without over-supplying. Report a clean no-drain tick rather than dispatch an empty haul.
		return h.tickReport(cmd.ContainerID, ""), nil
	}

	// A drain tick always announces how much work it is about to hand off, so a stall is visible
	// against this line.
	logger.Log("INFO", fmt.Sprintf("Construction drain: dispatching %d lot-task(s) across %d idle hauler(s) for %d ready material-task(s) (worker cap %d, %d slot(s) free)", len(lots), len(idleShips), len(tasks), workerCap, slots), map[string]interface{}{
		"lot_tasks": len(lots), "ready_tasks": len(tasks), "idle_haulers": len(idleShips), "worker_cap": workerCap, "free_slots": slots,
	})
	// Report before dispatching, so a tick never counts a supply it just started: a delivery belongs
	// to the tick that observes it finish.
	report := h.tickReport(cmd.ContainerID, "")
	h.dispatchSupplyWorkers(ctx, cmd, systemSymbol, lots, playerID)
	return report, nil
}

// activateConstructionTasks promotes PENDING construction tasks whose deps are complete to READY
// and re-sources deferred ones. The enter/exit + count logging makes a stuck activation visible in
// the log stream rather than an undiagnosable silent block.
func (h *RunConstructionCoordinatorHandler) activateConstructionTasks(ctx context.Context, playerID int) {
	if h.newActivator == nil {
		return
	}
	activator := h.newActivator(playerID)
	if activator == nil {
		return
	}
	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", "Construction drain: activating construction tasks", nil)
	promoted := activator.ActivateConstructionTasks(ctx)
	logger.Log("INFO", fmt.Sprintf("Construction drain: activation done (%d task(s) promoted to READY)", promoted), map[string]interface{}{"promoted": promoted})
}

// dispatchSupplyWorkers starts one worker per lot. The hull and its buy budget are registered
// BEFORE the worker starts, so this tick's own claim sweep and every later tick already read the
// hull as taken and the units as spoken for.
func (h *RunConstructionCoordinatorHandler) dispatchSupplyWorkers(
	ctx context.Context,
	cmd *RunConstructionCoordinatorCommand,
	systemSymbol string,
	lots []constructionLot,
	playerID shared.PlayerID,
) {
	logger := common.LoggerFromContext(ctx)
	for i := range lots {
		lot := lots[i]
		// Reservation and deadline are each resolved ONCE, for both the registration and the
		// release/expiry that must balance it: the registry can then only give up on a worker that
		// has already blown its own task deadline.
		reserved := lot.buyReservation()
		timeout := h.scaledSupplyTaskTimeout(ctx, lot.task)
		seq, admitted := h.supplies.admit(lot.ship.ShipSymbol(), cmd.ContainerID, materialKey(lot.task), reserved, h.clock.Now().Add(timeout+h.registrationGrace()))
		if !admitted {
			logger.Log("WARNING", fmt.Sprintf("Skipping hauler %s for construction: another supply worker still holds it", lot.ship.ShipSymbol()), nil)
			continue
		}
		go h.runSupplyWorker(ctx, cmd, systemSymbol, lot, playerID, seq, timeout)
	}
}

// registrationGrace is how long past its task deadline a worker's registration survives.
func (h *RunConstructionCoordinatorHandler) registrationGrace() time.Duration {
	if h.reapGrace > 0 {
		return h.reapGrace
	}
	return constructionWorkerReapGrace
}

// reapAbandonedSupplies takes back the hulls of workers the drain has stopped believing in — past
// their deadline, past the grace, and still registered — and returns each to the idle pool through
// the same CAS-guarded release its own worker would have used. A reaped worker that comes back finds
// it no longer owns the hull and stands down without spending.
func (h *RunConstructionCoordinatorHandler) reapAbandonedSupplies(ctx context.Context, cmd *RunConstructionCoordinatorCommand) {
	expired := h.supplies.reap(h.clock.Now())
	if len(expired) == 0 {
		return
	}
	logger := common.LoggerFromContext(ctx)
	playerID := shared.MustNewPlayerID(cmd.PlayerID)
	for _, hold := range expired {
		logger.Log("ERROR", fmt.Sprintf("Construction drain: supply worker on %s outlived its deadline by more than %s without finishing — reclaiming the hull and its %d-unit buy reservation", hold.hull, h.registrationGrace(), hold.reserved), map[string]interface{}{
			"ship": hold.hull, "container": hold.containerID, "reserved_units": hold.reserved,
		})
		h.releaseClaim(ctx, hold.containerID, hold.hull, playerID)
	}
}

// tickReport is what a tick reports: the supplies that COMPLETED since this container's previous
// tick, plus an optional no-work reason. Workers outlive their dispatching tick, so a delivery is
// counted by the tick that observes it finish — exactly once, and never lost.
func (h *RunConstructionCoordinatorHandler) tickReport(containerID, noWorkReason string) *RunConstructionCoordinatorResponse {
	return &RunConstructionCoordinatorResponse{TasksDrained: h.supplies.harvest(containerID), NoWorkReason: noWorkReason}
}

// runSupplyWorker advances one hull's lot on its OWN goroutine, PAST the tick that dispatched it. It
// owns that hull for its whole life: it claims it, works it, releases it, and deregisters LAST, so
// the tick's claim sweep and every later tick's discovery leave a live worker's hull alone.
func (h *RunConstructionCoordinatorHandler) runSupplyWorker(ctx context.Context, cmd *RunConstructionCoordinatorCommand, systemSymbol string, lot constructionLot, playerID shared.PlayerID, seq uint64, timeout time.Duration) {
	logger := common.LoggerFromContext(ctx)
	hull := lot.ship.ShipSymbol()
	claimed := false
	delivered := false
	// Deregister LAST and on EVERY exit — registered first so it runs after the release below, and
	// runs at all when a panic unwinds past both. A hull whose worker has stopped existing must go
	// back to the pool, not be retired with it.
	defer func() { h.supplies.retire(hull, cmd.ContainerID, seq, delivered) }()
	defer func() {
		// One supply's panic is not the daemon's to die of: contain it here, with the stack, so the
		// drain keeps ticking and the hull comes back on this worker's own cleanup.
		if failure := recover(); failure != nil {
			logger.Log("ERROR", fmt.Sprintf("Construction drain: supply worker on %s panicked and was abandoned: %v\n%s", hull, failure, debug.Stack()), map[string]interface{}{
				"ship": hull, "good": lot.task.Good(), "task": lot.task.ID(),
			})
		}
		// Release a claim this worker is still entitled to drop: its own, or an orphan the reap left
		// behind. A hull the reap has since handed to ANOTHER worker is mid-haul under that worker's
		// claim, and un-claiming it would put a laden hull back in the idle pool.
		if claimed && h.supplies.releasable(hull, seq) {
			h.releaseClaim(ctx, cmd.ContainerID, hull, playerID)
		}
	}()

	// Atomic claim under the drain's dedicated-fleet identity: a hull pinned to ANOTHER fleet, or
	// grabbed since discovery, is rejected at the DB, not clobbered. The operation string equals the
	// preferred fleet tag (h.dedicatedFleet, default "manufacturing" == operationManufacturing) so
	// the drain can claim its OWN dedicated hulls (tag == operation) while a foreign-pinned hull is
	// still rejected.
	if err := h.shipRepo.ClaimShip(ctx, hull, cmd.ContainerID, playerID, h.dedicatedFleet(cmd)); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Skipping hauler %s for construction: claim rejected: %v", hull, err), nil)
		return // lot stays undispatched; the material's task is retried next tick
	}
	claimed = true
	// Nothing above is bounded by the task deadline, so a worker can reach here long after the reap
	// gave it up. Its buy reservation went back to the pool then and a later tick has re-planned
	// without it: stand down rather than spend against a bill that no longer nets this load out.
	if !h.supplies.owns(hull, seq) {
		logger.Log("WARNING", fmt.Sprintf("Standing down the supply of %s via %s: the drain reclaimed the hull while this worker was still claiming it, so its buy is no longer budgeted", lot.task.Good(), hull), map[string]interface{}{
			"ship": hull, "good": lot.task.Good(), "task": lot.task.ID(),
		})
		return
	}
	// supplyTaskBounded: a per-task deadline so a single wedged task can never hold this worker — and
	// the slot it occupies under the cap — forever. Task-level failures are recorded here
	// (fail/defer) and never propagated, so one worker's failure does not disturb its peers.
	delivered = h.supplyTaskBounded(ctx, cmd, systemSymbol, lot, playerID, timeout)
}

// operationManufacturing is the shared "manufacturing" fleet/claim identity: the construction-supply
// drain claims idle in-system haulers under this operation and prefers hulls tagged with it (see
// dedicatedFleet below). The construction drain is its sole consumer.
const operationManufacturing = "manufacturing"

// constructionSourcingNode builds the SupplyChainNode the drain hands to ProduceGood for one
// construction material. Unified gate-fill ALWAYS resolves the full scarcity-gated dependency tree
// via the shared SupplyChainResolver, ignoring the planner's frozen buy-vs-fabricate decision:
// feeding is INHERENT in the tree, so a gate material whose good HAS a source factory is
// fabricated+fed instead of bought cold (a pure-BUY feeds nothing — the bug this closes). The
// resolver itself decides buy-vs-fabricate per node by live supply, so it PRODUCES a scarce
// intermediate that has a factory (recursing its sub-chain to relieve the scarcity) and BUYS an
// abundant one. The tree resolves under the run strategy (smart by default) and is bounded by the
// pipeline's SupplyChainDepth (the depth backstop) + the resolver's cycle guard.
//
// It falls back to the planner's frozen decision ONLY when the resolver is unwired (existing
// coordinator tests) or cannot build the tree (stale/absent market data): a buy-final task
// (FactorySymbol == "") becomes a bare BUY; a fabricate task becomes the one-level fabricate node.
// A fabricate task whose good has no known recipe (should not happen — the planner never fabricates
// a raw good) falls back to a BUY so the engine attempts a market source rather than polling forever
// on a childless fabricate.
func (h *RunConstructionCoordinatorHandler) constructionSourcingNode(ctx context.Context, task *manufacturing.ManufacturingTask, systemSymbol string, playerID int) *goods.SupplyChainNode {
	if tree := h.resolveFabricationTree(ctx, task, systemSymbol, playerID); tree != nil {
		return tree
	}
	if task.FactorySymbol() == "" {
		return &goods.SupplyChainNode{Good: task.Good(), AcquisitionMethod: goods.AcquisitionBuy}
	}
	// Fallback: the original one-level fabricate node.
	node := goods.NewSupplyChainNode(task.Good(), goods.AcquisitionFabricate)
	for _, input := range goods.GetRequiredInputs(task.Good()) {
		node.AddChild(goods.NewSupplyChainNode(input, goods.AcquisitionBuy))
	}
	if node.IsLeaf() {
		return &goods.SupplyChainNode{Good: task.Good(), AcquisitionMethod: goods.AcquisitionBuy}
	}
	return node
}

// resolveFabricationTree builds the scarcity-gated dependency tree for a FABRICATE material via the
// shared resolver. It stamps, on the tree-build ctx only (the buy-vs-fabricate decision is
// baked into the tree at build time — ProduceGood just walks the shape, so produceCtx is untouched):
//   - the per-run PRODUCTION strategy (WithProductionStrategy — smart by default), so a scarce
//     intermediate with a factory fabricates while an abundant one is bought;
//   - the pipeline's SupplyChainDepth as the fabricate depth cap (WithFabricateDepthCap — the safety
//     backstop; a 0/unset pipeline resolves to the depth-3 default);
//   - the pipeline's per-good overrides (WithGoodGatingOverrides), so a single bottleneck material
//     can be pinned buy/fabricate without disturbing the rest.
//
// Returns nil — so constructionSourcingNode falls back to the one-level node — when the resolver is
// unwired or errors (stale/absent market data). The pipeline is read the way remainingBill does
// (FindByID); config fields (depth, overrides) are immutable post-creation, so this read needs no
// lock (matching pipelineExecuting's unlocked read).
func (h *RunConstructionCoordinatorHandler) resolveFabricationTree(ctx context.Context, task *manufacturing.ManufacturingTask, systemSymbol string, playerID int) *goods.SupplyChainNode {
	if h.resolver == nil {
		return nil
	}
	depth := 0
	var overrides manufacturing.GoodGatingOverrides
	if task.PipelineID() != "" {
		if pipeline, err := h.pipelineRepo.FindByID(ctx, task.PipelineID()); err == nil && pipeline != nil {
			depth = pipeline.SupplyChainDepth()
			overrides = pipeline.GoodOverrides()
		}
	}
	buildCtx := mfgServices.WithProductionStrategy(ctx, mfgServices.DefaultProductionStrategy)
	buildCtx = mfgServices.WithFabricateDepthCap(buildCtx, depth, false)
	buildCtx = mfgServices.WithGoodGatingOverrides(buildCtx, overrides)
	tree, err := h.resolver.BuildDependencyTree(buildCtx, task.Good(), systemSymbol, playerID)
	if err != nil || tree == nil {
		return nil
	}
	return tree
}

func (h *RunConstructionCoordinatorHandler) operationContext(cmd *RunConstructionCoordinatorCommand) *shared.OperationContext {
	if cmd.ContainerID == "" {
		return nil
	}
	return shared.NewOperationContext(cmd.ContainerID, constructionOperationContext)
}
